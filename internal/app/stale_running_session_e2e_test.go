package app

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestBuildSettlesStaleRunningSnapshotAcrossRestart is the issue #475
// falsifiable end-to-end gate through the FULL composition (app.Build →
// server.Service), offline, over two SEPARATE Build calls — mirroring
// TestApproveAfterRestartE2E's two-Build shape. It proves the actual reported
// bug is closed for the population it was confirmed against: a
// subagent-*-prefixed child session whose crash left a trailing tool_use with
// no matching result and a persisted "state":"running" snapshot that nobody
// ever prompts or resumes again.
//
//  1. built1 ("the crashed process"): seed a StateRunning snapshot carrying a
//     dangling tool_use DIRECTLY into the durable jsonlstore — bypassing
//     CreateSession/StartRunContent entirely, since a normal prompt flow
//     through Step 3's funnel repair would mask the population this test
//     targets (a child id nothing ever re-opens). Age the snapshot file's
//     mtime past staleSessionWindow, then close built1 without ever starting
//     a run for this id — genuinely untouched, dying mid-crash.
//  2. built2: a brand-new Build over the SAME store dir (the restart). Build's
//     OWN composition wiring (startStaleSessionReconcile, called from Build —
//     NOT this test) starts a goroutine that runs an immediate startup sweep
//     pass. This test never calls sweepStaleSessions itself: it polls
//     ListSessions until that real goroutine settles the id, or times out —
//     so a deleted/broken build.go wiring line fails this test, not just the
//     sweep's own unit tests. Then it asserts: ListSessions reports the id
//     settled (no longer "running"), and the reloaded conversation passes
//     ValidateToolPairing (no dangling tool_use) — with NO prompt, resume, or
//     manual repair call in between.
//
// Mutation-verified: reverting issue #475 Steps 2-4 (SessionStale/
// SettleIfStale/the composition sweep) to a pre-fix worktree makes this test
// fail to COMPILE (sweepStaleSessions/SessionStale/SettleIfStale/
// LeaseSweepDisabled do not exist yet at that point in the branch's history)
// — see the commit message for the exact commit and verification transcript.
// Separately, deleting the ONE composition line that wires
// startStaleSessionReconcile into Build (so the mechanism exists but never
// runs in production) leaves the mechanism's own direct-call unit tests
// green but must make THIS test time out — that's the gap this test closes.
func TestBuildSettlesStaleRunningSnapshotAcrossRestart(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	memoryDir := t.TempDir()
	workspace := t.TempDir()

	baseCfg := func() Config {
		return Config{
			Workspace:   workspace,
			NoSoul:      true,
			StoreDir:    storeDir,
			MemoryDir:   memoryDir,
			envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
			providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
				return mockllm.New(mockllm.TextTurn("unused"))
			},
			liveModelHTTPClient: offlineHTTPClient(),
		}
	}

	const childID session.SessionID = "subagent-crash-orphan-475"

	// built1: the crashed process. Never start a run for childID — seed the
	// orphaned snapshot straight into the store, exactly as if built1's own
	// (never-invoked) store.Save had been the crash's last write.
	cfg1 := baseCfg()
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}

	orphan := crashOrphanedSessionFixture(t, childID, time.Now())
	seedStore, err := jsonlstore.New(storeDir)
	if err != nil {
		built1.Close()
		t.Fatalf("open store for direct seed: %v", err)
	}
	if err := seedStore.Save(ctx, orphan); err != nil {
		built1.Close()
		t.Fatalf("seed crash-orphaned snapshot: %v", err)
	}

	// Age the snapshot's logical modification time past staleSessionWindow.
	// The helper understands both historical v1 and current v2 storage.
	seededPath := jsonlSnapshotPath(t, storeDir, childID)
	old := time.Now().Add(-2 * time.Hour)
	setJSONLSnapshotMtime(t, seededPath, old)

	built1.Close() // process death: childID was never touched by any run.

	// built2: a brand-new Build over the SAME store — the restart.
	cfg2 := baseCfg()
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	// Do NOT call sweepStaleSessions directly. Build #2's own composition
	// wiring (startStaleSessionReconcile, called from Build) already started
	// a goroutine that runs an immediate startup sweep pass — poll for that
	// REAL goroutine to settle the id instead, so a deleted/broken wiring
	// line in build.go (the mechanism exists but Build never starts it) makes
	// this test time out rather than staying silently green.
	const pollTimeout = 5 * time.Second
	const pollInterval = 20 * time.Millisecond
	var gotState string
	deadline := time.Now().Add(pollTimeout)
	for {
		rows, err := built2.Service.ListSessions(ctx)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		found := false
		for _, row := range rows {
			if row.SessionID == string(childID) {
				found = true
				gotState = row.State
				break
			}
		}
		if !found {
			t.Fatalf("ListSessions never reported %q", childID)
		}
		if gotState != string(session.StateRunning) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for Build's real startup sweep goroutine to settle %q (still %q) — is startStaleSessionReconcile still wired into Build?", pollTimeout, childID, gotState)
		}
		time.Sleep(pollInterval)
	}
	if gotState != string(session.StateIdle) {
		t.Fatalf("ListSessions state for %q = %q, want %q", childID, gotState, session.StateIdle)
	}

	reloaded, err := built2.Service.GetSession(ctx, childID)
	if err != nil {
		t.Fatalf("GetSession after settle: %v", err)
	}
	if err := session.ValidateToolPairing(reloaded.Conversation.Messages); err != nil {
		t.Fatalf("tool pairing invalid after settle: %v", err)
	}
}
