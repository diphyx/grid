package node

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/diphyx/grid/pkg/kit"
	"github.com/diphyx/grid/pkg/wire"
)

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// testNode starts a real node with the kit cmds; it returns the node, the kit's hub key, and the signal address.
func testNode(t *testing.T, data string) (*Node, wire.Keypair, string) {
	return testNodeWith(t, data, "0")
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func testNodeWith(t *testing.T, data, expires string) (*Node, wire.Keypair, string) {
	t.Helper()
	hub, _ := wire.GenerateKeypair(nil)
	nodeKey, _ := wire.GenerateKeypair(nil)
	dir := t.TempDir()
	priv := filepath.Join(dir, "node.key")
	os.WriteFile(priv, []byte(nodeKey.Private.String()+"\n"), 0o600)
	cmds, _ := filepath.Abs("../kit/cmds")
	sigPort := freePort(t)
	kitCmds, _ := os.ReadFile(filepath.Join(cmds, "node.toml"))
	cfg := fmt.Sprintf(`private = %q
port = %d
tags = ["kit"]
slots = 2
expires = %q
history = "30d"
signal = true
data = %q

[hub]
ip = "127.0.0.1"
port = %d
key = %q
`, priv, freePort(t), expires, data, sigPort, hub.Public.String()) + strings.ReplaceAll(string(kitCmds), "KIT", cmds)
	path := filepath.Join(dir, "node.toml")
	os.WriteFile(path, []byte(cfg), 0o644)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(c, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Listen(); err != nil {
		t.Fatal(err)
	}
	go n.Serve()
	return n, hub, fmt.Sprintf("127.0.0.1:%d", sigPort)
}

// TestKit runs the whole test kit against the real node.
func TestKit(t *testing.T) {
	n, hub, sig := testNode(t, t.TempDir())
	defer n.Close()
	out := io.Discard
	if testing.Verbose() {
		out = os.Stdout
	}
	r := kit.NewReport(out, false)
	kit.TestNode(kit.NodeTarget{Addr: fmt.Sprintf("127.0.0.1:%d", n.config.Port), NodeKey: n.key.Public, Hub: hub, Slots: 2, Listen: sig, Wait: 15 * time.Second}, r)
	for _, x := range r.All {
		if !x.Pass {
			t.Errorf("%s %s: %s", x.Spec, x.Name, x.Note)
		}
	}
	if len(r.All) < 70 {
		t.Errorf("only %d checks ran", len(r.All))
	}
}

func dialNode(t *testing.T, n *Node, hub wire.Keypair) *wire.Session {
	t.Helper()
	c, err := wire.Connect(fmt.Sprintf("127.0.0.1:%d", n.config.Port), hub, n.key.Public, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := wire.NewSession(c)
	go s.Run(nil)
	return s
}

// TestRestart: jobs survive a restart; a cmd that was running is failed; seq goes on.
func TestRestart(t *testing.T) {
	data := t.TempDir()
	n, hub, _ := testNode(t, data)
	s := dialNode(t, n, hub)
	var ok wire.OpenOK
	if err := s.Do(wire.CALL_OPEN, wire.Open{Tag: "kit"}, &ok); err != nil {
		t.Fatal(err)
	}
	if err := s.Do(wire.CALL_EXEC, wire.Exec{Job: ok.Job, Cmd: "sleep", Args: []string{"30"}}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	s.Close()
	n.Close()
	time.Sleep(300 * time.Millisecond)

	// same data, same keys: reuse the config
	n2, err := New(n.config, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := n2.Listen(); err != nil {
		t.Fatal(err)
	}
	go n2.Serve()
	defer n2.Close()
	s2 := dialNode(t, n2, hub)
	defer s2.Close()
	var st wire.State
	if err := s2.Do(wire.CALL_STAT, wire.JobRef{Job: ok.Job}, &st); err != nil {
		t.Fatal(err)
	}
	if st.State != wire.STATE_FAILED || st.Message != "node restarted" || st.Cmd != "sleep" {
		t.Fatalf("stat %+v", st)
	}
	j := n2.job(ok.Job)
	j.mutex.Lock()
	evs, _ := j.events(0, 100)
	j.mutex.Unlock()
	if len(evs) != 2 || evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("events %+v", evs)
	}
}

// TestExpires: an idle job cleans itself; a running one does not.
func TestExpires(t *testing.T) {
	n, hub, _ := testNodeWith(t, t.TempDir(), "1s")
	defer n.Close()
	s := dialNode(t, n, hub)
	defer s.Close()
	var a, b wire.OpenOK
	s.Do(wire.CALL_OPEN, wire.Open{Tag: "kit"}, &a)
	s.Do(wire.CALL_OPEN, wire.Open{Tag: "kit"}, &b)
	if a.Expires != 1000 {
		t.Fatalf("expires %d, want 1000", a.Expires)
	}
	s.Do(wire.CALL_EXEC, wire.Exec{Job: b.Job, Cmd: "sleep", Args: []string{"5"}}, nil)
	time.Sleep(3 * time.Second)
	if err := s.Do(wire.CALL_STAT, wire.JobRef{Job: a.Job}, nil); !wire.IsReject(err, wire.UNKNOWN) {
		t.Fatalf("idle job still there: %v", err)
	}
	if err := s.Do(wire.CALL_STAT, wire.JobRef{Job: b.Job}, nil); err != nil {
		t.Fatalf("running job expired: %v", err)
	}
	hs := n.history.Read()
	if len(hs) == 0 || hs[len(hs)-1].State != "expired" {
		t.Fatalf("history %+v", hs)
	}
}

// fakeHub accepts beat and post from one node and records the posts.
type fakeHub struct {
	ln       net.Listener
	mu       sync.Mutex
	posts    []wire.Post
	sessions []*wire.Session
}

func listenHub(t *testing.T, addr string, hub wire.Keypair, nodeKey wire.Key) *fakeHub {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHub{ln: ln}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			c, err := wire.NewServer(nc, hub, wire.Peers{"127.0.0.1": nodeKey}, nil)
			if err != nil {
				nc.Close()
				continue
			}
			s := wire.NewSession(c)
			h.mu.Lock()
			h.sessions = append(h.sessions, s)
			h.mu.Unlock()
			go s.Run(func(r *wire.Request) {
				if r.Call == wire.CALL_POST {
					var p wire.Post
					wire.DecodeBody(r.Body, &p)
					h.mu.Lock()
					h.posts = append(h.posts, p)
					h.mu.Unlock()
				}
				r.OK(wire.Empty{})
			})
		}
	}()
	return h
}

func (h *fakeHub) close() {
	h.ln.Close()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		s.Close()
	}
}

func (h *fakeHub) seqs(job string) []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []uint64
	for _, p := range h.posts {
		if p.Job == job {
			out = append(out, p.Event.Seq)
		}
	}
	return out
}

