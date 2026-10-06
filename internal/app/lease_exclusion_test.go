package app

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"sync"
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
// renew/release the other's hold). The default TTL is generous so an actively-renewing
// holder keeps its lease for the whole test.
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

type firstThenBlockingProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *firstThenBlockingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		return func(yield func(port.Chunk, error) bool) {
			if yield(port.Chunk{Kind: port.ChunkText, Text: "round complete"}, nil) {
				yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
			}
		}, nil
	}
	return func(yield func(port.Chunk, error) bool) {
		<-ctx.Done()
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func (*firstThenBlockingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func TestADR_0294_AppAndMecak8sLeaseCompositionSharesMutationCapability(t *testing.T) {
	ctx := context.Background()
	storeDir, leaseDir, workspace, memoryDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg1 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("owner"))
	}
	owner, err := buildIsolated(t, ctx, cfg1)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	sess, err := owner.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := owner.Service.StartRun(ctx, sess.ID, "hold ownership")
	if err != nil {
		t.Fatal(err)
	}
	drain(run)
	owner.Service.FinishRun(sess.ID, run)

	cfg2 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("competitor"))
	}
	competitor, err := buildIsolated(t, ctx, cfg2)
	if err != nil {
		t.Fatal(err)
	}
	defer competitor.Close()
	if _, err := competitor.Service.SetMode(ctx, sess.ID, session.ModePlan); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("competing Build SetMode = %v, want ErrSessionLeasedElsewhere", err)
	}
	got, err := competitor.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != session.ModeDefault {
		t.Fatalf("competing Build mutated mode to %q", got.Mode)
	}
}

// TestCrossProcessLeaseExclusion is the cloud-native Phase 4 falsifiable gate: a
// session leased by one Build (replica) cannot be run by a second Build over the
// SAME store + lease dir, until the first settles its local run and releases
// ownership (EndSession) or its lease lapses (TTL).
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
	built1, err := buildIsolated(t, ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	defer built1.Close()

	sess, err := built1.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
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
	built2, err := buildIsolated(t, ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	_, err = built2.Service.StartRun(ctx, sess.ID, "second writer")
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun #2 while #1 holds the lease = %v, want ErrSessionLeasedElsewhere", err)
	}

	// Close is not cancel: settle and join built1's local awaiting run before
	// EndSession releases ownership. A close while the run is parked must retain
	// the lease and resources (ADR 0291).
	if err := built1.Service.Approve(ctx, sess.ID, askID, session.VerdictDeny); err != nil {
		t.Fatalf("deny run #1: %v", err)
	}
	drain(run1)
	built1.Service.FinishRun(sess.ID, run1)
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

// TestCrossProcessLeaseExpiryTakeover verifies the generic SessionLease contract:
// after a lease expires, another replica takes over even if the prior holder is
// still live. The successor must receive a strictly greater fencing token and
// successfully drive the recovered session.
func TestCrossProcessLeaseExpiryTakeover(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	leaseDir := t.TempDir()
	workspace := t.TempDir()
	memoryDir := t.TempDir()

	cfg1 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg1.SessionLeaseTTL = 30 * time.Second
	cfg1.SessionLeaseRenewInterval = 10 * time.Second
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("replica one"))
	}
	built1, err := buildIsolated(t, ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	defer built1.Close()

	cfg2 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg2.SessionLeaseTTL = 30 * time.Second
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("replica two"))
	}
	built2, err := buildIsolated(t, ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	sess, err := built1.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "first run")
	if err != nil {
		t.Fatalf("StartRun #1: %v", err)
	}
	drain(run1)
	built1.Service.FinishRun(sess.ID, run1)
	firstToken := leaseToken(t, leaseDir)

	if _, err := built2.Service.StartRun(ctx, sess.ID, "take over before expiry"); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("StartRun #2 before expiry = %v, want ErrSessionLeasedElsewhere", err)
	}
	expireLeaseRecord(t, leaseDir)

	var run2 *agent.Run
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for run2 == nil {
		select {
		case <-deadline:
			t.Fatal("StartRun #2 never took over after lease expiry")
		case <-ticker.C:
			run2, err = built2.Service.StartRun(ctx, sess.ID, "take over after expiry")
			if err != nil && !errors.Is(err, server.ErrSessionLeasedElsewhere) {
				t.Fatalf("StartRun #2 while waiting for expiry: %v", err)
			}
		}
	}
	drain(run2)
	built2.Service.FinishRun(sess.ID, run2)
	final, err := built2.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession after successor run: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("successor run state = %q, want completed", final.State)
	}
	if successorToken := leaseToken(t, leaseDir); successorToken <= firstToken {
		t.Fatalf("successor token = %d, want > expired token %d", successorToken, firstToken)
	}
}

