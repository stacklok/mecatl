package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
)

// toolhiveLevelDiag captures Log calls WITH their level, so a test can assert
// both the message substring and the severity (INFO vs WARN) the probe emits.
type toolhiveLevelDiag struct {
	mu    sync.Mutex
	lines []struct {
		level port.Level
		msg   string
	}
}

func (d *toolhiveLevelDiag) Log(_ context.Context, level port.Level, msg string, _ ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines = append(d.lines, struct {
		level port.Level
		msg   string
	}{level, msg})
}
func (d *toolhiveLevelDiag) With(...any) port.Diagnostics { return d }

func (d *toolhiveLevelDiag) hasAtLevel(level port.Level, sub string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, l := range d.lines {
		if l.level == level && strings.Contains(l.msg, sub) {
			return true
		}
	}
	return false
}

func (d *toolhiveLevelDiag) has(sub string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, l := range d.lines {
		if strings.Contains(l.msg, sub) {
			return true
		}
	}
	return false
}

// toolhiveModelsClient serves a canned OpenAI-shaped /v1/models response over
// an injected transport — never a real network call.
func toolhiveModelsClient(t *testing.T, body string) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
}

func toolhiveUnauthorizedClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("nope")), Header: make(http.Header)}, nil
	})}
}

const toolhiveFixtureJSON = `{"object":"list","data":[
  {"id":"claude-sonnet-4-6","display_name":"Claude Sonnet 4.6"},
  {"id":"gpt-5","display_name":"GPT-5"}
]}`

// writeToolhiveConfig writes a minimal ToolHive config.yaml fixture (an `llm:`
// block only) and returns its path — the toolhiveConfigPath test seam always
// points here, never the real home directory.
func writeToolhiveConfig(t *testing.T, gatewayURL string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "llm:\n  gateway_url: " + gatewayURL + "\n  proxy:\n    listen_port: 14000\n"
	if err := writeFileT(t, path, body); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	return path
}

// (1) intent + probe-ok ⇒ entry registered, lister wired, intentDriven, baseURL loopback.
func TestToolhiveIntent_ProbeOK_Registered(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	diag := &toolhiveLevelDiag{}
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
		Diagnostics:         diag,
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup(providerToolhive)
	if !ok {
		t.Fatal("toolhive entry not registered")
	}
	if entry.lister == nil {
		t.Fatal("toolhive entry has no lister")
	}
	if !entry.intentDriven {
		t.Error("toolhive entry should be intentDriven")
	}
	if !strings.HasPrefix(entry.baseURL, "http://127.0.0.1:") {
		t.Errorf("baseURL = %q, want loopback", entry.baseURL)
	}
	if !diag.hasAtLevel(port.LevelInfo, "registered and reachable") {
		t.Error("expected an INFO 'registered and reachable' diagnostic")
	}
}

// (2) intent + probe-down (mock transport refuses) ⇒ entry STILL registered;
// toolhive models empty; status unreachable.
func TestToolhiveIntent_ProbeDown_StillRegistered(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	diag := &toolhiveLevelDiag{}
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: offlineHTTPClient(),
		Diagnostics:         diag,
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup(providerToolhive)
	if !ok {
		t.Fatal("toolhive entry must still be registered when the probe fails")
	}
	status, ok := reg.outcomes.getStatus(providerToolhive)
	if !ok || status.State != statusUnreachable {
		t.Fatalf("status = %+v, ok=%v, want unreachable", status, ok)
	}
	if !diag.hasAtLevel(port.LevelInfo, "probe failed") {
		t.Error("expected an INFO probe-failed diagnostic (non-explicit intent)")
	}
	_ = entry
}

