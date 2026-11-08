package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The CLI end to end: build `grid`, run a node and a hub as real processes, drive both with
// the CLI the way a person would.

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "grid-cli-")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "grid")
	build := exec.Command("go", "build", "-o", bin, "..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// grid runs the CLI and returns its output and exit code.
func grid(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if exitError, ok := err.(*exec.ExitError); ok {
		code = exitError.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func mustGrid(t *testing.T, args ...string) string {
	t.Helper()
	out, code := grid(t, args...)
	if code != 0 {
		t.Fatalf("grid %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

// daemon starts `grid <side> run` and stops it at the end of the test.
func daemon(t *testing.T, socket string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	logFile, _ := os.Create(filepath.Join(t.TempDir(), "log"))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(logFile.Name())
	t.Fatalf("%s did not start:\n%s", strings.Join(args, " "), b)
}

func TestHubAndNode(t *testing.T) {
	// short paths: a Unix socket path has a small limit on macOS
	dir, err := os.MkdirTemp("/tmp", "gc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// keys the way a person makes them
	for _, name := range []string{"hub", "node"} {
		public := strings.TrimSpace(mustGrid(t, "key", "new", filepath.Join(dir, name)))
		if got := strings.TrimSpace(mustGrid(t, "key", "pub", filepath.Join(dir, name+".key"))); got != public {
			t.Fatalf("key pub %s, key new said %s", got, public)
		}
		info, _ := os.Stat(filepath.Join(dir, name+".key"))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s.key mode %v", name, info.Mode().Perm())
		}
	}
	if out, code := grid(t, "key", "new", filepath.Join(dir, "hub")); code == 0 || !strings.Contains(out, "not overwritten") {
		t.Fatalf("key new overwrote:\n%s", out)
	}
	cmds, _ := filepath.Abs("../pkg/kit/cmds")
	nodePort, hubPort := freePort(t), freePort(t)
	nodeSock, hubSock := filepath.Join(dir, "n.sock"), filepath.Join(dir, "h.sock")

	node := fmt.Sprintf(`identity = "%[1]s/node.key"
port = %[2]d
tags = ["kit"]
signal = true
socket = %[3]q

[hub]
ip = "127.0.0.1"
port = %[4]d
trust = "%[1]s/hub.pub"

[jobs]
path = "%[1]s/ndata/jobs"
limit = 2

[history]
path = "%[1]s/ndata/history"

[share]
path = "%[1]s/ndata/share"
limit = "64MB"
`, dir, nodePort, nodeSock, hubPort) + strings.ReplaceAll(mustRead(t, filepath.Join(cmds, "node.toml")), "KIT", cmds)
	os.WriteFile(filepath.Join(dir, "node.toml"), []byte(node), 0o644)
	hub := fmt.Sprintf(`identity = "%[1]s/hub.key"
signal = true
port = %[2]d
socket = %[3]q

[jobs]
path = "%[1]s/hdata/jobs"

[history]
path = "%[1]s/hdata/history"

[[nodes]]
tags = ["kit"]
ip = "127.0.0.1"
port = %[4]d
trust = "%[1]s/node.pub"
share = true
`, dir, hubPort, hubSock, nodePort)
	os.WriteFile(filepath.Join(dir, "hub.toml"), []byte(hub), 0o644)

	os.MkdirAll(filepath.Join(dir, "job", "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "job", "src", "in.txt"), []byte("in"), 0o644)
	os.WriteFile(filepath.Join(dir, "job", "build.toml"), []byte(`tag = "kit"

[[push]]
from = "./src"
to = "src"

[[steps]]
cmd = "write"
args = ["out/report.txt", "all good"]

[[steps]]
cmd = "echo"
args = ["done"]

[[pull]]
path = "out"

[[pull]]
path = "result.json"
`), 0o644)
	os.WriteFile(filepath.Join(dir, "job", "share.toml"), []byte(`tag = "kit"

[[push]]
from = "./src"
to = "model"
share = true

[[steps]]
cmd = "echo"
`), 0o644)
	os.WriteFile(filepath.Join(dir, "job", "sleep.toml"), []byte("tag = \"kit\"\n\n[[steps]]\ncmd = \"sleep\"\nargs = [\"30\"]\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "job", "fail.toml"), []byte("tag = \"kit\"\n\n[[steps]]\ncmd = \"fail\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "job", "bad.toml"), []byte("tag = \"kit\"\nretry = 2\n\n[[steps]]\ncmd = \"echo\"\n"), 0o644)

	daemon(t, nodeSock, "node", "run", "--config", filepath.Join(dir, "node.toml"))
	daemon(t, hubSock, "hub", "run", "--config", filepath.Join(dir, "hub.toml"))
	h := "--socket=" + hubSock

	// submit --wait follows the job and exits 0 on done
	out := mustGrid(t, "hub", "submit", filepath.Join(dir, "job", "build.toml"), "--wait", h)
	for _, want := range []string{"job  queued", "push src: normal", "hub  pushed", "cmd  done write", "out  done", "hub  pulled", "job  done"} {
		if !strings.Contains(out, want) {
			t.Errorf("submit --wait: no %q in\n%s", want, out)
		}
	}
	id := strings.SplitN(out, "\n", 2)[0]

	// views
	if out := mustGrid(t, "hub", "jobs", h); !strings.Contains(out, id) || !strings.Contains(out, "done") {
		t.Errorf("jobs:\n%s", out)
	}

	// a share push: installed on the node once, then hit; the hub packs it once
	if out := mustGrid(t, "hub", "submit", filepath.Join(dir, "job", "share.toml"), "--wait", h); !strings.Contains(out, "push model: installed") {
		t.Errorf("first share job:\n%s", out)
	}
	if out := mustGrid(t, "hub", "submit", filepath.Join(dir, "job", "share.toml"), "--wait", h); !strings.Contains(out, "push model: hit") {
		t.Errorf("second share job:\n%s", out)
	}
	if out := mustGrid(t, "hub", "submit", filepath.Join(dir, "job", "share.toml"), "--wait", "--repack", h); !strings.Contains(out, "push model: hit") {
		t.Errorf("repacked, unchanged:\n%s", out)
	}
	if out := mustGrid(t, "hub", "history", "--kind", "job", h); !strings.Contains(out, "submitted") || !strings.Contains(out, id) {
		t.Errorf("hub history:\n%s", out)
	}
	if out := mustGrid(t, "hub", "job", id, h); !strings.Contains(out, "out/report.txt") || !strings.Contains(out, "write  done") {
		t.Errorf("job:\n%s", out)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(mustGrid(t, "hub", "job", id, "--json", h)), &view); err != nil || view["state"] != "done" {
		t.Errorf("job --json: %v %v", err, view["state"])
	}
	if out := mustGrid(t, "hub", "tail", id, h); !strings.Contains(out, "job  done") {
		t.Errorf("tail:\n%s", out)
	}
	if out := mustGrid(t, "hub", "tail", id, "--after", "12", h); strings.Contains(out, "queued") || !strings.Contains(out, "job  done") {
		t.Errorf("tail --after:\n%s", out)
	}

	// pull copies the files out
	mustGrid(t, "hub", "pull", id, filepath.Join(dir, "got"), h)
	if got := mustRead(t, filepath.Join(dir, "got", "out", "report.txt")); got != "all good" {
		t.Errorf("pulled %q", got)
	}

	// a failed step: --wait exits 1
	if out, code := grid(t, "hub", "submit", filepath.Join(dir, "job", "fail.toml"), "--wait", h); code != 1 || !strings.Contains(out, "boom") {
		t.Errorf("failed job: exit %d\n%s", code, out)
	}

	// kill, then drop
	sleepID := strings.TrimSpace(mustGrid(t, "hub", "submit", filepath.Join(dir, "job", "sleep.toml"), h))
	waitFor(t, func() bool { return strings.Contains(mustGrid(t, "hub", "jobs", h), "running") })
	mustGrid(t, "hub", "kill", sleepID, h)
	waitFor(t, func() bool { return strings.Contains(mustGrid(t, "hub", "job", sleepID, h), "stopped") })
	mustGrid(t, "hub", "drop", sleepID, h)
	if _, code := grid(t, "hub", "job", sleepID, h); code == 0 {
		t.Error("dropped job still shown")
	}

	// status and nodes, once a beat came in
	waitFor(t, func() bool { return strings.Contains(mustGrid(t, "hub", "nodes", h), "online") })
	if out := mustGrid(t, "hub", "status", h); !strings.Contains(out, "1 online of 1") || !strings.Contains(out, "signal  on") {
		t.Errorf("status:\n%s", out)
	}

	// the node's side
	n := "--socket=" + nodeSock
	if out := mustGrid(t, "node", "status", n); !strings.Contains(out, "connected") || strings.Contains(out, "not connected") {
		t.Errorf("node status:\n%s", out)
	}
	if out := mustGrid(t, "node", "history", "--kind", "job", n); !strings.Contains(out, "/write") {
		t.Errorf("node history:\n%s", out)
	}
	if out := mustGrid(t, "node", "history", "--kind", "share", n); !strings.Contains(out, "install") {
		t.Errorf("node history share:\n%s", out)
	}
	if out := mustGrid(t, "node", "status", n); !strings.Contains(out, "share  1, ") {
		t.Errorf("node status share:\n%s", out)
	}

	// bad input
	if out, code := grid(t, "hub", "submit", filepath.Join(dir, "missing.toml"), h); code == 0 {
		t.Errorf("missing job file accepted:\n%s", out)
	}
	if out, code := grid(t, "hub", "submit", filepath.Join(dir, "job", "bad.toml"), h); code == 0 || !strings.Contains(out, "retry") {
		t.Errorf("unknown key in a job file accepted:\n%s", out)
	}
	if out, code := grid(t, "hub", "job", "nope", h); code == 0 {
		t.Errorf("unknown job accepted:\n%s", out)
	}
	if _, code := grid(t, "bogus"); code != 2 {
		t.Errorf("unknown command: exit %d", code)
	}
}

// TestKit: `grid test vectors` matches the committed vectors.
func TestKit(t *testing.T) {
	out := mustGrid(t, "test", "vectors")
	if out != mustRead(t, "../spec/vectors.json") {
		t.Error("grid test vectors differs from spec/vectors.json")
	}
	if _, code := grid(t, "test"); code != 2 {
		t.Errorf("grid test with no mode: exit %d", code)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out")
}
