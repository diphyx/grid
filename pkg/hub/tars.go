package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The tar store, under the jobs' path:
//
//	tars/<sha256>            a push tar, by the hex sha256 of its bytes; its push id is the first
//	                         32 hex chars
//	tars/manifest/<key>      the sha256 of the tar packed from the source with that manifest
//
// Every push of every job is kept here, once per content: the same input is packed and stored
// once, whichever jobs use it. A tar no job uses is removed by sweep.

// TAR_KEEP is how long a tar no job uses stays, so a submit has time to save its record.
const TAR_KEEP = time.Hour

// tars is the hub's tar store.
type tars struct {
	dir string
}

func newTars(dir string) (*tars, error) {
	mkdirError := os.MkdirAll(filepath.Join(dir, "manifest"), 0o755)
	if mkdirError != nil {
		return nil, mkdirError
	}

	return &tars{dir: dir}, nil
}

// path is where the tar with that sha256 is.
func (tars *tars) path(sum string) string { return filepath.Join(tars.dir, sum) }

// lookup finds the tar packed from a source with that manifest; it counts as used now.
func (tars *tars) lookup(manifest string) (string, bool) {
	if !isHex(manifest, 64) {
		return "", false
	}
	content, readError := os.ReadFile(filepath.Join(tars.dir, "manifest", manifest))
	sum := strings.TrimSpace(string(content))
	if readError != nil || !isHex(sum, 64) {
		return "", false
	}
	now := time.Now()
	if os.Chtimes(tars.path(sum), now, now) != nil {
		return "", false
	}

	return sum, true
}

// put stores a tar and returns its sha256; with a manifest, a later lookup finds it.
func (tars *tars) put(manifest string, tar io.Reader) (string, error) {
	file, createError := os.CreateTemp(tars.dir, ".tmp-*")
	if createError != nil {
		return "", createError
	}
	defer func() { _ = os.Remove(file.Name()) }() // gone after the rename, or never used

	hash := sha256.New()
	_, copyError := io.Copy(io.MultiWriter(file, hash), tar)
	closeError := file.Close()
	if copyError != nil {
		return "", copyError
	}
	if closeError != nil {
		return "", closeError
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	renameError := os.Rename(file.Name(), tars.path(sum))
	if renameError != nil {
		return "", renameError
	}
	if isHex(manifest, 64) {
		writeError := os.WriteFile(filepath.Join(tars.dir, "manifest", manifest), []byte(sum+"\n"), 0o644)
		if writeError != nil {
			return "", writeError
		}
	}

	return sum, nil
}

// sweep removes the tars no job uses, unused for TAR_KEEP, and the manifests that point at no tar.
func (tars *tars) sweep(used map[string]bool) {
	entries, _ := os.ReadDir(tars.dir)
	for _, entry := range entries {
		name := entry.Name()
		info, infoError := entry.Info()
		if infoError != nil || entry.IsDir() || used[name] || time.Since(info.ModTime()) < TAR_KEEP {
			continue
		}
		if isHex(name, 64) || strings.HasPrefix(name, ".tmp-") {
			_ = os.Remove(filepath.Join(tars.dir, name))
		}
	}
	manifests, _ := os.ReadDir(filepath.Join(tars.dir, "manifest"))
	for _, entry := range manifests {
		path := filepath.Join(tars.dir, "manifest", entry.Name())
		content, _ := os.ReadFile(path)
		_, statError := os.Stat(tars.path(strings.TrimSpace(string(content))))
		if errors.Is(statError, fs.ErrNotExist) {
			_ = os.Remove(path)
		}
	}
}

func isHex(text string, length int) bool {
	if len(text) != length {
		return false
	}
	_, decodeError := hex.DecodeString(text)

	return decodeError == nil && strings.ToLower(text) == text
}
