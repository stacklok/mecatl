package server_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type refreshStore struct {
	*memstore.Store
	mu            sync.Mutex
	saves         int
	saveErr       error
	commitOnError bool
	confirmMode   string
	saveStarted   chan struct{}
	releaseSave   chan struct{}
}

func (s *refreshStore) Save(ctx context.Context, candidate *session.Session) error {
	s.mu.Lock()
	s.saves++
	err := s.saveErr
	commit := err == nil || s.commitOnError
	s.mu.Unlock()
	if s.saveStarted != nil {
		close(s.saveStarted)
	}
	if s.releaseSave != nil {
		<-s.releaseSave
	}
	if commit {
		if saveErr := s.Store.Save(ctx, candidate); saveErr != nil {
			return saveErr
		}
	}
	return err
}

func (s *refreshStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.mu.Lock()
	confirm := s.saves > 0 && s.saveErr != nil
	mode := s.confirmMode
	s.mu.Unlock()
	if confirm && mode == "unavailable" {
		return nil, port.NewSessionLoadFailure(port.SessionLoadFailureStore, errors.New("unavailable"))
	}
	loaded, err := s.Store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if confirm && mode == "mismatch" {
		if err := loaded.RenameTitle("concurrent value"); err != nil {
			return nil, err
		}
	}
	return loaded, nil
}

