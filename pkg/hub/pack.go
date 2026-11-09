package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// A push source is a local file or folder: packed into a tar once, and known again by its
// manifest, so it is not packed again while it does not change.

// Pack writes a local file or folder as a push tar: a folder's contents, or one file by its name.
func Pack(writer io.Writer, from string) error {
	root, name, openError := openSource(from)
	if openError != nil {
		return openError
	}
	defer func() { _ = root.Close() }()

	entries, collectError := wire.Collect(root, name, 0)
	if collectError != nil {
		return collectError
	}

	return wire.WriteTarFS(writer, root, entries)
}

// Manifest is the key of a push source: the sha256 of its absolute path and of every entry under
// it, with path, type, size, mtime in ns, mode, and link target. Only the folder is walked, no
// file is read; the same key means the same tar, unless a file changed while keeping its size
// and mtime.
func Manifest(from string) (string, error) {
	absolute, absError := filepath.Abs(from)
	if absError != nil {
		return "", absError
	}
	root, name, openError := openSource(absolute)
	if openError != nil {
		return "", openError
	}
	defer func() { _ = root.Close() }()

	entries, collectError := wire.Collect(root, name, 0)
	if collectError != nil {
		return "", collectError
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\n", absolute)
	for _, entry := range entries {
		kind := "f"
		switch {
		case entry.Dir:
			{
				kind = "d"
			}
		case entry.Link != "":
			{
				kind = "l"
			}
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\x00%t\x00%s\n", entry.Path, kind, entry.Size, entry.ModTime.UnixNano(), entry.Exec, entry.Link)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// openSource opens what a push packs: a folder's contents, or one file by its name.
func openSource(from string) (*os.Root, string, error) {
	info, statError := os.Stat(from)
	if statError != nil {
		return nil, "", statError
	}
	dir, name := from, "."
	if !info.IsDir() {
		dir, name = filepath.Dir(from), filepath.Base(from)
	}
	root, openError := os.OpenRoot(dir)
	if openError != nil {
		return nil, "", openError
	}

	return root, name, nil
}
