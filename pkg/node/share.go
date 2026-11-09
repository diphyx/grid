package node

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diphyx/grid/v2/pkg/history"
	"github.com/diphyx/grid/v2/pkg/wire"
)

// The share, on disk:
//
//	share/<id>/         a share push, extracted, read only; linked into the spaces that use it
//	share/<id>.json     shareMeta; its mtime is the last hit
//	share/.tmp-<rand>/  a share being installed
//	share/.old-<rand>/  a share being removed

// Linked is a share linked into a job's space.
type Linked struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// shareMeta is what the node keeps about a share.
type shareMeta struct {
	Sha256 []byte `json:"sha256"` // of the whole tar; the id is its first 16 bytes
	Size   int64  `json:"size"`   // the tar's size, what the share counts for
}

// shares is the node's share space. One mutex covers a hit and its link, an install's last step,
// and a removal, so a share is never removed between a hit and its link.
type shares struct {
	dir     string // absolute: links point at it
	limit   int64  // bytes; 0 = off
	expires time.Duration
	history *history.Log

	mutex    sync.Mutex
	used     int64          // installed bytes
	reserved int64          // bytes of installs under way
	inUse    map[string]int // id → live jobs linking it
}

// errNoRoom says a share does not fit; the push then goes into the space.
var errNoRoom = errors.New("share: no room")

// openShares makes the share space and clears what a stop left half done.
func openShares(config Share, log *history.Log) (*shares, error) {
	dir, absError := filepath.Abs(config.Path)
	if absError != nil {
		return nil, absError
	}
	created := &shares{dir: dir, limit: config.Limit.Bytes, expires: config.Expires.D(), history: log, inUse: map[string]int{}}
	if created.limit == 0 {
		return created, nil
	}
	mkdirError := os.MkdirAll(dir, 0o755)
	if mkdirError != nil {
		return nil, mkdirError
	}
	entries, readDirError := os.ReadDir(dir)
	if readDirError != nil {
		return nil, readDirError
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, ".tmp-") || strings.HasPrefix(name, ".old-"):
			{
				_ = removeTree(filepath.Join(dir, name))
			}
		case entry.IsDir() && wire.IsID(name):
			{
				meta, readError := created.readMeta(name)
				if readError != nil {
					_ = removeTree(filepath.Join(dir, name)) // a share without meta is not whole
					_ = os.Remove(filepath.Join(dir, name+".json"))

					continue
				}
				created.used += meta.Size
			}
		}
	}

	return created, nil
}

func (shares *shares) isOn() bool { return shares.limit > 0 }

func (shares *shares) readMeta(id string) (shareMeta, error) {
	var meta shareMeta
	content, readFileError := os.ReadFile(filepath.Join(shares.dir, id+".json"))
	if readFileError != nil {
		return meta, readFileError
	}

	return meta, json.Unmarshal(content, &meta)
}

// hit links the share into the job when the node has it; ok false is a miss.
func (shares *shares) hit(job *Job, id, to string) (shareMeta, bool, error) {
	shares.mutex.Lock()
	defer shares.mutex.Unlock()

	meta, readError := shares.readMeta(id)
	if readError != nil {
		return meta, false, nil
	}
	_, statError := os.Stat(filepath.Join(shares.dir, id))
	if statError != nil {
		return meta, false, nil
	}
	linkError := shares.linkLocked(job, id, to)
	var reject *wire.Reject
	if errors.As(linkError, &reject) {
		return meta, false, reject
	}
	if linkError != nil {
		return meta, false, nil // no link here: the push goes into the space
	}

	return meta, true, nil
}

// reserve holds room for an install of size bytes, removing unused shares, least recently hit
// first, as needed; errNoRoom when it cannot fit.
func (shares *shares) reserve(size int64) error {
	shares.mutex.Lock()
	defer shares.mutex.Unlock()

	if !shares.isOn() || size > shares.limit {
		return errNoRoom
	}
	for shares.used+shares.reserved+size > shares.limit {
		if !shares.evictLocked() {
			return errNoRoom
		}
	}
	shares.reserved += size

	return nil
}