func (s *refreshStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func TestMCPRefreshLoadFailuresAreSanitized(t *testing.T) {
	const private = "decode failed at /private/store/session.json"
	diag := &recordingDiagnostics{}
	store := loadFailingStore{err: port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, errors.New(private))}
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Store: store})
	svc, err := newPlacementTestService(server.Config{
		Engine: eng, Store: store, OwnershipEnforced: true, Diagnostics: diag,
		MCPRefresh: func(context.Context) (server.MCPRefreshSnapshot, error) { return server.MCPRefreshSnapshot{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "owner"})
	_, err = svc.RefreshMcpSources(ctx, "session")
	if !errors.Is(err, server.ErrInternal) || strings.Contains(err.Error(), private) {
		t.Fatalf("RefreshMcpSources error = %q, want sanitized internal error", err)
	}
	if !diag.contains("snapshot") {
		t.Fatalf("private diagnostic classification missing: %+v", diag.entries)
	}
}

func TestMCPSourceReconciliation_Scenario4_ServiceRefreshMutationMatrix(t *testing.T) {
	owner := &session.Principal{Issuer: "https://issuer.example.com", Subject: "alice"}
	ownerCtx := session.WithPrincipal(context.Background(), owner)
	foreignCtx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: owner.Issuer, Subject: "bob"})

	newFixture := func(t *testing.T, names []string, saveErr error, commitOnError bool, onRefresh func(), leases ...port.SessionLease) (*server.Service, *refreshStore, session.SessionID) {
		t.Helper()
		var lease port.SessionLease
		if len(leases) > 0 {
			lease = leases[0]
		}
		store := &refreshStore{Store: memstore.New(), saveErr: saveErr, commitOnError: commitOnError}
		eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test"})
		svc, err := newPlacementTestService(server.Config{
			Engine: eng, Store: store, OwnershipEnforced: true,
			SessionLease: lease, LeaseOwner: "test", LeaseTTL: time.Hour, LeaseRenewInterval: time.Hour,
			RootAuthority: func(session.SessionKind) session.Authority {
				return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}, Provenance: "test"}
			},
			MCPRefresh: func(context.Context) (server.MCPRefreshSnapshot, error) {
				if onRefresh != nil {
					onRefresh()
				}
				return server.MCPRefreshSnapshot{Revision: 7, ToolNames: append([]string(nil), names...)}, nil
			},
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		sess, err := svc.CreateSession(ownerCtx, session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		store.mu.Lock()
		store.saves = 0
		store.mu.Unlock()
		return svc, store, sess.ID
	}

	t.Run("no-op-zero-write-no-mutation-capability", func(t *testing.T) {
		testLease := &mutationLease{}
		svc, store, id := newFixture(t, []string{"Read"}, nil, false, nil, testLease)
		got, err := svc.RefreshMcpSources(ownerCtx, id)
		if err != nil {
			t.Fatalf("RefreshMcpSources: %v", err)
		}
		acquires, _ := testLease.counts()
		if got.Revision != 7 || got.Changed || store.saveCount() != 0 || acquires != 0 {
			t.Fatalf("result=%+v saves=%d lease acquires=%d", got, store.saveCount(), acquires)
		}
	})

	t.Run("addition-acquires-mutation-authority", func(t *testing.T) {
		testLease := &mutationLease{}
		svc, store, id := newFixture(t, []string{"mcp__new__call"}, nil, false, nil, testLease)
		if _, err := svc.RefreshMcpSources(ownerCtx, id); err != nil {
			t.Fatalf("RefreshMcpSources: %v", err)
		}
		acquires, releases := testLease.counts()
		if store.saveCount() != 1 || acquires != 1 || releases != 1 {
			t.Fatalf("saves=%d lease=%d/%d", store.saveCount(), acquires, releases)
		}
	})

	for _, tc := range []struct {
		name          string
		saveErr       error
		commitOnError bool
		confirmMode   string
		wantErr       bool
		wantUncertain bool
	}{
		{name: "save-direct-success"},
		{name: "save-error-reload-exact-candidate", saveErr: errors.New("ambiguous"), commitOnError: true},
		{name: "save-error-reload-exact-old", saveErr: errors.New("rejected"), wantErr: true},
		{name: "save-error-reload-mismatch-uncertain", saveErr: errors.New("ambiguous"), confirmMode: "mismatch", wantErr: true, wantUncertain: true},
		{name: "save-error-reload-unavailable-uncertain", saveErr: errors.New("ambiguous"), confirmMode: "unavailable", wantErr: true, wantUncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, id := newFixture(t, []string{"Read", "mcp__new__call"}, tc.saveErr, tc.commitOnError, nil)
			store.confirmMode = tc.confirmMode
			got, err := svc.RefreshMcpSources(ownerCtx, id)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantUncertain && !errors.Is(err, server.ErrUnavailable) {
				t.Fatalf("err=%v want uncertain ErrUnavailable", err)
			}
			if store.saveCount() != 1 {
				t.Fatalf("save count=%d want 1", store.saveCount())
			}
			persisted, loadErr := store.Store.Load(context.Background(), id)
			if loadErr != nil {
				t.Fatalf("Load: %v", loadErr)
			}
			auth, _ := persisted.BoundAuthority()
			hasNew := auth.CapabilitySet.Contains(governance.CapabilitySet{Tools: []string{"mcp__new__call"}})
			if !tc.wantErr && (!hasNew || !got.Changed || got.Revision != 7) {
				t.Fatalf("result=%+v authority=%+v", got, auth)
			}
			if tc.wantErr && hasNew {
				t.Fatalf("failed save widened authority: %+v", auth)
			}
		})
	}

	t.Run("cancel-before-save", func(t *testing.T) {
		ctx, cancel := context.WithCancel(ownerCtx)
		svc, store, id := newFixture(t, []string{"mcp__new__call"}, nil, false, cancel)
		_, err := svc.RefreshMcpSources(ctx, id)
		if !errors.Is(err, context.Canceled) || store.saveCount() != 0 {
			t.Fatalf("err=%v saves=%d", err, store.saveCount())
		}
	})

	t.Run("cancel-after-save-start", func(t *testing.T) {
		svc, store, id := newFixture(t, []string{"mcp__new__call"}, nil, false, nil)
		store.saveStarted = make(chan struct{})
		store.releaseSave = make(chan struct{})
		ctx, cancel := context.WithCancel(ownerCtx)
		done := make(chan error, 1)
		go func() {
			_, err := svc.RefreshMcpSources(ctx, id)
			done <- err
		}()
		<-store.saveStarted
		cancel()
		close(store.releaseSave)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
		persisted, err := store.Store.Load(context.Background(), id)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		auth, _ := persisted.BoundAuthority()
		if !auth.CapabilitySet.Contains(governance.CapabilitySet{Tools: []string{"mcp__new__call"}}) {
			t.Fatalf("post-start cancellation rolled back committed authority: %+v", auth)
		}
	})

	t.Run("completed-state-preserved", func(t *testing.T) {
		svc, store, id := newFixture(t, []string{"mcp__new__call"}, nil, false, nil)
		sess, err := store.Store.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := sess.Complete(); err != nil {
			t.Fatal(err)
		}
		if err := store.Store.Save(context.Background(), sess); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RefreshMcpSources(ownerCtx, id); err != nil {
			t.Fatalf("RefreshMcpSources: %v", err)
		}
		persisted, _ := store.Store.Load(context.Background(), id)
		if persisted.State != session.StateCompleted {
			t.Fatalf("completed refresh reopened state: %s", persisted.State)
		}
	})

	t.Run("no-fs-and-unrelated-authority-preserved", func(t *testing.T) {
		store := &refreshStore{Store: memstore.New()}
		eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test"})
		svc, err := newPlacementTestService(server.Config{
			Engine: eng, Store: store, OwnershipEnforced: true,
			SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
				return server.SessionEngineResult{Engine: eng}, nil
			},
			RootAuthority: func(session.SessionKind) session.Authority {
				return session.Authority{CapabilitySet: governance.CapabilitySet{
					Tools: []string{"Read", "mcp__client__keep"}, RemainingDelegationDepth: 3,
				}, Provenance: "test"}
			},
			MCPRefresh: func(context.Context) (server.MCPRefreshSnapshot, error) {
				return server.MCPRefreshSnapshot{Revision: 8, ToolNames: []string{"mcp__direct__new"}}, nil
			},
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		sess, err := svc.CreateSessionWithProfile(ownerCtx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS)
		if err != nil {
			t.Fatalf("CreateSessionWithProfile: %v", err)
		}
		before, _ := sess.BoundAuthority()
		store.mu.Lock()
		store.saves = 0
		store.mu.Unlock()
		if _, err := svc.RefreshMcpSources(ownerCtx, sess.ID); err != nil {
			t.Fatalf("RefreshMcpSources: %v", err)
		}
		afterSession, err := store.Store.Load(context.Background(), sess.ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		after, _ := afterSession.BoundAuthority()
		if after.CapabilitySet.FileSystem || after.CapabilitySet.DirectWrite || after.CapabilitySet.RemainingDelegationDepth != before.CapabilitySet.RemainingDelegationDepth {
			t.Fatalf("unrelated no-FS authority changed: before=%+v after=%+v", before, after)
		}
		if !after.CapabilitySet.Contains(governance.CapabilitySet{Tools: []string{"mcp__client__keep", "mcp__direct__new"}}) {
			t.Fatalf("client/unrelated authority not preserved: %+v", after)
		}
	})

	t.Run("ineligible-state-and-taxonomy", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*session.Session) error
		}{
			{"running", func(s *session.Session) error { return s.BeginTurn() }},
			{"awaiting", func(s *session.Session) error {
				if err := s.BeginTurn(); err != nil {
					return err
				}
				return s.PauseForApproval(session.PendingAsk{AskID: "ask"})
			}},
			{"failed", func(s *session.Session) error {
				if err := s.BeginTurn(); err != nil {
					return err
				}
				return s.Fail()
			}},
			{"cancelled", func(s *session.Session) error {
				if err := s.BeginTurn(); err != nil {
					return err
				}
				return s.Cancel()
			}},
			{"child", func(s *session.Session) error {
				return s.RestoreSessionMetadata(session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: "parent", CallID: "call"})
			}},
			{"schedule", func(s *session.Session) error {
				return s.RestoreSessionMetadata(session.SessionKindScheduled, session.SessionRelationship{ScheduleName: "nightly"})
			}},
			{"debug", func(s *session.Session) error {
				return s.RestoreSessionMetadata(session.SessionKindDebug, session.SessionRelationship{DebugTargetID: "target"})
			}},
			{"broker-conflicting", func(s *session.Session) error { s.ExternalBinding = "broker"; return nil }},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var reconciles atomic.Int32
				svc, store, id := newFixture(t, []string{"mcp__new__call"}, nil, false, func() { reconciles.Add(1) })
				sess, err := store.Store.Load(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				if err := tc.mutate(sess); err != nil {
					t.Fatalf("mutate: %v", err)
				}
				if err := store.Store.Save(context.Background(), sess); err != nil {
					t.Fatalf("save fixture: %v", err)
				}
				if _, err := svc.RefreshMcpSources(ownerCtx, id); !errors.Is(err, server.ErrFailedPrecondition) {
					t.Fatalf("err=%v want ErrFailedPrecondition", err)
				}
				if store.saveCount() != 0 {
					t.Fatalf("refresh persisted ineligible session %d times", store.saveCount())
				}
				if got := reconciles.Load(); got != 0 {
					t.Fatalf("ineligible refresh reconciled %d times, want 0", got)
				}
			})
		}
	})

	t.Run("stable-union-retry-converges", func(t *testing.T) {
		svc, store, id := newFixture(t, []string{"mcp__new__call"}, nil, false, nil)
		if _, err := svc.RefreshMcpSources(ownerCtx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RefreshMcpSources(ownerCtx, id); err != nil {
			t.Fatal(err)
		}
		if store.saveCount() != 1 {
			t.Fatalf("converged refresh saves=%d want 1", store.saveCount())
		}
	})

	t.Run("concurrent-callers-fresh-load-under-run-entry-exclusion", func(t *testing.T) {
		svc, store, id := newFixture(t, []string{"mcp__new__call"}, nil, false, nil)
		const callers = 8
		start := make(chan struct{})
		errs := make(chan error, callers)
		for range callers {
			go func() {
				<-start
				_, err := svc.RefreshMcpSources(ownerCtx, id)
				errs <- err
			}()
		}
		close(start)
		for range callers {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent refresh: %v", err)
			}
		}
		if store.saveCount() != 1 {
			t.Fatalf("concurrent stable union saved %d times, want 1", store.saveCount())
		}
	})

	t.Run("run-entry-lock-precedes-reconciliation", func(t *testing.T) {
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		var calls atomic.Int32
		svc, _, id := newFixture(t, []string{"Read"}, nil, false, func() {
			entered <- struct{}{}
			if calls.Add(1) == 1 {
				<-release
			}
		})
		done := make(chan error, 2)
		go func() { _, err := svc.RefreshMcpSources(ownerCtx, id); done <- err }()
		<-entered
		go func() { _, err := svc.RefreshMcpSources(ownerCtx, id); done <- err }()
		select {
		case <-entered:
			t.Fatal("second reconciler entered before the first released run-entry exclusion")
		case <-time.After(20 * time.Millisecond):
		}
		close(release)
		for range 2 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("serialized reconciler calls = %d, want 2", got)
		}
	})

	t.Run("active-run-excluded-at-local-linearization-point", func(t *testing.T) {
		svc, store, id := newFixture(t, []string{"mcp__new__call"}, nil, false, nil)
		run, err := svc.StartRun(ownerCtx, id, "keep registered")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		if _, err := svc.RefreshMcpSources(ownerCtx, id); !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("refresh during registered run err=%v", err)
		}
		for range run.Events() {
		}
		svc.FinishRun(id, run)
		if store.saveCount() != 0 {
			t.Fatalf("refresh during run saved %d times", store.saveCount())
		}
	})

	t.Run("actual-service-grant-count-and-name-byte-bounds", func(t *testing.T) {
		t.Run("historical-authority-bound", func(t *testing.T) {
			makeNames := func(n int) []string {
				names := make([]string, n)
				for i := range names {
					names[i] = fmt.Sprintf("historical_tool_%05d", i)
				}
				return names
			}
			newHistoricalFixture := func(t *testing.T, initial, active []string) (*server.Service, *refreshStore, session.SessionID) {
				t.Helper()
				store := &refreshStore{Store: memstore.New()}
				eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test"})
				svc, err := newPlacementTestService(server.Config{
					Engine: eng, Store: store, OwnershipEnforced: true,
					RootAuthority: func(session.SessionKind) session.Authority {
						return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: append([]string(nil), initial...)}, Provenance: "test"}
					},
					MCPRefresh: func(context.Context) (server.MCPRefreshSnapshot, error) {
						return server.MCPRefreshSnapshot{Revision: 7, ToolNames: append([]string(nil), active...)}, nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				sess, err := svc.CreateSession(ownerCtx, session.ModeDefault, session.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				store.mu.Lock()
				store.saves = 0
				store.mu.Unlock()
				return svc, store, sess.ID
			}

			atBound := makeNames(16_384)
			svc, store, id := newHistoricalFixture(t, atBound, atBound[:1])
			got, err := svc.RefreshMcpSources(ownerCtx, id)
			if err != nil || got.Changed || store.saveCount() != 0 {
				t.Fatalf("at bound result=%+v err=%v saves=%d", got, err, store.saveCount())
			}

			overBound := makeNames(16_385)
			svc, store, id = newHistoricalFixture(t, overBound, overBound[:1])
			if _, err := svc.RefreshMcpSources(ownerCtx, id); !errors.Is(err, server.ErrFailedPrecondition) || store.saveCount() != 0 {
				t.Fatalf("over bound err=%v saves=%d", err, store.saveCount())
			}

			legacyAbove512 := makeNames(513)
			svc, store, id = newHistoricalFixture(t, legacyAbove512, []string{"mcp__new__call"})
			got, err = svc.RefreshMcpSources(ownerCtx, id)
			if err != nil || !got.Changed || store.saveCount() != 1 {
				t.Fatalf("valid >512 result=%+v err=%v saves=%d", got, err, store.saveCount())
			}
		})

		t.Run("union-count-bound", func(t *testing.T) {
			names := make([]string, 1_025)
			for i := range names {
				names[i] = fmt.Sprintf("mcp__bounded__tool_%04d", i)
			}
			svc, store, id := newFixture(t, names, nil, false, nil)
			if _, err := svc.RefreshMcpSources(ownerCtx, id); !errors.Is(err, server.ErrFailedPrecondition) {
				t.Fatalf("err=%v want ErrFailedPrecondition", err)
			}
			if store.saveCount() != 0 {
				t.Fatalf("over-bound union saved %d times", store.saveCount())
			}
		})

		t.Run("tool-name-byte-bound", func(t *testing.T) {
			name := strings.Repeat("x", 257)
			svc, store, id := newFixture(t, []string{name}, nil, false, nil)
			if _, err := svc.RefreshMcpSources(ownerCtx, id); !errors.Is(err, server.ErrFailedPrecondition) {
				t.Fatalf("err=%v want ErrFailedPrecondition", err)
			}
			if store.saveCount() != 0 {
				t.Fatalf("invalid-name union saved %d times", store.saveCount())
			}
		})
	})

	t.Run("owner-concealment", func(t *testing.T) {
		svc, store, id := newFixture(t, []string{"mcp__new__call"}, nil, false, nil)
		_, err := svc.RefreshMcpSources(foreignCtx, id)
		if !errors.Is(err, server.ErrNotFound) || store.saveCount() != 0 {
			t.Fatalf("foreign refresh err=%v saves=%d", err, store.saveCount())
		}
	})
}
