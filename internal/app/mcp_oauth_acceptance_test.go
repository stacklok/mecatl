package app_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/internal/configgen"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const acceptanceStaticBearer = "acceptance-static-bearer-canary"

func TestMCPOAuthHermeticAcceptance(t *testing.T) {
	t.Run("login restore refresh restart successor refresh", func(t *testing.T) {
		fixture := newLoginFixture(t)
		credentialRoot := filepath.Join(t.TempDir(), "credentials")
		settings, lookup, keyCanary := writeAcceptanceOAuthSettings(t, fixture, credentialRoot)
		var surfaces []string
		canaries := acceptanceCanaries(keyCanary)
		defer func() { assertNoAcceptanceSecrets(t, surfaces, canaries) }()

		resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{settings}})
		profiles, err := loadAcceptanceMCPProfiles(t, resolver.OperatorMCP(), lookup)
		collectError(&surfaces, err)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("operator login profile resolution failed")
		}
		serverConfig, ok := profiles.OAuthServer("PrOtEcTeD")
		if !ok {
			assertNoAcceptanceSecrets(t, surfaces, canaries)
			t.Fatal("resolved operator profile did not select the protected OAuth server")
		}
		browser := &loginBrowser{client: fixture.server.Client()}
		runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
		collectError(&surfaces, err)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal(err)
		}
		err = app.LoginMCP(context.Background(), serverConfig, runtime)
		collectError(&surfaces, err)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("explicit MCP login failed")
		}
		if err := profiles.Close(); err != nil {
			collectError(&surfaces, err)
			assertNoAcceptanceSecrets(t, surfaces, canaries)
			t.Fatal("login process profile close failed")
		}
		if err := profiles.Close(); err != nil {
			collectError(&surfaces, err)
			assertNoAcceptanceSecrets(t, surfaces, canaries)
			t.Fatal("idempotent login process profile close failed")
		}
		loginCounts := fixture.snapshot()
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if browser.calls.Load() != 1 || loginCounts.authorize != 1 || loginCounts.token != 1 || loginCounts.refresh != 0 || loginCounts.opened < 1 || loginCounts.closed != 1 {
			t.Fatalf("login phase counts browser=%d authorize=%d token=%d refresh=%d sessions=%d/%d", browser.calls.Load(), loginCounts.authorize, loginCounts.token, loginCounts.refresh, loginCounts.opened, loginCounts.closed)
		}
		assertEncryptedCredentialArtifacts(t, credentialRoot, keyCanary)

		diagA := &acceptanceDiag{}
		builtA, err := buildAcceptanceMCP(t, settings, lookup, diagA, mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("warm", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("warm complete"),
		))
		collectError(&surfaces, err)
		surfaces = append(surfaces, diagnosticSurfaces(diagA)...)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("first serving process Build failed")
		}
		defer builtA.Close()
		warm, err := runAcceptanceTool(builtA, "warm restore")
		surfaces = append(surfaces, warm...)
		collectError(&surfaces, err)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("warm restore run failed")
		}
		warmCounts := fixture.snapshot()
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if warmCounts.authorize != 1 || warmCounts.token != 1 || warmCounts.refresh != 0 || browser.calls.Load() != 1 || warmCounts.toolCalls != 1 {
			t.Fatalf("warm restore unexpectedly authorized or refreshed: authorize=%d token=%d refresh=%d browser=%d tools=%d", warmCounts.authorize, warmCounts.token, warmCounts.refresh, browser.calls.Load(), warmCounts.toolCalls)
		}
		surfaces = append(surfaces, diagnosticSurfaces(diagA)...)
		builtA.Close()
		builtA.Close()
		closedA := fixture.snapshot()
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if closedA.closed <= warmCounts.closed {
			t.Fatal("process A close did not close its MCP session before profile teardown")
		}

		expireAcceptanceCredential(t, credentialRoot, keyCanary, serverConfig)
		assertEncryptedCredentialArtifacts(t, credentialRoot, keyCanary)

		diagB := &acceptanceDiag{}
		builtB, err := buildAcceptanceMCP(t, settings, lookup, diagB, mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("refresh", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("refresh complete"),
		))
		collectError(&surfaces, err)
		surfaces = append(surfaces, diagnosticSurfaces(diagB)...)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("refresh serving process Build failed")
		}
		defer builtB.Close()
		refreshed, err := runAcceptanceTool(builtB, "lazy refresh")
		surfaces = append(surfaces, refreshed...)
		collectError(&surfaces, err)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("lazy refresh run failed")
		}
		refreshCounts := fixture.snapshot()
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if refreshCounts.refresh != 1 || refreshCounts.token != 2 || refreshCounts.authorize != 1 || refreshCounts.toolCalls != 2 || refreshCounts.unexpectedAuth != 0 {
			t.Fatalf("refresh phase counts authorize=%d token=%d refresh=%d tools=%d unexpected-auth=%d", refreshCounts.authorize, refreshCounts.token, refreshCounts.refresh, refreshCounts.toolCalls, refreshCounts.unexpectedAuth)
		}
		surfaces = append(surfaces, diagnosticSurfaces(diagB)...)
		builtB.Close()
		builtB.Close()
		closedB := fixture.snapshot()
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if closedB.closed <= refreshCounts.closed {
			t.Fatal("process B close did not close its MCP session before profile teardown")
		}
		assertEncryptedCredentialArtifacts(t, credentialRoot, keyCanary)

		diagC := &acceptanceDiag{}
		builtC, err := buildAcceptanceMCP(t, settings, lookup, diagC, mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("restart", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("restart complete"),
		))
		collectError(&surfaces, err)
		surfaces = append(surfaces, diagnosticSurfaces(diagC)...)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("restart serving process Build failed")
		}
		defer builtC.Close()
		restarted, err := runAcceptanceTool(builtC, "second restart")
		surfaces = append(surfaces, restarted...)
		collectError(&surfaces, err)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("second restart run failed")
		}
		restartCounts := fixture.snapshot()
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if restartCounts.authorize != 1 || restartCounts.token != 2 || restartCounts.refresh != 1 || restartCounts.toolCalls != 3 || restartCounts.unexpectedAuth != 0 {
			t.Fatalf("second restart did not restore rotated credential: authorize=%d token=%d refresh=%d tools=%d unexpected-auth=%d", restartCounts.authorize, restartCounts.token, restartCounts.refresh, restartCounts.toolCalls, restartCounts.unexpectedAuth)
		}

		builtC.Close()
		builtC.Close()
		closedC := fixture.snapshot()
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if closedC.closed <= restartCounts.closed {
			t.Fatal("process C close did not close its restored MCP session")
		}

		expireAcceptanceCredential(t, credentialRoot, keyCanary, serverConfig)
		assertEncryptedCredentialArtifacts(t, credentialRoot, keyCanary)

		diagD := &acceptanceDiag{}
		builtD, err := buildAcceptanceMCP(t, settings, lookup, diagD, mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("successor", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("successor complete"),
		))
		collectError(&surfaces, err)
		surfaces = append(surfaces, diagnosticSurfaces(diagD)...)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("successor refresh serving process Build failed")
		}
		defer builtD.Close()
		successor, err := runAcceptanceTool(builtD, "successor refresh")
		surfaces = append(surfaces, successor...)
		collectError(&surfaces, err)
		surfaces = append(surfaces, diagnosticSurfaces(diagD)...)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("successor refresh run failed")
		}
		successorCounts := fixture.snapshot()
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if successorCounts.authorize != 1 || successorCounts.token != 3 || successorCounts.refresh != 2 || successorCounts.toolCalls != 4 || successorCounts.unexpectedAuth != 0 || browser.calls.Load() != 1 || fixture.refreshRequestCount(loginRefreshToken) != 1 || fixture.refreshRequestCount(loginRotatedRefreshToken) != 1 {
			t.Fatalf("successor refresh counts authorize=%d token=%d refresh=%d tools=%d unexpected-auth=%d browser=%d", successorCounts.authorize, successorCounts.token, successorCounts.refresh, successorCounts.toolCalls, successorCounts.unexpectedAuth, browser.calls.Load())
		}
		assertEncryptedCredentialArtifacts(t, credentialRoot, keyCanary)
		builtD.Close()
		builtD.Close()
		surfaces = append(surfaces, diagnosticSurfaces(diagD)...)
		finalCounts := fixture.snapshot()
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if finalCounts.closed <= successorCounts.closed {
			t.Fatal("process D close did not close its refreshed MCP session")
		}

		reference, err := os.ReadFile(filepath.Join("..", "..", "user-docs", "reference", "configuration.md"))
		collectError(&surfaces, err)
		if err == nil {
			surfaces = append(surfaces, string(reference))
		}
		surfaces = append(surfaces, configgen.Skeleton())
		assertNoAcceptanceSecrets(t, surfaces, canaries)
	})

	t.Run("headless clean store is fail soft", func(t *testing.T) {
		fixture := newLoginFixture(t)
		settings, lookup, keyCanary := writeAcceptanceOAuthSettings(t, fixture, filepath.Join(t.TempDir(), "clean"))
		canaries := acceptanceCanaries(keyCanary)
		var surfaces []string
		defer func() { assertNoAcceptanceSecrets(t, surfaces, canaries) }()
		diag := &acceptanceDiag{}
		built, buildErr := buildAcceptanceMCPConfig(t, settings, lookup, diag, mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("missing", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("headless complete"),
		), true)
		collectError(&surfaces, buildErr)
		surfaces = append(surfaces, diagnosticSurfaces(diag)...)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if buildErr != nil {
			t.Fatal("headless clean-store Build failed")
		}
		defer built.Close()
		runSurfaces, runErr := runAcceptanceTool(built, "headless")
		surfaces = append(surfaces, runSurfaces...)
		collectError(&surfaces, runErr)
		surfaces = append(surfaces, diagnosticSurfaces(diag)...)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if runErr != nil {
			t.Fatal("headless clean-store run failed")
		}
		counts := fixture.snapshot()
		if counts.authorize != 0 || counts.token != 0 || counts.refresh != 0 || counts.opened != 0 || counts.toolCalls != 0 {
			t.Fatalf("headless clean store initiated authentication: authorize=%d token=%d refresh=%d sessions=%d tools=%d", counts.authorize, counts.token, counts.refresh, counts.opened, counts.toolCalls)
		}
		joined := strings.Join(surfaces, "\n")
		if !strings.Contains(joined, "MCP OAuth login required") || !strings.Contains(joined, "mecated mcp login protected") || !strings.Contains(joined, "unknown tool") {
			t.Fatal("headless outputs omitted the safe remediation or unknown-tool result")
		}
		assertNoAcceptanceSecrets(t, surfaces, canaries)
	})

	t.Run("token failure is redacted", func(t *testing.T) {
		fixture := newLoginFixture(t)
		fixture.failToken = true
		settings, lookup, keyCanary := writeAcceptanceOAuthSettings(t, fixture, filepath.Join(t.TempDir(), "failure"))
		canaries := acceptanceCanaries(keyCanary)
		var surfaces []string
		defer func() { assertNoAcceptanceSecrets(t, surfaces, canaries) }()

		resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{settings}})
		profiles, err := loadAcceptanceMCPProfiles(t, resolver.OperatorMCP(), lookup)
		collectError(&surfaces, err)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("token-failure profile resolution failed")
		}
		defer profiles.Close()
		serverConfig, ok := profiles.OAuthServer("protected")
		if !ok {
			t.Fatal("token-failure profile selection failed")
		}
		browser := &loginBrowser{client: fixture.server.Client()}
		runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
		collectError(&surfaces, err)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err != nil {
			t.Fatal("token-failure runtime construction failed")
		}
		err = app.LoginMCP(context.Background(), serverConfig, runtime)
		collectError(&surfaces, err)
		assertNoAcceptanceSecrets(t, surfaces, canaries)
		if err == nil {
			t.Fatal("token failure path was not triggered")
		}
		counts := fixture.snapshot()
		if counts.token != 1 || counts.authorize != 1 {
			t.Fatalf("token failure path counts authorize=%d token=%d", counts.authorize, counts.token)
		}
	})

	for _, mode := range []string{"static_bearer", "none"} {
		t.Run(mode+" regression", func(t *testing.T) {
			fixture := newLoginFixture(t)
			canaries := acceptanceCanaries("")
			var surfaces []string
			defer func() { assertNoAcceptanceSecrets(t, surfaces, canaries) }()
			lookups := 0
			var body string
			lookup := func(name string) (string, bool) {
				lookups++
				if name == "MECATL_ACCEPTANCE_STATIC" {
					return acceptanceStaticBearer, true
				}
				return "", false
			}
			if mode == "static_bearer" {
				fixture.acceptedBearer = acceptanceStaticBearer
				body = fmt.Sprintf("mcp:\n  servers:\n    - name: protected\n      url: %q\n      auth:\n        mode: static_bearer\n        static_bearer: {token_env: MECATL_ACCEPTANCE_STATIC}\n", fixture.resource())
			} else {
				fixture.publicResource = true
				body = fmt.Sprintf("mcp:\n  servers:\n    - name: protected\n      url: %q\n      auth: {mode: none}\n", fixture.resource())
			}
			settings := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			diag := &acceptanceDiag{}
			built, buildErr := buildAcceptanceMCP(t, settings, lookup, diag, mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("mode", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("mode complete"),
			))
			collectError(&surfaces, buildErr)
			surfaces = append(surfaces, diagnosticSurfaces(diag)...)
			assertNoAcceptanceSecrets(t, surfaces, canaries)
			if buildErr != nil {
				t.Fatalf("%s Build failed", mode)
			}
			defer built.Close()
			runSurfaces, runErr := runAcceptanceTool(built, mode)
			surfaces = append(surfaces, runSurfaces...)
			collectError(&surfaces, runErr)
			surfaces = append(surfaces, diagnosticSurfaces(diag)...)
			assertNoAcceptanceSecrets(t, surfaces, canaries)
			if runErr != nil {
				t.Fatalf("%s run failed", mode)
			}
			if !strings.Contains(strings.Join(surfaces, "\n"), "fixture-ready") {
				t.Fatalf("%s tool result was not model-visible", mode)
			}
			counts := fixture.snapshot()
			if counts.metadata != 0 || counts.authorize != 0 || counts.token != 0 || counts.refresh != 0 || counts.toolCalls != 1 {
				t.Fatalf("%s unexpectedly used OAuth: metadata=%d authorize=%d token=%d refresh=%d tools=%d", mode, counts.metadata, counts.authorize, counts.token, counts.refresh, counts.toolCalls)
			}
			if mode == "static_bearer" && lookups != 1 {
				t.Fatalf("static bearer lookups=%d, want 1", lookups)
			}
			if mode == "none" && lookups != 0 {
				t.Fatalf("none profile performed %d secret lookups", lookups)
			}
		})
	}
}

