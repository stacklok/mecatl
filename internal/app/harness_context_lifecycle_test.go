package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type failingHarnessPlacement struct{}

func (failingHarnessPlacement) Bind(context.Context, server.PlacementBindRequest) (server.PlacementBinding, error) {
	return server.PlacementBinding{}, errors.New("late placement validation failure")
}

func TestHarnessBuildLateFailureOwnsEachCleanupOnce(t *testing.T) {
	kinds := harnessEmptyKinds()
	kinds.Commands = permconfig.HarnessContextKind{Sources: []string{"command"}, Mode: "combine"}
	kinds.Rules = permconfig.HarnessContextKind{Sources: []string{"rules"}, Mode: "combine"}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"command", "rules"}, Kinds: kinds})
	var commandClosed, rulesClosed atomic.Int32
	cfg.HarnessCommandSources = []HarnessSourceRegistration[server.CommandSourceBinding]{{ID: "command", Scope: HarnessSourceScopeProcess, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return &hcCommands{values: map[string]string{"x": "body"}}, func() error { commandClosed.Add(1); return nil }, nil
	}}}
	cfg.HarnessRulesSources = []HarnessSourceRegistration[prompt.RulesSource]{{ID: "rules", Scope: HarnessSourceScopeProcess, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (prompt.RulesSource, func() error, error) {
		return frozenHarnessRules{rules: []prompt.Rule{{Name: "rule", Body: "body"}}}, func() error { rulesClosed.Add(1); return nil }, nil
	}}}
	cfg.PlacementProvider = failingHarnessPlacement{}
	cfg.PlacementScope = "late-failure"
	built, err := buildIsolated(t, t.Context(), cfg)
	if err == nil || built != nil || !strings.Contains(err.Error(), "validate default placement") {
		t.Fatalf("late Build failure=%v, built=%v", err, built)
	}
	if commandClosed.Load() != 1 || rulesClosed.Load() != 1 {
		t.Fatalf("late Build cleanup command=%d rules=%d", commandClosed.Load(), rulesClosed.Load())
	}
	cfg.PlacementProvider = nil
	cfg.PlacementScope = ""
	built, err = buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	built.Close()
	built.Close()
	if commandClosed.Load() != 2 || rulesClosed.Load() != 2 {
		t.Fatalf("successful repeated Close cleanup command=%d rules=%d", commandClosed.Load(), rulesClosed.Load())
	}
}

func TestHarnessCommandShutdownWaitsForBorrowers(t *testing.T) {
	var closed atomic.Int32
	source := &hcCommands{values: map[string]string{"x": "body"}}
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopeProcess, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return source, func() error { closed.Add(1); return nil }, nil
	}}
	r, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	binding, release, err := r.Borrow(t.Context(), "session", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	r.Close()
	if closed.Load() != 0 {
		t.Fatal("process source closed during an in-flight borrow")
	}
	if _, err := binding.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if closed.Load() != 1 {
		t.Fatalf("cleanup calls=%d", closed.Load())
	}
	if _, _, err := r.Borrow(t.Context(), "another", nil, ""); err == nil {
		t.Fatal("borrow after shutdown succeeded")
	}
}

