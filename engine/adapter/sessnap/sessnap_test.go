package sessnap_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// runningSession builds a session in StateRunning carrying conversation,
// counters and limits, used by several round-trip tests.
func runningSession(t *testing.T) *session.Session {
	t.Helper()
	s := session.New("s1", session.ModePlan, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{
		MaxTurns: 10, MaxToolCalls: 20, MaxConsecutiveFailures: 3,
	}, time.Unix(1700000000, 0).UTC())
	// Phase 1 inert labels + cumulative usage: exercised by every round-trip test
	// via assertEquivalent.
	s.Profile = "no-fs"
	s.ProviderID = "openrouter"
	s.ModelID = "anthropic/claude-3.5-sonnet"
	s.ReasoningEffort = "high"
	s.DebugMCPServers = []string{"github"}
	s.DebugMCPTools = []string{"mcp__github__get_issue"}
	s.DebugTargetFingerprint = "internal-incarnation-token"
	s.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	s.SetTitle("Fix the flaky CI job")
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.RecordUsage(session.Usage{
		InputTokens: 900, OutputTokens: 250, CacheReadTokens: 600, CacheWriteTokens: 100, ReasoningTokens: 80,
	}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("thinking", "rsn", []session.ToolCall{
		session.NewToolCall("c1", "Edit", json.RawMessage(`{"path":"x"}`)),
	})); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.RecordToolResults([]session.ToolResult{
		session.NewToolResult("c1", "ok"),
		session.NewToolError("c2", "boom"),
	}); err != nil {
		t.Fatalf("RecordToolResults: %v", err)
	}
	return s
}

// assertEquivalent checks the fields the store must preserve.
func assertEquivalent(t *testing.T, got, want *session.Session) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.State != want.State {
		t.Errorf("State = %q, want %q", got.State, want.State)
	}
	if got.Mode != want.Mode {
		t.Errorf("Mode = %q, want %q", got.Mode, want.Mode)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if got.Limits != want.Limits {
		t.Errorf("Limits = %+v, want %+v", got.Limits, want.Limits)
	}
	if got.Counters != want.Counters {
		t.Errorf("Counters = %+v, want %+v", got.Counters, want.Counters)
	}
	if got.Profile != want.Profile {
		t.Errorf("Profile = %q, want %q", got.Profile, want.Profile)
	}
	if got.ProviderID != want.ProviderID {
		t.Errorf("ProviderID = %q, want %q", got.ProviderID, want.ProviderID)
	}
	if got.ModelID != want.ModelID {
		t.Errorf("ModelID = %q, want %q", got.ModelID, want.ModelID)
	}
	if got.ReasoningEffort != want.ReasoningEffort {
		t.Errorf("ReasoningEffort = %q, want %q", got.ReasoningEffort, want.ReasoningEffort)
	}
	if !reflect.DeepEqual(got.DebugMCPServers, want.DebugMCPServers) || !reflect.DeepEqual(got.DebugMCPTools, want.DebugMCPTools) {
		t.Errorf("debug MCP labels = %v/%v, want %v/%v", got.DebugMCPServers, got.DebugMCPTools, want.DebugMCPServers, want.DebugMCPTools)
	}
	if got.DebugTargetFingerprint != want.DebugTargetFingerprint {
		t.Errorf("DebugTargetFingerprint was not preserved")
	}
	if got.EnvironmentRef != want.EnvironmentRef {
		t.Errorf("EnvironmentRef = %+v, want %+v", got.EnvironmentRef, want.EnvironmentRef)
	}
	if got.Title != want.Title {
		t.Errorf("Title = %q, want %q", got.Title, want.Title)
	}
	if got.TitleProvenance != want.TitleProvenance {
		t.Errorf("TitleProvenance = %q, want %q", got.TitleProvenance, want.TitleProvenance)
	}
	if got.Usage != want.Usage {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want.Usage)
	}
	if !reflect.DeepEqual(got.Conversation, want.Conversation) {
		t.Errorf("Conversation mismatch:\n got = %+v\nwant = %+v", got.Conversation, want.Conversation)
	}
	gotR, gotOK := got.StopReason()
	wantR, wantOK := want.StopReason()
	if gotR != wantR || gotOK != wantOK {
		t.Errorf("StopReason = %q,%v; want %q,%v", gotR, gotOK, wantR, wantOK)
	}
}

