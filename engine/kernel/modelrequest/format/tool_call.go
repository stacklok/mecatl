package format

import (
	"errors"
	"unicode/utf8"
)

// ToolCall retains an original requested call, not its effective invocation.
// Assignment shares Arguments; Clone detaches them. Zero is invalid.
//
//dtogen:validate Validate
type ToolCall struct {
	// ProviderCallID is required nonempty exact UTF-8, unique within its assistant
	// response, not across responses. It is not a kernel occurrence identity.
	ProviderCallID string `cbor:"i"`
	// Name is exact UTF-8 and may be empty or unavailable.
	Name string `cbor:"n"`
	// Arguments is opaque, including malformed/non-JSON bytes. Nil and empty are
	// equivalent. Validation must not parse, repair, or substitute these bytes.
	Arguments []byte `cbor:"a"`
}

// Validate checks identifier syntax only, never argument meaning.
func (v ToolCall) Validate() error {
	if v.ProviderCallID == "" || !utf8.ValidString(v.ProviderCallID) || !utf8.ValidString(v.Name) {
		return errors.New("call requires a nonempty UTF-8 provider ID and UTF-8 name")
	}
	return nil
}
