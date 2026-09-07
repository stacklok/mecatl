package clientauth

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

func resourceConnection(resource, target, issuer string) Connection {
	return Connection{
		Identity: Identity{
			Target: target, Issuer: issuer, ClientID: "client", Audience: "audience",
			RedirectURI: "http://127.0.0.1/oauth/callback", Scopes: []string{"openid"},
		},
		ResourceURL: resource,
	}
}

func writeConnectionsForTest(t *testing.T, registry *Registry, connections ...Connection) {
	t.Helper()
	rows := make([]registryRow, 0, len(connections))
	for _, conn := range connections {
		canonical, err := normalizeConnection(conn)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, registryRow{connection: canonical, valid: true})
	}
	if err := registry.writeRows(rows); err != nil {
		t.Fatal(err)
	}
}

func TestADR_0305_AdditiveResourceRegistryIdentity(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn := resourceConnection("https://api.example.com", "grpc.example.com:7443", "https://issuer.example.com")
	if _, err := registry.Upsert(conn); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Find("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.ResourceURL != "https://api.example.com" || got.Identity.Target != "grpc.example.com:7443" {
		t.Fatalf("saved connection conflated resource and target: %#v", got)
	}
	if !got.Identity.Equal(conn.Identity) {
		t.Fatalf("credential identity changed: got %#v want %#v", got.Identity, conn.Identity)
	}
	byHostname, err := registry.Find("api.example.com")
	if err != nil || !byHostname.Identity.Equal(conn.Identity) {
		t.Fatalf("bare resource alias = %#v, %v", byHostname, err)
	}
	moved := resourceConnection("https://api.example.com", "other.example.com:7443", "https://issuer-two.example.com")
	if _, err := registry.Upsert(moved); err != nil {
		t.Fatal(err)
	}
	got, err = registry.Find("https://api.example.com")
	if err != nil || !got.Identity.Equal(moved.Identity) {
		t.Fatalf("resource alias did not move atomically: %#v, %v", got, err)
	}
	if _, err := registry.Find("grpc.example.com:7443"); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("superseded target remains reachable: %v", err)
	}
}

func TestADR_0305_RegistryLookupAmbiguity(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := resourceConnection("https://one.example.com", "shared.example.com:443", "https://issuer-one.example.com")
	second := resourceConnection("https://two.example.com", "shared.example.com:443", "https://issuer-two.example.com")
	writeConnectionsForTest(t, registry, first, second)
	if got, err := registry.Find("https://one.example.com"); err != nil || !got.Identity.Equal(first.Identity) {
		t.Fatalf("resource lookup = %#v, %v", got, err)
	}
	if _, err := registry.Find("shared.example.com:443"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("ambiguous target error = %v, want ErrCorrupt", err)
	}
}

func TestADR_0277_LegacyRegistryCompatibility(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacy := resourceConnection("", "legacy.example.com:443", "https://issuer.example.com")
	if _, err := registry.Upsert(legacy); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Find("legacy.example.com:443")
	if err != nil || got.ResourceURL != "" || !got.Identity.Equal(legacy.Identity) {
		t.Fatalf("legacy lookup = %#v, %v", got, err)
	}
	if _, err := registry.Find("https://legacy.example.com"); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("legacy resource alias = %v, want not found", err)
	}
}

