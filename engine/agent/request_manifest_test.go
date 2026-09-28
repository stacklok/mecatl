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
	name      string
	readOnly  bool
	secret    string
	specCalls *int
}

func (t manifestTool) Spec() tool.ToolSpec {
	if t.specCalls != nil {
		*t.specCalls++
	}
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

func (a customManifestInstructions) Assemble(context.Context) ([]session.Message, error) {
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
	counter := agent.HeuristicTokenCounter{}
	wantRequestTokens := counter.Count(observed.System.Render()) + counter.CountMessages(observed.Messages)
	for _, spec := range observed.Tools {
		wantRequestTokens += 4 + counter.Count(spec.Name) + counter.Count(spec.Description) + counter.Count(string(spec.Schema))
	}
	if manifest.TokenEstimateMethod != "local_estimate" || manifest.EstimatedRequestTokens == nil || *manifest.EstimatedRequestTokens != wantRequestTokens {
		t.Errorf("request estimate = method %q tokens %v, want local_estimate/%d", manifest.TokenEstimateMethod, manifest.EstimatedRequestTokens, wantRequestTokens)
	}
	if manifest.EstimatedSystemTokens == nil || manifest.EstimatedEphemeralFragmentTokens == nil || manifest.EstimatedPersistedHistoryTokens == nil || manifest.EstimatedAdvertisedToolTokens == nil {
		t.Errorf("missing component estimates: %+v", manifest)
	}
	if len(manifest.AdvertisedTools) != len(observed.Tools) {
		t.Fatalf("tool metrics = %d, want %d", len(manifest.AdvertisedTools), len(observed.Tools))
	}
	for i, spec := range observed.Tools {
		metric := manifest.AdvertisedTools[i]
		if metric.Name != spec.Name || metric.NameBytes != len(spec.Name) || metric.DescriptionBytes != len(spec.Description) || metric.SchemaBytes != len(spec.Schema) {
			t.Errorf("tool metric[%d] bytes = %+v, want request spec %q", i, metric, spec.Name)
		}
		wantTokens := 4 + counter.Count(spec.Name) + counter.Count(spec.Description) + counter.Count(string(spec.Schema))
		if metric.EstimatedTokens != wantTokens {
			t.Errorf("tool metric[%d] tokens = %d, want %d", i, metric.EstimatedTokens, wantTokens)
		}
	}
	if manifest.EstimatedEphemeralFragmentBytes == nil || manifest.EstimatedPersistedHistoryBytes == nil || manifest.EstimatedAdvertisedToolBytes == nil || manifest.EstimatedSystemBytes == nil {
		t.Errorf("missing component byte estimates: %+v", manifest)
	}
	if len(manifest.Prompt) < 2 {
		t.Fatalf("prompt metadata = %+v", manifest.Prompt)
	}
	last := manifest.Prompt[len(manifest.Prompt)-1]
	if last.Kind != prompt.InstructionProvenanceCustom || last.Provenance != prompt.InstructionProvenanceUnknown || last.Rules != nil || last.OmittedRules != 0 {
		t.Fatalf("custom instruction metadata = %+v", last)
	}
	fragmentBytes, err := json.Marshal(observed.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if last.Bytes != len(fragmentBytes) {
		t.Fatalf("custom instruction byte count does not describe final request fragment: %+v", last)
	}
	wantFragmentTokens := counter.CountMessages([]session.Message{observed.Messages[0]})
	if last.EstimatedTokens == nil || *last.EstimatedTokens != wantFragmentTokens {
		t.Errorf("fragment token estimate = %v, want %d", last.EstimatedTokens, wantFragmentTokens)
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

func TestRequestManifestMetricsDoNotRefreshToolSpecs(t *testing.T) {
	run := func(durableEvidence bool) int {
		calls := 0
		cat := tool.NewCatalog()
		cat.MustRegister(manifestTool{name: "Counted", readOnly: true, secret: "secret", specCalls: &calls})
		eng := newEngine(agent.Deps{
			LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: cat,
			EnableDurableEvidence: durableEvidence,
		})
		sess := newSession(t, session.Limits{})
		for range eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}).Events() {
		}
		return calls
	}

	withoutManifest := run(false)
	withManifest := run(true)
	if withManifest != withoutManifest {
		t.Fatalf("Spec calls with manifest = %d, without manifest = %d; metrics must use only req.Tools", withManifest, withoutManifest)
	}
}

func TestRequestManifestLegacyMetricsRemainUnknown(t *testing.T) {
	var manifest session.RequestManifestPayload
	if err := json.Unmarshal([]byte(`{"tool_names":[],"tool_decisions":[],"advertised_tool_schema_bytes":0,"message_count":0,"message_bytes":0,"prompt":[]}`), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.TokenEstimateMethod != "" || manifest.EstimatedRequestTokens != nil || manifest.EstimatedSystemTokens != nil || manifest.EstimatedEphemeralFragmentTokens != nil || manifest.EstimatedPersistedHistoryTokens != nil || manifest.EstimatedAdvertisedToolTokens != nil || manifest.AdvertisedTools != nil {
		t.Fatalf("legacy manifest fabricated metrics: %+v", manifest)
	}
}

type manifestRulesSource struct {
	calls int
	rules []prompt.Rule
}

func (s *manifestRulesSource) ListRules(context.Context) ([]prompt.Rule, error) {
	s.calls++
	return s.rules, nil
}

func TestRequestManifestRulesAfterCompaction(t *testing.T) {
	const secret = "rule-body-canary"
	src := &manifestRulesSource{rules: []prompt.Rule{
		{Name: "project", Body: secret, Origin: prompt.RuleOriginProject},
		{Name: "hostile\"<x>\nuser", Body: secret, Paths: []string{"**/*.go"}, Origin: prompt.RuleOriginUser},
		{Name: "omitted", Body: secret},
	}}
	instructions := prompt.RulesAssembler{Src: src, MaxCount: 2}
	compactor := &manifestCompactor{}
	var observed port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		observed = req
	})}, mockllm.TextTurn("done"))
	eng := newEngine(agent.Deps{
		LLM: provider, Catalog: tool.NewCatalog(), Instructions: instructions,
		EnableDurableEvidence: true, Compactor: compactor,
		TokenCounter:  agent.HeuristicTokenCounter{CharsPerToken: 1},
		ContextWindow: func() int { return 10 }, CompactionRatio: 0.8,
	})
	sess := newSession(t, session.Limits{})
	if err := sess.SeedHistory([]session.Message{session.NewUserMessage(strings.Repeat("x", 100))}); err != nil {
		t.Fatal(err)
	}
	var manifest *session.RequestManifestPayload
	for _, ev := range drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})) {
		if ev.Type == session.EvRequestManifest {
			manifest = ev.RequestManifest
		}
	}
	if src.calls != 1 || compactor.calls != 1 || manifest == nil {
		t.Fatalf("source/compactor calls = %d/%d, manifest=%v", src.calls, compactor.calls, manifest)
	}
	plain, err := instructions.Assemble(context.Background())
	if err != nil || len(observed.Messages) < 2 || observed.Messages[0].Text != plain[0].Text || observed.Messages[1].Text != "compacted" {
		t.Fatalf("unexpected model request: %+v, err=%v", observed.Messages, err)
	}
	var rules *session.RequestPromptComponent
	for i := range manifest.Prompt {
		if manifest.Prompt[i].Provenance == session.RequestProvenanceRules {
			rules = &manifest.Prompt[i]
		}
	}
	if rules == nil || len(rules.Rules) != 2 || rules.OmittedRules != 1 {
		t.Fatalf("rules metadata = %+v", rules)
	}
	counter := agent.HeuristicTokenCounter{CharsPerToken: 1}
	for i, metric := range rules.Rules {
		if metric.Origin != []string{"project", "user"}[i] || metric.RenderedBytes <= len(secret) || metric.EstimatedTokens != counter.Count(observed.Messages[0].Text[blockStart(observed.Messages[0].Text, i):blockEnd(observed.Messages[0].Text, i)]) {
			t.Fatalf("rule metric %d = %+v", i, metric)
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "**/*.go") || strings.Contains(string(encoded), "omitted\"") {
		t.Fatalf("manifest leaked rule content: %s, err=%v", encoded, err)
	}
}

