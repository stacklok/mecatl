package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type manifestTool struct {
	name     string
	readOnly bool
	secret   string
}

func (t manifestTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Description: "description " + t.secret, Schema: json.RawMessage(`{"type":"object","x-secret":"` + t.secret + `"}`)}
}
func (t manifestTool) ReadOnly() bool { return t.readOnly }
func (manifestTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}
func (t manifestTool) Advertised() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Description: "lightweight"}
}

type customManifestInstructions struct{ secret string }

func (a customManifestInstructions) Assemble(context.Context, tool.Workspace) ([]session.Message, error) {
	return []session.Message{session.NewUserMessage("custom secret " + a.secret)}, nil
}

type manifestSink struct {
	mu   sync.Mutex
	seen bool
}

func (s *manifestSink) Emit(_ context.Context, ev session.Event) {
	if ev.Type == session.EvRequestManifest {
		s.mu.Lock()
		s.seen = true
		s.mu.Unlock()
	}
}
func (s *manifestSink) Seen() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.seen }

func TestRequestManifestDisabledByDefault(t *testing.T) {
	sink := &manifestSink{}
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) {
		if sink.Seen() {
			t.Error("default Deps emitted request.manifest before provider Stream")
		}
	})}, mockllm.TextTurn("done"))
	eng := newEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Sink: sink})
	run := eng.Run(context.Background(), session.New("manifest-off", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Time{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "hello"})
	for _, ev := range drain(run) {
		if ev.Type == session.EvRequestManifest {
			t.Fatal("zero/default Deps emitted request.manifest")
		}
	}
	if sink.Seen() {
		t.Fatal("live EventSink incorrectly enabled request manifests")
	}
}

