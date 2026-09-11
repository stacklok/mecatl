package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func nativeConfigArgs(home string, extra ...string) []string {
	args := nativeDefaultConfigArgs()
	args = append(args, "--credential-home", home)
	return append(args, extra...)
}

func nativeDefaultConfigArgs(extra ...string) []string {
	args := []string{"mecatui", "llm", "config", "set", "corp", "--gateway-url", "https://gateway.example/v1", "--issuer", "https://issuer.example", "--client-id", "mecatl", "--default-model", "model-a"}
	return append(args, extra...)
}

func runNativeConfigForTest(t *testing.T, args []string) (string, error) {
	t.Helper()
	res := resolveInvocation(args)
	if res.err != nil {
		return "", res.err
	}
	var stdout, stderr bytes.Buffer
	err := runLLMConfigCommand(res, &stdout, &stderr)
	return stdout.String() + stderr.String(), err
}

func TestNativeLLMConfigSetGrammarAndCreation(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	home := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	res := resolveInvocation(nativeConfigArgs(home))
	if res.err != nil || res.mode != modeLLMConfig || res.llmAction != "set" || res.llmEndpoint != "corp" {
		t.Fatalf("resolution = %+v", res)
	}
	out, err := runNativeConfigForTest(t, nativeConfigArgs(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mecatui llm login corp") {
		t.Fatalf("output = %q", out)
	}
	data, err := os.ReadFile(filepath.Join(xdg, "mecatl", "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := permconfig.ValidateYAML(data); err != nil {
		t.Fatalf("written settings invalid: %v\n%s", err, data)
	}
	var cfg permconfig.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	ep := cfg.LLM.Endpoints["corp"]
	if cfg.LLM.CredentialHome != home || ep.Protocol != "openai-responses" || ep.OIDC.ResourceAudience != "" || len(ep.OIDC.Scopes) != 2 || !slices.Contains(ep.OIDC.Scopes, "openid") || !slices.Contains(ep.OIDC.Scopes, "offline_access") || ep.IssuerTrust.Policy != "public" || ep.GatewayTrust.Policy != "public" {
		t.Fatalf("written native config = %+v / %+v", cfg.LLM, ep)
	}
}

func TestNativeLLMConfigSetDefaultsCredentialHomeFromXDGState(t *testing.T) {
	config, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", state)

	if _, err := runNativeConfigForTest(t, nativeDefaultConfigArgs()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(state, "mecatl", "provider-oidc")
	assertNativeCredentialHome(t, config, want)
	for _, path := range []string{filepath.Join(state, "mecatl"), want} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("default directory %q mode = %v, want owner-only directory", path, info.Mode())
		}
	}
}

func TestNativeLLMConfigSetDefaultsCredentialHomeFromUserHome(t *testing.T) {
	config, home := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)

	if _, err := runNativeConfigForTest(t, nativeDefaultConfigArgs()); err != nil {
		t.Fatal(err)
	}
	assertNativeCredentialHome(t, config, filepath.Join(home, ".local", "state", "mecatl", "provider-oidc"))
}

func TestNativeLLMConfigSetFallsBackFromRelativeXDGStateHome(t *testing.T) {
	for _, stateHome := range []string{".", "relative-state"} {
		t.Run(stateHome, func(t *testing.T) {
			config, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
			t.Chdir(cwd)
			t.Setenv("XDG_CONFIG_HOME", config)
			t.Setenv("XDG_STATE_HOME", stateHome)
			t.Setenv("HOME", home)

			if _, err := runNativeConfigForTest(t, nativeDefaultConfigArgs()); err != nil {
				t.Fatal(err)
			}
			assertNativeCredentialHome(t, config, filepath.Join(home, ".local", "state", "mecatl", "provider-oidc"))
			entries, err := os.ReadDir(cwd)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("relative XDG_STATE_HOME wrote under cwd: %v", entries)
			}
		})
	}
}

func assertNativeCredentialHome(t *testing.T, configDir, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, "mecatl", "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg permconfig.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.CredentialHome != want {
		t.Fatalf("credential home = %q, want %q", cfg.LLM.CredentialHome, want)
	}
}

func TestNativeLLMConfigSetRejectsSymlinkedDefaultCredentialHome(t *testing.T) {
	config, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", state)
	parent := filepath.Join(state, "mecatl")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(parent, "provider-oidc")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := runNativeConfigForTest(t, nativeDefaultConfigArgs()); err == nil {
		t.Fatal("symlinked default credential home was accepted")
	}
	if _, err := os.Stat(filepath.Join(config, "mecatl", "settings.yaml")); !os.IsNotExist(err) {
		t.Fatalf("settings written for symlinked default credential home: %v", err)
	}
}

