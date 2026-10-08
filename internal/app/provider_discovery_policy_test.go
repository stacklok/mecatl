package app

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestProviderModelDiscovery_Scenario2_ResolutionMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cfg        Config
		pid, model string
		state      discoveryProvider
		tokens     int
		source     windowSource
	}{
		{"global", Config{ContextWindowOverride: 111111, contextWindows: map[string]map[string]int{"a": {"m": 222222}}}, "a", "m", discoveryProvider{eligible: true, observations: []modelEntry{{ID: "m", ContextLimit: 333333}}}, 111111, windowGlobal},
		{"exact config", Config{contextWindows: map[string]map[string]int{"a": {"m": 222222}}}, "a", "m", discoveryProvider{eligible: true, observations: []modelEntry{{ID: "m", ContextLimit: 333333}}}, 222222, windowConfig},
		{"live beats catalog", Config{}, providerAnthropic, catAnthropicModel, discoveryProvider{eligible: true, observations: []modelEntry{{ID: catAnthropicModel, ContextLimit: 555555}}}, 555555, windowLive},
		{"other provider config", Config{contextWindows: map[string]map[string]int{"b": {"m": 222222}}}, "a", "m", discoveryProvider{eligible: true}, 0, windowUnknown},
		{"no suffix match", Config{contextWindows: map[string]map[string]int{"a": {"m": 222222}}}, "a", "vendor/m", discoveryProvider{eligible: true}, 0, windowUnknown},
		{"live after failure", Config{}, "a", "m", discoveryProvider{eligible: true, observations: []modelEntry{{ID: "m", ContextLimit: 333333}}, outcome: providerStatus{State: statusUnauthorized}}, 333333, windowLive},
		{"catalog before failure", Config{}, providerAnthropic, catAnthropicModel, discoveryProvider{eligible: true, outcome: providerStatus{State: statusUnreachable}}, catAnthropicCtx, windowCatalog},
		{"catalog namespace", Config{}, providerOpenAICodex, "gpt-5", discoveryProvider{eligible: true}, catalogContextWindow(providerOpenAI, "gpt-5"), windowCatalog},
		{"no lister", Config{}, "a", "m", discoveryProvider{}, 128000, windowFallback},
		{"healthy omission", Config{}, "a", "m", discoveryProvider{eligible: true, outcome: providerStatus{State: statusOK}}, 128000, windowFallback},
		{"refresh preserves omission", Config{}, "a", "m", discoveryProvider{eligible: true, inFlight: true, outcome: providerStatus{State: statusOK}}, 128000, windowFallback},
		{"unattempted", Config{}, "a", "m", discoveryProvider{eligible: true}, 0, windowUnknown},
		{"empty", Config{}, "a", "m", discoveryProvider{eligible: true, outcome: providerStatus{State: statusEmpty}}, 0, windowUnknown},
		{"failed", Config{}, "a", "m", discoveryProvider{eligible: true, outcome: providerStatus{State: statusUnreachable}}, 0, windowUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := &discoverySnapshot{providers: map[string]discoveryProvider{tc.pid: tc.state}}
			got := resolveModelWindow(tc.cfg, view, tc.pid, tc.model)
			if got.tokens != tc.tokens || got.source != tc.source || got.admissible != (tc.tokens > 0) {
				t.Fatalf("resolution=%+v, want %d/%s", got, tc.tokens, tc.source)
			}
			if got.admissible {
				lister := &fakeLister{err: errors.New("must not fetch")}
				d := discoveryFixture(t, map[string]providerEntry{tc.pid: {lister: lister}})
				d.view.Store(view) // immutable evidence fixture; no active worker
				d.reg.contextWindowOverride, d.reg.contextWindows = tc.cfg.ContextWindowOverride, tc.cfg.contextWindows
				if err := awaitContextWindow(context.Background(), d.reg, tc.pid, tc.model); err != nil {
					t.Fatal(err)
				}
				if lister.calls.Load() != 0 || d.snapshot() != view {
					t.Fatal("admissible resolution performed discovery")
				}
			}
		})
	}
}

func TestProviderModelDiscovery_Scenario4_BoundedRetryPolicy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		d := discoveryFixture(t, map[string]providerEntry{"a": {lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) {
			calls.Add(1)
			return nil, errors.New("offline")
		})}})
		_, _ = d.request(context.Background(), "a", discoveryPicker)
		synctest.Wait()
		completed := d.snapshot().providers["a"].completedAt
		for range 3 {
			_, _ = d.request(context.Background(), "a", discoveryAdmission)
		}
		if calls.Load() != 1 || !time.Now().Equal(completed) {
			t.Fatal("cooldown demand fetched or slept")
		}
		time.Sleep(10 * time.Second)
		if calls.Load() != 1 {
			t.Fatal("elapsed time performed discovery")
		}
		_, _ = d.request(context.Background(), "a", discoveryPicker)
		if calls.Load() != 2 {
			t.Fatal("eligible demand did not retry")
		}
	})
}