func TestRoundTripRunning(t *testing.T) {
	want := runningSession(t)
	got, err := sessnap.Unmarshal(mustMarshal(t, want))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	assertEquivalent(t, got, want)
}

func TestRoundTripAwaitingPreservesPendingAsk(t *testing.T) {
	want := runningSession(t)
	ask := session.PendingAsk{
		AskID:  "ask-1",
		Tool:   "Shell",
		Args:   json.RawMessage(`{"cmd":"rm -rf /"}`),
		Reason: "destructive",
		Call:   "call-1",
	}
	if err := want.PauseForApproval(ask); err != nil {
		t.Fatalf("PauseForApproval: %v", err)
	}

	got, err := sessnap.Unmarshal(mustMarshal(t, want))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	assertEquivalent(t, got, want)

	gotAsk, ok := got.PendingAsk()
	if !ok {
		t.Fatalf("restored session has no pending ask; want one")
	}
	if gotAsk.AskID != ask.AskID || gotAsk.Tool != ask.Tool ||
		gotAsk.Reason != ask.Reason || string(gotAsk.Args) != string(ask.Args) ||
		gotAsk.Call != ask.Call {
		t.Fatalf("pending ask = %+v, want %+v", gotAsk, ask)
	}

	// The restored session must be resumable to continue the loop.
	if got.State != session.StateAwaiting {
		t.Fatalf("restored state = %q, want awaiting", got.State)
	}
}

func TestRoundTripTerminalStates(t *testing.T) {
	cases := []struct {
		name     string
		drive    func(*session.Session)
		wantStop session.StopReason
	}{
		{"completed", func(s *session.Session) { _ = s.Complete() }, session.StopEndTurn},
		{"stopped_max_tool_calls", func(s *session.Session) { _ = s.Stop(session.StopMaxToolCalls) }, session.StopMaxToolCalls},
		{"cancelled", func(s *session.Session) { _ = s.Cancel() }, session.StopCancelled},
		{"failed", func(s *session.Session) { _ = s.Fail() }, session.StopError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := runningSession(t)
			tc.drive(want)
			got, err := sessnap.Unmarshal(mustMarshal(t, want))
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			assertEquivalent(t, got, want)
			if r, _ := got.StopReason(); r != tc.wantStop {
				t.Fatalf("restored StopReason = %q, want %q", r, tc.wantStop)
			}
		})
	}
}

// TestRoundTripRecordedStopReason asserts the snapshot captures and restores the
// EXACT terminal reason explicitly recorded on the session (via Stop), without
// inferring it from the conflated StopReason()/limit derivation. The session is
// driven so its Limits would derive a DIFFERENT reason than the one recorded, so
// a faithful round-trip can only come from RecordedStopReason.
func TestRoundTripRecordedStopReason(t *testing.T) {
	want := session.New("rec1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{
		// MaxTurns: 1 would derive StopMaxTurns once a turn begins...
		MaxTurns: 1,
	}, time.Unix(1700000000, 0).UTC())
	if err := want.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	// ...but we explicitly record a DIFFERENT reason.
	if err := want.Stop(session.StopMaxConsecutiveFailures); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	wantRec, ok := want.RecordedStopReason()
	if !ok || wantRec != session.StopMaxConsecutiveFailures {
		t.Fatalf("precondition RecordedStopReason = %q,%v", wantRec, ok)
	}

	got, err := sessnap.Unmarshal(mustMarshal(t, want))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	gotRec, gotOK := got.RecordedStopReason()
	if !gotOK || gotRec != session.StopMaxConsecutiveFailures {
		t.Fatalf("restored RecordedStopReason = %q,%v; want %q,true",
			gotRec, gotOK, session.StopMaxConsecutiveFailures)
	}
	if got.State != session.StateCompleted {
		t.Fatalf("restored state = %q, want completed", got.State)
	}
}

