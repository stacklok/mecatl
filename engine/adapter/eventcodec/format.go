package eventcodec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

// Format is the newest event encoding. Readers also accept eventlog-json/1.
const Format = "eventlog-json/2"

// UnmarshalFormat rejects contradictory outer and local identity versions.
func UnmarshalFormat(data []byte, format string) (session.Event, error) {
	if format != "eventlog-json/1" && format != Format {
		return session.Event{}, fmt.Errorf("eventcodec: unknown event format %q", format)
	}
	schema, err := identitySchema(data)
	if err != nil {
		return session.Event{}, err
	}
	if (format == Format) != (schema == 2) {
		return session.Event{}, fmt.Errorf("eventcodec: format %q does not match identity schema %d", format, schema)
	}
	return Unmarshal(data)
}

func identitySchema(data []byte) (int, error) {
	if !json.Valid(data) {
		return 0, fmt.Errorf("eventcodec: invalid event JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return 0, fmt.Errorf("eventcodec: event must be an object")
	}
	found := false
	// Do not use a map: it would silently accept repeated discriminators.
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return 0, err
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return 0, err
		}
		if !strings.EqualFold(key.(string), "identity_schema") {
			continue
		}
		if found {
			return 0, fmt.Errorf("eventcodec: conflicting identity schema fields")
		}
		found = true
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return 0, err
		}
		if n != 2 {
			return 0, fmt.Errorf("eventcodec: unknown identity schema %d", n)
		}
	}
	if found {
		return 2, nil
	}
	return 0, nil
}
