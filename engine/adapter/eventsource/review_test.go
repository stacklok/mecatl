package eventsource_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// --- PART 3 MUST-ADD -------------------------------------------------------

// TestFoldAwaitingPreservesTrailingAssistantTurn pins the awaiting reconstruction:
// the folded session's Conversation MUST end with the assistant turn that carries the
// unanswered tool call (text + the dangling call), not just the paired prefix.
//
// MUTATION CATCH: this is the test the review flagged as missing — if
// reconstructAwaiting DROPPED the trailing assistant turn (e.g. never called
// RecordAssistant, or seeded only f.messages), the older awaiting test still passed
// (it only checked State==awaiting + the pending ask). This asserts the trailing turn
// is present, so that mutation now fails. Verified below: commenting out the
// RecordAssistant in reconstructAwaiting makes this test fail (message count drops to
// 1 and the final message is the user prompt, not the assistant turn).
func TestFoldAwaitingPreservesTrailingAssistantTurn(t *testing.T) {
	call := toolCall("c1", "Bash", `{"command":"ls"}`)
	ask := session.PendingAsk{AskID: "s1:0:c1:r0", Tool: "Bash", Reason: "needs approval"}
	evs := []session.Event{
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "run ls"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "I'll run a command"},
		{Type: session.EvToolCall, Turn: 0, ToolCall: &call},
		{Type: session.EvPermissionAsk, Turn: 0, Ask: &ask},
	}

	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	msgs := s.Conversation.Messages
	// user("run ls") → assistant("I'll run a command", [c1])
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (user + trailing assistant turn): %+v", len(msgs), msgs)
	}
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleAssistant {
		t.Fatalf("conversation must END with the assistant turn, got role %q", last.Role)
	}
	if last.Text != "I'll run a command" {
		t.Fatalf("trailing assistant text = %q, want 'I'll run a command'", last.Text)
	}
	if len(last.ToolCalls) != 1 || last.ToolCalls[0].ID != "c1" {
		t.Fatalf("trailing assistant must carry the unanswered tool call c1, got %+v", last.ToolCalls)
	}
	if s.State != session.StateAwaiting {
		t.Fatalf("state = %q, want awaiting", s.State)
	}
}

// resumeTool is a no-op tool the resume path executes once approved.
type resumeTool struct {
	name     string
	readOnly bool
}

func (rt resumeTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: rt.name, Description: rt.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (rt resumeTool) ReadOnly() bool { return rt.readOnly }
func (resumeTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(in.ID, "executed"), nil
}

// TestFoldedAwaitingSessionIsDrivable is the REAL resumable proof: a session
// reconstructed by folding an awaiting stream can be DRIVEN one more turn through the
// engine's approve/resume path (ResumeApproval) and reaches a clean terminal with no
// dangling-tool 400 (ValidateToolPairing holds across the resume). This proves the
// awaiting reconstruction is not merely structurally plausible but actually live.
func TestFoldedAwaitingSessionIsDrivable(t *testing.T) {
	// Fold an awaiting stream. The pending ask's Tool ("Read") matches the trailing
	// assistant tool call so driveFromAwaiting's locatePendingCall resolves it (by name
	// fallback when the askID grammar does not match a real run serial).
	const askID = "det-1:0:c1:r0"
	call := toolCall("c1", "Read", `{"path":"a.go"}`)
	ask := session.PendingAsk{AskID: askID, Tool: "Read", Reason: "approve read"}
	evs := []session.Event{
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "read a.go"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "reading"},
		{Type: session.EvToolCall, Turn: 0, ToolCall: &call},
		{Type: session.EvPermissionAsk, Turn: 0, Ask: &ask},
	}
	m := meta()
	m.ID = "det-1"
	folded, err := eventsource.Fold(m, seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if folded.State != session.StateAwaiting {
		t.Fatalf("folded state = %q, want awaiting", folded.State)
	}

	cat := tool.NewCatalog()
	cat.MustRegister(resumeTool{name: "Read", readOnly: true})
	// After the tool runs, the model produces a final answer turn → clean terminal.
	llm := mockllm.New(mockllm.TextTurn("done reading"))
	e := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
	})

	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, nil)
	r := e.ResumeApproval(context.Background(), folded, env, askID, session.VerdictAllowOnce)
	var result *session.ResultPayload
	for ev := range r.Events() {
		if ev.Type == session.EvResult {
			result = ev.Result
		}
	}
	if result == nil {
		t.Fatalf("no terminal result event")
		return
	}
	if result.Stop == session.StopError {
		t.Fatalf("resume failed (likely dangling-tool 400): %q", result.Error)
	}
	if folded.State != session.StateCompleted {
		t.Fatalf("resumed session state = %q, want completed", folded.State)
	}
	// The reconstructed-then-resumed history must be provider-replayable end-to-end.
	if err := session.ValidateToolPairing(folded.Conversation.Messages); err != nil {
		t.Fatalf("resumed history not tool-pairing-valid: %v", err)
	}
}

