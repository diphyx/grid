package kit

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

func (nodeRun *nodeRun) signal(session *wire.Session, collector *signalHub) {
	defer collector.close()

	job, checkError := openJob(session, nodeRun.target.Tag)
	if !nodeRun.report.Check("§6", "open for post", checkError) {
		return
	}
	defer func() { _ = session.Do(wire.CALL_DROP, wire.JobRef{Job: job}, nil) }()

	tail, tailError := startTail(session, job, 0)
	_ = session.Do(wire.CALL_EXEC, wire.Exec{Job: job, Cmd: "echo"}, nil)
	if tail != nil {
		_, _ = tail.waitState(nodeRun.target.Wait)
		_ = tail.call.Cancel()
	}
	deadline := time.Now().Add(nodeRun.target.Wait)
	for time.Now().Before(deadline) {
		beats, posts := collector.snapshot(job)
		if len(beats) > 0 && len(posts) >= 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	beats, posts := collector.snapshot(job)
	checkError = nil
	if len(beats) == 0 {
		checkError = errors.New("no beat")
	} else {
		body := beats[len(beats)-1]
		if !contains(body.Tags, nodeRun.target.Tag) {
			checkError = fmt.Errorf("beat tags %v, want %q", body.Tags, nodeRun.target.Tag)
		} else if nodeRun.target.Slots > 0 && body.Free > uint64(nodeRun.target.Slots) {
			checkError = fmt.Errorf("free %d > slots %d", body.Free, nodeRun.target.Slots)
		}
	}
	nodeRun.report.Check("§6", "beat", checkError)
	checkError = nil
	seen := map[string]bool{}
	for _, post := range posts {
		var state wire.State
		if wire.Unmarshal(post.Event.Data, &state) == nil {
			seen[state.State] = true
		}
	}
	switch {
	case tailError != nil:
		{
			checkError = fmt.Errorf("tail: %w", tailError)
		}
	case !seen[wire.STATE_RUNNING] || !seen[wire.STATE_DONE]:
		{
			checkError = fmt.Errorf("%d posts, want running and done", len(posts))
		}
	default:
		{
			for _, post := range posts {
				event, ok := tail.find(func(event wire.Event) bool { return event.Seq == post.Event.Seq })
				if !ok || !bytes.Equal(event.Data, post.Event.Data) {
					checkError = fmt.Errorf("post seq %d differs from tail", post.Event.Seq)

					break
				}
			}
		}
	}
	nodeRun.report.Check("§6", "post = tail's state events", checkError)
}

// signalHub accepts the node's beat and post.
type signalHub struct {
	listener net.Listener
	mutex    sync.Mutex
	beats    []wire.Beat
	posts    []wire.Post
}

func listenSignal(address string, hub wire.Keypair, node wire.Key) (*signalHub, error) {
	listener, listenError := net.Listen("tcp", address)
	if listenError != nil {
		return nil, listenError
	}
	collector := &signalHub{listener: listener}
	go func() {
		for {
			socket, acceptError := listener.Accept()
			if acceptError != nil {
				return
			}
			go func() {
				connection, serverError := wire.NewServer(socket, hub, wire.Peers{wire.RemoteIP(socket): node}, nil)
				if serverError != nil {
					socket.Close()

					return
				}
				_ = wire.NewSession(connection).Run(collector.handle)
			}()
		}
	}()

	return collector, nil
}

func (signalHub *signalHub) handle(request *wire.Request) {
	switch request.Call {
	case wire.CALL_BEAT:
		{
			var body wire.Beat
			decodeBodyError := wire.DecodeBody(request.Body, &body)
			if decodeBodyError != nil {
				_ = request.Fail(decodeBodyError)

				return
			}
			signalHub.mutex.Lock()
			signalHub.beats = append(signalHub.beats, body)
			signalHub.mutex.Unlock()
		}
	case wire.CALL_POST:
		{
			var post wire.Post
			decodeBodyError := wire.DecodeBody(request.Body, &post)
			if decodeBodyError != nil {
				_ = request.Fail(decodeBodyError)

				return
			}
			signalHub.mutex.Lock()
			signalHub.posts = append(signalHub.posts, post)
			signalHub.mutex.Unlock()
		}
	default:
		{
			_ = request.Fail(wire.NewReject(wire.INVALID, "call %q", request.Call))

			return
		}
	}
	_ = request.OK(wire.Empty{})
}

func (signalHub *signalHub) snapshot(job string) ([]wire.Beat, []wire.Post) {
	signalHub.mutex.Lock()
	defer signalHub.mutex.Unlock()

	var posts []wire.Post
	for _, post := range signalHub.posts {
		if post.Job == job {
			posts = append(posts, post)
		}
	}

	return append([]wire.Beat(nil), signalHub.beats...), posts
}

func (signalHub *signalHub) close() { signalHub.listener.Close() }
