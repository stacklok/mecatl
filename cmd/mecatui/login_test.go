package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestRemoteLoginStoresAbsoluteIssuerCAReferenceAcrossCWDChanges(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	first := t.TempDir()
	second := t.TempDir()
	ca := filepath.Join(first, "issuer-ca.pem")
	if err := os.WriteFile(ca, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(first); err != nil {
		t.Fatal(err)
	}
	original := executeRemoteLogin
	t.Cleanup(func() { executeRemoteLogin = original })
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error {
		registry, openErr := clientauth.OpenRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
		if openErr != nil {
			return openErr
		}
		_, upsertErr := registry.Upsert(conn)
		return upsertErr
	}
	if err := runRemoteLogin("remote.example:443", []string{
		"--issuer", "https://issuer.example", "--client-id", "client", "--audience", "audience", "--tls-ca", "issuer-ca.pem",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(second); err != nil {
		t.Fatal(err)
	}
	conn, err := savedConnection("remote.example:443")
	if err != nil {
		t.Fatal(err)
	}
	physicalCA, err := filepath.EvalSymlinks(ca)
	if err != nil {
		t.Fatal(err)
	}
	if conn.IssuerCAFile != physicalCA || !filepath.IsAbs(conn.IssuerCAFile) {
		t.Fatalf("saved issuer CA = %q, want %q", conn.IssuerCAFile, physicalCA)
	}
}

func TestRemoteLoginExplicitEmptyIdentityDoesNotFallBackToDiscovery(t *testing.T) {
	original := discoverRemoteResource
	t.Cleanup(func() { discoverRemoteResource = original })
	discoverRemoteResource = func(context.Context, protectedResource) (discoveredResource, error) {
		t.Fatal("protected-resource discovery was attempted for an explicit identity flag")
		return discoveredResource{}, nil
	}
	for _, flag := range []string{"issuer", "client-id", "audience"} {
		t.Run(flag, func(t *testing.T) {
			if err := runRemoteLogin("https://resource.example", []string{"--" + flag + "="}); err == nil || !strings.Contains(err.Error(), "required together") {
				t.Fatalf("runRemoteLogin with --%s= error = %v, want incomplete explicit identity error", flag, err)
			}
		})
	}
}

func TestRemoteLoginAllowsSystemIssuerRoots(t *testing.T) {
	original := executeRemoteLogin
	t.Cleanup(func() { executeRemoteLogin = original })
	marker := errors.New("stop after connection capture")
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error {
		if conn.IssuerCAFile != "" {
			t.Fatalf("issuer CA = %q, want system roots", conn.IssuerCAFile)
		}
		return marker
	}

	err := runRemoteLogin("remote.example:443", []string{
		"--issuer", "https://issuer.example", "--client-id", "client", "--audience", "audience",
	})
	if !errors.Is(err, marker) {
		t.Fatalf("runRemoteLogin error = %v, want capture marker", err)
	}
}

func TestRemoteLoginWiresExactRedirectURL(t *testing.T) {
	t.Run("command identity", func(t *testing.T) {
		original := executeRemoteLogin
		t.Cleanup(func() { executeRemoteLogin = original })
		marker := errors.New("stop after option capture")
		executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error {
			if conn.Identity.RedirectURI != oauthlogin.ExactRedirectURL {
				t.Fatalf("redirect URI = %q, want exact callback", conn.Identity.RedirectURI)
			}
			return marker
		}
		err := runRemoteLogin("https://mecated.example", []string{
			"--issuer", "https://issuer.example",
			"--client-id", "client",
			"--audience", "audience",
			"--tls-ca", "ca.pem",
		})
		if !errors.Is(err, marker) {
			t.Fatalf("runRemoteLogin error = %v", err)
		}
	})

	t.Run("callback runtime", func(t *testing.T) {
		original := newRemoteLoginRuntime
		originalPrepare := prepareSavedLogin
		t.Cleanup(func() { newRemoteLoginRuntime = original; prepareSavedLogin = originalPrepare })
		prepareSavedLogin = func(context.Context, clientauth.Connection) (preparedSavedLogin, error) {
			return preparedSavedLogin{close: func() {}}, nil
		}
		marker := errors.New("stop after runtime option capture")
		newRemoteLoginRuntime = func(opts oauthlogin.Options) (*oauthlogin.Runtime, error) {
			if opts.RedirectURL != oauthlogin.ExactRedirectURL {
				t.Fatalf("runtime redirect URL = %q, want exact callback", opts.RedirectURL)
			}
			return nil, marker
		}
		caFile := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(caFile, []byte("not parsed before runtime construction"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runSavedRemoteLogin(context.Background(), clientauth.Connection{
			Identity:     clientauth.Identity{Issuer: "https://issuer.example"},
			IssuerCAFile: caFile,
		}, false)
		if !errors.Is(err, marker) {
			t.Fatalf("runSavedRemoteLogin error = %v", err)
		}
	})
}

func TestSavedRemoteLoginPreflightsLocalStorageBeforeRuntime(t *testing.T) {
	originalRuntime := newRemoteLoginRuntime
	t.Cleanup(func() { newRemoteLoginRuntime = originalRuntime })
	opened := false
	newRemoteLoginRuntime = func(oauthlogin.Options) (*oauthlogin.Runtime, error) {
		opened = true
		return nil, errors.New("runtime must not open")
	}
	originalPrepare := prepareSavedLogin
	t.Cleanup(func() { prepareSavedLogin = originalPrepare })
	prepareSavedLogin = func(context.Context, clientauth.Connection) (preparedSavedLogin, error) {
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runSavedRemoteLogin(t.Context(), clientauth.Connection{IssuerCAFile: caFile}, false)
	var authErr *client.AuthError
	if !errors.As(err, &authErr) || authErr.Reason != client.AuthStorageUnavailable {
		t.Fatalf("preflight error = %v, want storage unavailable", err)
	}
	if opened {
		t.Fatal("OIDC runtime opened before local credential preflight")
	}
}

func TestSavedPublicLoginDoesNotReadEmptyCAPath(t *testing.T) {
	originalRuntime := newRemoteLoginRuntime
	originalPrepare := prepareSavedLogin
	t.Cleanup(func() { newRemoteLoginRuntime = originalRuntime; prepareSavedLogin = originalPrepare })
	prepareSavedLogin = func(context.Context, clientauth.Connection) (preparedSavedLogin, error) {
		return preparedSavedLogin{close: func() {}}, nil
	}
	marker := errors.New("runtime reached")
	newRemoteLoginRuntime = func(oauthlogin.Options) (*oauthlogin.Runtime, error) { return nil, marker }
	if err := runSavedRemoteLogin(t.Context(), clientauth.Connection{IssuerAddressPolicy: clientauth.IssuerAddressPolicyPublic}, false); !errors.Is(err, marker) {
		t.Fatalf("saved public login error = %v, want runtime marker", err)
	}
}

func TestExistingSavedRemoteLoginMissingStoreDoesNotLaunchBrowserOrCreateState(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	originalRuntime := newRemoteLoginRuntime
	t.Cleanup(func() { newRemoteLoginRuntime = originalRuntime })
	opened := false
	newRemoteLoginRuntime = func(oauthlogin.Options) (*oauthlogin.Runtime, error) {
		opened = true
		return nil, errors.New("runtime must not open")
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runExistingSavedRemoteLogin(t.Context(), clientauth.Connection{IssuerCAFile: caFile}, false)
	var authErr *client.AuthError
	if !errors.As(err, &authErr) || authErr.Reason != client.AuthStorageUnavailable {
		t.Fatalf("reauthentication error = %v, want storage unavailable", err)
	}
	if opened {
		t.Fatal("OIDC runtime opened despite missing saved credential state")
	}
	if _, err := os.Stat(filepath.Join(xdg.ConfigHome, "mecatl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reauthentication created credential root: %v", err)
	}
}

// TestSigninErrorLeavesDiscoveryFailuresUnclassified pins that
// clientauth.ErrDiscovery (issuer unreachable, untrusted TLS, or unloadable
// JWKS -- an infrastructure/network problem) is never wrapped into a
// *client.AuthError. AuthStorageUnavailable's documented contract is a LOCAL
// dependency reauthentication cannot fix; wrapping a discovery failure in it
// would steer the user toward resetting local storage for a problem that
// storage reset can't fix, and would falsely claim reauthentication won't
// help when it might. signinError must instead return the raw error so
// client.AuthFailure's deliberate "remain unclassified" fallback applies.
func TestSigninErrorLeavesDiscoveryFailuresUnclassified(t *testing.T) {
	err := signinError(fmt.Errorf("dial issuer: %w", clientauth.ErrDiscovery))
	var authErr *client.AuthError
	if errors.As(err, &authErr) {
		t.Fatalf("signinError wrapped a discovery failure in AuthError{%v}", authErr.Reason)
	}
	if !errors.Is(err, clientauth.ErrDiscovery) {
		t.Fatalf("signinError lost the discovery cause: %v", err)
	}
	if reason, ok := client.AuthFailure(err, false); ok {
		t.Fatalf("discovery failure classified as %q, want unclassified", reason)
	}
}

func TestRemoteLoginIssuerPolicyFlags(t *testing.T) {
	original := executeRemoteLogin
	t.Cleanup(func() { executeRemoteLogin = original })
	var got clientauth.Connection
	executeRemoteLogin = func(_ context.Context, conn clientauth.Connection, _ bool) error { got = conn; return nil }
	args := []string{"--issuer", "https://issuer.example", "--client-id", "client", "--audience", "audience"}
	if err := runRemoteLogin("remote.example:443", args); err != nil {
		t.Fatal(err)
	}
	if got.IssuerAddressPolicy != clientauth.IssuerAddressPolicyPublic || got.IssuerCAFile != "" {
		t.Fatalf("public login connection = %#v", got)
	}
	if err := runRemoteLogin("remote.example:443", append(args, "--private-issuer")); err == nil || !strings.Contains(err.Error(), "requires --tls-ca") {
		t.Fatalf("private issuer without CA error = %v", err)
	}
}

func TestConfirmDiscoveredLoginRejectsTerminalControls(t *testing.T) {
	base := discoveredEnrollment{Resource: "https://api.example", MetadataURL: "https://api.example/.well-known/oauth-protected-resource", Connection: clientauth.Connection{Identity: clientauth.Identity{Issuer: "https://issuer.example", ClientID: "client", Audience: "audience", Target: "api.example:443", Scopes: []string{"openid"}}}}
	for _, value := range []string{"line\nfeed", "line\u2028separator", "line\u2029separator", "\x1b[2J"} {
		candidate := base
		candidate.Connection.Identity.Audience = value
		if _, err := confirmDiscoveredLogin(strings.NewReader("y\n"), io.Discard, candidate); !errors.Is(err, errDiscoveryRejected) {
			t.Errorf("confirmation accepted unsafe metadata value %q: %v", value, err)
		}
	}
}

func TestConfirmDiscoveredLoginAnswers(t *testing.T) {
	enrollment := discoveredEnrollment{Resource: "https://api.example", MetadataURL: "https://api.example/.well-known/oauth-protected-resource", Connection: clientauth.Connection{Identity: clientauth.Identity{Issuer: "https://issuer.example", ClientID: "client", Audience: "audience", Target: "api.example:443", Scopes: []string{"openid"}}}}
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"bare enter declines", "\n", false},
		{"EOF with no input declines", "", false},
		{"y accepts", "y\n", true},
		{"yes accepts", "yes\n", true},
		{"YES case-insensitive", "YES\n", true},
		{"no declines", "no\n", false},
		{"garbage declines", "maybe\n", false},
		{"y with no trailing newline accepts", "y", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := confirmDiscoveredLogin(strings.NewReader(tc.input), io.Discard, enrollment)
			if err != nil {
				t.Fatalf("confirmDiscoveredLogin(%q) error = %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("confirmDiscoveredLogin(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestPrepareSavedRemoteLoginSnapshotsPreventStaleResurrection pins the fix for
// a review finding: the discovered-login route reused prepareSavedRemoteLogin,
// which previously returned no ExpectedTarget/ExpectedCredential snapshot at
// all. That let a concurrent logout for the same target -- racing the
// interactive browser wait between preflight and Enroll's eventual commit --
// be silently undone: Enroll would resurrect the removed row because it had
// no CAS precondition to violate. prepareSavedRemoteLogin must now snapshot
// the pre-existing state (mirroring prepareExistingSavedRemoteLogin), so
// Enroll rejects a commit against state that changed underneath it.
func TestPrepareSavedRemoteLoginSnapshotsPreventStaleResurrection(t *testing.T) {
	reg, err := clientauth.OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := credentialstore.NewMemoryBackend()
	store, err := backend.Open("stale-resurrect")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	creds, err := clientauth.NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	id := clientauth.Identity{Target: "stale-resurrect.example:443", Issuer: "https://issuer.example", ClientID: "client", Audience: "api", RedirectURI: oauthlogin.ExactRedirectURL}
	conn := clientauth.Connection{Identity: id}
	if _, err := reg.Upsert(conn); err != nil {
		t.Fatal(err)
	}

	// This is the fix: prepareSavedRemoteLogin's snapshot step, exercised
	// directly against the fixture above (the real function additionally opens
	// the OS keyring, which is unavailable in a sandboxed test run).
	expectedTarget, expectedCredential, err := snapshotSavedLoginState(t.Context(), conn, reg, creds)
	if err != nil {
		t.Fatalf("snapshotSavedLoginState: %v", err)
	}
	if expectedTarget == nil || len(*expectedTarget) != 1 {
		t.Fatalf("expectedTarget = %#v, want a snapshot of the one existing row", expectedTarget)
	}
	if expectedCredential == nil || expectedCredential.Found {
		t.Fatalf("expectedCredential = %#v, want Found=false (no credential enrolled yet)", expectedCredential)
	}

	// Simulate the race: a concurrent logout removes the enrollment while this
	// sign-in's browser wait would have been in flight.
	if _, err := reg.DeleteTarget(id.Target, []clientauth.Connection{conn}); err != nil {
		t.Fatal(err)
	}

	err = clientauth.Enroll(t.Context(), conn, clientauth.Token{AccessToken: "new", TokenType: "Bearer"}, clientauth.EnrollmentConfig{
		Registry: reg, Credentials: creds,
		ExpectedTarget: expectedTarget, ExpectedCredential: expectedCredential,
	})
	if !errors.Is(err, clientauth.ErrTargetChanged) {
		t.Fatalf("Enroll after concurrent logout = %v, want ErrTargetChanged (stale resurrection must be rejected)", err)
	}
	if _, err := reg.FindTarget(id.Target); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("FindTarget after rejected stale Enroll = %v, want ErrNotFound (logout must not be undone)", err)
	}
}
