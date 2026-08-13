package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestNormalizeReasoningEffort pins the neutral-vocabulary validator (ADR 0055):
// auto/"" → unset (ok), the five tiers pass through (lowercased+trimmed), an unknown
// token → ("", false) so the caller treats it as unset + WARN (fail-soft).
func TestNormalizeReasoningEffort(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"", "", true},
		{"auto", "", true},
		{"AUTO", "", true},
		{"  ", "", true},
		{"low", "low", true},
		{"  Medium ", "medium", true},
		{"HIGH", "high", true},
		{"xhigh", "xhigh", true},
		{"max", "max", true},
		{"ultra", "", false},
		{"none", "", false}, // not in the neutral vocabulary
		{"7", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeReasoningEffort(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("NormalizeReasoningEffort(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

// TestClampEffortForProvider pins the per-provider clamp (ADR 0055): openai/openrouter
// clamp xhigh/max DOWN to high (clamped=true); low/medium/high pass through;
// anthropic identity-maps all five; "" is always a no-op.
func TestClampEffortForProvider(t *testing.T) {
	cases := []struct {
		provider, in, want string
		clamped            bool
	}{
		{providerOpenAI, "xhigh", "high", true},
		{providerOpenAI, "max", "high", true},
		{providerOpenAI, "high", "high", false},
		{providerOpenAI, "low", "low", false},
		{providerOpenRouter, "max", "high", true},
		{providerAnthropic, "xhigh", "xhigh", false},
		{providerAnthropic, "max", "max", false},
		{providerAnthropic, "high", "high", false},
		{providerOpenAI, "", "", false},
		{providerAnthropic, "", "", false},
	}
	for _, tc := range cases {
		got, clamped := clampEffortForProvider(tc.provider, tc.in)
		if got != tc.want || clamped != tc.clamped {
			t.Errorf("clampEffortForProvider(%q,%q) = (%q,%v), want (%q,%v)", tc.provider, tc.in, got, clamped, tc.want, tc.clamped)
		}
	}
}

// TestResolveSessionEffortPrecedence pins precedence + fail-soft (ADR 0055): a valid
// per-session value out-ranks the operator default; an unknown per-session value
// falls back to the operator default WITH a WARN; an unknown operator default
// normalises to unset WITH a WARN.
func TestResolveSessionEffortPrecedence(t *testing.T) {
	ctx := context.Background()

	// Per-session wins.
	rec := &recordingDiag{}
	cfg := Config{ReasoningEffort: "low", Diagnostics: rec}
	if got := resolveSessionEffort(ctx, cfg, "high"); got != "high" {
		t.Errorf("per-session should win: got %q want high", got)
	}

	// No per-session value → operator default.
	if got := resolveSessionEffort(ctx, Config{ReasoningEffort: "medium"}, ""); got != "medium" {
		t.Errorf("operator default should apply: got %q want medium", got)
	}

	// Unknown per-session value → fall back to operator default + WARN.
	rec2 := &recordingDiag{}
	cfg2 := Config{ReasoningEffort: "low", Diagnostics: rec2}
	if got := resolveSessionEffort(ctx, cfg2, "ultra"); got != "low" {
		t.Errorf("unknown per-session should fall back to operator default: got %q want low", got)
	}
	if !rec2.has("unknown per-session") {
		t.Errorf("expected a WARN for the unknown per-session value; got %v", rec2.messages())
	}

	// Unknown operator default → unset + WARN.
	rec3 := &recordingDiag{}
	cfg3 := Config{ReasoningEffort: "bananas", Diagnostics: rec3}
	if got := resolveSessionEffort(ctx, cfg3, ""); got != "" {
		t.Errorf("unknown operator default should be unset: got %q want \"\"", got)
	}
	if !rec3.has("unknown operator default") {
		t.Errorf("expected a WARN for the unknown operator default; got %v", rec3.messages())
	}
}

// TestFoldOperatorReasoningEffortCLIOutRanksYAML mirrors foldOperatorPosture:
// a CLI flag (ReasoningEffortFlagSet) wins; a nil resolver is a no-op; a YAML key
// folds when no CLI flag.
func TestFoldOperatorReasoningEffortCLIOutRanksYAML(t *testing.T) {
	// CLI wins.
	cliCfg := Config{ReasoningEffort: "high", ReasoningEffortFlagSet: true}
	if got := foldOperatorReasoningEffort(cliCfg); got.ReasoningEffort != "high" {
		t.Errorf("CLI flag should win: got %q want high", got.ReasoningEffort)
	}
	// Nil resolver no-op.
	if got := foldOperatorReasoningEffort(Config{}); got.ReasoningEffort != "" {
		t.Errorf("nil resolver no-op: got %q want empty", got.ReasoningEffort)
	}
	// YAML folds when no CLI flag.
	path := filepath.Join(t.TempDir(), "effort.yaml")
	if err := os.WriteFile(path, []byte("reasoning-effort: medium\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	res := permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})
	if res == nil {
		t.Fatal("resolver should be non-nil")
	}
	if got := foldOperatorReasoningEffort(Config{permResolver: res}); got.ReasoningEffort != "medium" {
		t.Errorf("YAML reasoning-effort should fold: got %q want medium", got.ReasoningEffort)
	}
}

// TestModelReasoningSupport pins the capability-gate source (ADR 0055): the mock
// provider is known-incapable; an UNKNOWN (uncatalogued) model is known=false (so
// the caller fails open); a catalogued reasoning model is known+supported.
func TestModelReasoningSupport(t *testing.T) {
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerMock: {id: providerMock, provider: mockllm.New(mockllm.TextTurn("x")), available: true},
		},
		defaultID: providerMock,
		meta:      newLiveMetaStore(),
	}
	// Mock → known-incapable.
	if sup, known := modelReasoningSupport(reg, providerMock, "anything"); !known || sup {
		t.Errorf("mock: got (sup=%v,known=%v), want (false,true)", sup, known)
	}
	// Uncatalogued real provider model → unknown → fail open.
	if sup, known := modelReasoningSupport(reg, providerOpenAI, "totally-made-up-model"); known || sup {
		t.Errorf("uncatalogued: got (sup=%v,known=%v), want (false,false=unknown→fail-open)", sup, known)
	}
}

// TestOperatorDefaultEffortForClampsAndWarns pins the STARTUP clamp narration (ADR
// 0055): an operator default of "max" against OpenAI clamps to "high" AND emits a
// WARN naming both the requested and clamped-to values; against Anthropic it
// identity-maps with NO warn; an unset default is silent.
func TestOperatorDefaultEffortForClampsAndWarns(t *testing.T) {
	rec := &recordingDiag{}
	got := operatorDefaultEffortFor(Config{ReasoningEffort: "max", Diagnostics: rec}, providerOpenAI)
	if got != "high" {
		t.Errorf("openai max → %q, want high (clamped)", got)
	}
	if !rec.has("clamped operator default") {
		t.Errorf("expected a startup clamp WARN; got %v", rec.messages())
	}

	rec2 := &recordingDiag{}
	got2 := operatorDefaultEffortFor(Config{ReasoningEffort: "max", Diagnostics: rec2}, providerAnthropic)
	if got2 != "max" {
		t.Errorf("anthropic max → %q, want max (identity)", got2)
	}
	if rec2.has("clamped") {
		t.Errorf("anthropic must NOT clamp/warn; got %v", rec2.messages())
	}

	// Unset default: silent, returns "".
	rec3 := &recordingDiag{}
	if got3 := operatorDefaultEffortFor(Config{Diagnostics: rec3}, providerOpenAI); got3 != "" {
		t.Errorf("unset default → %q, want empty", got3)
	}
	if len(rec3.messages()) != 0 {
		t.Errorf("unset default must be silent; got %v", rec3.messages())
	}
}

// recordingMock is a mockllm whose construction effort we capture via a recording
// remint closure.
func regWithRemintRecorder(defaultReply string) (*providerRegistry, *mockllm.Provider, *[]string) {
	def := mockllm.New(mockllm.TextTurn(defaultReply))
	var reminted []string
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenAI: {
				id:        providerOpenAI,
				provider:  def,
				available: true,
				remint: func(effort string, _ port.ProviderCapabilities) port.LLMProvider {
					reminted = append(reminted, effort)
					return mockllm.New(mockllm.TextTurn("REMINTED:" + effort))
				},
			},
		},
		defaultID: providerOpenAI,
		meta:      newLiveMetaStore(),
	}
	return reg, def, &reminted
}

// TestFactoryRemintsOnEffortDiffersFromDefault is the composition e2e: a session
// requesting an effort DIFFERENT from the operator default re-mints the adapter
// (the recording closure captures the clamped token), the turn runs on the re-minted
// provider, and the result echoes the resolved effort (ADR 0055).
func TestFactoryRemintsOnEffortDiffersFromDefault(t *testing.T) {
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	cfg := Config{Model: "gpt-5"} // operator default effort unset
	factory := sessionEngineFactory(cfg, reg, reg.entries[providerOpenAI].provider,
		memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5", ReasoningEffort: "high"},
		nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	if len(*reminted) != 1 || (*reminted)[0] != "high" {
		t.Fatalf("expected one re-mint with effort \"high\", got %v", *reminted)
	}
	if res.ReasoningEffort != "high" {
		t.Errorf("echoed ReasoningEffort = %q, want high", res.ReasoningEffort)
	}
	// The turn runs on the re-minted provider.
	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 5}, time.Now())
	got := drainRun(res.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi", Parts: nil}))
	if got != "REMINTED:high" {
		t.Errorf("turn ran on %q, want the re-minted provider (REMINTED:high)", got)
	}
}

// TestFactoryDegradesOnNoReasoningModel is the capability-gate DEGRADE (ADR 0055,
// adversarial #1): a session on a model the live source says has NO reasoning support
// drops the effort (no re-mint) and WARNs. A re-mint recorder + a recording diag prove
// both halves.
func TestFactoryDegradesOnNoReasoningModel(t *testing.T) {
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	rec := &recordingDiag{}
	// The live source describes "no-reason-model" as Reasoning:false (known-incapable).
	reg.meta.Swap(map[string][]modelEntry{
		providerOpenAI: {{ID: "no-reason-model", Reasoning: false}},
	})
	cfg := Config{Model: "gpt-5", Diagnostics: rec}
	factory := sessionEngineFactory(cfg, reg, reg.entries[providerOpenAI].provider,
		memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "no-reason-model", ReasoningEffort: "high"},
		nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()
	if len(*reminted) != 0 {
		t.Fatalf("a no-reasoning model must NOT receive effort (no re-mint); got %v", *reminted)
	}
	if res.ReasoningEffort != "" {
		t.Errorf("echoed ReasoningEffort = %q, want empty (degraded)", res.ReasoningEffort)
	}
	if !rec.has("does not support reasoning effort") {
		t.Errorf("expected a degrade WARN; got %v", rec.messages())
	}
}

// TestFactoryFailsOpenOnUnknownModel is the capability-gate FAIL-OPEN (ADR 0055,
// adversarial fail-open arm): a session on an UNKNOWN (uncatalogued, no live entry)
// model SENDS the effort anyway (re-mint happens) — the provider 400s honestly if it
// really cannot, matching the thinking-path unknown=capable posture.
func TestFactoryFailsOpenOnUnknownModel(t *testing.T) {
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	factory := sessionEngineFactory(Config{Model: "gpt-5"}, reg, reg.entries[providerOpenAI].provider,
		memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "totally-unknown-model", ReasoningEffort: "high"},
		nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()
	if len(*reminted) != 1 || (*reminted)[0] != "high" {
		t.Fatalf("an unknown model must FAIL OPEN (send effort): got %v", *reminted)
	}
	if res.ReasoningEffort != "high" {
		t.Errorf("echoed ReasoningEffort = %q, want high (fail-open)", res.ReasoningEffort)
	}
}

// TestFactoryClampsForOpenAIAndEchoes: an OpenAI session requesting "max" is clamped
// to "high" — the re-mint receives "high" and the echo is "high" (the locked
// xhigh/max→high contract).
func TestFactoryClampsForOpenAIAndEchoes(t *testing.T) {
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	factory := sessionEngineFactory(Config{Model: "gpt-5"}, reg, reg.entries[providerOpenAI].provider,
		memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5", ReasoningEffort: "max"},
		nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()
	if len(*reminted) != 1 || (*reminted)[0] != "high" {
		t.Fatalf("openai \"max\" must clamp to \"high\" at re-mint, got %v", *reminted)
	}
	if res.ReasoningEffort != "high" {
		t.Errorf("echoed ReasoningEffort = %q, want high (clamped)", res.ReasoningEffort)
	}
}

// TestFactoryDefaultPathNoRemint: an UNSET session effort (equal to the unset
// operator default) reuses the shared provider — NO re-mint (the byte-identical
// default path).
func TestFactoryDefaultPathNoRemint(t *testing.T) {
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	factory := sessionEngineFactory(Config{Model: "gpt-5"}, reg, reg.entries[providerOpenAI].provider,
		memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	// A model-only selector (no effort) must NOT re-mint.
	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5"},
		nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()
	if len(*reminted) != 0 {
		t.Fatalf("an unset effort must NOT re-mint; got %v", *reminted)
	}
	if res.ReasoningEffort != "" {
		t.Errorf("echoed ReasoningEffort = %q, want empty (unset)", res.ReasoningEffort)
	}
	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 5}, time.Now())
	got := drainRun(res.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi", Parts: nil}))
	if got != "DEFAULT-REPLY" {
		t.Errorf("turn ran on %q, want the shared default provider (DEFAULT-REPLY)", got)
	}
}

// TestServiceToRealFactoryRemintsClampedEffort is the TRUE end-to-end (QA): a
// server.Service wired to the REAL sessionEngineFactory (over a recording-remint
// registry) — the one Service→real-factory join the other tests stub. A
// CreateSession with reasoning_effort:"max" on OpenAI must (a) construct the adapter
// with the CLAMPED "high" (the recorder captures it), (b) echo "high" on
// ResolvedModel, and (c) route the turn to the re-minted provider.
func TestServiceToRealFactoryRemintsClampedEffort(t *testing.T) {
	ctx := context.Background()
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	store := memstore.New()
	factory := sessionEngineFactory(Config{Model: "gpt-5"}, reg, reg.entries[providerOpenAI].provider,
		store, permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("SHARED")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "gpt-5",
		}),
		Store:         store,
		Workspaces:    func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits: session.Limits{MaxTurns: 5},
		Now:           func() time.Time { return time.Unix(0, 0) },
		SessionEngine: factory,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sess, err := svc.CreateSessionWithProvider(ctx, "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5", ReasoningEffort: "max"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	// (a) the REAL factory clamped openai "max"→"high" and constructed the adapter with it.
	if len(*reminted) != 1 || (*reminted)[0] != "high" {
		t.Fatalf("real factory must re-mint the OpenAI adapter with the clamped \"high\"; got %v", *reminted)
	}
	// (b) the Service echoes the clamped effort on resolved_model.
	if rm := svc.ResolvedModel(sess.ID); rm.ReasoningEffort != "high" {
		t.Errorf("ResolvedModel.ReasoningEffort = %q, want \"high\" (clamped)", rm.ReasoningEffort)
	}
	// (c) the turn runs on the re-minted provider through the Service.
	run, err := svc.StartRun(ctx, sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRunApp(run); got != "REMINTED:high" {
		t.Errorf("turn routed to %q, want the re-minted provider (REMINTED:high)", got)
	}
}

// drainServerRunApp consumes a server-started run to completion, auto-allowing any
// permission ask, returning the terminal text (the app-package twin of the
// server_test drainServerRun helper).
func drainServerRunApp(run *agent.Run) string {
	var final string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	return final
}