func TestHarnessCommandBindingConcurrentRetryAndRetirement(t *testing.T) {
	var calls, closed atomic.Int32
	source := &hcCommands{values: map[string]string{"x": "body"}}
	boom := errors.New("bind failed")
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(_ context.Context, scope HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, func() error { closed.Add(1); return nil }, boom
		}
		if scope.Principal == nil || scope.Principal.Subject != "owner" {
			return nil, nil, errors.New("wrong scope")
		}
		scope.Principal.Subject = "mutated copy"
		return source, func() error { closed.Add(1); return nil }, nil
	}}
	r, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	owner := &session.Principal{Issuer: "issuer", Subject: "owner"}
	if _, _, err := r.Borrow(t.Context(), "s", owner, ""); !errors.Is(err, boom) {
		t.Fatalf("first bind=%v", err)
	}
	if closed.Load() != 1 {
		t.Fatal("failed binding leaked its cleanup")
	}
	const borrowers = 8
	var wg sync.WaitGroup
	releases := make(chan func(), borrowers)
	for range borrowers {
		wg.Go(func() {
			_, release, err := r.Borrow(t.Context(), "s", owner, "")
			if err != nil {
				t.Error(err)
				return
			}
			releases <- release
		})
	}
	wg.Wait()
	close(releases)
	if calls.Load() != 2 {
		t.Fatalf("bindings=%d want one failed then one successful", calls.Load())
	}
	if owner.Subject != "owner" {
		t.Fatal("binder mutated caller metadata")
	}
	if _, _, err := r.Borrow(t.Context(), "s", owner, "no-fs"); err == nil {
		t.Fatal("profile mismatch accepted")
	}
	other := &session.Principal{Issuer: "issuer", Subject: "other"}
	if _, _, err := r.Borrow(t.Context(), "s", other, ""); err == nil {
		t.Fatal("owner mismatch accepted")
	}
	r.Retire("s")
	r.Retire("s")
	if closed.Load() != 1 {
		t.Fatal("retirement closed in-use source")
	}
	for release := range releases {
		release()
		release()
	}
	if closed.Load() != 2 {
		t.Fatalf("cleanup calls=%d", closed.Load())
	}
	if _, _, err := r.Borrow(t.Context(), "s", owner, ""); err == nil {
		t.Fatal("retired binding admitted new borrower")
	}
}

func TestHarnessCommandUnselectedRegistrationIsNotBound(t *testing.T) {
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "unused", Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		t.Error("unselected source was bound")
		return prompt.NoopExpander{}, nil, nil
	}}
	r, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

type harnessRulesFunc func(context.Context) ([]prompt.Rule, error)

func (f harnessRulesFunc) ListRules(ctx context.Context) ([]prompt.Rule, error) { return f(ctx) }

func TestProcessHarnessTransientFailureRetries(t *testing.T) {
	var binds, cleanups atomic.Int32
	transient := errors.New("transient bind failure")
	regs, closeAll := cacheProcessHarness([]HarnessSourceRegistration[string]{{
		ID: "process", Scope: HarnessSourceScopeProcess,
		Bind: func(context.Context, HarnessSourceScope) (string, func() error, error) {
			if binds.Add(1) == 1 {
				return "discarded", func() error { cleanups.Add(1); return nil }, transient
			}
			return "healthy", func() error { cleanups.Add(1); return nil }, nil
		},
	}}, nil)
	if _, _, err := regs[0].Bind(t.Context(), HarnessSourceScope{}); !errors.Is(err, transient) {
		t.Fatalf("first bind error = %v", err)
	}
	got, _, err := regs[0].Bind(t.Context(), HarnessSourceScope{})
	if err != nil || got != "healthy" {
		t.Fatalf("retry = %q, %v", got, err)
	}
	if binds.Load() != 2 || cleanups.Load() != 1 {
		t.Fatalf("before close binds=%d cleanups=%d", binds.Load(), cleanups.Load())
	}
	closeAll()
	if cleanups.Load() != 2 {
		t.Fatalf("after close cleanups=%d", cleanups.Load())
	}
}

func TestProcessHarnessSuccessfulSnapshotSharedAcrossOwners(t *testing.T) {
	var binds, snapshots, cleanups atomic.Int32
	regs, closeAll := cacheProcessHarness([]HarnessSourceRegistration[string]{{
		ID: "process", Scope: HarnessSourceScopeProcess,
		Bind: func(context.Context, HarnessSourceScope) (string, func() error, error) {
			binds.Add(1)
			return "bound", func() error { cleanups.Add(1); return nil }, nil
		},
	}}, func(_ context.Context, value string) (string, error) {
		snapshots.Add(1)
		return value + "-snapshot", nil
	})
	defer closeAll()

	const owners = 8
	var wg sync.WaitGroup
	for range owners {
		wg.Go(func() {
			got, _, err := regs[0].Bind(t.Context(), HarnessSourceScope{})
			if err != nil || got != "bound-snapshot" {
				t.Errorf("cached bind = %q, %v", got, err)
			}
		})
	}
	wg.Wait()
	if binds.Load() != 1 || snapshots.Load() != 1 || cleanups.Load() != 0 {
		t.Fatalf("binds=%d snapshots=%d cleanups=%d", binds.Load(), snapshots.Load(), cleanups.Load())
	}
}

