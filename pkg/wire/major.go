package wire

import (
	"fmt"
	"strconv"

	"github.com/pelletier/go-toml/v2"
)

// The generation (docs/versions.md): a major is its own product. Its configs say
// `grid = MAJOR`, its state is under STATE, and it speaks PROLOGUE; nothing of another
// generation is read, kept, or talked to. Bump MAJOR, and only it, for a release that breaks
// the config, the state, or the protocol.
const (
	MAJOR = "2"                      // the major of every release of this generation
	STATE = "/var/lib/grid/" + MAJOR // the root of its state on disk
)

// CheckMajor checks that a config file is for this generation: `grid = MAJOR`, before any
// other check, so a config of another generation gets one clear answer, not its unknown keys.
// A file that is not TOML passes: the full decode reports it.
func CheckMajor(content []byte) error {
	var head struct {
		Grid *int `toml:"grid"`
	}
	unmarshalError := toml.Unmarshal(content, &head)
	if unmarshalError != nil {
		return nil
	}
	if head.Grid == nil {
		return fmt.Errorf("grid: required: grid = %s, the generation, as the first line", MAJOR)
	}
	if strconv.Itoa(*head.Grid) != MAJOR {
		return fmt.Errorf("config is for grid %d, this is grid %s: write a fresh one for grid %s", *head.Grid, MAJOR, MAJOR)
	}

	return nil
}