func blockStart(text string, index int) int {
	start := strings.Index(text, "\n<rule ")
	if index == 1 {
		start += len("\n<rule ") + strings.Index(text[start+len("\n<rule "):], "\n<rule ")
	}
	return start
}

func blockEnd(text string, index int) int {
	start := blockStart(text, index)
	return start + strings.Index(text[start:], "</rule>") + len("</rule>")
}

type manifestCompactor struct{ calls int }

func (c *manifestCompactor) Compact(context.Context, *session.Conversation) ([]session.Message, string, error) {
	c.calls++
	return []session.Message{session.NewUserMessage("compacted")}, "summary", nil
}

func TestRequestManifestMeasuresPostCompactionRequest(t *testing.T) {
	compactor := &manifestCompactor{}
	var observed port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		observed = req
	})}, mockllm.TextTurn("done"))
	eng := newEngine(agent.Deps{
		LLM: provider, Catalog: tool.NewCatalog(), EnableDurableEvidence: true,
		Compactor: compactor, TokenCounter: agent.HeuristicTokenCounter{CharsPerToken: 1},
		ContextWindow: func() int { return 10 }, CompactionRatio: 0.8,
	})
	sess := newSession(t, session.Limits{})
	if err := sess.SeedHistory([]session.Message{session.NewUserMessage(strings.Repeat("x", 100))}); err != nil {
		t.Fatal(err)
	}

	var manifest *session.RequestManifestPayload
	for _, ev := range drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})) {
		if ev.Type == session.EvRequestManifest {
			manifest = ev.RequestManifest
		}
	}
	if compactor.calls != 1 || manifest == nil {
		t.Fatalf("compactor calls/manifest = %d/%v, want 1/non-nil", compactor.calls, manifest)
	}
	if len(observed.Messages) != 1 || observed.Messages[0].Text != "compacted" {
		t.Fatalf("provider request was not compacted: %+v", observed.Messages)
	}
	wantPersisted := (agent.HeuristicTokenCounter{CharsPerToken: 1}).CountMessages(observed.Messages)
	if manifest.EstimatedPersistedHistoryTokens == nil || *manifest.EstimatedPersistedHistoryTokens != wantPersisted {
		t.Fatalf("post-compaction persisted estimate = %v, want %d", manifest.EstimatedPersistedHistoryTokens, wantPersisted)
	}
}
