package sessnap

import (
	"encoding/json"
	"testing"
)

func TestLegacyParentCallRejected(t *testing.T) {
	raw := []byte(`{"id":"child","state":"idle","mode":"default","kind":"subagent","relationship":{"parent_session_id":"parent","call_id":"raw-parent-provider"},"environment_ref":{"kind":"local","id":"/ws","revision":"v1"},"created_at":"1970-01-01T00:00:00Z","messages":[]}`)
	var dto Snapshot
	if err := json.Unmarshal(raw, &dto); err == nil {
		t.Fatal("accepted unprovable legacy delegation relationship")
	}
	if _, err := Unmarshal(raw); err == nil {
		t.Fatal("restored unprovable legacy delegation relationship")
	}
}