func TestProcessHarnessCloseDoesNotWaitForPendingBind(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var cleanups atomic.Int32
	regs, closeAll := cacheProcessHarness([]HarnessSourceRegistration[string]{{
		ID: "process", Scope: HarnessSourceScopeProcess,
		Bind: func(context.Context, HarnessSourceScope) (string, func() error, error) {
			close(started)
			<-release
			return "late", func() error { cleanups.Add(1); return nil }, nil
		},
	}}, nil)
	result := make(chan error, 1)
	go func() {
		got, _, err := regs[0].Bind(context.Background(), HarnessSourceScope{})
		if got != "" {
			result <- fmt.Errorf("late source published: %q", got)
			return
		}
		result <- err
	}()
	<-started
	waitCtx, cancelWait := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() {
		_, _, err := regs[0].Bind(waitCtx, HarnessSourceScope{})
		waited <- err
	}()
	cancelWait()
	if err := <-waited; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter = %v", err)
	}
	closed := make(chan struct{})
	go func() { closeAll(); close(closed) }()
	select {
	case <-closed:
	case <-t.Context().Done():
		t.Fatal("close waited for pending binder")
	}
	close(release)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "is closed") {
		t.Fatalf("late bind error = %v", err)
	}
	if cleanups.Load() != 1 {
		t.Fatalf("late cleanup calls=%d", cleanups.Load())
	}
}

func TestProcessHarnessRulesCancellationRetriesButOtherErrorsStayFailSoft(t *testing.T) {
	t.Run("cancellation retries", func(t *testing.T) {
		var binds, cleanups atomic.Int32
		cfg := Config{HarnessRulesSources: []HarnessSourceRegistration[prompt.RulesSource]{{
			ID: "rules", Scope: HarnessSourceScopeProcess,
			Bind: func(context.Context, HarnessSourceScope) (prompt.RulesSource, func() error, error) {
				n := binds.Add(1)
				source := harnessRulesFunc(func(ctx context.Context) ([]prompt.Rule, error) {
					if n == 1 {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return []prompt.Rule{{Name: "healthy", Body: "healthy"}}, nil
				})
				return source, func() error { cleanups.Add(1); return nil }, nil
			},
		}}}
		prepareHarnessProcessBindings(&cfg)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := cfg.HarnessRulesSources[0].Bind(ctx, HarnessSourceScope{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled snapshot = %v", err)
		}
		source, _, err := cfg.HarnessRulesSources[0].Bind(t.Context(), HarnessSourceScope{})
		if err != nil {
			t.Fatal(err)
		}
		rules, err := source.ListRules(t.Context())
		if err != nil || len(rules) != 1 || rules[0].Body != "healthy" {
			t.Fatalf("retried rules=%v, err=%v", rules, err)
		}
		cfg.harnessContextClose()
		if binds.Load() != 2 || cleanups.Load() != 2 {
			t.Fatalf("binds=%d cleanups=%d", binds.Load(), cleanups.Load())
		}
	})

	t.Run("non-cancellation remains fail-soft", func(t *testing.T) {
		var binds atomic.Int32
		backendErr := errors.New("rules backend unavailable")
		cfg := Config{HarnessRulesSources: []HarnessSourceRegistration[prompt.RulesSource]{{
			ID: "rules", Scope: HarnessSourceScopeProcess,
			Bind: func(context.Context, HarnessSourceScope) (prompt.RulesSource, func() error, error) {
				binds.Add(1)
				return harnessRulesFunc(func(context.Context) ([]prompt.Rule, error) { return nil, backendErr }), nil, nil
			},
		}}}
		prepareHarnessProcessBindings(&cfg)
		defer cfg.harnessContextClose()
		for range 2 {
			source, _, err := cfg.HarnessRulesSources[0].Bind(t.Context(), HarnessSourceScope{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.ListRules(t.Context()); !errors.Is(err, backendErr) {
				t.Fatalf("frozen rules error = %v", err)
			}
		}
		if binds.Load() != 1 {
			t.Fatalf("fail-soft source binds=%d", binds.Load())
		}
	})
}
