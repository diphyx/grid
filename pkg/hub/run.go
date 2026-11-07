package hub

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// Timing of the worker.
const (
	RETRY_EVERY = 2 * time.Second  // a queued job tries the nodes again
	CALL_RETRY  = time.Second      // a call that did not reach the node is sent again
	KILL_WAIT   = 15 * time.Second // after kill, how long to wait for the node's `stopped`
)

var (
	errLost    = errors.New("node lost")
	errHalt    = errors.New("stopped")
	errClosing = errors.New("hub closing")
	errGone    = errors.New("the node no longer has the job")
)

// outcome is how one attempt ended.
type outcome struct {
	state   string // done, failed, stopped; "" when the job goes on elsewhere or later
	message string
	problem error // errLost, errHalt, errClosing, errGone
}

// work carries one job to its end: place it on a node, run it there, and on a lost node
// place it again, up to its retries.
func (hub *Hub) work(job *entry) {
	defer close(job.done)

	for {
		record := job.snapshot()
		if record.Node == "" {
			clearError := job.clearFiles()
			if clearError != nil {
				hub.settle(job, nil, outcome{state: STATE_FAILED, message: "clear files: " + clearError.Error()})

				return
			}
			placed, nodeJob, placeError := hub.place(job)
			if placeError != nil {
				hub.settle(job, nil, outcome{state: STATE_STOPPED, message: "stopped before it started", problem: placeError})

				return
			}
			job.update(func(record *Record) {
				record.Node, record.NodeJob, record.NodeSeq = placed.address, nodeJob, 0
				record.Pushed, record.Step, record.Execed, record.Pulled = false, 0, false, false
				record.Attempt++
				record.State, record.Message = STATE_RUNNING, ""
				if record.Started.IsZero() {
					record.Started = time.Now()
				}
				for i := range record.Steps {
					record.Steps[i] = StepRun{Cmd: record.Steps[i].Cmd, State: wire.STATE_IDLE}
				}
			})
			job.emit(EVENT_JOB, map[string]any{"state": STATE_RUNNING})
			job.note(fmt.Sprintf("attempt %d on %s", job.snapshot().Attempt, placed.address))
			record = job.snapshot()
		}
		current := hub.linkOf(record.Node)
		if current == nil {
			hub.settle(job, nil, outcome{state: STATE_FAILED, message: "node " + record.Node + " is no longer in the config"})

			return
		}
		current.mutex.Lock()
		current.running++
		current.mutex.Unlock()
		result := hub.attempt(job, current)
		current.mutex.Lock()
		current.running--
		current.mutex.Unlock()
		switch {
		case errors.Is(result.problem, errClosing):
			{
				return
			}
		case errors.Is(result.problem, errLost) || errors.Is(result.problem, errGone):
			{
				record = job.snapshot()
				if record.Attempt > record.Job.Retries {
					hub.settle(job, current, outcome{state: STATE_FAILED, message: result.problem.Error() + "; no retries left"})

					return
				}
				job.note(result.problem.Error() + "; again on another node")
				job.update(func(record *Record) { record.Node, record.NodeJob = "", "" })
			}
		default:
			{
				hub.settle(job, current, result)

				return
			}
		}
	}
}

// settle ends the job: drops it on the node, records the end, or forgets it if dropped.
func (hub *Hub) settle(job *entry, on *link, result outcome) {
	record := job.snapshot()
	if on != nil && record.NodeJob != "" {
		session, connectError := on.connect(hub.key)
		if connectError == nil {
			_ = session.Do(wire.CALL_DROP, wire.JobRef{Job: record.NodeJob}, nil)
		}
	}
	job.mutex.Lock()
	dropped := job.dropped
	job.mutex.Unlock()
	if dropped {
		return
	}
	// the event first: a reader that sees the job finished must also find its last event
	job.emit(EVENT_JOB, map[string]any{"state": result.state, "message": result.message})
	job.update(func(record *Record) {
		record.State, record.Message, record.Ended = result.state, result.message, time.Now()
	})
	hub.log.Debug("job end", "job", record.ID, "state", result.state, "message", result.message)
}

