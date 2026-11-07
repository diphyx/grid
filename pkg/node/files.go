package node

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// push receives a tar into the space (§10). The node owns the offset:
// the partial stream lives in push/<hash of path> until it ends.
func (node *Node) push(request *wire.Request, job *Job, body wire.Push) error {
	base := body.Path
	if base == "" {
		base = "."
	}
	key := sha256.Sum256([]byte(base))
	part := job.path("push/" + hex.EncodeToString(key[:8]))
	mkdirAllError := os.MkdirAll(job.path("push"), 0o755)
	if mkdirAllError != nil {
		return mkdirAllError
	}
	file, openFileError := os.OpenFile(part, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if openFileError != nil {
		return openFileError
	}
	defer file.Close()

	info, openFileError := file.Stat()
	if openFileError != nil {
		return openFileError
	}
	dataError := request.Data(wire.PushOffset{Offset: uint64(info.Size())})
	if dataError != nil {
		return nil
	}
	for {
		envelope, nextError := request.Next()
		if nextError != nil {
			return nil // connection gone; the partial stays for the next push
		}
		job.touch()
		switch {
		case envelope.Data != nil:
			{
				var chunk []byte
				unmarshalError := wire.Unmarshal(envelope.Data, &chunk)
				if unmarshalError != nil {
					return wire.NewReject(wire.INVALID, "push data: want bytes")
				}
				_, writeError := file.Write(chunk)
				if writeError != nil {
					return writeError
				}
			}
		case envelope.Cancel != nil:
			{
				return request.End(wire.Empty{})
			}
		case envelope.End != nil:
			{
				defer func() { _ = os.Remove(part) }()

				sum, extractError := wire.Extract(file, job.space, base)
				if extractError != nil {
					return extractError
				}
				return request.OK(wire.PushOK{Sha256: sum})
			}
		}
	}
}

// pull sends files from the space as a tar (§10).
func (node *Node) pull(request *wire.Request, job *Job, body wire.Pull) error {
	entries, collectError := wire.Collect(job.space, body.Path, body.Since)
	if collectError != nil {
		return collectError
	}
	var files []wire.File
	for _, entry := range entries {
		if !entry.Dir && entry.Link == "" {
			files = append(files, wire.File{Path: entry.Path, Size: uint64(entry.Size), Modified: uint64(entry.ModTime.UnixMilli())})
		}
	}
	if files == nil {
		files = []wire.File{}
	}
	okError := request.OK(wire.PullOK{Files: files})
	if okError != nil {
		return nil
	}
	if len(files) == 0 {
		_ = request.End(wire.Empty{}) // §6: no files, no data

		return nil
	}
	pipeReader, pipeWriter := io.Pipe()
	go func() { pipeWriter.CloseWithError(wire.WriteTarFS(pipeWriter, job.space, entries)) }()
	defer pipeReader.Close()

	hash := sha256.New()
	skip := int64(body.Offset)
	chunk := make([]byte, wire.MAX_CHUNK)
	for {
		if request.IsCanceled() {
			_ = request.End(wire.Empty{})

			return nil
		}
		index, readFullError := io.ReadFull(pipeReader, chunk)
		if index > 0 {
			hash.Write(chunk[:index])
			chunk := chunk[:index]
			if skip >= int64(index) {
				skip -= int64(index)
				chunk = nil
			} else if skip > 0 {
				chunk, skip = chunk[skip:], 0
			}
			if len(chunk) > 0 {
				if request.Data(chunk) != nil {
					return nil
				}
				job.touch()
			}
		}
		if errors.Is(readFullError, io.EOF) || errors.Is(readFullError, io.ErrUnexpectedEOF) {
			break
		}
		if readFullError != nil {
			return wire.NewReject(wire.INVALID, "pull: %v", readFullError)
		}
	}
	_ = request.End(wire.PullEnd{Sha256: hash.Sum(nil)})

	return nil
}

// touch resets expires.
func (job *Job) touch() {
	job.mutex.Lock()
	job.meta.Used = time.Now()
	job.mutex.Unlock()
}
