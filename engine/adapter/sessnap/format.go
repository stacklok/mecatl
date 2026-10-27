package sessnap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// Format is the newest snapshot encoding. Readers also accept sessnap-json/1.
const Format = "sessnap-json/2"

// UnmarshalFormat rejects contradictory outer and local identity versions.
func UnmarshalFormat(data []byte, format string) (*session.Session, error) {
	if format != "sessnap-json/1" && format != Format {
		return nil, fmt.Errorf("sessnap: unknown snapshot format %q", format)
	}
	schema, err := identitySchema(data)
	if err != nil {
		return nil, err
	}
	if (format == Format) != (schema == 2) {
		return nil, fmt.Errorf("sessnap: format %q does not match identity schema %d", format, schema)
	}
	return Unmarshal(data)
}

// UnmarshalJSON selects a version-specific decoder before interpreting invocation
// references. Both paths yield only current-schema data; Restore does not migrate.
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	schema, err := identitySchema(data)
	if err != nil {
		return err
	}
	var decoded Snapshot
	if schema == 0 {
		decoded, err = decodeLegacySnapshot(data)
	} else {
		decoded, err = decodeCurrentSnapshot(data)
	}
	if err != nil {
		return err
	}
	*s = decoded
	return nil
}

func decodeCurrentSnapshot(data []byte) (Snapshot, error) {
	// A defined type retains the full current DTO's fields but has no JSON
	// methods, avoiding recursive calls to Snapshot.UnmarshalJSON.
	type currentSnapshot Snapshot
	var decoded struct {
		currentSnapshot
		// Shadow messages to require its presence: null is valid empty history,
		// but an absent field must not silently discard the conversation.
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return Snapshot{}, err
	}
	if err := json.Unmarshal(decoded.Messages, &decoded.currentSnapshot.Messages); err != nil {
		return Snapshot{}, err
	}
	inc, err := snapshotIncarnation(decoded.Incarnation, decoded.ID, decoded.CreatedAt, decoded.Owner)
	if err != nil {
		return Snapshot{}, err
	}
	decoded.Incarnation = inc
	return Snapshot(decoded.currentSnapshot), nil
}

func snapshotIncarnation(inc session.IncarnationID, id session.SessionID, createdAt time.Time, owner *session.Principal) (session.IncarnationID, error) {
	if inc == "" {
		return session.LegacyIncarnationID(id, createdAt.UnixNano(), owner), nil
	}
	if !inc.Valid() {
		return "", errors.New("sessnap: invalid incarnation")
	}
	return inc, nil
}

func identitySchema(data []byte) (int, error) {
	if !json.Valid(data) {
		return 0, fmt.Errorf("sessnap: invalid snapshot JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return 0, fmt.Errorf("sessnap: snapshot must be an object")
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
		// identity_hold is never supported data, even when null or case-varied.
		if strings.EqualFold(key.(string), "identity_hold") {
			return 0, errors.New("sessnap: unsupported identity_hold")
		}
		if !strings.EqualFold(key.(string), "identity_schema") {
			continue
		}
		if found {
			return 0, fmt.Errorf("sessnap: conflicting identity schema fields")
		}
		found = true
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return 0, err
		}
		if n != 2 {
			return 0, fmt.Errorf("sessnap: unknown identity schema %d", n)
		}
	}
	if found {
		return 2, nil
	}
	return 0, nil
}