// place opens the job on the first node that takes it; while none does, the job stays queued.
func (hub *Hub) place(job *entry) (*link, string, error) {
	tag := job.snapshot().Job.Tag
	isWaitingNoted := false
	for {
		for _, candidate := range hub.candidates(tag, nil) {
			session, connectError := candidate.connect(hub.key)
			if connectError != nil {
				continue
			}
			var opened wire.OpenOK
			openError := session.Do(wire.CALL_OPEN, wire.Open{Tag: tag}, &opened)
			if openError == nil {
				candidate.seen()

				return candidate, opened.Job, nil
			}
			var reject *wire.Reject
			if errors.As(openError, &reject) {
				candidate.seen()

				continue
			}
			candidate.failed()
		}
		if !isWaitingNoted {
			job.note("no node with tag " + tag + " has room; waiting")
			isWaitingNoted = true
		}
		select {
		case <-job.stop:
			{
				return nil, "", errHalt
			}
		case <-hub.stop:
			{
				return nil, "", errClosing
			}
		case <-time.After(RETRY_EVERY):
			{
			}
		}
	}
}

// attempt runs the job on one node from where its record says it is: push, each step, pull.
func (hub *Hub) attempt(job *entry, on *link) outcome {
	record := job.snapshot()
	follow := hub.follow(job, on)
	defer follow.close()

	if !record.Pushed {
		for i, push := range record.Job.Push {
			pushError := hub.call(job.stop, on, func(session *wire.Session) error {
				return hub.push(session, job.stop, record.NodeJob, push.To, job.path("push/"+strconv.Itoa(i)))
			})
			if pushError != nil {
				return hub.failure(job, pushError, "push "+push.From)
			}
		}
		if len(record.Job.Push) > 0 {
			job.note("pushed")
		}
		job.update(func(record *Record) { record.Pushed = true })
	}
	if job.isHalted() {
		return outcome{state: STATE_STOPPED, message: "killed"}
	}
	isStopping := false
	finalState, finalMessage := STATE_DONE, ""
	for step := job.snapshot().Step; step < len(record.Job.Steps); step++ {
		current := record.Job.Steps[step]
		ended, endError := hub.step(job, on, follow, step, current)
		if errors.Is(endError, errHalt) {
			isStopping = true
		} else if endError != nil {
			return hub.failure(job, endError, "step "+current.Cmd)
		}
		job.update(func(record *Record) {
			record.Steps[step].State, record.Steps[step].Message, record.Steps[step].End = ended.State, ended.Message, time.Now()
			record.Step, record.Execed = step+1, false
		})
		if isStopping {
			break
		}
		if ended.State != wire.STATE_DONE {
			// a job ends `stopped` only by kill; a step stopped otherwise (its timeout) fails it
			finalState, finalMessage = STATE_FAILED, fmt.Sprintf("step %d (%s): %s", step+1, current.Cmd, ended.State)
			if ended.Message != "" {
				finalMessage += ": " + ended.Message
			}

			break
		}
	}
	if isStopping || job.isHalted() {
		return outcome{state: STATE_STOPPED, message: "killed"}
	}
	if !job.snapshot().Pulled {
		for _, pull := range record.Job.Pull {
			pullError := hub.call(job.stop, on, func(session *wire.Session) error {
				return hub.pull(session, job, record.NodeJob, pull.Path)
			})
			if pullError != nil {
				return hub.failure(job, pullError, "pull "+pull.Path)
			}
		}
		if len(record.Job.Pull) > 0 {
			job.note("pulled")
		}
		job.update(func(record *Record) { record.Pulled = true })
	}

	return outcome{state: finalState, message: finalMessage}
}

