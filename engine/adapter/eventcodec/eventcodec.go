// Package eventcodec is the shared local identity codec for all typed event logs.
// Outer transport format tags are deliberately owned by the store/driver adapters.
package eventcodec

import "github.com/stacklok/mecatl/engine/session"

// Unmarshal selects a version-specific decoder before interpreting invocation
// references. Unsupported well-formed old records return ErrSkipLegacyRecord;
// current records decode directly.
func Unmarshal(data []byte) (session.Event, error) {
	schema, err := identitySchema(data)
	if err != nil {
		return session.Event{}, err
	}
	return decodeEvent(data, schema)
}

// decodeEvent requires an already validated schema discriminator.
func decodeEvent(data []byte, schema int) (session.Event, error) {
	if schema == 0 {
		return decodeLegacyEvent(data)
	}
	return decodeCurrentEvent(data)
}
