package kit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"sync"

	"github.com/diphyx/grid/pkg/wire"
)

// Vectors are fixed bytes any implementation can check its encoder against.
type Vectors struct {
	Prologue  string           `json:"prologue"`
	Initiator VectorKeys       `json:"initiator"`
	Responder VectorKeys       `json:"responder"`
	Handshake []string         `json:"handshake"` // frames, hex, length prefix included
	Transport []VectorFrame    `json:"transport"`
	Envelopes []VectorEnvelope `json:"envelopes"`
}

type VectorKeys struct {
	Private   string `json:"private"`   // base64
	Public    string `json:"public"`    // base64
	Ephemeral string `json:"ephemeral"` // private, hex: the 32 bytes the handshake reads as random
}

type VectorFrame struct {
	From  string `json:"from"`
	Plain string `json:"plain"` // the envelope, CBOR hex
	Frame string `json:"frame"` // on the wire, hex
}

type VectorEnvelope struct {
	Name string `json:"name"`
	CBOR string `json:"cbor"`
}

func seed(text string) []byte {
	sum := sha256.Sum256([]byte("grid vector " + text))

	return sum[:]
}

type recConn struct {
	net.Conn

	mutex *sync.Mutex
	log   *[]string
	who   string
}

// Write records each frame before passing it on.
func (recConn recConn) Write(content []byte) (int, error) {
	recConn.mutex.Lock()
	*recConn.log = append(*recConn.log, recConn.who+":"+hex.EncodeToString(content))
	recConn.mutex.Unlock()

	return recConn.Conn.Write(content)
}

// MakeVectors builds the vectors; the same every run.
func MakeVectors() (*Vectors, error) {
	keypair, _ := wire.GenerateKeypair(bytes.NewReader(seed("initiator static")))
	responderKey, _ := wire.GenerateKeypair(bytes.NewReader(seed("responder static")))
	vectors := &Vectors{
		Prologue:  wire.PROLOGUE,
		Initiator: VectorKeys{keypair.Private.String(), keypair.Public.String(), hex.EncodeToString(seed("initiator ephemeral"))},
		Responder: VectorKeys{responderKey.Private.String(), responderKey.Public.String(), hex.EncodeToString(seed("responder ephemeral"))},
	}
	left, right := net.Pipe()
	var mutex sync.Mutex
	var log []string
	initiator := recConn{Conn: left, mutex: &mutex, log: &log, who: "initiator"}
	responder := recConn{Conn: right, mutex: &mutex, log: &log, who: "responder"}
	type response struct {
		connection *wire.Conn
		problem    error
	}
	channel := make(chan response, 1)
	go func() {
		connection, serverError := wire.NewServer(responder, responderKey, wire.Peers{wire.RemoteIP(right): keypair.Public}, &wire.Handshake{Random: bytes.NewReader(seed("responder ephemeral"))})
		channel <- response{connection, serverError}
	}()
	initiatorConnection, clientError := wire.NewClient(initiator, keypair, responderKey.Public, &wire.Handshake{Random: bytes.NewReader(seed("initiator ephemeral"))})
	if clientError != nil {
		return nil, clientError
	}
	result := <-channel
	if result.problem != nil {
		return nil, result.problem
	}
	connection := result.connection
	for _, line := range log {
		vectors.Handshake = append(vectors.Handshake, line[bytes.IndexByte([]byte(line), ':')+1:])
	}
	log = nil

	job := hex.EncodeToString(seed("job")[:16])
	msgs := []struct {
		from     *wire.Conn
		to       *wire.Conn
		who      string
		envelope *wire.Envelope
	}{
		{initiatorConnection, connection, "initiator", &wire.Envelope{ID: 1, Call: wire.CALL_STAT, Body: wire.MustRaw(wire.JobRef{Job: job})}},
		{connection, initiatorConnection, "responder", &wire.Envelope{ID: 1, OK: wire.MustRaw(wire.State{State: wire.STATE_IDLE})}},
	}
	for _, message := range msgs {
		done := make(chan error, 1)
		go func() { _, recvError := message.to.Recv(); done <- recvError }()
		sendError := message.from.Send(message.envelope)
		if sendError != nil {
			return nil, sendError
		}
		doneError := <-done
		if doneError != nil {
			return nil, doneError
		}
		plain, _ := wire.Marshal(message.envelope)
		frame := log[len(log)-1]
		vectors.Transport = append(vectors.Transport, VectorFrame{From: message.who, Plain: hex.EncodeToString(plain), Frame: frame[bytes.IndexByte([]byte(frame), ':')+1:]})
	}
	initiatorConnection.Close()
	connection.Close()

	sum := sha256.Sum256(nil)
	event := wire.Event{Seq: 1, Time: 1700000000000, Type: wire.EVENT_LOG, Data: wire.MustRaw(wire.Log{Stream: "out", Line: "hello"})}
	for _, sample := range []struct {
		name     string
		envelope *wire.Envelope
	}{
		{"call open", &wire.Envelope{ID: 1, Call: wire.CALL_OPEN, Body: wire.MustRaw(wire.Open{Tag: "build"})}},
		{"ok open", &wire.Envelope{ID: 1, OK: wire.MustRaw(wire.OpenOK{Job: job, Expires: 600000})}},
		{"call exec", &wire.Envelope{ID: 2, Call: wire.CALL_EXEC, Body: wire.MustRaw(wire.Exec{Job: job, Cmd: "build", Args: []string{"linux"}, Env: map[string]string{"CC": "clang"}})}},
		{"ok empty", &wire.Envelope{ID: 2, OK: wire.MustRaw(wire.Empty{})}},
		{"reject", &wire.Envelope{ID: 3, Reject: &wire.Reject{Reason: wire.RUNNING, Message: "an exec is on"}}},
		{"data event", &wire.Envelope{ID: 4, Data: wire.MustRaw(event)}},
		{"data bytes", &wire.Envelope{ID: 5, Data: wire.MustRaw([]byte("chunk"))}},
		{"data push offset", &wire.Envelope{ID: 5, Data: wire.MustRaw(wire.PushOffset{Offset: 0})}},
		{"call push share", &wire.Envelope{ID: 7, Call: wire.CALL_PUSH, Body: wire.MustRaw(wire.Push{Job: job, Path: "model", ID: hex.EncodeToString(sum[:16])})}},
		{"data push have", &wire.Envelope{ID: 7, Data: wire.MustRaw(wire.PushOffset{Have: true})}},
		{"ok push share", &wire.Envelope{ID: 7, OK: wire.MustRaw(wire.PushOK{Sha256: sum[:], Share: true})}},
		{"call beat", &wire.Envelope{ID: 8, Call: wire.CALL_BEAT, Body: wire.MustRaw(wire.Beat{Tags: []string{"build"}, Free: 2, Share: true})}},
		{"end pull", &wire.Envelope{ID: 6, End: wire.MustRaw(wire.PullEnd{Sha256: sum[:]})}},
		{"cancel", &wire.Envelope{ID: 4, Cancel: &wire.Empty{}}},
		{"ping", &wire.Envelope{Ping: &wire.Empty{}}},
		{"pong", &wire.Envelope{Pong: &wire.Empty{}}},
	} {
		content, marshalError := wire.Marshal(sample.envelope)
		if marshalError != nil {
			return nil, marshalError
		}
		vectors.Envelopes = append(vectors.Envelopes, VectorEnvelope{sample.name, hex.EncodeToString(content)})
	}

	return vectors, nil
}