// (3) Byte-identical baseline: ToolhiveLLM:false vs ToolhiveLLM:true+nonexistent
// path ⇒ registry surface identical, zero probe HTTP calls.
func TestToolhiveIntent_ByteIdenticalBaseline(t *testing.T) {
	env := fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"})

	regOff, err := buildProviderRegistry(Config{Model: "gpt-5"}, env)
	if err != nil {
		t.Fatalf("buildProviderRegistry (off): %v", err)
	}

	var calls int
	countingClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, context.DeadlineExceeded
	})}
	regOn, err := buildProviderRegistry(Config{
		Model:               "gpt-5",
		ToolhiveLLM:         true,
		toolhiveConfigPath:  filepath.Join(t.TempDir(), "nonexistent.yaml"),
		liveModelHTTPClient: countingClient,
	}, env)
	if err != nil {
		t.Fatalf("buildProviderRegistry (on, no config): %v", err)
	}

	if calls != 0 {
		t.Errorf("expected zero probe HTTP calls with no detected config, got %d", calls)
	}
	if got, want := regOn.Available(), regOff.Available(); !equalStrings(got, want) {
		t.Errorf("Available() = %v, want %v", got, want)
	}
	if regOn.Default() != regOff.Default() {
		t.Errorf("Default() = %q, want %q", regOn.Default(), regOff.Default())
	}
	if regOn.ResolvedDefaultModel() != regOff.ResolvedDefaultModel() {
		t.Errorf("ResolvedDefaultModel() = %q, want %q", regOn.ResolvedDefaultModel(), regOff.ResolvedDefaultModel())
	}
	eOn, _ := regOn.Lookup(providerOpenAI)
	eOff, _ := regOff.Lookup(providerOpenAI)
	if eOn.baseURL != eOff.baseURL || (eOn.lister == nil) != (eOff.lister == nil) {
		t.Errorf("openai entry drifted: on=%+v off=%+v", eOn, eOff)
	}
}

// (4) toolhive sole + probe-ok ⇒ Default()=="toolhive", ResolvedDefaultModel()==first-listed.
func TestToolhiveSole_ProbeOK_DefaultAndModel(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if reg.Default() != providerToolhive {
		t.Fatalf("Default() = %q, want %q", reg.Default(), providerToolhive)
	}
	if want := "claude-sonnet-4-6"; reg.ResolvedDefaultModel() != want {
		t.Errorf("ResolvedDefaultModel() = %q, want %q (first-listed)", reg.ResolvedDefaultModel(), want)
	}
}

// (5) toolhive sole + probe-ok + empty list ⇒ Build error containing the
// actionable copy (R2.3).
func TestToolhiveSole_ProbeOKEmpty_BuildFails(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	_, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: toolhiveModelsClient(t, `{"object":"list","data":[]}`),
	}, fakeEnv(nil))
	if err == nil {
		t.Fatal("expected an error when the sole provider's credential lists zero models")
	}
	if !strings.Contains(err.Error(), "lists no models") {
		t.Errorf("error %q missing the actionable copy", err.Error())
	}
}

