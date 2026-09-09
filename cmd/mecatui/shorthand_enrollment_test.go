package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestOAuthProtectedResource_Scenario4_ShorthandEnrollment(t *testing.T) {
	originalDiscover := discoverRemoteResource
	originalConfirm := confirmDiscoveredEnrollment
	originalLogin := executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource = originalDiscover
		confirmDiscoveredEnrollment = originalConfirm
		executeRemoteLogin = originalLogin
	})
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) {
		return discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource", GRPCTarget: "api.example.com:443"}, Issuer: "https://ISSUER.example.com", Audience: "api", ClientID: "client", Scopes: []string{"api.read"}, ScopesPresent: true}, nil
	}
	confirmed := false
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) {
		confirmed = true
		return true, nil
	}
	var got clientauth.Connection
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool, _ clientauth.CredentialStoreMode) error {
		got = conn
		return nil
	}

	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if !confirmed || got.Identity.Target != "api.example.com:443" || got.Identity.Issuer != "https://ISSUER.example.com" || got.Identity.Audience != "api" || got.Identity.ClientID != "client" {
		t.Fatalf("shorthand enrollment = confirmed:%v connection:%#v", confirmed, got)
	}
	if got.IssuerAddressPolicy != clientauth.IssuerAddressPolicyPublic {
		t.Fatalf("shorthand issuer policy = %q, want public", got.IssuerAddressPolicy)
	}
	if gotScopes := strings.Join(got.Identity.Scopes, ","); gotScopes != "api.read" {
		t.Fatalf("shorthand scopes = %q", gotScopes)
	}
}

func TestDiscoveredLoginFirstEnrollmentConfirmsWithoutCreatingRegistry(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	originalDiscover, originalConfirm, originalLogin := discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin = originalDiscover, originalConfirm, originalLogin
	})
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) {
		return discoveredLoginFixture(), nil
	}
	confirmed := false
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) {
		confirmed = true
		if _, err := os.Stat(filepath.Join(xdg.ConfigHome, "mecatl")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("registry lookup created local state before confirmation: %v", err)
		}
		return true, nil
	}
	loggedIn := false
	executeRemoteLogin = func(context.Context, clientauth.Connection, bool, clientauth.CredentialStoreMode) error {
		loggedIn = true
		return nil
	}

	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if !confirmed || !loggedIn {
		t.Fatalf("first enrollment = confirmed:%v logged in:%v", confirmed, loggedIn)
	}
}

func TestDiscoveredLoginExistingEmptyRegistryConfirmsWithoutWriting(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	originalDiscover, originalConfirm, originalLogin := discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin = originalDiscover, originalConfirm, originalLogin
	})
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) {
		return discoveredLoginFixture(), nil
	}
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("registry lookup wrote local state before confirmation: %#v", entries)
		}
		return true, nil
	}
	executeRemoteLogin = func(context.Context, clientauth.Connection, bool, clientauth.CredentialStoreMode) error { return nil }

	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveredLoginCorruptRegistryConfirms(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clientauth-connections.json"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalDiscover, originalConfirm, originalLogin := discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin = originalDiscover, originalConfirm, originalLogin
	})
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) {
		return discoveredLoginFixture(), nil
	}
	confirmed := false
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) {
		confirmed = true
		return true, nil
	}
	loggedIn := false
	executeRemoteLogin = func(context.Context, clientauth.Connection, bool, clientauth.CredentialStoreMode) error {
		loggedIn = true
		return nil
	}

	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if !confirmed || !loggedIn {
		t.Fatalf("corrupt registry = confirmed:%v logged in:%v", confirmed, loggedIn)
	}
}

func TestDiscoveredLoginMatchingSavedEnrollmentSkipsConfirmation(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	originalDiscover, originalConfirm, originalLogin := discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin = originalDiscover, originalConfirm, originalLogin
	})
	discovered := discoveredLoginFixture()
	enrollment, err := discoveredEnrollmentFrom(discovered, "", []string{"api.read"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := clientauth.OpenRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Upsert(enrollment.Connection); err != nil {
		t.Fatal(err)
	}
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) { return discovered, nil }
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) {
		t.Fatal("matching saved discovery requested confirmation")
		return false, nil
	}
	loggedIn := false
	executeRemoteLogin = func(context.Context, clientauth.Connection, bool, clientauth.CredentialStoreMode) error {
		loggedIn = true
		return nil
	}

	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if !loggedIn {
		t.Fatal("matching saved discovery did not continue to login")
	}
}

