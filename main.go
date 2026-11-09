// grid runs the grid hub and node daemons, their CLI, and the test kit. See cmd/.
package main

import (
	"os"

	"github.com/diphyx/grid/v2/cmd"
)

func main() {
	os.Exit(cmd.Run(os.Args[1:]))
}
