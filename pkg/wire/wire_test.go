package wire

import (
	"archive/tar"
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func pair(t *testing.T, hc, hs *Handshake, peers func(cli Keypair) Peers) (*Conn, *Conn, error, error) {
	t.Helper()
	cli, _ := GenerateKeypair(nil)
	srv, _ := GenerateKeypair(nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type res struct {
		c   *Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			ch <- res{nil, err}
			return
		}
		c, err := NewServer(nc, srv, peers(cli), hs)
		if err != nil {
			nc.Close()
		}
		ch <- res{c, err}
	}()
	c, cerr := Connect(ln.Addr().String(), cli, srv.Public, hc)
	r := <-ch
	return c, r.c, cerr, r.err
}

func allow(cli Keypair) Peers { return Peers{"127.0.0.1": cli.Public} }

func TestHandshake(t *testing.T) {
	c, s, cerr, serr := pair(t, nil, nil, allow)
	if cerr != nil || serr != nil {
		t.Fatal(cerr, serr)
	}
	defer c.Close()
	defer s.Close()
	if err := c.Send(&Envelope{ID: 1, Call: CALL_STAT, Body: MustRaw(JobRef{Job: "x"})}); err != nil {
		t.Fatal(err)
	}
	e, err := s.Recv()
	if err != nil || e.Call != CALL_STAT || e.ID != 1 {
		t.Fatal(e, err)
	}
	if err := s.Send(&Envelope{ID: 1, OK: MustRaw(Empty{})}); err != nil {
		t.Fatal(err)
	}
	if e, err := c.Recv(); err != nil || e.Kind() != KIND_OK {
		t.Fatal(e, err)
	}
}

func TestHandshakeRejects(t *testing.T) {
	if _, _, _, serr := pair(t, &Handshake{Prologue: "grid/0"}, nil, allow); serr == nil {
		t.Fatal("prologue mismatch accepted")
	}
	other, _ := GenerateKeypair(nil)
	if _, _, _, serr := pair(t, nil, nil, func(Keypair) Peers { return Peers{"127.0.0.1": other.Public} }); serr != ErrPeerKey {
		t.Fatal("wrong key accepted:", serr)
	}
	if _, _, _, serr := pair(t, nil, nil, func(Keypair) Peers { return Peers{"10.9.9.9": other.Public} }); serr != ErrPeerIP {
		t.Fatal("wrong ip accepted:", serr)
	}
}

func TestSession(t *testing.T) {
	c, s, cerr, serr := pair(t, nil, nil, allow)
	if cerr != nil || serr != nil {
		t.Fatal(cerr, serr)
	}
	cs, ss := NewSession(c), NewSession(s)
	go cs.Run(nil)
	go ss.Run(func(r *Request) {
		var b JobRef
		if err := DecodeBody(r.Body, &b); err != nil {
			r.Fail(err)
			return
		}
		r.OK(State{State: STATE_IDLE})
	})
	defer cs.Close()
	var st State
	if err := cs.Do(CALL_STAT, JobRef{Job: "0123456789abcdef0123456789abcdef"}, &st); err != nil || st.State != STATE_IDLE {
		t.Fatal(st, err)
	}
	if err := cs.Do(CALL_STAT, JobRef{Job: "bad"}, nil); !IsReject(err, INVALID) {
		t.Fatal("want invalid, got", err)
	}
	// unknown keys are ignored
	if err := cs.Do(CALL_STAT, map[string]any{"job": "0123456789abcdef0123456789abcdef", "zz": 1}, &st); err != nil {
		t.Fatal(err)
	}
}

func TestEnvelopeCheck(t *testing.T) {
	bad := []*Envelope{
		{},
		{ID: 1},
		{ID: 1, OK: MustRaw(Empty{}), End: MustRaw(Empty{})},
		{Call: "stat", Body: MustRaw(Empty{})},
		{ID: 1, Call: "stat"},
		{ID: 1, Ping: &Empty{}},
	}
	for i, e := range bad {
		if e.Check() == nil {
			t.Errorf("%d: accepted", i)
		}
	}
	good := []*Envelope{
		{ID: 1, Call: "stat", Body: MustRaw(Empty{})},
		{ID: 1, OK: MustRaw(Empty{})},
		{Ping: &Empty{}},
		{ID: 2, Cancel: &Empty{}},
	}
	for i, e := range good {
		b, _ := Marshal(e)
		var d Envelope
		if err := Unmarshal(b, &d); err != nil || d.Check() != nil {
			t.Errorf("%d: %v %v", i, err, d.Check())
		}
	}
}

func TestPaths(t *testing.T) {
	for _, p := range []string{"a", "a/b", ".", "a/./b"} {
		if CheckPath(p) != nil {
			t.Error("rejected", p)
		}
	}
	for _, p := range []string{"", "/a", "../a", "a/../../b", "a\\b"} {
		if CheckPath(p) == nil {
			t.Error("accepted", p)
		}
	}
}

func TestTar(t *testing.T) {
	m := time.Unix(1700000000, 0)
	es := []Entry{{Path: "b.txt", Data: []byte("b"), ModTime: m}, {Path: "a", Dir: true, ModTime: m}, {Path: "a/x", Data: []byte("x"), Exec: true, ModTime: m}}
	b1, b2 := TarBytes(es), TarBytes([]Entry{es[2], es[0], es[1]})
	if !bytes.Equal(b1, b2) {
		t.Fatal("not deterministic")
	}
	got, err := ReadTar(bytes.NewReader(b1))
	if err != nil || len(got) != 3 || got[0].Path != "a" || string(got[2].Data) != "b" || !got[1].Exec {
		t.Fatal(got, err)
	}
	for _, h := range []*tar.Header{
		{Name: "../x", Typeflag: tar.TypeReg},
		{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "a/l", Typeflag: tar.TypeSymlink, Linkname: "../../x"},
		{Name: "h", Typeflag: tar.TypeLink, Linkname: "a"},
		{Name: "f", Typeflag: tar.TypeFifo},
	} {
		if CheckHeader(h) == nil {
			t.Error("accepted", h.Name)
		}
	}
	if CheckHeader(&tar.Header{Name: "a/l", Typeflag: tar.TypeSymlink, Linkname: "../b"}) != nil {
		t.Error("rejected inside link")
	}
}

// TestExtractLinks: a symlink from the same tar, or one already there, cannot carry an
// entry out of the root.
func TestExtractLinks(t *testing.T) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, header := range []*tar.Header{
		{Name: "d1/d2/s", Typeflag: tar.TypeSymlink, Linkname: "../.."},
		{Name: "d1/d2/s/l", Typeflag: tar.TypeSymlink, Linkname: "../outside"},
	} {
		if writer.WriteHeader(header) != nil {
			t.Fatal("header")
		}
	}
	_ = writer.Close()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := Extract(bytes.NewReader(buffer.Bytes()), root, "."); err == nil {
		t.Fatal("accepted a link through a link")
	}
	if _, err := os.Lstat(dir + "/d1"); err == nil {
		t.Fatal("wrote before rejecting")
	}

	if root.Symlink(".", "here") != nil {
		t.Fatal("symlink")
	}
	inside := TarBytes([]Entry{{Path: "here/x", Data: []byte("x"), ModTime: time.Unix(1700000000, 0)}})
	if _, err := Extract(bytes.NewReader(inside), root, "."); err == nil {
		t.Fatal("accepted an entry through an existing link")
	}
}

