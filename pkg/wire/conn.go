package wire

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flynn/noise"
)

const (
	PROLOGUE          = "grid/" + MAJOR // §1: one protocol per generation
	DEFAULT_PORT      = 9420            // §1
	HANDSHAKE_TIMEOUT = 10 * time.Second
	PING_AFTER        = 15 * time.Second // §4
	DEAD_AFTER        = 30 * time.Second // §4
	BEAT_EVERY        = 5 * time.Second  // §6
)

// Suite is Noise_IK_25519_ChaChaPoly_SHA256 (§1).
var Suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

var (
	ErrPeerIP  = errors.New("handshake: ip not allowed")
	ErrPeerKey = errors.New("handshake: key not allowed")
)

// Peers maps an IP to the public key it must hold (§1).
type Peers map[string]Key

// Handshake overrides defaults; tests and vectors use it.
type Handshake struct {
	Prologue string    // "" = Prologue
	Random   io.Reader // nil = crypto/rand; used for the ephemeral key
}

func (handshake *Handshake) prologue() []byte {
	if handshake == nil || handshake.Prologue == "" {
		return []byte(PROLOGUE)
	}
	return []byte(handshake.Prologue)
}

func (handshake *Handshake) random() io.Reader {
	if handshake == nil {
		return nil
	}
	return handshake.Random
}

// Conn is one encrypted connection. Send is safe for concurrent use; Recv is not.
type Conn struct {
	Peer Key

	socket           net.Conn
	sender, receiver *noise.CipherState
	wmu              sync.Mutex
	lastSend         atomic.Int64
	lastRecv         atomic.Int64
	closed           atomic.Bool
}

// Addr formats ip:port; port 0 = DefaultPort.
func Addr(ip string, port int) string {
	if port == 0 {
		port = DEFAULT_PORT
	}

	return net.JoinHostPort(ip, strconv.Itoa(port))
}

// Connect opens TCP and runs the handshake as the initiator.
func Connect(address string, local Keypair, peer Key, handshake *Handshake) (*Conn, error) {
	socket, dialTimeoutError := net.DialTimeout("tcp", address, HANDSHAKE_TIMEOUT)
	if dialTimeoutError != nil {
		return nil, dialTimeoutError
	}
	connection, dialTimeoutError := NewClient(socket, local, peer, handshake)
	if dialTimeoutError != nil {
		socket.Close()

		return nil, dialTimeoutError
	}

	return connection, nil
}

// NewClient runs the handshake as the initiator on nc.
func NewClient(socket net.Conn, local Keypair, peer Key, options *Handshake) (*Conn, error) {
	handshake, newHandshakeStateError := noise.NewHandshakeState(noise.Config{
		CipherSuite:   Suite,
		Random:        options.random(),
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		Prologue:      options.prologue(),
		StaticKeypair: noise.DHKey{Private: local.Private[:], Public: local.Public[:]},
		PeerStatic:    peer[:],
	})
	if newHandshakeStateError != nil {
		return nil, newHandshakeStateError
	}
	_ = socket.SetDeadline(time.Now().Add(HANDSHAKE_TIMEOUT))
	defer func() { _ = socket.SetDeadline(time.Time{}) }()

	message, _, _, newHandshakeStateError := handshake.WriteMessage(nil, nil)
	if newHandshakeStateError != nil {
		return nil, newHandshakeStateError
	}
	writeFrameError := WriteFrame(socket, message)
	if writeFrameError != nil {
		return nil, writeFrameError
	}
	message, newHandshakeStateError = ReadFrame(socket)
	if newHandshakeStateError != nil {
		return nil, fmt.Errorf("handshake: %w", newHandshakeStateError)
	}
	_, sender, receiver, newHandshakeStateError := handshake.ReadMessage(nil, message)
	if newHandshakeStateError != nil {
		return nil, fmt.Errorf("handshake: %w", newHandshakeStateError)
	}
	return newConn(socket, sender, receiver, peer), nil
}

