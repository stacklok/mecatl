package sessnap

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyParentCallStaysPrivate(t *testing.T) {
	raw := []byte(`{"id":"child","state":"idle","mode":"default","kind":"subagent","relationship":{"parent_session_id":"parent","call_id":"raw-parent-provider"},"environment_ref":{"kind":"local","id":"/ws","revision":"v1"},"created_at":"1970-01-01T00:00:00Z","messages":[]}`)
	var dto Snapshot
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Relationship.CallID != "" || dto.Relationship.Legacy == nil || dto.Relationship.Legacy.ProviderCallID != "raw-parent-provider" {
		t.Fatal("old parent ref became canonical")
	}
	before, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Unmarshal(before)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if s.Relationship.CallID != "" || s.Relationship.Legacy == nil || s.Relationship.Legacy.ProviderCallID != "raw-parent-provider" {
			t.Fatal("private parent provenance lost")
		}
		if session.HistoricalRelationship(s.Relationship) != (session.SessionRelationship{}) {
			t.Fatal("unproven parent edge projected")
		}
		snap, err := Of(s)
		if err != nil {
			t.Fatal(err)
		}
		snap.Relationship.Legacy.ProviderCallID = "copy-only"
		if s.Relationship.Legacy.ProviderCallID != "raw-parent-provider" {
			t.Fatal("Of relationship alias")
		}
		encoded, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(encoded, []byte(`"identity_schema":2`)) {
			t.Fatal("schema not upgraded")
		}
		s, err = Unmarshal(encoded)
		if err != nil {
			t.Fatal(err)
		}
	}
}
