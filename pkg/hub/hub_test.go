package hub

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diphyx/grid/pkg/kit"
	"github.com/diphyx/grid/pkg/node"
	"github.com/diphyx/grid/pkg/wire"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func writeKey(t *testing.T, dir, name string, k wire.Key) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(k.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// realNode starts a node with the kit cmds that trusts hubKey; signalPort 0 = no signal.
func realNode(t *testing.T, hubKey wire.Key, signalPort int) (*node.Node, wire.Key, int) {
	t.Helper()
	dir := t.TempDir()
	nk, _ := wire.GenerateKeypair(nil)
	cmds, _ := filepath.Abs("../kit/cmds")
	port := freePort(t)
	signal := "false"
	if signalPort != 0 {
		signal = "true"
	}
	kitCmds, _ := os.ReadFile(filepath.Join(cmds, "node.toml"))
	cfg := fmt.Sprintf(`private = %q
port = %d
tags = ["kit"]
slots = 2
signal = %s
data = %q
socket = %q

[hub]
ip = "127.0.0.1"
port = %d
key = %q
`, writeKey(t, dir, "node.key", nk.Private), port, signal, filepath.Join(dir, "data"), filepath.Join(dir, "s"), signalPort, hubKey.String()) + strings.ReplaceAll(string(kitCmds), "KIT", cmds)
	path := filepath.Join(dir, "node.toml")
	os.WriteFile(path, []byte(cfg), 0o644)
	c, err := node.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(c, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Listen(); err != nil {
		t.Fatal(err)
	}
	go n.Serve()
	t.Cleanup(func() { n.Close() })
	return n, nk.Public, port
}

type peerSpec struct {
	key  wire.Key
	port int
}

func newHub(t *testing.T, data string, hk wire.Keypair, signalPort int, peers ...peerSpec) *Hub {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{Private: writeKey(t, dir, "hub.key", hk.Private), Data: data, Socket: filepath.Join(dir, "h.sock")}
	if signalPort != 0 {
		cfg.Signal, cfg.Port = true, signalPort
	}
	for _, p := range peers {
		cfg.Nodes = append(cfg.Nodes, Peer{Tags: []string{"kit"}, IP: "127.0.0.1", Port: p.port, Key: p.key.String()})
	}
	h, err := New(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func wait(t *testing.T, h *Hub, id string, d time.Duration) Record {
	t.Helper()
	done := make(chan Record, 1)
	go func() { r, _ := h.Wait(id); done <- r }()
	select {
	case r := <-done:
		return r
	case <-time.After(d):
		r, _ := h.Job(id)
		t.Fatalf("job still %s after %s: %+v", r.State, d, r)
	}
	return Record{}
}

func types(t *testing.T, h *Hub, id string) []string {
	var out []string
	h.Events(id, 0, nil, func(e wire.Event) bool { out = append(out, e.Type); return true })
	return out
}

func TestRun(t *testing.T) {
	hk, _ := wire.GenerateKeypair(nil)
	_, nk, port := realNode(t, hk.Public, 0)
	h := newHub(t, t.TempDir(), hk, 0, peerSpec{nk, port})

	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "in"), 0o755)
	os.WriteFile(filepath.Join(src, "in", "a.txt"), []byte("alpha"), 0o644)
	id, err := h.Submit(Job{
		Tag:   "kit",
		Push:  []Push{{From: src, To: "src"}},
		Steps: []Step{{Cmd: "write", Args: []string{"out/r.txt", "made"}}, {Cmd: "echo", Args: []string{"hi"}}},
		Pull:  []Pull{{Path: "out"}, {Path: "result.json"}, {Path: "src"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	r := wait(t, h, id, 20*time.Second)
	if r.State != STATE_DONE || r.Steps[0].State != wire.STATE_DONE || r.Steps[1].State != wire.STATE_DONE {
		t.Fatalf("record %+v", r)
	}
	v, _ := h.View(id)
	var names []string
	for _, f := range v.Files {
		names = append(names, f.Path)
	}
	got := strings.Join(names, ",")
	if got != "out/r.txt,result.json,src/in/a.txt" {
		t.Fatalf("files %s", got)
	}
	ts := strings.Join(types(t, h, id), ",")
	for _, want := range []string{"job", "note", "state", "log"} {
		if !strings.Contains(ts, want) {
			t.Errorf("no %s event in %s", want, ts)
		}
	}
}

func TestFailoverAndFail(t *testing.T) {
	hk, _ := wire.GenerateKeypair(nil)
	_, nk, port := realNode(t, hk.Public, 0)
	dead, _ := wire.GenerateKeypair(nil)
	// the first node is not there; open fails over to the second
	h := newHub(t, t.TempDir(), hk, 0, peerSpec{dead.Public, freePort(t)}, peerSpec{nk, port})
	id, err := h.Submit(Job{Tag: "kit", Steps: []Step{{Cmd: "echo"}, {Cmd: "fail"}, {Cmd: "echo"}}, Pull: []Pull{{Path: "x"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	r := wait(t, h, id, 20*time.Second)
	if r.State != STATE_FAILED || !strings.Contains(r.Message, "boom") || r.Node != wire.Addr("127.0.0.1", port) {
		t.Fatalf("record %+v", r)
	}
	if r.Steps[2].State != wire.STATE_IDLE {
		t.Fatalf("step after the failure ran: %+v", r.Steps)
	}
}

// TestUnknownCmd: a cmd the node does not offer fails the job; it is not a lost job to retry.
func TestUnknownCmd(t *testing.T) {
	hk, _ := wire.GenerateKeypair(nil)
	_, nk, port := realNode(t, hk.Public, 0)
	h := newHub(t, t.TempDir(), hk, 0, peerSpec{nk, port})
	id, err := h.Submit(Job{Tag: "kit", Steps: []Step{{Cmd: "nosuch"}}, Retries: 2}, "")
	if err != nil {
		t.Fatal(err)
	}
	r := wait(t, h, id, 20*time.Second)
	if r.State != STATE_FAILED || r.Attempt != 1 || !strings.Contains(r.Message, "nosuch") {
		t.Fatalf("record %+v", r)
	}
}

func TestKillAndDrop(t *testing.T) {
	hk, _ := wire.GenerateKeypair(nil)
	_, nk, port := realNode(t, hk.Public, 0)
	h := newHub(t, t.TempDir(), hk, 0, peerSpec{nk, port})
	id, _ := h.Submit(Job{Tag: "kit", Steps: []Step{{Cmd: "sleep", Args: []string{"60"}}}}, "")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := h.Job(id)
		if r.Steps[0].State == wire.STATE_RUNNING {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := h.Kill(id); err != nil {
		t.Fatal(err)
	}
	r := wait(t, h, id, 20*time.Second)
	if r.State != STATE_STOPPED {
		t.Fatalf("record %+v", r)
	}
	if err := h.Drop(id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Job(id); err != ErrUnknown {
		t.Fatalf("dropped job still there: %v", err)
	}
}

func TestLostNodeRerun(t *testing.T) {
	old := callLost
	callLost = 2 * time.Second
	defer func() { callLost = old }()
	hk, _ := wire.GenerateKeypair(nil)
	n1, nk1, p1 := realNode(t, hk.Public, 0)
	_, nk2, p2 := realNode(t, hk.Public, 0)
	h := newHub(t, t.TempDir(), hk, 0, peerSpec{nk1, p1}, peerSpec{nk2, p2})
	id, _ := h.Submit(Job{Tag: "kit", Steps: []Step{{Cmd: "sleep", Args: []string{"3"}}}, Retries: 1}, "")
	deadline := time.Now().Add(10 * time.Second)
	var first string
	for time.Now().Before(deadline) {
		r, _ := h.Job(id)
		if r.Steps[0].State == wire.STATE_RUNNING {
			first = r.Node
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if first != wire.Addr("127.0.0.1", p1) {
		// the hub chose node 2; this test needs node 1 to be the one that dies
		t.Skipf("job went to %s first", first)
	}
	n1.Close()
	r := wait(t, h, id, 30*time.Second)
	if r.State != STATE_DONE || r.Attempt != 2 || r.Node != wire.Addr("127.0.0.1", p2) {
		t.Fatalf("record %+v", r)
	}
}

func TestRestartResumes(t *testing.T) {
	hk, _ := wire.GenerateKeypair(nil)
	_, nk, port := realNode(t, hk.Public, 0)
	data := t.TempDir()
	h := newHub(t, data, hk, 0, peerSpec{nk, port})
	id, _ := h.Submit(Job{Tag: "kit", Steps: []Step{{Cmd: "sleep", Args: []string{"2"}}, {Cmd: "echo", Args: []string{"after"}}}}, "")
	time.Sleep(700 * time.Millisecond)
	h.Close()
	time.Sleep(200 * time.Millisecond)
	h2 := newHub(t, data, hk, 0, peerSpec{nk, port})
	r := wait(t, h2, id, 20*time.Second)
	if r.State != STATE_DONE || r.Attempt != 1 {
		t.Fatalf("record %+v", r)
	}
}

// TestKit runs the hub against the kit's fake node: every frame the hub sends is checked.
func TestKit(t *testing.T) {
	hk, _ := wire.GenerateKeypair(nil)
	nk, _ := wire.GenerateKeypair(nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sigPort := freePort(t)
	r := kit.NewReport(io.Discard, false)
	f := &kit.FakeNode{Key: nk, Peers: wire.Peers{"127.0.0.1": hk.Public}, Report: r, Hub: fmt.Sprintf("127.0.0.1:%d", sigPort), HubKey: hk.Public}
	go f.Serve(ln)
	h := newHub(t, t.TempDir(), hk, sigPort, peerSpec{nk.Public, ln.Addr().(*net.TCPAddr).Port})

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	id, err := h.Submit(Job{Tag: "kit", Push: []Push{{From: src}}, Steps: []Step{{Cmd: "write", Args: []string{"out/r.txt", "x"}}, {Cmd: "echo"}}, Pull: []Pull{{Path: "out"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := wait(t, h, id, 20*time.Second)
	if rec.State != STATE_DONE {
		t.Fatalf("record %+v", rec)
	}
	time.Sleep(6 * time.Second) // one beat
	f.Close()
	f.Summary(r)
	for _, x := range r.All {
		if !x.Pass {
			t.Errorf("%s %s: %s", x.Spec, x.Name, x.Note)
		}
	}
	if !h.Nodes()[0].Signal {
		t.Errorf("no beat seen: %+v", h.Nodes()[0])
	}
}

// TestKillDuringPush: a kill mid-transfer stops the job there; no step runs.
func TestKillDuringPush(t *testing.T) {
	hk, _ := wire.GenerateKeypair(nil)
	_, nk, port := realNode(t, hk.Public, 0)
	h := newHub(t, t.TempDir(), hk, 0, peerSpec{nk, port})
	src := t.TempDir()
	big := make([]byte, 48<<20)
	os.WriteFile(filepath.Join(src, "big.bin"), big, 0o644)
	id, err := h.Submit(Job{Tag: "kit", Push: []Push{{From: src}}, Steps: []Step{{Cmd: "echo"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := h.Job(id)
		if r.State == STATE_RUNNING {
			break
		}
		time.Sleep(time.Millisecond)
	}
	h.Kill(id)
	r := wait(t, h, id, 20*time.Second)
	if r.State != STATE_STOPPED || r.Steps[0].State != wire.STATE_IDLE {
		t.Fatalf("record %+v", r)
	}
	t.Log(r.Message)
}