// (6) toolhive sole + probe-down ⇒ Build SUCCEEDS, default toolhive, model "",
// WARN present; the sync-refresh path then heals defaultModel to first-listed
// (the §1 accepted deviation).
func TestToolhiveSole_ProbeDown_BootsThenHeals(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	diag := &toolhiveLevelDiag{}
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: offlineHTTPClient(),
		Diagnostics:         diag,
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry must succeed even when the sole provider is down: %v", err)
	}
	if reg.Default() != providerToolhive {
		t.Fatalf("Default() = %q, want %q", reg.Default(), providerToolhive)
	}
	if reg.ResolvedDefaultModel() != "" {
		t.Fatalf("ResolvedDefaultModel() = %q, want empty until healed", reg.ResolvedDefaultModel())
	}

	// Now simulate the proxy coming up: swap the lister's transport by
	// re-pointing the entry (buildProviderRegistry already wired the lister
	// over the ORIGINAL client) — instead, exercise healDefaultModel directly
	// the way the live-refresh swap path calls it.
	preHeal, ok := reg.Lookup(providerToolhive)
	if !ok {
		t.Fatal("toolhive entry missing before heal")
	}

	reg.healDefaultModel(diag, map[string][]modelEntry{
		providerToolhive: {{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6"}},
	})
	if want := "claude-sonnet-4-6"; reg.ResolvedDefaultModel() != want {
		t.Errorf("ResolvedDefaultModel() after heal = %q, want %q", reg.ResolvedDefaultModel(), want)
	}
	if !diag.has("auto-selected") {
		t.Error("expected an 'auto-selected' INFO after healing")
	}

	// Issue #262 review finding 4: the runtime heal must re-mint the entry's
	// caps/provider exactly like the Build-time auto-select does — a stale
	// defaultCaps baseline (computed for model="") would make the per-session
	// factory's capsDiff comparison wrong for every session against the
	// healed default.
	postHeal, ok := reg.Lookup(providerToolhive)
	if !ok {
		t.Fatal("toolhive entry missing after heal")
	}
	wantCaps := modelCapability(reg, providerToolhive, "claude-sonnet-4-6")
	if postHeal.defaultCaps != wantCaps {
		t.Errorf("defaultCaps after heal = %+v, want %+v (modelCapability for the healed model)", postHeal.defaultCaps, wantCaps)
	}
	if preHeal.provider == postHeal.provider {
		t.Error("provider unchanged after heal — the caps/effort re-mint did not run (review finding 4)")
	}
}

// toggleTransport is a stateful http.RoundTripper that fails until Store(true)
// flips it "up" — the seam TestToolhiveSole_ProbeDown_HealsThroughRealRefresh
// uses to simulate the proxy coming up BETWEEN two real fetches over the
// SAME lister (unlike offlineHTTPClient/toolhiveModelsClient, which are each
// permanently one state).
type toggleTransport struct {
	up   atomic.Bool
	body string
}

func (t *toggleTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if !t.up.Load() {
		return nil, errors.New("connection refused: proxy not running")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Header:     make(http.Header),
	}, nil
}

// TestToolhiveSole_ProbeDown_HealsThroughRealRefresh drives healDefaultModel
// through the REAL swap path (startLiveModelRefresh, sync=true) rather than
// calling it directly — proving the §1 deviation's heal actually fires end to
// end through the mechanism production uses (the direct-call test above only
// proves the method's own logic, not that the wiring reaches it).
func TestToolhiveSole_ProbeDown_HealsThroughRealRefresh(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	transport := &toggleTransport{body: toolhiveFixtureJSON} // starts down (up=false)
	client := &http.Client{Transport: transport}

	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: client,
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry must succeed even when the sole provider is down: %v", err)
	}
	if reg.Default() != providerToolhive {
		t.Fatalf("Default() = %q, want %q", reg.Default(), providerToolhive)
	}
	if reg.ResolvedDefaultModel() != "" {
		t.Fatalf("ResolvedDefaultModel() = %q, want empty until healed", reg.ResolvedDefaultModel())
	}

	transport.up.Store(true) // "the proxy comes up"

	swap := newFakeSwapper()
	startLiveModelRefresh(port.NopDiagnostics{}, reg, swap, true /* sync */, 0)

	if want := "claude-sonnet-4-6"; reg.ResolvedDefaultModel() != want {
		t.Fatalf("ResolvedDefaultModel() after a real sync live-refresh = %q, want %q (first-listed)", reg.ResolvedDefaultModel(), want)
	}
}

// TestHealDefaultModel_ConcurrentWithResolvedDefaultModel is the `-race`
// falsifiable pin for the data race a review found: healDefaultModel can run
// from BOTH the async live-refresh goroutine and refreshStaleModels
// (reachable off the ListModels request path), and ResolvedDefaultModel can
// be read from any request-handling goroutine at any time post-Build. This
// hammers a healDefaultModel writer against concurrent ResolvedDefaultModel
// readers AND concurrent healDefaultModel callers (simulating both real
// racers) — it must pass under `go test -race`. The entry carries a remint
// closure (issue #262 review finding 4) so healDefaultModel's remintEntry
// actually WRITES the entries map on every heal — the entriesMu `-race` pin
// this test exists for would be a no-op against a remint-less entry (the
// write branch never fires). Concurrent Lookup/Available readers are added
// alongside ResolvedDefaultModel to hammer entriesMu directly, not just
// defaultModelMu.
func TestHealDefaultModel_ConcurrentWithResolvedDefaultModel(t *testing.T) {
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {
				id: providerToolhive, intentDriven: true,
				remint: func(string, port.ProviderCapabilities) port.LLMProvider {
					return mockllm.New(mockllm.TextTurn("x"))
				},
			},
		},
		defaultID: providerToolhive,
	}
	byProvider := map[string][]modelEntry{providerToolhive: {{ID: "claude-sonnet-4-6"}}}
	diag := port.NopDiagnostics{}

	var wg sync.WaitGroup
	// Many concurrent healers (simulating the async refresh + refreshStaleModels racing).
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.healDefaultModel(diag, byProvider)
		}()
	}
	// Many concurrent Lookup/Available readers (entriesMu — review finding 4).
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = reg.Lookup(providerToolhive)
			_ = reg.Available()
		}()
	}
	// Many concurrent readers (simulating request-handling goroutines).
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.ResolvedDefaultModel()
		}()
	}
	wg.Wait()

	if want := "claude-sonnet-4-6"; reg.ResolvedDefaultModel() != want {
		t.Fatalf("ResolvedDefaultModel() = %q, want %q", reg.ResolvedDefaultModel(), want)
	}
}