// release gives back a reservation.
func (shares *shares) release(size int64) {
	shares.mutex.Lock()
	defer shares.mutex.Unlock()

	shares.reserved -= size
}

// install extracts the tar in file into the share as id, read only, and links it into the job.
// Room must be reserved for size; install gives it back.
func (shares *shares) install(job *Job, file io.ReadSeeker, id, to string, sum []byte, size int64) error {
	defer shares.release(size)

	temporary, tempError := shares.tempDir(".tmp-")
	if tempError != nil {
		return tempError
	}
	defer func() { _ = removeTree(temporary) }() // gone after the rename, or never used

	root, rootError := os.OpenRoot(temporary)
	if rootError != nil {
		return rootError
	}
	_, extractError := wire.Extract(file, root, ".")
	_ = root.Close()
	if extractError != nil {
		return extractError
	}
	readOnlyError := makeReadOnly(temporary)
	if readOnlyError != nil {
		return readOnlyError
	}
	content, _ := json.Marshal(shareMeta{Sha256: sum, Size: size})

	shares.mutex.Lock()
	defer shares.mutex.Unlock()

	final := filepath.Join(shares.dir, id)
	_, statError := os.Stat(final)
	if errors.Is(statError, fs.ErrNotExist) {
		writeError := os.WriteFile(final+".json", content, 0o644)
		if writeError != nil {
			return writeError
		}
		renameError := os.Rename(temporary, final)
		if renameError != nil {
			_ = os.Remove(final + ".json")

			return renameError
		}
		shares.used += size
		shares.history.Add(history.Entry{Kind: history.KIND_SHARE, Event: "install", Subject: id, Message: wire.Size{Bytes: size}.String()})
	}
	// else another job installed it meanwhile: the same content, so use that one

	return shares.linkLocked(job, id, to)
}

// linkLocked links share id at to in the job's space, records it in the job, and counts it as a
// hit; the caller holds shares.mutex.
func (shares *shares) linkLocked(job *Job, id, to string) error {
	job.mutex.Lock()
	defer job.mutex.Unlock()

	if job.dropped {
		return wire.NewReject(wire.UNKNOWN, "no job %s", job.ID())
	}
	_, lstatError := job.space.Lstat(to)
	if lstatError == nil {
		return wire.NewReject(wire.INVALID, "push: %s is already in the space", to)
	}
	mkdirError := job.space.MkdirAll(path.Dir(to), 0o755)
	if mkdirError != nil {
		return wire.NewReject(wire.INVALID, "push: %v", mkdirError)
	}
	linkError := linkDir(job.space, job.path("space"), to, filepath.Join(shares.dir, id))
	if linkError != nil {
		return linkError
	}
	job.meta.Shares = append(job.meta.Shares, Linked{ID: id, Path: to})
	_ = job.save()
	shares.inUse[id]++
	now := time.Now()
	_ = os.Chtimes(filepath.Join(shares.dir, id+".json"), now, now)

	return nil
}

// unlink lets go of the job's shares before its space is removed: each link is removed itself,
// never followed. Locks shares.mutex, then job.mutex, as linkLocked does.
func (shares *shares) unlink(job *Job) {
	shares.mutex.Lock()
	defer shares.mutex.Unlock()

	job.mutex.Lock()
	defer job.mutex.Unlock()

	for _, linked := range job.meta.Shares {
		if job.space != nil {
			_ = job.space.Remove(linked.Path)
		}
		shares.inUse[linked.ID]--
		if shares.inUse[linked.ID] <= 0 {
			delete(shares.inUse, linked.ID)
		}
	}
}

// keep counts the shares of a job reopened after a restart.
func (shares *shares) keep(job *Job) {
	shares.mutex.Lock()
	defer shares.mutex.Unlock()

	for _, linked := range job.meta.Shares {
		shares.inUse[linked.ID]++
	}
}