// NewServer checks the IP, runs the handshake as the responder, then checks the key.
func NewServer(socket net.Conn, local Keypair, peers Peers, handshake *Handshake) (*Conn, error) {
	ip := RemoteIP(socket)
	want, ok := peers[ip]
	if !ok {
		for peerIP, key := range peers {
			if NormIP(peerIP) == ip {
				want, ok = key, true
			}
		}
	}
	if !ok {
		return nil, ErrPeerIP
	}
	state, newHandshakeStateError := noise.NewHandshakeState(noise.Config{
		CipherSuite:   Suite,
		Random:        handshake.random(),
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		Prologue:      handshake.prologue(),
		StaticKeypair: noise.DHKey{Private: local.Private[:], Public: local.Public[:]},
	})
	if newHandshakeStateError != nil {
		return nil, newHandshakeStateError
	}
	_ = socket.SetDeadline(time.Now().Add(HANDSHAKE_TIMEOUT))
	defer func() { _ = socket.SetDeadline(time.Time{}) }()

	message, newHandshakeStateError := ReadFrame(socket)
	if newHandshakeStateError != nil {
		return nil, fmt.Errorf("handshake: %w", newHandshakeStateError)
	}
	_, _, _, readMessageError := state.ReadMessage(nil, message)
	if readMessageError != nil {
		return nil, fmt.Errorf("handshake: %w", readMessageError)
	}
	var got Key
	copy(got[:], state.PeerStatic())
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return nil, ErrPeerKey
	}
	message, receiver, sender, newHandshakeStateError := state.WriteMessage(nil, nil)
	if newHandshakeStateError != nil {
		return nil, newHandshakeStateError
	}
	writeFrameError := WriteFrame(socket, message)
	if writeFrameError != nil {
		return nil, writeFrameError
	}
	return newConn(socket, sender, receiver, got), nil
}

// RemoteIP is the peer's IP without the port; IPv4-mapped IPv6 is unmapped.
func RemoteIP(socket net.Conn) string {
	host, _, splitHostPortError := net.SplitHostPort(socket.RemoteAddr().String())
	if splitHostPortError != nil {
		return socket.RemoteAddr().String()
	}
	return NormIP(host)
}

// NormIP writes an IP the one way Peers keys use.
func NormIP(ip string) string {
	address, parseAddrError := netip.ParseAddr(ip)
	if parseAddrError == nil {
		return address.Unmap().String()
	}
	return ip
}

func newConn(socket net.Conn, sender, receiver *noise.CipherState, peer Key) *Conn {
	connection := &Conn{socket: socket, sender: sender, receiver: receiver, Peer: peer}
	now := time.Now().UnixNano()
	connection.lastSend.Store(now)
	connection.lastRecv.Store(now)

	return connection
}

// WritePlain encrypts and writes one plaintext.
func (connection *Conn) WritePlain(payload []byte) error {
	if len(payload) > MAX_PLAIN {
		return ErrFrameSize
	}
	connection.wmu.Lock()
	defer connection.wmu.Unlock()

	ciphertext, encryptError := connection.sender.Encrypt(nil, nil, payload)
	if encryptError != nil {
		return encryptError
	}
	// a peer that stops reading must not hold the writer forever (§4: 30 s silent is dead)
	_ = connection.socket.SetWriteDeadline(time.Now().Add(DEAD_AFTER))
	writeFrameError := WriteFrame(connection.socket, ciphertext)
	if writeFrameError != nil {
		connection.Close() // a frame cut short leaves the stream unreadable

		return writeFrameError
	}
	connection.lastSend.Store(time.Now().UnixNano())

	return nil
}

// ReadPlain reads and decrypts one plaintext.
func (connection *Conn) ReadPlain() ([]byte, error) {
	ciphertext, readFrameError := ReadFrame(connection.socket)
	if readFrameError != nil {
		return nil, readFrameError
	}
	payload, readFrameError := connection.receiver.Decrypt(nil, nil, ciphertext)
	if readFrameError != nil {
		return nil, readFrameError
	}
	connection.lastRecv.Store(time.Now().UnixNano())

	return payload, nil
}

// Send encodes and writes one envelope.
func (connection *Conn) Send(envelope *Envelope) error {
	content, marshalError := Marshal(envelope)
	if marshalError != nil {
		return marshalError
	}
	return connection.WritePlain(content)
}

// Recv reads one envelope, ping and pong included.
func (connection *Conn) Recv() (*Envelope, error) {
	payload, readPlainError := connection.ReadPlain()
	if readPlainError != nil {
		return nil, readPlainError
	}
	var envelope Envelope
	unmarshalError := Unmarshal(payload, &envelope)
	if unmarshalError != nil {
		return nil, unmarshalError
	}
	return &envelope, nil
}

// Keepalive sends ping after PingAfter idle and closes after DeadAfter silent (§4).
// It returns when the connection closes.
func (connection *Conn) Keepalive() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if connection.closed.Load() {
			return
		}
		now := time.Now().UnixNano()
		if time.Duration(now-connection.lastRecv.Load()) > DEAD_AFTER {
			connection.Close()

			return
		}
		if time.Duration(now-connection.lastSend.Load()) > PING_AFTER {
			_ = connection.Send(&Envelope{Ping: &Empty{}})
		}
	}
}

// Close closes the connection.
func (connection *Conn) Close() error {
	connection.closed.Store(true)

	return connection.socket.Close()
}