// TestHealDefaultModel_NoOpWhenAlreadySet: a non-empty defaultModel is NEVER
// overwritten (an operator/session pin must survive a later heal attempt).
func TestHealDefaultModel_NoOpWhenAlreadySet(t *testing.T) {
	reg := &providerRegistry{
		entries:   map[string]providerEntry{providerToolhive: {id: providerToolhive, intentDriven: true}},
		defaultID: providerToolhive, defaultModel: "pinned-model",
	}
	reg.healDefaultModel(&toolhiveLevelDiag{}, map[string][]modelEntry{
		providerToolhive: {{ID: "other-model"}},
	})
	if reg.defaultModel != "pinned-model" {
		t.Errorf("defaultModel = %q, want unchanged 'pinned-model'", reg.defaultModel)
	}
}

// (7) Tier test: openai-keyed + toolhive ⇒ default openai; anthropic-keyed +
// toolhive ⇒ default anthropic (proves the TIER, not alphabetics — "toolhive"
// sorts after "anthropic" and before "openai" alphabetically, so this would
// fail under a naive sorted-first pick for the anthropic case, and pass by
// accident for openai; the anthropic case is the load-bearing half).
func TestToolhiveTier_KeyedProviderAlwaysWinsDefault(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	client := toolhiveModelsClient(t, toolhiveFixtureJSON)

	t.Run("openai-keyed", func(t *testing.T) {
		reg, err := buildProviderRegistry(Config{
			ToolhiveLLM: true, toolhiveConfigPath: cfgPath, liveModelHTTPClient: client,
		}, fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		if reg.Default() != providerOpenAI {
			t.Errorf("Default() = %q, want %q", reg.Default(), providerOpenAI)
		}
	})

	t.Run("anthropic-keyed", func(t *testing.T) {
		reg, err := buildProviderRegistry(Config{
			ToolhiveLLM: true, toolhiveConfigPath: cfgPath, liveModelHTTPClient: client,
		}, fakeEnv(map[string]string{"ANTHROPIC_API_KEY": "sk-anthropic"}))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		if reg.Default() != providerAnthropic {
			t.Errorf("Default() = %q, want %q (a keyed provider must ALWAYS out-tier an intent-driven one)", reg.Default(), providerAnthropic)
		}
	})
}

// (8) --toolhive-llm-base-url must resolve to loopback (validateToolhiveBaseURL);
// an explicit valid loopback override SKIPS the config-file read entirely — a
// poisoned config path would fail this test if it were read.
func TestToolhiveExplicitBaseURL_LoopbackOnly(t *testing.T) {
	if err := validateToolhiveBaseURL(Config{ToolhiveLLMBaseURL: "http://10.0.0.5:14000/v1"}); err == nil {
		t.Fatal("expected an error for a non-loopback --toolhive-llm-base-url")
	}
	if err := validateToolhiveBaseURL(Config{ToolhiveLLMBaseURL: "http://127.0.0.1:9999/v1"}); err != nil {
		t.Fatalf("loopback base-url must validate: %v", err)
	}
	if err := validateToolhiveBaseURL(Config{ToolhiveLLMBaseURL: "http://localhost:9999/v1"}); err != nil {
		t.Fatalf("\"localhost\" must validate: %v", err)
	}
	if err := validateToolhiveBaseURL(Config{ToolhiveLLMBaseURL: "http://[::1]:9999/v1"}); err != nil {
		t.Fatalf("[::1] must validate: %v", err)
	}
	if err := validateToolhiveBaseURL(Config{ToolhiveLLMBaseURL: "ftp://127.0.0.1:9999/v1"}); err == nil {
		t.Fatal("expected an error for a non-http(s) scheme")
	}
}

func TestToolhiveExplicitBaseURL_SkipsConfigFile(t *testing.T) {
	// A path that would fail the test if DetectConfig ever read it (malformed
	// YAML — a read attempt errors loudly in spirit, though DetectConfig itself
	// fails soft; the REAL assertion is the zero-calls counter below).
	poisonPath := filepath.Join(t.TempDir(), "poison.yaml")
	if err := writeFileT(t, poisonPath, "llm: [not a mapping\n"); err != nil {
		t.Fatalf("write poison fixture: %v", err)
	}
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLMBaseURL:  "http://127.0.0.1:9999/v1",
		toolhiveConfigPath:  poisonPath,
		liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup(providerToolhive)
	if !ok {
		t.Fatal("toolhive entry not registered via explicit base-url")
	}
	if entry.baseURL != "http://127.0.0.1:9999/v1" {
		t.Errorf("baseURL = %q, want the explicit override verbatim", entry.baseURL)
	}
	if !entry.intentExplicit {
		t.Error("intentExplicit should be true for an explicit base-url override")
	}
}

// (9) clampEffortForProvider joins toolhive to the openai/openrouter clamp case.
func TestClampEffortForProvider_Toolhive(t *testing.T) {
	for _, effort := range []string{effortMax, effortXHigh} {
		got, didClamp := clampEffortForProvider(providerToolhive, effort)
		if !didClamp || got != effortHigh {
			t.Errorf("clampEffortForProvider(toolhive, %q) = (%q, %v), want (high, true)", effort, got, didClamp)
		}
	}
	if got, didClamp := clampEffortForProvider(providerToolhive, effortLow); didClamp || got != effortLow {
		t.Errorf("clampEffortForProvider(toolhive, low) = (%q, %v), want (low, false)", got, didClamp)
	}
}

// (12) Status projection: providerStatusProto covers ok/unreachable/
// unauthorized/empty; openrouter/anthropic produce NO status rows (v1 is
// toolhive-scoped).
func TestProviderStatusProto_ToolhiveScopedOnly(t *testing.T) {
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenRouter: {id: providerOpenRouter, available: true},
			providerToolhive:   {id: providerToolhive, available: true, intentDriven: true},
		},
		outcomes: newLiveOutcomeStore(),
	}
	reg.outcomes.recordFailure(providerOpenRouter, statusUnreachable, "should never surface")
	reg.outcomes.recordSuccess(providerToolhive, []modelEntry{{ID: "m1"}})

	out := providerStatusProto(reg)
	if len(out) != 1 {
		t.Fatalf("got %d status rows, want 1 (toolhive only): %+v", len(out), out)
	}
	if out[0].GetProviderId() != providerToolhive || out[0].GetState() != statusOK {
		t.Errorf("row = %+v, want {toolhive ok}", out[0])
	}
}