func TestRoundTripIdle(t *testing.T) {
	want := session.New("idle1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/w", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0).UTC())
	got, err := sessnap.Unmarshal(mustMarshal(t, want))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	assertEquivalent(t, got, want)
}

func mustMarshal(t *testing.T, s *session.Session) []byte {
	t.Helper()
	b, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return b
}

// TestRoundTripWithParts asserts a user message carrying image+audio media parts
// survives a Marshal→Unmarshal round-trip (bytes are base64 in JSON).
func TestRoundTripWithParts(t *testing.T) {
	s := session.New("m1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	parts := []session.Content{
		{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
		{Kind: session.MediaAudio, MIMEType: "audio/wav", URL: "https://example.com/a.wav"},
	}
	if err := s.RecordUserPromptWithParts("describe", parts, nil); err != nil {
		t.Fatalf("record: %v", err)
	}

	line, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.Conversation, s.Conversation) {
		t.Fatalf("conversation mismatch:\n got=%+v\nwant=%+v", got.Conversation, s.Conversation)
	}
	gm := got.Conversation.Messages[0]
	if len(gm.Parts) != 2 || gm.Parts[0].Kind != session.MediaImage || gm.Parts[1].URL != "https://example.com/a.wav" {
		t.Fatalf("restored parts = %+v", gm.Parts)
	}
}

// TestSnapshotRoundTripsPhase asserts the OpenAI Responses phase marker on an
// assistant message survives Marshal -> Unmarshal (issue #46). The DTO field is
// omitempty, so an empty phase is wire-omitted and an old snapshot still decodes.
// It uses an UNKNOWN, non-enum value ("some_future_phase_v2") to also lock the
// forward-compat opaque-pass-through guarantee: snapshotting never validates or
// branches on the value, so a novel phase round-trips verbatim.
func TestSnapshotRoundTripsPhase(t *testing.T) {
	const novel = "some_future_phase_v2"
	s := session.New("p1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	m := session.NewAssistantMessage("Done.", "", nil)
	m.ProviderPhase = novel
	if err := s.RecordAssistant(m); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}

	line, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(line), `"phase":"`+novel+`"`) {
		t.Fatalf("snapshot JSON missing the VERBATIM novel phase %q; got:\n%s", novel, line)
	}
	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	gm := got.Conversation.Messages[0]
	if gm.ProviderPhase != novel {
		t.Fatalf("restored ProviderPhase = %q, want the verbatim novel value %q", gm.ProviderPhase, novel)
	}

	// An empty-phase assistant message must wire-omit the key (additive, no bump).
	s2 := session.New("p2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s2.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s2.RecordAssistant(session.NewAssistantMessage("plain", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	line2, err := sessnap.Marshal(s2)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(line2), `"phase"`) {
		t.Fatalf("empty-phase snapshot must omit the phase key; got:\n%s", line2)
	}
}

