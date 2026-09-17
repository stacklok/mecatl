package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestDirectMCPOnboarding_Scenario3_AddOrderingAndResiduals(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	defaultPath := filepath.Join(config, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultPath, []byte("mcp: {servers: []}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(target, []byte("permissions: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	if err := runMCPAdd([]string{"calendar", server.URL, "--file", target, "--credential-store", "file"}, io.Discard, io.Discard); err == nil {
		t.Fatal("add accepted a target shadowed by another MCP source")
	}
	if requests != 0 {
		t.Fatalf("discovery requests = %d, want zero after target validation failed", requests)
	}
}

func TestDirectMCPOnboarding_Scenario3_BrokerModeRejectedBeforeDiscoveryOrCustody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("mcp:\n  mode: broker\n  servers: []\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runMCPAdd([]string{"calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "mcp.mode is broker") {
		t.Fatalf("broker add error = %v, want explicit broker rejection", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("broker add mutated settings:\n%s", got)
	}
	if strings.Contains(output.String(), "discovering protected resource") || strings.Contains(output.String(), "selecting credential custody") {
		t.Fatalf("broker add performed a later onboarding stage: %q", output.String())
	}
}
func TestMCPCredentialStatusProjection(t *testing.T) {
	localRoot := filepath.Join(t.TempDir(), "credentials")
	server := permconfig.MCPServerProfile{Name: "oauth", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: localRoot, Key: &permconfig.MCPNativeCredentialKey{Mode: "file"}}}}}}
	if got := mcpCredentialStatus(server); got != "login required" {
		t.Fatalf("missing local custody status = %q, want login required", got)
	}
	if err := os.MkdirAll(localRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localRoot, "mcp-credential-backend.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mcpCredentialStatus(server); got != "recovery required" {
		t.Fatalf("invalid marker status = %q, want recovery required", got)
	}
}
func TestDirectMCPOnboarding_Scenario5_BoundedTruthfulStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	data := "# operator note\nmcp:\n  servers:\n    - name: calendar\n      url: https://mcp.example/mcp\n      auth: {mode: none}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runMCPList([]string{"--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	canonical, err := mcpSettingsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "calendar\thttps://mcp.example/mcp\tnone\tready\t"+canonical) {
		t.Fatalf("list output = %q", output.String())
	}
	if !strings.Contains(output.String(), "MCP settings source: "+canonical) || !strings.Contains(output.String(), "MCP settings winner: "+canonical) {
		t.Fatalf("list provenance = %q", output.String())
	}
	output.Reset()
	if err := runMCPRemove([]string{"calendar", "--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	remaining, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(remaining), "calendar") || !strings.Contains(string(remaining), "# operator note") {
		t.Fatalf("settings after remove:\n%s", remaining)
	}
	if !strings.Contains(output.String(), "no upstream client was revoked") {
		t.Fatalf("remove output = %q", output.String())
	}
}

