package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestApproveAfterRestartE2E is the cloud-native Phase 2 falsifiable gate through
// the FULL composition (app.Build → server.Service), offline. It mirrors
// TestSelectorSessionSurvivesRestartE2E's two-Build shape:
//
//  1. built1 over a shared StoreDir; the model returns a Write tool call gated as
//     Ask (Write under ModeDefault asks). Start a run, range to EvPermissionAsk,
//     capture the AskID, and model the relay's Persist-on-ask (Service.Persist) so
//     a durable StateAwaiting snapshot is saved. DO NOT approve.
//  2. built1.Close() = process death: the parked run + askRegistry die; the durable
//     awaiting snapshot is the last write to the store.
//  3. built2 over the SAME store; built2.Service.ApproveRun(sess, askID,
//     VerdictAllowOnce) → LookupRun miss → resumeFromAwaiting → rehydrate →
//     ResumeApproval → the pending Write executes.
//  4. Relay the returned run's events: assert EXACTLY ONE non-error EvToolResult for
//     the pending call id, a clean StopEndTurn terminal, and a completed session.
//
// Mutation-verified (each leg has a documented failing mutation):
//   - revert resumeFromAwaiting to noActiveRun → step 3 returns ErrNoActiveRun, the
//     ApproveRun assertion fails.
//   - skip the sibling close-out (the multi-tool engine unit test) → dangling
//     tool_use → the resumed run fails instead of completing.
//   - re-dispatch / double-record the pending call → two EvToolResult for the call
//     id (and the Write filesystem effect twice) → the exactly-once assertion fails.
func TestApproveAfterRestartE2E(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	memoryDir := t.TempDir()
	workspace := t.TempDir()
	target := filepath.Join(workspace, "note.txt")

	baseCfg := func() Config {
		return Config{
			Workspace:           workspace,
			NoSoul:              true,
			StoreDir:            storeDir,
			MemoryDir:           memoryDir,
			envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
			liveModelHTTPClient: offlineHTTPClient(),
		}
	}

	// built1: the model issues a single Write tool call. The default FS catalog has a
	// real Write tool that ASKS under ModeDefault, so the run parks awaiting.
	cfg1 := baseCfg()
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.ToolCallTurn(
			session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"note.txt","content":"survived the restart"}`)),
		))
	}
	built1, err := buildIsolated(t, ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "write the note")
	if err != nil {
		built1.Close()
		t.Fatalf("StartRun (pre-restart): %v", err)
	}
	var askID string
	for ev := range run1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			// Model the relay's Persist-on-ask: a durable StateAwaiting snapshot.
			built1.Service.Persist(ctx, sess.ID)
			break // stop ranging; DO NOT approve. The parked run dies with built1.
		}
	}
	if askID == "" {
		built1.Close()
		t.Fatal("the pre-restart run never raised a permission ask for Write")
	}
	// The target must NOT exist yet — the Write is parked, not executed.
	if _, statErr := os.Stat(target); statErr == nil {
		built1.Close()
		t.Fatal("the Write executed before approval — it must be parked at the ask")
	}
	built1.Close() // process death: parked run + askRegistry die; awaiting snapshot persists.

	// built2: a brand-new Build over the SAME store. The model has the continuation
	// turn (the Write tool call already happened pre-restart).
	cfg2 := baseCfg()
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("done after approval"))
	}
	built2, err := buildIsolated(t, ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	run2, err := built2.Service.ApproveRun(ctx, sess.ID, askID, session.VerdictAllowOnce, "")
	if err != nil {
		t.Fatalf("ApproveRun after restart: %v (want a rehydrated resume, not ErrNoActiveRun)", err)
	}
	if run2 == nil {
		t.Fatal("ApproveRun after restart returned a nil run — the resume-from-awaiting path did not fire")
	}

	var stop session.StopReason
	var nonErrorResults, totalResults int
	for ev := range run2.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "w1" {
			totalResults++
			if !ev.ToolResult.IsError {
				nonErrorResults++
			}
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	built2.Service.FinishRun(sess.ID, run2)

	if nonErrorResults != 1 || totalResults != 1 {
		t.Fatalf("EvToolResult for w1: non-error=%d total=%d, want exactly 1 non-error / 1 total (exactly-once)", nonErrorResults, totalResults)
	}
	if stop != session.StopEndTurn {
		t.Fatalf("resumed run stop = %q, want %q", stop, session.StopEndTurn)
	}
	// The filesystem side effect happened EXACTLY ONCE.
	data, statErr := os.ReadFile(target)
	if statErr != nil {
		t.Fatalf("the approved Write did not create the target file: %v", statErr)
	}
	if string(data) != "survived the restart" {
		t.Fatalf("target content = %q, want %q", string(data), "survived the restart")
	}
	final, err := built2.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession after resume: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("resumed session final state = %q, want completed", final.State)
	}
}