// TestSnapshotRoundTripsReasoningItemID asserts the OpenAI Responses
// reasoning-item id (Message.ReasoningItemID) survives Marshal -> Unmarshal so a
// restarted process can replay the provider-assigned reasoning-item id on
// subsequent stateless turns (prevents the "id":"""" -> 400 replay bug). The DTO
// field is omitempty, so an empty id is wire-omitted and an old snapshot still
// decodes. Mirrors TestSnapshotRoundTripsPhase's shape.
func TestSnapshotRoundTripsReasoningItemID(t *testing.T) {
	const id = "rs_x"
	s := session.New("rid1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	m := session.NewAssistantMessage("thinking blob", "rsn", nil)
	m.ReasoningItemID = id
	if err := s.RecordAssistant(m); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}

	line, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(line), `"reasoning_item_id":"`+id+`"`) {
		t.Fatalf("snapshot JSON missing reasoning_item_id key with value %q; got:\n%s", id, line)
	}
	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	gm := got.Conversation.Messages[0]
	if gm.ReasoningItemID != id {
		t.Fatalf("restored ReasoningItemID = %q, want %q", gm.ReasoningItemID, id)
	}

	// An empty ReasoningItemID assistant message must wire-omit the key (additive, no bump).
	s2 := session.New("rid2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s2.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s2.RecordAssistant(session.NewAssistantMessage("plain", "rsn", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	line2, err := sessnap.Marshal(s2)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(line2), `"reasoning_item_id"`) {
		t.Fatalf("empty-id snapshot must omit the reasoning_item_id key; got:\n%s", line2)
	}
}

// TestSnapshotRoundTripsItemID asserts that a ToolCall carrying a non-empty
// ItemID survives a Marshal -> Unmarshal round-trip with the value intact, and
// that the wire JSON contains the "item_id" key (not a vacuous no-op).
//
// This pins the fix for the "Duplicate item found with id fc_N" error from Azure
// GPT-5.x: the ToolCall.ItemID field must survive snapshot persistence so that a
// restarted process can still replay the provider-assigned item id on subsequent
// stateless turns. It also verifies backward compat: an old snapshot with no
// "item_id" key decodes with ItemID == "" (the zero value, wire-omitted on
// replay — safe, non-OpenAI providers / pre-fix captures stay unchanged).
func TestSnapshotRoundTripsItemID(t *testing.T) {
	const itemID = "fc_7"
	s := session.New("itm1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	call := session.ToolCall{
		ID:     "call_abc",
		Name:   "read_file",
		Args:   json.RawMessage(`{"path":"a.go"}`),
		ItemID: itemID,
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}

	line, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// The item_id key must be present in the wire JSON (non-vacuous round-trip).
	if !strings.Contains(string(line), `"item_id":"`+itemID+`"`) {
		t.Fatalf("snapshot JSON missing item_id key with value %q; got:\n%s", itemID, line)
	}

	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	msgs := got.Conversation.Messages
	if len(msgs) == 0 {
		t.Fatalf("restored conversation has no messages")
	}
	var restoredCall *session.ToolCall
	for i := range msgs {
		if len(msgs[i].ToolCalls) > 0 {
			c := msgs[i].ToolCalls[0]
			restoredCall = &c
			break
		}
	}
	if restoredCall == nil {
		t.Fatalf("restored conversation has no ToolCalls in any message")
		return
	}
	if restoredCall.ItemID != itemID {
		t.Fatalf("restored ItemID = %q, want %q", restoredCall.ItemID, itemID)
	}

	// Backward-compat: an old snapshot without an "item_id" key decodes with
	// ItemID == "" (wire-omit safe — replaying without an id is the pre-fix behavior).
	oldSnap := `{"id":"old2","state":"idle","mode":"default","limits":{},"counters":{},` +
		`"environment_ref":{"Kind":"local","ID":"/ws","Revision":"in-tree-v1"},"created_at":"2023-11-14T22:13:20Z",` +
		`"messages":[{"role":"assistant","tool_calls":[{"ID":"call_x","Name":"read_file","Args":{"path":"z.go"}}]}]}`
	got2, err := sessnap.Unmarshal([]byte(oldSnap))
	if err != nil {
		t.Fatalf("Unmarshal old snapshot: %v", err)
	}
	if len(got2.Conversation.Messages) == 0 || len(got2.Conversation.Messages[0].ToolCalls) == 0 {
		t.Fatalf("old snapshot has no tool calls after unmarshal")
	}
	if got2.Conversation.Messages[0].ToolCalls[0].ItemID != "" {
		t.Fatalf("old snapshot (no item_id key) decoded ItemID = %q, want empty",
			got2.Conversation.Messages[0].ToolCalls[0].ItemID)
	}
}

