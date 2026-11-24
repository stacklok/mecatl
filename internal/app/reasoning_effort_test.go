package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// TestNormalizeReasoningEffort pins the neutral-vocabulary validator:
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

// TestResolveSessionEffortPrecedence pins precedence + fail-soft: a valid
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

// regWithRemintRecorder builds an OpenAI registry the way buildProviderRegistry
// does: the entry's shared provider is an effortProvider at the (unset) operator
// default over the Auto adapter def, and every adapter built for a level is
// recorded and answers "REMINTED:<level>". The live fixture is empty, so gpt-5's
// levels come from the catalog (low, medium, high).
func regWithRemintRecorder(defaultReply string) (*providerRegistry, *mockllm.Provider, *[]string) {
	def := mockllm.New(mockllm.TextTurn(defaultReply))
	var reminted []string
	remint := func(effort string, _ port.ProviderCapabilities) port.LLMProvider {
		reminted = append(reminted, effort)
		return mockllm.New(mockllm.TextTurn("REMINTED:" + effort))
	}
	reg := &providerRegistry{entries: map[string]providerEntry{}, defaultID: providerOpenAI, meta: newMetadataFixture()}
	entry := providerEntry{id: providerOpenAI, available: true, remint: remint}
	entry.provider = reg.newEffortProvider(entry, "gpt-5", "", newEffortAdapters(def, func(effort string) port.LLMProvider {
		return remint(effort, port.ProviderCapabilities{})
	}))
	reg.entries[providerOpenAI] = entry
	return reg, def, &reminted
}

func countMessages(rec *recordingDiag, substr string) int {
	n := 0
	for _, msg := range rec.messages() {
		if strings.Contains(msg, substr) {
			n++
		}
	}
	return n
}

func effortFactory(cfg Config, reg *providerRegistry) server.SessionEngineFactory {
	return sessionEngineFactory(cfg, reg, reg.entries[providerOpenAI].provider,
		memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
}

func runEffortTurn(t *testing.T, res server.SessionEngineResult) string {
	t.Helper()
	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 5}, time.Now())
	return drainRun(res.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi", Parts: nil}))
}

