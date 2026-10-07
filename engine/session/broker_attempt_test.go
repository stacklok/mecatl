package session_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"iter"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func slotSession(t *testing.T) (*session.Session, session.BrokerSessionRef, session.BrokerCatalogueRef, time.Time) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	ref := session.BrokerSessionRef(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	cat := session.BrokerCatalogueRef(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	s := session.New("slots", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "slots", Revision: "1"}, session.Limits{}, now)
	if err := s.BindAuthority(session.Authority{Provenance: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptBrokerCatalogue(ref, cat, session.BrokerConnectionRef(cat), now.Add(time.Hour), []string{"remote"}); err != nil {
		t.Fatal(err)
	}
	return s, ref, cat, now
}

func TestBrokerHostSlots_BoundedLifetime(t *testing.T) {
	s, ref, cat, now := slotSession(t)
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("reused-provider-id", "remote", []byte(`{}`))
	for n := uint64(1); n <= 4100; n++ {
		if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
			t.Fatal(err)
		}
		a, err := s.PrepareBrokerInvocation(ref, cat, call, now)
		if err != nil || a.Slot != 0 || a.Sequence != n {
			t.Fatalf("prepare %d: %+v, %v", n, a, err)
		}
		if err := s.DispatchBrokerInvocation(a); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordBrokerInvocationResult(a, call.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult(call.ID, "ok")}); err != nil {
			t.Fatal(err)
		}
		access, _ := s.BrokerAccess()
		if access.Current == nil || access.Current.Disposition != session.BrokerAttemptCompleted || access.AdmittedSequence != n || len(access.Attempted) != 0 || access.Pending != "" {
			t.Fatalf("unsettled/unbounded state at %d: %+v", n, access)
		}
		b, err := json.Marshal(access)
		if err != nil || len(b) > 1024 {
			t.Fatalf("state grew at %d: %d bytes, %v", n, len(b), err)
		}
	}
	if err := session.ValidateToolPairing(s.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
	snap, err := sessnap.Of(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if next, err := restored.PrepareBrokerInvocation(ref, cat, call, now); err != nil || next.Sequence != 4101 {
		t.Fatalf("paired completion did not survive restart: %+v %v", next, err)
	}
}

func TestBrokerHostSlots_AllocationRejectionAndAdmission(t *testing.T) {
	s, ref, cat, now := slotSession(t)
	call := session.NewToolCall("same", "remote", []byte(`{}`))
	a, err := s.PrepareBrokerInvocation(ref, cat, call, now)
	if err != nil {
		t.Fatal(err)
	}
	access, _ := s.BrokerAccess()
	if access.AdmittedSequence != 0 {
		t.Fatal("allocation consumed broker high-water")
	}
	if _, err := s.PrepareBrokerInvocation(ref, cat, call, now); err == nil {
		t.Fatal("lost ack permitted next allocation")
	}
	if err := s.RejectUnadmittedBrokerInvocation(session.BrokerAttempt{Sequence: 2}); err == nil {
		t.Fatal("foreign rejection cleared allocation")
	}
	if err := s.RejectUnadmittedBrokerInvocation(a); err != nil {
		t.Fatal(err)
	}
	b, err := s.PrepareBrokerInvocation(ref, cat, call, now)
	if err != nil || b != a {
		t.Fatalf("rejection introduced gap: %+v %v", b, err)
	}
	if err := s.AdmitBrokerInvocation(b); err != nil {
		t.Fatal(err)
	}
	if err := s.RejectUnadmittedBrokerInvocation(b); err == nil {
		t.Fatal("admitted attempt reused as unconsumed")
	}
	if err := s.SettleBrokerInvocation(b, session.BrokerAttemptNotDispatched); err != nil {
		t.Fatal(err)
	}
	c, err := s.PrepareBrokerInvocation(ref, cat, call, now)
	if err != nil || c.Sequence != 2 {
		t.Fatalf("admitted non-dispatch did not consume: %+v %v", c, err)
	}
}

func TestBrokerHostSlots_SameOccurrencePairAndUnknownFence(t *testing.T) {
	s, ref, cat, now := slotSession(t)
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("same", "remote", []byte(`{}`))
	// The old same-ID result cannot discharge the new occurrence.
	if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult(call.ID, "old")}); err != nil {
		t.Fatal(err)
	}
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
	if err := s.RecordBrokerInvocationResult(session.BrokerAttempt{Sequence: 2}, call.ID); err == nil {
		t.Fatal("wrong receipt accepted")
	}
	if err := s.SettleBrokerInvocation(a, session.BrokerAttemptCompleted); err == nil {
		t.Fatal("completed without result pair")
	}
	if err := s.RecordToolResults([]session.ToolResult{session.NewToolError(call.ID, "synthetic interruption")}); err != nil {
		t.Fatal(err)
	}
	access, _ := s.BrokerAccess()
	if access.Current.Phase != "dispatched" {
		t.Fatal("synthetic error settled effects")
	}
	if err := s.SettleBrokerInvocation(a, session.BrokerAttemptUnknown); err != nil {
		t.Fatal(err)
	}
	access, _ = s.BrokerAccess()
	if access.AdmittedSequence != 0 {
		t.Fatal("uncertainty invented affirmative admission evidence")
	}
	if err := s.SettleBrokerInvocation(a, session.BrokerAttemptNotDispatched); err == nil {
		t.Fatal("unknown converted to no effects")
	}
	if err := s.RejectUnadmittedBrokerInvocation(a); err == nil {
		t.Fatal("unknown discarded")
	}
	if _, err := s.PrepareBrokerInvocation(ref, cat, call, now); err == nil {
		t.Fatal("unknown allowed automatic progress")
	}
}

