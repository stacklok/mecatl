package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

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
	if !strings.Contains(out.String(), "mecated mcp login SERVER [--no-browser]") ||
		!strings.Contains(out.String(), "--reset-dcr-registration | --retry-dcr-registration") {
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

func TestParseMCPLoginDCRResetAndRetryFlags(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want mcp.OAuthDCRLoginAction
	}{
		{"--reset-dcr-registration", mcp.OAuthDCRLoginResetRegistration},
		{"--retry-dcr-registration", mcp.OAuthDCRLoginRetryRegistration},
	} {
		parsed, err := parseMCPLoginArgs([]string{"gateway", tc.flag}, io.Discard)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.flag, err)
		}
		if parsed.dcrAction != tc.want {
			t.Fatalf("parse %s action = %v, want %v", tc.flag, parsed.dcrAction, tc.want)
		}
	}
	for _, args := range [][]string{
		{"gateway", "--reset-dcr-registration", "--retry-dcr-registration"},
		{"gateway", "--retry-dcr-registration", "--reset-dcr-registration"},
		{"gateway", "--reset-dcr-registration", "--reset-dcr-registration"},
		{"gateway", "--retry-dcr-registration", "--retry-dcr-registration"},
	} {
		if _, err := parseMCPLoginArgs(args, io.Discard); !errors.Is(err, errMCPLoginUsage) {
			t.Errorf("parseMCPLoginArgs(%q) error = %v", args, err)
		}
	}

	const secret = "registration-client-id-secret-canary"
	for _, tc := range []struct {
		name  string
		kind  mcp.OAuthDCRRecoveryCategory
		want  []string
		avoid []string
	}{
		{name: "legacy pending", kind: mcp.OAuthDCRRecoveryPending, want: []string{"previous registration attempt did not complete", "safe failure stage was not recorded", "--retry-dcr-registration", "duplicate or orphan client"}, avoid: []string{"--reset-dcr-registration", secret}},
		{name: "corrupt", kind: mcp.OAuthDCRRecoveryCorrupt, want: []string{"corrupt or unreadable", "Keep credential-store records unchanged", "deployment operator or support team", "do not send credential-store contents", "do not repair corrupt state or revoke an upstream client"}, avoid: []string{"--retry-dcr-registration", "--reset-dcr-registration", "delete", secret}},
		{name: "unknown outcome", kind: mcp.OAuthDCRRecoveryRegistrationOutcomeUnknown, want: []string{"request outcome is unknown", "--retry-dcr-registration", "orphan client"}, avoid: []string{secret}},
		{name: "invalid response", kind: mcp.OAuthDCRRecoveryResponseInvalid, want: []string{"provider returned a registration response", "could not safely use", "--retry-dcr-registration", "orphan client"}, avoid: []string{"contract validation", secret}},
		{name: "persistence", kind: mcp.OAuthDCRRecoveryReadyPersistence, want: []string{"ready record was not persisted", "--retry-dcr-registration", "orphan client"}, avoid: []string{secret}},
		{name: "identity drift", kind: mcp.OAuthDCRRecoveryResetRequired, want: []string{"identity differs", "--reset-dcr-registration", "valid ready registration"}, avoid: []string{"--retry-dcr-registration", secret}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recovery := errors.Join(app.ErrMCPLoginAuthorization, mcp.NewOAuthDCRRecoveryError(tc.kind), errors.New(secret))
			got := mcpLoginRemedy(recovery).Error()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("recovery remedy = %q, want %q", got, want)
				}
			}
			for _, forbidden := range tc.avoid {
				if strings.Contains(got, forbidden) {
					t.Fatalf("recovery remedy leaked or suggested forbidden %q: %q", forbidden, got)
				}
			}
		})
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
	executeMCPLogin = func(context.Context, mcp.ServerConfig, oauthlogin.Options, app.MCPLoginOptions) error {
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

func TestRunMCPLoginExecutionPathUsesRandomCallback(t *testing.T) {
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
	executeMCPLogin = func(_ context.Context, server mcp.ServerConfig, opts oauthlogin.Options, loginOpts app.MCPLoginOptions) error {
		calls++
		if server.Name != "GitHub" || server.OAuth == nil || server.OAuth.CredentialStore == nil || server.OAuth.CredentialReader != nil {
			t.Fatalf("selected server = %#v", server)
		}
		if !opts.NoBrowser || opts.URLWriter == nil || opts.RedirectURL != "" {
			t.Fatalf("runtime options = %#v; no-browser or random-path default was not forwarded", opts)
		}
		if loginOpts.DCRAction != mcp.OAuthDCRLoginRetryRegistration {
			t.Fatalf("login options = %#v", loginOpts)
		}
		return nil
	}
	var out strings.Builder
	if err := runMCPLogin([]string{"github", "--permission-config", local, "--no-browser", "--retry-dcr-registration"}, &out); err != nil {
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

func TestADR_0325_DCRResetAndRetryCLI(t *testing.T) {
	fixture := newMCPLoginDCRFixture(t)
	root := filepath.Join(t.TempDir(), "credentials")
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := os.WriteFile(settings, []byte(mcpLoginDCRYAML(fixture, root)), 0o600); err != nil {
		t.Fatal(err)
	}
	original := executeMCPLogin
	t.Cleanup(func() { executeMCPLogin = original })

	seed := func(t *testing.T, ready, grant bool) {
		t.Helper()
		profiles, err := loadMCPLoginProfiles([]string{settings})
		if err != nil {
			t.Fatalf("load seed profile: %v", err)
		}
		defer profiles.Close()
		server, ok := profiles.OAuthServer("connector")
		if !ok {
			t.Fatal("connector seed profile missing")
		}
		mcp.AllowOAuthLoopbackForTest(t, server.OAuth)
		mcp.TrustOAuthCertificateForTest(t, server.OAuth, fixture.server.Certificate())
		prepared, callbackPath, err := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, mcp.OAuthDCRLoginReuse)
		if err != nil {
			t.Fatalf("prepare seed: %v", err)
		}
		if !ready {
			return
		}
		prepared.RedirectURL = "http://127.0.0.1:49152" + callbackPath
		prepared.Presenter = mcp.OAuthPresenterFunc(func(_ context.Context, raw string) (*auth.AuthorizationResult, error) {
			u, parseErr := url.Parse(raw)
			if parseErr != nil {
				return nil, parseErr
			}
			return &auth.AuthorizationResult{Code: "fixture-code", State: u.Query().Get("state"), Iss: fixture.server.URL}, nil
		})
		controller, err := mcp.NewOAuthController(context.Background(), fixture.resource(), prepared)
		if err != nil {
			t.Fatalf("register seed: %v", err)
		}
		defer controller.Close()
		if grant {
			req, reqErr := http.NewRequest(http.MethodGet, fixture.resource(), nil)
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"WWW-Authenticate": {`Bearer scope="openid"`}}, Body: io.NopCloser(strings.NewReader(""))}
			if err := controller.Authorize(context.Background(), req, resp); err != nil {
				t.Fatal(err)
			}
		}
	}
	registrationKey := dcrLoginRecordKey("mecatl/mcp/oauth-dcr-registration-key/v1", "work", "operator", fixture.resource(), fixture.server.URL)
	getRecord := func(t *testing.T, key []byte) credentialstore.Record {
		t.Helper()
		profiles, err := loadMCPLoginProfiles([]string{settings})
		if err != nil {
			t.Fatal(err)
		}
		defer profiles.Close()
		server, _ := profiles.OAuthServer("connector")
		record, err := server.OAuth.CredentialStore.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	assertUnchanged := func(t *testing.T, key []byte, before credentialstore.Record) {
		t.Helper()
		after := getRecord(t, key)
		if string(after.Value) != string(before.Value) || !after.Version.Equal(before.Version) {
			t.Fatal("rejected CLI action mutated the durable record")
		}
	}
	runAction := func(t *testing.T, actionFlag string, operation func(mcp.ServerConfig, app.MCPLoginOptions) error) (int, error) {
		t.Helper()
		calls := 0
		executeMCPLogin = func(_ context.Context, server mcp.ServerConfig, _ oauthlogin.Options, options app.MCPLoginOptions) error {
			calls++
			mcp.AllowOAuthLoopbackForTest(t, server.OAuth)
			mcp.TrustOAuthCertificateForTest(t, server.OAuth, fixture.server.Certificate())
			return operation(server, options)
		}
		args := []string{"connector", "--permission-config", settings}
		if actionFlag != "" {
			args = append(args, actionFlag)
		}
		return calls, runMCPLogin(args, io.Discard)
	}

	t.Run("missing registration rejects reset without bootstrap", func(t *testing.T) {
		calls, err := runAction(t, "--reset-dcr-registration", func(server mcp.ServerConfig, options app.MCPLoginOptions) error {
			_, _, prepErr := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, options.DCRAction)
			return prepErr
		})
		if err == nil || calls != 1 {
			t.Fatalf("missing reset calls=%d error=%v", calls, err)
		}
		profiles, loadErr := loadMCPLoginProfiles([]string{settings})
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		server, _ := profiles.OAuthServer("connector")
		_, getErr := server.OAuth.CredentialStore.Get(context.Background(), registrationKey)
		_ = profiles.Close()
		if !errors.Is(getErr, credentialstore.ErrNotFound) {
			t.Fatalf("rejected reset created registration state: %v", getErr)
		}
	})

	t.Run("ready reset and pending retry reach login seam", func(t *testing.T) {
		seed(t, true, true)
		ready := getRecord(t, registrationKey)
		var readyEnvelope struct {
			Generation   string `json:"generation"`
			Registration struct {
				ClientID string `json:"client_id"`
			} `json:"registration"`
		}
		if err := json.Unmarshal(ready.Value, &readyEnvelope); err != nil {
			t.Fatal(err)
		}
		grantKey := dcrLoginRecordKey("mecatl/mcp/oauth-dcr-credential-key/v1", "work", "operator", fixture.resource(), fixture.server.URL, "dcr", readyEnvelope.Registration.ClientID, readyEnvelope.Generation)
		grant := getRecord(t, grantKey)
		profiles, err := loadMCPLoginProfiles([]string{settings})
		if err != nil {
			t.Fatal(err)
		}
		server, _ := profiles.OAuthServer("connector")
		corruptGrant, err := server.OAuth.CredentialStore.Put(context.Background(), grantKey, []byte(`{"schema":"corrupt"}`), &grant.Version)
		_ = profiles.Close()
		if err != nil {
			t.Fatal(err)
		}
		calls, err := runAction(t, "--reset-dcr-registration", func(server mcp.ServerConfig, options app.MCPLoginOptions) error {
			_, _, prepErr := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, options.DCRAction)
			return prepErr
		})
		if err == nil || calls != 1 {
			t.Fatalf("corrupt grant reset calls=%d error=%v", calls, err)
		}
		assertUnchanged(t, registrationKey, ready)
		assertUnchanged(t, grantKey, corruptGrant)
		profiles, err = loadMCPLoginProfiles([]string{settings})
		if err != nil {
			t.Fatal(err)
		}
		server, _ = profiles.OAuthServer("connector")
		if _, err := server.OAuth.CredentialStore.Put(context.Background(), grantKey, grant.Value, &corruptGrant.Version); err != nil {
			t.Fatal(err)
		}
		_ = profiles.Close()

		for name, operation := range map[string]func(mcp.ServerConfig, app.MCPLoginOptions) error{
			"retry ready": func(server mcp.ServerConfig, _ app.MCPLoginOptions) error {
				_, _, prepErr := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, mcp.OAuthDCRLoginRetryRegistration)
				return prepErr
			},
			"backend failure": func(server mcp.ServerConfig, options app.MCPLoginOptions) error {
				unavailable := *server.OAuth
				unavailable.CredentialStore = unavailableMCPLoginStore{Store: server.OAuth.CredentialStore}
				_, _, prepErr := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, unavailable, options.DCRAction)
				return prepErr
			},
		} {
			t.Run(name, func(t *testing.T) {
				before := getRecord(t, registrationKey)
				flag := "--reset-dcr-registration"
				if name == "retry ready" {
					flag = "--retry-dcr-registration"
				}
				calls, actionErr := runAction(t, flag, operation)
				if actionErr == nil || calls != 1 {
					t.Fatalf("calls=%d error=%v", calls, actionErr)
				}
				assertUnchanged(t, registrationKey, before)
			})
		}

		beforeConflict := getRecord(t, registrationKey)
		for name, args := range map[string][]string{
			"conflicting flags": {"connector", "--permission-config", settings, "--reset-dcr-registration", "--retry-dcr-registration"},
			"grant reset flag":  {"connector", "--permission-config", settings, "--reset-dcr-grant"},
		} {
			t.Run(name, func(t *testing.T) {
				seamCalls := 0
				executeMCPLogin = func(context.Context, mcp.ServerConfig, oauthlogin.Options, app.MCPLoginOptions) error {
					seamCalls++
					return nil
				}
				usageErr := runMCPLogin(args, io.Discard)
				if !errors.Is(usageErr, errMCPLoginUsage) || seamCalls != 0 {
					t.Fatalf("error=%v seam calls=%d", usageErr, seamCalls)
				}
				assertUnchanged(t, registrationKey, beforeConflict)
			})
		}

		calls, err = runAction(t, "--reset-dcr-registration", func(server mcp.ServerConfig, options app.MCPLoginOptions) error {
			_, _, prepErr := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, options.DCRAction)
			return prepErr
		})
		if err != nil || calls != 1 {
			t.Fatalf("ready reset calls=%d error=%v", calls, err)
		}
		pending := getRecord(t, registrationKey)
		if pending.Version.Equal(ready.Version) || string(pending.Value) == string(ready.Value) {
			t.Fatal("ready reset did not replace the registration attempt")
		}
		calls, err = runAction(t, "--retry-dcr-registration", func(server mcp.ServerConfig, options app.MCPLoginOptions) error {
			_, _, prepErr := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, options.DCRAction)
			return prepErr
		})
		if err != nil || calls != 1 {
			t.Fatalf("pending retry calls=%d error=%v", calls, err)
		}
		retried := getRecord(t, registrationKey)
		if retried.Version.Equal(pending.Version) || string(retried.Value) == string(pending.Value) {
			t.Fatal("pending retry did not replace the registration attempt")
		}
	})

	t.Run("invalid action state and plain pending preserve record", func(t *testing.T) {
		for _, flag := range []string{"", "--reset-dcr-registration"} {
			before := getRecord(t, registrationKey)
			calls, err := runAction(t, flag, func(server mcp.ServerConfig, options app.MCPLoginOptions) error {
				_, _, prepErr := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, options.DCRAction)
				return prepErr
			})
			if err == nil || calls != 1 {
				t.Fatalf("flag %q calls=%d error=%v", flag, calls, err)
			}
			assertUnchanged(t, registrationKey, before)
		}
	})

	t.Run("corrupt registration cannot be reset or retried", func(t *testing.T) {
		profiles, err := loadMCPLoginProfiles([]string{settings})
		if err != nil {
			t.Fatal(err)
		}
		server, _ := profiles.OAuthServer("connector")
		before := getRecord(t, registrationKey)
		corrupt, err := server.OAuth.CredentialStore.Put(context.Background(), registrationKey, []byte(`{"schema":"corrupt"}`), &before.Version)
		_ = profiles.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, flag := range []string{"--reset-dcr-registration", "--retry-dcr-registration"} {
			calls, runErr := runAction(t, flag, func(server mcp.ServerConfig, options app.MCPLoginOptions) error {
				_, _, prepErr := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, options.DCRAction)
				return prepErr
			})
			if runErr == nil || calls != 1 {
				t.Fatalf("flag %q calls=%d error=%v", flag, calls, runErr)
			}
			assertUnchanged(t, registrationKey, corrupt)
		}
	})

	t.Run("non-DCR modifier reaches login seam and fails", func(t *testing.T) {
		legacy := filepath.Join(t.TempDir(), "legacy.yaml")
		if err := os.WriteFile(legacy, []byte(mcpLoginOAuthYAML("legacy", "local", filepath.Join(t.TempDir(), "legacy-credentials"))), 0o600); err != nil {
			t.Fatal(err)
		}
		calls := 0
		executeMCPLogin = func(ctx context.Context, server mcp.ServerConfig, runtimeOptions oauthlogin.Options, options app.MCPLoginOptions) error {
			calls++
			runtime, err := oauthlogin.New(runtimeOptions)
			if err != nil {
				return err
			}
			return app.LoginMCPWithOptions(ctx, server, runtime, options)
		}
		err := runMCPLogin([]string{"legacy", "--permission-config", legacy, "--reset-dcr-registration"}, io.Discard)
		if err == nil || calls != 1 {
			t.Fatalf("non-DCR modifier calls=%d error=%v", calls, err)
		}
	})
}

