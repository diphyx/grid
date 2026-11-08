//go:build !windows

package node

import "os"

// linkDir makes name in the space a link to the folder target, outside the space.
func linkDir(space *os.Root, _, name, target string) error {
	return space.Symlink(target, name)
}
