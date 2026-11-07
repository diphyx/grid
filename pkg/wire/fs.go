package wire

import (
	"archive/tar"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Extract checks every entry of the tar in f, then writes them under base in root (§10);
// it returns the stream's sha256. A bad entry writes nothing.
func Extract(file io.ReadSeeker, root *os.Root, base string) ([]byte, error) {
	hash := sha256.New()
	_, seekError := file.Seek(0, io.SeekStart)
	if seekError != nil {
		return nil, seekError
	}
	links := map[string]bool{} // the symlinks this tar makes
	hashed := io.TeeReader(file, hash)
	reader := tar.NewReader(hashed)
	for {
		header, entryError := reader.Next()
		if entryError == io.EOF {
			break
		}
		if entryError != nil {
			return nil, NewReject(INVALID, "tar: %v", entryError)
		}
		checkHeaderError := CheckHeader(header)
		if checkHeaderError != nil {
			return nil, checkHeaderError
		}
		// a link, from this tar or already there, must not carry a later entry elsewhere
		name := path.Join(base, strings.TrimSuffix(header.Name, "/"))
		for folder := path.Dir(name); folder != "." && folder != "/"; folder = path.Dir(folder) {
			if links[folder] {
				return nil, NewReject(INVALID, "%s: a symlink on the way", header.Name)
			}
		}
		linkError := checkNoLink(root, path.Dir(name))
		if linkError != nil {
			return nil, linkError
		}
		if header.Typeflag == tar.TypeSymlink {
			links[name] = true
		}
	}
	_, restError := io.Copy(io.Discard, hashed) // the padding after the last entry counts too
	if restError != nil {
		return nil, restError
	}
	sum := hash.Sum(nil)

	_, rewindError := file.Seek(0, io.SeekStart)
	if rewindError != nil {
		return nil, rewindError
	}
	baseError := root.MkdirAll(base, 0o755)
	if baseError != nil {
		return nil, NewReject(INVALID, "path %q: %v", base, baseError)
	}
	reader = tar.NewReader(file)
	for {
		header, entryError := reader.Next()
		if entryError == io.EOF {
			return sum, nil
		}
		if entryError != nil {
			return nil, entryError
		}
		name := path.Join(base, header.Name)
		mkdirAllError := root.MkdirAll(path.Dir(name), 0o755)
		if mkdirAllError != nil {
			return nil, NewReject(INVALID, "%s: %v", name, mkdirAllError)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			{
				entryError = root.MkdirAll(name, 0o755)
			}
		case tar.TypeSymlink:
			{
				_ = root.Remove(name)
				entryError = root.Symlink(header.Linkname, name)
			}
		default:
			{
				mode := os.FileMode(0o644)
				if header.Mode&0o111 != 0 {
					mode = 0o755
				}
				var file *os.File
				file, entryError = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
				if entryError == nil {
					_, entryError = io.Copy(file, reader)
					closeError := file.Close()
					if entryError == nil {
						entryError = closeError
					}
					if entryError == nil {
						entryError = root.Chmod(name, mode)
					}
				}
			}
		}
		if entryError != nil {
			return nil, NewReject(INVALID, "%s: %v", name, entryError)
		}
		if header.Typeflag != tar.TypeSymlink {
			chtimesError := root.Chtimes(name, header.ModTime, header.ModTime)
			if chtimesError != nil {
				return nil, NewReject(INVALID, "%s: %v", name, chtimesError)
			}
		}
	}
}

// Collect lists the entries under p in root, sorted (§10); since > 0 keeps only files
// modified after it, in unix ms. A missing path gives none.
func Collect(root *os.Root, path string, since uint64) ([]Entry, error) {
	_, lstatError := root.Lstat(path)
	if errors.Is(lstatError, fs.ErrNotExist) {
		return nil, nil
	} else if lstatError != nil {
		return nil, NewReject(INVALID, "%s: %v", path, lstatError)
	}
	var entries []Entry
	walkDirError := fs.WalkDir(root.FS(), path, func(name string, dirEntry fs.DirEntry, problem error) error {
		if problem != nil {
			return problem
		}
		if name == "." {
			return nil
		}
		info, lstatError := root.Lstat(name)
		if lstatError != nil {
			return lstatError
		}
		entry := Entry{Path: filepath.ToSlash(name), ModTime: info.ModTime()}
		newer := since == 0 || uint64(info.ModTime().UnixMilli()) > since
		switch {
		case info.IsDir():
			{
				if since != 0 {
					return nil
				}
				entry.Dir = true
			}
		case info.Mode()&fs.ModeSymlink != 0:
			{
				entry.Link, problem = root.Readlink(name)
				if problem != nil {
					return problem
				}
				checkHeaderError := CheckHeader(entry.Header())
				if checkHeaderError != nil {
					return checkHeaderError
				}
				if !newer {
					return nil
				}
			}
		case info.Mode().IsRegular():
			{
				if !newer {
					return nil
				}
				entry.Size, entry.Exec = info.Size(), info.Mode()&0o111 != 0
			}
		default:
			{
				return nil // devices, sockets, fifos never travel
			}
		}
		entries = append(entries, entry)

		return nil
	})
	if walkDirError != nil {
		var reject *Reject
		if errors.As(walkDirError, &reject) {
			return nil, reject
		}
		return nil, NewReject(INVALID, "%s: %v", path, walkDirError)
	}
	SortEntries(entries)

	return entries, nil
}

// WriteTarFS writes entries from Collect as a tar, reading file bodies from root.
func WriteTarFS(writer io.Writer, root *os.Root, entries []Entry) error {
	tarWriter := tar.NewWriter(writer)
	for i := range entries {
		entry := &entries[i]
		writeHeaderError := tarWriter.WriteHeader(entry.Header())
		if writeHeaderError != nil {
			return writeHeaderError
		}
		if entry.Dir || entry.Link != "" {
			continue
		}
		file, openError := root.Open(entry.Path)
		if openError != nil {
			return openError
		}
		_, openError = io.CopyN(tarWriter, file, entry.Size)
		file.Close()
		if openError != nil {
			return openError
		}
	}

	return tarWriter.Close()
}

// checkNoLink rejects a folder path with a symlink anywhere along it.
func checkNoLink(root *os.Root, folder string) error {
	current := ""
	for _, part := range strings.Split(path.Clean(folder), "/") {
		if part == "." || part == "" {
			continue
		}
		current = path.Join(current, part)
		info, statError := root.Lstat(current)
		if errors.Is(statError, fs.ErrNotExist) {
			return nil // the rest is made fresh
		}
		if statError != nil {
			return NewReject(INVALID, "%s: %v", current, statError)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return NewReject(INVALID, "%s: a symlink on the way", current)
		}
	}

	return nil
}