func TestDiscoveredLoginChangedEnrollmentConfirms(t *testing.T) {
	mutations := map[string]func(*clientauth.Connection){
		"resource":              func(c *clientauth.Connection) { c.ResourceURL = "https://other.example.com" },
		"target":                func(c *clientauth.Connection) { c.Identity.Target = "other.example.com:443" },
		"issuer":                func(c *clientauth.Connection) { c.Identity.Issuer = "https://other-issuer.example.com" },
		"client ID":             func(c *clientauth.Connection) { c.Identity.ClientID = "other-client" },
		"audience":              func(c *clientauth.Connection) { c.Identity.Audience = "other-audience" },
		"redirect URI":          func(c *clientauth.Connection) { c.Identity.RedirectURI = "http://127.0.0.1:18473/other" },
		"scopes":                func(c *clientauth.Connection) { c.Identity.Scopes = []string{"other.read"} },
		"issuer CA":             func(c *clientauth.Connection) { c.IssuerCAFile = filepath.Join(xdg.ConfigHome, "issuer-ca.pem") },
		"issuer address policy": func(c *clientauth.Connection) { c.IssuerAddressPolicy = clientauth.IssuerAddressPolicyPrivate },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			oldConfigHome := xdg.ConfigHome
			xdg.ConfigHome = t.TempDir()
			t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
			discovered := discoveredLoginFixture()
			enrollment, err := discoveredEnrollmentFrom(discovered, "", []string{"api.read"})
			if err != nil {
				t.Fatal(err)
			}
			saved := enrollment.Connection
			mutate(&saved)
			registry, err := clientauth.OpenRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Upsert(saved); err != nil {
				t.Fatal(err)
			}
			originalDiscover, originalConfirm, originalLogin := discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin
			t.Cleanup(func() {
				discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin = originalDiscover, originalConfirm, originalLogin
			})
			discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) { return discovered, nil }
			confirmed := false
			confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) {
				confirmed = true
				return true, nil
			}
			executeRemoteLogin = func(context.Context, clientauth.Connection, bool, clientauth.CredentialStoreMode) error { return nil }
			if err := runRemoteLogin("api.example.com", nil); err != nil {
				t.Fatal(err)
			}
			if !confirmed {
				t.Fatalf("changed %s did not request confirmation", name)
			}
		})
	}
}

func discoveredLoginFixture() discoveredResource {
	return discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api", ClientID: "client", Scopes: []string{"api.read"}, ScopesPresent: true}
}

