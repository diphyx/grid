package kit

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/diphyx/grid/v2/pkg/wire"
)

func (nodeRun *nodeRun) files(session *wire.Session) {
	job, checkError := openJob(session, nodeRun.target.Tag)
	if !nodeRun.report.Check("§6", "open for files", checkError) {
		return
	}
	defer func() { _ = session.Do(wire.CALL_DROP, wire.JobRef{Job: job}, nil) }()

	at := time.Unix(1700000000, 0)
	small := wire.TarBytes([]wire.Entry{
		{Path: "in", Dir: true, ModTime: at},
		{Path: "in/a.txt", Data: []byte("alpha"), ModTime: at},
		{Path: "hello.txt", Data: []byte("hi"), ModTime: at},
	})
	nodeRun.report.Check("§10", "push", nodeRun.push(session, job, "", small, -1))

	// pull a file back
	before := uint64(time.Now().UnixMilli())
	files, body, sum, checkError := pull(session, wire.Pull{Job: job, Path: "hello.txt"})
	if checkError == nil {
		checkError = wantTar(body, sum, "hello.txt", "hi")
	}
	if checkError == nil && (len(files) != 1 || files[0].Path != "hello.txt" || files[0].Size != 2) {
		checkError = fmt.Errorf("files %+v", files)
	}
	nodeRun.report.Check("§10", "pull a file", checkError)

	_, body2, sum2, checkError := pull(session, wire.Pull{Job: job, Path: "in"})
	if checkError == nil {
		checkError = wantTar(body2, sum2, "in/a.txt", "alpha")
	}
	nodeRun.report.Check("§10", "pull a folder, entries relative to the space", checkError)

	if len(body) > 10 {
		_, part, psum, pullError := pull(session, wire.Pull{Job: job, Path: "hello.txt", Offset: 10})
		if pullError == nil && (!bytes.Equal(part, body[10:]) || !bytes.Equal(psum, sum)) {
			pullError = errors.New("bytes or sha256 differ from the full pull")
		}
		nodeRun.report.Check("§10", "pull offset resumes", pullError)
	}

	files, _, _, checkError = pull(session, wire.Pull{Job: job, Path: "hello.txt", Since: uint64(time.Now().Add(time.Hour).UnixMilli())})
	if checkError == nil && len(files) != 0 {
		checkError = fmt.Errorf("files %+v, want none", files)
	}
	nodeRun.report.Check("§10", "pull since → only newer", checkError)

	// a cmd writes, pull since picks it up
	tail, checkError := startTail(session, job, 0)
	if checkError == nil {
		defer func() { _ = tail.call.Cancel() }()

		if nodeRun.run(session, tail, job, wire.Exec{Job: job, Cmd: "write", Args: []string{"out/r.txt", "made"}}, wire.STATE_DONE) {
			_, content, sum, pullError := pull(session, wire.Pull{Job: job, Path: "out", Since: before - 1000})
			if pullError == nil {
				pullError = wantTar(content, sum, "out/r.txt", "made")
			}
			nodeRun.report.Check("§10", "pull a cmd's output", pullError)
			_, content, sum, pullError = pull(session, wire.Pull{Job: job, Path: "result.json"})
			if pullError == nil {
				pullError = wantTar(content, sum, "result.json", `{"ok":true}`)
			}
			nodeRun.report.Check("§9", "result.json", pullError)
		}
	}

	// push into a folder
	nodeRun.report.Check("§10", "push path", nodeRun.push(session, job, "sub", wire.TarBytes([]wire.Entry{{Path: "z.txt", Data: []byte("z"), ModTime: at}}), -1))
	_, content, checksum, checkError := pull(session, wire.Pull{Job: job, Path: "sub/z.txt"})
	if checkError == nil {
		checkError = wantTar(content, checksum, "sub/z.txt", "z")
	}
	nodeRun.report.Check("§10", "push path → entries under it", checkError)

	// resume a broken push
	big := make([]byte, 3*wire.MAX_CHUNK)
	rand.Read(big)
	bigTar := wire.TarBytes([]wire.Entry{{Path: "big.bin", Data: big, ModTime: at}})
	nodeRun.report.Check("§10", "push resumes at the node's offset", nodeRun.pushBroken(job, bigTar))
	_, content, checksum, checkError = pull(session, wire.Pull{Job: job, Path: "big.bin"})
	if checkError == nil {
		checkError = wantTar(content, checksum, "big.bin", string(big))
	}
	nodeRun.report.Check("§10", "resumed push is whole", checkError)
	nodeRun.report.Check("§4", "pull cancel → end", pullCancel(session, job, "big.bin"))

	// bad paths
	nodeRun.report.Check("§5", "push path ../ → invalid", wantReject(nodeRun.push(session, job, "../x", small, -1), wire.INVALID))
	nodeRun.report.Check("§5", "pull path ../ → invalid", wantReject(session.Do(wire.CALL_PULL, wire.Pull{Job: job, Path: "../x"}, nil), wire.INVALID))
	nodeRun.report.Check("§10", "tar entry leaving the space → invalid", wantReject(nodeRun.push(session, job, "", rawTar("../evil.txt", "", "x"), -1), wire.INVALID))
	nodeRun.report.Check("§10", "tar symlink out → invalid", wantReject(nodeRun.push(session, job, "", rawTar("l", "../../etc/passwd", ""), -1), wire.INVALID))
}

// push sends a tar; stop >= 0 sends only that many bytes and returns.
func (nodeRun *nodeRun) push(session *wire.Session, job, path string, tarb []byte, stop int) error {
	return nodeRun.pushFrom(session, job, path, tarb, stop, -1)
}

