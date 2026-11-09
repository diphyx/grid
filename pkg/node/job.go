package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// A job on disk:
//
//	jobs/<id>/space/    the space
//	jobs/<id>/events    the event log (wire.EventLog)
//	jobs/<id>/job       Meta, JSON
//	jobs/<id>/push/     partial pushes, one file per path
//	jobs/<id>/own       the file behind $GRID_EVENTS
//
// A share push is a link in the space to the share (share.go), not a copy.

// Meta is what the node keeps about a job.
type Meta struct {
	ID     string     `json:"id"`
	Tag    string     `json:"tag"`
	Opened time.Time  `json:"opened"`
	Used   time.Time  `json:"used"`
	State  wire.State `json:"state"`
	Runs   []Run      `json:"runs,omitempty"`
	Posted uint64     `json:"posted,omitempty"` // with signal: every state event up to this seq reached the hub
	Shares []Linked   `json:"shares,omitempty"` // shares linked into the space
}

// Run is one exec.
type Run struct {
	Cmd     string    `json:"cmd"`
	State   string    `json:"state"`
	Message string    `json:"message,omitempty"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end,omitzero"`
}

type Job struct {
	dir string

	mutex    sync.Mutex
	meta     Meta
	space    *os.Root
	log      *wire.EventLog
	changed  chan struct{}
	proc     *proc
	dropped  bool
	expiring bool   // the janitor is dropping it
	onState  func() // wakes the signal loop
}

// ID is the job id.
func (job *Job) ID() string { return job.meta.ID }

func (job *Job) path(name string) string { return filepath.Join(job.dir, name) }

func newJob(dir string, meta Meta) (*Job, error) {
	mkdirAllError := os.MkdirAll(filepath.Join(dir, "space"), 0o755)
	if mkdirAllError != nil {
		return nil, mkdirAllError
	}
	job := &Job{dir: dir, meta: meta, changed: make(chan struct{})}
	openFilesError := job.openFiles()
	if openFilesError == nil {
		openFilesError = job.save()
	}
	if openFilesError != nil {
		_ = job.remove() // leave nothing for the next start to trip on

		return nil, openFilesError
	}

	return job, nil
}

// loadJob reads a job from disk after a restart.
func loadJob(dir string) (*Job, error) {
	content, readFileError := os.ReadFile(filepath.Join(dir, "job"))
	if readFileError != nil {
		return nil, readFileError
	}
	job := &Job{dir: dir, changed: make(chan struct{})}
	unmarshalError := json.Unmarshal(content, &job.meta)
	if unmarshalError != nil {
		return nil, unmarshalError
	}
	openFilesError := job.openFiles()
	if openFilesError != nil {
		return nil, openFilesError
	}
	return job, nil
}

func (job *Job) openFiles() error {
	space, rootError := os.OpenRoot(job.path("space"))
	if rootError != nil {
		return rootError
	}
	events, logError := wire.OpenEventLog(job.path("events"))
	if logError != nil {
		_ = space.Close()

		return logError
	}
	job.space, job.log = space, events

	return nil
}

// save writes Meta; the caller holds job.mutex, or owns the job.
func (job *Job) save() error {
	content, marshalIndentError := json.MarshalIndent(job.meta, "", "  ")
	if marshalIndentError != nil {
		return marshalIndentError
	}
	temporary := job.path("job.tmp")
	writeFileError := os.WriteFile(temporary, content, 0o644)
	if writeFileError != nil {
		return writeFileError
	}

	return os.Rename(temporary, job.path("job"))
}

// emit appends an event; the caller holds job.mutex. One that would not fit in a frame becomes a
// `log` line saying so.
func (job *Job) emit(typ string, data any) {
	if job.dropped {
		return
	}
	size := len(wire.MustRaw(data))
	if size > wire.MAX_PLAIN-256 {
		typ, data = wire.EVENT_LOG, wire.Log{Stream: "err", Line: fmt.Sprintf("event %s dropped: %d bytes", typ, size)}
	}
	_, appendError := job.log.Append(typ, data)
	if appendError != nil && typ != wire.EVENT_STATE {
		return
	}
	// a state counts even if its event could not be written: the job must not stay running
	if typ == wire.EVENT_STATE {
		job.meta.State = data.(wire.State)
		_ = job.save() // best effort: the event log is the record
		if job.onState != nil {
			job.onState()
		}
	}
	job.notify()
}

func (job *Job) notify() {
	close(job.changed)
	job.changed = make(chan struct{})
}

// events reads up to max events after seq `after`.
func (job *Job) events(after uint64, max int) ([]wire.Event, error) {
	return job.log.Read(after, max)
}

// unposted returns the state events the hub has not acked yet, and the seq read up to.
func (job *Job) unposted() ([]wire.Event, uint64) {
	job.mutex.Lock()
	from := job.meta.Posted
	job.mutex.Unlock()
	var states []wire.Event
	last := from
	for {
		events, readError := job.log.Read(last, 256)
		if readError != nil || len(events) == 0 {
			return states, last
		}
		for _, event := range events {
			if event.Type == wire.EVENT_STATE {
				states = append(states, event)
			}
			last = event.Seq
		}
	}
}

// markPosted records that the hub has every state event up to seq.
func (job *Job) markPosted(seq uint64) {
	job.mutex.Lock()
	defer job.mutex.Unlock()

	if job.dropped || seq <= job.meta.Posted {
		return
	}
	job.meta.Posted = seq
	_ = job.save()
}

// remove deletes the job from disk; the caller holds job.mutex.
func (job *Job) remove() error {
	job.dropped = true
	if job.space != nil {
		_ = job.space.Close()
	}
	if job.log != nil {
		_ = job.log.Close()
	}
	job.notify()

	return os.RemoveAll(job.dir)
}

// idle reports whether the job can expire: nothing running.
func (job *Job) idle() bool { return job.proc == nil }
