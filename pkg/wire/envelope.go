package wire

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/fxamacker/cbor/v2"
)

// Raw is an undecoded CBOR value.
type Raw = cbor.RawMessage

// Empty is `{}`.
type Empty struct{}

// Envelope is one frame's content (§4): exactly one kind key.
type Envelope struct {
	ID     uint64  `cbor:"id,omitempty"`
	Call   string  `cbor:"call,omitempty"`
	Body   Raw     `cbor:"body,omitempty"`
	OK     Raw     `cbor:"ok,omitempty"`
	Reject *Reject `cbor:"reject,omitempty"`
	Data   Raw     `cbor:"data,omitempty"`
	End    Raw     `cbor:"end,omitempty"`
	Cancel *Empty  `cbor:"cancel,omitempty"`
	Ping   *Empty  `cbor:"ping,omitempty"`
	Pong   *Empty  `cbor:"pong,omitempty"`
}

// Kinds of envelope.
const (
	KIND_CALL   = "call"
	KIND_OK     = "ok"
	KIND_REJECT = "reject"
	KIND_DATA   = "data"
	KIND_END    = "end"
	KIND_CANCEL = "cancel"
	KIND_PING   = "ping"
	KIND_PONG   = "pong"
)

func (envelope *Envelope) kinds() []string {
	var keys []string
	if envelope.Call != "" {
		keys = append(keys, KIND_CALL)
	}
	if envelope.OK != nil {
		keys = append(keys, KIND_OK)
	}
	if envelope.Reject != nil {
		keys = append(keys, KIND_REJECT)
	}
	if envelope.Data != nil {
		keys = append(keys, KIND_DATA)
	}
	if envelope.End != nil {
		keys = append(keys, KIND_END)
	}
	if envelope.Cancel != nil {
		keys = append(keys, KIND_CANCEL)
	}
	if envelope.Ping != nil {
		keys = append(keys, KIND_PING)
	}
	if envelope.Pong != nil {
		keys = append(keys, KIND_PONG)
	}

	return keys
}

// Kind is the envelope's kind, or "" if it has none or several.
func (envelope *Envelope) Kind() string {
	keys := envelope.kinds()
	if len(keys) == 1 {
		return keys[0]
	}
	return ""
}

// Check enforces §4: one kind; id on all but ping and pong; a call has a body.
func (envelope *Envelope) Check() error {
	keys := envelope.kinds()
	if len(keys) != 1 {
		return fmt.Errorf("envelope: %d kind keys %v, want 1", len(keys), keys)
	}
	switch keys[0] {
	case KIND_PING, KIND_PONG:
		{
			if envelope.ID != 0 {
				return errors.New("envelope: ping/pong with id")
			}
		}
	default:
		{
			if envelope.ID == 0 {
				return errors.New("envelope: missing id")
			}
		}
	}
	if keys[0] == KIND_CALL && envelope.Body == nil {
		return errors.New("envelope: call without body")
	}
	return nil
}

var (
	encMode cbor.EncMode
	decMode cbor.DecMode
)

func init() {
	var problem error
	encMode, problem = cbor.CoreDetEncOptions().EncMode()
	if problem != nil {
		panic(problem)
	}
	decMode, problem = (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF}).DecMode()
	if problem != nil {
		panic(problem)
	}
}

// Marshal encodes v as deterministic CBOR.
func Marshal(value any) ([]byte, error) { return encMode.Marshal(value) }

// Unmarshal decodes CBOR; unknown keys are ignored (§5).
func Unmarshal(content []byte, value any) error { return decMode.Unmarshal(content, value) }

// MustRaw encodes v or panics; for building envelopes.
func MustRaw(value any) Raw {
	content, marshalError := Marshal(value)
	if marshalError != nil {
		panic(marshalError)
	}

	return content
}

var jsonDec cbor.DecMode

func init() {
	var problem error
	jsonDec, problem = (cbor.DecOptions{DefaultMapType: reflect.TypeOf(map[string]any(nil))}).DecMode()
	if problem != nil {
		panic(problem)
	}
}

// ToAny decodes CBOR into values encoding/json can write.
func ToAny(raw Raw) any {
	var value any
	if jsonDec.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}