func TestDirectMCPOnboarding_ListProhibitsNetworkCustodyBrowserAndMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { requests++ }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	data := strings.Replace(mcpLoginOAuthYAML("local", "local", root), "https://mcp.example/mcp", server.URL, 1)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MECATL_LIST_CANARY", "must-not-be-read")
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := runMCPList([]string{"--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || strings.Contains(output.String(), "must-not-be-read") {
		t.Fatalf("list performed network/custody access: requests=%d output=%q", requests, output.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("list mutated settings")
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list created mutation lock: %v", err)
	}
	if strings.Contains(output.String(), "authorize") || strings.Contains(output.String(), "browser") {
		t.Fatalf("list presented browser flow: %q", output.String())
	}
}
func TestMCPSettingsLockSerializesSafeLock(t *testing.T) {
	path, err := mcpSettingsFile(filepath.Join(t.TempDir(), "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockMCPSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	unlock, err = lockMCPSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestMCPSettingsLockRejectsNonOwnerOnlyFile(t *testing.T) {
	path, err := mcpSettingsFile(filepath.Join(t.TempDir(), "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".lock", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lockMCPSettings(path); err == nil {
		t.Fatal("lockMCPSettings accepted a non-owner-only lock")
	}
}

func TestDirectMCPOnboarding_Scenario3_ReadOriginsAndWriteTarget(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	defaultPath := filepath.Join(config, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultPath, []byte("mcp: {servers: []}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(target, []byte("permissions: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := mcpMutationTarget(target)
	if err == nil || !strings.Contains(err.Error(), defaultPath) || !strings.Contains(err.Error(), target) {
		t.Fatalf("mcpMutationTarget error = %v", err)
	}
}

func TestMCPLoginFileAndPermissionConfigAreMutuallyExclusive(t *testing.T) {
	if _, err := parseMCPLoginArgs([]string{"server", "--file", "settings.yaml", "--permission-config", "other.yaml"}, io.Discard); err == nil {
		t.Fatal("accepted mutually exclusive settings selectors")
	}
	parsed, err := parseMCPLoginArgs([]string{"server", "--file=settings.yaml"}, io.Discard)
	if err != nil || parsed.file != "settings.yaml" {
		t.Fatalf("parseMCPLoginArgs = %#v, %v", parsed, err)
	}
}

func TestDirectMCPOnboarding_Scenario3_AtomicNarrowMutation(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(target, []byte("mcp: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readMCPSettings(link); err == nil {
		t.Fatal("read accepted symlink")
	}

	path := filepath.Join(dir, "safe.yaml")
	if err := os.WriteFile(path, []byte("mcp: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := readMCPSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("mcp:\n  servers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMCPSettings(path, before, []byte("mcp:\n  servers:\n    - name: replacement\n")); err == nil {
		t.Fatal("write accepted stale settings")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mcp:\n  servers: []\n" {
		t.Fatalf("stale write replaced settings: %q", got)
	}
}

func TestMCPSettingsRejectsSymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(linkDir, "settings.yaml")
	if _, err := readMCPSettings(path); err == nil {
		t.Fatal("read accepted symlinked parent")
	}
	if err := writeMCPSettings(path, mcpSettingsSnapshot{data: []byte("{}\n")}, []byte("mcp: {}\n")); err == nil {
		t.Fatal("write accepted symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(realDir, "settings.yaml")); !os.IsNotExist(err) {
		t.Fatal("write followed symlinked parent")
	}
}

// TestMCPAddDefaultSettingsPathFollowsXDGNotOSNative pins the exact bug this
// fixed: mecated's own xdgconfig convention (XDG_CONFIG_HOME, or ~/.config on
// every OS including macOS) is the ONE location every mecatl command must
// agree on. The stdlib os.UserConfigDir() resolves somewhere else entirely on
// macOS (~/Library/Application Support) — using it here would silently split
// "mcp add"'s default settings file from every other command's, per
// test-isolation.md's default-discovery-test discipline this is
// nonparallel and uses a synthetic HOME/XDG_CONFIG_HOME.
func TestMCPAddDefaultSettingsPathFollowsXDGNotOSNative(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", xdg)
	path, err := defaultMCPSettingsFile()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(xdg, "mecatl", "settings.yaml")
	if path != want {
		t.Fatalf("defaultMCPSettingsFile() = %q, want %q (an OS-native os.UserConfigDir() path would diverge from every other mecatl command's default)", path, want)
	}
}

func TestDirectMCPOnboarding_Scenario3_ConfigErrorsNameBrokenLayer(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "malformed", data: []byte("mcp:\n  servers: [\n")},
		{name: "wrong schema", data: []byte("mcp: nope\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := runMCPList([]string{"--file", path}, io.Discard); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("list error = %v, want selected path", err)
			} else if strings.Contains(err.Error(), "server is not configured") {
				t.Fatalf("list degraded broken config to absence: %v", err)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	if err := os.Mkdir(missing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runMCPList([]string{"--file", missing}, io.Discard); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("unreadable list error = %v, want selected path", err)
	}
}

func TestDirectMCPOnboarding_Scenario2_ExternalAndSharedCustody(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	server := permconfig.MCPServerProfile{Name: "external", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "environment"}}}}
	if got := mcpCredentialStatus(server); got != "unknown" {
		t.Fatalf("external-key status = %q, want unknown without reading the key", got)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external-key inspection created local custody: %v", err)
	}
}

func TestDirectMCPOnboarding_Scenario4_CleanupEveryTerminalPath(t *testing.T) {
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("{}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover, oldLogin := discoverMCPDirectIssuer, executeMCPLogin
	t.Cleanup(func() { discoverMCPDirectIssuer, executeMCPLogin = oldDiscover, oldLogin })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{Issuer: "https://issuer.example"}, nil
	}
	executeMCPLogin = func(context.Context, mcp.ServerConfig, oauthlogin.Options, app.MCPLoginOptions) error {
		return app.ErrMCPLoginConnect
	}
	if err := runMCPAdd([]string{"cleanup", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, io.Discard, io.Discard); err == nil {
		t.Fatal("login failure was ignored")
	}
	settings, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "name: 'cleanup'") {
		t.Fatalf("failed login lost published retry profile: %s", settings)
	}
	if strings.Contains(string(settings), "access-token") {
		t.Fatal("failed login persisted an access token in settings")
	}
}

func TestDirectMCPOnboarding_Scenario4_ProgressAndVerification(t *testing.T) {
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover, oldLogin := discoverMCPDirectIssuer, executeMCPLogin
	t.Cleanup(func() { discoverMCPDirectIssuer, executeMCPLogin = oldDiscover, oldLogin })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{Issuer: "https://issuer.example"}, nil
	}
	loginCalls := 0
	executeMCPLogin = func(_ context.Context, server mcp.ServerConfig, _ oauthlogin.Options, _ app.MCPLoginOptions) error {
		loginCalls++
		if server.Name != "Calendar" || server.OAuth == nil {
			t.Fatalf("login server = %#v", server)
		}
		published, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(published), "name: 'Calendar'") {
			t.Fatalf("login handoff occurred before settings publication: %q, %v", published, err)
		}
		return nil
	}
	var output strings.Builder
	if err := runMCPAdd([]string{"Calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"writable target:", "discovering protected resource", "selecting credential custody", "settings saved", "succeeded for Calendar"} {
		if !strings.Contains(output.String(), stage) {
			t.Fatalf("add output = %q, missing %q", output.String(), stage)
		}
	}
	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want one", loginCalls)
	}
	for _, canary := range []string{"access-token", "client-secret", "https://issuer.example/oauth/authorize"} {
		if strings.Contains(output.String(), canary) {
			t.Fatalf("add output leaked %q: %q", canary, output.String())
		}
	}
	settings, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: 'Calendar'", "profile: 'calendar'", "principal: 'local-user'", "issuer: 'https://issuer.example'"} {
		if !strings.Contains(string(settings), want) {
			t.Fatalf("published settings = %q, missing %q", settings, want)
		}
	}
}

func TestDirectMCPOnboarding_Scenario5_ListIsOfflineAndNonPresenting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(mcpLoginOAuthYAML("secret", "environment", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MECATL_LOGIN_CREDENTIAL", "must-not-be-read")
	var output strings.Builder
	if err := runMCPList([]string{"--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, "secret\thttps://mcp.example/mcp\toauth/cimd\tunknown") || strings.Contains(got, "must-not-be-read") {
		t.Fatalf("offline list output = %q", got)
	}
	if strings.Contains(got, "https://issuer.example") || strings.Contains(got, "authorize") {
		t.Fatalf("list presented OAuth details: %q", got)
	}
}

func TestDirectMCPOnboarding_Scenario6_MissingLifecycleRemovesProfileOnly(t *testing.T) {
	fixture := newMCPLoginDCRFixture(t)
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(mcpLoginDCRYAML(fixture, filepath.Join(t.TempDir(), "credentials"))), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRemove := removeMCPOAuthDCR
	t.Cleanup(func() { removeMCPOAuthDCR = oldRemove })
	calls := 0
	removeMCPOAuthDCR = func(context.Context, string, mcp.OAuthOptions) (mcp.OAuthDCRRemovalResult, error) {
		calls++
		return mcp.OAuthDCRRemovalResult{}, nil
	}
	var output strings.Builder
	if err := runMCPRemove([]string{"connector", "--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(output.String(), "lifecycle record not found") {
		t.Fatalf("remove calls=%d output=%q", calls, output.String())
	}
	remaining, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(remaining), "connector") {
		t.Fatalf("profile remained after missing-lifecycle removal: %s", remaining)
	}
}

func TestDirectMCPOnboarding_Scenario6_RemovalStateMachine(t *testing.T) {
	fixture := newMCPLoginDCRFixture(t)
	root := filepath.Join(t.TempDir(), "credentials")
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := os.WriteFile(settings, []byte(mcpLoginDCRYAML(fixture, root)), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := loadMCPLoginProfiles([]string{settings})
	if err != nil {
		t.Fatal(err)
	}
	server, ok := profiles.OAuthServer("connector")
	if !ok {
		profiles.Close()
		t.Fatal("connector seed profile missing")
	}
	mcp.AllowOAuthLoopbackForTest(t, server.OAuth)
	mcp.TrustOAuthCertificateForTest(t, server.OAuth, fixture.server.Certificate())
	prepared, callbackPath, err := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, mcp.OAuthDCRLoginReuse)
	if err != nil {
		profiles.Close()
		t.Fatal(err)
	}
	prepared.RedirectURL = "http://127.0.0.1:49152" + callbackPath
	prepared.Presenter = mcp.OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		return &auth.AuthorizationResult{Code: "fixture-code", State: u.Query().Get("state"), Iss: fixture.server.URL}, nil
	})
	controller, err := mcp.NewOAuthController(context.Background(), fixture.resource(), prepared)
	if err != nil {
		profiles.Close()
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, fixture.resource(), nil)
	if err != nil {
		controller.Close()
		profiles.Close()
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"WWW-Authenticate": {`Bearer scope="openid"`}}, Body: io.NopCloser(strings.NewReader(""))}
	if err := controller.Authorize(context.Background(), req, resp); err != nil {
		controller.Close()
		profiles.Close()
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		profiles.Close()
		t.Fatal(err)
	}
	if err := runMCPRemove([]string{"connector", "--file", settings}, io.Discard); err != nil {
		profiles.Close()
		t.Fatal(err)
	}
	key := dcrLoginRecordKey("mecatl/mcp/oauth-dcr-lifecycle-key/v1", "connector")
	record, err := server.OAuth.CredentialStore.Get(context.Background(), key)
	profiles.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record.Value), `"state":"removed"`) {
		t.Fatalf("lifecycle record = %s, want removed tombstone", record.Value)
	}
	remaining, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(remaining), "connector") {
		t.Fatalf("settings retained removed profile: %s", remaining)
	}
}
func TestDirectMCPOnboarding_Scenario1_FailureHasNoSideEffects(t *testing.T) {
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("mcp:\n  servers: []\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover := discoverMCPDirectIssuer
	t.Cleanup(func() { discoverMCPDirectIssuer = oldDiscover })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{}, errors.New("discovery fixture failed")
	}
	var output strings.Builder
	if err := runMCPAdd([]string{"calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, &output, io.Discard); err == nil {
		t.Fatal("discovery failure was ignored")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("discovery failure changed settings: %q", got)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "mecatl", "mcp-credentials")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery failure created custody state: %v", err)
	}
	if strings.Contains(output.String(), "selecting credential custody") || strings.Contains(output.String(), "settings saved") {
		t.Fatalf("discovery failure continued onboarding: %q", output.String())
	}
}

