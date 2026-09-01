package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidAuthorizationID(t *testing.T) {
	t.Parallel()
	for _, valid := range []string{"authorization-1", "tenant:authorization", "a_b.c", "opaque/path?value%2F"} {
		if !ValidAuthorizationID(valid) {
			t.Errorf("ValidAuthorizationID(%q) = false", valid)
		}
	}
	for _, invalid := range []string{"", " has-space", "line\nbreak", string([]byte{0xff}), strings.Repeat("a", maxAuthorizationValueBytes+1)} {
		if ValidAuthorizationID(invalid) {
			t.Errorf("ValidAuthorizationID(%q) = true", invalid)
		}
	}
}

func authorizationPending() PendingAuthorization {
	call := NewToolCall("call-2", "external_create", json.RawMessage(`{"title":"review"}`))
	call.ItemID = "provider-item-2"
	deferred := NewToolCall("call-3", "external_list", json.RawMessage(`{"after":"today"}`))
	deferred.ItemID = "provider-item-3"
	return PendingAuthorization{
		Authorization: ExternalAuthorization{
			ID:          "authorization-1",
			DisplayName: "Calendar",
			Binding:     AuthorizationBinding("tenant/calendar opaque\nvalue"),
			ExpiresAt:   time.Unix(1_800_000_000, 0),
		},
		Call: call,
		Deferred: []ToolCall{
			deferred,
		},
	}
}

func authorizationSession(t *testing.T) *Session {
	t.Helper()
	s := newTestSession(Limits{})
	mustOK(t, s.BeginTurn())
	pending := authorizationPending()
	originalCall := pending.Call
	originalCall.Args = json.RawMessage(`{"title":"original"}`)
	originalDeferred := pending.Deferred[0]
	mustOK(t, s.RecordAssistant(NewAssistantMessage("", "", []ToolCall{
		NewToolCall("call-1", "Read", json.RawMessage(`{"path":"a"}`)),
		originalCall,
		originalDeferred,
	})))
	mustOK(t, s.RecordToolResults([]ToolResult{NewToolResult("call-1", "a")}))
	return s
}

func TestExternalAuthorizationTransitionAndDeepCopy(t *testing.T) {
	s := authorizationSession(t)
	pending := authorizationPending()
	mustOK(t, s.PauseForAuthorization(pending))
	if s.State != StateAuthorizing {
		t.Fatalf("State = %q, want %q", s.State, StateAuthorizing)
	}
	if _, ok := s.PendingAsk(); ok {
		t.Fatal("PendingAsk unexpectedly set")
	}

	pending.Call.Args[2] = 'X'
	pending.Deferred[0].Args[2] = 'X'
	got, ok := s.PendingAuthorization()
	if !ok || string(got.Call.Args) != `{"title":"review"}` || string(got.Deferred[0].Args) != `{"after":"today"}` {
		t.Fatalf("PendingAuthorization after input mutation = %+v, %v", got, ok)
	}
	got.Call.Args[2] = 'X'
	got.Deferred[0].Args[2] = 'X'
	again, ok := s.PendingAuthorization()
	if !ok || string(again.Call.Args) != `{"title":"review"}` || string(again.Deferred[0].Args) != `{"after":"today"}` {
		t.Fatalf("PendingAuthorization after accessor mutation = %+v, %v", again, ok)
	}

	claimed, err := s.ClaimAuthorization()
	mustOK(t, err)
	if s.State != StateRunning || !reflect.DeepEqual(claimed, again) {
		t.Fatalf("claim = state %q pending %+v", s.State, claimed)
	}
	if _, err := s.ClaimAuthorization(); !errors.Is(err, ErrNoPendingAuthorization) {
		t.Fatalf("second claim = %v, want ErrNoPendingAuthorization", err)
	}
}

func TestExternalAuthorizationValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PendingAuthorization)
	}{
		{"empty authorization ID", func(p *PendingAuthorization) { p.Authorization.ID = "" }},
		{"malformed authorization ID", func(p *PendingAuthorization) { p.Authorization.ID = "bad\nvalue" }},
		{"control display name", func(p *PendingAuthorization) { p.Authorization.DisplayName = "Calendar\nforged" }},
		{"surrounding whitespace display name", func(p *PendingAuthorization) { p.Authorization.DisplayName = " Calendar" }},
		{"oversized display name", func(p *PendingAuthorization) { p.Authorization.DisplayName = strings.Repeat("x", 257) }},
		{"empty binding", func(p *PendingAuthorization) { p.Authorization.Binding = "" }},
		{"invalid UTF-8 binding", func(p *PendingAuthorization) { p.Authorization.Binding = AuthorizationBinding(string([]byte{0xff})) }},
		{"oversized binding", func(p *PendingAuthorization) {
			p.Authorization.Binding = AuthorizationBinding(strings.Repeat("x", 257))
		}},
		{"zero expiry", func(p *PendingAuthorization) { p.Authorization.ExpiresAt = time.Time{} }},
		{"mismatched call ID", func(p *PendingAuthorization) { p.Call.ID = "other" }},
		{"mismatched call name", func(p *PendingAuthorization) { p.Call.Name = "other" }},
		{"mismatched provider item", func(p *PendingAuthorization) { p.Call.ItemID = "other" }},
		{"mismatched deferred args", func(p *PendingAuthorization) {
			p.Deferred[0].Args = json.RawMessage(`{"after":"modified"}`)
		}},
		{"duplicate deferred", func(p *PendingAuthorization) { p.Deferred[0].ID = p.Call.ID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := authorizationSession(t)
			p := authorizationPending()
			tc.mutate(&p)
			if err := s.PauseForAuthorization(p); err == nil {
				t.Fatal("PauseForAuthorization succeeded for malformed pending state")
			}
			if s.State != StateRunning {
				t.Fatalf("State = %q after rejection", s.State)
			}
		})
	}
}

