package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type parkedHarnessCommands struct {
	started chan struct{}
	resume  chan struct{}
	once    *sync.Once
}

func (c parkedHarnessCommands) List(ctx context.Context) ([]prompt.Command, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.resume:
		return []prompt.Command{{Name: "go"}}, nil
	}
}

func (parkedHarnessCommands) Expand(_ context.Context, _ string) (string, bool, error) {
	return "EXPANDED-OLD-GENERATION", true, nil
}

func TestHarnessRetiredServiceEngineDrainsParkedOperation(t *testing.T) {
	kinds := harnessEmptyKinds()
	kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"tenant"}, Mode: "combine"}
	kinds.Commands = permconfig.HarnessContextKind{Sources: []string{"tenant"}, Mode: "combine"}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"tenant"}, Kinds: kinds})
	started, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var cleaned atomic.Int32
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) { requests = append(requests, request) })}, mockllm.TextTurn("done"))
	cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		return hcAssembler("INSTRUCTIONS-OLD-GENERATION"), nil, nil
	}}}
	cfg.HarnessCommandSources = []HarnessSourceRegistration[server.CommandSourceBinding]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return parkedHarnessCommands{started: started, resume: resume, once: &once}, func() error { cleaned.Add(1); return nil }, nil
	}}}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(t.Context(), sess.ID, "/go")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	built.Service.CloseSession(sess.ID)
	close(resume)
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	if len(requests) != 1 {
		t.Fatalf("model requests=%d", len(requests))
	}
	var actual strings.Builder
	actual.WriteString(requests[0].System.Render())
	for _, message := range requests[0].Messages {
		actual.WriteString(message.Text)
	}
	if !strings.Contains(actual.String(), "EXPANDED-OLD-GENERATION") {
		t.Fatalf("old leased engine lost the command expansion after its parked list: %q", actual.String())
	}
	if cleaned.Load() != 1 {
		t.Fatalf("retired generation cleanup calls = %d, want 1", cleaned.Load())
	}
}

func TestHarnessResolverBindingDoesNotHoldGlobalLock(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	var blockedOnce sync.Once
	var cleaned atomic.Int32
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(_ context.Context, scope HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		if scope.Principal != nil && scope.Principal.Subject == "blocked" {
			blockedOnce.Do(func() { close(started) })
			<-unblock
		}
		return &hcCommands{values: map[string]string{"x": scope.Principal.Subject}}, func() error { cleaned.Add(1); return nil }, nil
	}}
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	blocked := &session.Principal{Issuer: "issuer", Subject: "blocked"}
	other := &session.Principal{Issuer: "issuer", Subject: "other"}
	blockedDone := make(chan error, 1)
	go func() {
		_, release, borrowErr := resolver.Borrow(context.Background(), "blocked", blocked, "")
		if release != nil {
			release()
		}
		blockedDone <- borrowErr
	}()
	<-started
	binding, release, err := resolver.Borrow(t.Context(), "other", other, "")
	if err != nil {
		t.Fatalf("unrelated borrow blocked by remote binder: %v", err)
	}
	if out, ok, expandErr := binding.Expand(t.Context(), "/x"); expandErr != nil || !ok || out != "other" {
		t.Fatalf("unrelated binding = %q,%v,%v", out, ok, expandErr)
	}
	release()

	waitCtx, cancel := context.WithCancel(context.Background())
	waitDone := make(chan error, 1)
	go func() {
		_, _, waitErr := resolver.Borrow(waitCtx, "blocked", blocked, "")
		waitDone <- waitErr
	}()
	cancel()
	if waitErr := <-waitDone; !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("same-id canceled waiter = %v", waitErr)
	}

	resolver.Close()
	close(unblock)
	if bindErr := <-blockedDone; bindErr == nil {
		t.Fatal("binding published after resolver shutdown")
	}
	if cleaned.Load() != 2 {
		t.Fatalf("cleanup calls = %d, want successful unrelated and late blocked results", cleaned.Load())
	}
}