func TestBrokerHostSlots_RestoredReservedAndRoundTrip(t *testing.T) {
	for _, phase := range []string{"reserved", "dispatched", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			s, ref, cat, now := slotSession(t)
			call := session.NewToolCall("same", "remote", []byte(`{"sensitive":"not-in-metadata"}`))
			a, err := s.PrepareBrokerInvocation(ref, cat, call, now)
			if err != nil {
				t.Fatal(err)
			}
			if phase != "reserved" {
				if err := s.DispatchBrokerInvocation(a); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "unknown" {
				if err := s.SettleBrokerInvocation(a, session.BrokerAttemptUnknown); err != nil {
					t.Fatal(err)
				}
			}
			want, _ := s.BrokerAccess()
			snap, err := sessnap.Of(s)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(snap)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte("not-in-metadata")) {
				t.Fatal("arguments leaked to snapshot")
			}
			var decoded sessnap.Snapshot
			if err := json.Unmarshal(b, &decoded); err != nil {
				t.Fatal(err)
			}
			restored, err := decoded.Restore()
			if err != nil {
				t.Fatal(err)
			}
			got, _ := restored.BrokerAccess()
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("roundtrip drift: %+v != %+v", got, want)
			}
			got.Current.Phase = "terminal"
			unchanged, _ := restored.BrokerAccess()
			if unchanged.Current.Phase != want.Current.Phase {
				t.Fatal("Current alias escaped")
			}
			if err := restored.DispatchBrokerInvocation(a); err == nil {
				t.Fatal("restored unresolved dispatched without inspection")
			}
			if err := restored.RecordBrokerInvocationResult(a, call.ID); err == nil {
				t.Fatal("restored unresolved accepted receipt")
			}
			if _, err := restored.PrepareBrokerInvocation(ref, cat, call, now); err == nil {
				t.Fatal("restored fence released")
			}
			// Adoption preserves the restored fence.
			if err := restored.AdoptBrokerCatalogue(ref, cat, session.BrokerConnectionRef(cat), now.Add(time.Hour), []string{"remote"}); err != nil {
				t.Fatal(err)
			}
			if err := restored.DispatchBrokerInvocation(a); err == nil {
				t.Fatal("catalogue adoption cleared restored fence")
			}
		})
	}
}

func TestBrokerHostSlots_EventMetadataClone(t *testing.T) {
	s, ref, cat, now := slotSession(t)
	a, err := s.PrepareBrokerInvocation(ref, cat, session.NewToolCall("same", "remote", []byte(`{}`)), now)
	if err != nil {
		t.Fatal(err)
	}
	access, _ := s.BrokerAccess()
	authority := s.Authority.Clone()
	empty := iter.Seq2[session.Event, error](func(func(session.Event, error) bool) {})
	folded, err := eventsource.Fold(eventsource.SessionMeta{ID: s.ID, Mode: s.Mode, EnvironmentRef: s.EnvironmentRef, CreatedAt: now, Incarnation: s.Incarnation(), Authority: &authority, BrokerAccess: &access}, empty)
	if err != nil {
		t.Fatal(err)
	}
	access.Current.Digest[0] ^= 1
	got, _ := folded.BrokerAccess()
	original, _ := s.BrokerAccess()
	if !reflect.DeepEqual(got, original) {
		t.Fatal("folded metadata aliased source")
	}
	if err := folded.DispatchBrokerInvocation(a); err == nil {
		t.Fatal("fold granted dispatch authority")
	}
}

