package node

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// Node serves one config.
type Node struct {
	config  *Config
	key     wire.Keypair
	hubKey  wire.Key
	log     *slog.Logger
	history *history
	started time.Time

	mutex sync.Mutex
	jobs  map[string]*Job

	listener  net.Listener
	posts     chan struct{} // wakes the signal loop
	hubOnline atomic.Bool
	closing   atomic.Bool
	running   sync.WaitGroup // one per running cmd; Close waits for them to stop
	stop      chan struct{}
	sessions  sync.Map // *wire.Session → struct{}: open hub connections, closed by Close
}

// New loads keys and jobs left on disk.
func New(config *Config, log *slog.Logger) (*Node, error) {
	key, hubKey, keysError := config.Keys()
	if keysError != nil {
		return nil, keysError
	}
	if log == nil {
		log = slog.Default()
	}
	self := &Node{config: config, key: key, hubKey: hubKey, log: log, started: time.Now(),
		jobs: map[string]*Job{}, posts: make(chan struct{}, 1), stop: make(chan struct{})}
	mkdirAllError := os.MkdirAll(self.jobsDir(), 0o755)
	if mkdirAllError != nil {
		return nil, mkdirAllError
	}
	self.history, keysError = openHistory(filepath.Join(config.Data, "history"), config.History.D())
	if keysError != nil {
		return nil, keysError
	}
	loadError := self.load()
	if loadError != nil {
		return nil, loadError
	}
	return self, nil
}

func (node *Node) jobsDir() string { return filepath.Join(node.config.Data, "jobs") }

// load reopens jobs after a restart; a cmd that was running is failed.
func (node *Node) load() error {
	entries, readDirError := os.ReadDir(node.jobsDir())
	if readDirError != nil {
		return readDirError
	}
	for _, entry := range entries {
		if !entry.IsDir() || wire.CheckJob(entry.Name()) != nil {
			continue
		}
		job, loadJobError := loadJob(filepath.Join(node.jobsDir(), entry.Name()))
		if loadJobError != nil {
			node.log.Error("job not loaded", "job", entry.Name(), "err", loadJobError)
			continue
		}
		job.onState = node.onState
		if job.meta.State.State == wire.STATE_RUNNING {
			job.mutex.Lock()
			node.finish(job, wire.State{State: wire.STATE_FAILED, Cmd: job.meta.State.Cmd, Message: "node restarted"})
			job.mutex.Unlock()
		}
		node.jobs[job.ID()] = job
	}
	if len(node.jobs) > 0 {
		node.log.Debug("jobs reopened", "count", len(node.jobs))
	}

	return nil
}

// Addr is where the node listens.
func (node *Node) Addr() string {
	if node.listener != nil {
		return node.listener.Addr().String()
	}
	return wire.Addr("", node.config.Port)
}

// Listen opens the port; Serve then accepts on it.
func (node *Node) Listen() error {
	listener, listenError := net.Listen("tcp", wire.Addr("", node.config.Port))
	if listenError != nil {
		return listenError
	}
	node.listener = listener

	return nil
}

// Serve runs until Close.
func (node *Node) Serve() error {
	if node.listener == nil {
		listenError := node.Listen()
		if listenError != nil {
			return listenError
		}
	}
	node.log.Debug("node up", "addr", node.Addr(), "key", node.key.Public.String(), "tags", node.config.Tags, "slots", node.config.Slots)
	go node.janitor()
	if node.config.Signal {
		go node.signal()
	}
	peers := wire.Peers{node.config.Hub.IP: node.hubKey}
	for {
		socket, acceptError := node.listener.Accept()
		if acceptError != nil {
			select {
			case <-node.stop:
				return nil
			default:
			}

			return acceptError
		}
		go func() {
			connection, serverError := wire.NewServer(socket, node.key, peers, nil)
			if serverError != nil {
				node.log.Debug("handshake refused", "ip", wire.RemoteIP(socket), "err", serverError)
				socket.Close()

				return
			}
			session := wire.NewSession(connection)
			node.sessions.Store(session, struct{}{})
			_ = session.Run(node.handle)
			node.sessions.Delete(session)
		}()
	}
}

// Close stops the node and waits for its running cmds to stop (SIGTERM, then SIGKILL).
// Jobs stay on disk; those cmds end without a state change, so the next start marks
// them failed: "node restarted".
func (node *Node) Close() error {
	if node.closing.Swap(true) {
		return nil
	}
	close(node.stop)
	node.sessions.Range(func(session, _ any) bool {
		_ = session.(*wire.Session).Close()

		return true
	})
	if node.listener != nil {
		node.listener.Close()
	}
	node.mutex.Lock()
	for _, job := range node.jobs {
		job.mutex.Lock()
		if job.proc != nil {
			job.proc.halt(wire.STATE_STOPPED)
		}
		job.mutex.Unlock()
	}
	node.mutex.Unlock()
	// cmds run in their own process groups: nothing else stops them once the node exits
	node.running.Wait()

	return nil
}

// janitor drops idle jobs past expires and prunes history.
func (node *Node) janitor() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	last := time.Now()
	for {
		select {
		case <-node.stop:
			return
		case <-ticker.C:
		}
		exp := node.config.Expires.D()
		if exp > 0 {
			node.mutex.Lock()
			var old []*Job
			for _, job := range node.jobs {
				job.mutex.Lock()
				if job.idle() && time.Since(job.meta.Used) > exp {
					old = append(old, job)
				}
				job.mutex.Unlock()
			}
			node.mutex.Unlock()
			for _, job := range old {
				node.expireJob(job, exp)
			}
		}
		if time.Since(last) > time.Hour {
			node.history.prune()
			last = time.Now()
		}
	}
}

// Jobs lists open jobs, oldest first.
func (node *Node) Jobs() []Meta {
	node.mutex.Lock()
	defer node.mutex.Unlock()

	out := make([]Meta, 0, len(node.jobs))
	for _, job := range node.jobs {
		job.mutex.Lock()
		out = append(out, job.meta)
		job.mutex.Unlock()
	}
	sort.Slice(out, func(left, right int) bool { return out[left].Opened.Before(out[right].Opened) })

	return out
}

func (node *Node) job(id string) *Job {
	node.mutex.Lock()
	defer node.mutex.Unlock()

	return node.jobs[id]
}