// TestLoadKey: a key that starts with "/" is still a key, not a path.
func TestLoadKey(t *testing.T) {
	for i := 0; i < 2000; i++ {
		k, _ := GenerateKeypair(nil)
		got, err := LoadKey(k.Public.String())
		if err != nil || got != k.Public {
			t.Fatalf("%s: %v", k.Public, err)
		}
	}
	dir := t.TempDir()
	k, _ := GenerateKeypair(nil)
	p := dir + "/k.pub"
	os.WriteFile(p, []byte(k.Public.String()+"\n"), 0o644)
	if got, err := LoadKey(p); err != nil || got != k.Public {
		t.Fatalf("path: %v", err)
	}
	if _, err := LoadKey(dir + "/missing"); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestParseEventLine(t *testing.T) {
	good := map[string]struct {
		name  string
		pairs map[string]string
	}{
		`[started]`:             {"started", nil},
		`[progress] percent=50`: {"progress", map[string]string{"percent": "50"}},
		`[stage] name=compile note="half way there"`: {"stage", map[string]string{"name": "compile", "note": "half way there"}},
		`[q] say="a \"quoted\" word" path=C:\x`:      {"q", map[string]string{"say": `a "quoted" word`, "path": `C:\x`}},
		`[log] line=ok`:                              {"log", map[string]string{"line": "ok"}},
		`[a.b-c_d]   k=v   `:                         {"a.b-c_d", map[string]string{"k": "v"}},
		`[e] empty=""`:                               {"e", map[string]string{"empty": ""}},
	}
	for line, want := range good {
		name, pairs, err := ParseEventLine(line)
		if err != nil || name != want.name || fmt.Sprint(pairs) != fmt.Sprint(want.pairs) {
			t.Errorf("%s: got %q %v %v", line, name, pairs, err)
		}
	}
	bad := []string{
		``, `progress percent=50`, `compiling main.go`, `[Progress] x=1`, `[] x=1`,
		`[progress]percent=50`, `[progress] percent`, `[progress] percent=50 oops`,
		`[p] note="open`, `[p] a="x"b=1`, `[p] a=x"y`, `[p] Key=1`, `[` + strings.Repeat("a", 65) + `]`,
		`[p] x=` + strings.Repeat("a", EVENT_LINE_SIZE),
	}
	for _, line := range bad {
		if _, _, err := ParseEventLine(line); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
}

func TestSize(t *testing.T) {
	good := map[string]int64{"0": 0, "512B": 512, "1KB": KB, "100MB": 100 * MB, "10GB": 10 * GB, "2TB": 2 * TB}
	for text, want := range good {
		got, err := ParseSize(text)
		if err != nil || got != want {
			t.Errorf("%s: got %d %v, want %d", text, got, err, want)
		}
		if text != "0" && (Size{Bytes: want}).String() != text {
			t.Errorf("%s: string %s", text, Size{Bytes: want})
		}
	}
	for _, text := range []string{"", "5", "-1GB", "1.5GB", "GB", "10gb", "10 GiB"} {
		if _, err := ParseSize(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	var config struct {
		Limit Size `toml:"limit"`
	}
	if err := DecodeTOML([]byte("limit = 10\n"), &config); err == nil {
		t.Error("a bare number accepted as a size")
	}
	if err := DecodeTOML([]byte("limit = \"10GB\"\n"), &config); err != nil || config.Limit.Bytes != 10*GB {
		t.Errorf("got %v %v", config.Limit, err)
	}
}

func TestPushCheck(t *testing.T) {
	job, id := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, push := range []Push{{Job: job}, {Job: job, Path: "x"}, {Job: job, Path: "a/b", ID: id}} {
		if err := push.Check(); err != nil {
			t.Errorf("%+v: %v", push, err)
		}
	}
	for _, push := range []Push{{Job: job, ID: id}, {Job: job, Path: ".", ID: id}, {Job: job, Path: "x", ID: "B"}, {Job: job, Path: "../x"}} {
		if !IsReject(push.Check(), INVALID) {
			t.Errorf("%+v accepted", push)
		}
	}
}
