package app_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func writeDCRSettings(t *testing.T, fixture *loginFixture, root string) (string, func(string) (string, bool)) {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte("dcr-acceptance-encryption-key-32"))
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	body := fmt.Sprintf(`mcp:
  mode: global
  servers:
    - name: protected
      url: %q
      auth:
        mode: oauth
        oauth:
          profile: connector
          principal: local-user
          issuer: %q
          client: {mode: dcr, dcr: {}}
          scopes: [openid]
          request_refresh_token: false
          credentials:
            mode: local
            local: {root: %q, key_env: MECATL_DCR_KEY}
          network:
            additional_origins: []
            private_origins: [%q]
            max_redirects: 0
`, fixture.resource(), fixture.issuer(), root, fixture.origin())
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return settings, func(name string) (string, bool) {
		if name == "MECATL_DCR_KEY" {
			return key, true
		}
		return "", false
	}
}

func loginDCRProfile(t *testing.T, fixture *loginFixture, settings string, lookup func(string) (string, bool), runtime *oauthlogin.Runtime) {
	t.Helper()
	resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{settings}})
	profiles, err := loadAcceptanceMCPProfiles(t, resolver.OperatorMCP(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	cfg, ok := profiles.OAuthServer("protected")
	if !ok {
		t.Fatal("DCR profile was not resolved")
	}
	mcp.TrustOAuthCertificateForTest(t, cfg.OAuth, fixture.server.Certificate())
	if err := app.LoginMCP(context.Background(), cfg, runtime); err != nil {
		t.Fatalf("DCR login: %v", err)
	}
}

func TestDirectMCPDCR_RegistrationFailureCategorySurvivesLoginWrapping(t *testing.T) {
	fixture := newDCRLoginFixture(t)
	fixture.badDCRResponse = true
	fixture.dcrRegAccess = "registration-secret-must-not-surface"
	settings, lookup := writeDCRSettings(t, fixture, filepath.Join(t.TempDir(), "credentials"))
	resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{settings}})
	profiles, err := loadAcceptanceMCPProfiles(t, resolver.OperatorMCP(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	cfg, ok := profiles.OAuthServer("protected")
	if !ok {
		t.Fatal("DCR profile was not resolved")
	}
	mcp.TrustOAuthCertificateForTest(t, cfg.OAuth, fixture.server.Certificate())
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: &loginBrowser{client: fixture.server.Client()}})
	if err != nil {
		t.Fatal(err)
	}

	loginErr := app.LoginMCPWithOptions(context.Background(), cfg, runtime, app.MCPLoginOptions{})
	if !errors.Is(loginErr, mcp.ErrOAuthDCRRecoveryRequired) ||
		mcp.OAuthDCRRecoveryCategoryOf(loginErr) != mcp.OAuthDCRRecoveryResponseInvalid {
		t.Fatalf("login error lost DCR recovery category: %v, category %v", loginErr, mcp.OAuthDCRRecoveryCategoryOf(loginErr))
	}
	if !errors.Is(loginErr, app.ErrMCPLoginAuthorization) || errors.Is(loginErr, app.ErrMCPLoginConnect) {
		t.Fatalf("DCR registration failure category = %v, want authorization and not connect", loginErr)
	}
	if strings.Contains(loginErr.Error(), fixture.dcrRegAccess) {
		t.Fatalf("DCR registration failure leaked response content: %q", loginErr)
	}
}

