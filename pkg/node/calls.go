package node

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"github.com/diphyx/grid/pkg/history"
	"github.com/diphyx/grid/pkg/wire"
)

func (node *Node) handle(request *wire.Request) {
	var problem error
	switch request.Call {
	case wire.CALL_OPEN:
		{
			problem = node.open(request)
		}
	case wire.CALL_EXEC:
		{
			var body wire.Exec
			problem = node.withJob(request, &body, func(job *Job) error { return node.exec(request, job, body) })
		}
	case wire.CALL_TAIL:
		{
			var body wire.Tail
			problem = node.withJob(request, &body, func(job *Job) error { return node.tail(request, job, body) })
		}
	case wire.CALL_STAT:
		{
			var body wire.JobRef
			problem = node.withJob(request, &body, func(job *Job) error {
				job.mutex.Lock()
				state := job.meta.State
				job.mutex.Unlock()

				return request.OK(state)
			})
		}
	case wire.CALL_KILL:
		{
			var body wire.JobRef
			problem = node.withJob(request, &body, func(job *Job) error {
				job.mutex.Lock()
				if job.proc != nil {
					job.proc.halt("kill")
				}
				job.mutex.Unlock()

				return request.OK(wire.Empty{})
			})
		}
	case wire.CALL_PUSH:
		{
			var body wire.Push
			problem = node.withJob(request, &body, func(job *Job) error { return node.push(request, job, body) })
		}
	case wire.CALL_PULL:
		{
			var body wire.Pull
			problem = node.withJob(request, &body, func(job *Job) error { return node.pull(request, job, body) })
		}
	case wire.CALL_DROP:
		{
			var body wire.JobRef
			problem = node.withJob(request, &body, func(job *Job) error { return node.drop(request, job) })
		}
	default:
		{
			problem = wire.NewReject(wire.INVALID, "unknown call %q", request.Call)
		}
	}
	if problem != nil {
		_ = request.Fail(problem)
	}
}

// jobOf is any body with a job.
type jobOf interface {
	wire.Checker
}

func jobID(value any) string {
	switch typed := value.(type) {
	case *wire.Exec:
		{
			return typed.Job
		}
	case *wire.Tail:
		{
			return typed.Job
		}
	case *wire.JobRef:
		{
			return typed.Job
		}
	case *wire.Push:
		{
			return typed.Job
		}
	case *wire.Pull:
		{
			return typed.Job
		}
	}

	return ""
}

// withJob decodes the body, finds the job, resets expires, and runs fn.
func (node *Node) withJob(request *wire.Request, body jobOf, callback func(job *Job) error) error {
	decodeBodyError := wire.DecodeBody(request.Body, body)
	if decodeBodyError != nil {
		return decodeBodyError
	}
	node.mutex.Lock()
	job := node.jobs[jobID(body)]
	node.mutex.Unlock()
	if job == nil {
		return wire.NewReject(wire.UNKNOWN, "no job %s", jobID(body))
	}
	job.touch()

	return callback(job)
}

func (node *Node) open(request *wire.Request) error {
	var body wire.Open
	decodeBodyError := wire.DecodeBody(request.Body, &body)
	if decodeBodyError != nil {
		return decodeBodyError
	}
	isServed := false
	for _, name := range node.config.Tags {
		isServed = isServed || name == body.Tag
	}
	if !isServed {
		return wire.NewReject(wire.UNKNOWN, "tag %q not served", body.Tag)
	}
	node.mutex.Lock()
	defer node.mutex.Unlock()

	if len(node.jobs) >= node.config.Jobs.Limit {
		return wire.NewReject(wire.FULL, "%d of %d jobs open", len(node.jobs), node.config.Jobs.Limit)
	}
	id := make([]byte, 16)
	rand.Read(id)
	now := time.Now()
	meta := Meta{ID: hex.EncodeToString(id), Tag: body.Tag, Opened: now, Used: now, State: wire.State{State: wire.STATE_IDLE}}
	job, newJobError := newJob(filepath.Join(node.jobsDir(), meta.ID), meta)
	if newJobError != nil {
		return fmt.Errorf("open: %w", newJobError)
	}
	job.onState = node.onState
	node.jobs[meta.ID] = job
	node.log.Debug("open", "job", meta.ID, "tag", body.Tag)

	return request.OK(wire.OpenOK{Job: meta.ID, Expires: uint64(node.config.Jobs.Expires.D().Milliseconds())})
}

