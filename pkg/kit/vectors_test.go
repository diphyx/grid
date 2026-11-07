package kit

import (
	"encoding/json"
	"os"
	"testing"
)

// TestVectors fails if spec/vectors/vectors.json is out of date.
// Regenerate: go run . test vectors > spec/vectors.json
func TestVectors(t *testing.T) {
	v, err := MakeVectors()
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := MakeVectors()
	a, _ := json.MarshalIndent(v, "", "  ")
	b, _ := json.MarshalIndent(v2, "", "  ")
	if string(a) != string(b) {
		t.Fatal("vectors are not deterministic")
	}
	f, err := os.ReadFile("../../spec/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(f) != string(a)+"\n" {
		t.Fatal("spec/vectors/vectors.json is out of date")
	}
}
