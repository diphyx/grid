package wire

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// EventLog is an append-only file of events: u32 big-endian length + CBOR event, repeated.
// Seq is the position: the first event is 1. A torn last record is cut off on open.
type EventLog struct {
	mutex sync.Mutex
	file  *os.File
	index []int64
	size  int64
}

// OpenEventLog opens or creates the log at path and indexes what is there.
func OpenEventLog(path string) (*EventLog, error) {
	file, openError := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if openError != nil {
		return nil, openError
	}
	info, statError := file.Stat()
	if statError != nil {
		_ = file.Close()

		return nil, statError
	}
	eventLog := &EventLog{file: file, size: info.Size()}
	var offset int64
	var head [4]byte
	for {
		_, headError := file.ReadAt(head[:], offset)
		if headError != nil {
			break
		}
		length := int64(binary.BigEndian.Uint32(head[:]))
		if offset+4+length > eventLog.size {
			break
		}
		eventLog.index = append(eventLog.index, offset)
		offset += 4 + length
	}
	if offset != eventLog.size {
		truncateError := file.Truncate(offset)
		if truncateError != nil {
			_ = file.Close()

			return nil, truncateError
		}
		eventLog.size = offset
	}

	return eventLog, nil
}

// Append adds an event of type typ with data, numbered and timed now.
func (eventLog *EventLog) Append(typ string, data any) (Event, error) {
	eventLog.mutex.Lock()
	defer eventLog.mutex.Unlock()

	event := Event{Seq: uint64(len(eventLog.index) + 1), Time: uint64(time.Now().UnixMilli()), Type: typ, Data: MustRaw(data)}
	body := MustRaw(event)
	record := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(record, uint32(len(body)))
	copy(record[4:], body)
	_, writeError := eventLog.file.WriteAt(record, eventLog.size)
	if writeError != nil {
		return Event{}, writeError
	}
	eventLog.index = append(eventLog.index, eventLog.size)
	eventLog.size += int64(len(record))

	return event, nil
}

// Len is the number of events, which is also the last seq.
func (eventLog *EventLog) Len() uint64 {
	eventLog.mutex.Lock()
	defer eventLog.mutex.Unlock()

	return uint64(len(eventLog.index))
}

// Read returns up to max events after seq after.
func (eventLog *EventLog) Read(after uint64, max int) ([]Event, error) {
	eventLog.mutex.Lock()
	defer eventLog.mutex.Unlock()

	var out []Event
	for i := after; i < uint64(len(eventLog.index)) && len(out) < max; i++ {
		var head [4]byte
		_, headError := eventLog.file.ReadAt(head[:], eventLog.index[i])
		if headError != nil {
			return out, headError
		}
		body := make([]byte, binary.BigEndian.Uint32(head[:]))
		_, bodyError := eventLog.file.ReadAt(body, eventLog.index[i]+4)
		if bodyError != nil && !errors.Is(bodyError, io.EOF) {
			return out, bodyError
		}
		var event Event
		unmarshalError := Unmarshal(body, &event)
		if unmarshalError != nil {
			return out, unmarshalError
		}
		out = append(out, event)
	}

	return out, nil
}

// Close closes the file.
func (eventLog *EventLog) Close() error {
	eventLog.mutex.Lock()
	defer eventLog.mutex.Unlock()

	return eventLog.file.Close()
}
