package kit

import (
	"errors"
	"fmt"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

func (nodeRun *nodeRun) exec(session *wire.Session) {
	job, checkError := openJob(session, nodeRun.target.Tag)
	if !nodeRun.report.Check("§6", "open for exec", checkError) {
		return
	}
	defer func() { _ = session.Do(wire.CALL_DROP, wire.JobRef{Job: job}, nil) }()

	stream, checkError := startTail(session, job, 0)
	if !nodeRun.report.Check("§8", "tail before exec", checkError) {
		return
	}
	defer func() { _ = stream.call.Cancel() }()

	// echo: args, log, state events, stat
	checkError = session.Do(wire.CALL_EXEC, wire.Exec{Job: job, Cmd: "echo", Args: []string{"hello", "grid"}}, nil)
	if nodeRun.report.Check("§6", "exec", checkError) {
		state, waitStateError := stream.waitState(nodeRun.target.Wait)
		if waitStateError == nil && state.State != wire.STATE_DONE {
			waitStateError = fmt.Errorf("state %q, want done", state.State)
		}
		nodeRun.report.Check("§9", "exit 0 → done", waitStateError)
		nodeRun.report.Check("§9", "args appended", stream.wantLog("out", "hello grid"))
		nodeRun.report.Check("§8", "state events: running, then done", stream.wantStates(wire.STATE_RUNNING, wire.STATE_DONE))
		nodeRun.report.Check("§8", "seq from 1, no gaps; time set", stream.seqOK())
		var got wire.State
		waitStateError = session.Do(wire.CALL_STAT, wire.JobRef{Job: job}, &got)
		if waitStateError == nil && (got.State != wire.STATE_DONE || got.Cmd != "echo") {
			waitStateError = fmt.Errorf("got %+v", got)
		}
		nodeRun.report.Check("§8", "stat = last state event", waitStateError)
	}

	nodeRun.report.Check("§6", "exec unknown cmd → unknown", wantReject(session.Do(wire.CALL_EXEC, wire.Exec{Job: job, Cmd: "kit-no-such-cmd"}, nil), wire.UNKNOWN))
	nodeRun.report.Check("§6", "exec env GRID_* → invalid", wantReject(session.Do(wire.CALL_EXEC, wire.Exec{Job: job, Cmd: "env", Env: map[string]string{"GRID_X": "1"}}, nil), wire.INVALID))
	nodeRun.report.Check("§6", "exec unknown job → unknown", wantReject(session.Do(wire.CALL_EXEC, wire.Exec{Job: randJob(), Cmd: "echo"}, nil), wire.UNKNOWN))

	// env
	if nodeRun.run(session, stream, job, wire.Exec{Job: job, Cmd: "env", Env: map[string]string{"KIT_X": "1"}}, wire.STATE_DONE) {
		nodeRun.report.Check("§9", "env added", stream.wantLog("out", "KIT_X=1"))
		nodeRun.report.Check("§9", "GRID_JOB set", stream.wantLog("out", "GRID_JOB="+job))
		nodeRun.report.Check("§9", "GRID_SPACE set", stream.wantLogPrefix("out", "GRID_SPACE="))
		nodeRun.report.Check("§9", "GRID_EVENTS set", stream.wantLogPrefix("out", "GRID_EVENTS="))
	}

	// fail
	if nodeRun.run(session, stream, job, wire.Exec{Job: job, Cmd: "fail"}, wire.STATE_FAILED) {
		state := stream.lastState()
		var problem error
		if state.Message != "boom" {
			problem = fmt.Errorf("message %q, want the last stderr line %q", state.Message, "boom")
		}
		nodeRun.report.Check("§9", "failed → message = last stderr line", problem)
		nodeRun.report.Check("§8", "stderr → log err", stream.wantLog("err", "boom"))
	}

	// sleep, running, kill
	mark := stream.count()
	checkError = session.Do(wire.CALL_EXEC, wire.Exec{Job: job, Cmd: "sleep", Args: []string{"60"}}, nil)
	if nodeRun.report.Check("§6", "exec sleep", checkError) {
		_, _ = stream.waitFrom(mark, func(event wire.Event) bool { return isState(event, wire.STATE_RUNNING) }, nodeRun.target.Wait)
		nodeRun.report.Check("§6", "exec while running → running", wantReject(session.Do(wire.CALL_EXEC, wire.Exec{Job: job, Cmd: "echo"}, nil), wire.RUNNING))
		var state wire.State
		_ = session.Do(wire.CALL_STAT, wire.JobRef{Job: job}, &state)
		nodeRun.report.Check("§7", "stat while running → running", wantState(state, wire.STATE_RUNNING))
		nodeRun.report.Check("§6", "kill", session.Do(wire.CALL_KILL, wire.JobRef{Job: job}, nil))
		got, waitStateFromError := stream.waitStateFrom(mark, nodeRun.target.Wait)
		if waitStateFromError == nil {
			waitStateFromError = wantState(got, wire.STATE_STOPPED)
		}
		nodeRun.report.Check("§7", "kill → stopped", waitStateFromError)
		nodeRun.report.Check("§6", "kill with nothing on → ok", session.Do(wire.CALL_KILL, wire.JobRef{Job: job}, nil))
	}

	// timeout
	if nodeRun.run(session, stream, job, wire.Exec{Job: job, Cmd: "nap"}, wire.STATE_STOPPED) {
		nodeRun.report.Check("§7", "timeout → stopped", nil)
	}

	// own events
	if nodeRun.run(session, stream, job, wire.Exec{Job: job, Cmd: "emit"}, wire.STATE_DONE) {
		var problem error
		event, ok := stream.find(func(event wire.Event) bool { return event.Type == wire.EVENT_PREFIX+"kit.ping" })
		if !ok {
			problem = errors.New("no evt.kit.ping event")
		} else {
			var data map[string]string
			if wire.Unmarshal(event.Data, &data) != nil || data["n"] != "1" || data["note"] != "half way" {
				problem = fmt.Errorf("data %x, want {n: \"1\", note: \"half way\"}", []byte(event.Data))
			}
		}
		nodeRun.report.Check("§8", "own events from $GRID_EVENTS, with evt. prefix", problem)
		_, hasDone := stream.find(func(event wire.Event) bool { return event.Type == wire.EVENT_PREFIX+"kit.done" })
		var doneProblem error
		if !hasDone {
			doneProblem = errors.New("no evt.kit.done event")
		}
		nodeRun.report.Check("§8", "own event with no pairs", doneProblem)
		nodeRun.report.Check("§8", "bad event line → log err", stream.wantLog("err", "not an event"))
	}

	// tail after
	last := stream.count()
	if last > 2 {
		tail, startTailError := startTail(session, job, uint64(last-1))
		if startTailError == nil {
			_, startTailError = tail.wait(func(wire.Event) bool { return true }, nodeRun.target.Wait)
			if startTailError == nil {
				first := tail.events()[0].Seq
				if first != uint64(last) {
					startTailError = fmt.Errorf("first seq %d, want %d", first, last)
				}
			}
			_ = tail.call.Cancel()
		}
		nodeRun.report.Check("§8", "tail after resumes", startTailError)
	}

	// cancel
	tail, checkError := startTail(session, job, 0)
	if checkError == nil {
		_ = tail.call.Cancel()
		checkError = tail.waitEnd(nodeRun.target.Wait)
	}
	nodeRun.report.Check("§4", "cancel → end", checkError)
}

// run execs, waits for the end state, and checks it.
func (nodeRun *nodeRun) run(session *wire.Session, tail *tailer, job string, body wire.Exec, want string) bool {
	mark := tail.count()
	name := "exec " + body.Cmd + " → " + want
	doError := session.Do(wire.CALL_EXEC, body, nil)
	if doError != nil {
		nodeRun.report.Check("§9", name, doError)

		return false
	}
	state, waitStateFromError := tail.waitStateFrom(mark, nodeRun.target.Wait+10*time.Second)
	if waitStateFromError == nil {
		waitStateFromError = wantState(state, want)
	}

	return nodeRun.report.Check("§9", name, waitStateFromError)
}

// §10
