package sessnap

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacySourceToolCallIDPreservesDerivation(t *testing.T) {
	inc := session.IncarnationID("legacy_" + strings.Repeat("a", 64))
	const want = session.ToolCallID("call_legacy_f7a6dc9b5fe9f199006b0aa28f3696e570191dab4d26e3b7e5f1958da9840ea7")
	if got := legacySourceToolCallID(inc, 7, 2); got != want {
		t.Fatalf("legacy source ToolCallID = %q, want %q", got, want)
	}
}

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
}

func TestLegacyIdentityStableAcrossReadsAndSaves(t *testing.T) {
	for _, name := range []string{"legacy-completed", "legacy-retry"} {
		t.Run(name, func(t *testing.T) {
			data := legacyFixture(t, name)
			first := restoreFixture(t, data)
			second := restoreFixture(t, data)
			assertLegacyControlValues(t, name, first)
			assertLegacyControlValues(t, name, second)
			if first.Incarnation() != second.Incarnation() || !reflect.DeepEqual(first.Conversation.Messages, second.Conversation.Messages) {
				t.Fatal("reads before save disagree")
			}
			var old Snapshot
			if err := json.Unmarshal(data, &old); err != nil {
				t.Fatal(err)
			}
			if old.IdentitySchema != 2 || len(old.Messages) != len(first.Conversation.Messages) || old.Pending != nil || old.PendingAuthorization != nil {
				t.Fatal("legacy decode did not normalize to the current schema")
			}
			// A decoded legacy Snapshot re-encodes as the current schema.
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

func TestLegacyReplaySurvivesForkPruneAndOwnership(t *testing.T) {
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
	if !child.LearningEligible() || s.LearningEligible() {
		t.Fatal("fresh child inherited legacy exclusion or legacy source became eligible")
	}
	if err := s.ReplaceHistoryAtBoundary(want); err != nil {
		t.Fatal(err)
	}
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Conversation.Messages[0].Replay.Bindings[0].PairingID = "mutated"
	restored, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Conversation.Messages, want) {
		t.Fatal("Of aliases source metadata")
	}
	restored.Conversation.Messages[0].Replay.Provider.Data[0] = '!'
	restored.Conversation.Messages[0].Replay.Bindings[0].ItemID = "changed"
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
	encrypted := restoreFixture(t, []byte(`{"id":"replay","state":"idle","environment_ref":{"Kind":"mem","ID":"workspace","Revision":"v1"},"messages":[{"role":"assistant","reasoning":"encrypted-replay-v0","reasoning_item_id":"rs_legacy_1","phase":"commentary"}]}`))
	var encryptedEnvelope map[string]string
	if err := json.Unmarshal(encrypted.Conversation.Messages[0].Replay.Provider.Data, &encryptedEnvelope); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(encryptedEnvelope, map[string]string{"reasoning": "encrypted-replay-v0", "reasoning_item_id": "rs_legacy_1", "phase": "commentary"}) {
		t.Fatal("encrypted replay envelope changed")
	}
	completed := restoreFixture(t, legacyFixture(t, "legacy-completed"))
	m := completed.Conversation.Messages[1]
	if m.Replay.Provider.Format != "mecatl.legacy-message" || m.Replay.Provider.Version != 1 {
		t.Fatal("wrong generic replay namespace")
	}
	var old struct {
		Messages []legacyMessageDTO `json:"messages"`
	}
	if err := json.Unmarshal(legacyFixture(t, "legacy-completed"), &old); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]string
	if err := json.Unmarshal(m.Replay.Provider.Data, &envelope); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(envelope, map[string]string{"reasoning": old.Messages[1].Reasoning, "reasoning_item_id": old.Messages[1].ReasoningItemID, "phase": old.Messages[1].Phase}) {
		t.Fatal("generic replay payload changed")
	}
}

func TestLegacyOrphanResultRejected(t *testing.T) {
	data := legacyFixture(t, "legacy-completed")
	data = bytes.Replace(data, []byte(`"CallID":"reused", "Content":"first"`), []byte(`"CallID":"missing", "Content":"first"`), 1)
	if _, err := Unmarshal(data); err == nil {
		t.Fatal("accepted orphan legacy result")
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
	incarnation := session.NewIncarnationID()
	encodedIncarnation, err := json.Marshal(incarnation)
	if err != nil {
		t.Fatal(err)
	}
	data = withJSONFields(t, legacyFixture(t, "legacy-completed"), map[string]json.RawMessage{"incarnation": encodedIncarnation})
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	withInc, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if withInc.Incarnation() != incarnation || withInc.LearningEligible() || withInc.Conversation.Messages[1].ToolCalls[0].ID != legacySourceToolCallID(incarnation, 1, 0) {
		t.Fatal("conversion did not use stored incarnation or enabled learning")
	}
	if strings.Contains(string(withInc.Conversation.Messages[1].ToolCalls[0].ID), "reused") {
		t.Fatal("provider key leaked into canonical identity")
	}
}

func TestNormalizedCodecRejectsOrphanCanonicalResults(t *testing.T) {
	s := restoreFixture(t, legacyFixture(t, "legacy-completed"))
	snap, err := Of(s)
	if err != nil {
		t.Fatal(err)
	}
	snap.Messages = append(snap.Messages, messageDTO{Role: session.RoleTool, ToolResult: &session.ToolResult{CallID: "unpaired", Content: "orphan"}})
	if _, err := snap.Restore(); err == nil {
		t.Fatal("accepted orphan canonical result in normalized history")
	}
	s.Conversation.Append(session.NewToolMessage(session.ToolResult{CallID: "unpaired", Content: "orphan"}))
	if _, err := Of(s); err == nil {
		t.Fatal("saved unsafe normalized history")
	}
}
