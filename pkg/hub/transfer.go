package hub

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/diphyx/grid/pkg/wire"
)

// push sends one packed tar; the node says how much it already has (§10).
func (hub *Hub) push(session *wire.Session, stop <-chan struct{}, nodeJob, to, path string) error {
	file, openError := os.Open(path)
	if openError != nil {
		return openError
	}
	defer func() { _ = file.Close() }()

	call, startError := session.Start(wire.CALL_PUSH, wire.Push{Job: nodeJob, Path: to})
	if startError != nil {
		return startError
	}
	defer call.Close()

	first, nextError := call.Next()
	if nextError != nil {
		return nextError
	}
	if first.Reject != nil {
		return first.Reject
	}
	var offset wire.PushOffset
	unmarshalError := wire.Unmarshal(first.Data, &offset)
	if unmarshalError != nil {
		return fmt.Errorf("push: %w", unmarshalError)
	}
	_, seekError := file.Seek(int64(offset.Offset), io.SeekStart)
	if seekError != nil {
		return seekError
	}
	chunk := make([]byte, wire.MAX_CHUNK)
	for {
		select {
		case <-stop:
			{
				_ = call.Cancel()

				return errHalt
			}
		default:
			{
			}
		}
		count, readError := file.Read(chunk)
		if count > 0 {
			dataError := call.Data(chunk[:count])
			if dataError != nil {
				return dataError
			}
		}
		if errors.Is(readError, io.EOF) {
			break
		}
		if readError != nil {
			return readError
		}
	}
	endError := call.End(wire.Empty{})
	if endError != nil {
		return endError
	}
	var pushed wire.PushOK
	replyError := call.Reply(&pushed)
	if replyError != nil {
		return replyError
	}
	_, rewindError := file.Seek(0, io.SeekStart)
	if rewindError != nil {
		return rewindError
	}
	hash := sha256.New()
	_, hashError := io.Copy(hash, file)
	if hashError != nil {
		return hashError
	}
	if !bytes.Equal(hash.Sum(nil), pushed.Sha256) {
		return errors.New("push: sha256 differs")
	}

	return nil
}

// pull takes one path into the job's files. The partial stream stays on disk, so a broken pull
// resumes at its size; a sha256 mismatch starts it again from 0.
func (hub *Hub) pull(session *wire.Session, job *entry, nodeJob, path string) error {
	sum := sha256.Sum256([]byte(path))
	part := job.path(fmt.Sprintf("pull-%x.part", sum[:6]))
	file, openError := os.OpenFile(part, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if openError != nil {
		return openError
	}
	defer func() { _ = file.Close() }()

	info, statError := file.Stat()
	if statError != nil {
		return statError
	}
	call, startError := session.Start(wire.CALL_PULL, wire.Pull{Job: nodeJob, Path: path, Offset: uint64(info.Size())})
	if startError != nil {
		return startError
	}
	defer call.Close()

	var listed wire.PullOK
	replyError := call.Reply(&listed)
	if replyError != nil {
		return replyError
	}
	for {
		var envelope *wire.Envelope
		select {
		case <-job.stop:
			{
				_ = call.Cancel()

				return errHalt
			}
		case received, isOpen := <-call.C:
			{
				if !isOpen {
					return wire.ErrClosed
				}
				envelope = received
			}
		case <-session.Done():
			{
				return wire.ErrClosed
			}
		}
		if envelope.Reject != nil {
			return envelope.Reject
		}
		if envelope.Data != nil {
			var chunk []byte
			unmarshalError := wire.Unmarshal(envelope.Data, &chunk)
			if unmarshalError != nil {
				return fmt.Errorf("pull data: %w", unmarshalError)
			}
			_, writeError := file.Write(chunk)
			if writeError != nil {
				return writeError
			}

			continue
		}
		if envelope.End == nil {
			continue
		}
		var ended wire.PullEnd
		_ = wire.Unmarshal(envelope.End, &ended)
		if len(listed.Files) == 0 {
			_ = os.Remove(part)

			return nil
		}
		_, seekError := file.Seek(0, io.SeekStart)
		if seekError != nil {
			return seekError
		}
		hash := sha256.New()
		_, hashError := io.Copy(hash, file)
		if hashError != nil {
			return hashError
		}
		if !bytes.Equal(hash.Sum(nil), ended.Sha256) {
			_ = file.Truncate(0)

			return errors.New("pull: sha256 differs; starting again")
		}
		root, rootError := os.OpenRoot(job.path("files"))
		if rootError != nil {
			return rootError
		}
		_, extractError := wire.Extract(file, root, ".")
		_ = root.Close()
		if extractError != nil {
			return extractError
		}

		return os.Remove(part)
	}
}
