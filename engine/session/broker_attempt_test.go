package session_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func slotSession(t *testing.T) (*session.Session, session.BrokerSessionRef, session.BrokerCatalogueRef, time.Time) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	ref := session.BrokerSessionRef(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	cat := session.BrokerCatalogueRef(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	s := session.New("occurrence", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "occurrence", Revision: "1"}, session.Limits{}, now)
	if err := s.BindAuthority(session.Authority{Provenance: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptBrokerCatalogue(ref, cat, session.BrokerConnectionRef(cat), now.Add(time.Hour), []string{"remote"}); err != nil {
		t.Fatal(err)
	}
	return s, ref, cat, now
}

func TestBrokerOccurrencePreflightAndPair(t *testing.T) {
	s, ref, cat, now := slotSession(t)
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("reused-provider-id", "remote", []byte(`{}`))
	var previous session.BrokerAttempt
	for range 4 {
		if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
			t.Fatal(err)
		}
		a, err := s.PrepareBrokerInvocation(ref, cat, call, now)
		if err != nil || !a.Valid() || a == previous {
			t.Fatalf("occurrence: %+v %v", a, err)
		}
		access, _ := s.BrokerAccess()
		if access.Current != nil {
			t.Fatal("preflight persisted uncertainty")
		}
		if err := s.DispatchBrokerInvocation(a); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordBrokerInvocationResult(previous, call.ID); err == nil {
			t.Fatal("old receipt matched reused provider ID")
		}
		if err := s.RecordBrokerInvocationResult(a, call.ID); err != nil {
			t.Fatal(err)
		}
		access, _ = s.BrokerAccess()
		if access.Current == nil {
			t.Fatal("cleared before paired result")
		}
		if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult(call.ID, "ok")}); err != nil {
			t.Fatal(err)
		}
		access, _ = s.BrokerAccess()
		if access.Current != nil {
			t.Fatal("paired result did not clear marker")
		}
		previous = a
	}
}

func TestBrokerOccurrenceRestoredUncertaintyNeverResends(t *testing.T) {
	s, ref, cat, now := slotSession(t)
	call := session.NewToolCall("same", "remote", []byte(`{"sensitive":"not-in-metadata"}`))
	a, err := s.PrepareBrokerInvocation(ref, cat, call, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchBrokerInvocation(a); err != nil {
		t.Fatal(err)
	}
	snap, err := sessnap.Of(s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(snap)
	if err != nil || bytes.Contains(b, []byte("not-in-metadata")) {
		t.Fatalf("snapshot: %v", err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restored.PrepareBrokerInvocation(ref, cat, call, now); err == nil {
		t.Fatal("restored uncertainty permitted resend")
	}
	if err := restored.DispatchBrokerInvocation(a); err == nil {
		t.Fatal("restored uncertainty dispatched")
	}
	if err := restored.RecordBrokerInvocationResult(a, call.ID); err == nil {
		t.Fatal("restored uncertainty accepted receipt")
	}
	if err := restored.AdoptBrokerCatalogue(ref, cat, session.BrokerConnectionRef(cat), now.Add(time.Hour), []string{"remote"}); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.PrepareBrokerInvocation(ref, cat, call, now); err == nil {
		t.Fatal("catalogue adoption cleared uncertainty")
	}
}

func TestBrokerOccurrenceSyntheticErrorAndLegacyState(t *testing.T) {
	s, ref, cat, now := slotSession(t)
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("same", "remote", []byte(`{}`))
	if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	a, err := s.PrepareBrokerInvocation(ref, cat, call, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchBrokerInvocation(a); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolResults([]session.ToolResult{session.NewToolError(call.ID, "synthetic interruption")}); err != nil {
		t.Fatal(err)
	}
	access, _ := s.BrokerAccess()
	if access.Current == nil {
		t.Fatal("synthetic error cleared uncertainty")
	}
	access.Current.Attempt = session.BrokerAttempt{}
	if err := json.Unmarshal([]byte(`{"slot":0,"sequence":7}`), &access.Current.Attempt); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreBrokerAccess(access); err == nil {
		t.Fatal("legacy unresolved slot accepted")
	}
}
