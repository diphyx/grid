package wire

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	MAX_FRAME = 65535          // §3: max payload on the wire
	MAX_PLAIN = MAX_FRAME - 16 // §3: max plaintext after the Noise tag
	MAX_CHUNK = 60 * 1024      // §10: max bytes in one data chunk
)

var ErrFrameSize = errors.New("frame: too large")

// WriteFrame writes `| u16 big-endian length | payload |` (§3).
func WriteFrame(writer io.Writer, payload []byte) error {
	if len(payload) > MAX_FRAME {
		return ErrFrameSize
	}
	content := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(content, uint16(len(payload)))
	copy(content[2:], payload)
	_, writeError := writer.Write(content)

	return writeError
}

// ReadFrame reads one frame payload.
func ReadFrame(reader io.Reader) ([]byte, error) {
	var head [2]byte
	_, headError := io.ReadFull(reader, head[:])
	if headError != nil {
		return nil, headError
	}
	payload := make([]byte, binary.BigEndian.Uint16(head[:]))
	_, bodyError := io.ReadFull(reader, payload)
	if bodyError != nil {
		return nil, bodyError
	}

	return payload, nil
}