func TestNativeLLMConfigSetUpdatePreservesUnrelatedAndSupportsOptions(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	path := filepath.Join(xdg, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	initial := "# keep this comment\nposture: trusted\nmodels:\n  default_provider: openai\nllm:\n  credential_home: " + home + "\n  endpoints:\n    other:\n      protocol: openai-responses\n      url: https://other.example/v1\n      default_model: old\n      oidc:\n        issuer: https://issuer.example\n        client_id: old\n        scopes: [openid]\n      issuer_trust: {policy: public}\n      gateway_trust: {policy: public}\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	issuerCA := filepath.Join(t.TempDir(), "issuer.pem")
	gatewayCA := filepath.Join(t.TempDir(), "gateway.pem")
	_, err := runNativeConfigForTest(t, nativeConfigArgs(home, "--resource-audience", "https://gateway.example", "--scope", "models.read", "--scope", "offline_access", "--issuer-ca-bundle", issuerCA, "--gateway-ca-bundle", gatewayCA))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# keep this comment", "posture: trusted", "default_provider: openai", "other:", "resource_audience: https://gateway.example", "models.read", issuerCA, gatewayCA} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings missing %q:\n%s", want, text)
		}
	}
	var cfg permconfig.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	ep := cfg.LLM.Endpoints["corp"]
	if len(ep.OIDC.Scopes) != 2 || !slices.Contains(ep.OIDC.Scopes, "models.read") || !slices.Contains(ep.OIDC.Scopes, "offline_access") || ep.IssuerTrust.Policy != "private-ca" || ep.GatewayTrust.Policy != "private-ca" {
		t.Fatalf("options not preserved: %+v", ep)
	}
}

func TestNativeLLMConfigSetRejectsUnusableExplicitCredentialHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	unsafe := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{filepath.Join(t.TempDir(), "missing"), unsafe} {
		if _, err := runNativeConfigForTest(t, nativeConfigArgs(home)); err == nil {
			t.Fatalf("unusable credential home accepted: %q", home)
		}
	}
	if _, err := os.Stat(filepath.Join(xdg, "mecatl", "settings.yaml")); !os.IsNotExist(err) {
		t.Fatalf("settings written for unusable credential home: %v", err)
	}
}

func TestNativeLLMConfigSetRejectsDifferentSharedCredentialHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	for _, home := range []string{first, second} {
		if err := os.Mkdir(home, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runNativeConfigForTest(t, nativeConfigArgs(first)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(xdg, "mecatl", "settings.yaml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	args := nativeConfigArgs(second)
	args[4] = "other"
	output, err := runNativeConfigForTest(t, args)
	if err == nil || !strings.Contains(err.Error(), "shared by all native endpoints") {
		t.Fatalf("different shared credential home error = %v", err)
	}
	if output != "" {
		t.Fatalf("different shared credential home output = %q", output)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("different shared credential home changed settings:\n%s", after)
	}
}

func TestNativeLLMConfigSetOmittedHomeRejectsConfiguredSharedHome(t *testing.T) {
	config, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", state)
	existing := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runNativeConfigForTest(t, nativeConfigArgs(existing)); err != nil {
		t.Fatal(err)
	}

	args := nativeDefaultConfigArgs()
	args[4] = "other"
	if _, err := runNativeConfigForTest(t, args); err == nil || !strings.Contains(err.Error(), "shared by all native endpoints") {
		t.Fatalf("omitted shared credential home error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(state, "mecatl")); !os.IsNotExist(err) {
		t.Fatalf("conflicting omitted home created default directories: %v", err)
	}
	assertNativeCredentialHome(t, config, existing)
}

func TestNativeLLMConfigSetInvalidInputDoesNotWriteOrLogin(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	path := filepath.Join(xdg, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("posture: strict\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldLogin := executeToolHiveLogin
	loginCalls := 0
	executeToolHiveLogin = func(context.Context, bool) error { loginCalls++; return nil }
	t.Cleanup(func() { executeToolHiveLogin = oldLogin })

	cases := [][]string{
		{"mecatui", "llm", "config", "set"},
		nativeConfigArgs("relative/path"),
		nativeConfigArgs(filepath.Join(t.TempDir(), "creds"), "--gateway-url", "http://insecure.example"),
		nativeConfigArgs(filepath.Join(t.TempDir(), "creds"), "--scope", "bad scope"),
		append(nativeConfigArgs(filepath.Join(t.TempDir(), "creds")), "--client-id", ""),
	}
	for _, args := range cases {
		if _, err := runNativeConfigForTest(t, args); err == nil {
			t.Fatalf("invalid input accepted: %v", args)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("invalid input changed settings: %v, %q", err, got)
		}
	}
	if loginCalls != 0 {
		t.Fatalf("config set performed %d login(s)", loginCalls)
	}
}
