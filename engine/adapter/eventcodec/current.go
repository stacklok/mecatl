package eventcodec

import (
	"encoding/json"

	"github.com/stacklok/mecatl/engine/session"
)

type wireEvent struct {
	IdentitySchema int `json:"identity_schema"`
	session.Event
}

// Marshal writes schema 2.
func Marshal(e session.Event) ([]byte, error) {
	return json.Marshal(wireEvent{IdentitySchema: 2, Event: e})
}

func decodeCurrentEvent(data []byte) (session.Event, error) {
	var wire wireEvent
	if err := json.Unmarshal(data, &wire); err != nil {
		return session.Event{}, err
	}
	return wire.Event, nil
}
