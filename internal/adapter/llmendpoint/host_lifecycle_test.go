package llmendpoint_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
)

type replacingStore struct {
	credentialstore.Store
	replaced bool
}

func (s *replacingStore) Delete(ctx context.Context, key []byte, expected credentialstore.Version) error {
	if !s.replaced {
		record, err := s.Get(ctx, key)
		if err != nil {
			return err
		}
		if _, err := s.Put(ctx, key, record.Value, &record.Version); err != nil {
			return err
		}
		s.replaced = true
	}
	return s.Store.Delete(ctx, key, expected)
}

func TestInvariant_native_endpoint_source_requires_exact_record_before_availability(t *testing.T) {
	backend := credentialstore.NewMemoryBackend()
	store, _ := backend.Open(llmendpoint.CredentialNamespace)
	repo := llmendpoint.NewCredentialRepository(store)
	id := testIdentity()
	source := &llmendpoint.LifecycleSource{Identity: id, Repository: repo, Lifecycle: llmendpoint.Lifecycle{Repository: repo, Locker: llmendpoint.MemoryLocker()}}
	if err := source.Validate(t.Context()); !errors.Is(err, llmendpoint.ErrNotEnrolled) {
		t.Fatalf("missing exact record validated: %v", err)
	}
	if _, err := repo.Save(t.Context(), id, llmendpoint.Token{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := source.Validate(t.Context()); err != nil {
		t.Fatalf("exact record rejected: %v", err)
	}
	drift := id
	drift.ResourceAudience = "other"
	source.Identity = drift
	if err := source.Validate(t.Context()); !errors.Is(err, llmendpoint.ErrNotEnrolled) {
		t.Fatalf("identity-drifted record validated: %v", err)
	}
}

func TestNativeLLMGatewayLogin_Scenario5_LocalStatusIsPassive(t *testing.T) {
	backend := credentialstore.NewMemoryBackend()
	store, err := backend.Open(llmendpoint.CredentialNamespace)
	if err != nil {
		t.Fatal(err)
	}
	repo := llmendpoint.NewCredentialRepository(store)
	id := testIdentity()
	now := time.Unix(1_700_000_000, 0)
	calls := 0
	lifecycle := llmendpoint.Lifecycle{
		Repository: repo,
		Locker:     llmendpoint.MemoryLocker(),
		Authorize: func(context.Context) (llmendpoint.Token, error) {
			calls++
			return llmendpoint.Token{}, errors.New("must not authorize")
		},
		Exchange: func(context.Context, string) (llmendpoint.Token, error) {
			calls++
			return llmendpoint.Token{}, errors.New("must not refresh")
		},
	}
	if got := lifecycle.Status(t.Context(), id, now); got != llmendpoint.StatusNotEnrolled {
		t.Fatalf("absent status = %q", got)
	}
	if _, err := repo.Save(t.Context(), id, llmendpoint.Token{AccessToken: "access-canary", RefreshToken: "refresh-canary", TokenType: "Bearer", Expiry: now.Add(time.Hour)}, nil); err != nil {
		t.Fatal(err)
	}
	if got := lifecycle.Status(t.Context(), id, now); got != llmendpoint.StatusUsable {
		t.Fatalf("usable status = %q", got)
	}
	if calls != 0 {
		t.Fatalf("passive status invoked active lifecycle %d times", calls)
	}
}

func TestNativeLLMGatewayLogin_Scenario5_LogoutDeleteThenRevoke(t *testing.T) {
	backend := credentialstore.NewMemoryBackend()
	store, err := backend.Open(llmendpoint.CredentialNamespace)
	if err != nil {
		t.Fatal(err)
	}
	repo := llmendpoint.NewCredentialRepository(store)
	id := testIdentity()
	old := llmendpoint.Token{AccessToken: "old-access", RefreshToken: "old-refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	if _, err := repo.Save(t.Context(), id, old, nil); err != nil {
		t.Fatal(err)
	}
	deletedBeforeRevoke := false
	lifecycle := llmendpoint.Lifecycle{Repository: repo, Locker: llmendpoint.MemoryLocker()}
	err = lifecycle.Logout(t.Context(), id, func(ctx context.Context, got llmendpoint.Token) error {
		if got.AccessToken != old.AccessToken || got.RefreshToken != old.RefreshToken {
			t.Fatalf("revocation token mismatch")
		}
		_, loadErr := repo.Load(ctx, id)
		deletedBeforeRevoke = errors.Is(loadErr, credentialstore.ErrNotFound)
		return errors.New("provider unavailable")
	})
	if err != nil {
		t.Fatalf("best-effort revoke made logout fail: %v", err)
	}
	if !deletedBeforeRevoke {
		t.Fatal("revocation ran before authoritative local deletion")
	}
	if _, err := repo.Load(t.Context(), id); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("revocation failure restored local state: %v", err)
	}

	base, err := backend.Open(llmendpoint.CredentialNamespace)
	if err != nil {
		t.Fatal(err)
	}
	racing := &replacingStore{Store: base}
	concurrentRepo := llmendpoint.NewCredentialRepository(racing)
	if _, err := concurrentRepo.Save(t.Context(), id, old, nil); err != nil {
		t.Fatal(err)
	}
	concurrent := llmendpoint.Lifecycle{Repository: concurrentRepo, Locker: llmendpoint.MemoryLocker()}
	if err := concurrent.Logout(t.Context(), id, func(context.Context, llmendpoint.Token) error {
		t.Fatal("revoke ran after conditional delete lost a race")
		return nil
	}); !errors.Is(err, credentialstore.ErrConflict) {
		t.Fatalf("concurrent replacement logout = %v, want conflict", err)
	}
	if _, err := concurrentRepo.Load(t.Context(), id); err != nil {
		t.Fatalf("concurrent replacement did not survive: %v", err)
	}
}
