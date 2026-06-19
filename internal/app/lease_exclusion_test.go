package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// leaseBaseCfg returns a Config wired for a SHARED jsonl store + a SHARED flock
// session lease, so two Builds in one process model two replicas over one store
// (each Build gets a DISTINCT lease owner via the per-Build nonce, so neither can
// renew/release the other's hold). The TTL is generous so an actively-renewing
// holder keeps its lease for the whole test (the release-takeover leg); the
// TTL-EXPIRY takeover leg has its own config (see TestCrossProcessLeaseExpiryTakeover).
func leaseBaseCfg(t *testing.T, storeDir, leaseDir, workspace, memoryDir string) Config {
	t.Helper()
	return Config{
		Workspace:           workspace,
		NoSoul:              true,
		StoreDir:            storeDir,
		MemoryDir:           memoryDir,
		SessionLeaseDir:     leaseDir,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
	}
}

// TestCrossProcessLeaseExclusion is the cloud-native Phase 4 falsifiable gate: a
// session leased by one Build (replica) cannot be run by a second Build over the
// SAME store + lease dir, until the first releases (EndSession) or its lease
// lapses (TTL).
//
// Mutation-verified: remove the acquireLease call in StartRunContent → Build #2's
// StartRun succeeds while #1 holds → the exclusion assertion fails.
func TestCrossProcessLeaseExclusion(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	leaseDir := t.TempDir()
	workspace := t.TempDir()
	memoryDir := t.TempDir()

	// Build #1 keeps a long-running turn parked so it HOLDS the lease while Build #2
	// tries to enter: the model issues a Write that asks (parks awaiting) under
	// ModeDefault.
	cfg1 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.ToolCallTurn(
			session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"note.txt","content":"replica one"}`)),
		))
	}
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	defer built1.Close()

	sess, err := built1.Service.CreateSession(ctx, workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "write the note")
	if err != nil {
		t.Fatalf("StartRun #1: %v", err)
	}
	// Range to the ask so the run is parked AWAITING — built1 now holds the lease.
	var askID string
	for ev := range run1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askID = ev.Ask.AskID
			built1.Service.Persist(ctx, sess.ID)
			break
		}
	}
	if askID == "" {
		t.Fatal("run #1 never parked at a permission ask")
	}

	// Build #2 over the SAME store + lease dir = a second replica. Its StartRun must
	// be refused while #1 holds the lease.
	cfg2 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("replica two"))
	}
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	_, err = built2.Service.StartRun(ctx, sess.ID, "second writer")
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun #2 while #1 holds the lease = %v, want ErrSessionLeasedElsewhere", err)
	}

	// Release on built1 (EndSession) frees the lease; built2 may now take over.
	if err := built1.Service.EndSession(ctx, sess.ID); err != nil {
		t.Fatalf("EndSession #1: %v", err)
	}
	run2, err := built2.Service.StartRun(ctx, sess.ID, "second writer after release")
	if err != nil {
		t.Fatalf("StartRun #2 after #1 released = %v, want success", err)
	}
	drain(run2)
	built2.Service.FinishRun(sess.ID, run2)
}

// TestCrossProcessLeaseExpiryTakeover is the TTL-EXPIRY takeover leg (the honest
// closure for the S5 finding): a holder whose lease LAPSES (it stopped renewing —
// a stalled/slow holder, the in-process analogue of a crash the flock could not
// observe) is taken over by a second replica once the TTL passes, WITHOUT an
// explicit release.
//
// Build #1 is wired with a short TTL (1s) and a renew interval LONGER than the TTL
// (10s), so its renewer never refreshes before the lease lapses — modelling a
// holder that stopped renewing. Build #2 is refused immediately, then succeeds
// after the lease expires (a bounded real-clock poll; the conformance suite covers
// the fake-clock expiry contract, so this only needs to confirm the composition
// wiring honours expiry).
func TestCrossProcessLeaseExpiryTakeover(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	leaseDir := t.TempDir()
	workspace := t.TempDir()
	memoryDir := t.TempDir()

	cfg1 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg1.SessionLeaseTTL = 1 * time.Second            // lease lapses 1s after acquire...
	cfg1.SessionLeaseRenewInterval = 10 * time.Second // ...and the renewer never fires first.
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.ToolCallTurn(
			session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"note.txt","content":"replica one"}`)),
		))
	}
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	defer built1.Close()

	sess, err := built1.Service.CreateSession(ctx, workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "write the note")
	if err != nil {
		t.Fatalf("StartRun #1: %v", err)
	}
	var askID string
	for ev := range run1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askID = ev.Ask.AskID
			built1.Service.Persist(ctx, sess.ID)
			break
		}
	}
	if askID == "" {
		t.Fatal("run #1 never parked at a permission ask")
	}

	cfg2 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg2.SessionLeaseTTL = 1 * time.Second
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("took over after ttl"))
	}
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	// Immediately, built2 is refused — #1's lease is still live (just acquired).
	if _, err := built2.Service.StartRun(ctx, sess.ID, "too soon"); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun #2 before TTL = %v, want ErrSessionLeasedElsewhere", err)
	}

	// After the TTL lapses (and #1 never renewed), built2 takes over. Bounded poll.
	var run2 *agent.Run
	deadline := time.After(10 * time.Second)
	for {
		run2, err = built2.Service.StartRun(ctx, sess.ID, "take over after ttl")
		if err == nil {
			break
		}
		if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
			t.Fatalf("StartRun #2 during TTL poll = %v, want ErrSessionLeasedElsewhere or success", err)
		}
		select {
		case <-deadline:
			t.Fatal("built2 never took over after the lease TTL lapsed (expiry not honoured)")
		case <-time.After(100 * time.Millisecond):
		}
	}
	drain(run2)
	built2.Service.FinishRun(sess.ID, run2)
}

