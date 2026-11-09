package kit

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// NodeTarget is a node under test. The node must trust Hub's public key
// for the kit's IP, serve Tag, and define the cmds in pkg/kit/cmds/node.toml.
type NodeTarget struct {
	Addr    string       // ip:port of the node
	NodeKey wire.Key     // the node's public key
	Hub     wire.Keypair // the kit's key pair, as the hub
	Tag     string       // default "kit"
	Limit   int          // the node's [jobs] limit; 0 = skip the full check
	Share   bool         // the node's share is on: run the share checks
	Listen  string       // ip:port to accept beat and post on; "" = skip signal checks
	Wait    time.Duration
}

type nodeRun struct {
	target NodeTarget
	report *Report
}

// TestNode runs every node check and reports into r.
func TestNode(target NodeTarget, report *Report) {
	if target.Tag == "" {
		target.Tag = "kit"
	}
	if target.Wait == 0 {
		target.Wait = 15 * time.Second
	}
	run := &nodeRun{target: target, report: report}
	var collector *signalHub
	if target.Listen != "" {
		var problem error
		collector, problem = listenSignal(target.Listen, target.Hub, target.NodeKey)
		if !report.Check("§1", "listen for beat and post", problem) {
			collector = nil
		}
	}
	session, connectError := run.dial()
	if !report.Check("§1", "handshake", connectError) {
		return
	}
	defer session.Close()

	run.connection()
	run.envelope(session)
	run.types(session)
	run.open(session)
	run.exec(session)
	run.files(session)
	run.share(session)
	run.drop(session)
	if collector != nil {
		run.signal(session, collector)
	} else {
		report.Skip("§6", "beat and post", "no --listen")
	}
}

func (nodeRun *nodeRun) dial() (*wire.Session, error) {
	connection, connectError := wire.Connect(nodeRun.target.Addr, nodeRun.target.Hub, nodeRun.target.NodeKey, nil)
	if connectError != nil {
		return nil, connectError
	}
	session := wire.NewSession(connection)
	session.OnBad = func(envelope *wire.Envelope, problem error) {
		nodeRun.report.Check("§4", "frame from the node", fmt.Errorf("%v: %+v", problem, envelope))
	}
	go func() { _ = session.Run(nil) }()

	return session, nil
}

// §1

func (nodeRun *nodeRun) connection() {
	_, connectError := wire.Connect(nodeRun.target.Addr, nodeRun.target.Hub, nodeRun.target.NodeKey, &wire.Handshake{Prologue: "grid/0"})
	nodeRun.report.Check("§1", "wrong version closes", wantErr(connectError))
	stranger, _ := wire.GenerateKeypair(nil)
	_, connectError = wire.Connect(nodeRun.target.Addr, stranger, nodeRun.target.NodeKey, nil)
	nodeRun.report.Check("§1", "unknown key closes", wantErr(connectError))
}

func wantErr(problem error) error {
	if problem == nil {
		return errors.New("handshake succeeded")
	}
	return nil
}

// §4

func (nodeRun *nodeRun) envelope(session *wire.Session) {
	pong := make(chan struct{}, 1)
	session.OnPong = func() {
		select {
		case pong <- struct{}{}:
		default:
		}
	}
	_ = session.Send(&wire.Envelope{Ping: &wire.Empty{}})
	select {
	case <-pong:
		nodeRun.report.Check("§4", "ping → pong", nil)
	case <-time.After(5 * time.Second):
		nodeRun.report.Check("§4", "ping → pong", errors.New("no pong in 5 s"))
	}
	nodeRun.report.Check("§4", "unknown call → invalid", wantReject(session.Do("nope", wire.Empty{}, nil), wire.INVALID))

	// two calls at once on one connection
	var group sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		group.Add(1)
		go func() {
			defer group.Done()

			errs[i] = wantReject(session.Do(wire.CALL_STAT, wire.JobRef{Job: randJob()}, nil), wire.UNKNOWN)
		}()
	}
	group.Wait()
	nodeRun.report.Check("§4", "calls overlap", errors.Join(errs...))
}

// §5