func TestDirectMCPDCRLifecycleRecoveryAcceptance(t *testing.T) {
	fixture := newDCRLoginFixture(t)
	credentialRoot := filepath.Join(t.TempDir(), "credentials")
	settings, lookup, _ := writeDCRAcceptanceOAuthSettings(t, fixture, credentialRoot, "stable")

	load := func(settings string) (*cliconfig.MCPProfiles, mcp.ServerConfig) {
		resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{settings}})
		profiles, err := loadAcceptanceMCPProfiles(t, resolver.OperatorMCP(), lookup)
		if err != nil {
			t.Fatal(err)
		}
		server, ok := profiles.OAuthServer("protected")
		if !ok {
			profiles.Close()
			t.Fatal("DCR server was not resolved")
		}
		mcp.TrustOAuthCertificateForTest(t, server.OAuth, fixture.server.Certificate())
		return profiles, server
	}
	login := func(server mcp.ServerConfig, action mcp.OAuthDCRLoginAction) {
		browser := &loginBrowser{client: fixture.server.Client()}
		runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.LoginMCPWithOptions(context.Background(), server, runtime, app.MCPLoginOptions{DCRAction: action}); err != nil {
			t.Fatal(err)
		}
		if browser.calls.Load() != 1 {
			t.Fatalf("browser calls = %d, want 1", browser.calls.Load())
		}
	}

	profiles, server := load(settings)
	login(server, mcp.OAuthDCRLoginReuse)
	if err := profiles.Close(); err != nil {
		t.Fatal(err)
	}
	seeded := fixture.snapshot()
	if seeded.registered != 1 || seeded.authorize != 1 || seeded.token != 1 || seeded.toolCalls != 0 {
		t.Fatalf("initial DCR login did not complete registration/authorization: %+v", seeded)
	}

	drifted, _, _ := writeDCRAcceptanceOAuthSettings(t, fixture, credentialRoot, "drifted")
	profiles, server = load(drifted)
	driftBrowser := &loginBrowser{client: fixture.server.Client()}
	driftRuntime, err := oauthlogin.New(oauthlogin.Options{Launcher: driftBrowser})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.LoginMCP(context.Background(), server, driftRuntime); !errors.Is(err, mcp.ErrOAuthDCRRecoveryRequired) || mcp.OAuthDCRRecoveryCategoryOf(err) != mcp.OAuthDCRRecoveryResetRequired {
		t.Fatalf("identity drift login error = %v, want reset-required recovery", err)
	}
	if err := profiles.Close(); err != nil {
		t.Fatal(err)
	}
	diag := &acceptanceDiag{}
	built, err := buildDCRAcceptanceMCP(t, fixture, drifted, lookup, diag, mockllm.New(mockllm.TextTurn("unreached")))
	if err != nil {
		t.Fatal(err)
	}
	built.Close()
	afterDrift := fixture.snapshot()
	if driftBrowser.calls.Load() != 0 || afterDrift.registered != seeded.registered || afterDrift.authorize != seeded.authorize || afterDrift.token != seeded.token || afterDrift.toolCalls != seeded.toolCalls || afterDrift.authenticated != seeded.authenticated {
		t.Fatalf("identity drift had OAuth or protected-tool side effects: before=%+v after=%+v", seeded, afterDrift)
	}
	if got := strings.Join(diagnosticSurfaces(diag), "\n"); !strings.Contains(got, "MCP OAuth DCR valid ready registration identity differs from current profile, principal, canonical resource, or exact issuer") || !strings.Contains(got, "--reset-dcr-registration") {
		t.Fatalf("identity drift omitted reset-required diagnostic: %q", diagnosticSurfaces(diag))
	}

	profiles, server = load(drifted)
	login(server, mcp.OAuthDCRLoginResetRegistration)
	if err := profiles.Close(); err != nil {
		t.Fatal(err)
	}
	afterReset := fixture.snapshot()
	if afterReset.registered != seeded.registered+1 || afterReset.authorize != seeded.authorize+1 || afterReset.token != seeded.token+1 {
		t.Fatalf("reset did not continue through registration and authorization: before=%+v after=%+v", seeded, afterReset)
	}
	verifyDCRAcceptanceRead(t, fixture, drifted, lookup)

	retrySettings, _, _ := writeDCRAcceptanceOAuthSettings(t, fixture, filepath.Join(t.TempDir(), "retry-credentials"), "retry")
	profiles, server = load(retrySettings)
	if _, _, err := mcp.PrepareOAuthDCRLogin(context.Background(), server.URL, *server.OAuth, mcp.OAuthDCRLoginReuse); err != nil {
		t.Fatal(err)
	}
	beforeRetry := fixture.snapshot()
	login(server, mcp.OAuthDCRLoginRetryRegistration)
	if err := profiles.Close(); err != nil {
		t.Fatal(err)
	}
	afterRetry := fixture.snapshot()
	if afterRetry.registered != beforeRetry.registered+1 || afterRetry.authorize != beforeRetry.authorize+1 || afterRetry.token != beforeRetry.token+1 {
		t.Fatalf("retry did not continue through registration and authorization: before=%+v after=%+v", beforeRetry, afterRetry)
	}
	verifyDCRAcceptanceRead(t, fixture, retrySettings, lookup)

	pendingRoot := filepath.Join(t.TempDir(), "pending-credentials")
	pendingSettings, pendingLookup, _ := writeDCRAcceptanceOAuthSettings(t, fixture, pendingRoot, "pending")
	pendingResolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{pendingSettings}})
	pendingProfiles, err := loadAcceptanceMCPProfiles(t, pendingResolver.OperatorMCP(), pendingLookup)
	if err != nil {
		t.Fatal(err)
	}
	pendingServer, ok := pendingProfiles.OAuthServer("protected")
	if !ok {
		t.Fatal("pending DCR server was not resolved")
	}
	mcp.TrustOAuthCertificateForTest(t, pendingServer.OAuth, fixture.server.Certificate())
	if _, _, err := mcp.PrepareOAuthDCRLogin(context.Background(), pendingServer.URL, *pendingServer.OAuth, mcp.OAuthDCRLoginReuse); err != nil {
		t.Fatal(err)
	}
	if err := pendingProfiles.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(pendingSettings)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `profile: "pending"`, `profile: "other-profile"`, 1))
	if err := os.WriteFile(pendingSettings, body, 0o600); err != nil {
		t.Fatal(err)
	}
	pendingDiag := &acceptanceDiag{}
	pendingBuilt, err := buildDCRAcceptanceMCP(t, fixture, pendingSettings, pendingLookup, pendingDiag, mockllm.New(mockllm.TextTurn("unreached")))
	if err != nil {
		t.Fatal(err)
	}
	pendingBuilt.Close()
	got := strings.Join(diagnosticSurfaces(pendingDiag), "\n")
	for _, want := range []string{"MCP OAuth DCR pending registration identity mismatch", "restore the matching OAuth profile, principal, canonical resource, and exact issuer configuration", "--retry-dcr-registration"} {
		if !strings.Contains(got, want) {
			t.Fatalf("pending identity mismatch diagnostic = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "--reset-dcr-registration") {
		t.Fatalf("pending identity mismatch diagnostic suggested reset: %q", got)
	}
}

func writeDCRAcceptanceOAuthSettings(t *testing.T, fixture *loginFixture, root, profile string) (string, func(string) (string, bool), string) {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	body := fmt.Sprintf(`mcp:
  servers:
    - name: protected
      url: %q
      auth:
        mode: oauth
        oauth:
          profile: %q
          principal: operator
          issuer: %q
          client: {mode: dcr, dcr: {}}
          credentials: {mode: local, local: {root: %q, key_env: MECATL_DCR_ACCEPTANCE_KEY}}
          network: {additional_origins: [], private_origins: [%q], max_redirects: 0}
`, fixture.resource(), profile, fixture.issuer(), root, fixture.origin())
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return settings, func(name string) (string, bool) { return key, name == "MECATL_DCR_ACCEPTANCE_KEY" }, key
}

func verifyDCRAcceptanceRead(t *testing.T, fixture *loginFixture, settings string, lookup func(string) (string, bool)) {
	t.Helper()
	built, err := buildDCRAcceptanceMCP(t, fixture, settings, lookup, &acceptanceDiag{}, mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("read", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("read complete"),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	surfaces, err := runAcceptanceTool(built, "DCR read")
	if err != nil || !strings.Contains(strings.Join(surfaces, "\n"), "fixture-ready") {
		t.Fatalf("authenticated DCR MCP read = %q, %v", surfaces, err)
	}
}

func loadAcceptanceMCPProfiles(t *testing.T, operator *permconfig.MCPSection, lookup func(string) (string, bool)) (*cliconfig.MCPProfiles, error) {
	t.Helper()
	profiles, err := cliconfig.LoadMCPProfiles(cliconfig.MCPProfileLoadOptions{Operator: operator, LookupEnv: lookup})
	if err != nil {
		return nil, err
	}
	for i := range profiles.Servers {
		if profiles.Servers[i].OAuth != nil {
			mcp.AllowOAuthLoopbackForTest(t, profiles.Servers[i].OAuth)
		}
	}
	return profiles, nil
}

type acceptanceLoopbackProfileResolver struct {
	t      *testing.T
	loader *cliconfig.MCPProfileResolver
	cert   *x509.Certificate
}

func (r *acceptanceLoopbackProfileResolver) Load(operator *permconfig.MCPSection) ([]mcp.ServerConfig, interface{ Close() error }, error) {
	servers, lifecycle, err := r.loader.Load(operator)
	if err != nil {
		return nil, lifecycle, err
	}
	for i := range servers {
		if servers[i].OAuth != nil {
			mcp.AllowOAuthLoopbackForTest(r.t, servers[i].OAuth)
			mcp.TrustOAuthCertificateForTest(r.t, servers[i].OAuth, r.cert)
		}
	}
	return servers, lifecycle, nil
}

func writeAcceptanceOAuthSettings(t *testing.T, fixture *loginFixture, root string) (string, func(string) (string, bool), string) {
	t.Helper()
	keyCanary := base64.StdEncoding.EncodeToString([]byte("524-acceptance-encryption-key-32"))
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	body := fmt.Sprintf(`mcp:
  servers:
    - name: protected
      url: %q
      auth:
        mode: oauth
        oauth:
          profile: acceptance
          principal: operator
          issuer: %q
          client:
            mode: preregistered
            preregistered: {id: %q, secret_env: MECATL_ACCEPTANCE_CLIENT_SECRET}
          scopes: [read]
          request_refresh_token: true
          credentials:
            mode: local
            local: {root: %q, key_env: MECATL_ACCEPTANCE_CREDENTIAL_KEY}
          network:
            additional_origins: []
            private_origins: [%q]
            max_redirects: 2
`, fixture.resource(), fixture.issuer(), loginClientID, root, fixture.origin())
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"MECATL_ACCEPTANCE_CLIENT_SECRET":  loginClientSecret,
		"MECATL_ACCEPTANCE_CREDENTIAL_KEY": keyCanary,
	}
	return settings, func(name string) (string, bool) { value, ok := values[name]; return value, ok }, keyCanary
}

func buildAcceptanceMCP(t *testing.T, settings string, lookup func(string) (string, bool), diag *acceptanceDiag, llm *mockllm.Provider) (*app.Built, error) {
	t.Helper()
	return buildAcceptanceMCPConfigWithCert(t, settings, lookup, diag, llm, false, nil)
}

func buildDCRAcceptanceMCP(t *testing.T, fixture *loginFixture, settings string, lookup func(string) (string, bool), diag *acceptanceDiag, llm *mockllm.Provider) (*app.Built, error) {
	t.Helper()
	return buildAcceptanceMCPConfigWithCert(t, settings, lookup, diag, llm, false, fixture.server.Certificate())
}

func buildAcceptanceMCPConfig(t *testing.T, settings string, lookup func(string) (string, bool), diag *acceptanceDiag, llm *mockllm.Provider, headless bool) (*app.Built, error) {
	t.Helper()
	return buildAcceptanceMCPConfigWithCert(t, settings, lookup, diag, llm, headless, nil)
}

func buildAcceptanceMCPConfigWithCert(t *testing.T, settings string, lookup func(string) (string, bool), diag *acceptanceDiag, llm *mockllm.Provider, headless bool, cert *x509.Certificate) (*app.Built, error) {
	t.Helper()
	return app.Build(context.Background(), app.Config{
		Workspace: filepath.Dir(settings), Model: "mock", MockProvider: llm, Headless: headless, Diagnostics: diag,
		PermissionConfigs: []string{settings},
		MCPProfileLoader: &acceptanceLoopbackProfileResolver{
			t: t, loader: cliconfig.NewMCPProfileResolver(nil, lookup), cert: cert,
		},
	})
}

func runAcceptanceTool(built *app.Built, prompt string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		return nil, err
	}
	run, err := built.Service.StartRunContent(ctx, sess.ID, prompt, nil)
	if err != nil {
		return nil, err
	}
	var surfaces []string
	for event := range run.Events() {
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
		}
		if event.ToolResult != nil {
			surfaces = append(surfaces, event.ToolResult.Content)
		}
		if event.Result != nil {
			surfaces = append(surfaces, event.Result.Text, event.Result.Error)
		}
	}
	if !strings.Contains(strings.Join(surfaces, "\n"), "complete") {
		return surfaces, fmt.Errorf("run %q did not reach the scripted terminal result", prompt)
	}
	return surfaces, nil
}