func TestRequestManifestDescribesFinalRequestWithoutContent(t *testing.T) {
	const secret = "manifest-secret-canary"
	cat := tool.NewCatalog()
	for _, candidate := range []tool.Tool{
		manifestTool{name: "Allowed", readOnly: true, secret: secret},
		manifestTool{name: tool.ShellToolName, readOnly: true, secret: secret},
		manifestTool{name: "Denied", readOnly: true, secret: secret},
		manifestTool{name: "Mutating", readOnly: false, secret: secret},
		manifestTool{name: "Shadow", readOnly: true, secret: secret},
		manifestTool{name: "mcp__github__issues", readOnly: true, secret: secret},
	} {
		cat.MustRegister(candidate)
	}

	sink := &manifestSink{}
	var observed port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		observed = req
		if !sink.Seen() {
			t.Error("provider Stream called before request.manifest was emitted")
		}
	})}, mockllm.TextTurn("done"))
	eng := newEngine(agent.Deps{
		LLM: provider, Catalog: cat, Sink: sink, ProgressiveTools: true,
		EnableDurableEvidence: true,
		Model:                 "safe-model", ContextWindow: func() int { return 8192 },
		Instructions: customManifestInstructions{secret: secret},
	})
	sess := authoritySession(t, "Allowed", tool.ShellToolName, "Mutating", "Shadow", "mcp__github__issues")
	if err := sess.SetMode(session.ModePlan); err != nil {
		t.Fatal(err)
	}
	sess.ProviderID = "safe-provider"
	sess.ReasoningEffort = "high"
	prior := session.NewAssistantMessage("https://credentials.example/"+secret, "reasoning-provider-blob "+secret, nil)
	prior.ProviderPhase = "provider-phase-" + secret
	if err := sess.SeedHistory([]session.Message{prior}); err != nil {
		t.Fatal(err)
	}
	run := eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{
		Text: "user " + secret,
		ExtraTools: []tool.Tool{
			manifestTool{name: "Shadow", readOnly: true, secret: secret},
			manifestTool{name: "OverlayOnly", readOnly: true, secret: secret},
		},
	})

	var manifest *session.RequestManifestPayload
	var manifestIndex, firstProviderOutput = -1, -1
	for i, ev := range drain(run) {
		if ev.Type == session.EvRequestManifest {
			manifest, manifestIndex = ev.RequestManifest, i
		}
		if firstProviderOutput < 0 && (ev.Type == session.EvMessageDelta || ev.Type == session.EvTurnEnd) {
			firstProviderOutput = i
		}
	}
	if manifest == nil {
		t.Fatal("missing request.manifest")
	}
	if manifestIndex < 0 || firstProviderOutput < 0 || manifestIndex >= firstProviderOutput {
		t.Fatalf("manifest/provider output order = %d/%d", manifestIndex, firstProviderOutput)
	}
	if manifest.Provider != "safe-provider" || manifest.Model != "safe-model" || manifest.ReasoningEffort != "high" || manifest.ContextWindow != 8192 {
		t.Fatalf("identity/window = %+v", manifest)
	}
	wantTools := []string{"Allowed", "Shadow", tool.ToolSearchName, "mcp__github__issues", "OverlayOnly"}
	if strings.Join(manifest.ToolNames, ",") != strings.Join(wantTools, ",") {
		t.Fatalf("tool names = %v, want %v", manifest.ToolNames, wantTools)
	}
	decisions := map[string]string{}
	for _, decision := range manifest.ToolDecisions {
		if _, duplicate := decisions[decision.Name]; duplicate {
			t.Fatalf("duplicate decision for %q: %+v", decision.Name, manifest.ToolDecisions)
		}
		decisions[decision.Name] = decision.Decision
	}
	wantDecisions := map[string]string{
		"Allowed":             session.RequestToolDisclosureHidden,
		tool.ShellToolName:    session.RequestToolMountUnavailable,
		"Denied":              session.RequestToolAuthorityFiltered,
		"Mutating":            session.RequestToolModeFiltered,
		"Shadow":              session.RequestToolShadowed,
		tool.ToolSearchName:   session.RequestToolAdvertised,
		"mcp__github__issues": session.RequestToolDisclosureHidden,
		"OverlayOnly":         session.RequestToolAdvertised,
	}
	if len(decisions) != len(wantDecisions) {
		t.Fatalf("decision set = %v, want exactly %v", decisions, wantDecisions)
	}
	for name, want := range wantDecisions {
		if got := decisions[name]; got != want {
			t.Errorf("decision[%s] = %q, want %q (all=%v)", name, got, want, decisions)
		}
	}
	wantSources := map[string]string{
		"Allowed":             session.RequestToolSourceCatalog,
		"Shadow":              session.RequestToolSourceOverlay,
		"mcp__github__issues": session.RequestToolSourceMCP,
		"OverlayOnly":         session.RequestToolSourceOverlay,
	}
	for _, decision := range manifest.ToolDecisions {
		if want, ok := wantSources[decision.Name]; ok && decision.Source != want {
			t.Errorf("source[%s] = %q, want %q", decision.Name, decision.Source, want)
		}
	}
	messageBytes, err := json.Marshal(observed.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.MessageCount != len(observed.Messages) || manifest.MessageBytes != len(messageBytes) {
		t.Errorf("message metadata = count %d bytes %d; request has count %d bytes %d", manifest.MessageCount, manifest.MessageBytes, len(observed.Messages), len(messageBytes))
	}
	wantSchemaBytes := 0
	for _, spec := range observed.Tools {
		wantSchemaBytes += len(spec.Schema)
	}
	if manifest.AdvertisedToolSchemaBytes != wantSchemaBytes {
		t.Errorf("advertised tool schema bytes = %d, want %d", manifest.AdvertisedToolSchemaBytes, wantSchemaBytes)
	}
	if len(manifest.Prompt) < 2 {
		t.Fatalf("prompt metadata = %+v", manifest.Prompt)
	}
	last := manifest.Prompt[len(manifest.Prompt)-1]
	if last.Kind != prompt.InstructionProvenanceCustom || last.Provenance != prompt.InstructionProvenanceUnknown {
		t.Fatalf("custom instruction metadata = %+v", last)
	}
	fragmentBytes, err := json.Marshal(observed.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if last.Bytes != len(fragmentBytes) {
		t.Fatalf("custom instruction byte count does not describe final request fragment: %+v", last)
	}
	for _, component := range manifest.Prompt {
		var body []byte
		switch component.Provenance {
		case session.RequestProvenanceStable:
			body = []byte(observed.System.StablePrefix)
		case session.RequestProvenanceVolatile:
			body = []byte(observed.System.VolatileSuffix)
		default:
			continue
		}
		if component.Bytes != len(body) {
			t.Errorf("system component does not describe final request: %+v", component)
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "x-secret") || strings.Contains(string(encoded), "description") {
		t.Fatalf("manifest leaked request content: %s", encoded)
	}
}
