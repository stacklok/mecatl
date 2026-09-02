package clientauth

import (
	"errors"
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

func TestADR_0290_AdditiveResourceRegistryIdentity(t *testing.T) {
	registry, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn := resourceConnection("https://api.example.com/v1", "grpc.example.com:7443", "https://issuer.example.com")
	if _, err := registry.Upsert(conn); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Find("https://api.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ResourceURL != "https://api.example.com/v1" || got.Identity.Target != "grpc.example.com:7443" {
		t.Fatalf("saved connection conflated resource and target: %#v", got)
	}
	if !got.Identity.Equal(conn.Identity) {
		t.Fatalf("credential identity changed: got %#v want %#v", got.Identity, conn.Identity)
	}
}

func TestADR_0290_RegistryLookupAmbiguity(t *testing.T) {
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

func TestADR_0290_SavedIdentityDrift(t *testing.T) {
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

func TestADR_0290_ResourceAliasLifecycle(t *testing.T) {
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

func TestADR_0290_LegacyResourceAliasPolicy(t *testing.T) {
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

func TestADR_0290_AliasConcurrency(t *testing.T) {
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
	target, err := registry.Find("grpc.example.com:7443")
	if err != nil || !resource.Identity.Equal(target.Identity) {
		t.Fatalf("aliases do not select one credential record: resource=%#v target=%#v err=%v", resource, target, err)
	}
}
