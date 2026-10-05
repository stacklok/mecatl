package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// roundTripFunc adapts a func to http.RoundTripper so a test can serve a canned
// /v1/models response WITHOUT any network — the SDK takes our *http.Client via
// option.WithHTTPClient. No test ever contacts api.anthropic.com.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fixtureClient serves the trimmed anthropic /v1/models fixture for any request.
func fixtureClient(t *testing.T) *http.Client {
	t.Helper()
	fixture, err := os.ReadFile("testdata/models.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		// Assert the key rides as x-api-key (a READ-only metadata GET) and is never
		// dropped — but the fixture serves regardless of path.
		if r.Header.Get("x-api-key") == "" {
			t.Errorf("lister request missing x-api-key header")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(fixture))),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}
}

// TestListerMapsModelInfo proves the lister maps the rich ModelInfo into the neutral
// Model: the output ceiling (max_tokens), context window (max_input_tokens), image,
// and the thinking descriptor (adaptive / enabled / none) — via a MOCK transport.
func TestListerMapsModelInfo(t *testing.T) {
	l := NewLister("sk-test-key", "", fixtureClient(t))
	models, err := l.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}

	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
	}

	// Adaptive model: ceiling/context/image from live, thinking adaptive.
	opus := byID["claude-opus-4-8"]
	if opus.OutputLimit != 128000 || opus.ContextLimit != 1_000_000 {
		t.Errorf("opus limits: out=%d ctx=%d, want 128000/1000000", opus.OutputLimit, opus.ContextLimit)
	}
	if !opus.Image {
		t.Errorf("opus should report image input")
	}
	if !opus.Thinking.Adaptive || opus.Thinking.Enabled {
		t.Errorf("opus thinking: adaptive=%v enabled=%v, want adaptive=true enabled=false", opus.Thinking.Adaptive, opus.Thinking.Enabled)
	}

	// Manual model: thinking enabled (not adaptive).
	sonnet := byID["claude-sonnet-4-5"]
	if sonnet.Thinking.Adaptive || !sonnet.Thinking.Enabled {
		t.Errorf("sonnet thinking: adaptive=%v enabled=%v, want adaptive=false enabled=true", sonnet.Thinking.Adaptive, sonnet.Thinking.Enabled)
	}
	if sonnet.OutputLimit != 64000 {
		t.Errorf("sonnet output ceiling = %d, want 64000", sonnet.OutputLimit)
	}

	// None model: no thinking, no image.
	haiku := byID["claude-3-5-haiku-20241022"]
	if haiku.Thinking.Adaptive || haiku.Thinking.Enabled {
		t.Errorf("haiku thinking must be none, got adaptive=%v enabled=%v", haiku.Thinking.Adaptive, haiku.Thinking.Enabled)
	}
	if haiku.Image {
		t.Errorf("haiku must not report image input")
	}
	if haiku.OutputLimit != 8192 {
		t.Errorf("haiku output ceiling = %d, want 8192", haiku.OutputLimit)
	}
}

func TestListerRejectsOversizedResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"` + strings.Repeat("x", 1<<20) + `"}]}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}
	_, err := NewLister("sk-test-key", "", client).ListModels(context.Background())
	if err == nil {
		t.Fatal("ListModels accepted an oversized response")
	}
}

func TestListerPreservesHTTPStatus(t *testing.T) {
	const responseSentinel = "reflected-bearer-do-not-log"
	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusBadGateway} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: statusCode,
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"` + responseSentinel + `"}}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			})}
			_, err := NewLister("sk-test-key", "", client).ListModels(context.Background())
			if err == nil {
				t.Fatal("ListModels succeeded, want HTTP error")
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(err, &statusErr) || statusErr.StatusCode() != statusCode {
				t.Fatalf("ListModels error = %v, want StatusCode() == %d", err, statusCode)
			}
			if strings.Contains(err.Error(), responseSentinel) {
				t.Fatalf("ListModels error exposed response body: %v", err)
			}
		})
	}
}

