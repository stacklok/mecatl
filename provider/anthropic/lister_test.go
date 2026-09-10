package anthropic

import (
	"context"
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

// descriptor drives the mode authoritatively; an UNKNOWN model (known=false) falls
// back to the embedded prefix matrix (the offline floor).
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
