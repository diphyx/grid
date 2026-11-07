package kit

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

func (fakeNode *FakeNode) push(request *wire.Request) error {
	var body wire.Push
	decodeBodyError := wire.DecodeBody(request.Body, &body)
	if decodeBodyError != nil {
		return decodeBodyError
	}
	job, jobError := fakeNode.job(body.Job)
	if jobError != nil {
		return jobError
	}
	base := body.Path
	if base == "" {
		base = "."
	}
	job.mutex.Lock()
	partial := job.partial[base]
	job.mutex.Unlock()
	dataError := request.Data(wire.PushOffset{Offset: uint64(len(partial))})
	if dataError != nil {
		return nil
	}
	for {
		envelope, nextError := request.Next()
		if nextError != nil {
			return nil // connection gone; partial stays
		}
		switch {
		case envelope.Data != nil:
			{
				var chunk []byte
				unmarshalError := wire.Unmarshal(envelope.Data, &chunk)
				if unmarshalError != nil {
					return wire.NewReject(wire.INVALID, "push data: want bytes")
				}
				if len(chunk) > wire.MAX_CHUNK {
					return wire.NewReject(wire.INVALID, "push data: %d bytes, over %d", len(chunk), wire.MAX_CHUNK)
				}
				job.mutex.Lock()
				gone := job.dropped
				job.partial[base] = append(job.partial[base], chunk...)
				job.used = time.Now()
				job.mutex.Unlock()
				if gone {
					return wire.NewReject(wire.UNKNOWN, "no job %s", job.id)
				}
			}
		case envelope.Cancel != nil:
			{
				return request.End(wire.Empty{})
			}
		case envelope.End != nil:
			{
				job.mutex.Lock()
				defer job.mutex.Unlock()

				if job.dropped {
					return wire.NewReject(wire.UNKNOWN, "no job %s", job.id)
				}
				whole := job.partial[base]
				delete(job.partial, base)
				entries, readTarError := wire.ReadTar(bytes.NewReader(whole))
				if readTarError != nil {
					return wire.NewReject(wire.INVALID, "tar: %v", readTarError)
				}
				for _, entry := range entries {
					if entry.Dir {
						continue
					}
					path := entry.Path
					if base != "." {
						path = base + "/" + path
					}
					entry.Path = path
					job.files[path] = entry
				}
				sum := sha256.Sum256(whole)

				return request.OK(wire.PushOK{Sha256: sum[:]})
			}
		}
	}
}

func (fakeNode *FakeNode) pull(request *wire.Request) error {
	var pullBody wire.Pull
	decodeBodyError := wire.DecodeBody(request.Body, &pullBody)
	if decodeBodyError != nil {
		return decodeBodyError
	}
	job, jobError := fakeNode.job(pullBody.Job)
	if jobError != nil {
		return jobError
	}
	job.mutex.Lock()
	job.used = time.Now()
	var entries []wire.Entry
	for path, entry := range job.files {
		if pullBody.Path == "." || path == pullBody.Path || strings.HasPrefix(path, pullBody.Path+"/") {
			if pullBody.Since == 0 || uint64(entry.ModTime.UnixMilli()) > pullBody.Since {
				entries = append(entries, entry)
			}
		}
	}
	job.mutex.Unlock()
	wire.SortEntries(entries)
	files := make([]wire.File, len(entries))
	for i, entry := range entries {
		files[i] = wire.File{Path: entry.Path, Size: uint64(len(entry.Data)), Modified: uint64(entry.ModTime.UnixMilli())}
	}
	okError := request.OK(wire.PullOK{Files: files})
	if okError != nil {
		return nil
	}
	if len(entries) == 0 {
		_ = request.End(wire.Empty{})

		return nil
	}
	body := wire.TarBytes(entries)
	for i := min(int(pullBody.Offset), len(body)); i < len(body); i += wire.MAX_CHUNK {
		if request.IsCanceled() {
			_ = request.End(wire.Empty{})

			return nil
		}
		if request.Data(body[i:min(i+wire.MAX_CHUNK, len(body))]) != nil {
			return nil
		}
	}
	sum := sha256.Sum256(body)
	_ = request.End(wire.PullEnd{Sha256: sum[:]})

	return nil
}