func TestVisibleReasoning_Scenario1_ExplicitCapabilitiesWin(t *testing.T) {
	for _, tc := range []struct {
		name, capabilities string
		adaptive, manual   bool
	}{
		{name: "unsupported", capabilities: `{"thinking":{"supported":false}}`},
		{name: "unsupported overrides contradictory types", capabilities: `{"thinking":{"supported":false,"types":{"adaptive":{"supported":true},"enabled":{"supported":true}}}}`},
		{name: "adaptive", capabilities: `{"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}}}`, adaptive: true},
		{name: "manual", capabilities: `{"thinking":{"supported":true,"types":{"enabled":{"supported":true}}}}`, manual: true},
	} {
		t.Run("listing/"+tc.name, func(t *testing.T) {
			var info sdk.ModelInfo
			if err := json.Unmarshal([]byte(`{"id":"`+tc.name+`","capabilities":`+tc.capabilities+`}`), &info); err != nil {
				t.Fatalf("unmarshal model info: %v", err)
			}
			got := mapModelInfo(info).Thinking
			if !got.Known || got.Adaptive != tc.adaptive || got.Enabled != tc.manual {
				t.Fatalf("thinking = %+v, want known adaptive=%v manual=%v", got, tc.adaptive, tc.manual)
			}
		})
	}

	resolve := func(model string) (adaptive, enabled, known bool) {
		switch model {
		case "unsupported":
			return false, false, true
		case "adaptive":
			return true, false, true
		case "manual":
			return false, true, true
		default:
			return false, false, false
		}
	}
	for _, tc := range []struct {
		model    string
		adaptive bool
		manual   bool
	}{
		{model: "unsupported"},
		{model: "adaptive", adaptive: true},
		{model: "manual", manual: true},
		{model: "claude-sonnet-4-5", manual: true},
	} {
		t.Run(tc.model, func(t *testing.T) {
			cfg := thinkingConfigFor(tc.model, 64_000, 4096, resolve)
			if (cfg.OfAdaptive != nil) != tc.adaptive || (cfg.OfEnabled != nil) != tc.manual {
				t.Fatalf("thinking config = %+v, want adaptive=%v manual=%v", cfg, tc.adaptive, tc.manual)
			}
			if cfg.OfAdaptive != nil && cfg.OfAdaptive.Display != sdk.ThinkingConfigAdaptiveDisplaySummarized {
				t.Errorf("adaptive display = %q, want summarized", cfg.OfAdaptive.Display)
			}
			if cfg.OfEnabled != nil && cfg.OfEnabled.Display != sdk.ThinkingConfigEnabledDisplaySummarized {
				t.Errorf("manual display = %q, want summarized", cfg.OfEnabled.Display)
			}
		})
	}
}

func TestVisibleReasoning_Scenario1_SparseListingPreservesOtherFields(t *testing.T) {
	const key = "test-listing-key"
	const body = `{"data":[{"id":"claude-opus-4-8","type":"model","display_name":"Claude Opus 4.8","created_at":"2026-01-01T00:00:00Z","max_input_tokens":1000000,"max_tokens":128000}],"has_more":false}`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("X-Api-Key"); got != key {
			t.Errorf("listing credential = %q, want resolved credential", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})}
	models, err := NewLister(key, "", client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1", len(models))
	}
	got := models[0]
	if got.ID != "claude-opus-4-8" || got.OutputLimit != 128000 || got.ContextLimit != 1_000_000 {
		t.Errorf("sparse model = %+v, want preserved id and limits", got)
	}
	if got.Thinking.Known {
		t.Errorf("sparse thinking metadata became known: %+v", got.Thinking)
	}
	replay := unpackReasoning(packReasoning([]reasoningBlock{{Kind: reasoningKindThinking, Thinking: "provider summary", Signature: "signed-replay"}}))
	if len(replay) != 1 || replay[0].Signature != "signed-replay" || replay[0].Thinking != "provider summary" {
		t.Fatalf("signed replay changed while preserving sparse metadata: %+v", replay)
	}
}

// TestThinkingResolverLiveThenPrefixFloor proves a live descriptor drives the
// mode authoritatively while an unknown model uses the embedded prefix floor.
func TestThinkingResolverLiveThenPrefixFloor(t *testing.T) {
	// A resolver that reports a live "manual enabled" for a model whose PREFIX would
	// otherwise be adaptive — proving the live descriptor wins when known.
	resolve := func(model string) (adaptive, enabled, known bool) {
		switch model {
		case "claude-opus-4-8": // prefix matrix says adaptive; live says manual
			return false, true, true
		case "claude-live-none": // a model with NO thinking, live-known
			return false, false, true
		default: // unknown to the live source ⇒ fall back to the prefix matrix
			return false, false, false
		}
	}

	// LIVE wins: opus-4-8 (prefix=adaptive) gets MANUAL from the live descriptor.
	cfg := thinkingConfigFor("claude-opus-4-8", 64000, 4096, resolve)
	if cfg.OfEnabled == nil || cfg.OfAdaptive != nil {
		t.Errorf("live manual must override prefix-adaptive, got %+v", cfg)
	}

	// LIVE none: a live thinking=none model gets NO thinking field.
	cfg = thinkingConfigFor("claude-live-none", 64000, 4096, resolve)
	if cfg != (sdk.ThinkingConfigParamUnion{}) {
		t.Errorf("live none must omit the thinking field, got %+v", cfg)
	}

	// UNKNOWN-to-live: fall back to the prefix matrix. claude-opus-4-8 here is moot;
	// use a real prefix-adaptive id the resolver returns known=false for by routing
	// through a resolver that always misses.
	miss := func(string) (bool, bool, bool) { return false, false, false }
	cfg = thinkingConfigFor("claude-opus-4-8", 64000, 4096, miss)
	if cfg.OfAdaptive == nil {
		t.Errorf("unknown-to-live must fall back to prefix matrix (adaptive), got %+v", cfg)
	}
	// A 3.5 model with a missing resolver stays NONE via the prefix floor.
	cfg = thinkingConfigFor("claude-3-5-haiku-20241022", 64000, 4096, miss)
	if (cfg != sdk.ThinkingConfigParamUnion{}) {
		t.Errorf("3.5 incapable via prefix floor must omit thinking, got %+v", cfg)
	}
}
