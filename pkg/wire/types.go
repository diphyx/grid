package wire

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Calls (§6).
const (
	CALL_OPEN = "open"
	CALL_PUSH = "push"
	CALL_EXEC = "exec"
	CALL_TAIL = "tail"
	CALL_STAT = "stat"
	CALL_KILL = "kill"
	CALL_PULL = "pull"
	CALL_DROP = "drop"
	CALL_BEAT = "beat"
	CALL_POST = "post"
)

// States (§7).
const (
	STATE_IDLE    = "idle"
	STATE_RUNNING = "running"
	STATE_DONE    = "done"
	STATE_FAILED  = "failed"
	STATE_STOPPED = "stopped"
)

// Event types (§8).
const (
	EVENT_LOG   = "log"
	EVENT_STATE = "state"
)

// Reject reasons (§11).
const (
	FULL    = "full"
	RUNNING = "running"
	UNKNOWN = "unknown"
	INVALID = "invalid"
)

// Reject is `{reason, message?}`; it is also an error.
type Reject struct {
	Reason  string `cbor:"reason"`
	Message string `cbor:"message,omitempty"`
}

// Error is the reject as text.
func (reject *Reject) Error() string {
	if reject.Message == "" {
		return "reject " + reject.Reason
	}
	return "reject " + reject.Reason + ": " + reject.Message
}

