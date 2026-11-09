package kit

import (
	"errors"
	"fmt"
	"time"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// §6 a job is its opener's: a second trusted hub runs its own jobs, and every call it makes on
// the first hub's is unknown, as the first's are on its.

func (nodeRun *nodeRun) hubs(session *wire.Session) {
	if nodeRun.target.Other == (wire.Keypair{}) {
		nodeRun.report.Skip("§6", "a job is its opener's", "no --other-private")

		return
	}
	other, dialError := nodeRun.dialOther()
	if !nodeRun.report.Check("§1", "second hub handshake", dialError) {
		return
	}
	defer other.Close()

	first, openError := openJob(session, nodeRun.target.Tag)
	if !nodeRun.report.Check("§6", "first hub opens a job", openError) {
		return
	}
	defer func() { _ = session.Do(wire.CALL_DROP, wire.JobRef{Job: first}, nil) }()

	own, openError := openJob(other, nodeRun.target.Tag)
	if !nodeRun.report.Check("§6", "second hub opens its own job", openError) {
		return
	}
	defer func() { _ = other.Do(wire.CALL_DROP, wire.JobRef{Job: own}, nil) }()
	runError := other.Do(wire.CALL_EXEC, wire.Exec{Job: own, Cmd: "echo", Args: []string{"other"}}, nil)
	if runError == nil {
		runError = waitDone(other, own, nodeRun.target.Wait)
	}
	nodeRun.report.Check("§6", "second hub runs its own job", runError)

	for _, each := range []struct {
		name string
		call func(*wire.Session, string) error
	}{
		{"stat", func(on *wire.Session, job string) error { return on.Do(wire.CALL_STAT, wire.JobRef{Job: job}, nil) }},
		{"exec", func(on *wire.Session, job string) error {
			return on.Do(wire.CALL_EXEC, wire.Exec{Job: job, Cmd: "echo"}, nil)
		}},
		{"kill", func(on *wire.Session, job string) error { return on.Do(wire.CALL_KILL, wire.JobRef{Job: job}, nil) }},
		{"tail", func(on *wire.Session, job string) error { return firstReply(on, wire.CALL_TAIL, wire.Tail{Job: job}) }},
		{"push", func(on *wire.Session, job string) error {
			return firstReply(on, wire.CALL_PUSH, wire.Push{Job: job, Path: "x", ID: randJob()})
		}},
		{"pull", func(on *wire.Session, job string) error {
			return firstReply(on, wire.CALL_PULL, wire.Pull{Job: job, Path: "."})
		}},
		{"drop", func(on *wire.Session, job string) error { return on.Do(wire.CALL_DROP, wire.JobRef{Job: job}, nil) }},
	} {
		nodeRun.report.Check("§6", each.name+" on another hub's job → unknown", wantReject(each.call(other, first), wire.UNKNOWN))
		nodeRun.report.Check("§6", "the first hub "+each.name+" on the second's → unknown", wantReject(each.call(session, own), wire.UNKNOWN))
	}
	var state wire.State
	statError := session.Do(wire.CALL_STAT, wire.JobRef{Job: first}, &state)
	if statError == nil {
		statError = wantState(state, wire.STATE_IDLE)
	}
	nodeRun.report.Check("§6", "a job outlives another hub's calls", statError)
}

// dialOther connects as the second hub, at OtherAddr: the kit reaches the node there from
// another of its IPs.
func (nodeRun *nodeRun) dialOther() (*wire.Session, error) {
	if nodeRun.target.OtherAddr == "" {
		return nil, errors.New("no --other-addr: the second hub needs its own IP")
	}
	connection, connectError := wire.Connect(nodeRun.target.OtherAddr, nodeRun.target.Other, nodeRun.target.NodeKey, nil)
	if connectError != nil {
		return nil, connectError
	}
	session := wire.NewSession(connection)
	go func() { _ = session.Run(nil) }()

	return session, nil
}

// firstReply starts a streaming call and returns its first frame's reject, if any; a call
// that goes on is cancelled.
func firstReply(session *wire.Session, name string, body any) error {
	call, startError := session.Start(name, body)
	if startError != nil {
		return startError
	}
	defer call.Close()

	envelope, nextError := call.Next()
	if nextError != nil {
		return nextError
	}
	if envelope.Reject != nil {
		return envelope.Reject
	}
	_ = call.Cancel()

	return nil
}

// waitDone polls stat until the job is done, or wait passes.
func waitDone(session *wire.Session, job string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		var state wire.State
		statError := session.Do(wire.CALL_STAT, wire.JobRef{Job: job}, &state)
		if statError != nil {
			return statError
		}
		if state.State == wire.STATE_DONE {
			return nil
		}
		if state.State != wire.STATE_RUNNING && state.State != wire.STATE_IDLE {
			return fmt.Errorf("state %q, want done", state.State)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("still %s after %s", state.State, wait)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