func TestHarnessGenerationOldReleaseCannotEvictReplacement(t *testing.T) {
	var binds, closed atomic.Int32
	oldCleanupStarted, finishOldCleanup := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(finishOldCleanup) })
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		generation := binds.Add(1)
		return &hcCommands{values: map[string]string{"x": fmt.Sprint(generation)}}, func() error {
			if generation == 1 {
				close(oldCleanupStarted)
				<-finishOldCleanup
			}
			closed.Add(1)
			return nil
		}, nil
	}}
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	old, releaseOld, err := resolver.Borrow(t.Context(), "s", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	oldGeneration := old.(*resolvedCommandBinding).generation
	resolver.Retire("s")
	leased := generationCommands{harnessGeneration: oldGeneration, source: old}
	if out, ok, expandErr := leased.Expand(t.Context(), "/x"); expandErr != nil || !ok || out != "1" {
		t.Fatalf("already-borrowed generation stopped draining after retirement: %q,%v,%v", out, ok, expandErr)
	}
	if _, _, err := resolver.Borrow(t.Context(), "s", nil, ""); err == nil {
		t.Fatal("ordinary borrow resurrected retirement")
	}
	if err := resolver.Activate(t.Context(), "s", nil, ""); err != nil {
		t.Fatal(err)
	}
	current, releaseCurrent, err := resolver.Borrow(t.Context(), "s", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseCurrent()
	if current == old {
		t.Fatal("retired generation reopened")
	}
	done := make(chan struct{})
	go func() { releaseOld(); close(done) }()
	select {
	case <-oldCleanupStarted:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	out, ok, err := current.Expand(t.Context(), "/x")
	if err != nil || !ok || out != "2" {
		t.Fatalf("replacement during old cleanup=%q,%v,%v", out, ok, err)
	}
	unblock.Do(func() { close(finishOldCleanup) })
	<-done
	if _, _, err := leased.Expand(t.Context(), "/x"); err == nil {
		t.Fatal("drained retired generation remained usable after owner release")
	}
	if oldEntry := oldGeneration.entry; oldEntry.binding != nil || oldEntry.cleanup != nil {
		t.Fatal("drained generation retained binding or cleanup payload")
	}
	releaseOld()
	again, releaseAgain, err := resolver.Borrow(t.Context(), "s", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if again != current || binds.Load() != 2 {
		t.Fatal("old cleanup evicted replacement")
	}
	releaseAgain()
	resolver.Close()
	if err := resolver.Activate(t.Context(), "s", nil, ""); err == nil {
		t.Fatal("activation after Build shutdown")
	}
	if closed.Load() != 1 {
		t.Fatalf("in-use replacement closed early: %d", closed.Load())
	}
	releaseCurrent()
	if closed.Load() != 2 {
		t.Fatalf("cleanup calls=%d", closed.Load())
	}
}

func TestHarnessGenerationActivationRechecksScopeAndAuthorization(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice"}
	var revoked atomic.Bool
	var binds atomic.Int32
	denied := errors.New("source access revoked")
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		binds.Add(1)
		if revoked.Load() {
			return nil, nil, denied
		}
		return &hcCommands{values: map[string]string{"x": "allowed"}}, nil, nil
	}}
	r, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, release, err := r.Borrow(t.Context(), "s", owner, "")
	if err != nil {
		t.Fatal(err)
	}
	release()
	r.Retire("s")
	if err := r.Activate(t.Context(), "s", &session.Principal{Issuer: "issuer", Subject: "bob"}, ""); err == nil {
		t.Fatal("changed owner activated")
	}
	if err := r.Activate(t.Context(), "s", owner, "no-fs"); err == nil {
		t.Fatal("changed profile activated")
	}
	if binds.Load() != 1 {
		t.Fatal("mismatched scope reached binder")
	}
	revoked.Store(true)
	if err := r.Activate(t.Context(), "s", owner, ""); !errors.Is(err, denied) {
		t.Fatalf("activation=%v", err)
	}
	if _, _, err := r.Borrow(t.Context(), "s", owner, ""); err == nil {
		t.Fatal("failed activation removed retirement")
	}
	revoked.Store(false)
	if err := r.Activate(t.Context(), "s", owner, ""); err != nil {
		t.Fatal(err)
	}
	if binds.Load() != 3 {
		t.Fatal("activation did not retry fresh binding")
	}
}

func TestHarnessGenerationServiceReloadOwnerAuthorization(t *testing.T) {
	source := memfs.NewWorkspace("/logical")
	if _, err := source.CreateFile(t.Context(), ".mecatl/commands/x.md", []byte("selected")); err != nil {
		t.Fatal(err)
	}
	cfg := hcConfiguredFiles(t, source)
	cfg.OwnershipEnforced = true
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	alice := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser})
	s, err := built.Service.CreateSession(alice, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := built.Service.ListCommandsForSession(alice, s.ID); err != nil {
		t.Fatal(err)
	}
	built.Service.CloseSession(s.ID)
	if _, err := built.Service.LoadSession(bob, s.ID); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign reload=%v", err)
	}
	if _, err := built.Service.ListCommandsForSession(alice, s.ID); err == nil {
		t.Fatal("unauthorized reload reactivated source")
	}
	if _, err := built.Service.LoadSession(alice, s.ID); err != nil {
		t.Fatal(err)
	}
	commands, err := built.Service.ListCommandsForSession(alice, s.ID)
	if err != nil || len(commands) != 1 {
		t.Fatalf("authorized reload listing=%v,%v", commands, err)
	}
}