type unavailableMCPLoginStore struct {
	credentialstore.Store
}

func (unavailableMCPLoginStore) Get(context.Context, []byte) (credentialstore.Record, error) {
	return credentialstore.Record{}, credentialstore.ErrUnavailable
}

type mcpLoginDCRFixture struct {
	server        *httptest.Server
	registerCount int
	tokenCount    int
}

func newMCPLoginDCRFixture(t *testing.T) *mcpLoginDCRFixture {
	t.Helper()
	fixture := &mcpLoginDCRFixture{}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := fixture.server.URL
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "oauth-protected-resource"):
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": fixture.resource(), "authorization_servers": []string{issuer}, "scopes_supported": []string{"openid"}})
		case strings.Contains(r.URL.Path, ".well-known/oauth-authorization-server"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/oauth/authorize", "token_endpoint": issuer + "/oauth/token", "registration_endpoint": issuer + "/oauth/register",
				"scopes_supported": []string{"openid"}, "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code"},
				"token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"},
			})
		case r.URL.Path == "/oauth/register":
			fixture.registerCount++
			var request oauthex.ClientRegistrationMetadata
			_ = json.NewDecoder(r.Body).Decode(&request)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"client_id": "public-client", "token_endpoint_auth_method": "none", "redirect_uris": request.RedirectURIs,
				"grant_types": request.GrantTypes, "response_types": request.ResponseTypes, "scope": request.Scope,
			})
		case r.URL.Path == "/oauth/token":
			fixture.tokenCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token", "token_type": "Bearer", "expires_in": 3600, "scope": "openid"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *mcpLoginDCRFixture) resource() string { return f.server.URL + "/gw/mcp" }

func mcpLoginDCRYAML(fixture *mcpLoginDCRFixture, root string) string {
	return fmt.Sprintf(`mcp:
  mode: global
  servers:
    - name: connector
      url: %q
      auth:
        mode: oauth
        oauth:
          profile: work
          principal: operator
          issuer: %q
          client: {mode: dcr, dcr: {}}
          scopes: [openid]
          request_refresh_token: false
          credentials:
            mode: local
            local: {root: %q, key_env: MECATL_LOGIN_KEY}
          network: {additional_origins: [], private_origins: [%q], max_redirects: 0}
`, fixture.resource(), fixture.server.URL, root, fixture.server.URL)
}

func dcrLoginRecordKey(domain string, fields ...string) []byte {
	framed := []byte(domain)
	var size [4]byte
	for _, field := range fields {
		if len(field) > math.MaxUint32 {
			panic("test fixture field too large")
		}
		binary.BigEndian.PutUint32(size[:], uint32(len(field))) // #nosec G115 -- test field is bounded above.
		framed = append(framed, size[:]...)
		framed = append(framed, field...)
	}
	digest := sha256.Sum256(framed)
	return digest[:]
}
