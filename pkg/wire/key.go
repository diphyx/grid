package wire

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// Key is a 32-byte Curve25519 key (§2).
type Key [32]byte

// Keypair is a side's static key pair.
type Keypair struct {
	Private Key
	Public  Key
}

// ParseKey reads a key in standard base64 with padding.
func ParseKey(text string) (Key, error) {
	var key Key
	content, decodeStringError := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if decodeStringError != nil {
		return key, fmt.Errorf("key: %w", decodeStringError)
	}
	if len(content) != len(key) {
		return key, fmt.Errorf("key: %d bytes, want 32", len(content))
	}
	copy(key[:], content)

	return key, nil
}

// String is the key in base64.
func (key Key) String() string { return base64.StdEncoding.EncodeToString(key[:]) }

// IsPath reports whether a `key` value is a path rather than a key: anything that does not
// decode as a key. A key may start with "/", so the first character cannot tell.
func IsPath(value string) bool {
	_, parseError := ParseKey(value)

	return parseError != nil
}

// LoadKey reads a `key` value: the key itself, or a path to a one-line file holding it.
func LoadKey(value string) (Key, error) {
	key, parseError := ParseKey(value)
	if parseError == nil {
		return key, nil
	}
	content, readFileError := os.ReadFile(value)
	if readFileError != nil {
		return Key{}, readFileError
	}

	return ParseKey(string(content))
}

// LoadPrivate reads a `private` key file and derives the public key.
func LoadPrivate(path string) (Keypair, error) {
	content, readFileError := os.ReadFile(path)
	if readFileError != nil {
		return Keypair{}, readFileError
	}
	key, readFileError := ParseKey(string(content))
	if readFileError != nil {
		return Keypair{}, readFileError
	}
	return KeypairFrom(key)
}

// KeypairFrom derives the public key of a private key.
func KeypairFrom(private Key) (Keypair, error) {
	pub, x25519Error := curve25519.X25519(private[:], curve25519.Basepoint)
	if x25519Error != nil {
		return Keypair{}, x25519Error
	}
	keypair := Keypair{Private: private}
	copy(keypair.Public[:], pub)

	return keypair, nil
}

// GenerateKeypair makes a new key pair; r = nil uses crypto/rand.
func GenerateKeypair(reader io.Reader) (Keypair, error) {
	if reader == nil {
		reader = rand.Reader
	}
	var private Key
	_, readFullError := io.ReadFull(reader, private[:])
	if readFullError != nil {
		return Keypair{}, readFullError
	}
	return KeypairFrom(private)
}
