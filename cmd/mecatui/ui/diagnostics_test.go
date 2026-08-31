package ui

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type fakeServerInfo struct {
	info       client.ServerInfo
	err        error
	calls      int
	providerID string
}

func (f *fakeServerInfo) GetServerInfo(_ context.Context, providerID string) (client.ServerInfo, error) {
	f.providerID = providerID
	f.calls++
	return f.info, f.err
}

func TestDebugLaunchDefaultEmbeddedFirstTurnIsExact(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	const objective = "Diagnose the bound target session and explain the most likely cause of its reported behavior."
	m.deps.DebugTarget = "target"
	m.deps.Embedded = true
	m.deps.ClientBuild = "client-v1"
	m.deps.ServerImpl = "mecated"
	m.deps.Server = "unix:///runtime/mecated.sock"
	m.pendingInitialPrompt = objective

	_, submit, started := m.startInitialPrompt()
	if !started || submit == nil {
		t.Fatal("embedded default debugger prompt did not start")
	}
	runBatchLeaves(submit)
	want := debuggerInitialPrompt(m.diagnosticsReport("client-v1", "mecated", "unix:///runtime/mecated.sock", "", "embedded"), objective)
	if got := send.frames()[0].GetPrompt().GetText(); got != want {
		t.Fatalf("embedded default first turn differs from contract:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestDebugLaunchPrependsAutomaticRemoteDiagnosticsToFirstTurn(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	info := &fakeServerInfo{info: client.ServerInfo{BuildID: "server-v1", ServerImplementation: "mecated", DisplayServerEndpoint: "https://server.example/rpc", LLMProviderDisplayEndpoint: "https://provider.example/v1"}}
	m.deps.DebugTarget = "target"
	m.deps.ServerInfo = info
	m.deps.ClientBuild = "client-v1"
	m.pendingInitialPrompt = "Why did it fail?"
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openrouter", ModelID: "model"}

	m0, lookup, started := m.startInitialPrompt()
	if !started || lookup == nil || len(send.frames()) != 0 {
		t.Fatalf("debug baseline lookup start = %t/%v, frames=%d", started, lookup, len(send.frames()))
	}
	m = m0.(Model)
	msg := lookup().(diagnosticsMsg)
	m0, submit := m.Update(msg)
	m = m0.(Model)
	runBatchLeaves(submit)
	frames := send.frames()
	if info.calls != 1 || len(frames) != 1 {
		t.Fatalf("lookup calls/first turns = %d/%d, want 1/1", info.calls, len(frames))
	}
	got := frames[0].GetPrompt().GetText()
	wantPrompt := debuggerInitialPrompt(
		m.diagnosticsReport(info.info.BuildID, info.info.ServerImplementation, info.info.DisplayServerEndpoint, info.info.LLMProviderDisplayEndpoint, "ok"),
		"Why did it fail?",
	)
	if got != wantPrompt {
		t.Fatalf("first debugger turn differs from objective-first contract:\n--- got ---\n%s\n--- want ---\n%s", got, wantPrompt)
	}
	for _, want := range []string{
		"Objective\nWhy did it fail?",
		"Required workflow\n1. Call InspectSession with view=status first.",
		"Expected report\n- Observed facts, each naming its evidence source",
		"<<<CURRENT_DEBUGGER_RUNTIME_CONTEXT (not target evidence)",
		"Mecatl diagnostics (current client state only):",
		"server lookup: ok",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("first debugger turn missing %q:\n%s", want, got)
		}
	}
	objective := strings.Index(got, "Objective")
	workflow := strings.Index(got, "Required workflow")
	report := strings.Index(got, "Expected report")
	runtimeContext := strings.Index(got, "<<<CURRENT_DEBUGGER_RUNTIME_CONTEXT")
	if objective != 0 || objective >= workflow || workflow >= report || report >= runtimeContext {
		t.Fatalf("debugger prompt hierarchy is out of order: objective=%d workflow=%d report=%d runtime=%d", objective, workflow, report, runtimeContext)
	}
	_, duplicate, restarted := m.startInitialPrompt()
	if restarted || duplicate != nil || len(send.frames()) != 1 {
		t.Fatalf("debugger initial turn was submitted more than once: restarted=%v frames=%d", restarted, len(send.frames()))
	}
}

func TestDebugLaunchRemoteDiagnosticsFailureIsClassifiedAndNonBlocking(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	m.deps.DebugTarget = "target"
	m.deps.ServerInfo = &fakeServerInfo{err: errors.New("Bearer secret at https://private.example")}
	m.pendingInitialPrompt = "diagnose"
	m0, lookup, _ := m.startInitialPrompt()
	m = m0.(Model)
	m0, submit := m.Update(lookup())
	_ = m0
	runBatchLeaves(submit)
	got := send.frames()[0].GetPrompt().GetText()
	want := debuggerInitialPrompt(m.diagnosticsReport("", "", "", "", "invalid-response"), "diagnose")
	if got != want {
		t.Fatalf("failed debugger lookup changed first-turn contract:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if !strings.Contains(got, "server lookup: invalid-response") || !strings.Contains(got, "\n\nRequired workflow") || strings.Contains(got, "secret") || strings.Contains(got, "private.example") {
		t.Fatalf("failed debugger lookup was unsafe or blocking: %q", got)
	}
}

func TestNonDebugInitialPromptRemainsByteIdentical(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	const prompt = "  exact ordinary prompt  "
	m.pendingInitialPrompt = prompt
	m0, submit, started := m.startInitialPrompt()
	_ = m0
	if !started {
		t.Fatal("ordinary initial prompt was not started")
	}
	runBatchLeaves(submit)
	if got := send.frames()[0].GetPrompt().GetText(); got != strings.TrimSpace(prompt) {
		t.Fatalf("ordinary initial prompt = %q", got)
	}
}

func TestDiagnosticsCommandSendsSafeRemoteReport(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	info := &fakeServerInfo{info: client.ServerInfo{BuildID: "server-v1.2.3", ServerImplementation: "future-server", DisplayServerEndpoint: "https://server.example:8443/rpc", LLMProviderDisplayEndpoint: "https://provider.example/v1"}}
	m.deps.ServerInfo, m.deps.ClientBuild = info, "client-v1.2.3"
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openrouter", ModelID: "openai/gpt-5"}
	m.activeMode = "acceptEdits"

	_, cmd, handled := m.dispatchBareBuiltin(" \t/diagnostics  ")
	if !handled || cmd == nil {
		t.Fatal("whitespace-trimmed /diagnostics must be intercepted")
	}
	msg := cmd()
	m0, cmd := m.Update(msg)
	m = m0.(Model)
	_ = firstBatchLeaf(t, cmd)
	frames := send.frames()
	if info.calls != 1 || info.providerID != "openrouter" || len(frames) == 0 {
		t.Fatalf("lookup/provider/send = %d/%q/%d, want 1/openrouter/at least 1", info.calls, info.providerID, len(frames))
	}
	got := frames[0].GetPrompt().GetText()
	for _, want := range []string{
		"platform: " + runtime.GOOS + "/" + runtime.GOARCH,
		"client build: client-v1.2.3", "server mode: remote", "server build: server-v1.2.3", "server implementation: future-server",
		"server lookup: ok", "server endpoint: https://server.example:8443/rpc", "LLM provider endpoint: https://provider.example/v1", "active provider: openrouter", "active model: openai/gpt-5", "permission mode: acceptEdits",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q:\n%s", want, got)
		}
	}
	const expectedLines = 12
	if lines := strings.Split(got, "\n"); len(lines) != expectedLines {
		t.Fatalf("report has %d lines, want the %d-line allowlist: %q", len(lines), expectedLines, got)
	}
}

// TestDiagnosticsArgumentPolicy keeps arguments local and leaves unknown slash
// commands model-facing; diagnostics now follows the same builtin policy as every
// other client-side command.
func TestDiagnosticsArgumentPolicy(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	for _, input := range []string{"/diagnostics anything", "/diagnostics\nanything"} {
		m.prompt.Rewrite(input)
		m0, cmd := m.submitPrompt()
		m = m0.(Model)
		if cmd != nil || len(send.frames()) != 0 {
			t.Fatalf("arguments for %q escaped local dispatch: cmd=%v frames=%d", input, cmd, len(send.frames()))
		}
		if m.prompt.Value() != input {
			t.Fatalf("arguments for %q changed input to %q", input, m.prompt.Value())
		}
		if got := stripANSIstr(m.statusMsg); !strings.Contains(got, "does not take arguments") || !strings.Contains(got, "/diagnostics") {
			t.Fatalf("argument warning = %q", got)
		}
	}

	m.prompt.Rewrite("/unknown anything")
	m0, cmd := m.submitPrompt()
	_ = m0
	runBatchLeaves(cmd)
	frames := send.frames()
	if len(frames) == 0 || frames[len(frames)-1].GetPrompt().GetText() != "/unknown anything" {
		t.Fatalf("unknown slash command = %#v, want unchanged model-facing prompt", frames)
	}
}

func TestDiagnosticsSafeFailures(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{}, false)
	m.deps.ClientBuild = "bad\nclient"
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "bad value", ModelID: ""}
	m.activeMode = "\x1b[31m"
	for _, err := range []error{
		status.Error(codes.Unimplemented, "https://secret.example/token"),
		status.Error(codes.Unavailable, "TLS failed at https://secret.example/token"),
		errors.New("authorization: Bearer secret"),
	} {
		msg := diagnosticsMsg{err: err}
		m0, _ := m.handleDiagnostics(msg)
		report := m0.(Model).prompt.Value()
		if strings.Contains(report, "secret") || strings.Contains(report, "https://") {
			t.Fatalf("unsafe failure leaked into report: %q", report)
		}
	}
	report := m.diagnosticsReport("bad\nserver", "bad\nimplementation", "bad\nendpoint", "https://user:secret@provider.example/v1?token=secret", "ok")
	for _, want := range []string{"client build: unavailable", "server build: unavailable", "server implementation: unavailable", "server endpoint: unavailable", "LLM provider endpoint: https://provider.example/v1", "active provider: unavailable", "active model: unavailable", "permission mode: unavailable"} {
		if !strings.Contains(report, want) {
			t.Errorf("malformed/unknown state missing fallback %q in %q", want, report)
		}
	}
	if strings.Contains(report, "secret") || strings.Contains(report, "token") {
		t.Fatalf("endpoint credentials leaked into report: %q", report)
	}
	if report := m.diagnosticsReport("build", strings.Repeat("x", 129), "", "", "ok"); !strings.Contains(report, "server implementation: unavailable") {
		t.Errorf("oversized server implementation was not rejected: %q", report)
	}
}

func TestDiagnosticsEmbeddedSkipsLookup(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	info := &fakeServerInfo{err: errors.New("must not call")}
	m.deps.ServerInfo, m.deps.ClientBuild, m.deps.ServerImpl, m.deps.Embedded = info, "embedded-v1", "mecatui", true
	_, cmd, handled := m.dispatchBareBuiltin("/diagnostics")
	if !handled || cmd == nil {
		t.Fatal("embedded diagnostics was not dispatched")
	}
	_ = firstBatchLeaf(t, cmd)
	if info.calls != 0 {
		t.Fatalf("embedded diagnostics lookup calls = %d, want 0", info.calls)
	}
	got := send.frames()[0].GetPrompt().GetText()
	if !strings.Contains(got, "server mode: embedded") || !strings.Contains(got, "server build: embedded-v1") || !strings.Contains(got, "server implementation: mecatui") || !strings.Contains(got, "server endpoint: unavailable") || !strings.Contains(got, "LLM provider endpoint: unavailable") {
		t.Fatalf("embedded report = %q", got)
	}
}
