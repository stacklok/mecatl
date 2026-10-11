package server

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type recoveryHook struct{}

func (recoveryHook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhaseSessionStart || ev.Phase == governance.PhaseStop {
		return governance.HookOutcome{Block: true, Message: "blocked"}, nil
	}
	return governance.HookOutcome{}, nil
}

func TestRetryFailedRunAdmitsCommittedHookTailBeforeExecution(t *testing.T) {
	ctx := t.Context()
	store := memstore.New()
	log := &readCountingHookLog{}
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("recovered")), Catalog: tool.NewCatalog(), Hooks: recoveryHook{}, Model: "mock"})
	svc, err := NewService(Config{Engine: engine, Store: store, EventLog: log, Diagnostics: port.NopDiagnostics{}, PlacementProvider: lifecyclePlacementProvider{}, PlacementScope: "test", SharedEngineRoot: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New("retry-hook-tail", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordFailureMetadata(session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressPrecommit}); err != nil {
		t.Fatal(err)
	}
	sess.RestoreHookRevision(3)
	if err := store.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(ctx, sess.ID, session.Event{Type: session.EvHook, Hook: &session.HookPayload{HookID: "hook_prior", SessionID: sess.ID, Revision: 8}}); err != nil {
		t.Fatal(err)
	}
	svc.cfg.EventLog = &failingHookLog{}
	if run, err := svc.RetryFailedRun(ctx, sess.ID); run != nil || !errors.Is(err, ErrInternal) || err.Error() != ErrInternal.Error() {
		t.Fatalf("failed recovery launched retry or leaked storage failure: %v %v", run, err)
	}
	unchanged, err := store.Load(ctx, sess.ID)
	if err != nil || unchanged.State != session.StateFailed || unchanged.HookRevision() != 3 || unchanged.FailureMetadata().Disposition != session.RetryDispositionRetryable {
		t.Fatalf("failed admission mutated retry: %+v %v", unchanged, err)
	}
	svc.cfg.EventLog = log
	run, err := svc.RetryFailedRun(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewRunEventRecorder(ctx, svc, sess.ID)
	var firstHook *session.HookPayload
	var text string
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvHook && firstHook == nil {
			firstHook = ev.Hook
		}
		if ev.Type == session.EvMessageDelta {
			text += ev.Text
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
		recorder.Observe(ev)
	}
	recorder.Close()
	svc.FinishRun(sess.ID, run)
	if text != "recovered" || stop != session.StopEndTurn {
		t.Fatalf("retry model output = %q, stop = %s", text, stop)
	}
	if firstHook == nil || firstHook.Revision <= 8 || firstHook.SessionID != sess.ID || log.reads != 1 {
		t.Fatalf("retry hook = %+v, admission scans = %d", firstHook, log.reads)
	}
	found := false
	for _, ev := range log.recorded {
		if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.HookID == firstHook.HookID && ev.Hook.Revision == firstHook.Revision {
			found = true
		}
	}
	if !found {
		t.Fatal("retry hook not retained")
	}
}

func TestResumedRunHookStreamAndCommittedTail(t *testing.T) {
	ctx := t.Context()
	store := memstore.New()
	log := &readCountingHookLog{}
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Hooks: recoveryHook{}, Model: "mock"})
	svc, err := NewService(Config{Engine: engine, Store: store, EventLog: log, Diagnostics: port.NopDiagnostics{}, PlacementProvider: lifecyclePlacementProvider{}, PlacementScope: "test", SharedEngineRoot: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New("hook-recovery-run", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	sess.RestoreHookRevision(3)
	if err := store.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	// A failed append consumes a revision without committing it; a post-write
	// failure leaves a committed revision despite reporting an error.
	log.failCalls = map[int]bool{1: true}
	log.postWriteFailCalls = map[int]bool{2: true}
	for _, revision := range []uint64{5, 8} {
		if err := log.Append(ctx, sess.ID, session.Event{Type: session.EvHook, Hook: &session.HookPayload{HookID: "hook_old", SessionID: sess.ID, Revision: revision}}); err == nil {
			t.Fatal("expected injected append failure")
		}
	}
	run, err := svc.StartRunContent(ctx, sess.ID, "go", nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewRunEventRecorder(ctx, svc, sess.ID)
	var published session.Event
	for ev := range run.Events() {
		if ev.Type == session.EvHook && published.Hook == nil {
			published = ev
		}
		recorder.Observe(ev)
	}
	recorder.Close()
	svc.FinishRun(sess.ID, run)
	if published.Hook == nil || published.Hook.Revision != 9 || published.Hook.SessionID != sess.ID {
		t.Fatalf("first resumed stream hook = %+v", published.Hook)
	}
	if log.reads != 1 {
		t.Fatalf("run admission read log %d times", log.reads)
	}
	found := false
	for _, ev := range log.recorded {
		if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.HookID == published.Hook.HookID && ev.Hook.Revision == 9 {
			found = true
		}
	}
	if !found {
		t.Fatal("streamed hook was not retained in the event log")
	}
}

type cancelledRecoveryHook struct {
	entered chan struct{}
	release chan struct{}
	first   atomic.Bool
}

func (h *cancelledRecoveryHook) Run(ctx context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhaseSessionStart && h.first.CompareAndSwap(false, true) {
		close(h.entered)
		<-h.release
	}
	return recoveryHook{}.Run(ctx, ev)
}

func TestCancelledHookPublicationRecoversRetainedHighWater(t *testing.T) {
	ctx := t.Context()
	store := memstore.New()
	log := &readCountingHookLog{}
	hooks := &cancelledRecoveryHook{entered: make(chan struct{}), release: make(chan struct{})}
	svc, err := NewService(Config{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Hooks: hooks, Model: "mock"}), Store: store, EventLog: log, Diagnostics: port.NopDiagnostics{}, PlacementProvider: lifecyclePlacementProvider{}, PlacementScope: "test", SharedEngineRoot: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New("cancel-recovery", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := store.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	first, err := svc.StartRunContent(ctx, sess.ID, "go", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-hooks.entered
	first.Cancel()
	close(hooks.release)
	record := func(run *agent.Run) uint64 {
		recorder := NewRunEventRecorder(ctx, svc, sess.ID)
		var firstHook uint64
		for ev := range run.Events() {
			if ev.Type == session.EvHook && firstHook == 0 {
				firstHook = ev.Hook.Revision
			}
			recorder.Observe(ev)
		}
		recorder.Close()
		svc.FinishRun(sess.ID, run)
		return firstHook
	}
	cancelledRevision := record(first)
	if cancelledRevision == 0 {
		t.Fatal("cancelled publication emitted no hook")
	}
	second, err := svc.StartRunContent(ctx, sess.ID, "again", nil)
	if err != nil {
		t.Fatal(err)
	}
	resumedRevision := record(second)
	if resumedRevision <= cancelledRevision || log.reads != 2 {
		t.Fatalf("cancelled revision %d resumed revision %d admission scans %d", cancelledRevision, resumedRevision, log.reads)
	}
}

type failFirstPublishedHookLog struct {
	readCountingHookLog
	postWrite bool
	failed    bool
}

func (l *failFirstPublishedHookLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	if ev.Type == session.EvHook && !l.failed {
		l.failed = true
		call := len(l.attempts) + 1
		if l.postWrite {
			if l.postWriteFailCalls == nil {
				l.postWriteFailCalls = make(map[int]bool)
			}
			l.postWriteFailCalls[call] = true
		} else {
			if l.failCalls == nil {
				l.failCalls = make(map[int]bool)
			}
			l.failCalls[call] = true
		}
	}
	return l.countingEventLog.Append(ctx, id, ev)
}

func TestFailedHookAppendThenResumedRunUsesCommittedHighWater(t *testing.T) {
	for _, postWrite := range []bool{false, true} {
		name := "before write"
		if postWrite {
			name = "after write"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			store := memstore.New()
			log := &failFirstPublishedHookLog{postWrite: postWrite}
			diag := &countingDiagnostics{}
			svc, err := NewService(Config{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Hooks: recoveryHook{}, Model: "mock"}), Store: store, EventLog: log, Diagnostics: diag, PlacementProvider: lifecyclePlacementProvider{}, PlacementScope: "test", SharedEngineRoot: "/repo"})
			if err != nil {
				t.Fatal(err)
			}
			sess := session.New("append-recovery", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
			if err := store.Save(ctx, sess); err != nil {
				t.Fatal(err)
			}
			var revisions []uint64
			for range 2 {
				run, err := svc.StartRunContent(ctx, sess.ID, "go", nil)
				if err != nil {
					t.Fatal(err)
				}
				recorder := NewRunEventRecorder(ctx, svc, sess.ID)
				for ev := range run.Events() {
					if ev.Type == session.EvHook {
						revisions = append(revisions, ev.Hook.Revision)
					}
					recorder.Observe(ev)
				}
				recorder.Close()
				svc.FinishRun(sess.ID, run)
			}
			if !log.failed || diag.warnings == 0 || len(revisions) != 4 || revisions[2] <= revisions[1] || log.reads != 2 {
				t.Fatalf("append policy / resumed versions: failed=%v warnings=%d revisions=%v scans=%d", log.failed, diag.warnings, revisions, log.reads)
			}
			found := false
			for _, ev := range log.recorded {
				if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.Revision == revisions[2] {
					found = true
				}
			}
			if !found {
				t.Fatal("next revision did not reach durable sink")
			}
		})
	}
}

func TestAuthorizationRecoveryReadsTailBeforeContinuation(t *testing.T) {
	for _, mode := range []string{"control", "expiry", "interrupt"} {
		t.Run(mode, func(t *testing.T) {
			f := newLifecycleFixture(t, session.AuthorizationDenied, nil, time.Now, nil)
			makeHookEngine := func(mode session.PermissionMode) SessionEngineResult {
				engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Hooks: recoveryHook{}, Policy: lifecycleAllowPolicy{}, Store: f.store, Model: "mock"})
				return SessionEngineResult{Engine: engine, BuiltForMode: mode, Close: func() error { return nil }}
			}
			f.svc.cfg.SessionEngine = func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, mode session.PermissionMode) (SessionEngineResult, error) {
				return makeHookEngine(mode), nil
			}
			f.svc.cfg.SessionEngineWithTools = func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, mode session.PermissionMode, _ []tool.Tool) (SessionEngineResult, error) {
				return makeHookEngine(mode), nil
			}
			log := &readCountingHookLog{}
			f.svc.cfg.EventLog = log
			stored, err := f.store.Load(t.Context(), "authorization-session")
			if err != nil {
				t.Fatal(err)
			}
			stored.RestoreHookRevision(3)
			if err := f.store.Save(t.Context(), stored); err != nil {
				t.Fatal(err)
			}
			if err := log.Append(t.Context(), stored.ID, session.Event{Type: session.EvHook, Hook: &session.HookPayload{HookID: "hook_prior", SessionID: stored.ID, Revision: 8}}); err != nil {
				t.Fatal(err)
			}
			control := MCPAuthorizationControl{SessionID: stored.ID, AuthorizationID: f.pending.Authorization.ID}
			switch mode {
			case "control":
				result, err := f.svc.RecheckMCPAuthorization(t.Context(), stored.ID, control)
				if err != nil {
					t.Fatal(err)
				}
				recorder := NewRunEventRecorder(t.Context(), f.svc, stored.ID)
				for _, ev := range drainLifecycleRun(t, f.svc, result) {
					recorder.Observe(ev)
				}
				recorder.Close()
			case "expiry":
				f.svc.cfg.Now = func() time.Time { return f.pending.Authorization.ExpiresAt }
				entry := &authorizationExpiry{}
				f.svc.mu.Lock()
				f.svc.authorizationExpiry[stored.ID] = entry
				f.svc.mu.Unlock()
				f.svc.expireAuthorization(stored.ID, control.AuthorizationID, entry)
				deadline := time.Now().Add(time.Second)
				for {
					settled, loadErr := f.store.Load(t.Context(), stored.ID)
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					if settled.State.IsTerminal() && !f.svc.IsLive(stored.ID) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("expiry continuation did not settle")
					}
					time.Sleep(time.Millisecond)
				}
			case "interrupt":
				f.broker.attachErr = brokercontract.ErrStateUnavailable
				_, err := f.svc.StartRunContent(t.Context(), stored.ID, "go", nil)
				if !errors.Is(err, ErrFailedPrecondition) {
					t.Fatalf("restored authorization start = %v", err)
				}
				run, err := f.svc.StartRunContent(t.Context(), stored.ID, "resume", nil)
				if err != nil {
					t.Fatal(err)
				}
				recorder := NewRunEventRecorder(t.Context(), f.svc, stored.ID)
				for ev := range run.Events() {
					recorder.Observe(ev)
				}
				recorder.Close()
				f.svc.FinishRun(stored.ID, run)
			}
			loaded, err := f.store.Load(t.Context(), stored.ID)
			if err != nil {
				t.Fatal(err)
			}
			first := uint64(0)
			for _, ev := range log.recorded {
				if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.Revision > 8 && (first == 0 || ev.Hook.Revision < first) {
					first = ev.Hook.Revision
				}
			}
			if loaded.HookRevision() < 8 || log.reads == 0 || first != 9 {
				t.Fatalf("%s recovery = revision %d reads %d first published hook %d", mode, loaded.HookRevision(), log.reads, first)
			}
		})
	}
}