func TestDirectMCPOnboarding_AddCustodyFailureLeavesSettingsUnpublished(t *testing.T) {
	configBase, stateHome := t.TempDir(), t.TempDir()
	configHome := filepath.Join(configBase, "config")
	// publishMCPAdd derives the file-custody key path from XDG_CONFIG_HOME.
	// Make that path a regular file so custody fails before the settings CAS.
	if err := os.WriteFile(configHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("mcp:\n  servers: []\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover := discoverMCPDirectIssuer
	t.Cleanup(func() { discoverMCPDirectIssuer = oldDiscover })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{Issuer: "https://issuer.example"}, nil
	}
	var output strings.Builder
	if err := runMCPAdd([]string{"calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, &output, io.Discard); err == nil {
		t.Fatal("custody failure was ignored")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("custody failure published settings: %q", got)
	}
	if strings.Contains(output.String(), "settings saved") {
		t.Fatalf("custody failure reached publication: %q", output.String())
	}
}

func TestDirectMCPOnboarding_AddLoginFailureLeavesPublishedProfileForRetry(t *testing.T) {
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover, oldLogin := discoverMCPDirectIssuer, executeMCPLogin
	t.Cleanup(func() { discoverMCPDirectIssuer, executeMCPLogin = oldDiscover, oldLogin })
	discoverMCPDirectIssuer = func(context.Context, string) (mcp.DirectIssuerDiscovery, error) {
		return mcp.DirectIssuerDiscovery{Issuer: "https://issuer.example"}, nil
	}
	loginCalls := 0
	executeMCPLogin = func(context.Context, mcp.ServerConfig, oauthlogin.Options, app.MCPLoginOptions) error {
		loginCalls++
		return app.ErrMCPLoginConnect
	}
	if err := runMCPAdd([]string{"Calendar", "https://mcp.example/mcp", "--file", path, "--credential-store", "file"}, io.Discard, io.Discard); err == nil {
		t.Fatal("login failure was ignored")
	}
	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want one", loginCalls)
	}
	settings, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "name: 'Calendar'") {
		t.Fatalf("login failure removed published retry profile: %s", settings)
	}
}

func TestDirectMCPOnboarding_RemovePreservesWrappingKeyAndSettingsOnRecoveryFailure(t *testing.T) {
	fixture := newMCPLoginDCRFixture(t)
	root := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := os.WriteFile(settings, []byte(mcpLoginDCRYAML(fixture, root)), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRemove := removeMCPOAuthDCR
	t.Cleanup(func() { removeMCPOAuthDCR = oldRemove })
	removeMCPOAuthDCR = func(context.Context, string, mcp.OAuthOptions) (mcp.OAuthDCRRemovalResult, error) {
		return mcp.OAuthDCRRemovalResult{}, mcp.NewOAuthDCRRecoveryError(mcp.OAuthDCRRecoveryPending)
	}
	before, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := runMCPRemove([]string{"connector", "--file", settings}, io.Discard); err == nil {
		t.Fatal("recovery-required removal succeeded")
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("recovery-required removal changed settings")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("recovery-required removal deleted wrapping-key custody root: %v", err)
	}
}
