package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

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
	resolver.Retire("s")
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