func TestADR_0305_SavedIdentityDrift(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	confirmed := resourceConnection("https://api.example.com", "grpc.example.com:7443", "https://issuer.example.com")
	if _, err := registry.Upsert(confirmed); err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	if _, err := creds.Upsert(t.Context(), confirmed.Identity, Token{AccessToken: "access", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Find("https://api.example.com")
	if err != nil || !sameConnections([]Connection{got}, []Connection{confirmed}) {
		t.Fatalf("saved tuple changed without enrollment: %#v, %v", got, err)
	}
	if _, err := creds.Load(t.Context(), got.Identity); err != nil {
		t.Fatalf("saved resource metadata made credential unreachable: %v", err)
	}
}

func TestADR_0305_ResourceAliasLifecycle(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn := resourceConnection("https://api.example.com/service", "grpc.example.com:7443", "https://issuer.example.com")
	if _, err := registry.Upsert(conn); err != nil {
		t.Fatal(err)
	}
	byResource, err := registry.Find("https://api.example.com/service")
	if err != nil {
		t.Fatal(err)
	}
	byTarget, err := registry.Find("grpc.example.com:7443")
	if err != nil || !sameConnections([]Connection{byResource}, []Connection{byTarget}) {
		t.Fatalf("aliases resolved differently: resource=%#v target=%#v err=%v", byResource, byTarget, err)
	}
	if _, err := registry.DeleteTarget(byResource.Identity.Target, []Connection{byResource}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Find("https://api.example.com/service"); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("deleted resource alias remains reachable: %v", err)
	}

	// Logout resolves the resource alias to the same target-locked record before
	// applying ADR 0277's credential and registry CAS cleanup.
	creds := credentials(t)
	if _, err := registry.Upsert(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), conn.Identity, Token{AccessToken: "access", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	result, err := Logout(t.Context(), conn.ResourceURL, LogoutConfig{Registry: registry, Credentials: creds})
	if err != nil || !result.RegistryDeleted || result.Entries != 1 {
		t.Fatalf("resource logout = %#v, %v", result, err)
	}
}

func TestADR_0305_LogoutRevalidatesMovedResourceAlias(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	first := resourceConnection("https://api.example.com", "one.example.com:7443", "https://issuer.example.com")
	second := resourceConnection("https://api.example.com", "two.example.com:7443", "https://issuer.example.com")
	if _, err := registry.Upsert(first); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Upsert(t.Context(), first.Identity, Token{AccessToken: "one", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	// Move the resource after Logout resolves its initial target but before it
	// acquires the resource transaction lock. This is the cross-target mutation
	// a second process can make in that gap.
	moved := false
	registry.targetLockAttempt = func() {
		if moved {
			return
		}
		moved = true
		registry.targetLockAttempt = nil
		if _, upsertErr := registry.Upsert(second); upsertErr != nil {
			t.Fatalf("concurrent resource move: %v", upsertErr)
		}
	}
	_, err = Logout(t.Context(), first.ResourceURL, LogoutConfig{Registry: registry, Credentials: creds})
	if !errors.Is(err, credentialstore.ErrConflict) {
		t.Fatalf("Logout after alias move = %v, want conflict", err)
	}
	got, err := registry.Find(first.ResourceURL)
	if err != nil || !got.Identity.Equal(second.Identity) {
		t.Fatalf("moved resource was not retained: %#v, %v", got, err)
	}
}

func TestADR_0305_LegacyResourceAliasPolicy(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacy := resourceConnection("", "legacy.example.com:443", "https://issuer.example.com")
	if _, err := registry.Upsert(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Find("https://legacy.example.com"); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("legacy row acquired an unconfirmed resource alias: %v", err)
	}
	got, err := registry.Find("legacy.example.com:443")
	if err != nil || got.ResourceURL != "" {
		t.Fatalf("legacy target lookup = %#v, %v", got, err)
	}
}

func TestADR_0305_EnrollResourceAliasDisplacesAcrossTargets(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	first := resourceConnection("https://api.example.com", "one.example.com:7443", "https://issuer.example.com")
	second := resourceConnection("https://api.example.com/", "two.example.com:7443", "https://issuer.example.com")
	if err := Enroll(t.Context(), first, Token{AccessToken: "one", TokenType: "Bearer"}, EnrollmentConfig{Registry: registry, Credentials: creds}); err != nil {
		t.Fatal(err)
	}
	if err := Enroll(t.Context(), second, Token{AccessToken: "two", TokenType: "Bearer"}, EnrollmentConfig{Registry: registry, Credentials: creds}); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Find("https://API.example.com")
	if err != nil || got.Identity.Target != second.Identity.Target {
		t.Fatalf("resource alias winner = %#v, %v", got, err)
	}
	if _, err := creds.Load(t.Context(), first.Identity); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("displaced credential remains: %v", err)
	}
}

func TestADR_0305_EnrollResourceAliasPreservesUnrelatedConnections(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	first := resourceConnection("https://api.example.com", "one.example.com:7443", "https://issuer.example.com")
	unrelated := resourceConnection("https://other.example.com", "other.example.com:7443", "https://issuer.example.com")
	if err := Enroll(t.Context(), first, Token{AccessToken: "one", TokenType: "Bearer"}, EnrollmentConfig{Registry: registry, Credentials: creds}); err != nil {
		t.Fatal(err)
	}
	if err := Enroll(t.Context(), unrelated, Token{AccessToken: "other", TokenType: "Bearer"}, EnrollmentConfig{Registry: registry, Credentials: creds}); err != nil {
		t.Fatal(err)
	}
	replacement := resourceConnection("https://api.example.com/", "two.example.com:7443", "https://issuer.example.com")
	if err := Enroll(t.Context(), replacement, Token{AccessToken: "two", TokenType: "Bearer"}, EnrollmentConfig{Registry: registry, Credentials: creds}); err != nil {
		t.Fatal(err)
	}
	if got, err := registry.Find(unrelated.ResourceURL); err != nil || !got.Identity.Equal(unrelated.Identity) {
		t.Fatalf("unrelated registry entry = %#v, %v", got, err)
	}
	if got, err := creds.Load(t.Context(), unrelated.Identity); err != nil || got.Token.AccessToken != "other" {
		t.Fatalf("unrelated credential = %#v, %v", got, err)
	}
	if rows, err := registry.List(); err != nil || len(rows) != 2 {
		t.Fatalf("registry rows = %#v, %v", rows, err)
	}
}

func TestADR_0305_ConcurrentCrossTargetResourceEnrollmentSerializes(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for n, target := range []string{"one.example.com:7443", "two.example.com:7443"} {
		n, target := n, target
		go func() {
			<-start
			conn := resourceConnection("https://api.example.com/", target, "https://issuer.example.com")
			errs <- Enroll(t.Context(), conn, Token{AccessToken: fmt.Sprintf("token-%d", n), TokenType: "Bearer"}, EnrollmentConfig{Registry: registry, Credentials: creds})
		}()
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, err := registry.Find("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Identity.Target != "one.example.com:7443" && got.Identity.Target != "two.example.com:7443" {
		t.Fatalf("unexpected concurrent winner: %#v", got)
	}
	if rows, err := registry.List(); err != nil || len(rows) != 1 {
		t.Fatalf("resource enrollment rows = %#v, %v", rows, err)
	}
}

func TestADR_0305_AliasConcurrency(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn := resourceConnection("https://api.example.com", "grpc.example.com:7443", "https://issuer.example.com")
	if _, err := registry.Upsert(conn); err != nil {
		t.Fatal(err)
	}
	resource, err := registry.Find("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	const readers = 16
	start := make(chan struct{})
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for n := 0; n < readers; n++ {
		wg.Add(1)
		go func(alias string) {
			defer wg.Done()
			<-start
			for range 50 {
				got, findErr := registry.Find(alias)
				if findErr != nil {
					errs <- findErr
					return
				}
				if !got.Identity.Equal(conn.Identity) {
					errs <- errors.New("alias resolved to another identity")
					return
				}
			}
		}(map[bool]string{true: "https://api.example.com", false: "grpc.example.com:7443"}[n%2 == 0])
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent alias lookup failed: %v", err)
	}
	target, err := registry.Find("grpc.example.com:7443")
	if err != nil || !resource.Identity.Equal(target.Identity) {
		t.Fatalf("aliases do not select one credential record: resource=%#v target=%#v err=%v", resource, target, err)
	}
}