func expireLeaseRecord(t *testing.T, leaseDir string) {
	t.Helper()
	entries, err := os.ReadDir(leaseDir)
	if err != nil {
		t.Fatalf("ReadDir lease directory: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(leaseDir, entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile lease record: %v", err)
		}
		var record struct {
			Owner  string    `json:"owner"`
			Token  uint64    `json:"token"`
			Expiry time.Time `json:"expiry"`
		}
		if err := json.Unmarshal(body, &record); err != nil {
			t.Fatalf("decode lease record: %v", err)
		}
		record.Expiry = time.Unix(0, 0)
		body, err = json.Marshal(record)
		if err != nil {
			t.Fatalf("encode expired lease record: %v", err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("write expired lease record: %v", err)
		}
		return
	}
	t.Fatal("lease record not found")
}

func leaseToken(t *testing.T, leaseDir string) uint64 {
	t.Helper()
	entries, err := os.ReadDir(leaseDir)
	if err != nil {
		t.Fatalf("ReadDir lease directory: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(leaseDir, entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile lease record: %v", err)
		}
		var record struct {
			Token uint64 `json:"token"`
		}
		if err := json.Unmarshal(body, &record); err != nil {
			t.Fatalf("decode lease record: %v", err)
		}
		return record.Token
	}
	t.Fatal("lease record not found")
	return 0
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
	built1, err := buildIsolated(t, ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	defer built1.Close()
	sess, err := built1.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
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
	built2, err := buildIsolated(t, ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	_, err = built2.Service.ApproveRun(ctx, sess.ID, askID, session.VerdictAllowOnce, "")
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("ApproveRun on #2 while #1 holds the lease = %v, want ErrSessionLeasedElsewhere", err)
	}
	// The Write must NOT have executed on #2 (it has no lease, so it never resumed).
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("the parked Write executed on the leased-out replica — double execution not prevented")
	}
}

// TestBuildChildLeaseBlocksRemoteRetention proves that engine-owned child
// lifecycles join the same distributed lease domain as manual cleanup and GC.
// Two Builds share one JSONL store and flock lease directory; while replica A's
// direct-team member is running, replica B's retention deletion is refused.
func TestBuildChildLeaseBlocksRemoteRetention(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	leaseDir := t.TempDir()
	workspace := t.TempDir()
	memoryDir := t.TempDir()

	cfg1 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg1.EnableTeams = true
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return &firstThenBlockingProvider{}
	}
	built1, err := buildIsolated(t, ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	defer built1.Close()

	cfg2 := leaseBaseCfg(t, storeDir, leaseDir, workspace, memoryDir)
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("idle"))
	}
	built2, err := buildIsolated(t, ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	teamID, _, err := built1.Service.CreateTeamOnDefaultPlacement(ctx, "lease-test", "hold", 0,
		[]agent.MemberSpec{{Name: "lead", Lead: true, InitialPrompt: "wait"}})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	memberID := agent.MemberSessionID(teamID, "lead")
	runCtx, cancelRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() {
		_, runErr := built1.Service.RunTeam(runCtx, teamID, nil)
		runDone <- runErr
	}()
	joined := false
	defer func() {
		cancelRun()
		if joined {
			return
		}
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
		}
	}()

	deadline := time.After(5 * time.Second)
	var lastErr error
	for {
		_, loadErr := built2.Service.GetSession(ctx, memberID)
		lastErr = loadErr
		if loadErr == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("team member never reached the shared store (err=%v)", lastErr)
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case err := <-runDone:
		t.Fatalf("RunTeam ended before remote cleanup check: %v", err)
	default:
	}

	if err := built2.Service.DeleteSessionForRetention(ctx, memberID); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		cancelRun()
		t.Fatalf("remote retention delete = %v, want ErrSessionLeasedElsewhere", err)
	}
	cancelRun()
	select {
	case err := <-runDone:
		joined = true
		if err != nil {
			t.Fatalf("RunTeam after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunTeam did not stop after cancellation")
	}
}

// TestCompositionAutoWiresLocalStoreLease confirms the safe local default: a Build
// with StoreDir and no explicit lease flag auto-wires the shared flock lease and
// an uncontended run proceeds normally.
func TestCompositionAutoWiresLocalStoreLease(t *testing.T) {
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
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// The automatically wired local lease is uncontended, so the run proceeds.
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun with automatic local lease = %v, want success", err)
	}
	drain(run)
	built.Service.FinishRun(sess.ID, run)
	final, err := built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("run with automatic local lease final state = %q, want completed", final.State)
	}
}

// drain consumes a run's events to completion.
func drain(run *agent.Run) {
	for range run.Events() {
	}
}
