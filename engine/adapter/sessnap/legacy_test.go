package sessnap

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func legacyFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func restoreFixture(t *testing.T, data []byte) *session.Session {
	t.Helper()
	s, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Ordinary visible JSON arguments retain the existing normalization behavior;
// private effective authorization bytes and provider payloads are compared exactly.
func sameStoredHistory(t *testing.T, a, b []session.Message) bool {
	t.Helper()
	x, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(x, y)
}

// Expected controls are frozen literals, not values emitted by the current codec.
func assertLegacyControlValues(t *testing.T, name string, s *session.Session) {
	t.Helper()
	if name == "legacy-retry" {
		if _, pending := s.FailedStepRetryPending(); pending {
			t.Fatal("original failed session acquired a prepared retry")
		}
		if s.State != session.StateFailed || s.LastError() != "legacy retryable stream failure" || s.FailureMetadata() != (session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressVisible}) {
			t.Fatal("original retry controls changed")
		}
	}
	var want session.PendingAsk
	switch name {
	case "legacy-awaiting-plan":
		want = session.PendingAsk{AskID: "ask-plan", Tool: "PresentPlan", Args: json.RawMessage(`{"summary":"plan approval"}`), Reason: "Review plan", Origin: session.ApprovalOriginPlan}
		if s.Mode != session.ModePlan {
			t.Fatal("original plan mode changed")
		}
	case "legacy-awaiting-guardrail":
		want = session.PendingAsk{AskID: "ask-guardrail", Tool: "Shell", Args: json.RawMessage(`{"command_summary":"legacy display summary"}`), Reason: "System temp approval", Origin: session.ApprovalOriginHookGuardrail,
			Guardrail: &session.GuardrailPendingScope{ReviewID: "review-1", Kind: session.GuardrailApprovalAction, GrantDigest: "digest-1", SessionOnly: true, RepeatAvailable: true}}
	case "legacy-awaiting-permission":
		want = session.PendingAsk{AskID: "ask-permission", Tool: "Read", Args: json.RawMessage(`{"path":"fixture.txt"}`), Reason: "Review read", Origin: session.ApprovalOriginPermission}
	default:
		return
	}
	got, ok := s.PendingAsk()
	if !ok || s.State != session.StateAwaiting || len(s.Conversation.Messages) != 1 || len(s.Conversation.Messages[0].ToolCalls) != 1 {
		t.Fatal("original pending control is unavailable")
	}
	want.Call = s.Conversation.Messages[0].ToolCalls[0].ID
	if want.Call == "" || !reflect.DeepEqual(got, want) {
		t.Fatalf("original pending controls changed: got %+v want %+v", got, want)
	}
}

func TestLegacyIdentityStableAcrossReadsAndSaves(t *testing.T) {
	for _, name := range []string{"legacy-completed", "legacy-authorizing", "legacy-retry", "legacy-awaiting-plan", "legacy-awaiting-guardrail", "legacy-awaiting-permission"} {
		t.Run(name, func(t *testing.T) {
			data := legacyFixture(t, name)
			first := restoreFixture(t, data)
			second := restoreFixture(t, data)
			assertLegacyControlValues(t, name, first)
			assertLegacyControlValues(t, name, second)
			if first.Incarnation() != second.Incarnation() || !reflect.DeepEqual(first.Conversation.Messages, second.Conversation.Messages) {
				t.Fatal("reads before save disagree")
			}
			if err := first.CheckIdentityReady(); err != nil {
				t.Fatal(err)
			}
			var old Snapshot
			if err := json.Unmarshal(data, &old); err != nil {
				t.Fatal(err)
			}
			if len(old.Messages) != 0 || old.Pending != nil || old.PendingAuthorization != nil {
				t.Fatal("old provider references decoded into canonical DTOs")
			}
			// A decoded legacy Snapshot is also safe to re-encode before restoration.
			reencoded, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			restored := restoreFixture(t, reencoded)
			assertLegacyControlValues(t, name, restored)
			if !sameStoredHistory(t, restored.Conversation.Messages, first.Conversation.Messages) {
				t.Fatal("legacy Snapshot re-encoding lost history")
			}
			for range 2 {
				snap, err := Of(first)
				if err != nil {
					t.Fatal(err)
				}
				if snap.IdentitySchema != 2 {
					t.Fatal("writer did not discriminate canonical IDs")
				}
				direct, err := snap.Restore()
				if err != nil {
					t.Fatal(err)
				}
				assertLegacyControlValues(t, name, direct)
				if !reflect.DeepEqual(direct.Conversation.Messages, first.Conversation.Messages) {
					t.Fatal("direct restore changed identity/provenance")
				}
				wire, err := Marshal(first)
				if err != nil {
					t.Fatal(err)
				}
				first = restoreFixture(t, wire)
				assertLegacyControlValues(t, name, first)
				if !sameStoredHistory(t, first.Conversation.Messages, second.Conversation.Messages) {
					t.Fatal("save/reload changed history")
				}
			}
		})
	}
}

