package sessnap

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacySnapshotContinuationRejected(t *testing.T) {
	for _, state := range []session.State{session.StateIdle, session.StateRunning, session.StateCompleted, session.StateFailed, session.StateCancelled, session.StateAwaiting, session.StateAuthorizing} {
		for i, pending := range []string{"", `,"pending":null,"pending_authorization":null`, `,"pending":{}`, `,"pending_authorization":{}`, `,"pending":false`, `,"pending_authorization":[]`, `,"pending":0`, `,"pending_authorization":""`} {
			t.Run(string(state)+pending, func(t *testing.T) {
				data := []byte(`{"id":"old","state":"` + string(state) + `","mode":"default","environment_ref":{"Kind":"mem","ID":"workspace","Revision":"v1"}` + pending + `}`)
				reject := state == session.StateAwaiting || state == session.StateAuthorizing || i >= 2
				var snap Snapshot
				err := json.Unmarshal(data, &snap)
				if reject {
					if err == nil {
						t.Fatal("decoded legacy continuation")
					}
					if _, err := UnmarshalFormat(data, "sessnap-json/1"); err == nil {
						t.Fatal("loaded legacy continuation")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				// Use idle for restoration: failed lifecycle requires separate retry facts.
				snap.State = session.StateIdle
				if _, err := snap.Restore(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestSnapshotIdentityHoldRejected(t *testing.T) {
	for _, schema := range []string{"", `"identity_schema":2,`} {
		base := `{` + schema + `"id":"hold-test","mode":"default","state":"idle","messages":[],"environment_ref":{"Kind":"mem","ID":"workspace","Revision":"v1"}`
		var snap Snapshot
		if err := json.Unmarshal([]byte(base+`}`), &snap); err != nil {
			t.Fatalf("decode positive control: %v", err)
		}
		if _, err := snap.Restore(); err != nil {
			t.Fatalf("restore positive control: %v", err)
		}
		if _, err := Unmarshal([]byte(base + `}`)); err != nil {
			t.Fatalf("load positive control: %v", err)
		}
		for _, hold := range []string{`"identity_hold":null`, `"identity_hold":{}`, `"identity_hold":{"unsupported":true}`, `"IDENTITY_HOLD":null`, `"Identity_Hold":{}`, `"identity_hold":{},"IDENTITY_HOLD":null`} {
			data := []byte(base + `,` + hold + `}`)
			if err := json.Unmarshal(data, &snap); err == nil {
				t.Errorf("decoded identity_hold: %s", data)
			}
			if _, err := Unmarshal(data); err == nil {
				t.Errorf("loaded identity_hold: %s", data)
			}
		}
	}
}

func TestLegacyNonpendingControlsPreserved(t *testing.T) {
	retry := restoreFixture(t, legacyFixture(t, "legacy-retry"))
	if err := retry.PrepareFailedStepRetry(); err != nil {
		t.Fatal(err)
	}
	enrollment, _ := enrollmentSnapshotSession(t)
	for _, s := range []*session.Session{retry, enrollment} {
		data, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, "identity_schema")
		data, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalFormat(data, "sessnap-json/1")
		if err != nil {
			t.Fatal(err)
		}
		wantRetry, wantRetryOK := s.FailedStepRetryPending()
		gotRetry, gotRetryOK := got.FailedStepRetryPending()
		wantEnrollment, wantEnrollmentOK := s.PendingWorkspaceEnrollment()
		gotEnrollment, gotEnrollmentOK := got.PendingWorkspaceEnrollment()
		if got.State != s.State || gotRetry != wantRetry || gotRetryOK != wantRetryOK || gotEnrollment != wantEnrollment || gotEnrollmentOK != wantEnrollmentOK {
			t.Fatal("legacy nonpending retry or enrollment changed")
		}
	}
}

func TestLegacyEmptyHistoryDecodeRestore(t *testing.T) {
	for _, messages := range []string{"", `,"messages":null`, `,"messages":[]`} {
		var snap Snapshot
		data := []byte(`{"id":"empty","state":"idle","mode":"default","environment_ref":{"Kind":"mem","ID":"workspace","Revision":"v1"}` + messages + `}`)
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatal(err)
		}
		if _, err := snap.Restore(); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		if s, err := UnmarshalFormat(encoded, Format); err != nil || len(s.Conversation.Messages) != 0 {
			t.Fatalf("empty history roundtrip: %v, %v", s, err)
		}
	}
}

func TestCurrentAuthorizationExactArgumentsAndDeferredOrder(t *testing.T) {
	s := session.New("current-auth", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	calls := []session.ToolCall{
		session.NewToolCall("target", "Shell", []byte(`{"command":"display only"}`)),
		session.NewToolCall("second", "Shell", []byte(`{ "command": "second" }`)),
		session.NewToolCall("third", "Shell", []byte(`{ "command": "third" }`)),
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("", calls)); err != nil {
		t.Fatal(err)
	}
	effective := calls[0]
	effective.Args = []byte("{\n  \"command\":  \"effective <>&\"\n}")
	want := session.PendingAuthorization{Authorization: session.ExternalAuthorization{ID: "auth", Binding: "private-binding", ExpiresAt: time.Unix(100, 0)}, Call: effective, Deferred: calls[1:]}
	if err := s.PauseForAuthorization(want); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		snap, err := Of(s)
		if err != nil {
			t.Fatal(err)
		}
		s, err = snap.Restore()
		if err != nil {
			t.Fatal(err)
		}
		data, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		s = restoreFixture(t, data)
		got, ok := s.PendingAuthorization()
		if !ok || !reflect.DeepEqual(got, want) || bytes.Equal(got.Call.Args, s.Conversation.Messages[0].ToolCalls[0].Args) {
			t.Fatal("private effective arguments or deferred order changed")
		}
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	snap.PendingAuthorization.Deferred[0], snap.PendingAuthorization.Deferred[1] = snap.PendingAuthorization.Deferred[1], snap.PendingAuthorization.Deferred[0]
	if _, err := snap.Restore(); err == nil {
		t.Fatal("accepted reordered deferred suffix")
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnmarshalFormat(data, Format); err == nil {
		t.Fatal("loaded reordered deferred suffix")
	}
}

func TestDirectSnapshotZeroSchemaRejected(t *testing.T) {
	s := session.New("direct", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
	s.Conversation.Append(session.NewAssistantMessage("canonical history", nil))
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	snap.IdentitySchema = 0
	if _, err := snap.Restore(); err == nil {
		t.Fatal("accepted unsupported direct schema zero and discarded canonical history")
	}
}
