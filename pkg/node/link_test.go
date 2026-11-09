package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// TestLink: a share linked into a space reads through the link, a symlink or a junction on
// Windows; removing the job removes the link, never the share's files.
func TestLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "share", "model")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(target, "sub", "w.bin"), []byte("weights"), 0o644)
	if err := makeReadOnly(target); err != nil {
		t.Fatal(err)
	}
	defer removeTree(target)

	job, err := newJob(filepath.Join(dir, "job"), Meta{ID: "link", State: wire.State{State: wire.STATE_IDLE}})
	if err != nil {
		t.Fatal(err)
	}
	if err := job.space.MkdirAll("lib", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := linkDir(job.space, job.path("space"), "lib/model", target); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(job.path("space"), "lib", "model", "sub", "w.bin"))
	if err != nil || string(content) != "weights" {
		t.Fatalf("through the link: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(job.path("space"), "lib", "model", "sub")); err != nil {
		t.Fatalf("a folder through the link: %v", err)
	}

	// what a job's remove does, with the link still there
	job.mutex.Lock()
	err = job.remove()
	job.mutex.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job.dir); !os.IsNotExist(err) {
		t.Fatalf("the job is still there: %v", err)
	}
	content, err = os.ReadFile(filepath.Join(target, "sub", "w.bin"))
	if err != nil || string(content) != "weights" {
		t.Fatalf("removing the job reached into the share: %q %v", content, err)
	}

	// a read only share can be removed
	if err := removeTree(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the share is still there: %v", err)
	}
}