func TestLegacyAuthorizationExactEffectiveBytesAndDeferredOrder(t *testing.T) {
	data := legacyFixture(t, "legacy-authorizing")
	// Effective arguments need not equal the assistant's visible call arguments.
	data = bytes.Replace(data, []byte("eyJjb21tYW5kIjogICJlY2hvIGF1dGgifQ=="), []byte("eyJjb21tYW5kIjogICJlZmZlY3RpdmUifQ=="), 1)
	s := restoreFixture(t, data)
	want, ok := s.PendingAuthorization()
	if !ok {
		t.Fatal("valid authorization held")
	}
	if string(want.Call.Args) != `{"command":  "effective"}` {
		t.Fatal("effective bytes replaced with visible args")
	}
	if want.Authorization.ID != "auth-legacy-1" || want.Authorization.Binding != "private-binding" || !want.Authorization.ExpiresAt.Equal(time.Date(2025, 1, 3, 3, 4, 5, 0, time.UTC)) {
		t.Fatal("authorization token/binding/expiry changed")
	}
	if string(want.Deferred[0].Args) != `{"command":"echo second"}` || want.Deferred[0].ID != s.Conversation.Messages[2].ToolCalls[1].ID {
		t.Fatal("deferred suffix changed")
	}
	for range 2 {
		data, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		s = restoreFixture(t, data)
		got, ok := s.PendingAuthorization()
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatal("second reload changed private continuation")
		}
	}
	claimed, err := s.ClaimAuthorization()
	if err != nil || !reflect.DeepEqual(claimed, want) {
		t.Fatal("claim did not preserve continuation")
	}
	if err := s.RestoreAuthorizationClaim(claimed); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyProvenanceSurvivesForkPruneAndOwnership(t *testing.T) {
	s := restoreFixture(t, legacyFixture(t, "legacy-completed"))
	messages := s.Conversation.Messages
	if messages[1].ToolCalls[0].ID == messages[3].ToolCalls[0].ID || messages[1].Replay.Bindings[0].PairingID != messages[3].Replay.Bindings[0].PairingID {
		t.Fatal("completed pairing-key reuse collapsed")
	}
	want := session.CloneMessages(messages[3:])
	child := session.New("child", session.ModeDefault, s.EnvironmentRef, session.Limits{}, time.Now())
	if err := child.SeedHistory(session.ForkSnapshot(s.Conversation)); err != nil {
		t.Fatal(err)
	}
	if child.Conversation.Messages[3].Legacy.Ordinal != 3 || child.Conversation.Messages[3].Legacy.Incarnation != s.Incarnation() {
		t.Fatal("fork rebased source coordinates")
	}
	if err := s.ReplaceHistoryAtBoundary(want); err != nil {
		t.Fatal(err)
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Conversation.Messages[0].Legacy.Calls[0].ProviderID = "mutated"
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Conversation.Messages, want) {
		t.Fatal("Of aliases source metadata")
	}
	restored.Conversation.Messages[0].Legacy.Ordinal = 0
	restored.Conversation.Messages[0].Legacy.Calls[0].Slot = 9
	again, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Conversation.Messages, want) {
		t.Fatal("Restore aliases snapshot metadata")
	}
	wire, err := Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if got := restoreFixture(t, wire); !reflect.DeepEqual(got.Conversation.Messages, want) {
		t.Fatal("pruned reload rebased identity")
	}
	changedKeys := bytes.ReplaceAll(legacyFixture(t, "legacy-completed"), []byte("reused"), []byte("different-provider-token"))
	changed := restoreFixture(t, changedKeys)
	if changed.Conversation.Messages[3].ToolCalls[0].ID != want[0].ToolCalls[0].ID {
		t.Fatal("canonical identity derived from provider key")
	}
}

