package subcred

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

func newTestStore(t *testing.T) (*Store, credentialstore.Store) {
	t.Helper()
	backing, err := credentialstore.NewMemoryBackend().Open(Namespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backing.Close() })
	store, err := New(backing)
	if err != nil {
		t.Fatal(err)
	}
	return store, backing
}

func sampleGrant() Grant {
	now := time.Now().UTC().Truncate(time.Second)
	return Grant{
		Provider:     ProviderAnthropic,
		AccessToken:  "access-canary",
		RefreshToken: "refresh-canary",
		ExpiresAt:    now.Add(time.Hour),
		AuthorizedAt: now,
		AccountID:    "acct-1",
		Email:        "user@example.test",
		OrgID:        "org-1",
		OrgName:      "Example Org",
	}
}

// A stored grant must round-trip every field, because a lost refresh token or
// anchor silently degrades renewal.
func TestGrantRoundTripsEveryField(t *testing.T) {
	store, _ := newTestStore(t)
	want := sampleGrant()
	if err := store.Save(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context(), ProviderAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Fatalf("tokens = %+v", got)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) || !got.AuthorizedAt.Equal(want.AuthorizedAt) {
		t.Fatalf("timestamps = %s / %s", got.ExpiresAt, got.AuthorizedAt)
	}
	if got.AccountID != want.AccountID || got.Email != want.Email {
		t.Fatalf("identity = %+v", got)
	}
	if got.OrgID != want.OrgID || got.OrgName != want.OrgName {
		t.Fatalf("organization = %+v", got)
	}
}

// Providers must not share a record; one sign-in cannot evict another.
func TestGrantsAreKeyedPerProvider(t *testing.T) {
	store, _ := newTestStore(t)
	anthropic := sampleGrant()
	codex := Grant{Provider: ProviderOpenAICodex, AccessToken: "codex-access", AccountID: "acct-codex"}
	for _, grant := range []Grant{anthropic, codex} {
		if err := store.Save(t.Context(), grant); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Load(t.Context(), ProviderOpenAICodex)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "codex-access" {
		t.Fatalf("codex grant = %+v", got)
	}
	if other, err := store.Load(t.Context(), ProviderAnthropic); err != nil || other.AccessToken != anthropic.AccessToken {
		t.Fatalf("anthropic grant was disturbed: %+v (%v)", other, err)
	}
}

// Repeated saves are what renewal does on every rotation, so a second write
// must replace the first rather than conflict with it.
func TestSaveReplacesExistingGrant(t *testing.T) {
	store, _ := newTestStore(t)
	first := sampleGrant()
	if err := store.Save(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		rotated := first
		rotated.AccessToken = fmt.Sprintf("access-%d", i)
		rotated.RefreshToken = fmt.Sprintf("refresh-%d", i)
		if err := store.Save(t.Context(), rotated); err != nil {
			t.Fatalf("rotation %d failed: %v", i, err)
		}
	}
	got, err := store.Load(t.Context(), ProviderAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "access-3" || got.RefreshToken != "refresh-3" {
		t.Fatalf("grant after rotation = %+v", got)
	}
}

// A separate process that rotated first leaves a version this handle has never
// seen. The write must still land rather than failing the request, because the
// value being written is the newer grant.
func TestSaveRecoversFromForeignRotation(t *testing.T) {
	store, backing := newTestStore(t)
	if err := store.Save(t.Context(), sampleGrant()); err != nil {
		t.Fatal(err)
	}

	// Simulate a peer writing underneath this handle.
	peer, err := New(backing)
	if err != nil {
		t.Fatal(err)
	}
	peerGrant := sampleGrant()
	peerGrant.AccessToken = "peer-access"
	if _, err := peer.Load(t.Context(), ProviderAnthropic); err != nil {
		t.Fatal(err)
	}
	if err := peer.Save(t.Context(), peerGrant); err != nil {
		t.Fatal(err)
	}

	ours := sampleGrant()
	ours.AccessToken = "our-access"
	if err := store.Save(t.Context(), ours); err != nil {
		t.Fatalf("write after a foreign rotation failed: %v", err)
	}
	got, err := store.Load(t.Context(), ProviderAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "our-access" {
		t.Fatalf("grant = %+v, want our write to have landed", got)
	}
}

// An absent grant must name the missing login, not surface a store internal.
func TestLoadReportsMissingGrant(t *testing.T) {
	store, _ := newTestStore(t)
	_, err := store.Load(t.Context(), ProviderAnthropic)
	if !errors.Is(err, ErrNoGrant) {
		t.Fatalf("error = %v, want ErrNoGrant", err)
	}
	if !strings.Contains(err.Error(), ProviderAnthropic) {
		t.Fatalf("error does not name the provider: %v", err)
	}
}

// A grant with no access token is unusable; storing it would mask the missing
// login behind a present-but-dead record.
func TestSaveRefusesEmptyAccessToken(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Save(t.Context(), Grant{Provider: ProviderAnthropic}); err == nil {
		t.Fatal("a grant with no access token was stored")
	}
}

// Logout must be idempotent, including against an empty store.
func TestDeleteIsIdempotent(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Delete(t.Context(), ProviderAnthropic); err != nil {
		t.Fatalf("delete on an empty store failed: %v", err)
	}
	if err := store.Save(t.Context(), sampleGrant()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.Delete(t.Context(), ProviderAnthropic); err != nil {
			t.Fatalf("delete failed: %v", err)
		}
	}
	if _, err := store.Load(t.Context(), ProviderAnthropic); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("grant survived delete: %v", err)
	}
}

// A grant re-saved after deletion must land: the retained version is stale and
// must not block a fresh sign-in.
func TestSaveAfterDeleteSucceeds(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Save(t.Context(), sampleGrant()); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), ProviderAnthropic); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), sampleGrant()); err != nil {
		t.Fatalf("re-login after logout failed: %v", err)
	}
	if _, err := store.Load(t.Context(), ProviderAnthropic); err != nil {
		t.Fatal(err)
	}
}