func TestBrokerHostSlots_QueryDigestAndValidation(t *testing.T) {
	call := session.NewToolCall("outer", "CallMcpWithQuery", []byte(`{"tool":"remote","arguments":{"x":1},"query":".x"}`))
	digest := session.BrokerCallDigest(call)
	changed := call
	changed.Args = []byte(`{"tool":"remote","arguments":{"x":1},"query":".y"}`)
	if digest == session.BrokerCallDigest(changed) {
		t.Fatal("query replacement did not change outer digest")
	}
	changed.Args = append([]byte(" "), call.Args...)
	if digest == session.BrokerCallDigest(changed) {
		t.Fatal("digest normalized exact argument bytes")
	}
	for _, attempt := range []session.BrokerAttempt{{}, {Slot: 64, Sequence: 1}} {
		if attempt.Valid() {
			t.Fatalf("invalid framing accepted: %+v", attempt)
		}
	}
	if !(session.BrokerAttempt{Slot: 63, Sequence: math.MaxUint64}).Valid() {
		t.Fatal("valid last slot/sequence rejected")
	}
	s, ref, cat, now := slotSession(t)
	access, _ := s.BrokerAccess()
	access.AdmittedSequence = math.MaxUint64
	if err := s.RestoreBrokerAccess(access); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareBrokerInvocation(ref, cat, call, now); err == nil {
		t.Fatal("sequence wrapped")
	}
}

func TestBrokerHostSlots_InvalidState(t *testing.T) {
	s, ref, cat, now := slotSession(t)
	if _, err := s.PrepareBrokerInvocation(ref, cat, session.NewToolCall("same", "remote", []byte(`{}`)), now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*session.BrokerAccess)
	}{
		{"zero sequence", func(a *session.BrokerAccess) { a.Current.Attempt.Sequence = 0 }},
		{"foreign host slot", func(a *session.BrokerAccess) { a.Current.Attempt.Slot = 1 }},
		{"gap", func(a *session.BrokerAccess) { a.Current.Attempt.Sequence = 3 }},
		{"unknown phase", func(a *session.BrokerAccess) { a.Current.Phase = "parked" }},
		{"unterminated disposition", func(a *session.BrokerAccess) { a.Current.Disposition = session.BrokerAttemptCompleted }},
		{"unadmitted completion", func(a *session.BrokerAccess) {
			a.Current.Phase = "terminal"
			a.Current.Disposition = session.BrokerAttemptCompleted
		}},
		{"terminal without disposition", func(a *session.BrokerAccess) { a.Current.Phase = "terminal"; a.AdmittedSequence = 1 }},
		{"empty digest", func(a *session.BrokerAccess) { a.Current.Digest = [32]byte{} }},
		{"empty call", func(a *session.BrokerAccess) { a.Current.CallID = "" }},
		{"high-water ahead", func(a *session.BrokerAccess) { a.AdmittedSequence = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := s.BrokerAccess()
			invalid, _ := s.BrokerAccess()
			tc.mutate(&invalid)
			if err := s.RestoreBrokerAccess(invalid); err == nil {
				t.Fatal("invalid state restored")
			}
			after, _ := s.BrokerAccess()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid restore mutated live state")
			}
		})
	}
}

func TestBrokerHostSlots_LegacyPathsCannotMix(t *testing.T) {
	s, ref, cat, now := slotSession(t)
	call := session.NewToolCall("same", "remote", []byte(`{}`))
	if _, err := s.PrepareBrokerInvocation(ref, cat, call, now); err != nil {
		t.Fatal(err)
	}
	access, _ := s.BrokerAccess()
	access.Attempted = []session.ToolCallID{"legacy"}
	if err := s.RestoreBrokerAccess(access); err == nil {
		t.Fatal("mixed persisted fences accepted")
	}
	legacy, _, _, _ := slotSession(t)
	old, _ := legacy.BrokerAccess()
	old.Attempted = []session.ToolCallID{"legacy"}
	old.Pending = "legacy"
	if err := legacy.RestoreBrokerAccess(old); err == nil {
		t.Fatal("obsolete persisted state silently migrated")
	}
}
