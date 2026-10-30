package sessnap

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestFormatIdentityConsistency(t *testing.T) {
	for _, name := range []string{"legacy-completed", "legacy-retry"} {
		t.Run(name, func(t *testing.T) {
			old := legacyFixture(t, name)
			s, err := UnmarshalFormat(old, "sessnap-json/1")
			if err != nil {
				t.Fatal(err)
			}
			newData, err := Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := UnmarshalFormat(newData, Format)
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := Marshal(restored)
			if err != nil || !bytes.Equal(newData, persisted) {
				t.Fatal("format round trip dropped persisted fields")
			}
			pending, pendingOK := s.PendingAuthorization()
			restoredPending, restoredPendingOK := restored.PendingAuthorization()
			if !sameStoredHistory(t, s.Conversation.Messages, restored.Conversation.Messages) || s.State != restored.State || pendingOK != restoredPendingOK || !reflect.DeepEqual(pending, restoredPending) {
				t.Fatal("migration lost history, lifecycle or pending authorization")
			}
			for _, tc := range []struct {
				format string
				data   []byte
			}{
				{"sessnap-json/1", newData}, {Format, old}, {"sessnap-json/99", newData},
			} {
				if _, err := UnmarshalFormat(tc.data, tc.format); err == nil {
					t.Fatalf("accepted contradictory format %s", tc.format)
				}
			}
			for _, bad := range []string{"null", "0", "1", "99", `"2"`} {
				data := bytes.Replace(newData, []byte(`"identity_schema":2`), []byte(`"identity_schema":`+bad), 1)
				if _, err := Unmarshal(data); err == nil {
					t.Fatalf("accepted local schema %s", bad)
				}
			}
		})
	}
	for _, raw := range []string{`null`, `[]`, `{"identity_schema":2,"IDENTITY_SCHEMA":2}`, `{"identity_schema":2,"identity_schema":2}`, `{"identity_schema":99,"identity_schema":2}`, `{"identity_schema":2} {}`} {
		if _, err := Unmarshal([]byte(raw)); err == nil {
			t.Errorf("accepted invalid schema %s", raw)
		}
		var snap Snapshot
		if err := json.Unmarshal([]byte(raw), &snap); err == nil {
			t.Errorf("direct Snapshot codec accepted invalid schema %s", raw)
		}
	}
}