func (node *Node) exec(request *wire.Request, job *Job, body wire.Exec) error {
	def, ok := node.config.Cmds[body.Cmd]
	if !ok {
		return wire.NewReject(wire.UNKNOWN, "no cmd %q", body.Cmd)
	}
	job.mutex.Lock()
	defer job.mutex.Unlock()

	if job.dropped || job.expiring {
		return wire.NewReject(wire.UNKNOWN, "no job %s", job.ID())
	}
	if job.proc != nil || job.meta.State.State == wire.STATE_RUNNING {
		return wire.NewReject(wire.RUNNING, "an exec is on")
	}
	node.start(job, body, def)
	node.log.Debug("exec", "job", job.ID(), "cmd", body.Cmd)

	return request.OK(wire.Empty{})
}

func (node *Node) tail(request *wire.Request, job *Job, body wire.Tail) error {
	okError := request.OK(wire.Empty{})
	if okError != nil {
		return nil
	}
	cancel := make(chan struct{})
	go func() {
		defer close(cancel)

		for {
			envelope, nextError := request.Next()
			if nextError != nil || envelope.Cancel != nil {
				return
			}
		}
	}()
	next := body.After
	for {
		job.mutex.Lock()
		batch, eventsError := job.events(next, 256)
		dropped, changed := job.dropped, job.changed
		job.mutex.Unlock()
		if eventsError != nil && dropped {
			_ = request.End(wire.Empty{}) // dropped meanwhile: its log is gone

			return nil
		}
		if eventsError != nil {
			return wire.NewReject(wire.INVALID, "tail: %v", eventsError)
		}
		for _, event := range batch {
			if request.Data(event) != nil {
				return nil
			}
			next = event.Seq
		}
		if len(batch) == 256 {
			continue
		}
		if dropped {
			_ = request.End(wire.Empty{})

			return nil
		}
		select {
		case <-changed:
		case <-cancel:
			_ = request.End(wire.Empty{})
			return nil
		}
	}
}

func (node *Node) drop(request *wire.Request, job *Job) error {
	node.dropJob(job, "drop")

	return request.OK(wire.Empty{})
}

// dropJob stops a running cmd, then removes the job.
// expireJob drops a job still idle past expires; checked again here, as an exec may have
// come in since the janitor looked.
func (node *Node) expireJob(job *Job, expires time.Duration) {
	job.mutex.Lock()
	isExpired := !job.dropped && job.idle() && time.Since(job.meta.Used) > expires
	if isExpired {
		job.expiring = true // exec now rejects it as gone
	}
	job.mutex.Unlock()
	if isExpired {
		node.dropJob(job, "expired")
	}
}

func (node *Node) dropJob(job *Job, why string) {
	job.mutex.Lock()
	process := job.proc
	job.mutex.Unlock()
	if process != nil {
		process.halt(wire.STATE_STOPPED)
		deadline := time.Now().Add(KILL_WAIT + 2*time.Second)
		for time.Now().Before(deadline) {
			job.mutex.Lock()
			done := job.proc == nil
			job.mutex.Unlock()
			if done {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	node.mutex.Lock()
	delete(node.jobs, job.ID())
	node.mutex.Unlock()
	node.shares.unlink(job)
	job.mutex.Lock()
	node.history.Add(history.Entry{Kind: history.KIND_JOB, Event: why, Subject: job.ID(), Took: time.Since(job.meta.Opened)})
	removeError := job.remove()
	job.mutex.Unlock()
	if removeError != nil {
		node.log.Error("drop: clean up", "job", job.ID(), "err", removeError)
	}
	node.log.Debug(why, "job", job.ID())
}
