package sessnap_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

var (
	effectiveCallArgs = json.RawMessage("{\n  \"title\" : \"effective review\"  \n}")
	deferredArgs      = json.RawMessage("{ \"after\" :  \"<tag>&value\u2028next\u2029end>\" }")
)

func authorizingSnapshotSession(t *testing.T) *session.Session {
	t.Helper()
	s := session.New("authorizing", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0))
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	calls := []session.ToolCall{
		session.NewToolCall("done", "Read", json.RawMessage(`{"path":"done"}`)),
		session.NewToolCall("parked", "external_create", json.RawMessage(`{"title":"model review"}`)),
		session.NewToolCall("later", "external_list", append(json.RawMessage(nil), deferredArgs...)),
	}
	calls[1].ItemID = "provider-item-parked"
	calls[2].ItemID = "provider-item-later"
	if err := s.RecordAssistant(session.NewAssistantMessage("", "", calls)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult("done", "ok")}); err != nil {
		t.Fatal(err)
	}
	effectiveCall := calls[1]
	effectiveCall.Args = append(json.RawMessage(nil), effectiveCallArgs...)
	if err := s.PauseForAuthorization(session.PendingAuthorization{
		Authorization: session.ExternalAuthorization{
			ID:          "authorization-1",
			DisplayName: "GitHub Enterprise",
			Binding:     session.AuthorizationBinding("opaque/binding value\nwith separator"),
			ExpiresAt:   time.Unix(1_800_000_000, 0),
		},
		Call: effectiveCall, Deferred: []session.ToolCall{calls[2]},
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuthorizingSnapshotRoundTrip(t *testing.T) {
	want := authorizingSnapshotSession(t)
	got, err := sessnap.Unmarshal(mustMarshal(t, want))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pending, ok := got.PendingAuthorization()
	if got.State != session.StateAuthorizing || !ok {
		t.Fatalf("round trip = state %q pending %+v, %v", got.State, pending, ok)
	}
	wantExpiry := time.Unix(1_800_000_000, 0)
	if pending.Authorization.DisplayName != "GitHub Enterprise" {
		t.Fatalf("DisplayName = %q", pending.Authorization.DisplayName)
	}
	if pending.Authorization.Binding != "opaque/binding value\nwith separator" {
		t.Fatalf("Binding = %q", pending.Authorization.Binding)
	}
	if pending.Authorization.ID != "authorization-1" || !pending.Authorization.ExpiresAt.Equal(wantExpiry) || pending.Call.ID != "parked" || pending.Call.ItemID != "provider-item-parked" || len(pending.Deferred) != 1 || pending.Deferred[0].ID != "later" || pending.Deferred[0].ItemID != "provider-item-later" {
		t.Fatalf("PendingAuthorization = %+v", pending)
	}
	if !bytes.Equal(pending.Call.Args, effectiveCallArgs) || !bytes.Equal(pending.Deferred[0].Args, deferredArgs) {
		t.Fatalf("effective args changed: call=%q deferred=%q", pending.Call.Args, pending.Deferred[0].Args)
	}
	claimed, err := got.ClaimAuthorization()
	if err != nil {
		t.Fatalf("ClaimAuthorization: %v", err)
	}
	if !bytes.Equal(claimed.Call.Args, effectiveCallArgs) || !bytes.Equal(claimed.Deferred[0].Args, deferredArgs) {
		t.Fatalf("claimed effective args changed: call=%q deferred=%q", claimed.Call.Args, claimed.Deferred[0].Args)
	}
}

func TestAuthorizingSnapshotFailsClosed(t *testing.T) {
	s := authorizingSnapshotSession(t)
	snap, err := sessnap.Of(s)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	data := sessnap.RestoreData{State: snap.State, Stop: snap.StopReason, Pending: snap.Pending, Counters: snap.Counters, TokenUsage: map[session.UsageKind]session.TokenUsage{}}
	if err := sessnap.RestoreState(session.New("old", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Time{}), data); err == nil {
		t.Fatal("public RestoreState accepted authorizing state")
	}
	snap.PendingAuthorization = nil
	if _, err := snap.Restore(); err == nil {
		t.Fatal("Restore accepted authorizing snapshot without pending state")
	}

	malformed := session.New("malformed", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Time{})
	malformed.State = session.StateAuthorizing
	if _, err := sessnap.Of(malformed); err == nil {
		t.Fatal("Of accepted authorizing session without pending state")
	}
}
