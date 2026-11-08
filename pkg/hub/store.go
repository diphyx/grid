package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// A job on the hub's disk:
//
//	jobs/<id>/job       Record, JSON
//	jobs/<id>/events    the job's events, the hub's own seq (wire.EventLog)
//	jobs/<id>/files/    what was pulled

// Hub job states: the node's end states, plus queued while no node has room.
const (
	STATE_QUEUED  = "queued"
	STATE_RUNNING = wire.STATE_RUNNING
	STATE_DONE    = wire.STATE_DONE
	STATE_FAILED  = wire.STATE_FAILED
	STATE_STOPPED = wire.STATE_STOPPED
)

// Hub event types, next to the node's own events it passes on.
const (
	EVENT_JOB  = "job"  // {state, message}: the hub job's state
	EVENT_NOTE = "note" // {text, node?, step?}: what the hub did
)

// Record is what the hub keeps about a job.
type Record struct {
	ID        string    `json:"id"`
	Job       Job       `json:"job"`
	Pushes    []string  `json:"pushes,omitempty"` // the sha256 of each push's tar, in the tar store
	State     string    `json:"state"`
	Message   string    `json:"message,omitempty"`
	Node      string    `json:"node,omitempty"`     // ip:port of the current or last node
	NodeJob   string    `json:"node_job,omitempty"` // the job id on that node
	NodeSeq   uint64    `json:"node_seq,omitempty"` // the last node event passed on
	Attempt   int       `json:"attempt"`
	Pushed    bool      `json:"pushed,omitempty"`
	Step      int       `json:"step"`             // the step running or next
	Execed    bool      `json:"execed,omitempty"` // Step was sent to the node
	Pulled    bool      `json:"pulled,omitempty"`
	Steps     []StepRun `json:"steps"`
	Submitted time.Time `json:"submitted"`
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
}

// StepRun is how one step went, on the last attempt.
type StepRun struct {
	Cmd     string    `json:"cmd"`
	State   string    `json:"state"`
	Message string    `json:"message,omitempty"`
	Start   time.Time `json:"start,omitzero"`
	End     time.Time `json:"end,omitzero"`
}

// IsFinished reports whether the job is over.
func (record *Record) IsFinished() bool {
	return record.State == STATE_DONE || record.State == STATE_FAILED || record.State == STATE_STOPPED
}

// entry is a job in memory, with its files.
type entry struct {
	dir     string
	mutex   sync.Mutex
	record  Record
	events  *wire.EventLog
	changed chan struct{}
	stop    chan struct{} // closed by kill or drop
	stopped bool
	dropped bool
	done    chan struct{} // closed when the worker returns
}

func (entry *entry) path(name string) string { return filepath.Join(entry.dir, name) }

// clearFiles removes what an earlier attempt pulled, whole or in part: a new attempt starts over.
func (entry *entry) clearFiles() error {
	parts, _ := filepath.Glob(entry.path("pull-*.part"))
	for _, part := range parts {
		_ = os.Remove(part)
	}
	removeError := os.RemoveAll(entry.path("files"))
	if removeError != nil {
		return removeError
	}

	return os.MkdirAll(entry.path("files"), 0o755)
}

// newEntry makes a job's folder; the caller saves the record once the job is complete on disk,
// so a folder without one is a submit that never finished.
func newEntry(dir string, record Record) (*entry, error) {
	mkdirError := os.MkdirAll(filepath.Join(dir, "files"), 0o755)
	if mkdirError != nil {
		return nil, mkdirError
	}
	events, openError := wire.OpenEventLog(filepath.Join(dir, "events"))
	if openError != nil {
		return nil, openError
	}

	return &entry{dir: dir, record: record, events: events, changed: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}, nil
}

func loadEntry(dir string) (*entry, error) {
	content, readError := os.ReadFile(filepath.Join(dir, "job"))
	if readError != nil {
		return nil, readError
	}
	var record Record
	unmarshalError := json.Unmarshal(content, &record)
	if unmarshalError != nil {
		return nil, unmarshalError
	}
	events, openError := wire.OpenEventLog(filepath.Join(dir, "events"))
	if openError != nil {
		return nil, openError
	}

	return &entry{dir: dir, record: record, events: events, changed: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}, nil
}

// save writes the record; the caller holds entry.mutex, or owns the entry.
func (entry *entry) save() error {
	content, marshalError := json.MarshalIndent(entry.record, "", "  ")
	if marshalError != nil {
		return marshalError
	}
	temporary := entry.path("job.tmp")
	writeError := os.WriteFile(temporary, content, 0o644)
	if writeError != nil {
		return writeError
	}

	return os.Rename(temporary, entry.path("job"))
}

// update changes the record under the lock, saves it, and wakes the watchers.
func (entry *entry) update(change func(record *Record)) {
	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	change(&entry.record)
	_ = entry.save() // best effort: the event log is the record of what happened
	entry.notify()
}

// emit appends an event and wakes the watchers.
func (entry *entry) emit(typ string, data any) {
	_, _ = entry.events.Append(typ, data)
	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	entry.notify()
}

// note records what the hub did, as an event.
func (entry *entry) note(text string) {
	entry.mutex.Lock()
	node, step := entry.record.Node, entry.record.Step
	entry.mutex.Unlock()
	entry.emit(EVENT_NOTE, map[string]any{"text": text, "node": node, "step": step})
}

// notify wakes the watchers; the caller holds entry.mutex.
func (entry *entry) notify() {
	close(entry.changed)
	entry.changed = make(chan struct{})
}

// snapshot is a copy of the record.
func (entry *entry) snapshot() Record {
	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	record := entry.record
	record.Steps = append([]StepRun(nil), entry.record.Steps...)

	return record
}

// isHalted reports whether kill or drop was asked.
func (entry *entry) isHalted() bool {
	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	return entry.stopped
}

// halt asks the worker to stop: kill, or drop.
func (entry *entry) halt() {
	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	if !entry.stopped {
		entry.stopped = true
		close(entry.stop)
	}
}