// TestPostsSurviveRestart: posts made while the hub is away arrive after a node restart, in
// order, once; and "hub connected" turns off as soon as the hub goes.
func TestPostsSurviveRestart(t *testing.T) {
	data := t.TempDir()
	n, hub, sig := testNode(t, data)
	s := dialNode(t, n, hub)
	var ok wire.OpenOK
	s.Do(wire.CALL_OPEN, wire.Open{Tag: "kit"}, &ok)
	s.Do(wire.CALL_EXEC, wire.Exec{Job: ok.Job, Cmd: "echo", Args: []string{"x"}}, nil)
	time.Sleep(500 * time.Millisecond) // done; no hub listening, so nothing is acked
	s.Close()
	n.Close()

	h := listenHub(t, sig, hub, n.key.Public)
	defer h.close()
	n2, err := New(n.config, quiet)
	if err != nil {
		t.Fatal(err)
	}
	n2.Listen()
	go n2.Serve()
	defer n2.Close()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(h.seqs(ok.Job)) < 2 {
		time.Sleep(100 * time.Millisecond)
	}
	got := fmt.Sprint(h.seqs(ok.Job))
	if got != "[1 3]" { // running, done; seq 2 is the log line
		t.Fatalf("posts %s, want [1 3]", got)
	}
	if !n2.Status().Online {
		t.Fatal("hub up but status says not connected")
	}
	h.close()
	time.Sleep(500 * time.Millisecond)
	if n2.Status().Online {
		t.Fatal("hub gone but status still says connected")
	}
	time.Sleep(200 * time.Millisecond)
	if got := fmt.Sprint(h.seqs(ok.Job)); got != "[1 3]" {
		t.Fatalf("posts sent again: %s", got)
	}
}

// TestReadLines: long lines split on character boundaries, an exact-size line adds no empty
// one, and text that is not UTF-8 is made so.
func TestReadLines(t *testing.T) {
	long := strings.Repeat("a", MAX_LINE-utf8.UTFMax-1) + "é" + "tail\n"
	exact := strings.Repeat("b", MAX_LINE-utf8.UTFMax) + "\n"
	var got []string
	readLines(strings.NewReader(long+exact+"bad \xff\nlast"), func(text string) { got = append(got, text) })
	for i, text := range got {
		if !utf8.ValidString(text) || len(text) > MAX_LINE {
			t.Fatalf("line %d: %d bytes, valid %v", i, len(text), utf8.ValidString(text))
		}
	}
	want := 2 + 1 + 1 + 1 // long: 2 parts; exact: 1; bad; last
	if len(got) != want || !strings.HasPrefix(got[1], "é") || got[3] != "bad �" || got[4] != "last" {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
}

// TestLeftBehind: a cmd that exits while something it started still runs ends at its exit,
// and what it left behind is stopped, whether it holds the output or not.
func TestLeftBehind(t *testing.T) {
	dir := t.TempDir()
	logs, err := openHistory(filepath.Join(dir, "history"), 0)
	if err != nil {
		t.Fatal(err)
	}
	self := &Node{history: logs, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	job, err := newJob(filepath.Join(dir, "job"), Meta{ID: "left", State: wire.State{State: wire.STATE_IDLE}})
	if err != nil {
		t.Fatal(err)
	}
	marker, quiet := filepath.Join(dir, "still-here"), filepath.Join(dir, "quiet-here")
	script := "(sleep 3; touch " + marker + ") & (sleep 3; touch " + quiet + ") >/dev/null 2>&1 & echo started"
	began := time.Now()
	job.mutex.Lock()
	self.start(job, wire.Exec{Cmd: "left"}, Cmd{Exec: []string{"/bin/sh", "-c", script}})
	job.mutex.Unlock()
	for {
		job.mutex.Lock()
		state := job.meta.State.State
		job.mutex.Unlock()
		if state == wire.STATE_DONE {
			break
		}
		if time.Since(began) > 2500*time.Millisecond {
			t.Fatalf("still %s: the step waited for what the cmd left behind", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(3 * time.Second)
	for _, left := range []string{marker, quiet} {
		if _, statError := os.Stat(left); statError == nil {
			t.Fatal("what the cmd left behind kept running:", filepath.Base(left))
		}
	}
}