func TestDirectMCPDCR_Scenario2_RegistersAuthorizesAndLists(t *testing.T) {
	fixture := newDCRLoginFixture(t)
	settings, lookup := writeDCRSettings(t, fixture, filepath.Join(t.TempDir(), "credentials"))
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	loginDCRProfile(t, fixture, settings, lookup, runtime)
	fixture.mu.Lock()
	register, token, basic, forms, authenticated := fixture.register, fixture.token, fixture.basicRequests, append([]url.Values(nil), fixture.tokenForms...), fixture.authorized
	fixture.mu.Unlock()
	if register != 1 || token != 1 || basic != 0 || browser.calls.Load() != 1 || authenticated == 0 {
		t.Fatalf("login register=%d token=%d Basic=%d browser=%d authenticated=%d", register, token, basic, browser.calls.Load(), authenticated)
	}
	if len(forms) != 1 || forms[0].Get("client_id") != loginClientID || forms[0].Get("client_secret") != "" || forms[0].Get("client_assertion") != "" || forms[0].Get("code_verifier") == "" {
		t.Fatalf("public token form = %v", forms)
	}

	diag := &acceptanceDiag{}
	built, err := buildDCRAcceptanceMCP(t, fixture, settings, lookup, diag, mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("read", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("complete")))
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	surfaces, err := runAcceptanceTool(built, "use the listed read tool")
	if err != nil {
		t.Fatalf("run: %v diagnostics=%v surfaces=%v", err, diagnosticSurfaces(diag), surfaces)
	}
	if !strings.Contains(strings.Join(surfaces, "\n"), "fixture-ready") {
		t.Fatalf("registered/authorized tool was not listed and callable: %v", surfaces)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.register != 1 || fixture.token != 1 || fixture.basicRequests != 0 || fixture.toolCalls != 1 {
		t.Fatalf("serving path register=%d token=%d Basic=%d tools=%d", fixture.register, fixture.token, fixture.basicRequests, fixture.toolCalls)
	}
}

func TestDirectMCPDCR_Scenario2_ReauthorizationRedirectAndScopeBinding(t *testing.T) {
	fixture := newDCRLoginFixture(t)
	root := filepath.Join(t.TempDir(), "credentials")
	settings, lookup := writeDCRSettings(t, fixture, root)
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	loginDCRProfile(t, fixture, settings, lookup, runtime)

	resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{settings}})
	profiles, err := loadAcceptanceMCPProfiles(t, resolver.OperatorMCP(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	cfg, ok := profiles.OAuthServer("protected")
	if !ok {
		t.Fatal("DCR profile was not resolved")
	}
	mcp.TrustOAuthCertificateForTest(t, cfg.OAuth, fixture.server.Certificate())
	resolved, callbackPath, err := mcp.PrepareOAuthDCRLogin(context.Background(), cfg.URL, *cfg.OAuth, mcp.OAuthDCRLoginReuse)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OAuth = &resolved
	if err := profiles.Close(); err != nil {
		t.Fatal(err)
	}
	expireAcceptanceCredential(t, root, base64.StdEncoding.EncodeToString([]byte("dcr-acceptance-encryption-key-32")), cfg)

	fixture.mu.Lock()
	fixture.dcrAccessToken = "replacement-dcr-access"
	beforeRegister, beforeToken, beforeRefresh := fixture.register, fixture.token, fixture.refresh
	firstRegisteredURI := fixture.registeredURI
	fixture.mu.Unlock()
	loginDCRProfile(t, fixture, settings, lookup, runtime)

	browser.mu.Lock()
	presented := append([]string(nil), browser.urls...)
	browser.mu.Unlock()
	if len(presented) != 2 {
		t.Fatalf("explicit authorization presentations = %d, want 2", len(presented))
	}
	first, err := url.Parse(presented[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := url.Parse(presented[1])
	if err != nil {
		t.Fatal(err)
	}
	firstRedirect, err := url.Parse(first.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	secondRedirect, err := url.Parse(second.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	registeredRedirect, err := url.Parse(firstRegisteredURI)
	if err != nil {
		t.Fatal(err)
	}
	if registeredRedirect.Path != callbackPath || firstRedirect.Path != callbackPath || secondRedirect.Path != callbackPath || firstRedirect.Port() == secondRedirect.Port() {
		t.Fatalf("registration callback binding changed: registered=%q first=%q second=%q", firstRegisteredURI, firstRedirect, secondRedirect)
	}
	if first.Query().Get("state") == second.Query().Get("state") || first.Query().Get("code_challenge") == second.Query().Get("code_challenge") {
		t.Fatal("explicit re-login reused state or PKCE challenge")
	}
	for _, authorization := range []*url.URL{first, second} {
		if authorization.Query().Get("scope") != "openid" || authorization.Query().Get("resource") != fixture.resource() {
			t.Fatalf("authorization scope/resource drift: %v", authorization.Query())
		}
	}
	fixture.mu.Lock()
	registers, tokens, refreshes := fixture.register, fixture.token, fixture.refresh
	fixture.mu.Unlock()
	if registers != beforeRegister || tokens != beforeToken+1 || refreshes != beforeRefresh {
		t.Fatalf("explicit re-login register=%d token=%d refresh=%d; before=%d/%d/%d", registers, tokens, refreshes, beforeRegister, beforeToken, beforeRefresh)
	}

	diag := &acceptanceDiag{}
	built, err := buildDCRAcceptanceMCP(t, fixture, settings, lookup, diag, mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("reauthorized", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("complete")))
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	surfaces, err := runAcceptanceTool(built, "use the harmless read tool after explicit re-login")
	if err != nil {
		t.Fatalf("run: %v diagnostics=%v", err, diagnosticSurfaces(diag))
	}
	if !strings.Contains(strings.Join(surfaces, "\n"), "fixture-ready") {
		t.Fatalf("replacement grant did not authorize harmless tool use: %v", surfaces)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.register != registers || fixture.token != tokens || fixture.refresh != refreshes || fixture.toolCalls == 0 {
		t.Fatalf("post-login use register=%d token=%d refresh=%d tools=%d", fixture.register, fixture.token, fixture.refresh, fixture.toolCalls)
	}
}

func TestDirectMCPDCR_Scenario2_RestartRestoresRegistrationGrantAndReadTool(t *testing.T) {
	fixture := newDCRLoginFixture(t)
	root := filepath.Join(t.TempDir(), "credentials")
	settings, lookup := writeDCRSettings(t, fixture, root)
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	loginDCRProfile(t, fixture, settings, lookup, runtime)
	if browser.calls.Load() != 1 {
		t.Fatalf("explicit presenter calls=%d", browser.calls.Load())
	}

	diag := &acceptanceDiag{}
	built, err := buildDCRAcceptanceMCP(t, fixture, settings, lookup, diag, mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("restart", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("complete")))
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	surfaces, err := runAcceptanceTool(built, "ordinary restart")
	if err != nil {
		t.Fatalf("run: %v diagnostics=%v surfaces=%v", err, diagnosticSurfaces(diag), surfaces)
	}
	projections := append(surfaces, diagnosticSurfaces(diag)...)
	settingsBytes, readErr := os.ReadFile(settings)
	if readErr != nil {
		t.Fatal(readErr)
	}
	projections = append(projections, string(settingsBytes))
	joined := strings.Join(projections, "\n")
	browser.mu.Lock()
	presented := append([]string(nil), browser.urls...)
	browser.mu.Unlock()
	fixture.mu.Lock()
	register, token, tools := fixture.register, fixture.token, fixture.toolCalls
	fixture.mu.Unlock()
	if browser.calls.Load() != 1 || register != 1 || token != 1 || tools != 1 {
		t.Fatalf("restart presenter=%d register=%d token=%d tools=%d", browser.calls.Load(), register, token, tools)
	}
	if len(presented) != 1 {
		t.Fatalf("explicit presenter URLs=%d", len(presented))
	}
	u, _ := url.Parse(presented[0])
	if strings.Contains(joined, presented[0]) || strings.Contains(joined, u.Query().Get("state")) || strings.Contains(joined, "login-code-canary") {
		t.Fatalf("authorization material escaped host presenter: %s", joined)
	}
}

func TestInvariant_direct_mcp_dcr_secret_redaction(t *testing.T) {
	const (
		clientID           = "app-client-id-redaction-canary"
		accessToken        = "app-access-token-redaction-canary"
		registrationAccess = "app-registration-access-redaction-canary"
		unsolicitedRefresh = "app-unsolicited-refresh-redaction-canary"
	)
	fixture := newDCRLoginFixture(t)
	fixture.dcrClientID, fixture.dcrAccessToken, fixture.dcrRegAccess = clientID, accessToken, registrationAccess
	root := filepath.Join(t.TempDir(), "credentials")
	settings, lookup := writeDCRSettings(t, fixture, root)
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	loginDCRProfile(t, fixture, settings, lookup, runtime)
	browser.mu.Lock()
	presented := append([]string(nil), browser.urls...)
	browser.mu.Unlock()
	if len(presented) != 1 {
		t.Fatalf("presented URLs=%d", len(presented))
	}
	presentedURL, err := url.Parse(presented[0])
	if err != nil {
		t.Fatal(err)
	}
	transient := []string{presented[0], "login-code-canary", presentedURL.Query().Get("state"), registrationAccess, unsolicitedRefresh}

	diag := &acceptanceDiag{}
	built, err := buildDCRAcceptanceMCP(t, fixture, settings, lookup, diag, mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("redaction", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("complete")))
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	model, err := runAcceptanceTool(built, "redaction projection")
	if err != nil {
		t.Fatal(err)
	}
	projections := append(model, diagnosticSurfaces(diag)...)

	bad := newDCRLoginFixture(t)
	bad.dcrClientID, bad.dcrAccessToken, bad.dcrRegAccess, bad.dcrRefreshToken = clientID, accessToken, registrationAccess, unsolicitedRefresh
	badSettings, badLookup := writeDCRSettings(t, bad, filepath.Join(t.TempDir(), "bad-credentials"))
	resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{badSettings}})
	profiles, loadErr := loadAcceptanceMCPProfiles(t, resolver.OperatorMCP(), badLookup)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	defer profiles.Close()
	badCfg, ok := profiles.OAuthServer("protected")
	if !ok {
		t.Fatal("bad DCR profile missing")
	}
	badBrowser := &loginBrowser{client: bad.server.Client()}
	badRuntime, runtimeErr := oauthlogin.New(oauthlogin.Options{Launcher: badBrowser})
	if runtimeErr != nil {
		t.Fatal(runtimeErr)
	}
	loginErr := app.LoginMCP(context.Background(), badCfg, badRuntime)
	if loginErr == nil {
		t.Fatal("unsolicited refresh token was accepted by app login")
	}
	projections = append(projections, loginErr.Error())

	settingsBytes, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	projections = append(projections, string(settingsBytes))
	for _, projection := range projections {
		for _, secret := range append([]string{clientID, accessToken}, transient...) {
			if secret != "" && strings.Contains(projection, secret) {
				t.Fatalf("non-credential projection leaked %q", secret)
			}
		}
	}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		value, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, secret := range append([]string{clientID, accessToken}, transient...) {
			if secret != "" && strings.Contains(string(value), secret) {
				t.Fatalf("encrypted credential artifact %s exposed %q", path, secret)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDirectMCPDCR_Scenario3_RestartIdentityMismatchFailsClosed(t *testing.T) {
	fixture := newDCRLoginFixture(t)
	fixture.dcrClientID = "restart-client-canary"
	fixture.dcrAccessToken = "restart-access-canary"
	root := filepath.Join(t.TempDir(), "credentials")
	settings, lookup := writeDCRSettings(t, fixture, root)
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	loginDCRProfile(t, fixture, settings, lookup, runtime)
	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	mismatchSettings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(mismatchSettings, []byte(strings.Replace(string(body), "principal: local-user", "principal: other-user", 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	diag := &acceptanceDiag{}
	built, err := buildDCRAcceptanceMCP(t, fixture, mismatchSettings, lookup, diag, mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("mismatch", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("complete")))
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	model, runErr := runAcceptanceTool(built, "restart with mismatched identity")
	projections := append(model, diagnosticSurfaces(diag)...)
	if runErr != nil {
		projections = append(projections, runErr.Error())
	}
	fixture.mu.Lock()
	registrations, tokens, tools := fixture.register, fixture.token, fixture.toolCalls
	fixture.mu.Unlock()
	if browser.calls.Load() != 1 || registrations != 1 || tokens != 1 || tools != 0 {
		t.Fatalf("mismatch performed hidden work: browser=%d register=%d token=%d tools=%d", browser.calls.Load(), registrations, tokens, tools)
	}
	joined := strings.Join(projections, "\n")
	if !strings.Contains(joined, "login required") && !strings.Contains(joined, "unknown tool") {
		t.Fatalf("host output omitted safe failure category: %s", joined)
	}
	for _, secret := range []string{"restart-client-canary", "restart-access-canary", "login-code-canary"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("host mismatch output leaked %q", secret)
		}
	}
}
