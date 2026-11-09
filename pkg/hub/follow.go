package hub

import (
	"errors"
	"sync"
	"time"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// follower tails the node job for the whole attempt, passes every event on into the hub
// job's log, and hands state events to the attempt. A broken tail resumes with after.
type follower struct {
	states chan wire.State
	errs   chan error
	quit   chan struct{}
	once   sync.Once
}

func (follower *follower) close() { follower.once.Do(func() { close(follower.quit) }) }

func (hub *Hub) follow(job *entry, on *link) *follower {
	follow := &follower{states: make(chan wire.State, 64), errs: make(chan error, 1), quit: make(chan struct{})}
	go func() {
		for {
			tailError := hub.call(nil, on, func(session *wire.Session) error {
				// read on every call: a retry resumes after the last event passed on
				record := job.snapshot()
				call, startError := session.Start(wire.CALL_TAIL, wire.Tail{Job: record.NodeJob, After: record.NodeSeq})
				if startError != nil {
					return startError
				}
				defer call.Close()

				replyError := call.Reply(nil)
				if replyError != nil {
					return replyError
				}
				ended := make(chan struct{})
				defer close(ended)

				go func() {
					select {
					case <-follow.quit:
						{
							_ = call.Cancel()
						}
					case <-ended:
						{
						}
					}
				}()

				for {
					envelope, nextError := call.Next()
					if nextError != nil {
						return nextError
					}
					switch {
					case envelope.Reject != nil:
						{
							return envelope.Reject
						}
					case envelope.End != nil:
						{
							return errGone
						}
					case envelope.Data != nil:
						{
							var event wire.Event
							unmarshalError := wire.Unmarshal(envelope.Data, &event)
							if unmarshalError != nil {
								continue
							}
							if !hub.pass(job, event) {
								continue // seen before: its state was already handed on
							}
							if event.Type == wire.EVENT_STATE {
								var state wire.State
								if wire.Unmarshal(event.Data, &state) == nil {
									select {
									case follow.states <- state:
										{
										}
									case <-follow.quit:
										{
											return errHalt
										}
									}
								}
							}
						}
					}
				}
			})
			select {
			case <-follow.quit:
				{
					return
				}
			default:
				{
				}
			}
			if errors.Is(tailError, errLost) || errors.Is(tailError, errClosing) || wire.IsReject(tailError, wire.UNKNOWN) || errors.Is(tailError, errGone) {
				if wire.IsReject(tailError, wire.UNKNOWN) {
					tailError = errGone
				}
				select {
				case follow.errs <- tailError:
					{
					}
				default:
					{
					}
				}

				return
			}
			// the session broke mid-stream: call it again from the last seq passed on
			select {
			case <-follow.quit:
				{
					return
				}
			case <-time.After(CALL_RETRY):
				{
				}
			}
		}
	}()

	return follow
}

// pass copies a node event into the hub job's log and remembers how far it got;
// it reports false for an event passed on before.
func (hub *Hub) pass(job *entry, event wire.Event) bool {
	job.mutex.Lock()
	isSeen := event.Seq <= job.record.NodeSeq
	job.mutex.Unlock()
	if isSeen {
		return false
	}
	_, _ = job.events.Append(event.Type, event.Data)
	job.update(func(record *Record) { record.NodeSeq = event.Seq })

	return true
}