func TestAuthorizationRecoveryFailurePreservesPendingAndBlocksTools(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	diag := &repairDiagnostics{}
	f.svc.cfg.Diagnostics = diag
	f.svc.cfg.EventLog = &failingHookLog{}
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
	if !errors.Is(err, ErrInternal) || err.Error() != ErrInternal.Error() || result.Run != nil {
		t.Fatalf("admission failure = %+v, %v", result, err)
	}
	if diag.text == "" || !strings.Contains(diag.text, "private storage path /secret/log") {
		t.Fatalf("missing operator recovery cause: %q", diag.text)
	}
	stored, err := f.store.Load(t.Context(), control.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != session.StateAuthorizing || f.attach.tool.calls.Load() != 0 || stored.HookRevision() != 0 {
		t.Fatalf("failed recovery advanced authorization or executed tools: %+v", stored)
	}
}

func TestAuthorizationExpiryRecoveryFailureIsVisibleWithoutExecuting(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	diag := &repairDiagnostics{}
	f.svc.cfg.Diagnostics = diag
	f.svc.cfg.EventLog = &failingHookLog{}
	f.svc.cfg.Now = func() time.Time { return f.pending.Authorization.ExpiresAt }
	entry := &authorizationExpiry{retries: maxAuthorizationExpiryRetries}
	f.svc.mu.Lock()
	f.svc.authorizationExpiry["authorization-session"] = entry
	f.svc.mu.Unlock()
	f.svc.expireAuthorization("authorization-session", f.pending.Authorization.ID, entry)
	stored, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != session.StateAuthorizing || f.attach.tool.calls.Load() != 0 || !strings.Contains(diag.text, "external authorization expiry retries exhausted") || !strings.Contains(diag.text, "private storage path /secret/log") {
		t.Fatalf("expiry recovery failed to remain pending with visible cause: state=%s calls=%d diagnostics=%q", stored.State, f.attach.tool.calls.Load(), diag.text)
	}
}

type failingHookLog struct{ countingEventLog }

func (*failingHookLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		yield(session.Event{}, errors.New("private storage path /secret/log"))
	}
}

