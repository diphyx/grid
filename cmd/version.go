package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// VERSION is the release, set at build time by package.sh and the Dockerfile:
// -ldflags "-X github.com/diphyx/grid/v2/cmd.VERSION=v1.2.3".
var VERSION = ""

// version is the release, else the module version from `go install …@v1.2.3`, else "dev".
func version() string {
	if VERSION != "" {
		return VERSION
	}
	info, ok := debug.ReadBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	return "dev"
}

// printVersion is `grid version`: the release and the protocol of its generation (wire.MAJOR).
func printVersion() int {
	fmt.Printf("grid %s (protocol %s)\n", version(), wire.PROLOGUE)

	return 0
}