// failure turns an error during an attempt into its outcome.
func (hub *Hub) failure(job *entry, problem error, what string) outcome {
	switch {
	case errors.Is(problem, errLost) || errors.Is(problem, errClosing) || errors.Is(problem, errGone):
		{
			return outcome{problem: problem}
		}
	case errors.Is(problem, errHalt):
		{
			return outcome{state: STATE_STOPPED, message: "killed during " + what}
		}
	case wire.IsReject(problem, wire.UNKNOWN) && what != "" && job.snapshot().NodeJob != "":
		{
			return outcome{problem: errGone}
		}
	}

	return outcome{state: STATE_FAILED, message: what + ": " + problem.Error()}
}

// step runs one step, or picks up one that was already sent, and waits for its end.
// On kill it kills the cmd and waits for `stopped`; then errHalt says the job is stopping.
func (hub *Hub) step(job *entry, on *link, follow *follower, index int, current Step) (wire.State, error) {
	record := job.snapshot()
	// an end counts only after this step's own `running`: an older end, still queued
	// from before a restart, belongs to an earlier step
	isStarted, isSent := false, false
	if record.Execed {
		// Picked up after a restart or a lost connection: the node knows how far it got.
		var known wire.State
		statError := hub.call(nil, on, func(session *wire.Session) error {
			return session.Do(wire.CALL_STAT, wire.JobRef{Job: record.NodeJob}, &known)
		})
		if statError != nil {
			return wire.State{}, statError
		}
		if known.Cmd == current.Cmd && known.State == wire.STATE_RUNNING {
			isStarted, isSent = true, true
		} else if known.Cmd == current.Cmd && known.State != wire.STATE_IDLE {
			return known, nil
		}
		// otherwise the exec never got there: send it now
	} else {
		job.update(func(record *Record) {
			record.Execed = true
			record.Steps[index].State, record.Steps[index].Start = wire.STATE_RUNNING, time.Now()
		})
		job.note(fmt.Sprintf("step %d: %s", index+1, current.Cmd))
	}
	if !isSent {
		execError := hub.call(nil, on, func(session *wire.Session) error {
			sendError := session.Do(wire.CALL_EXEC, wire.Exec{Job: record.NodeJob, Cmd: current.Cmd, Args: current.Args, Env: current.Env}, nil)
			if wire.IsReject(sendError, wire.RUNNING) {
				return nil // an earlier send got there; that exec is this step
			}

			return sendError
		})
		if wire.IsReject(execError, wire.UNKNOWN) && hub.hasJob(on, record.NodeJob) {
			// the job is there, so the cmd is not: a failure, not a lost job to retry
			return wire.State{}, errors.New(execError.Error())
		}
		if execError != nil {
			return wire.State{}, execError
		}
	}
	isKilled := false
	var killDeadline <-chan time.Time
	stop := job.stop
	for {
		select {
		case state := <-follow.states:
			{
				if state.State == wire.STATE_RUNNING && state.Cmd == current.Cmd {
					isStarted = true
				}
				if !isStarted || state.State == wire.STATE_RUNNING || state.State == wire.STATE_IDLE {
					continue
				}
				if isKilled {
					return state, errHalt
				}

				return state, nil
			}
		case followError := <-follow.errs:
			{
				return wire.State{}, followError
			}
		case <-stop:
			{
				stop = nil
				isKilled = true
				killDeadline = time.After(KILL_WAIT)
				job.note("killing " + current.Cmd)
				_ = hub.call(nil, on, func(session *wire.Session) error {
					return session.Do(wire.CALL_KILL, wire.JobRef{Job: record.NodeJob}, nil)
				})
			}
		case <-killDeadline:
			{
				return wire.State{State: wire.STATE_STOPPED, Cmd: current.Cmd, Message: "no answer to kill"}, errHalt
			}
		case <-hub.stop:
			{
				return wire.State{}, errClosing
			}
		}
	}
}

// hasJob reports whether the node still has the job.
func (hub *Hub) hasJob(on *link, nodeJob string) bool {
	statError := hub.call(nil, on, func(session *wire.Session) error {
		return session.Do(wire.CALL_STAT, wire.JobRef{Job: nodeJob}, nil)
	})

	return statError == nil
}
