// Package history is the event log of a node or a hub: what happened to its jobs, its shares,
// itself, and its peers, one JSON line per event. It is kept under a size limit and for a time.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Kinds of events.
const (
	KIND_JOB   = "job"   // a cmd finished, a job ended
	KIND_SHARE = "share" // a share installed or removed
	KIND_NODE  = "node"  // a node: started, stopped; on the hub, online, lost
	KIND_HUB   = "hub"   // a hub: started, stopped; on the node, connected, disconnected, refused
)

// Entry is one event.
type Entry struct {
	At      time.Time     `json:"at"` // when it happened, or ended
	Kind    string        `json:"kind"`
	Event   string        `json:"event"`
	Subject string        `json:"subject,omitempty"`
	Message string        `json:"message,omitempty"`
	Took    time.Duration `json:"took,omitempty"` // for what has a duration; it began at At − Took
}

// Log is the log on disk. A nil Log is off: it keeps nothing and reads empty.
type Log struct {
	path    string
	limit   int64         // bytes; over it, the oldest entries go
	expires time.Duration // older entries go; 0 = never
	mutex   sync.Mutex
	file    *os.File
	size    int64
}

// OpenLog opens or makes the log at path; limit 0 turns it off and returns nil.
func OpenLog(path string, limit int64, expires time.Duration) (*Log, error) {
	if limit == 0 {
		return nil, nil
	}
	mkdirError := os.MkdirAll(filepath.Dir(path), 0o755)
	if mkdirError != nil {
		return nil, mkdirError
	}
	file, openFileError := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if openFileError != nil {
		return nil, openFileError
	}
	info, statError := file.Stat()
	if statError != nil {
		_ = file.Close()

		return nil, statError
	}
	created := &Log{path: path, limit: limit, expires: expires, file: file, size: info.Size()}
	created.Prune()

	return created, nil
}

// Add appends an entry; a zero At is now. Past the limit, the oldest entries go.
func (log *Log) Add(entry Entry) {
	if log == nil {
		return
	}
	if entry.At.IsZero() {
		entry.At = time.Now()
	}
	content, _ := json.Marshal(entry)
	log.mutex.Lock()
	_, writeError := log.file.Write(append(content, '\n'))
	if writeError == nil {
		log.size += int64(len(content)) + 1
	}
	isOver := log.size > log.limit
	log.mutex.Unlock()
	if isOver {
		log.Prune()
	}
}

// Read returns the entries of a kind, "" = all, oldest first; on a read error, those read
// until then.
func (log *Log) Read(kind string) []Entry {
	if log == nil {
		return []Entry{}
	}
	log.mutex.Lock()
	defer log.mutex.Unlock()

	entries, _ := log.readLocked()
	out := []Entry{}
	for _, entry := range entries {
		if kind == "" || entry.Kind == kind {
			out = append(out, entry)
		}
	}

	return out
}

// Close closes the file.
func (log *Log) Close() error {
	if log == nil {
		return nil
	}
	log.mutex.Lock()
	defer log.mutex.Unlock()

	return log.file.Close()
}

// readLocked reads the file; the caller holds log.mutex. An error means the entries are not
// all there.
func (log *Log) readLocked() ([]Entry, error) {
	content, readFileError := os.ReadFile(log.path)
	if readFileError != nil {
		return nil, readFileError
	}
	var out []Entry
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			out = append(out, entry)
		}
	}

	return out, scanner.Err()
}

// Prune drops entries older than expires, then the oldest until the rest fit in 90% of the
// limit, so a full log is not rewritten on every Add.
func (log *Log) Prune() {
	if log == nil {
		return
	}
	// locked from read to rewrite, so an entry added meanwhile is not lost
	log.mutex.Lock()
	defer log.mutex.Unlock()

	// only a whole read may be rewritten: a partial one would lose the rest
	entries, readError := log.readLocked()
	if readError != nil {
		return
	}
	lines := make([][]byte, 0, len(entries))
	cut := time.Now().Add(-log.expires)
	for _, entry := range entries {
		if log.expires == 0 || entry.At.After(cut) {
			content, _ := json.Marshal(entry)
			lines = append(lines, append(content, '\n'))
		}
	}
	var total int64
	for _, line := range lines {
		total += int64(len(line))
	}
	if total > log.limit {
		for len(lines) > 0 && total > log.limit/10*9 {
			total -= int64(len(lines[0]))
			lines = lines[1:]
		}
	}
	if len(lines) == len(entries) && total == log.size {
		return
	}
	temporary := log.path + ".tmp"
	if os.WriteFile(temporary, bytes.Join(lines, nil), 0o644) != nil || os.Rename(temporary, log.path) != nil {
		return
	}
	_ = log.file.Close()
	file, openFileError := os.OpenFile(log.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if openFileError != nil {
		return
	}
	log.file, log.size = file, total
}