// TestFactorySendsListedEffort: a session requesting a level its model lists
// echoes it and sends it; the adapter for the level is built when the first
// request needs it.
func TestFactorySendsListedEffort(t *testing.T) {
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	res, err := effortFactory(Config{Model: "gpt-5"}, reg)(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5", ReasoningEffort: "high"},
		nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()
	if res.ReasoningEffort != "high" || res.RequestedReasoningEffort != "high" {
		t.Errorf("effort applied=%q requested=%q, want high/high", res.ReasoningEffort, res.RequestedReasoningEffort)
	}
	if got := runEffortTurn(t, res); got != "REMINTED:high" {
		t.Errorf("turn ran on %q, want the high adapter (REMINTED:high)", got)
	}
	if !slices.Equal(*reminted, []string{"high"}) {
		t.Errorf("adapters built = %q, want only high", *reminted)
	}
}

// TestFactoryDropsEffortForModelWithoutLevels: the live listing says the model
// takes no effort, so the session echoes none, runs at Auto, and WARNs once,
// while keeping the requested level.
func TestFactoryDropsEffortForModelWithoutLevels(t *testing.T) {
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	rec := &recordingDiag{}
	reg.diag = rec
	reg.meta.setMetadataFixture(map[string][]modelEntry{
		providerOpenAI: {{ID: "no-effort-model", Effort: knownEffortLevels(nil)}},
	})
	res, err := effortFactory(Config{Model: "gpt-5", Diagnostics: rec}, reg)(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "no-effort-model", ReasoningEffort: "high"},
		nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()
	if res.ReasoningEffort != "" || res.RequestedReasoningEffort != "high" {
		t.Errorf("effort applied=%q requested=%q, want \"\"/high", res.ReasoningEffort, res.RequestedReasoningEffort)
	}
	if got := runEffortTurn(t, res); got != "DEFAULT-REPLY" {
		t.Errorf("turn ran on %q, want the Auto adapter (DEFAULT-REPLY)", got)
	}
	if len(*reminted) != 0 {
		t.Errorf("adapters built = %q, want none", *reminted)
	}
	if n := countMessages(rec, "lists no effort level at or below the requested one"); n != 1 {
		t.Errorf("drop WARNs = %d, want exactly 1; got %v", n, rec.messages())
	}
}

// TestFactoryCapsUnknownModelAtHigh: no source knows the model's levels, and
// OpenAI is a capped provider, so a level up to high is sent as is and xhigh or
// max is sent as high instead of risking a refused request.
func TestFactoryCapsUnknownModelAtHigh(t *testing.T) {
	for _, requested := range []string{"high", "max"} {
		reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
		res, err := effortFactory(Config{Model: "gpt-5"}, reg)(context.Background(),
			server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "totally-unknown-model", ReasoningEffort: requested},
			nil, server.ProfileDefault, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		if res.ReasoningEffort != "high" || res.RequestedReasoningEffort != requested {
			t.Errorf("%s: effort applied=%q requested=%q, want high/%s", requested, res.ReasoningEffort, res.RequestedReasoningEffort, requested)
		}
		if got := runEffortTurn(t, res); got != "REMINTED:high" || !slices.Equal(*reminted, []string{"high"}) {
			t.Errorf("%s: turn ran on %q with adapters %q, want the high adapter", requested, got, *reminted)
		}
		_ = res.Close()
	}
}

// TestFactoryLowersUnlistedLevel: gpt-5 lists low, medium and high, so max is
// sent as high, the highest level it lists.
func TestFactoryLowersUnlistedLevel(t *testing.T) {
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	res, err := effortFactory(Config{Model: "gpt-5"}, reg)(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5", ReasoningEffort: "max"},
		nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()
	if res.ReasoningEffort != "high" || res.RequestedReasoningEffort != "max" {
		t.Errorf("effort applied=%q requested=%q, want high/max", res.ReasoningEffort, res.RequestedReasoningEffort)
	}
	if got := runEffortTurn(t, res); got != "REMINTED:high" || !slices.Equal(*reminted, []string{"high"}) {
		t.Errorf("turn ran on %q with adapters %q, want the high adapter", got, *reminted)
	}
}

// TestFactoryDefaultPathNoRemint: an UNSET session effort (equal to the unset
// operator default) reuses the shared Auto adapter — NO adapter is built.
func TestFactoryDefaultPathNoRemint(t *testing.T) {
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	res, err := effortFactory(Config{Model: "gpt-5"}, reg)(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5"},
		nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()
	if res.ReasoningEffort != "" {
		t.Errorf("echoed ReasoningEffort = %q, want empty (unset)", res.ReasoningEffort)
	}
	if got := runEffortTurn(t, res); got != "DEFAULT-REPLY" {
		t.Errorf("turn ran on %q, want the shared default provider (DEFAULT-REPLY)", got)
	}
	if len(*reminted) != 0 {
		t.Fatalf("an unset effort must NOT build an adapter; got %q", *reminted)
	}
}

// TestServiceToRealFactorySendsPerModelEffort is the TRUE end-to-end (QA): a
// server.Service wired to the REAL sessionEngineFactory. A CreateSession at max on
// a model whose live listing includes max must echo max on ResolvedModel and
// route the turn to the max adapter.
func TestServiceToRealFactorySendsPerModelEffort(t *testing.T) {
	ctx := context.Background()
	reg, _, reminted := regWithRemintRecorder("DEFAULT-REPLY")
	reg.meta.setMetadataFixture(map[string][]modelEntry{
		providerOpenAI: {{ID: "gpt-max", Effort: knownEffortLevels([]string{"medium", "high", "xhigh", "max"})}},
	})
	store := memstore.New()
	factory := sessionEngineFactory(Config{Model: "gpt-5"}, reg, reg.entries[providerOpenAI].provider,
		store, permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	svc, err := newTestServerService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("SHARED")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "gpt-5",
		}),
		Store: store,

		DefaultLimits: session.Limits{MaxTurns: 5},
		Now:           func() time.Time { return time.Unix(0, 0) },
		SessionEngine: factory,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-max", ReasoningEffort: "max"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if rm := svc.ResolvedModel(sess.ID); rm.ReasoningEffort != "max" {
		t.Errorf("ResolvedModel.ReasoningEffort = %q, want max", rm.ReasoningEffort)
	}
	run, err := svc.StartRun(ctx, sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRunApp(run); got != "REMINTED:max" || !slices.Equal(*reminted, []string{"max"}) {
		t.Errorf("turn routed to %q with adapters %q, want the max adapter", got, *reminted)
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
