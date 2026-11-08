package kit

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// emit adds an event; the caller holds job.mutex.
func (fakeNode *FakeNode) emit(job *fjob, typ string, data any) {
	event := wire.Event{Seq: uint64(len(job.events) + 1), Time: uint64(time.Now().UnixMilli()), Type: typ, Data: wire.MustRaw(data)}
	job.events = append(job.events, event)
	if typ == wire.EVENT_STATE {
		job.state = data.(wire.State)
		if fakeNode.Hub != "" {
			select {
			case fakeNode.posts <- postItem{job.id, event}:
			default:
			}
		}
	}
	close(job.changed)
	job.changed = make(chan struct{})
}

var kitCmds = map[string]bool{"echo": true, "env": true, "fail": true, "sleep": true, "nap": true, "emit": true, "write": true, "copy": true}

func (fakeNode *FakeNode) exec(request *wire.Request) error {
	var body wire.Exec
	decodeBodyError := wire.DecodeBody(request.Body, &body)
	if decodeBodyError != nil {
		return decodeBodyError
	}
	job, jobError := fakeNode.job(body.Job)
	if jobError != nil {
		return jobError
	}
	job.mutex.Lock()
	defer job.mutex.Unlock()

	job.used = time.Now()
	if job.state.State == wire.STATE_RUNNING {
		return wire.NewReject(wire.RUNNING, "an exec is on")
	}
	if !kitCmds[body.Cmd] {
		return wire.NewReject(wire.UNKNOWN, "no cmd %q", body.Cmd)
	}
	kill := make(chan struct{})
	job.kill = kill
	fakeNode.emit(job, wire.EVENT_STATE, wire.State{State: wire.STATE_RUNNING, Cmd: body.Cmd})
	go fakeNode.run(job, body, kill)

	return request.OK(wire.Empty{})
}

func (fakeNode *FakeNode) run(job *fjob, body wire.Exec, kill chan struct{}) {
	log := func(stream, line string) {
		job.mutex.Lock()
		fakeNode.emit(job, wire.EVENT_LOG, wire.Log{Stream: stream, Line: line})
		job.mutex.Unlock()
	}
	end := wire.State{State: wire.STATE_DONE, Cmd: body.Cmd}
	wait := func(duration time.Duration, timeout bool) {
		select {
		case <-kill:
			end.State = wire.STATE_STOPPED
		case <-time.After(duration):
			if timeout {
				end.State = wire.STATE_STOPPED
			}
		}
	}
	switch body.Cmd {
	case "echo":
		{
			log("out", strings.Join(body.Args, " "))
		}
	case "env":
		{
			keys := make([]string, 0, len(body.Env))
			for key := range body.Env {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				log("out", key+"="+body.Env[key])
			}
			log("out", "GRID_JOB="+job.id)
			log("out", "GRID_SPACE=/fake/"+job.id)
			log("out", "GRID_EVENTS=/fake/"+job.id+".events")
		}
	case "fail":
		{
			log("err", "first")
			log("err", "boom")
			end.State, end.Message = wire.STATE_FAILED, "boom"
		}
	case "sleep":
		{
			duration := 60 * time.Second
			if len(body.Args) > 0 {
				var seconds int
				_, _ = fmt.Sscan(body.Args[0], &seconds)
				duration = time.Duration(seconds) * time.Second
			}
			wait(duration, false)
		}
	case "nap":
		{
			wait(time.Second, true)
		}
	case "emit":
		{
			job.mutex.Lock()
			fakeNode.emit(job, wire.EVENT_PREFIX+"kit.ping", map[string]string{"n": "1", "note": "half way"})
			fakeNode.emit(job, wire.EVENT_PREFIX+"kit.done", nil)
			job.mutex.Unlock()
			log("err", "not an event")
		}
	case "copy":
		{
			job.mutex.Lock()
			from, ok := job.files[body.Args[0]]
			if ok && len(body.Args) == 2 && wire.CheckPath(body.Args[1]) == nil {
				job.files[body.Args[1]] = wire.Entry{Path: body.Args[1], Data: from.Data, ModTime: time.Now()}
			} else {
				end.State, end.Message = wire.STATE_FAILED, "usage: copy <from> <to>"
			}
			job.mutex.Unlock()
		}
	case "write":
		{
			if len(body.Args) == 2 && wire.CheckPath(body.Args[0]) == nil {
				now := time.Now()
				job.mutex.Lock()
				job.files[body.Args[0]] = wire.Entry{Path: body.Args[0], Data: []byte(body.Args[1]), ModTime: now}
				job.files["result.json"] = wire.Entry{Path: "result.json", Data: []byte(`{"ok":true}`), ModTime: now}
				job.mutex.Unlock()
			} else {
				end.State, end.Message = wire.STATE_FAILED, "usage: write <path> <content>"
			}
		}
	}
	job.mutex.Lock()
	defer job.mutex.Unlock()

	if job.dropped {
		return
	}
	if job.kill == kill {
		job.kill = nil
	}
	fakeNode.emit(job, wire.EVENT_STATE, end)
}

func (fakeNode *FakeNode) tail(request *wire.Request) error {
	var body wire.Tail
	decodeBodyError := wire.DecodeBody(request.Body, &body)
	if decodeBodyError != nil {
		return decodeBodyError
	}
	job, jobError := fakeNode.job(body.Job)
	if jobError != nil {
		return jobError
	}
	okError := request.OK(wire.Empty{})
	if okError != nil {
		return nil
	}
	cancel := make(chan struct{})
	go func() {
		for {
			envelope, nextError := request.Next()
			if nextError != nil {
				close(cancel)

				return
			}
			if envelope.Cancel != nil {
				close(cancel)

				return
			}
		}
	}()
	next := body.After
	for {
		job.mutex.Lock()
		events := job.events[min(int(next), len(job.events)):]
		dropped, changed := job.dropped, job.changed
		job.mutex.Unlock()
		for _, event := range events {
			if request.Data(event) != nil {
				return nil
			}
			next = event.Seq
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