// TestLoadV1SnapshotNoPartsIsTextOnly asserts a v1 snapshot JSON with no "parts"
// key decodes to a text-only message (nil Parts) without error — the additive
// field is back-compatible.
func TestLoadV1SnapshotNoPartsIsTextOnly(t *testing.T) {
	v1 := `{"id":"old","state":"idle","mode":"default","limits":{},"counters":{},` +
		`"environment_ref":{"Kind":"local","ID":"/ws","Revision":"in-tree-v1"},"created_at":"2023-11-14T22:13:20Z",` +
		`"messages":[{"role":"user","text":"hello there"}]}`
	got, err := sessnap.Unmarshal([]byte(v1))
	if err != nil {
		t.Fatalf("Unmarshal v1: %v", err)
	}
	if got.Conversation.Len() != 1 {
		t.Fatalf("messages = %d, want 1", got.Conversation.Len())
	}
	m := got.Conversation.Messages[0]
	if m.Text != "hello there" {
		t.Fatalf("text = %q", m.Text)
	}
	if m.Parts != nil {
		t.Fatalf("Parts = %v, want nil for a v1 (no parts) snapshot", m.Parts)
	}
}

// TestSnapshotRoundTripsPhase1Fields pins the four Phase 1 additive fields
// (profile, provider_id, model_id, usage) round-trip through Marshal/Unmarshal
// AND that they actually appear in the JSON (so the round-trip is not vacuously
// satisfied by both sides being zero). Mutation: dropping any field from Of or
// Restore fails this.
func TestSnapshotRoundTripsPhase1Fields(t *testing.T) {
	want := runningSession(t)
	line := mustMarshal(t, want)

	// The keys are present in the wire form (the round-trip carries real data).
	for _, key := range []string{`"profile"`, `"provider_id"`, `"model_id"`, `"reasoning_effort"`, `"usage"`} {
		if !strings.Contains(string(line), key) {
			t.Errorf("marshalled snapshot missing %s key:\n%s", key, line)
		}
	}

	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Profile != "no-fs" || got.ProviderID != "openrouter" || got.ModelID != "anthropic/claude-3.5-sonnet" {
		t.Errorf("labels not restored: profile=%q provider=%q model=%q", got.Profile, got.ProviderID, got.ModelID)
	}
	if got.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort not restored: got %q want \"high\"", got.ReasoningEffort)
	}
	wantUsage := session.Usage{InputTokens: 900, OutputTokens: 250, CacheReadTokens: 600, CacheWriteTokens: 100, ReasoningTokens: 80}
	if got.Usage != wantUsage {
		t.Errorf("Usage = %+v, want %+v", got.Usage, wantUsage)
	}
}