func TestProviderStatusProto_AllFourStates(t *testing.T) {
	reg := &providerRegistry{
		entries:  map[string]providerEntry{providerToolhive: {id: providerToolhive, available: true, intentDriven: true}},
		outcomes: newLiveOutcomeStore(),
	}

	reg.outcomes.recordSuccess(providerToolhive, []modelEntry{{ID: "m1"}})
	if got := providerStatusProto(reg)[0].GetState(); got != statusOK {
		t.Errorf("state = %q, want ok", got)
	}

	reg.outcomes.recordFailure(providerToolhive, statusUnreachable, toolhiveStatusHints[statusUnreachable])
	if got := providerStatusProto(reg)[0]; got.GetState() != statusUnreachable || got.GetHint() == "" {
		t.Errorf("row = %+v, want unreachable with a hint", got)
	}

	reg.outcomes.recordFailure(providerToolhive, statusUnauthorized, toolhiveStatusHints[statusUnauthorized])
	if got := providerStatusProto(reg)[0]; got.GetState() != statusUnauthorized || got.GetHint() == "" {
		t.Errorf("row = %+v, want unauthorized with a hint", got)
	}

	reg.outcomes.recordSuccess(providerToolhive, nil) // empty embedded catalog too
	if got := providerStatusProto(reg)[0]; got.GetState() != statusEmpty || got.GetHint() == "" {
		t.Errorf("row = %+v, want empty with a hint", got)
	}
}

