package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestResolveMCPLoginIsPureUntilRunner(t *testing.T) {
	res := resolveCommand([]string{"mecated", "mcp", "login", "GitHub", "--no-browser"})
	if res.err != nil || !res.handled || res.run == nil {
		t.Fatalf("resolution = %#v", res)
	}
}

func TestMCPLoginHelpAndUsageAreSideEffectFree(t *testing.T) {
	res := resolveCommand([]string{"mecated", "mcp", "login", "--help"})
	var out strings.Builder
	if err := res.run(strings.NewReader(""), &out, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
	if !strings.Contains(out.String(), "mecated mcp login SERVER [--no-browser]") {
		t.Fatalf("help = %q", out.String())
	}

	groupHelp := resolveCommand([]string{"mecated", "mcp", "--help"})
	if !groupHelp.handled || groupHelp.err != nil {
		t.Fatalf("group help resolution = %#v", groupHelp)
	}

	for _, argv := range [][]string{
		{"mecated", "mcp"},
		{"mecated", "mcp", "unknown"},
	} {
		if res := resolveCommand(argv); res.err == nil || res.handled {
			t.Errorf("%v resolved without a usage error", argv)
		}
	}
	for _, args := range [][]string{
		nil,
		{"one", "two"},
		{"server", "--issuer", "https://secret.example"},
		{"server", "--mcp-server-auth", "x"},
		{"server", "--permission-config"},
		{"server", "--permission-config", "--no-browser"},
	} {
		if _, err := parseMCPLoginArgs(args, io.Discard); err == nil {
			t.Errorf("parseMCPLoginArgs(%q) succeeded", args)
		}
	}
}

func TestMCPGroupHelpActionHasNoRuntimeSideEffects(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	storeRoot := filepath.Join(t.TempDir(), "credential-store")
	settings := filepath.Join(xdg, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := os.WriteFile(settings, []byte(mcpLoginOAuthYAML("help-canary", "local", storeRoot)), 0o600); err != nil {
		t.Fatal(err)
	}
	original := executeMCPLogin
	t.Cleanup(func() { executeMCPLogin = original })
	calls := 0
	executeMCPLogin = func(context.Context, mcp.ServerConfig, oauthlogin.Options) error {
		calls++
		return errors.New("help must not execute login")
	}

	res := resolveCommand([]string{"mecated", "mcp", "--help"})
	if !res.handled || res.err != nil || res.run == nil {
		t.Fatalf("group help resolution = %#v", res)
	}
	var out strings.Builder
	if err := res.run(strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("group help action: %v", err)
	}
	if !strings.Contains(out.String(), "mecated mcp login SERVER") {
		t.Fatalf("group help = %q", out.String())
	}
	if calls != 0 {
		t.Fatalf("group help executed login %d times", calls)
	}
	if _, err := os.Stat(storeRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("group help touched credential store: %v", err)
	}
}

func TestMCPLoginSelectionAndRemedies(t *testing.T) {
	profiles := &cliconfig.MCPProfiles{Servers: []mcp.ServerConfig{
		{Name: "plain"},
		{Name: "readonly", OAuth: &mcp.OAuthOptions{CredentialReader: loginReaderStub{}}},
	}}
	if _, err := selectMCPLoginServer(profiles, "plain"); err == nil || !strings.Contains(err.Error(), "mcp.servers[].auth.mode: oauth") {
		t.Fatalf("non-OAuth error = %v", err)
	}
	if _, err := selectMCPLoginServer(profiles, "readonly"); err == nil || !strings.Contains(err.Error(), "credentials.mode: local") || !strings.Contains(err.Error(), "environment credentials cannot be mutated") {
		t.Fatalf("read-only error = %v", err)
	}
	for category, remedy := range map[error]string{
		app.ErrMCPLoginConfig:        "auth.mode: oauth",
		app.ErrMCPLoginAuthorization: "browser callback",
		app.ErrMCPLoginConnect:       "server URL",
		app.ErrMCPLoginCredential:    "credential store",
		app.ErrMCPLoginCleanup:       "callback availability",
	} {
		if got := mcpLoginRemedy(category).Error(); !strings.Contains(got, remedy) {
			t.Errorf("remedy for %v = %q", category, got)
		}
	}

	provider := errors.Join(app.ErrMCPLoginAuthorization, &oauthlogin.AuthorizationErrorResponse{Code: "invalid_scope", Description: "requested scope disabled"})
	if got := mcpLoginRemedy(provider).Error(); !strings.Contains(got, "invalid_scope") || !strings.Contains(got, "requested scopes") {
		t.Errorf("provider remedy = %q", got)
	}
	rejected := errors.Join(app.ErrMCPLoginAuthorization, &oauthlogin.CallbackRejectedError{Reason: "nested-token-code-state-path-body-canary\nInjected"})
	if got := mcpLoginRemedy(rejected).Error(); strings.Contains(got, "canary") || !strings.Contains(got, "browser callback was rejected") {
		t.Errorf("callback remedy = %q", got)
	}
	for name, bind := range map[string]*oauthlogin.CallbackBindError{
		"occupied":    {Reason: oauthlogin.CallbackBindAddressInUse},
		"unavailable": {Reason: oauthlogin.CallbackBindUnavailable},
	} {
		const bindCanary = "nested-network-endpoint-token-state-path-canary"
		got := mcpLoginRemedy(errors.Join(app.ErrMCPLoginAuthorization, fmt.Errorf("%s: %w", bindCanary, bind))).Error()
		if strings.Contains(got, bindCanary) {
			t.Errorf("%s bind remedy leaked detail: %q", name, got)
		}
		if name == "occupied" && (!strings.Contains(got, "already in use") || !strings.Contains(got, "other login process")) {
			t.Errorf("occupied bind remedy = %q", got)
		}
		if name == "unavailable" && !strings.Contains(got, "local callback permissions") {
			t.Errorf("unavailable bind remedy = %q", got)
		}
	}
	const nestedCanary = "nested-network-provider-endpoint-token-code-state-path-body-canary"
	unknown := fmt.Errorf("%w: %s", app.ErrMCPLoginAuthorization, nestedCanary)
	if got := mcpLoginRemedy(unknown).Error(); strings.Contains(got, nestedCanary) {
		t.Errorf("generic remedy leaked nested cause: %q", got)
	}
}

type loginReaderStub struct{}

func (loginReaderStub) Get(context.Context, []byte) (credentialstore.Record, error) {
	return credentialstore.Record{}, credentialstore.ErrNotFound
}
func (loginReaderStub) Capabilities() credentialstore.Capabilities {
	return credentialstore.Capabilities{}
}
func (loginReaderStub) Close() error { return nil }

func TestMCPLoginUsesExplicitOperatorPrecedence(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	conventional := filepath.Join(xdg, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(conventional), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conventional, []byte(mcpLoginNoneYAML("conventional")), 0o600); err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(t.TempDir(), "operator.yaml")
	if err := os.WriteFile(explicit, []byte(mcpLoginNoneYAML("explicit")), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := loadMCPLoginProfiles([]string{explicit})
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	if got := namesOfLoginProfiles(profiles); got != "explicit" {
		t.Fatalf("selected profiles = %q, want explicit", got)
	}
}

func TestRunMCPLoginExecutionPathUsesFixedCallback(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	t.Setenv("MECATL_LOGIN_KEY", key)
	t.Setenv("MECATL_LOGIN_CREDENTIAL", base64.StdEncoding.EncodeToString([]byte("opaque")))

	local := filepath.Join(t.TempDir(), "local.yaml")
	if err := os.WriteFile(local, []byte(mcpLoginOAuthYAML("GitHub", "local", filepath.Join(t.TempDir(), "credentials"))), 0o600); err != nil {
		t.Fatal(err)
	}
	original := executeMCPLogin
	t.Cleanup(func() { executeMCPLogin = original })
	calls := 0
	executeMCPLogin = func(_ context.Context, server mcp.ServerConfig, opts oauthlogin.Options) error {
		calls++
		if server.Name != "GitHub" || server.OAuth == nil || server.OAuth.CredentialStore == nil || server.OAuth.CredentialReader != nil {
			t.Fatalf("selected server = %#v", server)
		}
		if !opts.NoBrowser || opts.URLWriter == nil || opts.RedirectURL != oauthlogin.ExactRedirectURL {
			t.Fatalf("runtime options = %#v; no-browser or fixed-callback redirect was not forwarded", opts)
		}
		return nil
	}
	var out strings.Builder
	if err := runMCPLogin([]string{"github", "--permission-config", local, "--no-browser"}, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(out.String(), "succeeded for GitHub") {
		t.Fatalf("calls=%d output=%q", calls, out.String())
	}

	for name, body := range map[string]struct {
		body string
		want string
	}{
		"environment": {mcpLoginOAuthYAML("env", "environment", ""), "environment credentials cannot be mutated"},
		"non-oauth":   {mcpLoginNoneYAML("plain"), "not OAuth-enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(path, []byte(body.body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := runMCPLogin([]string{nameToServer(name), "--permission-config", path}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), body.want) {
				t.Fatalf("error = %v, want %q", err, body.want)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("login seam called for rejected profile: %d", calls)
	}
}

func nameToServer(test string) string {
	if test == "environment" {
		return "env"
	}
	return "plain"
}

func namesOfLoginProfiles(profiles *cliconfig.MCPProfiles) string {
	var names []string
	for _, server := range profiles.Servers {
		names = append(names, server.Name)
	}
	return strings.Join(names, ",")
}

func mcpLoginNoneYAML(name string) string {
	return "mcp:\n  servers:\n    - name: " + name + "\n      url: https://mcp.example/mcp\n      auth: {mode: none}\n"
}

func mcpLoginOAuthYAML(name, mode, root string) string {
	credentials := "environment:\n              credential_env: MECATL_LOGIN_CREDENTIAL"
	if mode == "local" {
		credentials = "local:\n              root: " + root + "\n              key_env: MECATL_LOGIN_KEY"
	}
	return "mcp:\n  servers:\n    - name: " + name + "\n      url: https://mcp.example/mcp\n      auth:\n        mode: oauth\n        oauth:\n          profile: work\n          principal: operator\n          issuer: https://issuer.example\n          client:\n            mode: cimd\n            cimd: {document_url: https://client.example/metadata.json}\n          scopes: [read]\n          credentials:\n            mode: " + mode + "\n            " + credentials + "\n          network: {additional_origins: [https://client.example], private_origins: [], max_redirects: 0}\n"
}

func TestMCPLoginArgsAcceptServerInteractionAndConfigSelectionOnly(t *testing.T) {
	for _, test := range []struct {
		args      []string
		noBrowser bool
		configs   int
	}{
		{args: []string{"server"}},
		{args: []string{"server", "--no-browser"}, noBrowser: true},
		{args: []string{"--no-browser", "server"}, noBrowser: true},
		{args: []string{"server", "--permission-config", "one.yaml", "--permission-config=two.yaml"}, configs: 2},
	} {
		got, err := parseMCPLoginArgs(test.args, io.Discard)
		if err != nil || got.server != "server" || got.noBrowser != test.noBrowser || len(got.permissionConfigs) != test.configs {
			t.Errorf("parseMCPLoginArgs(%q) = %#v, %v", test.args, got, err)
		}
	}
}
