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
	"sync"
	"time"

	"github.com/diphyx/grid/pkg/history"
	"github.com/diphyx/grid/pkg/wire"
)

// Hub serves one config.
type Hub struct {
	config  *Config
	key     wire.Keypair
	log     *slog.Logger
	history *history.Log
	tars    *tars
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
	key, loadError := wire.LoadPrivate(config.Identity)
	if loadError != nil {
		return nil, fmt.Errorf("identity: %w", loadError)
	}
	if log == nil {
		log = slog.Default()
	}
	events, openLogError := history.OpenLog(config.History.Path, config.History.Limit.Bytes, config.History.Expires.D())
	if openLogError != nil {
		return nil, fmt.Errorf("history: %w", openLogError)
	}
	store, newTarsError := newTars(filepath.Join(config.Jobs.Path, "tars"))
	if newTarsError != nil {
		return nil, fmt.Errorf("jobs: %w", newTarsError)
	}
	hub := &Hub{config: config, key: key, log: log, history: events, tars: store, started: time.Now(), jobs: map[string]*entry{}, stop: make(chan struct{})}
	for i, peer := range config.Nodes {
		nodeKey, keyError := wire.LoadKey(peer.Trust)
		if keyError != nil {
			return nil, fmt.Errorf("nodes[%d].trust: %w", i, keyError)
		}
		hub.links = append(hub.links, &link{peer: peer, address: wire.Addr(peer.IP, peer.Port), key: nodeKey, history: events, share: peer.Share})
	}

	return hub, nil
}

// Key is the hub's public key, for the nodes' configs.
func (hub *Hub) Key() wire.Key { return hub.key.Public }

func (hub *Hub) jobsDir() string { return hub.config.Jobs.Path }

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
	hub.sweep()
	go hub.janitor()
	hub.log.Debug("hub up", "key", hub.key.Public.String(), "nodes", len(hub.links), "signal", hub.config.Signal)
	hub.history.Add(history.Entry{Kind: history.KIND_HUB, Event: "started"})

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
	hub.history.Add(history.Entry{Kind: history.KIND_HUB, Event: "stopped"})

	return hub.history.Close()
}

// Packed is one push for SubmitPacked: a tar, or the manifest of a tar the hub has stored.
type Packed struct {
	Manifest string    // the source's key (Manifest); "" = none
	Tar      io.Reader // nil = the stored tar for Manifest
}

// Stored reports whether the hub has a tar for a source with that manifest, so a submit can
// leave the tar out.
func (hub *Hub) Stored(manifest string) bool {
	_, ok := hub.tars.lookup(manifest)

	return ok
}

// Submit runs a job. Push paths are local, relative to base ("" = the current folder); they are
// packed now, so the job does not depend on them later, unless the hub has them already. It
// returns the job's id.
func (hub *Hub) Submit(job Job, base string) (string, error) {
	checkError := job.Check()
	if checkError != nil {
		return "", checkError
	}
	pushes := make([]Packed, len(job.Push))
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
		manifest, manifestError := Manifest(from)
		if manifestError != nil {
			return "", fmt.Errorf("push[%d]: %w", i, manifestError)
		}
		if hub.Stored(manifest) {
			pushes[i] = Packed{Manifest: manifest}

			continue
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
		pushes[i] = Packed{Manifest: manifest, Tar: file}
	}

	return hub.SubmitPacked(job, pushes)
}

// SubmitPacked runs a job whose pushes are already packed, one per job.Push, in order; the
// socket API uses it, since the files are on the caller's side.
func (hub *Hub) SubmitPacked(job Job, pushes []Packed) (string, error) {
	checkError := job.Check()
	if checkError != nil {
		return "", checkError
	}
	if len(pushes) != len(job.Push) {
		return "", fmt.Errorf("push: %d tars for %d entries", len(pushes), len(job.Push))
	}
	sums := make([]string, len(pushes))
	for i, packed := range pushes {
		if packed.Tar == nil {
			sum, ok := hub.tars.lookup(packed.Manifest)
			if !ok {
				return "", fmt.Errorf("push[%d]: no tar stored for manifest %q", i, packed.Manifest)
			}
			sums[i] = sum

			continue
		}
		sum, putError := hub.tars.put(packed.Manifest, packed.Tar)
		if putError != nil {
			return "", fmt.Errorf("push[%d]: %w", i, putError)
		}
		sums[i] = sum
	}
	checkTarsError := job.checkTars(hub.tars, sums)
	if checkTarsError != nil {
		return "", checkTarsError
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	record := Record{ID: hex.EncodeToString(id), Job: job, Pushes: sums, State: STATE_QUEUED, Submitted: time.Now()}
	for _, step := range job.Steps {
		record.Steps = append(record.Steps, StepRun{Cmd: step.Cmd, State: wire.STATE_IDLE})
	}
	dir := filepath.Join(hub.jobsDir(), record.ID)
	created, storeError := hub.store(dir, record)
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
	hub.history.Add(history.Entry{Kind: history.KIND_JOB, Event: "submitted", Subject: record.ID, Message: job.Tag})
	go hub.work(created)

	return record.ID, nil
}

// store writes a new job to disk; its pushes are in the tar store already.
func (hub *Hub) store(dir string, record Record) (*entry, error) {
	created, newError := newEntry(dir, record)
	if newError != nil {
		return nil, newError
	}
	saveError := created.save() // from here on the job exists
	if saveError != nil {
		_ = created.events.Close()

		return nil, saveError
	}

	return created, nil
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

// janitor drops finished jobs past expires, and the tars no job uses.
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
		keep := hub.config.Jobs.Expires.D()
		for _, record := range hub.Jobs() {
			if keep > 0 && record.IsFinished() && time.Since(record.Ended) > keep {
				_ = hub.Drop(record.ID)
			}
		}
		hub.sweep()
	}
}

// sweep removes the tars no job uses.
func (hub *Hub) sweep() {
	used := map[string]bool{}
	for _, record := range hub.Jobs() {
		for _, sum := range record.Pushes {
			used[sum] = true
		}
	}
	hub.tars.sweep(used)
}
