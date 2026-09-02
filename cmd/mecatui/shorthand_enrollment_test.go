package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
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
		return discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api", ClientID: "client", Scopes: []string{"api.read"}}, nil
	}
	confirmed := false
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) {
		confirmed = true
		return true, nil
	}
	var got clientauth.Connection
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error { got = conn; return nil }

	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if !confirmed || got.Identity.Target != "api.example.com:443" || got.Identity.Issuer != "https://issuer.example.com" || got.Identity.Audience != "api" || got.Identity.ClientID != "client" {
		t.Fatalf("shorthand enrollment = confirmed:%v connection:%#v", confirmed, got)
	}
	if got.IssuerAddressPolicy != clientauth.IssuerAddressPolicyPublic {
		t.Fatalf("shorthand issuer policy = %q, want public", got.IssuerAddressPolicy)
	}
	if gotScopes := strings.Join(got.Identity.Scopes, ","); gotScopes != "api.read,offline_access,openid,profile" {
		t.Fatalf("shorthand scopes = %q", gotScopes)
	}
}

func TestADR_0290_ResourceTargetSeparation(t *testing.T) {
	enrollment, err := discoveredEnrollmentFrom(discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com/service/v1", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource/service/v1", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api", ClientID: "client"}, "grpc.example.com:7443", "")
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Connection.Identity.Target != "grpc.example.com:7443" || enrollment.Resource != "https://api.example.com/service/v1" {
		t.Fatalf("resource and target were conflated: %#v", enrollment)
	}
}

func TestInvariant_oauth_three_transport_trust_split(t *testing.T) {
	enrollment, err := discoveredEnrollmentFrom(discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api", ClientID: "client"}, "", "")
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

func TestADR_0290_DiscoveredIdentityConfirmation(t *testing.T) {
	originalDiscover := discoverRemoteResource
	originalConfirm := confirmDiscoveredEnrollment
	originalLogin := executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource = originalDiscover
		confirmDiscoveredEnrollment = originalConfirm
		executeRemoteLogin = originalLogin
	})
	discovered := discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api", ClientID: "client", Scopes: []string{"api.read"}}
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) { return discovered, nil }
	confirmDiscoveredEnrollment = func(_ io.Reader, _ io.Writer, enrollment discoveredEnrollment) (bool, error) {
		if enrollment.Resource != discovered.Resource || enrollment.MetadataURL != discovered.MetadataURL {
			t.Fatalf("confirmation did not display discovered tuple: %#v", enrollment)
		}
		return false, nil
	}
	called := false
	executeRemoteLogin = func(context.Context, clientauth.Connection, bool) error { called = true; return nil }
	if err := runRemoteLogin("api.example.com", nil); err == nil || called {
		t.Fatalf("rejected confirmation = err %v, login called %v", err, called)
	}

	enrollment, err := discoveredEnrollmentFrom(discovered, "", "openid,profile,offline_access,api.read")
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
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) { return true, nil }
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error {
		if !reflect.DeepEqual(conn, confirmed.Connection) {
			t.Fatalf("authorization/enrollment input changed after confirmation: got %#v want %#v", conn, confirmed.Connection)
		}
		return nil
	}
	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
}

func TestADR_0290_DiscoveredScopeSelection(t *testing.T) {
	profile := discoveredResource{Scopes: []string{"api.read", "profile"}}
	scopes, err := discoveredScopes(profile, "api.write", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(scopes, ","), "api.write,offline_access,openid,profile"; got != want {
		t.Fatalf("explicit scopes = %q, want %q", got, want)
	}
	scopes, err = discoveredScopes(profile, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(scopes, ","), "api.read,offline_access,openid,profile"; got != want {
		t.Fatalf("profile scopes = %q, want %q", got, want)
	}
}

func TestADR_0277_ExplicitEnrollmentCompatibility(t *testing.T) {
	original := executeRemoteLogin
	t.Cleanup(func() { executeRemoteLogin = original })
	marker := errors.New("captured")
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error {
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
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	originalDiscover, originalConfirm, originalLogin := discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin
	t.Cleanup(func() {
		discoverRemoteResource, confirmDiscoveredEnrollment, executeRemoteLogin = originalDiscover, originalConfirm, originalLogin
	})
	profile := discoveredResource{protectedResource: protectedResource{Resource: "https://api.example.com/mcp", MetadataURL: "https://api.example.com/.well-known/oauth-protected-resource/mcp", GRPCTarget: "api.example.com:443"}, Issuer: "https://issuer.example.com", Audience: "api://mecatl", ClientID: "mecatui", Scopes: []string{"openid", "api.read"}}
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) { return profile, nil }
	confirmed := false
	confirmDiscoveredEnrollment = func(io.Reader, io.Writer, discoveredEnrollment) (bool, error) { confirmed = true; return true, nil }
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error {
		registry, err := clientauth.OpenRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
		if err != nil {
			return err
		}
		_, err = registry.Upsert(conn)
		return err
	}
	if err := runRemoteLogin("api.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("metadata confirmation was not required")
	}
	conn, err := savedConnection("https://api.example.com/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if conn.ResourceURL != profile.Resource || conn.Identity.Target != "api.example.com:443" || conn.Identity.Issuer != profile.Issuer || conn.Identity.ClientID != profile.ClientID {
		t.Fatalf("persisted confirmed connection = %#v", conn)
	}
	if conn.IssuerAddressPolicy != clientauth.IssuerAddressPolicyPublic {
		t.Fatalf("issuer policy = %q", conn.IssuerAddressPolicy)
	}
}

func TestADR_0290_ProviderCompatibility(t *testing.T) {
	identity := reflect.TypeOf(clientauth.Identity{})
	for _, name := range []string{"Resource", "TokenFormat", "OpaqueToken"} {
		if _, found := identity.FieldByName(name); found {
			t.Fatalf("OIDC identity unexpectedly supports %s; RFC 8707 and opaque tokens are not part of this profile", name)
		}
	}
}
