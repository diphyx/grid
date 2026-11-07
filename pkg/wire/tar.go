package wire

import (
	"archive/tar"
	"bytes"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// Entry is one tar entry (§10).
type Entry struct {
	Path    string
	Data    []byte    // regular files
	Size    int64     // a regular file's size when Data is streamed separately
	Exec    bool      // mode 0755
	Dir     bool      // a folder
	Link    string    // a symlink target
	ModTime time.Time // seconds kept
}

// Header builds the deterministic header for e (§10).
func (entry *Entry) Header() *tar.Header {
	header := &tar.Header{
		Name:    entry.Path,
		Mode:    0o644,
		ModTime: entry.ModTime.Truncate(time.Second),
		Format:  tar.FormatPAX,
	}
	switch {
	case entry.Dir:
		{
			header.Typeflag, header.Mode = tar.TypeDir, 0o755
			if !strings.HasSuffix(header.Name, "/") {
				header.Name += "/"
			}
		}
	case entry.Link != "":
		{
			header.Typeflag, header.Mode, header.Linkname = tar.TypeSymlink, 0o777, entry.Link
		}
	default:
		{
			header.Typeflag, header.Size = tar.TypeReg, int64(len(entry.Data))
			if entry.Data == nil {
				header.Size = entry.Size
			}
			if entry.Exec {
				header.Mode = 0o755
			}
		}
	}

	return header
}

// SortEntries orders entries bytewise by path (§10).
func SortEntries(entries []Entry) {
	sort.Slice(entries, func(i, index int) bool { return entries[i].Path < entries[index].Path })
}

// WriteTar writes entries, sorted, as a deterministic PAX tar.
func WriteTar(writer io.Writer, entries []Entry) error {
	entries = append([]Entry(nil), entries...)
	SortEntries(entries)
	tarWriter := tar.NewWriter(writer)
	for i := range entries {
		writeHeaderError := tarWriter.WriteHeader(entries[i].Header())
		if writeHeaderError != nil {
			return writeHeaderError
		}
		if len(entries[i].Data) > 0 {
			_, writeError := tarWriter.Write(entries[i].Data)
			if writeError != nil {
				return writeError
			}
		}
	}

	return tarWriter.Close()
}

// TarBytes is WriteTar into memory.
func TarBytes(entries []Entry) []byte {
	var buffer bytes.Buffer
	writeTarError := WriteTar(&buffer, entries)
	if writeTarError != nil {
		panic(writeTarError)
	}

	return buffer.Bytes()
}

// CheckHeader enforces §10 on an incoming entry: allowed types, paths inside, links inside.
func CheckHeader(header *tar.Header) error {
	name := strings.TrimSuffix(header.Name, "/")
	checkPathError := CheckPath(name)
	if checkPathError != nil {
		return checkPathError
	}
	switch header.Typeflag {
	case tar.TypeReg, tar.TypeDir:
	case tar.TypeSymlink:
		{
			if strings.HasPrefix(header.Linkname, "/") {
				return NewReject(INVALID, "symlink %q: absolute target", header.Name)
			}
			target := path.Clean(path.Join(path.Dir(name), header.Linkname))
			if target == ".." || strings.HasPrefix(target, "../") {
				return NewReject(INVALID, "symlink %q: leaves the space", header.Name)
			}
		}
	default:
		{
			return NewReject(INVALID, "entry %q: type %q not allowed", header.Name, header.Typeflag)
		}
	}

	return nil
}

// ReadTar reads a tar into entries, checking each (§10).
func ReadTar(reader io.Reader) ([]Entry, error) {
	tarReader := tar.NewReader(reader)
	var entries []Entry
	for {
		header, nextError := tarReader.Next()
		if nextError == io.EOF {
			return entries, nil
		}
		if nextError != nil {
			return nil, nextError
		}
		checkHeaderError := CheckHeader(header)
		if checkHeaderError != nil {
			return nil, checkHeaderError
		}
		entry := Entry{Path: strings.TrimSuffix(header.Name, "/"), ModTime: header.ModTime, Exec: header.Mode&0o111 != 0}
		switch header.Typeflag {
		case tar.TypeDir:
			{
				entry.Dir = true
			}
		case tar.TypeSymlink:
			{
				entry.Link = header.Linkname
			}
		default:
			{
				entry.Data, nextError = io.ReadAll(tarReader)
				if nextError != nil {
					return nil, nextError
				}
			}
		}
		entries = append(entries, entry)
	}
}