func TestAuthorizationSoleCallAcceptsNilAndEmptyDeferred(t *testing.T) {
	for _, deferred := range [][]ToolCall{nil, {}} {
		s := newTestSession(Limits{})
		mustOK(t, s.BeginTurn())
		original := NewToolCall("only", "external_read", json.RawMessage(`{"source":"model"}`))
		original.ItemID = "provider-only"
		mustOK(t, s.RecordAssistant(NewAssistantMessage("", "", []ToolCall{original})))
		effective := original
		effective.Args = json.RawMessage(`{"source":"effective"}`)
		mustOK(t, s.PauseForAuthorization(PendingAuthorization{
			Authorization: ExternalAuthorization{ID: "authorization-only", Binding: "private", ExpiresAt: time.Unix(1_800_000_000, 0)},
			Call:          effective,
			Deferred:      deferred,
		}))
		claimed, err := s.ClaimAuthorization()
		mustOK(t, err)
		if string(claimed.Call.Args) != `{"source":"effective"}` || len(claimed.Deferred) != 0 {
			t.Fatalf("claimed = %+v", claimed)
		}
	}
}

func TestAuthorizingRejectsConflictingMutations(t *testing.T) {
	s := authorizationSession(t)
	mustOK(t, s.PauseForAuthorization(authorizationPending()))
	before := CloneMessages(s.Conversation.Messages)
	for name, op := range map[string]func() error{
		"prompt":      func() error { return s.RecordUserPrompt("new", nil) },
		"assistant":   func() error { return s.RecordAssistant(NewAssistantMessage("new", "", nil)) },
		"results":     func() error { return s.RecordToolResults(nil) },
		"usage":       func() error { return s.RecordUsage(Usage{InputTokens: 1}) },
		"reset usage": s.ResetUsage,
		"replace":     func() error { return s.ReplaceHistory(nil) },
		"boundary":    func() error { return s.ReplaceHistoryAtBoundary(nil) },
		"seed":        func() error { return s.SeedHistory(nil) },
		"rehome":      func() error { return s.Rehome(EnvironmentRef{Kind: EnvKindLocal, ID: "other", Revision: "rev"}) },
		"mode":        func() error { return s.SetMode(ModePlan) },
		"complete":    s.Complete,
		"stop":        func() error { return s.Stop(StopEndTurn) },
		"cancel":      s.Cancel,
		"fail":        s.Fail,
		"abandon":     s.Abandon,
	} {
		t.Run(name, func(t *testing.T) {
			if err := op(); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("operation = %v, want ErrIllegalTransition", err)
			}
			if s.State != StateAuthorizing || !reflect.DeepEqual(before, s.Conversation.Messages) {
				t.Fatal("authorizing state was mutated")
			}
		})
	}
}

func TestAbortAuthorizationUsesClosedHarnessMessages(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"denied", "external authorization denied"},
		{"cancelled", "external authorization cancelled"},
		{"expired", "external authorization expired"},
		{"interrupted", "external authorization interrupted"},
		{"failed", "external authorization failed"},
		{"unavailable", "external authorization unavailable"},
		{"", "external authorization failed"},
		{"attacker-token", "external authorization failed"},
		{strings.Repeat("x", 1024), "external authorization failed"},
		{string([]byte{0xff, 0xfe}), "external authorization failed"},
	}
	for _, tc := range cases {
		s := authorizationSession(t)
		mustOK(t, s.PauseForAuthorization(authorizationPending()))
		results, err := s.AbortAuthorization(tc.reason)
		mustOK(t, err)
		if len(results) != 2 || results[0].Content != tc.want {
			t.Fatalf("reason %q: results = %+v, want first content %q", tc.reason, results, tc.want)
		}
	}

	s := authorizationSession(t)
	mustOK(t, s.PauseForAuthorization(authorizationPending()))
	results, err := s.InterruptAuthorization()
	mustOK(t, err)
	if results[0].Content != "external authorization interrupted" {
		t.Fatalf("interrupt content = %q", results[0].Content)
	}
}

func TestAuthorizationAbortAndInterruptPreservePairing(t *testing.T) {
	for _, resolve := range []struct {
		name string
		op   func(*Session) ([]ToolResult, error)
	}{
		{"abort", func(s *Session) ([]ToolResult, error) { return s.AbortAuthorization("cancelled") }},
		{"interrupt", func(s *Session) ([]ToolResult, error) { return s.InterruptAuthorization() }},
	} {
		t.Run(resolve.name, func(t *testing.T) {
			s := authorizationSession(t)
			mustOK(t, s.PauseForAuthorization(authorizationPending()))
			results, err := resolve.op(s)
			mustOK(t, err)
			if s.State != StateRunning || len(results) != 2 || results[0].CallID != "call-2" || results[1].CallID != "call-3" {
				t.Fatalf("resolution = state %q results %+v", s.State, results)
			}
			mustOK(t, s.RecordToolResults(results))
			if err := ValidateToolPairing(s.Conversation.Messages); err != nil {
				t.Fatalf("ValidateToolPairing: %v", err)
			}
		})
	}
}