// TestZeroUsageOmittedFromSnapshot pins the pointer-omitempty discipline: a
// session with zero Usage and empty labels emits NEITHER a "usage" key NOR the
// label keys, so a default session's snapshot stays byte-compatible with a
// pre-Phase-1 one.
func TestZeroUsageOmittedFromSnapshot(t *testing.T) {
	s := session.New("z", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	line := mustMarshal(t, s)
	for _, key := range []string{`"usage"`, `"profile"`, `"provider_id"`, `"model_id"`} {
		if strings.Contains(string(line), key) {
			t.Errorf("zero-value snapshot unexpectedly carries %s:\n%s", key, line)
		}
	}
}

// TestLoadV1SnapshotMissingPhase1FieldsLoads is the downgrade/adversarial guard:
// a snapshot with none of the Phase 1 keys still loads with an empty profile,
// empty selector, and zero Usage. Its exact EnvironmentRef remains authoritative;
// composition does not infer placement from a duplicate workspace field.
func TestLoadV1SnapshotMissingPhase1FieldsLoads(t *testing.T) {
	v1 := `{"id":"old","state":"idle","mode":"default","limits":{},"counters":{},` +
		`"environment_ref":{"Kind":"local","ID":"/ws","Revision":"in-tree-v1"},"created_at":"2023-11-14T22:13:20Z",` +
		`"messages":[{"role":"user","text":"hello there"}]}`
	got, err := sessnap.Unmarshal([]byte(v1))
	if err != nil {
		t.Fatalf("Unmarshal v1: %v", err)
	}
	if got.Profile != "" {
		t.Errorf("Profile = %q, want empty for a pre-Phase-1 snapshot", got.Profile)
	}
	if got.ProviderID != "" || got.ModelID != "" {
		t.Errorf("selector not empty: provider=%q model=%q", got.ProviderID, got.ModelID)
	}
	if got.Usage != (session.Usage{}) {
		t.Errorf("Usage = %+v, want zero for a snapshot with no usage key", got.Usage)
	}
}

// TestLoadV1SnapshotMissingTitleKeyLoads is the additive-field guard for Title:
// a snapshot with NO "title" key (a pre-Title snapshot, or one from a session
// that never seeded a title) decodes to an EMPTY Title — purely additive, no
// format-tag bump (the same omitempty precedent as Profile/ProviderID). The
// lazy deriveTitle fallback applies on read.
func TestLoadV1SnapshotMissingTitleKeyLoads(t *testing.T) {
	v1 := `{"id":"old","state":"idle","mode":"default","limits":{},"counters":{},` +
		`"environment_ref":{"Kind":"local","ID":"/ws","Revision":"in-tree-v1"},"created_at":"2023-11-14T22:13:20Z",` +
		`"messages":[{"role":"user","text":"hello there"}]}`
	got, err := sessnap.Unmarshal([]byte(v1))
	if err != nil {
		t.Fatalf("Unmarshal v1: %v", err)
	}
	if got.Title != "" {
		t.Errorf("Title = %q, want empty for a snapshot with no title key", got.Title)
	}
}

// TestRoundTripTitlePreserved asserts the seeded Title survives a marshal→
// unmarshal round-trip (the omitempty tag must NOT drop a non-empty title).
func TestRoundTripTitlePreserved(t *testing.T) {
	want := runningSession(t) // seeds Title "Fix the flaky CI job"
	line := mustMarshal(t, want)
	// The wire JSON must carry the title key (omitempty only drops the empty case).
	if !strings.Contains(string(line), `"title":`) {
		t.Fatalf("marshaled snapshot missing the title key:\n%s", line)
	}
	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	assertEquivalent(t, got, want)
}

// TestRoundTripEmptyTitleOmitsKey asserts an empty Title marshals to no title
// key (byte-identical to a pre-Title snapshot) — the omitempty contract.
func TestRoundTripEmptyTitleOmitsKey(t *testing.T) {
	s := session.New("s2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	line := mustMarshal(t, s)
	if strings.Contains(string(line), `"title"`) {
		t.Fatalf("empty-Title snapshot unexpectedly carries a title key:\n%s", line)
	}
	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Title != "" {
		t.Errorf("restored Title = %q, want empty", got.Title)
	}
}

// TestSnapshotRoundTripsPermanentFailed asserts a failed+permanent session round-trips
// true through Marshal/Unmarshal: the permanence flag on the session survives
// serialisation.
func TestSnapshotRoundTripsPermanentFailed(t *testing.T) {
	s := session.New("perm1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.RecordUserPrompt("do stuff", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.Fail(); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := s.RecordFailurePermanence(true); err != nil {
		t.Fatalf("RecordFailurePermanence: %v", err)
	}

	line := mustMarshal(t, s)
	// Wire JSON must carry the permanent key.
	if !strings.Contains(string(line), `"permanent":true`) {
		t.Fatalf("snapshot JSON missing permanent key; got:\n%s", line)
	}

	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.State != session.StateFailed {
		t.Fatalf("restored state = %q, want failed", got.State)
	}
	if !got.FailurePermanence() {
		t.Fatal("restored FailurePermanence = false, want true")
	}
}

// TestSnapshotFailedWithoutPermanentFlagRoundTripsFalse asserts a failed session with
// NO permanence stamp round-trips with FailurePermanence()==false.
func TestSnapshotFailedWithoutPermanentFlagRoundTripsFalse(t *testing.T) {
	s := session.New("perm2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.RecordUserPrompt("do stuff", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.Fail(); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	// Deliberately NO RecordFailurePermanence call.

	line := mustMarshal(t, s)
	// Wire JSON must NOT carry the permanent key (omitempty on false).
	if strings.Contains(string(line), `"permanent"`) {
		t.Fatalf("snapshot JSON unexpectedly carries permanent key; got:\n%s", line)
	}

	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.State != session.StateFailed {
		t.Fatalf("restored state = %q, want failed", got.State)
	}
	if got.FailurePermanence() {
		t.Fatal("restored FailurePermanence = true, want false")
	}
}

// TestLoadV1SnapshotMissingPermanentKeyLoadsFalse asserts backward-compat: an OLD JSON
// snapshot with no "permanent" key decodes with FailurePermanence()==false (the
// omitempty zero value). This is the downgrade/adversarial guard.
func TestLoadV1SnapshotMissingPermanentKeyLoadsFalse(t *testing.T) {
	v1 := `{"id":"old","state":"failed","mode":"default","limits":{},"counters":{},` +
		`"environment_ref":{"Kind":"local","ID":"/ws","Revision":"in-tree-v1"},"created_at":"2023-11-14T22:13:20Z",` +
		`"messages":[{"role":"user","text":"hello there"}],` +
		`"stop_reason":"error"}`
	got, err := sessnap.Unmarshal([]byte(v1))
	if err != nil {
		t.Fatalf("Unmarshal v1: %v", err)
	}
	if got.State != session.StateFailed {
		t.Fatalf("restored state = %q, want failed", got.State)
	}
	if got.FailurePermanence() {
		t.Fatal("restored FailurePermanence = true, want false (pre-permanent snapshot)")
	}
}

// TestSnapshotRoundTripsLastErrorFailed asserts a failed session stamped with a
// terminal cause round-trips it through Marshal/Unmarshal: the last_error key on the
// session survives serialisation (issue #332 — the snapshot is the durable cause
// source, so the marshal path must carry it).
func TestSnapshotRoundTripsLastErrorFailed(t *testing.T) {
	s := session.New("le1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.RecordUserPrompt("do stuff", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.Fail(); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := s.RecordLastError("upstream 503: model overloaded"); err != nil {
		t.Fatalf("RecordLastError: %v", err)
	}

	line := mustMarshal(t, s)
	// Wire JSON must carry the last_error key.
	if !strings.Contains(string(line), `"last_error":"upstream 503: model overloaded"`) {
		t.Fatalf("snapshot JSON missing last_error key; got:\n%s", line)
	}

	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.State != session.StateFailed {
		t.Fatalf("restored state = %q, want failed", got.State)
	}
	if got.LastError() != "upstream 503: model overloaded" {
		t.Fatalf("restored LastError = %q, want the stamped cause", got.LastError())
	}
}

// TestSnapshotFailedWithoutLastErrorRoundTripsEmpty asserts a failed session with NO
// cause stamp round-trips with LastError()=="" (omitempty on the empty string keeps
// the key off the wire).
func TestSnapshotFailedWithoutLastErrorRoundTripsEmpty(t *testing.T) {
	s := session.New("le2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.RecordUserPrompt("do stuff", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.Fail(); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	// Deliberately NO RecordLastError call.

	line := mustMarshal(t, s)
	// Wire JSON must NOT carry the last_error key (omitempty on "").
	if strings.Contains(string(line), `"last_error"`) {
		t.Fatalf("snapshot JSON unexpectedly carries last_error key; got:\n%s", line)
	}

	got, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.State != session.StateFailed {
		t.Fatalf("restored state = %q, want failed", got.State)
	}
	if got.LastError() != "" {
		t.Fatalf("restored LastError = %q, want empty", got.LastError())
	}
}

// TestLoadV1SnapshotMissingLastErrorKeyLoadsEmpty asserts backward-compat: an OLD
// JSON snapshot with no "last_error" key decodes with LastError()=="" (the omitempty
// zero value). This is the downgrade/adversarial guard (issue #332).
func TestLoadV1SnapshotMissingLastErrorKeyLoadsEmpty(t *testing.T) {
	v1 := `{"id":"old","state":"failed","mode":"default","limits":{},"counters":{},` +
		`"environment_ref":{"Kind":"local","ID":"/ws","Revision":"in-tree-v1"},"created_at":"2023-11-14T22:13:20Z",` +
		`"messages":[{"role":"user","text":"hello there"}],` +
		`"stop_reason":"error"}`
	got, err := sessnap.Unmarshal([]byte(v1))
	if err != nil {
		t.Fatalf("Unmarshal v1: %v", err)
	}
	if got.State != session.StateFailed {
		t.Fatalf("restored state = %q, want failed", got.State)
	}
	if got.LastError() != "" {
		t.Fatalf("restored LastError = %q, want empty (pre-last_error snapshot)", got.LastError())
	}
}

// TestSnapshotEnvironmentRefRoundTrip proves a non-zero EnvironmentRef survives
// Marshal→Unmarshal (ADR 0214, issue #462 phase 3).
func TestSnapshotEnvironmentRefRoundTrip(t *testing.T) {
	s := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Unix(1700000000, 0).UTC())
	s.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "inventory-v3"}
	got, err := sessnap.Unmarshal(mustMarshal(t, s))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.EnvironmentRef != s.EnvironmentRef {
		t.Fatalf("EnvironmentRef = %+v, want %+v", got.EnvironmentRef, s.EnvironmentRef)
	}
}

func TestSnapshotEnvironmentRefIsRequired(t *testing.T) {
	v1 := `{"id":"old","state":"idle","mode":"default","limits":{},"counters":{},` +
		`"created_at":"2023-11-14T22:13:20Z","messages":[]}`
	if _, err := sessnap.Unmarshal([]byte(v1)); err == nil {
		t.Fatal("snapshot without environment_ref restored successfully")
	}
}

// TestSnapshotEnvironmentRefForwardCompat proves an unknown extra key in a
// future snapshot does not break decode.
func TestSnapshotEnvironmentRefForwardCompat(t *testing.T) {
	v1 := `{"id":"new","state":"idle","mode":"default","limits":{},"counters":{},` +
		`"environment_ref":{"Kind":"local","ID":"/ws","Revision":"in-tree-v1"},"created_at":"2023-11-14T22:13:20Z","messages":[],` +
		`"future_key":123}`
	got, err := sessnap.Unmarshal([]byte(v1))
	if err != nil {
		t.Fatalf("Unmarshal forward: %v", err)
	}
	want := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	if got.EnvironmentRef != want {
		t.Fatalf("forward EnvironmentRef = %+v, want %+v", got.EnvironmentRef, want)
	}
}

func BenchmarkSnapshotOrdinarySession(b *testing.B) {
	s := session.New("ordinary", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0).UTC())
	b.ReportAllocs()
	for b.Loop() {
		if _, err := sessnap.Of(s); err != nil {
			b.Fatal(err)
		}
	}
}
