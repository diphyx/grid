package history

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "history")
	log, err := OpenLog(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	log.Add(Entry{Kind: KIND_JOB, Event: "done", Subject: "a/build", Took: 2 * time.Second})
	log.Add(Entry{Kind: KIND_SHARE, Event: "install", Subject: "b"})
	log.Add(Entry{Kind: KIND_JOB, Event: "drop", Subject: "a", At: time.Now().Add(-2 * time.Hour)})
	if got := log.Read(""); len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	if got := log.Read(KIND_JOB); len(got) != 2 || got[0].Took != 2*time.Second {
		t.Fatalf("job entries: %+v", got)
	}
	log.Prune()
	if got := log.Read(KIND_JOB); len(got) != 1 {
		t.Fatalf("after prune: %+v, want the old one gone", got)
	}
	log.Close()

	// reopened, the entries are still there
	log, err = OpenLog(path, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if got := log.Read(""); len(got) != 2 {
		t.Fatalf("reopened: %d entries, want 2", len(got))
	}
}

// TestLimit: past the limit, the oldest entries go and the newest stay.
func TestLimit(t *testing.T) {
	log, err := OpenLog(filepath.Join(t.TempDir(), "history"), 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	for i := range 200 {
		log.Add(Entry{Kind: KIND_NODE, Event: "started", Message: strings.Repeat("x", 20) + string(rune('a'+i%26))})
	}
	got := log.Read("")
	if len(got) == 0 || len(got) >= 200 {
		t.Fatalf("got %d entries", len(got))
	}
	if log.size > 4096 {
		t.Fatalf("size %d over the limit", log.size)
	}
	if last := got[len(got)-1].Message; !strings.HasSuffix(last, string(rune('a'+199%26))) {
		t.Fatalf("the newest entry is gone: %q", last)
	}
}

// TestOff: limit 0 keeps nothing.
func TestOff(t *testing.T) {
	log, err := OpenLog(filepath.Join(t.TempDir(), "history"), 0, 0)
	if err != nil || log != nil {
		t.Fatal(log, err)
	}
	log.Add(Entry{Kind: KIND_NODE, Event: "started"})
	if got := log.Read(""); len(got) != 0 {
		t.Fatal(got)
	}
}