// pushFrom pushes tarb, sending up to byte stop (-1 = all and end); with want >= 0, the node
// must answer that offset.
func (nodeRun *nodeRun) pushFrom(session *wire.Session, job, path string, tarb []byte, stop, want int) error {
	call, startError := session.Start(wire.CALL_PUSH, wire.Push{Job: job, Path: path})
	if startError != nil {
		return startError
	}
	defer call.Close()

	envelope, startError := call.Next()
	if startError != nil {
		return startError
	}
	if envelope.Reject != nil {
		return envelope.Reject
	}
	if envelope.Data == nil {
		return fmt.Errorf("first frame %q, want data {offset}", envelope.Kind())
	}
	var off wire.PushOffset
	unmarshalError := wire.Unmarshal(envelope.Data, &off)
	if unmarshalError != nil {
		return fmt.Errorf("data {offset}: %w", unmarshalError)
	}
	if off.Offset > uint64(len(tarb)) {
		return fmt.Errorf("offset %d past %d bytes", off.Offset, len(tarb))
	}
	if want >= 0 && off.Offset != uint64(want) {
		return fmt.Errorf("offset %d, want %d: the bytes it already has", off.Offset, want)
	}
	end := len(tarb)
	if stop >= 0 {
		end = stop
	}
	for i := int(off.Offset); i < end; i += wire.MAX_CHUNK {
		index := min(i+wire.MAX_CHUNK, end)
		dataError := call.Data(tarb[i:index])
		if dataError != nil {
			return dataError
		}
	}
	if stop >= 0 {
		return nil
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
	sum := sha256.Sum256(tarb)
	if !bytes.Equal(pushed.Sha256, sum[:]) {
		return errors.New("sha256 differs")
	}
	return nil
}

func (nodeRun *nodeRun) pushBroken(job string, tarb []byte) error {
	session, connectError := nodeRun.dial()
	if connectError != nil {
		return connectError
	}
	pushError := nodeRun.push(session, job, "", tarb, 2*wire.MAX_CHUNK)
	if pushError != nil {
		session.Close()

		return pushError
	}
	time.Sleep(500 * time.Millisecond)
	session.Close()
	secondSession, connectError := nodeRun.dial()
	if connectError != nil {
		return connectError
	}
	defer secondSession.Close()

	return nodeRun.pushFrom(secondSession, job, "", tarb, -1, 2*wire.MAX_CHUNK)
}

// §6 drop

// pull runs a pull and collects the stream.
func pull(session *wire.Session, pullBody wire.Pull) ([]wire.File, []byte, []byte, error) {
	call, startError := session.Start(wire.CALL_PULL, pullBody)
	if startError != nil {
		return nil, nil, nil, startError
	}
	defer call.Close()

	var listed wire.PullOK
	replyError := call.Reply(&listed)
	if replyError != nil {
		return nil, nil, nil, replyError
	}
	var body []byte
	for {
		envelope, nextError := call.Next()
		if nextError != nil {
			return nil, nil, nil, nextError
		}
		switch {
		case envelope.Reject != nil:
			{
				return nil, nil, nil, envelope.Reject
			}
		case envelope.Data != nil:
			{
				var chunk []byte
				unmarshalError := wire.Unmarshal(envelope.Data, &chunk)
				if unmarshalError != nil {
					return nil, nil, nil, fmt.Errorf("data: want bytes: %w", unmarshalError)
				}
				if len(chunk) > wire.MAX_CHUNK {
					return nil, nil, nil, fmt.Errorf("chunk %d bytes > %d", len(chunk), wire.MAX_CHUNK)
				}
				body = append(body, chunk...)
			}
		case envelope.End != nil:
			{
				var end wire.PullEnd
				unmarshalError := wire.Unmarshal(envelope.End, &end)
				if unmarshalError != nil {
					return nil, nil, nil, unmarshalError
				}
				if len(listed.Files) == 0 && len(body) > 0 {
					return nil, nil, nil, errors.New("no files but bytes sent")
				}
				return listed.Files, body, end.Sha256, nil
			}
		}
	}
}

// wantTar checks the sha256 of the whole stream and one entry's content.
func wantTar(body, sum []byte, path, content string) error {
	digest := sha256.Sum256(body)
	if !bytes.Equal(digest[:], sum) {
		return errors.New("sha256 differs")
	}
	entries, readTarError := wire.ReadTar(bytes.NewReader(body))
	if readTarError != nil {
		return fmt.Errorf("tar: %w", readTarError)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Path >= entries[i].Path {
			return fmt.Errorf("tar not sorted: %q before %q", entries[i-1].Path, entries[i].Path)
		}
	}
	for _, entry := range entries {
		if entry.Path == path {
			if string(entry.Data) != content {
				return fmt.Errorf("%s: content differs", path)
			}
			return nil
		}
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Path)
	}

	return fmt.Errorf("%s not in tar %v", path, names)
}

// pullCancel starts a pull, cancels it after the first chunk, and wants end (§4).
func pullCancel(session *wire.Session, job, path string) error {
	call, startError := session.Start(wire.CALL_PULL, wire.Pull{Job: job, Path: path})
	if startError != nil {
		return startError
	}
	defer call.Close()

	replyError := call.Reply(nil)
	if replyError != nil {
		return replyError
	}
	isCanceled := false
	deadline := time.After(10 * time.Second)
	for {
		select {
		case envelope := <-call.C:
			{
				switch {
				case envelope.Reject != nil:
					{
						return envelope.Reject
					}
				case envelope.End != nil:
					{
						return nil
					}
				case envelope.Data != nil && !isCanceled:
					{
						isCanceled = true
						cancelError := call.Cancel()
						if cancelError != nil {
							return cancelError
						}
					}
				}
			}
		case <-deadline:
			{
				return errors.New("no end in 10 s after cancel")
			}
		}
	}
}
