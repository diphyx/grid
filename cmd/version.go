package cmd

import (
	"fmt"
	"runtime/debug"
)

// VERSION is set at build time by releases and images: -ldflags "-X github.com/diphyx/grid/cmd.VERSION=v1.2.3".
var VERSION = ""

// version is `grid version`: the release, the module version for `go install …@v1.2.3`, or "dev".
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

func printVersion() int {
	fmt.Println("grid", version())

	return 0
}
