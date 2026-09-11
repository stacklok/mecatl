package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/oidcclient"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

type fakeNativeLLMHost struct {
	ids              []string
	login, logout    []string
	statuses         map[string]llmendpoint.Status
	loginErr         error
	secretSideEffect string
}

func (f *fakeNativeLLMHost) EndpointIDs() []string { return append([]string(nil), f.ids...) }
func (f *fakeNativeLLMHost) Login(context.Context, string) error {
	f.login = append(f.login, f.secretSideEffect)
	return f.loginErr
}
func (f *fakeNativeLLMHost) Status(_ context.Context, id string) llmendpoint.Status {
	return f.statuses[id]
}
func (f *fakeNativeLLMHost) Logout(_ context.Context, id string) error {
	f.logout = append(f.logout, id)
	return nil
}
func (*fakeNativeLLMHost) Close() error { return nil }

func TestNativeLLMGatewayLogin_Scenario4_HostOwnershipBoundary(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "llm", "login", "corp"})
	if res.err != nil || res.mode != modeLogin || res.llmAction != "login" || res.llmEndpoint != "corp" {
		t.Fatalf("native local login resolution = %+v", res)
	}
	remote := resolveInvocation([]string{"mecatui", "login", "server.example"})
	if remote.mode != modeRemoteLogin || remote.llmAction != "" {
		t.Fatalf("remote login crossed native host boundary: %+v", remote)
	}
}