// NewReject makes a reject; the message is printf-style.
func NewReject(reason, format string, args ...any) *Reject {
	return &Reject{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// IsReject reports whether err is a reject with that reason.
func IsReject(problem error, reason string) bool {
	var reject *Reject

	return errors.As(problem, &reject) && reject.Reason == reason
}

// Bodies, in and out.

type Open struct {
	Tag string `cbor:"tag"`
}
type OpenOK struct {
	Job     string `cbor:"job"`
	Expires uint64 `cbor:"expires"`
}

type Push struct {
	Job  string `cbor:"job"`
	Path string `cbor:"path,omitempty"`
	ID   string `cbor:"id,omitempty"` // a share push: the tar's id (§10)
}
type PushOffset struct {
	Offset uint64 `cbor:"offset"`
	Have   bool   `cbor:"have"` // a share hit: send no bytes, only end
}
type PushOK struct {
	Sha256 []byte `cbor:"sha256"`
	Share  bool   `cbor:"share"` // in the share, linked into the space; false = in the space itself
}

type Exec struct {
	Job  string            `cbor:"job"`
	Cmd  string            `cbor:"cmd"`
	Args []string          `cbor:"args,omitempty"`
	Env  map[string]string `cbor:"env,omitempty"`
}

type Tail struct {
	Job   string `cbor:"job"`
	After uint64 `cbor:"after,omitempty"`
}

type JobRef struct {
	Job string `cbor:"job"`
}

// State is `stat`'s ok and a `state` event's data.
type State struct {
	State   string `cbor:"state"`
	Cmd     string `cbor:"cmd,omitempty"`
	Message string `cbor:"message,omitempty"`
}

type Pull struct {
	Job    string `cbor:"job"`
	Path   string `cbor:"path"`
	Since  uint64 `cbor:"since,omitempty"`
	Offset uint64 `cbor:"offset,omitempty"`
}
type File struct {
	Path     string `cbor:"path"`
	Size     uint64 `cbor:"size"`
	Modified uint64 `cbor:"modified"`
}
type PullOK struct {
	Files []File `cbor:"files"`
}
type PullEnd struct {
	Sha256 []byte `cbor:"sha256,omitempty"`
}

type Beat struct {
	Tags  []string `cbor:"tags"`
	Free  uint64   `cbor:"free"`
	Share bool     `cbor:"share"` // the node's share is on
}

type Post struct {
	Job   string `cbor:"job"`
	Event Event  `cbor:"event"`
}

type Event struct {
	Seq  uint64 `cbor:"seq"`
	Time uint64 `cbor:"time"`
	Type string `cbor:"type"`
	Data Raw    `cbor:"data"`
}

// JSONEvent is an event for JSON: what the daemons' tail streams as JSON lines.
type JSONEvent struct {
	Seq  uint64 `json:"seq"`
	Time uint64 `json:"time"`
	Type string `json:"type"`
	Data any    `json:"data"`
}

// ToJSON turns an event into its JSON form.
func (event Event) ToJSON() JSONEvent {
	return JSONEvent{Seq: event.Seq, Time: event.Time, Type: event.Type, Data: ToAny(event.Data)}
}

type Log struct {
	Stream string `cbor:"stream"`
	Line   string `cbor:"line"`
}

// Checks (§5, §6). Each returns a reject invalid, or nil.

var (
	idRe   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	nameRe = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
)

// IsID reports whether text is an id (§5): 32 lowercase hex chars.
func IsID(text string) bool { return idRe.MatchString(text) }

// CheckJob enforces §5: a job is an id.
func CheckJob(jobID string) error {
	if !IsID(jobID) {
		return NewReject(INVALID, "job %q: want 32 lowercase hex", jobID)
	}
	return nil
}

// CheckID enforces §5 on a push id.
func CheckID(pushID string) error {
	if !IsID(pushID) {
		return NewReject(INVALID, "id %q: want 32 lowercase hex", pushID)
	}
	return nil
}

// CheckPath enforces §5: relative, `/` separators, no `..`, no leading `/`.
func CheckPath(path string) error {
	if path == "" {
		return NewReject(INVALID, "path: empty")
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\\") || strings.ContainsRune(path, 0) {
		return NewReject(INVALID, "path %q: must be relative with / separators", path)
	}
	for _, text := range strings.Split(path, "/") {
		if text == ".." {
			return NewReject(INVALID, "path %q: leaves the space", path)
		}
	}

	return nil
}

// IsState reports whether s is one of the states (§7).
func IsState(text string) bool {
	switch text {
	case STATE_IDLE, STATE_RUNNING, STATE_DONE, STATE_FAILED, STATE_STOPPED:
		{
			return true
		}
	}

	return false
}

// IsReason reports whether r is one of the reject reasons (§11).
func IsReason(text string) bool {
	switch text {
	case FULL, RUNNING, UNKNOWN, INVALID:
		{
			return true
		}
	}

	return false
}

// Check enforces the rules for a Open body; a bad one is a reject invalid.
func (open *Open) Check() error {
	if open.Tag == "" {
		return NewReject(INVALID, "open: missing tag")
	}
	return nil
}

// Check enforces the rules for a Push body; a bad one is a reject invalid.
func (push *Push) Check() error {
	checkJobError := CheckJob(push.Job)
	if checkJobError != nil {
		return checkJobError
	}
	if push.ID != "" {
		checkIDError := CheckID(push.ID)
		if checkIDError != nil {
			return checkIDError
		}
		// a share is linked in place of a path, never of the space itself
		if push.Path == "" || push.Path == "." {
			return NewReject(INVALID, "push: a share needs a path")
		}
	}
	if push.Path != "" {
		return CheckPath(push.Path)
	}
	return nil
}

// Check enforces the rules for a Exec body; a bad one is a reject invalid.
func (exec *Exec) Check() error {
	checkJobError := CheckJob(exec.Job)
	if checkJobError != nil {
		return checkJobError
	}
	if exec.Cmd == "" {
		return NewReject(INVALID, "exec: missing cmd")
	}
	for key := range exec.Env {
		if strings.HasPrefix(key, "GRID_") {
			return NewReject(INVALID, "exec: env may not set %s", key)
		}
	}

	return nil
}

// Check enforces the rules for a Tail body; a bad one is a reject invalid.
func (tail *Tail) Check() error { return CheckJob(tail.Job) }

// Check enforces the rules for a JobRef body; a bad one is a reject invalid.
func (jobRef *JobRef) Check() error { return CheckJob(jobRef.Job) }

// Check enforces the rules for a Pull body; a bad one is a reject invalid.
func (pull *Pull) Check() error {
	checkJobError := CheckJob(pull.Job)
	if checkJobError != nil {
		return checkJobError
	}
	return CheckPath(pull.Path)
}

// Check enforces the rules for a Beat body; a bad one is a reject invalid.
func (beat *Beat) Check() error { return nil }

// Check enforces the rules for a Post body; a bad one is a reject invalid.
func (post *Post) Check() error {
	checkJobError := CheckJob(post.Job)
	if checkJobError != nil {
		return checkJobError
	}
	if post.Event.Type != EVENT_STATE || post.Event.Seq == 0 {
		return NewReject(INVALID, "post: want a state event with seq")
	}
	return nil
}

// Checker is a body with a Check.
type Checker interface{ Check() error }

// DecodeBody decodes and checks a call body; a bad one is a reject invalid.
func DecodeBody(raw Raw, body Checker) error {
	unmarshalError := Unmarshal(raw, body)
	if unmarshalError != nil {
		return NewReject(INVALID, "body: %v", unmarshalError)
	}
	return body.Check()
}
