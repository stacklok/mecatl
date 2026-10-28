package eventcodec

import (
	"encoding/json"

	"github.com/stacklok/mecatl/engine/session"
)

type wireEvent struct {
	IdentitySchema int `json:"identity_schema"`
	session.Event
	Legacy *session.LegacyEventIdentity `json:"legacy_identity,omitempty"`
}

// Marshal writes schema 2, preserving private old-source provenance atomically.
func Marshal(e session.Event) ([]byte, error) {
	return json.Marshal(wireEvent{IdentitySchema: 2, Event: e, Legacy: e.LegacyIdentity()})
}

func decodeCurrentEvent(data []byte) (session.Event, error) {
	var wire wireEvent
	if err := json.Unmarshal(data, &wire); err != nil {
		return session.Event{}, err
	}
	if wire.Legacy != nil {
		wire.Event = wire.WithLegacyIdentity(*wire.Legacy)
	}
	return wire.Event, nil
}
