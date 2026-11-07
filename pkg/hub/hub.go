package hub

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// Hub serves one config.
type Hub struct {
	config  *Config
	key     wire.Keypair
	log     *slog.Logger
	started time.Time
	links   []*link

	mutex sync.Mutex
	jobs  map[string]*entry
	turn  int

	listener net.Listener
	stop     chan struct{}
	closing  bool
}

// New loads keys; Start reopens jobs left on disk and begins work.
func New(config *Config, log *slog.Logger) (*Hub, error) {
	checkError := config.check()
	if checkError != nil {
		return nil, checkError
	}
	key, loadError := wire.LoadPrivate(config.Private)
	if loadError != nil {
		return nil, fmt.Errorf("private: %w", loadError)
	}
	if log == nil {
		log = slog.Default()
	}
	hub := &Hub{config: config, key: key, log: log, started: time.Now(), jobs: map[string]*entry{}, stop: make(chan struct{})}
	for i, peer := range config.Nodes {
		nodeKey, keyError := wire.LoadKey(peer.Key)
		if keyError != nil {
			return nil, fmt.Errorf("nodes[%d].key: %w", i, keyError)
		}
		hub.links = append(hub.links, &link{peer: peer, address: wire.Addr(peer.IP, peer.Port), key: nodeKey})
	}

	return hub, nil
}

// Key is the hub's public key, for the nodes' configs.
func (hub *Hub) Key() wire.Key { return hub.key.Public }

func (hub *Hub) jobsDir() string { return filepath.Join(hub.config.Data, "jobs") }

// Start reopens the jobs on disk, resumes the unfinished ones, and listens for beat and post
// when signal is on. It returns once all of that is running.
func (hub *Hub) Start() error {
	mkdirError := os.MkdirAll(hub.jobsDir(), 0o755)
	if mkdirError != nil {
		return mkdirError
	}
	directories, readError := os.ReadDir(hub.jobsDir())
	if readError != nil {
		return readError
	}
	for _, directory := range directories {
		if !directory.IsDir() || wire.CheckJob(directory.Name()) != nil {
			continue
		}
		dir := filepath.Join(hub.jobsDir(), directory.Name())
		loaded, loadError := loadEntry(dir)
		if errors.Is(loadError, fs.ErrNotExist) {
			_ = os.RemoveAll(dir) // a submit that never finished

			continue
		}
		if loadError != nil {
			hub.log.Error("job not loaded", "job", directory.Name(), "err", loadError)

			continue
		}
		hub.mutex.Lock()
		hub.jobs[loaded.record.ID] = loaded
		hub.mutex.Unlock()
		if loaded.record.IsFinished() {
			close(loaded.done)

			continue
		}
		go hub.work(loaded)
	}
	if hub.config.Signal {
		listenError := hub.listen()
		if listenError != nil {
			return listenError
		}
	}
	go hub.janitor()
	hub.log.Debug("hub up", "key", hub.key.Public.String(), "nodes", len(hub.links), "signal", hub.config.Signal)

	return nil
}

// Close stops the hub. Jobs stay on disk and resume on the next Start; nodes keep running them.
func (hub *Hub) Close() error {
	hub.mutex.Lock()
	if hub.closing {
		hub.mutex.Unlock()

		return nil
	}
	hub.closing = true
	close(hub.stop)
	hub.mutex.Unlock()
	if hub.listener != nil {
		_ = hub.listener.Close()
	}
	for _, each := range hub.links {
		each.mutex.Lock()
		if each.session != nil {
			_ = each.session.Close()
		}
		each.mutex.Unlock()
	}

	return nil
}

