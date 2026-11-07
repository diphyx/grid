package kit

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

func openJob(session *wire.Session, tag string) (string, error) {
	var opened wire.OpenOK
	doError := session.Do(wire.CALL_OPEN, wire.Open{Tag: tag}, &opened)

	return opened.Job, doError
}

func randJob() string {
	content := make([]byte, 16)
	rand.Read(content)

	return hex.EncodeToString(content)
}

func wantReject(problem error, reason string) error {
	if wire.IsReject(problem, reason) {
		return nil
	}
	if problem == nil {
		return fmt.Errorf("ok, want reject %s", reason)
	}
	return fmt.Errorf("%v, want reject %s", problem, reason)
}

func wantState(state wire.State, want string) error {
	if state.State != want {
		return fmt.Errorf("state %q, want %q", state.State, want)
	}
	return nil
}

func isState(event wire.Event, want string) bool {
	if event.Type != wire.EVENT_STATE {
		return false
	}
	var state wire.State

	return wire.Unmarshal(event.Data, &state) == nil && state.State == want
}

func contains(list []string, text string) bool {
	for _, other := range list {
		if other == text {
			return true
		}
	}

	return false
}

// rawTar writes one entry with no checks, to test that the node rejects it.
func rawTar(name, link, data string) []byte {
	var buffer bytes.Buffer
	tarWriter := tar.NewWriter(&buffer)
	header := &tar.Header{Name: name, Mode: 0o644, ModTime: time.Unix(1700000000, 0), Format: tar.FormatPAX}
	if link != "" {
		header.Typeflag, header.Linkname = tar.TypeSymlink, link
	} else {
		header.Typeflag, header.Size = tar.TypeReg, int64(len(data))
	}
	_ = tarWriter.WriteHeader(header)
	_, _ = tarWriter.Write([]byte(data))
	tarWriter.Close()

	return buffer.Bytes()
}
