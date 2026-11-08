package hub

import (
	"fmt"
	"io"
	"io/fs"
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
	cfg := fmt.Sprintf(`identity = %q
port = %d
tags = ["kit"]
signal = %s
socket = %q

[hub]
ip = "127.0.0.1"
port = %d
trust = %q

[jobs]
path = %q
limit = 2

[history]
path = %q

[share]
path = %q
limit = "64MB"
`, writeKey(t, dir, "node.key", nk.Private), port, signal, filepath.Join(dir, "s"), signalPort, hubKey.String(),
		filepath.Join(dir, "data", "jobs"), filepath.Join(dir, "data", "history"), filepath.Join(dir, "data", "share")) + strings.ReplaceAll(string(kitCmds), "KIT", cmds)
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
	t.Cleanup(func() { n.Close(); writable(dir) })
	return n, nk.Public, port
}

// writable lets the test's temp dir go: the node's share is read only.
func writable(dir string) {
	filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			os.Chmod(name, 0o755)
		}
		return nil
	})
}

type peerSpec struct {
	key  wire.Key
	port int
}

func newHub(t *testing.T, data string, hk wire.Keypair, signalPort int, peers ...peerSpec) *Hub {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{Identity: writeKey(t, dir, "hub.key", hk.Private), Socket: filepath.Join(dir, "h.sock"),
		Jobs: Jobs{Path: filepath.Join(data, "jobs")}, History: History{Path: filepath.Join(data, "history")}}
	if signalPort != 0 {
		cfg.Signal, cfg.Port = true, signalPort
	}
	for _, p := range peers {
		cfg.Nodes = append(cfg.Nodes, Peer{Tags: []string{"kit"}, IP: "127.0.0.1", Port: p.port, Trust: p.key.String()})
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

// notes are the texts of a job's note events.
func notes(t *testing.T, h *Hub, id string) []string {
	var out []string
	h.Events(id, 0, nil, func(e wire.Event) bool {
		var data map[string]any
		if e.Type == EVENT_NOTE && wire.Unmarshal(e.Data, &data) == nil {
			out = append(out, fmt.Sprint(data["text"]))
		}
		return true
	})
	return out
}

// TestShare: a share push is installed on the node once, then hit; the hub packs a source once,
// until it changes.
func TestShare(t *testing.T) {
	hk, _ := wire.GenerateKeypair(nil)
	_, nk, port := realNode(t, hk.Public, 0)
	h := newHub(t, t.TempDir(), hk, 0, peerSpec{nk, port})

	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "in"), 0o755)
	os.WriteFile(filepath.Join(src, "in", "a.txt"), []byte("alpha"), 0o644)
	job := Job{
		Tag:   "kit",
		Push:  []Push{{From: src, To: "lib/model", Share: true}, {From: src, To: "copy"}},
		Steps: []Step{{Cmd: "write", Args: []string{"out/r.txt", "made"}}},
		Pull:  []Pull{{Path: "out"}},
	}
	run := func(want string) {
		t.Helper()
		id, err := h.Submit(job, "")
		if err != nil {
			t.Fatal(err)
		}
		r := wait(t, h, id, 20*time.Second)
		if r.State != STATE_DONE {
			t.Fatalf("record %+v", r)
		}
		got := strings.Join(notes(t, h, id), "; ")
		if !strings.Contains(got, "push lib/model: "+want) || !strings.Contains(got, "push copy: "+PUSHED_NORMAL) {
			t.Fatalf("notes %s, want push lib/model: %s", got, want)
		}
	}
	run(PUSHED_INSTALLED)
	manifest, _ := Manifest(src)
	if !h.Stored(manifest) {
		t.Fatal("the source is not stored")
	}
	run(PUSHED_HIT)

	// a changed source is a new tar, a new id: installed again
	os.WriteFile(filepath.Join(src, "in", "a.txt"), []byte("beta!"), 0o644)
	if h.Stored(must(Manifest(src))) {
		t.Fatal("a changed source is still stored")
	}
	run(PUSHED_INSTALLED)
	if views := h.Nodes(); !views[0].Share {
		t.Fatalf("the hub does not know the node's share is on: %+v", views[0])
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

// TestShareRules: what a share push may not share its place with.
func TestShareRules(t *testing.T) {
	step := []Step{{Cmd: "echo"}}
	bad := map[string]Job{
		"no to":        {Tag: "kit", Steps: step, Push: []Push{{From: "x", Share: true}}},
		"to the space": {Tag: "kit", Steps: step, Push: []Push{{From: "x", To: ".", Share: true}}},
		"push inside":  {Tag: "kit", Steps: step, Push: []Push{{From: "x", To: "m", Share: true}, {From: "y", To: "m/a"}}},
		"same to":      {Tag: "kit", Steps: step, Push: []Push{{From: "x", To: "m", Share: true}, {From: "y", To: "m"}}},
		"pull all":     {Tag: "kit", Steps: step, Push: []Push{{From: "x", To: "m", Share: true}}, Pull: []Pull{{Path: "."}}},
		"pull inside":  {Tag: "kit", Steps: step, Push: []Push{{From: "x", To: "m", Share: true}}, Pull: []Pull{{Path: "m/a"}}},
	}
	for name, job := range bad {
		if job.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := Job{Tag: "kit", Steps: step, Push: []Push{{From: "x", To: "m", Share: true}, {From: "y", To: "mm"}, {From: "z"}}, Pull: []Pull{{Path: "out"}}}
	if err := good.Check(); err != nil {
		t.Errorf("good: %v", err)
	}

	// a push into the space may not bring an entry where the share's link goes
	hk, _ := wire.GenerateKeypair(nil)
	h := newHub(t, t.TempDir(), hk, 0, peerSpec{hk.Public, freePort(t)})
	share, plain := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(share, "w"), []byte("w"), 0o644)
	os.MkdirAll(filepath.Join(plain, "m"), 0o755)
	os.WriteFile(filepath.Join(plain, "m", "x"), []byte("x"), 0o644)
	_, err := h.Submit(Job{Tag: "kit", Steps: step, Push: []Push{{From: share, To: "m", Share: true}, {From: plain}}}, "")
	if err == nil || !strings.Contains(err.Error(), "a share") {
		t.Fatalf("got %v", err)
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
