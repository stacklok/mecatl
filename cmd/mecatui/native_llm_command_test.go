package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
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
	for _, args := range [][]string{{"mecatui", "llm", "logout"}, {"mecatui", "llm", "login", "corp", "extra"}, {"mecatui", "llm", "status", "corp", "extra"}} {
		if got := resolveInvocation(args); got.err == nil {
			t.Fatalf("invalid grammar accepted: %v", args)
		}
	}
	var help bytes.Buffer
	writeTopLevelHelp(&help)
	for _, want := range []string{"mecatui login ADDRESS", "ToolHive MCP", "openai-codex", "llm login ENDPOINT", "llm status [ENDPOINT]", "llm logout ENDPOINT"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, help.String())
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

var _ = errors.New
