package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

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
