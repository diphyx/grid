package kit

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// tailer collects one tail stream.
type tailer struct {
	call      *wire.Call
	mutex     sync.Mutex
	collected []wire.Event
	ended     bool
	problem   error
	tick      chan struct{}
}

func startTail(session *wire.Session, job string, after uint64) (*tailer, error) {
	call, startError := session.Start(wire.CALL_TAIL, wire.Tail{Job: job, After: after})
	if startError != nil {
		return nil, startError
	}
	replyError := call.Reply(nil)
	if replyError != nil {
		call.Close()

		return nil, replyError
	}
	follower := &tailer{call: call, tick: make(chan struct{}, 1)}
	go func() {
		defer call.Close()

		for {
			envelope, nextError := call.Next()
			follower.mutex.Lock()
			switch {
			case nextError != nil:
				{
					follower.problem, follower.ended = nextError, true
				}
			case envelope.Reject != nil:
				{
					follower.problem, follower.ended = envelope.Reject, true
				}
			case envelope.End != nil:
				{
					follower.ended = true
				}
			case envelope.Data != nil:
				{
					var event wire.Event
					unmarshalError := wire.Unmarshal(envelope.Data, &event)
					if unmarshalError != nil {
						follower.problem = fmt.Errorf("event: %w", unmarshalError)
					} else {
						follower.collected = append(follower.collected, event)
					}
				}
			}
			done := follower.ended
			follower.mutex.Unlock()
			select {
			case follower.tick <- struct{}{}:
			default:
			}
			if done {
				return
			}
		}
	}()

	return follower, nil
}

func (tailer *tailer) events() []wire.Event {
	tailer.mutex.Lock()
	defer tailer.mutex.Unlock()

	return append([]wire.Event(nil), tailer.collected...)
}

func (tailer *tailer) count() int { return len(tailer.events()) }

func (tailer *tailer) find(callback func(wire.Event) bool) (wire.Event, bool) {
	for _, event := range tailer.events() {
		if callback(event) {
			return event, true
		}
	}

	return wire.Event{}, false
}

func (tailer *tailer) waitFrom(from int, callback func(wire.Event) bool, duration time.Duration) (wire.Event, error) {
	deadline := time.After(duration)
	for {
		collected := tailer.events()
		for _, event := range collected[min(from, len(collected)):] {
			if callback(event) {
				return event, nil
			}
		}
		tailer.mutex.Lock()
		ended, endedError := tailer.ended, tailer.problem
		tailer.mutex.Unlock()
		if ended {
			if endedError == nil {
				endedError = errors.New("tail ended")
			}

			return wire.Event{}, endedError
		}
		select {
		case <-tailer.tick:
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			return wire.Event{}, fmt.Errorf("no such event in %s", duration)
		}
	}
}

func (tailer *tailer) wait(callback func(wire.Event) bool, duration time.Duration) (wire.Event, error) {
	return tailer.waitFrom(0, callback, duration)
}

func isEnd(event wire.Event) bool {
	if event.Type != wire.EVENT_STATE {
		return false
	}
	var state wire.State

	return wire.Unmarshal(event.Data, &state) == nil && state.State != wire.STATE_RUNNING && state.State != wire.STATE_IDLE
}

func (tailer *tailer) waitStateFrom(from int, duration time.Duration) (wire.State, error) {
	event, waitFromError := tailer.waitFrom(from, isEnd, duration)
	if waitFromError != nil {
		return wire.State{}, waitFromError
	}
	var state wire.State

	return state, wire.Unmarshal(event.Data, &state)
}

func (tailer *tailer) waitState(duration time.Duration) (wire.State, error) {
	return tailer.waitStateFrom(0, duration)
}

func (tailer *tailer) lastState() wire.State {
	collected := tailer.events()
	for i := len(collected) - 1; i >= 0; i-- {
		if collected[i].Type == wire.EVENT_STATE {
			var state wire.State
			_ = wire.Unmarshal(collected[i].Data, &state)

			return state
		}
	}

	return wire.State{}
}

func (tailer *tailer) waitEnd(duration time.Duration) error {
	deadline := time.After(duration)
	for {
		tailer.mutex.Lock()
		ended, problem := tailer.ended, tailer.problem
		tailer.mutex.Unlock()
		if ended && problem != nil {
			return fmt.Errorf("ended with %w, want end {}", problem)
		}
		if ended {
			return nil
		}
		select {
		case <-tailer.tick:
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			return fmt.Errorf("no end in %s", duration)
		}
	}
}

func (tailer *tailer) logs(stream string) []string {
	var out []string
	for _, event := range tailer.events() {
		if event.Type != wire.EVENT_LOG {
			continue
		}
		var line wire.Log
		if wire.Unmarshal(event.Data, &line) == nil && line.Stream == stream {
			out = append(out, line.Line)
		}
	}

	return out
}

func (tailer *tailer) wantLog(stream, line string) error {
	for _, logLine := range tailer.logs(stream) {
		if logLine == line {
			return nil
		}
	}

	return fmt.Errorf("no %s line %q", stream, line)
}

func (tailer *tailer) wantLogPrefix(stream, prefix string) error {
	for _, line := range tailer.logs(stream) {
		if strings.HasPrefix(line, prefix) && len(line) > len(prefix) {
			return nil
		}
	}

	return fmt.Errorf("no %s line %q…", stream, prefix)
}

func (tailer *tailer) wantStates(want ...string) error {
	var got []string
	for _, event := range tailer.events() {
		if event.Type == wire.EVENT_STATE {
			var state wire.State
			_ = wire.Unmarshal(event.Data, &state)
			got = append(got, state.State)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("states %v, want %v", got, want)
	}
	return nil
}

func (tailer *tailer) seqOK() error {
	for i, event := range tailer.events() {
		if event.Seq != uint64(i+1) {
			return fmt.Errorf("event %d has seq %d", i+1, event.Seq)
		}
		if event.Time == 0 {
			return fmt.Errorf("event %d has no time", event.Seq)
		}
	}

	return nil
}