func TestADR_0305_ResourceTargetSeparation(t *testing.T) {
	enrollment, err := discoveredEnrollmentFrom(discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com/service/v1", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource/service/v1", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api", ClientID: "client", Scopes: []string{"api.read"}, ScopesPresent: true}, "grpc.example.com:7443", []string{"api.read"})
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Connection.Identity.Target != "grpc.example.com:7443" || enrollment.Resource != "https://api.example.com/service/v1" {
		t.Fatalf("resource and target were conflated: %#v", enrollment)
	}
}

func TestInvariant_oauth_three_transport_trust_split(t *testing.T) {
	enrollment, err := discoveredEnrollmentFrom(discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api", ClientID: "client", Scopes: []string{"api.read"}, ScopesPresent: true}, "", []string{"api.read"})
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Connection.IssuerCAFile != "" || enrollment.Connection.IssuerAddressPolicy != clientauth.IssuerAddressPolicyPublic {
		t.Fatalf("discovery inherited issuer trust override: %#v", enrollment.Connection)
	}
	grpc := client.DialConfig{Server: enrollment.Connection.Identity.Target, TLSCAFile: "grpc-ca.pem"}
	if err := applySavedRemoteTLSPolicy(config{}, &grpc); err != nil {
		t.Fatal(err)
	}
	if grpc.TLSCAFile != "grpc-ca.pem" || enrollment.Connection.IssuerCAFile != "" {
		t.Fatalf("gRPC CA leaked into issuer/discovery trust: grpc=%#v issuer=%#v", grpc, enrollment.Connection)
	}
}

func TestADR_0305_DiscoveredIdentityConfirmation(t *testing.T) {
	originalDiscover := discoverRemoteResource
	originalConfirm := confirmDiscoveredEnrollment
	originalLogin := executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource = originalDiscover
		confirmDiscoveredEnrollment = originalConfirm
		executeRemoteLogin = originalLogin
	})
	discovered := discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api", ClientID: "client", Scopes: []string{"api.read"}, ScopesPresent: true}
	discoveryCalls := 0
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) {
		discoveryCalls++
		return discovered, nil
	}
	confirmDiscoveredEnrollment = func(_ io.Reader, _ io.Writer, enrollment discoveredEnrollment) (bool, error) {
		if enrollment.Resource != discovered.Resource || enrollment.MetadataURL != discovered.MetadataURL {
			t.Fatalf("confirmation did not display discovered tuple: %#v", enrollment)
		}
		return false, nil
	}
	called := false
	executeRemoteLogin = func(context.Context, clientauth.Connection, bool, clientauth.CredentialStoreMode) error {
		called = true
		return nil
	}
	if err := runRemoteLogin("api.example.com", nil); err == nil || called {
		t.Fatalf("rejected confirmation = err %v, login called %v", err, called)
	}

	enrollment, err := discoveredEnrollmentFrom(discovered, "", []string{"api.read"})
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	accepted, err := confirmDiscoveredLogin(strings.NewReader(""), &output, enrollment)
	if err != nil || accepted {
		t.Fatalf("EOF confirmation = accepted:%v err:%v", accepted, err)
	}
	for _, value := range []string{enrollment.Resource, enrollment.MetadataURL, enrollment.Connection.Identity.Issuer, enrollment.Connection.Identity.Audience, enrollment.Connection.Identity.ClientID, strings.Join(enrollment.Connection.Identity.Scopes, ","), enrollment.Connection.Identity.Target} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("confirmation output omitted %q: %s", value, output.String())
		}
	}

	confirmed := enrollment
	discoveryCalls = 0
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) { return true, nil }
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool, _ clientauth.CredentialStoreMode) error {
		if discoveryCalls != 1 {
			t.Fatalf("discovery calls before authorization = %d, want one", discoveryCalls)
		}
		if !reflect.DeepEqual(conn, confirmed.Connection) {
			t.Fatalf("authorization/enrollment input changed after confirmation: got %#v want %#v", conn, confirmed.Connection)
		}
		return nil
	}
	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
}

func TestADR_0305_DiscoveredScopeSelection(t *testing.T) {
	profile := discoveredResource{ScopesPresent: true, Scopes: []string{"profile", "api.read"}}
	scopes, err := discoveredScopes(profile, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(scopes, ","), "api.read,profile"; got != want {
		t.Fatalf("advertised scopes = %q, want %q", got, want)
	}
	if _, err := discoveredScopes(profile, true); !errors.Is(err, errDiscoveryRejected) {
		t.Fatalf("discovery explicit scope error = %v, want rejection", err)
	}
}

func TestMecatuiServerOwnedDiscoveryScopes_Scenario1_OmittedMetadataUsesBaseline(t *testing.T) {
	originalDiscover, originalConfirm, originalLogin := discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin = originalDiscover, originalConfirm, originalLogin
	})
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) {
		return discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api", ClientID: "client"}, nil
	}
	var confirmed, loggedIn clientauth.Connection
	confirmDiscoveredEnrollment = func(_ io.Reader, _ io.Writer, enrollment discoveredEnrollment) (bool, error) {
		confirmed = enrollment.Connection
		return true, nil
	}
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool, _ clientauth.CredentialStoreMode) error {
		loggedIn = conn
		return nil
	}

	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"offline_access", "openid", "profile"}
	if !reflect.DeepEqual(confirmed.Identity.Scopes, want) {
		t.Fatalf("confirmation scopes = %#v, want %#v", confirmed.Identity.Scopes, want)
	}
	if !reflect.DeepEqual(loggedIn.Identity.Scopes, want) {
		t.Fatalf("login scopes = %#v, want %#v", loggedIn.Identity.Scopes, want)
	}
}

