package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/app"
)

// legacyIdentityFixture removes only fields added after the historical persisted
// shape. It edits a test-owned, stopped server's snapshot and committed log;
// no production migration or event-log backfill is enabled by this fixture.
func legacyIdentityFixture(t *testing.T, storeDir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(storeDir, "sid-v1"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot, events string
	for _, entry := range entries {
		switch {
		case strings.HasSuffix(entry.Name(), ".session.json"):
			snapshot = filepath.Join(storeDir, "sid-v1", entry.Name())
		case strings.HasSuffix(entry.Name(), ".events.jsonl"):
			events = filepath.Join(storeDir, "sid-v1", entry.Name())
		}
	}
	if snapshot == "" || events == "" {
		t.Fatalf("missing test-owned snapshot/log: %v", entries)
	}
	strip := func(data []byte) []byte {
		t.Helper()
		var value any
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			t.Fatal(err)
		}
		removed := 0
		var walk func(any, string)
		walk = func(value any, parent string) {
			switch value := value.(type) {
			case map[string]any:
				for key, child := range value {
					lower := strings.ToLower(key)
					if lower == "invocationid" || lower == "invocation_id" || lower == "hookid" || lower == "hook_id" ||
						(lower == "sessionid" || lower == "session_id") && (parent == "ask" || parent == "pending" || parent == "hook") ||
						lower == "revision" && parent == "hook" || lower == "hook_revision" {
						delete(value, key)
						removed++
						continue
					}
					walk(child, lower)
				}
			case []any:
				for _, child := range value {
					walk(child, parent)
				}
			}
		}
		walk(value, "")
		if removed == 0 {
			t.Fatal("fixture did not remove any additive identity fields")
		}
		result, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	original, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot, strip(original), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err = os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(original, []byte("\n")), []byte("\n"))
	changed := 0
	for i, line := range lines {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatal(err)
		}
		if raw := envelope["ev"]; len(raw) != 0 {
			// Only records with additive identity fields need changing.
			if bytes.Contains(raw, []byte("invocation_id")) || bytes.Contains(raw, []byte("session_id")) {
				envelope["ev"] = strip(raw)
				changed++
			}
		}
		lines[i], err = json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
	}
	if changed == 0 {
		t.Fatal("fixture did not strip a committed old call/ask event")
	}
	if err := os.WriteFile(events, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestLegacySnapshotLogPendingContinuationAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	workspace, storeDir := t.TempDir(), t.TempDir()
	settings := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(settings, []byte("permissions:\n  ask: [Write]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("provider-pair", "Write", []byte(`{"path":"approved.txt","content":"continued"}`))), mockllm.TextTurn("continued"))
	cfg := app.Config{Workspace: workspace, StoreDir: storeDir, Model: "mock-model", MockProvider: provider, Shell: "/bin/sh", Compaction: "heuristic", Tokenizer: "heuristic", PermissionConfigs: []string{settings}}
	target, _, stop := startPendingRecoveryServer(t, cfg)
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	wire := mecatlv1.NewHarnessServiceClient(conn)
	created, err := wire.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := wire.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: created.GetSessionId(), Text: "write"}}}); err != nil {
		t.Fatal(err)
	}
	var askID string
	for askID == "" {
		response, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if response.GetEvent().GetType() == "permission.ask" {
			askID = response.GetEvent().GetAsk().GetAskId()
		}
	}
	probe, err := client.Dial(client.DialConfig{Server: target})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := probe.DiscoverPendingApproval(ctx, created.GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	watch, err := probe.WatchPendingApprovalRun(ctx, pending)
	if err != nil {
		t.Fatal(err)
	}
	if boundary, err := watch.Recv(); err != nil || boundary.Kind != client.PendingApprovalEventBoundary {
		t.Fatalf("initial durability boundary: %+v %v", boundary, err)
	}
	watch.Close()
	_ = probe.Close()
	stop()
	_ = conn.Close()

	snapshot := legacyIdentityFixture(t, storeDir)
	oldBytes, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	var migratedID session.InvocationID
	for i := 0; i < 2; i++ {
		loaded, err := store.Load(ctx, session.SessionID(created.GetSessionId()))
		if err != nil {
			t.Fatal(err)
		}
		ask, ok := loaded.PendingAsk()
		if !ok || !strings.HasPrefix(string(ask.InvocationID), "legacy_tc_") || ask.InvocationID != loaded.Conversation.Messages[len(loaded.Conversation.Messages)-1].ToolCalls[0].InvocationID {
			t.Fatalf("snapshot migration %d: %+v", i, ask)
		}
		if i != 0 && migratedID != ask.InvocationID {
			t.Fatal("unchanged legacy snapshot migrated inconsistently")
		}
		migratedID = ask.InvocationID
	}
	if now, err := os.ReadFile(snapshot); err != nil || !bytes.Equal(now, oldBytes) {
		t.Fatalf("migration saved the snapshot: %v", err)
	}
	target, _, stop = startPendingRecoveryServer(t, cfg)
	defer stop()
	cl, err := client.Dial(client.DialConfig{Server: target})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cl.Close() }()
	selection, err := resolveStartupResume(ctx, cl, created.GetSessionId(), false)
	if err != nil {
		t.Fatalf("resume legacy fixture: %v", err)
	}
	if selection.Pending == nil || selection.Pending.AskID != askID || selection.Pending.OriginSessionID != created.GetSessionId() || selection.Pending.ToolCallID != string(migratedID) {
		t.Fatalf("snapshot provenance did not reach client: %+v", selection.Pending)
	}
	var snapshotID string
	for _, message := range selection.Transcript.Messages {
		for _, call := range message.ToolCalls {
			if call.Name == "Write" {
				snapshotID = call.ID
			}
		}
	}
	if snapshotID != string(migratedID) {
		t.Fatalf("authoritative transcript did not adopt snapshot occurrence: %q vs %q", snapshotID, migratedID)
	}
	seed, err := store.Load(ctx, session.SessionID(created.GetSessionId()))
	if err != nil {
		t.Fatal(err)
	}
	folded, err := eventsource.Fold(eventsource.SessionMeta{ID: seed.ID, Mode: seed.Mode, Limits: seed.Limits, EnvironmentRef: seed.EnvironmentRef, CreatedAt: seed.CreatedAt, Kind: seed.Kind, Relationship: seed.Relationship, Incarnation: seed.Incarnation(), Owner: seed.Owner}, store.Read(ctx, seed.ID))
	if err != nil {
		t.Fatalf("old event-log replay: %v", err)
	}
	var logID session.InvocationID
	for _, message := range folded.Conversation.Messages {
		for _, call := range message.ToolCalls {
			if call.Name == "Write" {
				logID = call.InvocationID
			}
		}
	}
	if !strings.HasPrefix(string(logID), "legacy_event_tc_") || logID == migratedID {
		t.Fatalf("old log and pending snapshot were merged: log=%q pending=%+v", logID, selection.Pending)
	}
	if now, err := os.ReadFile(snapshot); err != nil || !bytes.Equal(now, oldBytes) {
		t.Fatalf("startup/discovery saved migration-only metadata: %v", err)
	}
	oldAskFound := false
	for event, readErr := range store.Read(ctx, seed.ID) {
		if readErr != nil {
			t.Fatal(readErr)
		}
		if event.Type == session.EvPermissionAsk && event.Ask != nil && event.Ask.AskID == askID {
			oldAskFound = true
			if event.Ask.SessionID != "" || event.Ask.InvocationID != "" {
				t.Fatal("watch backfilled the immutable old event log")
			}
		}
	}
	if !oldAskFound {
		t.Fatal("old ask event missing after discovery")
	}
	model := ui.New(ui.Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: ctx, Resume: selection, NoAltScreen: true, NoBanner: true})
	mm, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = mm.(ui.Model)
	if !strings.Contains(model.View().Content, "Write") {
		t.Fatal("UI did not adopt the pending occurrence")
	}
	continued, err := cl.WatchPendingApprovalRun(ctx, *selection.Pending)
	if err != nil {
		t.Fatal(err)
	}
	defer continued.Close()
	if boundary, err := continued.Recv(); err != nil || boundary.Kind != client.PendingApprovalEventBoundary {
		t.Fatalf("continuation boundary: %+v %v", boundary, err)
	}
	if err := cl.ResolvePendingApproval(ctx, *selection.Pending, client.VerdictAllowOnce); err != nil {
		t.Fatal(err)
	}
	var result client.ToolResultMsg
	for {
		event, err := continued.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if toolResult, ok := event.Message.(client.ToolResultMsg); ok {
			result = toolResult
		}
		if event.Kind == client.PendingApprovalEventTerminal {
			break
		}
	}
	if result.CallID != string(migratedID) {
		t.Fatalf("continuation result attached to old log: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(workspace, "approved.txt")); err != nil {
		t.Fatalf("approved continuation did not execute: %v", err)
	}
	continued.Close()
	_ = cl.Close()
	stop()

	// Extend the stopped test store with review evidence: the old-format hook has
	// no public identity, while the continuation review/receipt carries the exact
	// migrated snapshot occurrence. These records traverse the real log, server
	// mapper, client decoder and UI; no production event is rewritten or backfilled.
	hookID := "review_" + string(migratedID) + "_action"
	id := session.SessionID(created.GetSessionId())
	for _, event := range []session.Event{
		{Type: session.EvHook, Text: "old hook evidence", Hook: &session.HookPayload{Phase: "PreToolUse", Tool: "Write", CallID: "provider-pair", Guardrail: &session.GuardrailReviewPayload{ReviewID: "old-review", Job: "action"}}},
		{Type: session.EvHook, Text: "continuation hook evidence", Hook: &session.HookPayload{HookID: hookID, Revision: 1, SessionID: id, InvocationID: migratedID, CallID: "provider-pair", Phase: "PostToolUse", Tool: "Write", Guardrail: &session.GuardrailReviewPayload{ReviewID: hookID, Job: "action", Disposition: "execute"}}},
		{Type: session.EvApproval, Approval: &session.ApprovalPayload{AskID: askID, Tool: "Write", Call: "provider-pair", SessionID: "child", InvocationID: migratedID, ReviewID: hookID, Verdict: session.VerdictStringDeny}},
		{Type: session.EvApproval, Approval: &session.ApprovalPayload{AskID: askID, Tool: "Write", Call: "provider-pair", SessionID: id, InvocationID: migratedID, ReviewID: hookID, Verdict: session.VerdictStringAllowOnce}},
	} {
		if err := store.Append(ctx, id, event); err != nil {
			t.Fatal(err)
		}
	}
	target, _, stop = startPendingRecoveryServer(t, cfg)
	defer stop()
	replayConn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replayConn.Close() }()
	replayed, err := mecatlv1.NewHarnessServiceClient(replayConn).StreamSessionEvents(ctx, &mecatlv1.StreamSessionEventsRequest{SessionId: created.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	var legacyCall, continuedHook, oldHook, rightReceipt, wrongReceipt bool
	for {
		event, err := replayed.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch event.GetType() {
		case "tool.call":
			legacyCall = legacyCall || event.GetToolCall().GetId() == ""
		case "hook":
			oldHook = oldHook || event.GetHook().GetHookId() == "" && event.GetText() == "old hook evidence"
			continuedHook = continuedHook || event.GetHook().GetHookId() == hookID && event.GetHook().GetCallId() == string(migratedID) && event.GetHook().GetOriginSessionId() == created.GetSessionId()
		case "approval":
			if event.GetApproval().GetReviewId() == hookID {
				wrongReceipt = wrongReceipt || event.GetApproval().GetOriginSessionId() == "child"
				rightReceipt = rightReceipt || event.GetApproval().GetOriginSessionId() == created.GetSessionId() && event.GetApproval().GetCallId() == string(migratedID)
			}
		}
		if msg := client.EventToMsg(event); msg != nil {
			mm, _ := model.Update(msg)
			model = mm.(ui.Model)
		}
	}
	if !legacyCall || !oldHook || !continuedHook || !rightReceipt || !wrongReceipt {
		t.Fatalf("real old/new log projection missing: oldCall=%v oldHook=%v continuedHook=%v rightReceipt=%v wrongReceipt=%v", legacyCall, oldHook, continuedHook, rightReceipt, wrongReceipt)
	}
	frame := model.View().Content
	if strings.Count(frame, "Guardrail: Write") < 2 || !strings.Contains(frame, "Write · approved.txt") || !strings.Contains(frame, "running · Write") {
		t.Fatal("UI replay hid evidence or attached the explicit continuation result to the old ID-less log card")
	}
}