// TestProviderStatusProto_AutoSelectedBit is the F7 (issue #262 review
// finding 7) server-side pin: probeToolhive's Build-time auto-pick sets
// default_model_auto_selected on the toolhive status row; a Build with an
// operator-configured model (cfg.Model, tier 1 — the same precedence a
// --default-model would occupy, without validateDefaultModel's catalog gate
// toolhive can never satisfy) does NOT set it, even though toolhive is still
// the default provider.
func TestProviderStatusProto_AutoSelectedBit(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	client := toolhiveModelsClient(t, toolhiveFixtureJSON)

	t.Run("auto-pick sets the bit", func(t *testing.T) {
		reg, err := buildProviderRegistry(Config{
			ToolhiveLLM: true, toolhiveConfigPath: cfgPath, liveModelHTTPClient: client,
		}, fakeEnv(nil))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		rows := providerStatusProto(reg)
		if len(rows) != 1 || !rows[0].GetDefaultModelAutoSelected() {
			t.Fatalf("rows = %+v, want exactly one toolhive row with DefaultModelAutoSelected=true", rows)
		}
	})

	t.Run("operator-configured model does not set the bit", func(t *testing.T) {
		reg, err := buildProviderRegistry(Config{
			ToolhiveLLM: true, toolhiveConfigPath: cfgPath, liveModelHTTPClient: client,
			Model: "gpt-5", // an explicit tier-1 operator choice
		}, fakeEnv(nil))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		if reg.Default() != providerToolhive {
			t.Fatalf("Default() = %q, want toolhive (still the sole provider)", reg.Default())
		}
		rows := providerStatusProto(reg)
		if len(rows) != 1 || rows[0].GetDefaultModelAutoSelected() {
			t.Fatalf("rows = %+v, want exactly one toolhive row with DefaultModelAutoSelected=false (operator-configured)", rows)
		}
	})
}

// TestProviderStatusProto_AvailableNotDefault_TrueWhenKeyedProviderIsDefault
// (this wave) pins available_not_default: when toolhive is registered +
// probed-ok + openrouter is keyed (so openrouter outranks it on the
// precedence ladder and becomes default), the toolhive status row carries
// available_not_default==true AND model_count==N (the live listing's length).
func TestProviderStatusProto_AvailableNotDefault_TrueWhenKeyedProviderIsDefault(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	client := toolhiveModelsClient(t, toolhiveFixtureJSON) // 2 models in the fixture
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: client,
	}, fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk-or"})) // keyed ⇒ openrouter is default
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if reg.Default() != providerOpenRouter {
		t.Fatalf("Default() = %q, want %q (keyed provider must outrank intent-driven)", reg.Default(), providerOpenRouter)
	}
	rows := providerStatusProto(reg)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (toolhive only): %+v", len(rows), rows)
	}
	row := rows[0]
	if row.GetProviderId() != providerToolhive {
		t.Fatalf("row provider = %q, want %q", row.GetProviderId(), providerToolhive)
	}
	if row.GetState() != statusOK {
		t.Errorf("state = %q, want ok", row.GetState())
	}
	if !row.GetAvailableNotDefault() {
		t.Errorf("available_not_default = false, want true (toolhive ok but openrouter is default)")
	}
	if want := int32(2); row.GetModelCount() != want { // toolhiveFixtureJSON has 2 models
		t.Errorf("model_count = %d, want %d", row.GetModelCount(), want)
	}
	if row.GetDefaultModelAutoSelected() {
		t.Errorf("default_model_auto_selected = true, want false (toolhive is NOT the default provider)")
	}
}

// TestProviderStatusProto_AvailableNotDefault_FalseWhenSoleProviderIsDefault
// is the inverse pin: toolhive sole + probed-ok ⇒ it IS the default, so
// available_not_default==false (the condition is "available AND not default",
// and the sole-provider case is the default by definition).
func TestProviderStatusProto_AvailableNotDefault_FalseWhenSoleProviderIsDefault(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	client := toolhiveModelsClient(t, toolhiveFixtureJSON)
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: client,
	}, fakeEnv(nil)) // no keyed provider ⇒ toolhive sole ⇒ default
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if reg.Default() != providerToolhive {
		t.Fatalf("Default() = %q, want %q (sole provider)", reg.Default(), providerToolhive)
	}
	rows := providerStatusProto(reg)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.GetAvailableNotDefault() {
		t.Errorf("available_not_default = true, want false (toolhive IS the default)")
	}
	if want := int32(2); row.GetModelCount() != want {
		t.Errorf("model_count = %d, want %d", row.GetModelCount(), want)
	}
}