func TestProviderModelDiscovery_Scenario2_ShutdownAndSkippedPublications(t *testing.T) {
	t.Run("startup skips completed native publication", func(t *testing.T) {
		native := &fakeLister{models: []modelEntry{{ID: "m", ContextLimit: 222222, InputModalities: []string{"text", "image"}}}}
		other := &fakeLister{models: []modelEntry{{ID: "m", ContextLimit: 333333}}}
		d := discoveryFixture(t, map[string]providerEntry{"native": {nativeEndpoint: true, lister: native}, "other": {lister: other}})
		_, _ = d.request(context.Background(), "native", discoveryPicker)
		before := d.snapshot().providers["native"]
		d.start(true, 0)
		if native.calls.Load() != 1 || other.calls.Load() != 1 || !reflect.DeepEqual(before, d.snapshot().providers["native"]) {
			t.Fatal("startup re-fetched or replaced the skipped native provider")
		}
		if resolveModelWindow(Config{}, d.snapshot(), "native", "m").tokens != 222222 || resolveModelWindow(Config{}, d.snapshot(), "other", "m").tokens != 333333 {
			t.Fatal("identical model IDs crossed provider boundaries")
		}
	})
	synctest.Test(t, func(t *testing.T) {
		var native atomic.Int32
		d := discoveryFixture(t, map[string]providerEntry{
			"a": {lister: discoveryListerFunc(func(ctx context.Context) ([]modelEntry, error) {
				<-ctx.Done()
				return []modelEntry{{ID: "too-late", ContextLimit: 999999}}, nil
			})},
			"native": {nativeEndpoint: true, lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) {
				native.Add(1)
				return []modelEntry{{ID: "native", ContextLimit: 222222}}, nil
			})},
		})
		_, _ = d.request(context.Background(), "native", discoveryAdmission)
		synctest.Wait()
		nativeState := d.snapshot().providers["native"]
		done := make(chan error, 1)
		go func() { _, err := d.request(context.Background(), "a", discoveryAdmission); done <- err }()
		synctest.Wait()
		before := d.snapshot()
		d.Close()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown wait=%v", err)
		}
		if d.snapshot() != before {
			t.Fatal("shutdown published cancelled work")
		}
		if !reflect.DeepEqual(nativeState, d.snapshot().providers["native"]) || native.Load() != 1 {
			t.Fatal("unrelated provider changed")
		}
		if _, err := d.request(context.Background(), "native", discoveryPicker); !errors.Is(err, context.Canceled) {
			t.Fatal("closed owner accepted demand")
		}
	})
}

func TestProviderModelDiscovery_Scenario1_PureReads(t *testing.T) {
	var calls atomic.Int32
	d := discoveryFixture(t, map[string]providerEntry{"native": {nativeEndpoint: true, lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) { calls.Add(1); return nil, nil })}})
	before := d.snapshot()
	discovery := newAgentModelDiscoveryTool(d)
	if !modelDiscoveryAvailable(d.reg, d) {
		t.Fatal("native demand-only lister unavailable to discovery")
	}
	_ = discovery.Spec()
	result, err := discovery.Execute(context.Background(), session.ToolCall{ID: "read", Args: []byte(`{}`)}, tool.Environment{})
	if err != nil || result.IsError {
		t.Fatalf("DiscoverModels: %v, %s", err, result.Content)
	}
	_ = modelCapability(d.reg, "native", "m")
	_ = d.reg.echoWindowResolver(Config{}, "native", "m")()
	_ = d.reg.windowResolver(Config{}, "native", "m")()
	_ = d.CurrentModelSnapshot()
	if calls.Load() != 0 || before != d.snapshot() {
		t.Fatal("pure read mutated discovery state or called lister")
	}
}

func TestProviderModelDiscovery_Scenario2_CandidateDefaultHealing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan port.ProviderCapabilities, 1), make(chan struct{})
		provider := mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})})
		reg := &providerRegistry{defaultID: providerToolhive, entries: map[string]providerEntry{providerToolhive: {
			id: providerToolhive, available: true, intentDriven: true, provider: provider,
			lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) {
				return []modelEntry{{ID: "chosen", ContextLimit: 222222, InputModalities: []string{"text"}}}, nil
			}),
			remint: func(_ string, caps port.ProviderCapabilities) port.LLMProvider {
				entered <- caps
				<-release
				return provider
			},
		}}}
		d := newProviderDiscovery(reg, Config{})
		defer d.Close()
		defer closeIfOpen(release)
		done := make(chan struct{})
		go func() { _, _ = d.request(context.Background(), providerToolhive, discoveryBootstrap); close(done) }()
		caps := <-entered
		if caps.Image {
			t.Fatal("remint used previous metadata instead of text-only candidate")
		}
		if resolveModelWindow(Config{}, d.snapshot(), providerToolhive, "chosen").admissible {
			t.Fatal("candidate became visible before matching publication")
		}
		// Wait for the actual timer, not another virtual sleep while its callback
		// contends on tail (Mutex waits are not durable synctest blocking).
		d.mu.Lock()
		deadlineDone := d.attempts[providerToolhive].ctx.Done()
		d.mu.Unlock()
		<-deadlineDone
		close(release)
		<-done
		synctest.Wait()
		if d.snapshot().providers[providerToolhive].outcome.State != statusOK {
			t.Fatal("timer replaced an already accepted candidate")
		}
		view := d.CurrentModelSnapshot()
		if len(view.Models) != 1 || view.Models[0].ContextLimit != 222222 || len(view.ProviderStatus) != 1 || !view.ProviderStatus[0].DefaultModelAutoSelected {
			t.Fatalf("incoherent healed publication: %+v", view)
		}
		if d.snapshot().defaults.model != "chosen" {
			t.Fatal("publication omitted healed default")
		}
	})
}
