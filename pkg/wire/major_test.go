package wire

import (
	"strings"
	"testing"
)

// TestCheckMajor: a config of this generation passes; one without `grid`, or of another
// generation, gets one clear error, before its keys are looked at; the prologue and the state
// are of the same generation.
func TestCheckMajor(t *testing.T) {
	for _, each := range []struct{ config, want string }{
		{"grid = " + MAJOR + "\nidentity = \"k\"\n", ""},
		{"identity = \"k\"\n[hub]\nip = \"10.0.0.1\"\n", "grid: required"},
		{"grid = 1\n[hub]\nip = \"10.0.0.1\"\n", "config is for grid 1, this is grid " + MAJOR},
		{"grid = \"x\"\n", ""}, // not this check's to report: the full decode says the type is wrong
		{"not toml [", ""},
	} {
		err := CheckMajor([]byte(each.config))
		if each.want == "" && err != nil || each.want != "" && (err == nil || !strings.Contains(err.Error(), each.want)) {
			t.Errorf("%q: %v, want %q", each.config, err, each.want)
		}
	}
	if PROLOGUE != "grid/"+MAJOR || STATE != "/var/lib/grid/"+MAJOR {
		t.Errorf("prologue %s, state %s: not of grid %s", PROLOGUE, STATE, MAJOR)
	}
}