func TestMecatuiServerOwnedDiscoveryScopes_Scenario1_RejectsDiscoveryScopesFlag(t *testing.T) {
	originalDiscover, originalConfirm, originalLogin := discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin = originalDiscover, originalConfirm, originalLogin
	})
	discovered := false
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) {
		discovered = true
		return discoveredResource{}, nil
	}
	confirmed := false
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) { confirmed = true; return true, nil }
	loggedIn := false
	executeRemoteLogin = func(context.Context, clientauth.Connection, bool, clientauth.CredentialStoreMode) error {
		loggedIn = true
		return nil
	}

	err := runRemoteLogin("api.example.com", []string{"--scopes", "api.read"})
	if err == nil || !strings.Contains(err.Error(), "--scopes is only valid with explicit") {
		t.Fatalf("discovery --scopes error = %v", err)
	}
	if discovered || confirmed || loggedIn {
		t.Fatalf("discovery flag started discovery:%v confirmation:%v login:%v", discovered, confirmed, loggedIn)
	}
}

// TestADR_0305_DiscoveredScopeRejectsCommaSmuggling pins the fix for a
// discovered scope value containing a literal comma: it must be rejected, not
// silently split into two bogus scopes when later CSV-joined and re-split by
// discoveredEnrollmentFrom.
func TestADR_0305_DiscoveredScopeRejectsCommaSmuggling(t *testing.T) {
	profile := discoveredResource{ScopesPresent: true, Scopes: []string{"api.read", "smuggled,scope"}}
	if _, err := discoveredScopes(profile, false); err == nil {
		t.Fatal("discovered scope containing a comma was accepted")
	}
}

func TestADR_0277_ExplicitEnrollmentCompatibility(t *testing.T) {
	original := executeRemoteLogin
	t.Cleanup(func() { executeRemoteLogin = original })
	marker := errors.New("captured")
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool, _ clientauth.CredentialStoreMode) error {
		if conn.Identity.Target != "remote.example:443" || conn.IssuerAddressPolicy != clientauth.IssuerAddressPolicyPrivate || conn.Identity.Scopes[0] != "custom" {
			t.Fatalf("explicit enrollment changed: %#v", conn)
		}
		return marker
	}
	err := runRemoteLogin("remote.example:443", []string{"--issuer", "https://issuer.example", "--client-id", "client", "--audience", "audience", "--tls-ca", "ca.pem", "--private-issuer", "--scopes", "custom"})
	if !errors.Is(err, marker) {
		t.Fatalf("explicit enrollment error = %v", err)
	}
}

func TestOAuthProtectedResource_Scenario6_EndToEnd(t *testing.T) {
	originalDiscover, originalConfirm, originalLogin := discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin = originalDiscover, originalConfirm, originalLogin
	})
	profile := server.ProtectedResourceProfile{Resource: "https://api.example.com/mcp", Issuer: "https://issuer.example.com", Audience: "api://mecatl", ClientID: "mecatui", Scopes: []string{"openid", "api.read"}}
	metadata := server.NewProtectedResourceHandler(profile)
	discoverRemoteResource = func(ctx context.Context, resource protectedResource) (discoveredResource, error) {
		return discoverProtectedResource(ctx, resource, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "openid-configuration") {
				return jsonResponse(`{"issuer":"https://issuer.example.com"}`), nil
			}
			rec := httptest.NewRecorder()
			metadata.ServeHTTP(rec, req)
			return rec.Result(), nil
		}))
	}
	confirmed := false
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) { confirmed = true; return true, nil }
	var received clientauth.Connection
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool, _ clientauth.CredentialStoreMode) error {
		received = conn
		return nil
	}
	if err := runRemoteLogin("https://api.example.com/mcp", nil); err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("metadata confirmation was not required")
	}
	if received.ResourceURL != profile.Resource || received.Identity.Target != "api.example.com:443" || received.Identity.Issuer != profile.Issuer || received.Identity.ClientID != profile.ClientID {
		t.Fatalf("login handoff did not preserve discovered tuple: %#v", received)
	}
	if received.IssuerAddressPolicy != clientauth.IssuerAddressPolicyPublic {
		t.Fatalf("issuer policy = %q", received.IssuerAddressPolicy)
	}
}

func TestADR_0305_ProviderCompatibility(t *testing.T) {
	identity := reflect.TypeOf(clientauth.Identity{})
	for _, name := range []string{"Resource", "TokenFormat", "OpaqueToken"} {
		if _, found := identity.FieldByName(name); found {
			t.Fatalf("OIDC identity unexpectedly supports %s; RFC 8707 and opaque tokens are not part of this profile", name)
		}
	}
}