func TestLegacyGenericReplayDoesNotInterpretProviderPayload(t *testing.T) {
	s := restoreFixture(t, legacyFixture(t, "legacy-authorizing"))
	m := s.Conversation.Messages[0]
	if m.Replay.Provider.Format != "mecatl.legacy-message" || m.Replay.Provider.Version != 1 {
		t.Fatal("wrong generic replay namespace")
	}
	var envelope map[string]string
	if err := json.Unmarshal(m.Replay.Provider.Data, &envelope); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(envelope, map[string]string{"reasoning": "encrypted-replay-v0", "reasoning_item_id": "rs_legacy_1", "phase": "commentary"}) {
		t.Fatal("generic legacy envelope changed")
	}
	completed := restoreFixture(t, legacyFixture(t, "legacy-completed"))
	var old []legacyMessageDTO
	var raw struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(legacyFixture(t, "legacy-completed"), &raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw.Messages, &old); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(completed.Conversation.Messages[1].Replay.Provider.Data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["reasoning"] != old[1].Reasoning {
		t.Fatal("signed Anthropic payload changed")
	}
}

func assertHeld(t *testing.T, s *session.Session) {
	t.Helper()
	if !errors.Is(s.CheckIdentityReady(), session.ErrMigrationHeld) {
		t.Fatal("ambiguous continuation is executable")
	}
	if _, ok := s.PendingAsk(); ok {
		t.Fatal("held ask gained authority")
	}
	if _, ok := s.PendingAuthorization(); ok {
		t.Fatal("held authorization gained authority")
	}
	if summary, ok := s.IdentityHoldSummary(); !ok || summary != "legacy-continuation-unproven" {
		t.Fatal("unsafe/missing summary")
	}
	before := s.IdentityHoldSnapshot()
	state := s.State
	operations := []func() error{s.Complete, s.Cancel, s.Fail, s.Recover, s.Abandon, s.BeginTurn, s.Reopen, s.Interrupt, s.PrepareFailedStepRetry,
		func() error { return s.Stop(session.StopBudget) },
		func() error { _, err := s.ResumeWith(); return err },
		func() error { _, err := s.ClaimAuthorization(); return err },
		func() error { _, err := s.InterruptAuthorization(); return err },
		func() error { _, err := s.AbortAuthorization("denied"); return err },
		s.ValidateAuthorizationState}
	for _, operation := range operations {
		if err := operation(); !errors.Is(err, session.ErrMigrationHeld) {
			t.Fatalf("hold guard returned %v", err)
		}
		if s.State != state || !reflect.DeepEqual(s.IdentityHoldSnapshot(), before) {
			t.Fatal("rejected operation mutated hold")
		}
	}
	if err := s.ValidateIdentityPersistence(); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.Conversation.Messages {
		for _, c := range m.ToolCalls {
			if c.ID == "" {
				t.Fatal("empty canonical call")
			}
		}
		if m.ToolResult != nil && m.ToolResult.CallID == "" {
			t.Fatal("empty canonical result")
		}
	}
}

func TestLegacyHoldExactPrivateContinuationSurvivesDirectAndJSON(t *testing.T) {
	for _, name := range []string{"legacy-ambiguous-pending", "legacy-authorizing"} {
		t.Run(name, func(t *testing.T) {
			data := legacyFixture(t, name)
			if name == "legacy-authorizing" {
				data = bytes.Replace(data, []byte(`"id": "provider-auth-call"`), []byte(`"id": "missing-provider-reference"`), 1)
			}
			s := restoreFixture(t, data)
			assertHeld(t, s)
			want := s.IdentityHoldSnapshot()
			var hold legacyHoldPayload
			if err := json.Unmarshal(want.Payload, &hold); err != nil {
				t.Fatal(err)
			}
			var original struct {
				Pending       json.RawMessage `json:"pending"`
				Authorization json.RawMessage `json:"pending_authorization"`
			}
			if err := json.Unmarshal(data, &original); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(hold.Pending, original.Pending) || !bytes.Equal(hold.Authorization, original.Authorization) {
				t.Fatal("original pending bytes changed")
			}
			wantHistory := session.CloneMessages(s.Conversation.Messages)
			for range 2 {
				snap, err := Of(s)
				if err != nil {
					t.Fatal(err)
				}
				direct, err := snap.Restore()
				if err != nil {
					t.Fatal(err)
				}
				direct.IdentityHoldSnapshot().Payload[0] = '!'
				if !reflect.DeepEqual(snap.IdentityHold, want) {
					t.Fatal("hold getter aliases bytes")
				}
				snap.IdentityHold.Payload[0] = '!'
				if !reflect.DeepEqual(direct.IdentityHoldSnapshot(), want) || !reflect.DeepEqual(s.IdentityHoldSnapshot(), want) {
					t.Fatal("direct Of/Restore aliases hold payload")
				}
				snap.IdentityHold.Payload[0] = want.Payload[0]
				wire, err := json.Marshal(snap)
				if err != nil {
					t.Fatal(err)
				}
				s = restoreFixture(t, wire)
				assertHeld(t, s)
				if !reflect.DeepEqual(s.IdentityHoldSnapshot(), want) || !sameStoredHistory(t, s.Conversation.Messages, wantHistory) {
					t.Fatal("held private data/history changed on save/reload")
				}
			}
		})
	}
}

func TestLegacyNeverRepairsContinuationByNameArgsOrFirstMatch(t *testing.T) {
	plan := legacyFixture(t, "legacy-awaiting-plan")
	for _, replacement := range []string{`"call":"missing"`, `"call":""`, `"unrelated":"plan-call"`} {
		t.Run(replacement, func(t *testing.T) {
			data := bytes.Replace(plan, []byte(`"call":"plan-call"`), []byte(replacement), 1)
			assertHeld(t, restoreFixture(t, data))
		})
	}
	auth := legacyFixture(t, "legacy-authorizing")
	for _, replacement := range []struct{ old, new string }{
		{`"item_id": "fc_item_auth"`, `"item_id": "other-item"`},
		{`"name": "Shell"`, `"name": "Other"`},
		{`"expires_at": "2025-01-03T03:04:05Z"`, `"expires_at": "0001-01-01T00:00:00Z"`},
		{`"binding": "private-binding"`, `"binding": ""`},
		{`"deferred": [{"id": "provider-call-reused", "name": "Shell", "args": "eyJjb21tYW5kIjoiZWNobyBzZWNvbmQifQ==", "item_id": "fc_item_legacy_2"}]`, `"deferred": []`},
	} {
		data := bytes.Replace(auth, []byte(replacement.old), []byte(replacement.new), 1)
		if bytes.Equal(data, auth) {
			t.Fatal("test replacement did not apply")
		}
		assertHeld(t, restoreFixture(t, data))
	}
}

func TestLegacyUnsupportedResultStaysPrivateWithoutOrphanCanonicalResult(t *testing.T) {
	data := legacyFixture(t, "legacy-completed")
	data = bytes.Replace(data, []byte(`"CallID":"reused", "Content":"first"`), []byte(`"CallID":"missing", "Content":"first"`), 1)
	s := restoreFixture(t, data)
	if s.Conversation.Messages[2].ToolResult != nil || s.Conversation.Messages[2].Text != "first" {
		t.Fatal("unsupported result became an orphan canonical result or lost readable text")
	}
	private := append([]byte(nil), s.Conversation.Messages[2].Legacy.Unsupported...)
	var original legacyMessageDTO
	if err := json.Unmarshal(private, &original); err != nil {
		t.Fatal(err)
	}
	if original.ToolResult.CallID != "missing" || len(original.ToolResult.Parts) != 1 {
		t.Fatal("unsupported identity/media lost")
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Conversation.Messages[2].Legacy.Unsupported[0] = '!'
	for range 2 {
		restored, err := snap.Restore()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(restored.Conversation.Messages[2].Legacy.Unsupported, private) {
			t.Fatal("unsupported payload aliases or changed")
		}
		restored.Conversation.Messages[2].Legacy.Unsupported[0] = '!'
	}
}

func TestIdentitySchemaAndIncarnationFailClosed(t *testing.T) {
	data := legacyFixture(t, "legacy-retry")
	for _, schema := range []string{"1", "3", "0", "null", `"2"`} {
		invalid := append([]byte(`{"identity_schema":`+schema+`,`), data[1:]...)
		if _, err := Unmarshal(invalid); err == nil {
			t.Fatalf("accepted unknown discriminator %s", schema)
		}
	}
	invalid := append([]byte(`{"incarnation":"corrupt",`), data[1:]...)
	if _, err := Unmarshal(invalid); err == nil {
		t.Fatal("accepted corrupt incarnation")
	}
	if _, err := (Snapshot{IdentitySchema: 99}).Restore(); err == nil {
		t.Fatal("direct restore accepted unknown schema")
	}
	var snap Snapshot
	if err := json.Unmarshal(legacyFixture(t, "legacy-completed"), &snap); err != nil {
		t.Fatal(err)
	}
	snap.Incarnation = session.NewIncarnationID()
	withInc, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if withInc.Conversation.Messages[1].Legacy.Incarnation != snap.Incarnation {
		t.Fatal("allocation preceded stable source incarnation restore")
	}
	if strings.Contains(string(withInc.Conversation.Messages[1].ToolCalls[0].ID), "reused") {
		t.Fatal("provider key leaked into canonical identity")
	}
}

func TestLegacyAuthorizationDeferredOrderIsStructural(t *testing.T) {
	var old Snapshot
	if err := json.Unmarshal(legacyFixture(t, "legacy-authorizing"), &old); err != nil {
		t.Fatal(err)
	}
	old.legacyMessages[2].ToolCalls = append(old.legacyMessages[2].ToolCalls, legacyCallDTO{ID: "third-provider", Name: "Read", Args: []byte(`{"path":"third"}`), ItemID: "third-item"})
	old.legacyAuthorization.Deferred = append(old.legacyAuthorization.Deferred, legacyAuthorizationCallDTO{ID: "third-provider", Name: "Read", Args: []byte(`{ "path": "third" }`), ItemID: "third-item"})
	var err error
	old.legacyAuthorizationBytes, err = json.Marshal(old.legacyAuthorization)
	if err != nil {
		t.Fatal(err)
	}
	s, err := old.Restore()
	if err != nil {
		t.Fatal(err)
	}
	want, ok := s.PendingAuthorization()
	if !ok || len(want.Deferred) != 2 || want.Deferred[0].ID != s.Conversation.Messages[2].ToolCalls[1].ID || want.Deferred[1].ID != s.Conversation.Messages[2].ToolCalls[2].ID {
		t.Fatal("deferred order/identity changed")
	}
	for range 2 {
		data, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		s = restoreFixture(t, data)
		got, ok := s.PendingAuthorization()
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatal("deferred exact bytes/order changed after reload")
		}
	}
	old.legacyAuthorization.Deferred[0], old.legacyAuthorization.Deferred[1] = old.legacyAuthorization.Deferred[1], old.legacyAuthorization.Deferred[0]
	old.legacyAuthorizationBytes, err = json.Marshal(old.legacyAuthorization)
	if err != nil {
		t.Fatal(err)
	}
	s, err = old.Restore()
	if err != nil {
		t.Fatal(err)
	}
	assertHeld(t, s)
	var payload legacyHoldPayload
	if err := json.Unmarshal(s.IdentityHoldSnapshot().Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload.Authorization, old.legacyAuthorizationBytes) {
		t.Fatal("invalid original deferred order discarded")
	}
}

func TestHeldCodecRejectsCorruptPrivatePayloadAndOrphanCanonicalResults(t *testing.T) {
	s := restoreFixture(t, legacyFixture(t, "legacy-ambiguous-pending"))
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	snap.IdentityHold.Payload = []byte(`{"version":99,"state":"awaiting"}`)
	if _, err := snap.Restore(); err == nil {
		t.Fatal("accepted unknown hold version")
	}
	fresh := session.New("bad-hold", session.ModeDefault, s.EnvironmentRef, session.Limits{}, time.Now())
	if err := fresh.RestoreIdentityMigrationHold(session.StateAwaiting, session.IdentityMigrationHold{Reason: "legacy-continuation-unproven", Payload: bytes.Repeat([]byte("x"), (16<<20)+1)}); err == nil || fresh.State != session.StateIdle {
		t.Fatal("hold bounds were not atomic")
	}
	snap, err = Of(s)
	if err != nil {
		t.Fatal(err)
	}
	snap.Messages = append(snap.Messages, messageDTO{Role: session.RoleTool, ToolResult: &session.ToolResult{CallID: "unpaired", Content: "orphan"}})
	if _, err := snap.Restore(); err == nil {
		t.Fatal("accepted orphan canonical result in held history")
	}
	s.Conversation.Append(session.NewToolMessage(session.ToolResult{CallID: "unpaired", Content: "orphan"}))
	if _, err := Of(s); err == nil {
		t.Fatal("saved unsafe held history")
	}
}

func TestDirectLegacySnapshotLiteralIsVersionAware(t *testing.T) {
	old := Snapshot{ID: "direct-old", State: session.StateAwaiting, Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "v1"},
		CreatedAt: time.Unix(0, 0), legacyMessages: []legacyMessageDTO{{Role: session.RoleAssistant, ToolCalls: []legacyCallDTO{{ID: "provider-key", Name: "Read", Args: []byte(`{}`), ItemID: "item"}}}},
		legacyPending:      &legacyPendingAskDTO{AskID: "ask", Call: "provider-key", Tool: "Read", Args: []byte(`{"display":true}`)},
		legacyPendingBytes: []byte(`{"AskID":"ask","Call":"provider-key","Tool":"Read","Args":{"display":true}}`)}
	first, err := old.Restore()
	if err != nil {
		t.Fatal(err)
	}
	second, err := old.Restore()
	if err != nil {
		t.Fatal(err)
	}
	ask, ok := first.PendingAsk()
	if !ok || ask.Call == "provider-key" || ask.Call != first.Conversation.Messages[0].ToolCalls[0].ID || !sameStoredHistory(t, first.Conversation.Messages, second.Conversation.Messages) {
		t.Fatal("direct legacy path reinterpreted identity or allocated randomly")
	}
}
