package node

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Entry is one history line: a finished cmd, or a job's end (Cmd "", State drop|expired).
// History keeps only cmds, states, and times.
type Entry struct {
	Job     string    `json:"job"`
	Tag     string    `json:"tag"`
	Cmd     string    `json:"cmd,omitempty"`
	State   string    `json:"state"`
	Message string    `json:"message,omitempty"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

type history struct {
	path  string
	keep  time.Duration // 0 = forever
	mutex sync.Mutex
	file  *os.File
}

func openHistory(path string, keep time.Duration) (*history, error) {
	file, openFileError := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if openFileError != nil {
		return nil, openFileError
	}
	created := &history{path: path, keep: keep, file: file}
	created.prune()

	return created, nil
}

func (history *history) add(meta Meta, run Run) {
	content, _ := json.Marshal(Entry{Job: meta.ID, Tag: meta.Tag, Cmd: run.Cmd, State: run.State, Message: run.Message, Start: run.Start, End: run.End})
	history.mutex.Lock()
	defer history.mutex.Unlock()

	_, _ = history.file.Write(append(content, '\n'))
}

// Read returns the entries, oldest first; on a read error, those read until then.
func (history *history) Read() []Entry {
	history.mutex.Lock()
	defer history.mutex.Unlock()

	entries, _ := history.readLocked()

	return entries
}

// readLocked reads the file; the caller holds history.mutex. An error means the entries
// are not all there.
func (history *history) readLocked() ([]Entry, error) {
	content, readFileError := os.ReadFile(history.path)
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

// prune drops entries older than keep.
func (history *history) prune() {
	if history.keep == 0 {
		return
	}
	// locked from read to rewrite, so an entry added meanwhile is not lost
	history.mutex.Lock()
	defer history.mutex.Unlock()

	// only a whole read may be rewritten: a partial one would lose the rest
	entries, readError := history.readLocked()
	if readError != nil {
		return
	}
	cut := time.Now().Add(-history.keep)
	var survivors bytes.Buffer
	kept := 0
	for _, entry := range entries {
		if entry.End.After(cut) {
			content, _ := json.Marshal(entry)
			survivors.Write(append(content, '\n'))
			kept++
		}
	}
	if kept == len(entries) {
		return
	}
	temporary := history.path + ".tmp"
	if os.WriteFile(temporary, survivors.Bytes(), 0o644) != nil || os.Rename(temporary, history.path) != nil {
		return
	}
	history.file.Close()
	history.file, _ = os.OpenFile(history.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}