// TestCrossProcessDoubleExecutionPreventedByLease is the twin of
// resume_awaiting_test.go's TestConcurrentApproveAfterRestartExecutesOnce, but
// for the CROSS-process case: two replicas (two Builds over one store + lease)
// must not both execute a parked tool. Replica #1 parks a Write awaiting; replica
// #2 tries to approve-resume it but is refused by the lease while #1 holds it, so
// the Write executes AT MOST ONCE (here: not at all on #2 until #1 yields).
//
// Mutation-verified: remove the acquireLease call in resumeFromAwaiting → #2's
// ApproveRun rehydrates and executes the Write while #1 still holds the session →
// the double-execution guard fails.
func TestCrossProcessDoubleExecutionPreventedByLease(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	leaseDir := t.TempDir()
	workspace := t.TempDir()
	memoryDir := t.TempDir()
	target := filepath.Join(workspace, "note.txt")

	cfg1 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.ToolCallTurn(
			session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"note.txt","content":"exactly once"}`)),
		))
	}
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	defer built1.Close()
	sess, err := built1.Service.CreateSession(ctx, workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "write the note")
	if err != nil {
		t.Fatalf("StartRun #1: %v", err)
	}
	var askID string
	for ev := range run1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askID = ev.Ask.AskID
			built1.Service.Persist(ctx, sess.ID)
			break
		}
	}
	if askID == "" {
		t.Fatal("run #1 never parked at a permission ask")
	}

	// Replica #2: while #1 STILL holds the session (the lease is live), an
	// ApproveRun that would resume-from-awaiting must be refused — so the parked
	// Write does NOT execute on #2.
	cfg2 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("done"))
	}
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	_, err = built2.Service.ApproveRun(ctx, sess.ID, askID, session.VerdictAllowOnce)
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("ApproveRun on #2 while #1 holds the lease = %v, want ErrSessionLeasedElsewhere", err)
	}
	// The Write must NOT have executed on #2 (it has no lease, so it never resumed).
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("the parked Write executed on the leased-out replica — double execution not prevented")
	}
}

// TestCompositionByteIdenticalWithoutLease confirms the default path: a Build with
// NO lease flag wires a nil SessionLease, so a StartRun acquires nothing and the
// run proceeds exactly as before Phase 4.
func TestCompositionByteIdenticalWithoutLease(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	memoryDir := t.TempDir()

	cfg := Config{
		Workspace:           workspace,
		NoSoul:              true,
		StoreDir:            storeDir,
		MemoryDir:           memoryDir,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
	}
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("no lease, no problem"))
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// No lease flag → buildSessionLease returns nil → the run proceeds normally.
	sess, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun without a lease = %v, want success (byte-identical default)", err)
	}
	drain(run)
	built.Service.FinishRun(sess.ID, run)
	final, err := built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("run without a lease final state = %q, want completed", final.State)
	}
}

// drain consumes a run's events to completion.
func drain(run *agent.Run) {
	for range run.Events() {
	}
}
