package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// carryoverFactory builds a server.SessionEngineFactory that returns a fresh
// minimal engine over a canned mockllm reply for any selector, recording the
// selector it was handed. It is the per-session engine for a selector carryover
// session (the source and the seeded new session each rehydrate one).
func carryoverFactory(reply string, seen *atomic.Value) server.SessionEngineFactory {
	return func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		if seen != nil {
			seen.Store(sel)
		}
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn(reply), mockllm.TextTurn(reply)),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		return server.SessionEngineResult{Engine: eng, Close: func() error { return nil }}, nil
	}
}

// persistBlobsSource builds a completed source session on providerID whose
// conversation carries provider-private replay blobs: an assistant turn with
// non-empty Reasoning/ProviderPhase and a tool call carrying ItemID, followed
// by its tool result. It is persisted via the store so the blobs round-trip
// through sessnap (the same path memstore's Load uses). This is the realistic
// shape a reasoning provider leaves behind after a turn that called a tool —
// the exact history a model-switch carryover snapshots. The returned session is
// StateCompleted, so validateCarryover's loadAndReopen reopens it to idle.
func persistBlobsSource(t *testing.T, store *memstore.Store, id session.SessionID, providerID string) {
	t.Helper()
	persistBlobsSourceCalls(t, store, id, providerID, []session.ToolCall{{
		ID:     session.ToolCallID("c1"),
		Name:   "Read",
		Args:   json.RawMessage(`{"path":"f.go"}`),
		ItemID: "fc_item_1",
	}})
}

func persistBlobsSourceCalls(t *testing.T, store *memstore.Store, id session.SessionID, providerID string, calls []session.ToolCall) {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 10}, time.Unix(0, 0))
	sess.ProviderID = providerID
	if err := sess.RecordUserPrompt("do the thing", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := sess.RecordAssistant(session.Message{
		Role:            session.RoleAssistant,
		Text:            "reading f.go",
		Reasoning:       "ENCRYPTED-REASONING-BLOB",
		ReasoningItemID: "rs_src_item_1",
		ProviderPhase:   "commentary",
		ToolCalls:       calls,
	}); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	results := make([]session.ToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, session.NewToolResult(call.ID, "file contents"))
	}
	if err := sess.RecordToolResults(results); err != nil {
		t.Fatalf("RecordToolResults: %v", err)
	}
	if err := sess.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save source: %v", err)
	}
}