func TestNativeLLMGatewayLogin_Scenario4_SecretCanaryNonDisclosure(t *testing.T) {
	canaries := []string{"access-token-canary", "authorization-code-canary", "https://issuer-canary.example/auth", "record-key-canary", "/trust/path/canary"}
	host := &fakeNativeLLMHost{ids: []string{canaries[0]}, secretSideEffect: canaries[1]}
	var stdout, stderr bytes.Buffer
	if err := runNativeLLMCommand(t.Context(), "login", canaries[0], false, host, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	combined := stdout.String() + stderr.String()
	if stdout.Len() != 0 || stderr.String() != "LLM endpoint login successful\n" {
		t.Fatalf("login output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	for _, canary := range canaries {
		if strings.Contains(combined, canary) {
			t.Fatalf("secret canary disclosed: %q", canary)
		}
	}
}

func TestNativeEnvironmentKeyCommandOutput(t *testing.T) {
	xdg, home := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	const keyCanary = "invalid-native-encryption-key-canary"
	t.Setenv("MECATL_NATIVE_LLM_CREDENTIAL_KEY", keyCanary)
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runNativeConfigForTest(t, nativeConfigArgs(home)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(xdg, "mecatl", "settings.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "source: keyring", "source: environment\n    key_env: MECATL_NATIVE_LLM_CREDENTIAL_KEY", 1)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	host, err := newNativeLLMHost(t.Context(), false, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()
	for _, action := range []string{"login", "logout", "status"} {
		err := runNativeLLMCommand(t.Context(), action, "corp", false, host, &stdout, &stderr)
		if action != "status" && (err == nil || !strings.Contains(err.Error(), "llm.credential_key")) {
			t.Fatalf("missing key-source remediation: %v", err)
		}
		if strings.Contains(stdout.String()+stderr.String()+fmt.Sprint(err), keyCanary) {
			t.Fatal("command disclosed encryption key")
		}
	}
	if !strings.Contains(stdout.String(), "storage-unavailable") {
		t.Fatal("status did not report unavailable key material")
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatal("invalid environment key initialized storage or keyring")
	}
}

func TestNativeLLMGatewayLogin_Scenario5_LocalStatusIsPassive(t *testing.T) {
	host := &fakeNativeLLMHost{
		ids: []string{"storage", "rejected", "not-enrolled", "corrupt", "expired", "usable"},
		statuses: map[string]llmendpoint.Status{
			"usable": llmendpoint.StatusUsable, "not-enrolled": llmendpoint.StatusNotEnrolled,
			"expired": llmendpoint.StatusExpired, "corrupt": llmendpoint.StatusCorrupt,
			"storage": llmendpoint.StatusStorageUnavailable, "rejected": llmendpoint.StatusRejected,
		},
	}
	var stdout, stderr bytes.Buffer
	if err := runNativeLLMCommand(t.Context(), "status", "", false, host, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	want := "corrupt\tcorrupt\nexpired\texpired\nnot-enrolled\tnot-enrolled\nrejected\trejected\nstorage\tstorage-unavailable\nusable\tusable\n"
	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("status output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if len(host.login) != 0 || len(host.logout) != 0 {
		t.Fatal("status invoked an active lifecycle operation")
	}
}

func TestNativeLLMGatewayLogin_Scenario6_CommandGrammarAndCopy(t *testing.T) {
	valid := []struct {
		args             []string
		action, endpoint string
	}{
		{[]string{"mecatui", "llm", "login", "corp"}, "login", "corp"},
		{[]string{"mecatui", "llm", "login", "corp", "--no-browser"}, "login", "corp"},
		{[]string{"mecatui", "llm", "status"}, "status", ""},
		{[]string{"mecatui", "llm", "status", "corp"}, "status", "corp"},
		{[]string{"mecatui", "llm", "logout", "corp"}, "logout", "corp"},
	}
	for _, tc := range valid {
		got := resolveInvocation(tc.args)
		if got.err != nil || got.llmAction != tc.action || got.llmEndpoint != tc.endpoint {
			t.Fatalf("resolve %v = %+v", tc.args, got)
		}
	}
	for _, args := range [][]string{
		{"mecatui", "llm", "logout"},
		{"mecatui", "llm", "login", "corp", "extra"},
		{"mecatui", "llm", "login", "corp", "--skip-browser"},
		{"mecatui", "llm", "login", "toolhive", "--no-browser"},
		{"mecatui", "llm", "login", "corp", "--no-browser", "--no-browser"},
		{"mecatui", "llm", "status", "corp", "extra"},
	} {
		if got := resolveInvocation(args); got.err == nil {
			t.Fatalf("invalid grammar accepted: %v", args)
		}
	}
	var help bytes.Buffer
	writeTopLevelHelp(&help)
	for _, want := range []string{"mecatui login ADDRESS", "ToolHive MCP", "openai-codex", "llm login ENDPOINT [--no-browser]", "llm status [ENDPOINT]", "llm logout ENDPOINT"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, help.String())
		}
	}
}

func TestNativeLLMGatewayLogin_CommandHelpDistinguishesBrowserFlags(t *testing.T) {
	output := captureStderr(t, func() {
		err := runLLMCommand(invocationResolution{remaining: []string{"--help"}})
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("runLLMCommand help error = %v", err)
		}
	})
	for _, want := range []string{
		"llm login ENDPOINT [--no-browser]",
		"--no-browser prints the authorization URL to stderr",
		"http://localhost:8666/callback",
		"llm login toolhive [--skip-browser]",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("command help missing %q:\n%s", want, output)
		}
	}
}

func TestNativeLLMGatewayLogin_BrowserUsesToolHiveCompatibleRedirect(t *testing.T) {
	opts := nativeLLMOAuthOptions(false, nil)
	if opts.NoBrowser || opts.URLWriter != nil || opts.RedirectURL != oauthlogin.ToolHiveCompatibleRedirectURL {
		t.Fatalf("native browser OAuth options = %+v", opts)
	}
}

func TestNativeLLMGatewayLogin_NoBrowserUsesFixedLoopbackPresenter(t *testing.T) {
	const (
		issuer           = "https://issuer.example.test"
		authorizationURL = issuer + "/authorize?state=state-canary"
	)
	var stderr bytes.Buffer
	opts := nativeLLMOAuthOptions(true, &stderr)
	if !opts.NoBrowser || opts.URLWriter != &stderr || opts.RedirectURL != oauthlogin.ToolHiveCompatibleRedirectURL {
		t.Fatalf("native no-browser OAuth options = %+v", opts)
	}
	runtime, err := oauthlogin.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Authorize(t.Context(), issuer, func(ctx context.Context, redirect string, present func(context.Context, string) (oauthlogin.Result, error)) error {
		if redirect != oauthlogin.ToolHiveCompatibleRedirectURL {
			return fmt.Errorf("native redirect = %q", redirect)
		}
		callback, parseErr := url.Parse(redirect)
		if parseErr != nil {
			return parseErr
		}
		query := callback.Query()
		query.Set("code", "authorization-code-canary")
		query.Set("state", "state-canary")
		query.Set("iss", issuer)
		callback.RawQuery = query.Encode()
		go func() {
			response, requestErr := http.Get(callback.String()) //nolint:gosec // Fixed loopback URL from the OAuth runtime.
			if requestErr == nil {
				_ = response.Body.Close()
			}
		}()
		_, presentErr := present(ctx, authorizationURL)
		return presentErr
	})
	if err != nil {
		t.Fatal(err)
	}
	output := stderr.String()
	if strings.Count(output, authorizationURL) != 1 || !strings.Contains(output, "callback must reach this machine") {
		t.Fatalf("native no-browser output = %q", output)
	}
	for _, secret := range []string{"authorization-code-canary", "access-token-canary", "refresh-token-canary", "/credential/path/canary"} {
		if strings.Contains(output, secret) {
			t.Fatalf("native no-browser output disclosed %q: %q", secret, output)
		}
	}
}

func TestNativeLLMGatewayLogin_Scenario6_ToolHiveCompatibilityAlias(t *testing.T) {
	for _, args := range [][]string{{"mecatui", "llm", "login"}, {"mecatui", "llm", "login", "toolhive"}} {
		got := resolveInvocation(args)
		if got.err != nil || got.llmEndpoint != "toolhive" || got.llmAction != "login" {
			t.Fatalf("ToolHive alias resolve %v = %+v", args, got)
		}
		if len(args) == 3 && !got.llmDeprecatedAlias {
			t.Fatal("bare ToolHive compatibility alias did not request a warning")
		}
	}
}

func TestNativeLLMGatewayLogin_Scenario6_TokenOutputHardening(t *testing.T) {
	original := executeToolHiveLogin
	t.Cleanup(func() { executeToolHiveLogin = original })
	executeToolHiveLogin = func(context.Context, bool) error { return nil }
	var stdout, stderr bytes.Buffer
	if err := runNativeLLMCommand(t.Context(), "login", "toolhive", false, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.String() != "ToolHive LLM gateway login successful\n" {
		t.Fatalf("ToolHive output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestNativeLLMGatewayLogin_Scenario6_ToolHiveLifecycleMessage(t *testing.T) {
	for _, action := range []string{"status", "logout"} {
		var stdout, stderr bytes.Buffer
		err := runNativeLLMCommand(t.Context(), action, "toolhive", false, nil, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "ToolHive owns") || !strings.Contains(err.Error(), "thv llm") {
			t.Fatalf("%s error = %v", action, err)
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("%s inspected or emitted lifecycle state", action)
		}
	}
}

func TestInvariant_native_llm_lifecycle_errors_are_safe_and_actionable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not enrolled", llmendpoint.ErrNotEnrolled, "llm login corp"},
		{"discovery", oidcclient.ErrDiscovery, "issuer trust"},
		{"authorization", oidcclient.ErrAuthorization, "browser"},
		{"callback occupied", errors.Join(oidcclient.ErrAuthorization, &oauthlogin.CallbackBindError{Reason: oauthlogin.CallbackBindAddressInUse}), "port 8666"},
		{"token", oidcclient.ErrToken, "OIDC endpoint configuration"},
		{"storage", credentialstore.ErrUnavailable, "protected credential storage"},
		{"keyring", oidcclient.ErrStorage, "protected credential storage"},
		{"timeout", context.DeadlineExceeded, "timed out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := &fakeNativeLLMHost{ids: []string{"corp"}, loginErr: fmt.Errorf("secret-canary: %w", tc.err)}
			err := runNativeLLMCommand(t.Context(), "login", "corp", false, host, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-canary") {
				t.Fatalf("safe remediation = %v, want %q without provider detail", err, tc.want)
			}
		})
	}
}

func TestInvariant_native_llm_status_and_unknown_endpoint_are_actionable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	empty := &fakeNativeLLMHost{statuses: map[string]llmendpoint.Status{}}
	err := runNativeLLMCommand(t.Context(), "status", "", false, empty, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "no native LLM endpoints are configured") || stdout.Len() != 0 {
		t.Fatalf("empty status = %v stdout=%q", err, stdout.String())
	}

	host := &fakeNativeLLMHost{ids: []string{"corp", "research"}, statuses: map[string]llmendpoint.Status{}}
	err = runNativeLLMCommand(t.Context(), "login", "missing", false, host, &stdout, &stderr)
	for _, want := range []string{"unknown native LLM endpoint", "corp, research", "llm status"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("unknown endpoint remediation = %v, want %q", err, want)
		}
	}
}

func TestInvariant_native_llm_enrollment_context_is_bounded(t *testing.T) {
	ctx, cancel := newNativeLLMEnrollmentContext(20 * time.Millisecond)
	defer cancel()
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("enrollment context = %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("enrollment context was not bounded")
	}
}

var _ = errors.New