// listLocked lists the shares not in use, least recently hit first; the caller holds
// shares.mutex.
func (shares *shares) listLocked() []os.FileInfo {
	entries, _ := os.ReadDir(shares.dir)
	var unused []os.FileInfo
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !wire.IsID(id) || shares.inUse[id] > 0 {
			continue
		}
		info, infoError := entry.Info()
		if infoError == nil {
			unused = append(unused, info)
		}
	}
	sort.Slice(unused, func(left, right int) bool { return unused[left].ModTime().Before(unused[right].ModTime()) })

	return unused
}

// evictLocked removes the least recently hit share not in use; false when there is none. The
// caller holds shares.mutex.
func (shares *shares) evictLocked() bool {
	unused := shares.listLocked()
	if len(unused) == 0 {
		return false
	}
	shares.removeLocked(strings.TrimSuffix(unused[0].Name(), ".json"), "limit")

	return true
}

// sweep removes the shares not in use and with no hit for expires.
func (shares *shares) sweep() {
	if !shares.isOn() || shares.expires == 0 {
		return
	}
	shares.mutex.Lock()
	defer shares.mutex.Unlock()

	for _, info := range shares.listLocked() {
		if time.Since(info.ModTime()) > shares.expires {
			shares.removeLocked(strings.TrimSuffix(info.Name(), ".json"), "expired")
		}
	}
}

// removeLocked removes one share: its meta first, so a stop halfway leaves no share to hit.
// The caller holds shares.mutex.
func (shares *shares) removeLocked(id, why string) {
	meta, _ := shares.readMeta(id)
	_ = os.Remove(filepath.Join(shares.dir, id+".json"))
	shares.used -= meta.Size
	old, tempError := shares.tempDir(".old-")
	if tempError == nil && os.Rename(filepath.Join(shares.dir, id), filepath.Join(old, id)) == nil {
		go func() { _ = removeTree(old) }()
	} else {
		_ = removeTree(filepath.Join(shares.dir, id))
	}
	shares.history.Add(history.Entry{Kind: history.KIND_SHARE, Event: "removed", Subject: id, Message: why})
}

// tempDir makes a new folder in the share space, named by prefix and a random part.
func (shares *shares) tempDir(prefix string) (string, error) {
	random := make([]byte, 8)
	_, _ = rand.Read(random)
	dir := filepath.Join(shares.dir, prefix+hex.EncodeToString(random))

	return dir, os.Mkdir(dir, 0o755)
}

// usage is what the share holds: bytes and shares.
func (shares *shares) usage() (int64, int) {
	shares.mutex.Lock()
	defer shares.mutex.Unlock()

	if !shares.isOn() {
		return 0, 0
	}
	count := 0
	entries, _ := os.ReadDir(shares.dir)
	for _, entry := range entries {
		if entry.IsDir() && wire.IsID(entry.Name()) {
			count++
		}
	}

	return shares.used, count
}

// makeReadOnly sets a tree read only: files 0444, or 0555 if executable; folders 0555, last,
// once nothing more goes in them.
func makeReadOnly(dir string) error {
	var folders []string
	walkError := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, problem error) error {
		if problem != nil {
			return problem
		}
		switch {
		case entry.IsDir():
			{
				folders = append(folders, name)
			}
		case entry.Type().IsRegular():
			{
				info, infoError := entry.Info()
				if infoError != nil {
					return infoError
				}
				mode := os.FileMode(0o444)
				if info.Mode()&0o111 != 0 {
					mode = 0o555
				}

				return os.Chmod(name, mode)
			}
		}

		return nil
	})
	if walkError != nil {
		return walkError
	}
	for i := len(folders) - 1; i >= 0; i-- {
		chmodError := os.Chmod(folders[i], 0o555)
		if chmodError != nil {
			return chmodError
		}
	}

	return nil
}

// removeTree removes a tree made read only: it is made writable first, which Windows needs for
// files too.
func removeTree(dir string) error {
	_ = filepath.WalkDir(dir, func(name string, entry fs.DirEntry, problem error) error {
		switch {
		case problem != nil:
			{
				return nil
			}
		case entry.IsDir():
			{
				_ = os.Chmod(name, 0o755)
			}
		case entry.Type().IsRegular():
			{
				_ = os.Chmod(name, 0o644)
			}
		}

		return nil
	})

	return os.RemoveAll(dir)
}