// Submit runs a job. Push paths are local, relative to base ("" = the current folder); they are
// packed now, so the job does not depend on them later. It returns the job's id.
func (hub *Hub) Submit(job Job, base string) (string, error) {
	checkError := job.Check()
	if checkError != nil {
		return "", checkError
	}
	pushes := make([]io.Reader, len(job.Push))
	var files []*os.File
	defer func() {
		for _, file := range files {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()

	for i, push := range job.Push {
		from := push.From
		if !filepath.IsAbs(from) && base != "" {
			from = filepath.Join(base, from)
		}
		file, createError := os.CreateTemp("", "grid-push-*.tar")
		if createError != nil {
			return "", createError
		}
		files = append(files, file)
		packError := Pack(file, from)
		if packError != nil {
			return "", fmt.Errorf("push[%d]: %w", i, packError)
		}
		_, seekError := file.Seek(0, io.SeekStart)
		if seekError != nil {
			return "", seekError
		}
		pushes[i] = file
	}

	return hub.SubmitPacked(job, pushes)
}

// SubmitPacked runs a job whose pushes are already tars, one per job.Push, in order;
// the socket API uses it, since the files are on the caller's side.
func (hub *Hub) SubmitPacked(job Job, pushes []io.Reader) (string, error) {
	checkError := job.Check()
	if checkError != nil {
		return "", checkError
	}
	if len(pushes) != len(job.Push) {
		return "", fmt.Errorf("push: %d tars for %d entries", len(pushes), len(job.Push))
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	record := Record{ID: hex.EncodeToString(id), Job: job, State: STATE_QUEUED, Submitted: time.Now()}
	for _, step := range job.Steps {
		record.Steps = append(record.Steps, StepRun{Cmd: step.Cmd, State: wire.STATE_IDLE})
	}
	dir := filepath.Join(hub.jobsDir(), record.ID)
	created, storeError := hub.store(dir, record, pushes)
	if storeError != nil {
		// leave nothing a restart would pick up as a job
		_ = os.RemoveAll(dir)

		return "", storeError
	}
	created.emit(EVENT_JOB, map[string]any{"state": STATE_QUEUED})
	hub.mutex.Lock()
	hub.jobs[record.ID] = created
	hub.mutex.Unlock()
	hub.log.Debug("submit", "job", record.ID, "tag", job.Tag, "steps", len(job.Steps))
	go hub.work(created)

	return record.ID, nil
}

// store writes a new job to disk: its record, then its push tars.
func (hub *Hub) store(dir string, record Record, pushes []io.Reader) (*entry, error) {
	created, newError := newEntry(dir, record)
	if newError != nil {
		return nil, newError
	}
	writeError := created.writePushes(pushes)
	if writeError == nil {
		writeError = created.save() // last: from here on the job exists
	}
	if writeError != nil {
		_ = created.events.Close()

		return nil, writeError
	}

	return created, nil
}

// writePushes saves the push tars, one file per job.Push.
func (entry *entry) writePushes(pushes []io.Reader) error {
	mkdirError := os.MkdirAll(entry.path("push"), 0o755)
	if mkdirError != nil {
		return mkdirError
	}
	for i, push := range pushes {
		file, createError := os.Create(entry.path("push/" + strconv.Itoa(i)))
		if createError != nil {
			return createError
		}
		_, copyError := io.Copy(file, push)
		closeError := file.Close()
		if copyError != nil {
			return copyError
		}
		if closeError != nil {
			return closeError
		}
	}

	return nil
}

// ErrUnknown is the error for a job id the hub does not have.
var ErrUnknown = errors.New("no such job")

func (hub *Hub) entry(id string) (*entry, error) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()

	found := hub.jobs[id]
	if found == nil {
		return nil, ErrUnknown
	}

	return found, nil
}

// Kill stops a job: its running step is killed on the node and the job ends `stopped`.
func (hub *Hub) Kill(id string) error {
	found, entryError := hub.entry(id)
	if entryError != nil {
		return entryError
	}
	found.halt()

	return nil
}

// Drop forgets a job and its files; a running one is killed first.
func (hub *Hub) Drop(id string) error {
	found, entryError := hub.entry(id)
	if entryError != nil {
		return entryError
	}
	found.mutex.Lock()
	found.dropped = true
	found.mutex.Unlock()
	found.halt()
	<-found.done
	hub.mutex.Lock()
	delete(hub.jobs, id)
	hub.mutex.Unlock()
	found.mutex.Lock()
	found.notify()
	found.mutex.Unlock()
	_ = found.events.Close()

	return os.RemoveAll(found.dir)
}

// Job is one job's record.
func (hub *Hub) Job(id string) (Record, error) {
	found, entryError := hub.entry(id)
	if entryError != nil {
		return Record{}, entryError
	}

	return found.snapshot(), nil
}

// Jobs lists every job the hub keeps, oldest first.
func (hub *Hub) Jobs() []Record {
	hub.mutex.Lock()
	entries := make([]*entry, 0, len(hub.jobs))
	for _, each := range hub.jobs {
		entries = append(entries, each)
	}
	hub.mutex.Unlock()
	out := make([]Record, 0, len(entries))
	for _, each := range entries {
		out = append(out, each.snapshot())
	}
	sort.Slice(out, func(left, right int) bool { return out[left].Submitted.Before(out[right].Submitted) })

	return out
}

// Nodes lists every node in the config.
func (hub *Hub) Nodes() []NodeView {
	out := make([]NodeView, len(hub.links))
	for i, each := range hub.links {
		out[i] = each.view()
	}

	return out
}

// Wait blocks until the job is finished or dropped, then returns its record.
func (hub *Hub) Wait(id string) (Record, error) {
	found, entryError := hub.entry(id)
	if entryError != nil {
		return Record{}, entryError
	}
	<-found.done

	return found.snapshot(), nil
}

// Events calls fn for each event after seq after, then for new ones as they come, until the job
// is finished and fn has seen them all, the job is dropped, fn returns false, or stop closes.
func (hub *Hub) Events(id string, after uint64, stop <-chan struct{}, callback func(wire.Event) bool) error {
	found, entryError := hub.entry(id)
	if entryError != nil {
		return entryError
	}
	next := after
	for {
		found.mutex.Lock()
		changed, dropped := found.changed, found.dropped
		finished := found.record.IsFinished()
		found.mutex.Unlock()
		if dropped {
			return nil
		}
		events, readError := found.events.Read(next, 256)
		if readError != nil {
			return readError
		}
		for _, event := range events {
			if !callback(event) {
				return nil
			}
			next = event.Seq
		}
		if len(events) == 256 {
			continue
		}
		if finished && next >= found.events.Len() {
			return nil
		}
		select {
		case <-changed:
			{
			}
		case <-stop:
			{
				return nil
			}
		case <-hub.stop:
			{
				return nil
			}
		}
	}
}

// Files lists the files a job brought back and returns a func that writes them as one tar;
// listing first lets the caller report a failure before it starts writing.
func (hub *Hub) Files(id string) (func(io.Writer) error, error) {
	found, entryError := hub.entry(id)
	if entryError != nil {
		return nil, entryError
	}
	root, openError := os.OpenRoot(found.path("files"))
	if openError != nil {
		return nil, openError
	}
	entries, collectError := wire.Collect(root, ".", 0)
	if collectError != nil {
		_ = root.Close()

		return nil, collectError
	}

	return func(writer io.Writer) error {
		defer func() { _ = root.Close() }()

		return wire.WriteTarFS(writer, root, entries)
	}, nil
}

// janitor drops finished jobs older than history.
func (hub *Hub) janitor() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-hub.stop:
			{
				return
			}
		case <-ticker.C:
			{
			}
		}
		keep := hub.config.History.D()
		if keep == 0 {
			continue
		}
		for _, record := range hub.Jobs() {
			if record.IsFinished() && time.Since(record.Ended) > keep {
				_ = hub.Drop(record.ID)
			}
		}
	}
}

// Pack writes a local file or folder as a push tar: a folder's contents, or one file by its name.
func Pack(writer io.Writer, from string) error {
	info, statError := os.Stat(from)
	if statError != nil {
		return statError
	}
	dir, name := from, "."
	if !info.IsDir() {
		dir, name = filepath.Dir(from), filepath.Base(from)
	}
	root, openError := os.OpenRoot(dir)
	if openError != nil {
		return openError
	}
	defer func() { _ = root.Close() }()

	entries, collectError := wire.Collect(root, name, 0)
	if collectError != nil {
		return collectError
	}

	return wire.WriteTarFS(writer, root, entries)
}