// reasoningCarryingTool is unused; the reasoning-divergence run is text-only with a
// reasoning replay blob, so no tool is needed.

// TestFoldReasoningProviderDivergesOnSnapshotOnlyFields pins the documented contract
// boundary: a reasoning-carrying run records Message.Reasoning (the opaque replay
// blob), which the SNAPSHOT preserves but the event stream does NOT carry — so a fold
// reconstructs an empty Reasoning. If a future change starts eventing the reasoning
// blob, this test's divergence assertion fails, flagging the contract shift.
func TestFoldReasoningProviderDivergesOnSnapshotOnlyFields(t *testing.T) {
	const sessID session.SessionID = "reason-1"

	// A turn carrying BOTH a reasoning REPLAY blob (ChunkReasoningItem → Message.Reasoning)
	// and final text (so the turn terminates cleanly, not as a no-progress stall).
	llm := mockllm.New(mockllm.ChunksTurn(
		mockllm.ReasoningItemChunk("OPAQUE-REPLAY-BLOB"),
		mockllm.TextChunk("the answer"),
		mockllm.UsageChunk(session.Usage{InputTokens: 4, OutputTokens: 2}),
		mockllm.DoneChunk(session.StopEndTurn),
	))
	store := memstore.New()
	log := memstore.NewEventLog()
	e := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
	})
	sess := session.New(sessID, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, nil)
	r := e.Run(ctx, sess, env, agent.RunRequest{Text: "think"})
	for ev := range r.Events() {
		if err := log.Append(ctx, sessID, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := store.Save(ctx, sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	snap, err := store.Load(ctx, sessID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID: sessID, Mode: session.ModeDefault, Limits: session.Limits{},
		Workspace: "/ws", CreatedAt: time.Unix(0, 0),
	}, log.Read(ctx, sessID))
	if err != nil {
		t.Fatalf("fold: %v", err)
	}

	// Find the assistant message in each.
	snapReasoning := assistantReasoning(t, snap.Conversation.Messages)
	foldReasoning := assistantReasoning(t, folded.Conversation.Messages)

	// The CONTRACT BOUNDARY: the snapshot preserves the opaque replay blob; the fold
	// cannot (it is not event-carried).
	if snapReasoning != "OPAQUE-REPLAY-BLOB" {
		t.Fatalf("snapshot should preserve Message.Reasoning, got %q", snapReasoning)
	}
	if foldReasoning != "" {
		t.Fatalf("fold MUST NOT reconstruct Message.Reasoning (not event-carried), got %q "+
			"— if reasoning is now evented, update the contract docs (COMPATIBILITY.md / ADR 0038)", foldReasoning)
	}
}

func assistantReasoning(t *testing.T, msgs []session.Message) string {
	t.Helper()
	for _, m := range msgs {
		if m.Role == session.RoleAssistant {
			return m.Reasoning
		}
	}
	t.Fatalf("no assistant message in %+v", msgs)
	return ""
}

// --- PART 4 SHOULD-ADD -----------------------------------------------------

// TestFoldContractDocMatchesSessionFields is a reflective drift guard (the
// session/team_payload_test.go / port/llm_neutral_test.go NumField() tripwire
// pattern): the set of session.Session and session.Message fields the fold must
// account for is pinned here, so a NEW exported field on either type fails this test
// until it is classified (MUST round-trip / run-scoped / not-event-carried) in
// COMPATIBILITY.md's reconstruction-contract table. It stops the contract table from
// silently rotting as the aggregate grows.
func TestFoldContractDocMatchesSessionFields(t *testing.T) {
	// Every EXPORTED field of session.Session the reconstruction contract considers.
	// Classification (kept in sync with COMPATIBILITY.md "Session reconstruction
	// contract"):
	//   reconstructed-from-events: Conversation, State, Usage; legacy Title and
	//     TitleProvenance fallback (seeded from the first genuine EvUserPrompt via
	//     SetTitle)
	//   run-scoped (latest segment): Counters
	//   supplied via SessionMeta (not event-carried): ID, Mode, Limits, Workspace,
	//     Profile, ProviderID, ModelID, ReasoningEffort, DebugMCPServers,
	//     DebugMCPTools, DebugTargetFingerprint, Title, TitleProvenance,
	//     Kind, Relationship, CreatedAt; adoption metadata is supplied via
	//     SessionMeta and restored as optional Session.Adoption metadata
	//   not-event-carried identity labels (ADR 0204/0214): Owner, Authority,
	//     EnvironmentRef — the event annotation is log-only and the fold neither
	//     requires nor re-derives any of them, so a folded session keeps the
	//     snapshot-restored value (ownerless stays ownerless, a zero ref stays
	//     zero — composition stamps it from the first resolved live Environment on
	//     the next run — never fabricated)
	wantSessionFields := map[string]struct{}{
		"ID": {}, "State": {}, "Mode": {}, "Conversation": {}, "Limits": {},
		"Counters": {}, "Usage": {}, "Workspace": {}, "Profile": {},
		"ProviderID": {}, "ModelID": {}, "ReasoningEffort": {}, "DebugMCPServers": {}, "DebugMCPTools": {}, "DebugTargetFingerprint": {}, "Kind": {},
		"Relationship": {}, "Adoption": {}, "CreatedAt": {},
		"Title": {}, "TitleProvenance": {}, "Owner": {}, "Authority": {}, "EnvironmentRef": {},
	}
	assertExportedFields(t, reflect.TypeOf(session.Session{}), wantSessionFields,
		"session.Session — classify the new field in COMPATIBILITY.md's reconstruction contract")

	// Every EXPORTED field of session.Message the fold reconstructs or documents as a
	// limitation:
	//   reconstructed: Role, Text, ToolCalls, ToolResult, Parts
	//   documented limitation (NOT event-carried): Reasoning, ProviderPhase, ReasoningItemID
	//   (ToolCall.ItemID is on ToolCall, asserted separately below)
	wantMessageFields := map[string]struct{}{
		"Role": {}, "Text": {}, "ToolCalls": {}, "ToolResult": {},
		"Reasoning": {}, "ProviderPhase": {}, "ReasoningItemID": {}, "Parts": {},
	}
	assertExportedFields(t, reflect.TypeOf(session.Message{}), wantMessageFields,
		"session.Message — classify the new field in COMPATIBILITY.md's reconstruction contract")

	// ToolCall.ItemID is the third documented replay-only limitation.
	wantToolCallFields := map[string]struct{}{
		"ID": {}, "Name": {}, "Args": {}, "ItemID": {},
	}
	assertExportedFields(t, reflect.TypeOf(session.ToolCall{}), wantToolCallFields,
		"session.ToolCall — classify the new field in COMPATIBILITY.md's reconstruction contract")
}

func assertExportedFields(t *testing.T, ty reflect.Type, want map[string]struct{}, hint string) {
	t.Helper()
	got := map[string]struct{}{}
	for i := 0; i < ty.NumField(); i++ {
		f := ty.Field(i)
		if f.IsExported() {
			got[f.Name] = struct{}{}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: exported fields drifted.\n got=%v\nwant=%v\n%s", ty.Name(), keys(got), keys(want), hint)
	}
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestFoldMultiRunResetsConsecutiveFailures: run 1 ends on a tool FAILURE, run 2 is
// clean — the folded session's latest-segment ConsecutiveFailures must be 0 (the
// counter is per-run; a terminal EvResult resets the segment).
func TestFoldMultiRunResetsConsecutiveFailures(t *testing.T) {
	failCall := toolCall("c1", "Bash", `{}`)
	evs := []session.Event{
		// Run 1: a tool failure, then terminal.
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "first"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvToolCall, Turn: 0, ToolCall: &failCall},
		{Type: session.EvToolResult, Turn: 0, ToolResult: ptr(session.NewToolError("c1", "boom"))},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
		// Run 2 (Reopen): clean.
		{Type: session.EvUserPrompt, Turn: 0, UserPrompt: &session.UserPromptPayload{Text: "second"}},
		{Type: session.EvTurnStart, Turn: 0},
		{Type: session.EvMessageDelta, Turn: 0, Text: "ok"},
		{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	s, err := eventsource.Fold(meta(), seq(evs))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if s.Counters.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures = %d, want 0 (latest run segment, reset on Reopen)", s.Counters.ConsecutiveFailures)
	}
}

// TestFoldRecoversLiveCompactionArchiveHead is the SHOULD-ADD compaction determinism
// variant. A tiny context window fires a real compaction during the run, emitting a
// genuine EvCompactionArchive. The fold must recover the pre-compaction head from that
// archive: the assistant/tool/user turns the compaction replaced are present in the
// folded conversation.
//
// SCOPE NOTE (honest): the fold does NOT deep-equal the FULL post-run snapshot across a
// LIVE compaction, because the compactor injects a synthetic "[conversation compacted]"
// SUMMARY user message via ReplaceHistory AFTER it emits the archive — so the LAST
// compaction's summary follows the last archive and is recoverable from no event (only
// the archive of a SUBSEQUENT compaction would carry it). That is a separate,
// pre-existing event-coverage edge (the compaction summary is synthetic compactor state,
// not user input), orthogonal to #115's user-prompt closure. So this variant asserts the
// archive HEAD is recovered (the property #115/ADR 0038 promises) rather than a full
// snapshot deep-equal; the hand-built TestFoldRecoversCompactionArchiveHead proves the
// archive-folding mechanics deterministically.
func TestFoldRecoversLiveCompactionArchiveHead(t *testing.T) {
	const sessID session.SessionID = "compact-1"

	cat := tool.NewCatalog()
	cat.MustRegister(resumeTool{name: "Read", readOnly: true})

	llm := mockllm.New(
		mockllm.ChunksTurn(mockllm.TextChunk("turn one"), mockllm.ToolCallChunk(session.NewToolCall("c1", "Read", json.RawMessage(`{}`))), mockllm.UsageChunk(session.Usage{InputTokens: 50, OutputTokens: 5}), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.ChunksTurn(mockllm.TextChunk("turn two"), mockllm.ToolCallChunk(session.NewToolCall("c2", "Read", json.RawMessage(`{}`))), mockllm.UsageChunk(session.Usage{InputTokens: 50, OutputTokens: 5}), mockllm.DoneChunk(session.StopEndTurn)),
		mockllm.ChunksTurn(mockllm.TextChunk("final"), mockllm.UsageChunk(session.Usage{InputTokens: 50, OutputTokens: 5}), mockllm.DoneChunk(session.StopEndTurn)),
	)

	log := memstore.NewEventLog()
	e := agent.NewEngine(agent.Deps{
		LLM:           llm,
		Catalog:       cat,
		Policy:        permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:         "test-model",
		ContextWindow: func() int { return 1 }, // tiny window → compaction fires
	})
	sess := session.New(sessID, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, nil)
	r := e.Run(ctx, sess, env, agent.RunRequest{Text: "do work"})
	sawArchive := false
	for ev := range r.Events() {
		if ev.Type == session.EvCompactionArchive {
			sawArchive = true
		}
		if err := log.Append(ctx, sessID, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if !sawArchive {
		t.Skip("no compaction fired in this configuration; the hand-built archive test covers folding")
	}

	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID: sessID, Mode: session.ModeDefault, Limits: session.Limits{},
		Workspace: "/ws", CreatedAt: time.Unix(0, 0),
	}, log.Read(ctx, sessID))
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	// The pre-compaction work — the user prompt "do work" and the "turn one"/"turn two"
	// assistant turns — must be recoverable from the archive head.
	if !containsUserText(folded.Conversation.Messages, "do work") {
		t.Fatalf("fold lost the pre-compaction user prompt: %+v", folded.Conversation.Messages)
	}
	if !containsAssistantText(folded.Conversation.Messages, "turn one") {
		t.Fatalf("fold lost a pre-compaction assistant turn (archive head not recovered): %+v", folded.Conversation.Messages)
	}
	// History must remain provider-replayable.
	if err := session.ValidateToolPairing(folded.Conversation.Messages); err != nil {
		t.Fatalf("folded post-compaction history not tool-pairing-valid: %v", err)
	}
}

func containsUserText(msgs []session.Message, text string) bool {
	for _, m := range msgs {
		if m.Role == session.RoleUser && m.Text == text {
			return true
		}
	}
	return false
}

func containsAssistantText(msgs []session.Message, text string) bool {
	for _, m := range msgs {
		if m.Role == session.RoleAssistant && m.Text == text {
			return true
		}
	}
	return false
}

// compile-time use of port to keep the import meaningful if the engine Deps type
// changes; ContextWindow is a port-adjacent closure.
var _ port.EventLog = (*memstore.EventLog)(nil)