func (nodeRun *nodeRun) types(session *wire.Session) {
	nodeRun.report.Check("§5", "missing key → invalid", wantReject(session.Do(wire.CALL_OPEN, wire.Empty{}, nil), wire.INVALID))
	nodeRun.report.Check("§5", "wrong type → invalid", wantReject(session.Do(wire.CALL_OPEN, map[string]any{"tag": 5}, nil), wire.INVALID))
	nodeRun.report.Check("§5", "bad job id → invalid", wantReject(session.Do(wire.CALL_STAT, wire.JobRef{Job: "x"}, nil), wire.INVALID))
	var opened wire.OpenOK
	doError := session.Do(wire.CALL_OPEN, map[string]any{"tag": nodeRun.target.Tag, "zz_unknown": 1}, &opened)
	if nodeRun.report.Check("§5", "unknown keys ignored", doError) {
		_ = session.Do(wire.CALL_DROP, wire.JobRef{Job: opened.Job}, nil)
	}
}

// §6 open, stat, full

func (nodeRun *nodeRun) open(session *wire.Session) {
	nodeRun.report.Check("§6", "open unknown tag → unknown", wantReject(session.Do(wire.CALL_OPEN, wire.Open{Tag: "kit-no-such-tag"}, nil), wire.UNKNOWN))
	job, checkError := openJob(session, nodeRun.target.Tag)
	if !nodeRun.report.Check("§6", "open", checkError) {
		return
	}
	nodeRun.report.Check("§5", "job id is 32 lowercase hex", wire.CheckJob(job))
	var state wire.State
	checkError = session.Do(wire.CALL_STAT, wire.JobRef{Job: job}, &state)
	if checkError == nil && (state.State != wire.STATE_IDLE || state.Cmd != "") {
		checkError = fmt.Errorf("got %+v, want idle without cmd", state)
	}
	nodeRun.report.Check("§7", "stat after open → idle", checkError)
	nodeRun.report.Check("§6", "stat unknown job → unknown", wantReject(session.Do(wire.CALL_STAT, wire.JobRef{Job: randJob()}, nil), wire.UNKNOWN))
	_ = session.Do(wire.CALL_DROP, wire.JobRef{Job: job}, nil)

	if nodeRun.target.Limit == 0 {
		nodeRun.report.Skip("§6", "open past the limit → full", "no --limit")

		return
	}
	var jobs []string
	defer func() {
		for _, jobID := range jobs {
			_ = session.Do(wire.CALL_DROP, wire.JobRef{Job: jobID}, nil)
		}
	}()
	for i := 0; i < nodeRun.target.Limit; i++ {
		jobID, checkError := openJob(session, nodeRun.target.Tag)
		if checkError != nil {
			nodeRun.report.Check("§6", "open past the limit → full", fmt.Errorf("open %d of %d: %w", i+1, nodeRun.target.Limit, checkError))

			return
		}
		jobs = append(jobs, jobID)
	}
	nodeRun.report.Check("§6", "open past the limit → full", wantReject(session.Do(wire.CALL_OPEN, wire.Open{Tag: nodeRun.target.Tag}, nil), wire.FULL))
}

// §6 exec, tail, kill; §7; §8; §9

func (nodeRun *nodeRun) drop(session *wire.Session) {
	job, checkError := openJob(session, nodeRun.target.Tag)
	if !nodeRun.report.Check("§6", "open for drop", checkError) {
		return
	}
	tail, _ := startTail(session, job, 0)
	_ = session.Do(wire.CALL_EXEC, wire.Exec{Job: job, Cmd: "sleep", Args: []string{"60"}}, nil)
	nodeRun.report.Check("§6", "drop while running", session.Do(wire.CALL_DROP, wire.JobRef{Job: job}, nil))
	if tail != nil {
		nodeRun.report.Check("§8", "drop ends tail", tail.waitEnd(nodeRun.target.Wait))
	}
	nodeRun.report.Check("§6", "stat after drop → unknown", wantReject(session.Do(wire.CALL_STAT, wire.JobRef{Job: job}, nil), wire.UNKNOWN))
	nodeRun.report.Check("§6", "drop after drop → unknown", wantReject(session.Do(wire.CALL_DROP, wire.JobRef{Job: job}, nil), wire.UNKNOWN))
}

// §6 beat, post