// An unreadable record must name the remedy instead of being silently treated
// as absent, which would hide a custody or corruption problem.
func TestLoadRejectsUnreadableRecord(t *testing.T) {
	store, backing := newTestStore(t)
	if _, err := backing.Put(t.Context(), []byte("grant/"+ProviderAnthropic), []byte("not json"), nil); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load(t.Context(), ProviderAnthropic)
	if err == nil || errors.Is(err, ErrNoGrant) {
		t.Fatalf("error = %v, want an unreadable-record error", err)
	}
	if !strings.Contains(err.Error(), "login again") {
		t.Fatalf("error lacks a remedy: %v", err)
	}
}

// A grant is a bearer secret plus identity; neither fmt nor slog may render
// any of it, including nested in another struct.
func TestGrantRedactsSecrets(t *testing.T) {
	grant := sampleGrant()
	nested := struct{ Grant Grant }{Grant: grant}
	for _, rendered := range []string{
		fmt.Sprintf("%v", grant),
		fmt.Sprintf("%s", grant),
		fmt.Sprintf("%+v", nested),
		grant.LogValue().String(),
	} {
		for _, secret := range []string{"access-canary", "refresh-canary", "user@example.test", "acct-1"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("rendering %q leaked %q", rendered, secret)
			}
		}
		if !strings.Contains(rendered, ProviderAnthropic) {
			t.Fatalf("rendering %q does not identify the provider", rendered)
		}
	}
}

// An empty provider is a programming error, not a key; it must be refused
// rather than writing a shared record.
func TestBlankProviderIsRefused(t *testing.T) {
	store, _ := newTestStore(t)
	if _, err := store.Load(t.Context(), "  "); err == nil {
		t.Fatal("a blank provider was loaded")
	}
	if err := store.Save(t.Context(), Grant{AccessToken: "a"}); err == nil {
		t.Fatal("a grant with no provider was stored")
	}
}