// TestOpenAICodexCarryoverReplayIDs pins the complete current Responses replay-ID
// classification. Same-provider Codex preserves its opaque state; every
// cross-provider Responses destination synthesizes stable collision-safe IDs;
// non-Responses destinations retain the ordinary stripped empty-ID form.
func TestOpenAICodexCarryoverReplayIDs(t *testing.T) {
	ctx := context.Background()
	svc, store := newMCPServiceStore(t, "reply", carryoverFactory("reply", nil))

	assert := func(t *testing.T, srcProvider, dstProvider string, sameProvider, synthesize bool) {
		t.Helper()
		srcID := session.SessionID("src-" + strings.ReplaceAll(srcProvider+"-"+dstProvider, "/", "-"))
		persistBlobsSourceCalls(t, store, srcID, srcProvider, []session.ToolCall{
			{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"f.go"}`), ItemID: "fc_item_1"},
			{ID: "c2", Name: "Grep", Args: json.RawMessage(`{"pattern":"needle"}`), ItemID: "fc_item_2"},
		})
		created, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: dstProvider, ModelID: "gpt-5"}, server.ProfileDefault, server.WithSourceSession(srcID))
		if err != nil {
			t.Fatalf("carryover %s -> %s: %v", srcProvider, dstProvider, err)
		}
		got, err := store.Load(ctx, created.ID)
		if err != nil {
			t.Fatalf("Load carryover: %v", err)
		}
		assistantCount, toolCallCount := 0, 0
		seenIDs := map[string]bool{}
		for _, message := range got.Conversation.Messages {
			if message.Role != session.RoleAssistant {
				continue
			}
			assistantCount++
			toolCallCount += len(message.ToolCalls)
			if sameProvider {
				if message.Reasoning != "ENCRYPTED-REASONING-BLOB" || message.ProviderPhase != "commentary" {
					t.Fatalf("same-provider Codex lost replay blobs: %+v", message)
				}
			} else if message.Reasoning != "" || message.ProviderPhase != "" {
				t.Fatalf("cross-provider carryover kept private replay blobs: %+v", message)
			}
			for i, call := range message.ToolCalls {
				switch {
				case sameProvider:
					want := fmt.Sprintf("fc_item_%d", i+1)
					if call.ItemID != want {
						t.Fatalf("same-provider item %d = %q, want %q", i, call.ItemID, want)
					}
				case synthesize:
					want := fmt.Sprintf("carryover_item_%d", i)
					if call.ItemID != want || strings.HasPrefix(call.ItemID, "fc_") {
						t.Fatalf("cross-provider synthetic item id = %q, want stable %q", call.ItemID, want)
					}
					if seenIDs[call.ItemID] {
						t.Fatalf("duplicate synthetic item id %q", call.ItemID)
					}
					seenIDs[call.ItemID] = true
				default:
					if call.ItemID != "" {
						t.Fatalf("non-Responses destination %q retained/synthesized item id %q", dstProvider, call.ItemID)
					}
				}
			}
		}
		if assistantCount != 1 || toolCallCount != 2 {
			t.Fatalf("assistant/tool-call count = %d/%d, want 1/2", assistantCount, toolCallCount)
		}
		if synthesize && len(seenIDs) != 2 {
			t.Fatalf("synthetic item id count = %d, want 2", len(seenIDs))
		}
	}

	t.Run("same Codex provider", func(t *testing.T) {
		assert(t, "openai-codex", "openai-codex", true, false)
	})
	for _, tc := range []struct {
		name, source, destination string
	}{
		{name: "OpenAI Responses", source: "anthropic", destination: "openai"},
		{name: "OpenRouter Responses", source: "anthropic", destination: "openrouter"},
		{name: "API OpenAI to Codex", source: "openai", destination: "openai-codex"},
		{name: "Codex to API OpenAI", source: "openai-codex", destination: "openai"},
		{name: "ToolHive Responses", source: "anthropic", destination: "toolhive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert(t, tc.source, tc.destination, false, true)
		})
	}
	for _, destination := range []string{"opencode", "anthropic", "mock"} {
		t.Run("non-Responses "+destination, func(t *testing.T) {
			assert(t, "openai", destination, false, false)
		})
	}
}

// TestCarryoverSeedsHistory: a source session on provider A with a
// provider-blob-carrying conversation, carried over onto a NEW session on the
// SAME provider, yields a NEW session whose seeded history equals the source's
// VERBATIM — Reasoning/ProviderPhase/ItemID intact (same-provider does NOT
// strip), text/roles/tool-call IDs/Args/results preserved, and a deep copy
// (mutating one does not alias the other). The seeded history is
// tool-pairing-valid. The seeded session then runs a turn to completion
// (history replays, no provider 400 from an orphaned tool result).
func TestCarryoverSeedsHistory(t *testing.T) {
	ctx := context.Background()

	const srcReply = "NEW-SESSION-REPLY"
	var seen atomic.Value
	svc, store := newMCPServiceStore(t, srcReply, carryoverFactory(srcReply, &seen))

	srcSel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "anthropic/claude-3.5-sonnet"}
	persistBlobsSource(t, store, "src-blobs", srcSel.ProviderID)

	// Reset the factory recorder so the next call is unambiguously the new
	// session's rehydration.
	seen.Store(server.ProviderSelector{})
	newSess, err := svc.CreateSessionWithProfile(ctx, session.ModeAccept, session.Limits{MaxTurns: 7}, srcSel, server.ProfileDefault, server.WithSourceSession("src-blobs"))
	if err != nil {
		t.Fatalf("CreateSessionWithProfile WithSourceSession: %v", err)
	}
	if newSess.ID == "" || newSess.ID == "src-blobs" {
		t.Fatalf("new session id = %q, want a new distinct id", newSess.ID)
	}

	srcSnap, err := store.Load(ctx, "src-blobs")
	if err != nil {
		t.Fatalf("Load src: %v", err)
	}
	newSnap, err := store.Load(ctx, newSess.ID)
	if err != nil {
		t.Fatalf("Load new: %v", err)
	}
	if got, want := len(newSnap.Conversation.Messages), len(srcSnap.Conversation.Messages); got != want {
		t.Fatalf("seeded history len = %d, want source's %d", got, want)
	}

	// SAME-provider: blobs carried VERBATIM (the pin this test owns — same-provider
	// must NOT strip). Walk the messages pairwise and assert each provider-private
	// blob survived the carryover byte-for-byte.
	for i, sm := range srcSnap.Conversation.Messages {
		nm := newSnap.Conversation.Messages[i]
		if nm.Role != sm.Role {
			t.Fatalf("msg %d: role = %q, want source's %q", i, nm.Role, sm.Role)
		}
		if nm.Text != sm.Text {
			t.Fatalf("msg %d: text = %q, want source's %q", i, nm.Text, sm.Text)
		}
		// Tool-result content survives the Store.Save→Load round-trip (the
		// pin Text alone misses — a tool message's Text is "" both sides).
		if sm.Role == session.RoleTool {
			if sm.ToolResult == nil {
				t.Fatalf("msg %d: source tool message has nil ToolResult", i)
			}
			if nm.ToolResult == nil {
				t.Fatalf("msg %d: tool message lost its ToolResult (carryover corrupted tool-result content)", i)
			}
			if nm.ToolResult.CallID != sm.ToolResult.CallID {
				t.Fatalf("msg %d: ToolResult.CallID = %q, want source's %q", i, nm.ToolResult.CallID, sm.ToolResult.CallID)
			}
			if nm.ToolResult.Content != sm.ToolResult.Content {
				t.Fatalf("msg %d: ToolResult.Content = %q, want source's %q", i, nm.ToolResult.Content, sm.ToolResult.Content)
			}
			if nm.ToolResult.IsError != sm.ToolResult.IsError {
				t.Fatalf("msg %d: ToolResult.IsError = %v, want source's %v", i, nm.ToolResult.IsError, sm.ToolResult.IsError)
			}
		}
		if nm.Reasoning != sm.Reasoning {
			t.Fatalf("msg %d: Reasoning = %q, want source's %q (same-provider must carry verbatim)", i, nm.Reasoning, sm.Reasoning)
		}
		if nm.ReasoningItemID != sm.ReasoningItemID {
			t.Fatalf("msg %d: ReasoningItemID = %q, want source's %q (same-provider must carry verbatim)", i, nm.ReasoningItemID, sm.ReasoningItemID)
		}
		if nm.ProviderPhase != sm.ProviderPhase {
			t.Fatalf("msg %d: ProviderPhase = %q, want source's %q (same-provider must carry verbatim)", i, nm.ProviderPhase, sm.ProviderPhase)
		}
		if len(nm.ToolCalls) != len(sm.ToolCalls) {
			t.Fatalf("msg %d: tool-call count = %d, want source's %d", i, len(nm.ToolCalls), len(sm.ToolCalls))
		}
		for j, sc := range sm.ToolCalls {
			nc := nm.ToolCalls[j]
			if nc.ID != sc.ID || nc.Name != sc.Name || string(nc.Args) != string(sc.Args) {
				t.Fatalf("msg %d call %d: tool-call identity/args drifted", i, j)
			}
			if nc.ItemID != sc.ItemID {
				t.Fatalf("msg %d call %d: ItemID = %q, want source's %q (same-provider must carry verbatim)", i, j, nc.ItemID, sc.ItemID)
			}
		}
	}
	// Specifically pin the assistant turn carried its blobs.
	var sawReasoning, sawReasoningItemID, sawPhase, sawItemID bool
	for _, m := range newSnap.Conversation.Messages {
		if m.Role == session.RoleAssistant && m.Reasoning == "ENCRYPTED-REASONING-BLOB" {
			sawReasoning = true
		}
		if m.Role == session.RoleAssistant && m.ReasoningItemID == "rs_src_item_1" {
			sawReasoningItemID = true
		}
		if m.Role == session.RoleAssistant && m.ProviderPhase == "commentary" {
			sawPhase = true
		}
		if m.Role == session.RoleAssistant && len(m.ToolCalls) > 0 && m.ToolCalls[0].ItemID == "fc_item_1" {
			sawItemID = true
		}
	}
	if !sawReasoning || !sawReasoningItemID || !sawPhase || !sawItemID {
		t.Fatalf("same-provider carryover lost a blob (reasoning=%v reasoningItemID=%v phase=%v itemID=%v)", sawReasoning, sawReasoningItemID, sawPhase, sawItemID)
	}

	// Seeded history is tool-pairing-valid (no orphaned tool result → no provider 400).
	if err := session.ValidateToolPairing(newSnap.Conversation.Messages); err != nil {
		t.Fatalf("seeded history not tool-pairing-valid: %v", err)
	}

	// Fresh aggregate: idle + zeroed counters/usage (carryover seeds history, not budget).
	if newSnap.State != session.StateIdle {
		t.Fatalf("new session state = %q, want idle", newSnap.State)
	}
	if newSnap.Counters != (session.Counters{}) {
		t.Fatalf("new session counters = %+v, want zeroed", newSnap.Counters)
	}

	// Deep copy: mutating the new session's loaded copy must not affect the source's.
	if len(newSnap.Conversation.Messages) > 0 && len(srcSnap.Conversation.Messages) > 0 {
		newFirst := &newSnap.Conversation.Messages[0]
		srcFirst := &srcSnap.Conversation.Messages[0]
		if newFirst == srcFirst {
			t.Fatalf("new session history aliases the source's backing array (not a deep copy)")
		}
	}
	newSnap.Conversation.Messages[0].Text = "MUTATED-NEW"
	srcAgain, err := store.Load(ctx, "src-blobs")
	if err != nil {
		t.Fatalf("re-Load src: %v", err)
	}
	if srcAgain.Conversation.Messages[0].Text == "MUTATED-NEW" {
		t.Fatalf("source history aliased the new session's (not a deep copy)")
	}

	// The factory saw the same selector (same-provider rehydration).
	if got := seen.Load().(server.ProviderSelector); got != srcSel {
		t.Fatalf("new session rehydration selector = %+v, want %+v", got, srcSel)
	}

	// The seeded session runs a turn to completion (history replays, no provider
	// 400 from an orphaned tool result — the ForkSnapshot+SeedHistory pairing).
	if got := driveCompletedTurn(t, svc, newSess.ID, "continue the plan"); got != srcReply {
		t.Fatalf("new session turn reply = %q, want %q", got, srcReply)
	}
}

// TestCarryoverCrossProviderStripsBlobs: a source on provider A whose
// conversation carries provider-private replay blobs (Reasoning/ProviderPhase
// and a tool call's ItemID), carried over onto a NEW session resolving to a
// DIFFERENT, non-OpenAI provider B, SUCCEEDS (no InvalidArgument) and the seeded
// history has those blobs CLEARED but text/roles/tool-call IDs/Args/tool results
// preserved. The seeded history is tool-pairing-valid. This pins the redesign:
// a model switch ALWAYS keeps the conversation; cross-provider seeds a
// provider-neutral copy via session.StripProviderState (both adapters omit
// empty blobs, so a stripped history replays safely to any provider). This test
// targets a NON-OpenAI destination so it exercises the strip-only path (no
// ItemID synthesis); the OpenAI-synthesis path has its own dedicated test.
func TestCarryoverCrossProviderStripsBlobs(t *testing.T) {
	ctx := context.Background()

	const newReply = "NEW-PROVIDER-REPLY"
	var seen atomic.Value
	svc, store := newMCPServiceStore(t, newReply, carryoverFactory(newReply, &seen))

	srcSel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "anthropic/claude-3.5-sonnet"}
	persistBlobsSource(t, store, "src-blobs-cross", srcSel.ProviderID)

	// Different provider -> carryover SUCCEEDS (strips the blobs).
	// Target a NON-OpenAI provider so the strip-only path (no ItemID synthesis)
	// is exercised — the OpenAI-synthesis path has its own dedicated test.
	newSel := server.ProviderSelector{ProviderID: "anthropic", ModelID: "claude-3-5-sonnet"}
	seen.Store(server.ProviderSelector{})
	newSess, err := svc.CreateSessionWithProfile(ctx, session.ModeAccept, session.Limits{MaxTurns: 7}, newSel, server.ProfileDefault, server.WithSourceSession("src-blobs-cross"))
	if err != nil {
		t.Fatalf("cross-provider carryover: err = %v, want nil (carryover is always allowed)", err)
	}
	if newSess.ID == "" || newSess.ID == "src-blobs-cross" {
		t.Fatalf("new session id = %q, want a new distinct id", newSess.ID)
	}

	// The new session rehydrated a per-session engine for the NEW provider.
	if got := seen.Load().(server.ProviderSelector); got != newSel {
		t.Fatalf("new session rehydration selector = %+v, want %+v", got, newSel)
	}

	srcSnap, err := store.Load(ctx, "src-blobs-cross")
	if err != nil {
		t.Fatalf("Load src: %v", err)
	}
	newSnap, err := store.Load(ctx, newSess.ID)
	if err != nil {
		t.Fatalf("Load new: %v", err)
	}
	if got, want := len(newSnap.Conversation.Messages), len(srcSnap.Conversation.Messages); got != want {
		t.Fatalf("seeded history len = %d, want source's %d", got, want)
	}

	// CROSS-provider: every provider-private blob is CLEARED, but the
	// provider-neutral fields (Role, Text, tool-call ID/Name/Args, tool results)
	// are preserved verbatim.
	for i, sm := range srcSnap.Conversation.Messages {
		nm := newSnap.Conversation.Messages[i]
		if nm.Role != sm.Role {
			t.Fatalf("msg %d: role = %q, want source's %q (stripping preserves roles)", i, nm.Role, sm.Role)
		}
		if nm.Text != sm.Text {
			t.Fatalf("msg %d: text = %q, want source's %q (stripping preserves text)", i, nm.Text, sm.Text)
		}
		// Tool-result content is provider-neutral, so it survives the strip
		// (the pin Text alone misses — a tool message's Text is "" both sides).
		// This catches a StripProviderState corruption of tool-result content.
		if sm.Role == session.RoleTool {
			if sm.ToolResult == nil {
				t.Fatalf("msg %d: source tool message has nil ToolResult", i)
			}
			if nm.ToolResult == nil {
				t.Fatalf("msg %d: tool message lost its ToolResult (strip corrupted tool-result content)", i)
			}
			if nm.ToolResult.CallID != sm.ToolResult.CallID {
				t.Fatalf("msg %d: ToolResult.CallID = %q, want source's %q (stripping preserves tool results)", i, nm.ToolResult.CallID, sm.ToolResult.CallID)
			}
			if nm.ToolResult.Content != sm.ToolResult.Content {
				t.Fatalf("msg %d: ToolResult.Content = %q, want source's %q (stripping preserves tool results)", i, nm.ToolResult.Content, sm.ToolResult.Content)
			}
			if nm.ToolResult.IsError != sm.ToolResult.IsError {
				t.Fatalf("msg %d: ToolResult.IsError = %v, want source's %v (stripping preserves tool results)", i, nm.ToolResult.IsError, sm.ToolResult.IsError)
			}
		}
		if nm.Reasoning != "" {
			t.Fatalf("msg %d: Reasoning = %q, want cleared (cross-provider must strip)", i, nm.Reasoning)
		}
		if nm.ReasoningItemID != "" {
			t.Fatalf("msg %d: ReasoningItemID = %q, want cleared (cross-provider must strip)", i, nm.ReasoningItemID)
		}
		if nm.ProviderPhase != "" {
			t.Fatalf("msg %d: ProviderPhase = %q, want cleared (cross-provider must strip)", i, nm.ProviderPhase)
		}
		if len(nm.ToolCalls) != len(sm.ToolCalls) {
			t.Fatalf("msg %d: tool-call count = %d, want source's %d", i, len(nm.ToolCalls), len(sm.ToolCalls))
		}
		for j, sc := range sm.ToolCalls {
			nc := nm.ToolCalls[j]
			if nc.ID != sc.ID || nc.Name != sc.Name || string(nc.Args) != string(sc.Args) {
				t.Fatalf("msg %d call %d: tool-call identity/args drifted (stripping preserves ID/Name/Args)", i, j)
			}
			if nc.ItemID != "" {
				t.Fatalf("msg %d call %d: ItemID = %q, want cleared (cross-provider must strip)", i, j, nc.ItemID)
			}
		}
	}

	// The assistant turn specifically: the blobs the source carried are gone, but
	// the text and the tool call (ID/Name/Args) survived.
	var sawAssistantText, sawToolCall, sawReasoning, sawReasoningItemID, sawPhase, sawItemID bool
	for _, m := range newSnap.Conversation.Messages {
		if m.Role == session.RoleAssistant {
			if m.Text == "reading f.go" {
				sawAssistantText = true
			}
			if len(m.ToolCalls) > 0 && m.ToolCalls[0].ID == "c1" {
				sawToolCall = true
			}
			if m.Reasoning != "" {
				sawReasoning = true
			}
			if m.ReasoningItemID != "" {
				sawReasoningItemID = true
			}
			if m.ProviderPhase != "" {
				sawPhase = true
			}
			for _, c := range m.ToolCalls {
				if c.ItemID != "" {
					sawItemID = true
				}
			}
		}
	}
	if !sawAssistantText || !sawToolCall {
		t.Fatalf("stripped history lost provider-neutral content (assistantText=%v toolCall=%v)", sawAssistantText, sawToolCall)
	}
	if sawReasoning || sawReasoningItemID || sawPhase || sawItemID {
		t.Fatalf("stripped history kept a provider-private blob (reasoning=%v reasoningItemID=%v phase=%v itemID=%v)", sawReasoning, sawReasoningItemID, sawPhase, sawItemID)
	}

	// Seeded history is tool-pairing-valid (the strip preserves pairing —
	// ForkSnapshot already stripped trailing orphans, and StripProviderState only
	// clears blobs, never calls/results).
	if err := session.ValidateToolPairing(newSnap.Conversation.Messages); err != nil {
		t.Fatalf("seeded history not tool-pairing-valid after strip: %v", err)
	}

	// Fresh aggregate: idle + zeroed counters/usage.
	if newSnap.State != session.StateIdle {
		t.Fatalf("new session state = %q, want idle", newSnap.State)
	}
	if newSnap.Counters != (session.Counters{}) {
		t.Fatalf("new session counters = %+v, want zeroed", newSnap.Counters)
	}

	// The seeded session runs a turn to completion on the new provider's engine
	// (the stripped, provider-neutral history replays cleanly — no 400).
	if got := driveCompletedTurn(t, svc, newSess.ID, "continue"); got != newReply {
		t.Fatalf("new session turn reply = %q, want %q", got, newReply)
	}
}

// TestCarryoverCrossProviderToOpenAISynthesizesItemIDs: a source on a
// non-OpenAI provider with blob-carrying tool calls, carried over onto a NEW
// session resolving to OpenAI. Every carried ToolCall now has a NON-empty,
// unique ItemID with the "carryover_item_" prefix (NOT "fc_" — cannot collide
// with the provider's auto-assigned sequential ids). The IDs are deterministic:
// carrying the SAME source twice yields the SAME ItemIDs. Text/roles/tool-call
// IDs/Args/tool results are preserved, and the seeded history is
// tool-pairing-valid. This pins the fix for the cross-provider replay
// correctness finding: on the second post-carryover turn, the provider
// auto-assigns sequential fc_N that collide with the current response's items
// → "Duplicate item found with id fc_N" HTTP 400 (observed on Azure GPT-5.x).
func TestCarryoverCrossProviderToOpenAISynthesizesItemIDs(t *testing.T) {
	ctx := context.Background()

	const newReply = "OPENAI-REPLY"
	var seen atomic.Value
	svc, store := newMCPServiceStore(t, newReply, carryoverFactory(newReply, &seen))

	srcSel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "anthropic/claude-3.5-sonnet"}
	persistBlobsSource(t, store, "src-blobs-openai", srcSel.ProviderID)

	newSel := server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-4o"}
	seen.Store(server.ProviderSelector{})
	newSess, err := svc.CreateSessionWithProfile(ctx, session.ModeAccept, session.Limits{MaxTurns: 7}, newSel, server.ProfileDefault, server.WithSourceSession("src-blobs-openai"))
	if err != nil {
		t.Fatalf("cross-provider carryover to openai: err = %v, want nil", err)
	}
	if newSess.ID == "" || newSess.ID == "src-blobs-openai" {
		t.Fatalf("new session id = %q, want a new distinct id", newSess.ID)
	}

	// The new session rehydrated a per-session engine for the NEW provider.
	if got := seen.Load().(server.ProviderSelector); got != newSel {
		t.Fatalf("new session rehydration selector = %+v, want %+v", got, newSel)
	}

	srcSnap, err := store.Load(ctx, "src-blobs-openai")
	if err != nil {
		t.Fatalf("Load src: %v", err)
	}
	newSnap, err := store.Load(ctx, newSess.ID)
	if err != nil {
		t.Fatalf("Load new: %v", err)
	}
	if got, want := len(newSnap.Conversation.Messages), len(srcSnap.Conversation.Messages); got != want {
		t.Fatalf("seeded history len = %d, want source's %d", got, want)
	}

	// Content preserved: roles, text, tool-call identity, tool results.
	for i, sm := range srcSnap.Conversation.Messages {
		nm := newSnap.Conversation.Messages[i]
		if nm.Role != sm.Role {
			t.Fatalf("msg %d: role = %q, want source's %q", i, nm.Role, sm.Role)
		}
		if nm.Text != sm.Text {
			t.Fatalf("msg %d: text = %q, want source's %q", i, nm.Text, sm.Text)
		}
		if sm.Role == session.RoleTool {
			if sm.ToolResult == nil || nm.ToolResult == nil {
				t.Fatalf("msg %d: tool message lost its ToolResult (src=%v new=%v)", i, sm.ToolResult, nm.ToolResult)
			}
			if nm.ToolResult.CallID != sm.ToolResult.CallID || nm.ToolResult.Content != sm.ToolResult.Content || nm.ToolResult.IsError != sm.ToolResult.IsError {
				t.Fatalf("msg %d: ToolResult drifted", i)
			}
		}
		// Reasoning/ProviderPhase/ReasoningItemID are STRIPPED (cross-provider).
		if nm.Reasoning != "" {
			t.Fatalf("msg %d: Reasoning = %q, want cleared", i, nm.Reasoning)
		}
		if nm.ReasoningItemID != "" {
			t.Fatalf("msg %d: ReasoningItemID = %q, want cleared", i, nm.ReasoningItemID)
		}
		if nm.ProviderPhase != "" {
			t.Fatalf("msg %d: ProviderPhase = %q, want cleared", i, nm.ProviderPhase)
		}
		if len(nm.ToolCalls) != len(sm.ToolCalls) {
			t.Fatalf("msg %d: tool-call count = %d, want source's %d", i, len(nm.ToolCalls), len(sm.ToolCalls))
		}
		for j, sc := range sm.ToolCalls {
			nc := nm.ToolCalls[j]
			if nc.ID != sc.ID || nc.Name != sc.Name || string(nc.Args) != string(sc.Args) {
				t.Fatalf("msg %d call %d: tool-call identity/args drifted", i, j)
			}
			// ITEM-ID SYNTHESISED: non-empty, unique, carryover_ prefix, NOT fc_.
			if nc.ItemID == "" {
				t.Fatalf("msg %d call %d: ItemID is empty — want a synthesised id (cross-provider to openai must synthesise)", i, j)
			}
			if !strings.HasPrefix(nc.ItemID, "carryover_item_") {
				t.Fatalf("msg %d call %d: ItemID = %q, want carryover_item_ prefix", i, j, nc.ItemID)
			}
			if strings.HasPrefix(nc.ItemID, "fc_") {
				t.Fatalf("msg %d call %d: ItemID = %q, must NOT use fc_ prefix (collision risk)", i, j, nc.ItemID)
			}
		}
	}

	// Uniqueness: every ItemID across the entire history is distinct.
	seenIDs := make(map[string]int) // id → msg index
	for i, m := range newSnap.Conversation.Messages {
		for _, c := range m.ToolCalls {
			if prevIdx, dup := seenIDs[c.ItemID]; dup {
				t.Fatalf("msg %d call %s: duplicate ItemID %q (first seen at msg %d)", i, c.ID, c.ItemID, prevIdx)
			}
			seenIDs[c.ItemID] = i
		}
	}
	if len(seenIDs) == 0 {
		t.Fatalf("no tool calls to synthesise ItemIDs for — source fixture missing tool calls")
	}

	// Seeded history is tool-pairing-valid.
	if err := session.ValidateToolPairing(newSnap.Conversation.Messages); err != nil {
		t.Fatalf("seeded history not tool-pairing-valid: %v", err)
	}

	// DETERMINISM: carrying the SAME source again yields the SAME ItemIDs.
	seen.Store(server.ProviderSelector{})
	sess2, err := svc.CreateSessionWithProfile(ctx, session.ModeAccept, session.Limits{MaxTurns: 7}, newSel, server.ProfileDefault, server.WithSourceSession("src-blobs-openai"))
	if err != nil {
		t.Fatalf("second cross-provider carryover to openai: err = %v, want nil", err)
	}
	snap2, err := store.Load(ctx, sess2.ID)
	if err != nil {
		t.Fatalf("Load second session: %v", err)
	}
	for i, m := range newSnap.Conversation.Messages {
		m2 := snap2.Conversation.Messages[i]
		for j, c := range m.ToolCalls {
			c2 := m2.ToolCalls[j]
			if c.ItemID != c2.ItemID {
				t.Fatalf("msg %d call %d: ItemID = %q, second carryover = %q — NOT deterministic", i, j, c.ItemID, c2.ItemID)
			}
		}
	}

	// The seeded session runs a turn to completion (history replays with
	// synthesised ItemIDs — no 400 from duplicate fc_N).
	if got := driveCompletedTurn(t, svc, newSess.ID, "continue"); got != newReply {
		t.Fatalf("new session turn reply = %q, want %q", got, newReply)
	}
}

// TestCarryoverBothDefaultProvider: source and new session both resolving to
// the server DEFAULT provider (empty selector, shared engine) succeed — the
// carryover is same-provider by the empty==default canonicalisation. The source
// here is a bare TextTurn (no provider-private blobs); the same-provider blob
// carry-over pin (Reasoning/ProviderPhase/ItemID survive verbatim) lives in
// TestCarryoverSeedsHistory over the blob-carrying persistBlobsSource helper —
// this test owns the shared-engine + empty-selector rehydration path, not the
// blob-preservation axis.
func TestCarryoverBothDefaultProvider(t *testing.T) {
	ctx := context.Background()

	// Build the service directly so the SHARED engine carries TWO text turns
	// (the source's and the seeded new session's): newMCPService wires a
	// single-turn mockllm.
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("DEFAULT-REPLY"), mockllm.TextTurn("DEFAULT-REPLY")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine:        shared,
		Store:         store,
		Workspaces:    func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits: session.Limits{MaxTurns: 10, MaxToolCalls: 20},
		Now:           func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	src, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	driveCompletedTurn(t, svc, src.ID, "hello")
	srcSnap, _ := store.Load(ctx, src.ID)
	if len(srcSnap.Conversation.Messages) == 0 {
		t.Fatalf("source has no history to carry over")
	}

	newSess, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSourceSession(src.ID))
	if err != nil {
		t.Fatalf("CreateSessionWithProfile WithSourceSession (both default): %v", err)
	}
	if newSess.ID == "" || newSess.ID == src.ID {
		t.Fatalf("new session id = %q, want a new distinct id", newSess.ID)
	}
	if svc.HasSessionEngineForTest(newSess.ID) {
		t.Fatalf("default carryover registered a per-session engine; it MUST ride the shared engine")
	}
	newSnap, _ := store.Load(ctx, newSess.ID)
	if got, want := len(newSnap.Conversation.Messages), len(srcSnap.Conversation.Messages); got != want {
		t.Fatalf("seeded history len = %d, want source's %d", got, want)
	}
	// The seeded session runs on the shared engine and replays its history.
	if got := driveCompletedTurn(t, svc, newSess.ID, "again"); got != "DEFAULT-REPLY" {
		t.Fatalf("new session turn reply = %q, want the shared engine's reply", got)
	}
}

// TestCarryoverRejectsRunningOrAwaitingSource: a source left in StateRunning is
// rejected with ErrFailedPrecondition (carryover requires a turn boundary).
func TestCarryoverRejectsRunningOrAwaitingSource(t *testing.T) {
	ctx := context.Background()
	svc, store := newMCPServiceStore(t, "shared", nil)

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Drive the persisted snapshot into StateRunning directly (the genuine
	// mid-run state loadAndReopen reads from the store).
	loaded, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := loaded.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := store.Save(ctx, loaded); err != nil {
		t.Fatalf("Save running: %v", err)
	}

	_, err = svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSourceSession(sess.ID))
	if !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("carryover on a running source: err = %v, want ErrFailedPrecondition", err)
	}
}

// TestCarryoverRejectsAwaitingSource: a source left in StateAwaiting (parked on
// a permission ask mid-turn) is rejected with ErrFailedPrecondition — the SAME
// turn-boundary rule as the running case, since a snapshot of an awaiting
// conversation carries a dangling unanswered tool call. The awaiting snapshot is
// persisted directly (the genuine parked-at-ask state loadAndReopen reads from
// the store): record the user prompt, BeginTurn, RecordAssistant with an
// unanswered tool call, PauseForApproval, Save.
func TestCarryoverRejectsAwaitingSource(t *testing.T) {
	ctx := context.Background()
	svc, store := newMCPServiceStore(t, "shared", nil)

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	loaded, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := loaded.RecordUserPrompt("do the thing", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := loaded.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	call := session.NewToolCall("ask-c1", "Write", json.RawMessage(`{"path":"f.go"}`))
	if err := loaded.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := loaded.PauseForApproval(session.PendingAsk{AskID: "a1", Tool: "Write", Call: call.ID}); err != nil {
		t.Fatalf("PauseForApproval: %v", err)
	}
	if loaded.State != session.StateAwaiting {
		t.Fatalf("source state = %q, want awaiting", loaded.State)
	}
	if err := store.Save(ctx, loaded); err != nil {
		t.Fatalf("Save awaiting: %v", err)
	}

	_, err = svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSourceSession(sess.ID))
	if !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("carryover on an awaiting source: err = %v, want ErrFailedPrecondition", err)
	}
}

// TestCarryoverMissingSource: a nonexistent source id surfaces the error from
// loadAndReopen (ErrNotFound).
func TestCarryoverMissingSource(t *testing.T) {
	ctx := context.Background()
	svc, _ := newMCPServiceStore(t, "shared", nil)

	_, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSourceSession("no-such-session"))
	if !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("carryover on a missing source: err = %v, want ErrNotFound", err)
	}
}