type acceptanceDiag struct {
	mu   sync.Mutex
	msgs []string
	args [][]any
}

func (d *acceptanceDiag) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.msgs = append(d.msgs, msg)
	d.args = append(d.args, args)
}

func (d *acceptanceDiag) With(...any) port.Diagnostics { return d }

func diagnosticSurfaces(diag *acceptanceDiag) []string {
	diag.mu.Lock()
	defer diag.mu.Unlock()
	result := append([]string(nil), diag.msgs...)
	for _, args := range diag.args {
		result = append(result, fmt.Sprint(args...))
	}
	return result
}

func collectError(surfaces *[]string, err error) {
	if err != nil {
		*surfaces = append(*surfaces, err.Error())
	}
}

func expireAcceptanceCredential(t *testing.T, root, encodedKey string, cfg mcp.ServerConfig) {
	t.Helper()
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != encodedKey {
		t.Fatal("decode acceptance credential key")
	}
	store, err := credentialstore.NewEncryptedFile(root, "mecatl-mcp-oauth", key)
	clear(key)
	if err != nil {
		t.Fatal("open acceptance credential store")
	}
	defer store.Close()
	if cfg.OAuth == nil {
		t.Fatal("acceptance OAuth configuration is missing")
	}
	recordKey, err := mcp.OAuthCredentialRecordKey(cfg.URL, *cfg.OAuth)
	if err != nil {
		t.Fatal("derive acceptance OAuth credential record key")
	}
	defer clear(recordKey)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record, err := store.Get(ctx, recordKey)
	if err != nil {
		t.Fatal("read acceptance OAuth credential record")
	}
	if len(record.Value) == 0 || len(record.Value) > credentialstore.MaxValueBytes {
		t.Fatal("acceptance OAuth credential record has invalid size")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(record.Value, &envelope); err != nil {
		t.Fatal("decode acceptance OAuth credential record")
	}
	var token map[string]json.RawMessage
	if err := json.Unmarshal(envelope["token"], &token); err != nil {
		t.Fatal("decode acceptance OAuth token record")
	}
	var priorExpiry string
	if err := json.Unmarshal(token["expiry"], &priorExpiry); err != nil || priorExpiry == "" {
		t.Fatal("acceptance OAuth token record has no expiry")
	}
	expired, err := json.Marshal(time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal("encode expired acceptance OAuth token timestamp")
	}
	token["expiry"] = expired
	envelope["token"], err = json.Marshal(token)
	if err != nil {
		t.Fatal("encode acceptance OAuth token record")
	}
	updated, err := json.Marshal(envelope)
	if err != nil || len(updated) > credentialstore.MaxValueBytes {
		t.Fatal("encode bounded acceptance OAuth credential record")
	}
	if _, err := store.Put(ctx, recordKey, updated, &record.Version); err != nil {
		t.Fatal("conditionally expire acceptance OAuth credential record")
	}
}

func assertEncryptedCredentialArtifacts(t *testing.T, root, encodedKey string) {
	t.Helper()
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil {
		t.Fatal("decode credential artifact scan key")
	}
	defer clear(key)
	secrets := map[string][]byte{
		"client secret":           []byte(loginClientSecret),
		"initial access token":    []byte(loginAccessToken),
		"initial refresh token":   []byte(loginRefreshToken),
		"rotated access token":    []byte(loginRotatedAccessToken),
		"rotated refresh token":   []byte(loginRotatedRefreshToken),
		"successor access token":  []byte(loginSuccessorAccessToken),
		"successor refresh token": []byte(loginSuccessorRefreshToken),
		"encoded store key":       []byte(encodedKey),
		"raw store key":           key,
	}
	files := 0
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		files++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for class, secret := range secrets {
			if len(secret) > 0 && strings.Contains(string(data), string(secret)) {
				t.Errorf("encrypted credential artifact exposed the %s", class)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("explicit login did not create a durable encrypted credential artifact")
	}
}

func acceptanceCanaries(key string) map[string]string {
	rawKey, err := base64.StdEncoding.Strict().DecodeString(key)
	if err != nil {
		rawKey = nil
	}
	defer clear(rawKey)
	return map[string]string{
		"encryption key encoding": key,
		"raw encryption key":      string(rawKey),
		"client secret":           loginClientSecret,
		"authorization code":      "login-code-canary",
		"initial access token":    loginAccessToken,
		"initial refresh token":   loginRefreshToken,
		"rotated access token":    loginRotatedAccessToken,
		"rotated refresh token":   loginRotatedRefreshToken,
		"successor access token":  loginSuccessorAccessToken,
		"successor refresh token": loginSuccessorRefreshToken,
		"static bearer":           acceptanceStaticBearer,
		"adapter failure body":    "token-failure-canary",
	}
}

func assertNoAcceptanceSecrets(t *testing.T, surfaces []string, canaries map[string]string) {
	t.Helper()
	joined := strings.Join(surfaces, "\n")
	for class, canary := range canaries {
		if canary != "" && strings.Contains(joined, canary) {
			t.Errorf("externally visible acceptance surface exposed the %s", class)
		}
	}
}

func TestADR_0325_DirectDCRConfigurationReference(t *testing.T) {
	reference, err := os.ReadFile(filepath.Join("..", "..", "user-docs", "reference", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	section := string(reference)
	for _, want := range []string{
		"direct/global profiles require an empty payload, discover from issuer",
		"broker profiles require discovery_url and nonempty scopes",
		"DiscoveryURL is required for broker DCR and forbidden for direct/global DCR",
		"direct/global DCR may omit it and uses exactly openid",
		"direct/global DCR defaults false and rejects true",
		"Ready direct-DCR identity drift is reset-required and uses --reset-dcr-registration",
		"pending identity drift is pending-identity-mismatch and cannot reset or retry until the matching profile, principal, canonical resource, and exact issuer are restored",
		"Corrupt direct-DCR state is not resettable",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("configuration reference omitted %q", want)
		}
	}
}
