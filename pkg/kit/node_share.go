package kit

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// §10 share

func (nodeRun *nodeRun) share(session *wire.Session) {
	first, checkError := openJob(session, nodeRun.target.Tag)
	if !nodeRun.report.Check("§6", "open for share", checkError) {
		return
	}
	defer func() { _ = session.Do(wire.CALL_DROP, wire.JobRef{Job: first}, nil) }()

	// new content each run, so the first push is a miss
	weights := make([]byte, 32)
	rand.Read(weights)
	model := wire.TarBytes([]wire.Entry{{Path: "w.bin", Data: weights, ModTime: time.Unix(1700000000, 0)}})
	id := tarID(model)
	_, _, pushError := pushShare(session, first, "bad", "X", model, false)
	nodeRun.report.Check("§5", "push id not 32 lowercase hex → invalid", wantReject(pushError, wire.INVALID))
	_, _, pushError = pushShare(session, first, "", id, model, false)
	nodeRun.report.Check("§6", "share push without a path → invalid", wantReject(pushError, wire.INVALID))
	if !nodeRun.target.Share {
		nodeRun.report.Skip("§10", "share push, hit, link", "no --share")

		return
	}

	pushed, isHave, pushError := pushShare(session, first, "model", id, model, false)
	if pushError == nil && (isHave || !pushed.Share) {
		pushError = fmt.Errorf("have %v, share %v; want a miss, then in the share", isHave, pushed.Share)
	}
	nodeRun.report.Check("§10", "share push, new id → sent, in the share", pushError)

	second, checkError := openJob(session, nodeRun.target.Tag)
	if !nodeRun.report.Check("§6", "open for a share hit", checkError) {
		return
	}
	defer func() { _ = session.Do(wire.CALL_DROP, wire.JobRef{Job: second}, nil) }()

	pushed, isHave, pushError = pushShare(session, second, "lib/model", id, nil, false)
	sum := sha256.Sum256(model)
	if pushError == nil && (!isHave || !pushed.Share || !bytes.Equal(pushed.Sha256, sum[:])) {
		pushError = fmt.Errorf("have %v, share %v; want have, in the share, the tar's sha256", isHave, pushed.Share)
	}
	nodeRun.report.Check("§10", "share push, known id → have, no bytes", pushError)

	tail, checkError := startTail(session, second, 0)
	if checkError == nil {
		defer func() { _ = tail.call.Cancel() }()

		if nodeRun.run(session, tail, second, wire.Exec{Job: second, Cmd: "copy", Args: []string{"lib/model/w.bin", "out/w.bin"}}, wire.STATE_DONE) {
			_, body, checksum, pullError := pull(session, wire.Pull{Job: second, Path: "out/w.bin"})
			if pullError == nil {
				pullError = wantTar(body, checksum, "out/w.bin", string(weights))
			}
			nodeRun.report.Check("§10", "a cmd reads a share at its path", pullError)
		}
	}

	_, _, pushError = pushShare(session, second, "again", id, []byte("x"), true)
	nodeRun.report.Check("§10", "data after have → invalid", wantReject(pushError, wire.INVALID))
	_, _, pushError = pushShare(session, second, "lib/model", id, nil, false)
	nodeRun.report.Check("§10", "share push at a taken path → invalid", wantReject(pushError, wire.INVALID))
	other := make([]byte, 16)
	rand.Read(other)
	_, _, pushError = pushShare(session, second, "other", hex.EncodeToString(other), model, false)
	nodeRun.report.Check("§10", "id not the tar's → invalid", wantReject(pushError, wire.INVALID))
}

// tarID is a tar's push id: its sha256, first 16 bytes, as hex.
func tarID(tarb []byte) string {
	sum := sha256.Sum256(tarb)

	return hex.EncodeToString(sum[:16])
}

// pushShare pushes tarb with a push id; it reports whether the node said have. With
// isForced, the bytes go even after have.
func pushShare(session *wire.Session, job, path, id string, tarb []byte, isForced bool) (wire.PushOK, bool, error) {
	var pushed wire.PushOK
	call, startError := session.Start(wire.CALL_PUSH, wire.Push{Job: job, Path: path, ID: id})
	if startError != nil {
		return pushed, false, startError
	}
	defer call.Close()

	envelope, nextError := call.Next()
	if nextError != nil {
		return pushed, false, nextError
	}
	if envelope.Reject != nil {
		return pushed, false, envelope.Reject
	}
	if envelope.Data == nil {
		return pushed, false, fmt.Errorf("first frame %q, want data {offset, have}", envelope.Kind())
	}
	var offset wire.PushOffset
	unmarshalError := wire.Unmarshal(envelope.Data, &offset)
	if unmarshalError != nil {
		return pushed, false, fmt.Errorf("data {offset, have}: %w", unmarshalError)
	}
	if offset.Have && offset.Offset != 0 {
		return pushed, true, errors.New("have with an offset")
	}
	if !offset.Have || isForced {
		for i := int(offset.Offset); i < len(tarb); i += wire.MAX_CHUNK {
			dataError := call.Data(tarb[i:min(i+wire.MAX_CHUNK, len(tarb))])
			if dataError != nil {
				return pushed, offset.Have, dataError
			}
		}
	}
	endError := call.End(wire.Empty{})
	if endError != nil {
		return pushed, offset.Have, endError
	}

	return pushed, offset.Have, call.Reply(&pushed)
}
