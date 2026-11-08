package kit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	if body.ID != "" && fakeNode.Share {
		fakeNode.mutex.Lock()
		share, isHave := fakeNode.shares[body.ID]
		fakeNode.mutex.Unlock()
		if isHave {
			linkError := job.link(base, share.entries)
			if linkError != nil {
				return linkError
			}

			return pushHave(request, share)
		}
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
				sum := sha256.Sum256(whole)
				isShare := body.ID != "" && fakeNode.Share
				if isShare && hex.EncodeToString(sum[:16]) != body.ID {
					return wire.NewReject(wire.INVALID, "push: id %s is not the tar's", body.ID)
				}
				if isShare {
					fakeNode.mutex.Lock()
					fakeNode.shares[body.ID] = fshare{entries: entries, sum: sum[:]}
					fakeNode.mutex.Unlock()
				}
				job.place(base, entries)

				return request.OK(wire.PushOK{Sha256: sum[:], Share: isShare})
			}
		}
	}
}

// pushHave answers a share the fake node has: have, then only end may come.
func pushHave(request *wire.Request, share fshare) error {
	dataError := request.Data(wire.PushOffset{Have: true})
	if dataError != nil {
		return nil
	}
	for {
		envelope, nextError := request.Next()
		if nextError != nil {
			return nil
		}
		switch {
		case envelope.Data != nil:
			{
				return wire.NewReject(wire.INVALID, "push: data after have")
			}
		case envelope.Cancel != nil:
			{
				return request.End(wire.Empty{})
			}
		case envelope.End != nil:
			{
				return request.OK(wire.PushOK{Sha256: share.sum, Share: true})
			}
		}
	}
}

// link puts a share's files at base, where the node would link it; base must be free.
func (job *fjob) link(base string, entries []wire.Entry) error {
	job.mutex.Lock()
	defer job.mutex.Unlock()

	for path := range job.files {
		if path == base || strings.HasPrefix(path, base+"/") {
			return wire.NewReject(wire.INVALID, "push: %s is already in the space", base)
		}
	}
	job.place(base, entries)

	return nil
}

// place puts a tar's files under base; the caller holds job.mutex.
func (job *fjob) place(base string, entries []wire.Entry) {
	for _, entry := range entries {
		if entry.Dir {
			continue
		}
		if base != "." {
			entry.Path = base + "/" + entry.Path
		}
		job.files[entry.Path] = entry
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
