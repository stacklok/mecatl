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
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// readTool is a trivial read-only tool the scripted run calls.
type readTool struct{}

func (readTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Read", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (readTool) ReadOnly() bool { return true }
func (readTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(in.ID, "file contents"), nil
}

// TestFoldEqualsSnapshotLoad is the DETERMINISM / replay-equivalence gate. It drives
// a real engine over mockllm (text → tool call → text — NO reasoning-blob
// dependence), capturing every emitted event into a memstore EventLog exactly as the
// server relay does, and also persisting a snapshot. It then reconstructs the session
// two ways — Fold over the event log, and SessionStore.Load over the snapshot — and
// asserts they are deep-equal on the MUST fields.
//
// CONTRACT BOUNDARY: this equivalence holds because mockllm exercises NO opaque replay
// fields (Message.Reasoning / Message.ProviderPhase / ToolCall.ItemID are all empty),
// which is exactly the provider class for which the fold is byte-identical-replay
// faithful. A reasoning provider would diverge on those snapshot-only fields — the
// documented limitation (see the package doc + ADR 0038). The CORE proof here is the
// Conversation deep-equal: buildRequest sends the Conversation verbatim, so two
// deep-equal conversations replay byte-identically given a stable System/Tools/Model.
// (We assert the conversation + counters + usage + state directly rather than a
// captured LLMRequest because the engine-level request observer plumbing would add
// significant harness weight for the same guarantee the conversation deep-equal already
// gives on this provider class.)
func TestFoldEqualsSnapshotLoad(t *testing.T) {
	const sessID session.SessionID = "det-1"

	cat := tool.NewCatalog()
	cat.MustRegister(readTool{})

	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("let me look"),
			mockllm.ToolCallChunk(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`))),
			mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 2}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("all done"),
			mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 3}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)

	store := memstore.New()
	log := memstore.NewEventLog()
	e := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
	})

	sess := session.New(sessID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, ws, nil)
	r := e.Run(ctx, sess, env, agent.RunRequest{Text: "look at a.go"})

	// Mimic the relay: append EVERY observed event to the durable log in order.
	for ev := range r.Events() {
		if err := log.Append(ctx, sessID, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// Persist the snapshot AFTER the run terminates (the run drove sess to terminal).
	if err := store.Save(ctx, sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reconstruct both ways.
	snap, err := store.Load(ctx, sessID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID:             sessID,
		Mode:           session.ModeDefault,
		Limits:         session.Limits{},
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
		CreatedAt:      time.Unix(0, 0),
	}, log.Read(ctx, sessID))
	if err != nil {
		t.Fatalf("fold: %v", err)
	}

	// MUST round-trip: the FULL Conversation (every field mockllm exercises), INCLUDING
	// the user prompt — now event-carried via EvUserPrompt, so the fold reconstructs it
	// (no user-strip carve-out). This is the provider class with no opaque replay fields
	// (Reasoning/ProviderPhase/ItemID all empty), so a full deep-equal here is the
	// byte-identical-replay proof: buildRequest sends the Conversation verbatim, so two
	// deep-equal conversations replay byte-identically given a stable System/Tools/Model.
	if !reflect.DeepEqual(folded.Conversation.Messages, snap.Conversation.Messages) {
		t.Fatalf("conversation mismatch:\n folded=%+v\n  snap=%+v", folded.Conversation.Messages, snap.Conversation.Messages)
	}
	// MUST round-trip: cumulative Usage (folded sums per-run EvResult; snap persists it).
	if folded.Usage != snap.Usage {
		t.Fatalf("usage mismatch: folded=%+v snap=%+v", folded.Usage, snap.Usage)
	}
	// MUST round-trip: terminal State + recorded stop reason.
	if folded.State != snap.State {
		t.Fatalf("state mismatch: folded=%q snap=%q", folded.State, snap.State)
	}
	fStop, _ := folded.RecordedStopReason()
	sStop, _ := snap.RecordedStopReason()
	if fStop != sStop {
		t.Fatalf("stop mismatch: folded=%q snap=%q", fStop, sStop)
	}
	// MUST round-trip: Counters (latest run segment).
	if folded.Counters != snap.Counters {
		t.Fatalf("counters mismatch: folded=%+v snap=%+v", folded.Counters, snap.Counters)
	}
	// Pending must agree (both nil here — a completed run).
	_, fAsk := folded.PendingAsk()
	_, sAsk := snap.PendingAsk()
	if fAsk != sAsk {
		t.Fatalf("pending mismatch: folded=%v snap=%v", fAsk, sAsk)
	}

	// Sanity: the FULL conversation is user prompt + 3 model/tool turns, and the fold
	// now reconstructs all 4 (the user prompt is event-carried via EvUserPrompt), so the
	// deep-equal above is meaningful and complete.
	if len(snap.Conversation.Messages) != 4 || len(folded.Conversation.Messages) != 4 {
		t.Fatalf("expected snap=4 and folded=4 (1 user + 3 model/tool) messages, got snap=%d folded=%d",
			len(snap.Conversation.Messages), len(folded.Conversation.Messages))
	}
	if folded.Conversation.Messages[0].Role != session.RoleUser || folded.Conversation.Messages[0].Text != "look at a.go" {
		t.Fatalf("folded conversation must lead with the reconstructed user prompt, got %+v", folded.Conversation.Messages[0])
	}
}