type readCountingHookLog struct {
	countingEventLog
	reads int
}

func (l *readCountingHookLog) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	l.reads++
	return l.countingEventLog.Read(ctx, id)
}

func TestHookRevisionRecoveryOnlyAtPublicationAdmission(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	log := &readCountingHookLog{}
	sess := session.New("hook-recovery", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "fixture", Revision: "v1"}, session.Limits{}, time.Now())
	sess.RestoreHookRevision(3)
	if err := store.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []uint64{2, 8, 7} {
		if err := log.Append(ctx, sess.ID, session.Event{Type: session.EvHook, Hook: &session.HookPayload{HookID: "hook_record", SessionID: sess.ID, Revision: revision}}); err != nil {
			t.Fatal(err)
		}
	}
	for range 1000 {
		if err := log.Append(ctx, sess.ID, session.Event{Type: session.EvMessageDelta, Text: "history"}); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{cfg: Config{Store: store, EventLog: log, Diagnostics: port.NopDiagnostics{}}}
	for range 2 {
		loaded, err := svc.GetSession(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := loaded.HookRevision(); got != 3 {
			t.Fatalf("read-only revision = %d", got)
		}
	}
	if log.reads != 0 {
		t.Fatalf("read-only lookup scanned the log %d times", log.reads)
	}
	loaded, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.admitHookPublication(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	if got := loaded.NextHookRevision(); got != 9 || log.reads != 1 {
		t.Fatalf("admitted next revision = %d, log scans = %d", got, log.reads)
	}
	unchanged, err := store.Load(ctx, sess.ID)
	if err != nil || unchanged.HookRevision() != 3 {
		t.Fatalf("admission triggered a migration save: revision %d, err %v", unchanged.HookRevision(), err)
	}
	svc.cfg.EventLog = &failingHookLog{}
	if _, err := svc.GetSession(ctx, sess.ID); err != nil {
		t.Fatalf("read-only lookup failed on log outage: %v", err)
	}
	if err := svc.admitHookPublication(ctx, unchanged); !errors.Is(err, ErrInternal) || err.Error() != ErrInternal.Error() {
		t.Fatalf("lost event log allowed publication or exposed backend: %v", err)
	}
}