// TestProviderStatusProto_AvailableNotDefault_FalseWhenUnreachable pins that a
// probe-down toolhive (state != "ok") never advertises available_not_default
// even when it is NOT the default — the condition requires reachability.
func TestProviderStatusProto_AvailableNotDefault_FalseWhenUnreachable(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: offlineHTTPClient(), // probe fails
	}, fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk-or"})) // openrouter default
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	rows := providerStatusProto(reg)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.GetAvailableNotDefault() {
		t.Errorf("available_not_default = true, want false (toolhive unreachable)")
	}
	if row.GetModelCount() != 0 {
		t.Errorf("model_count = %d, want 0 (never listed successfully)", row.GetModelCount())
	}
}

// TestProbeToolhive_Unauthorized_WarnsAndClassifies pins the 401/403 ⇒
// unauthorized classification + the WARN level (always WARN regardless of
// explicit/detected source).
func TestProbeToolhive_Unauthorized_Warns(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	diag := &toolhiveLevelDiag{}
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: toolhiveUnauthorizedClient(),
		Diagnostics:         diag,
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	status, ok := reg.outcomes.getStatus(providerToolhive)
	if !ok || status.State != statusUnauthorized {
		t.Fatalf("status = %+v, want unauthorized", status)
	}
	if !diag.hasAtLevel(port.LevelWarn, "probe failed") {
		t.Error("expected a WARN probe-failed diagnostic for an unauthorized credential")
	}
}

// TestResolveToolhiveIntent_Disabled: ToolhiveLLM=false skips detection
// entirely, even when a valid config file is present (the shared-host opt-out).
func TestResolveToolhiveIntent_Disabled(t *testing.T) {
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	_, _, _, ok := resolveToolhiveIntent(Config{ToolhiveLLM: false, toolhiveConfigPath: cfgPath})
	if ok {
		t.Fatal("expected no intent when ToolhiveLLM is false")
	}
}

func TestPreferredDefaultProvider_ToolhiveOnly(t *testing.T) {
	reg := &providerRegistry{entries: map[string]providerEntry{
		providerToolhive: {id: providerToolhive, intentDriven: true},
	}}
	if got := preferredDefaultProvider(reg); got != providerToolhive {
		t.Errorf("preferredDefaultProvider() = %q, want %q (a zero-key ToolHive-only deployment must still get a default)", got, providerToolhive)
	}
}

// TestPreferredDefaultProvider_TierBeatsAlphabetics is the MUTATION-RESISTANT
// pin for the intentDriven tier check: it hand-builds a registry where the
// INTENT-DRIVEN entry's id ("aaa-gw") sorts ALPHABETICALLY BEFORE the
// KEY-DRIVEN entry's id ("zzz-key") — so a naive "first id in sorted order"
// implementation (or the intentDriven check deleted entirely) would wrongly
// return "aaa-gw". Neither real provider id ever exercises this ordering
// (every current key-driven id — openai/openrouter/anthropic — sorts before
// "toolhive"), which is exactly why the other tier tests (openai-keyed,
// anthropic-keyed) pass even with the tier check deleted: this is the ONE
// test that actually falls over under that mutation.
func TestPreferredDefaultProvider_TierBeatsAlphabetics(t *testing.T) {
	reg := &providerRegistry{entries: map[string]providerEntry{
		"aaa-gw":  {id: "aaa-gw", intentDriven: true},
		"zzz-key": {id: "zzz-key", intentDriven: false},
	}}
	if got := preferredDefaultProvider(reg); got != "zzz-key" {
		t.Fatalf("preferredDefaultProvider() = %q, want %q (a key-driven provider must ALWAYS out-tier an intent-driven one, regardless of sort order)", got, "zzz-key")
	}
}

// writeFileT is a tiny os.WriteFile wrapper.
func writeFileT(t *testing.T, path, body string) error {
	t.Helper()
	return os.WriteFile(path, []byte(body), 0o600)
}
