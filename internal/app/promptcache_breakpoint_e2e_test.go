package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// breakpointCapture records each captured Responses request body plus whether
// its LAST input_text block carried a prompt_cache_breakpoint.
type breakpointCapture struct {
	mu     sync.Mutex
	marked []bool
	bodies []string
}

// lastInputTextMarked walks the Responses request body the way the protocol
// shapes it and reports whether the final input_text block of the final message
// carries the explicit breakpoint.
//
// It reads the JSON rather than calling the adapter's own helper on purpose:
// this test exists to prove the marker survives all the way onto the WIRE, so
// re-using the producer as the oracle would make it circular.
func lastInputTextMarked(t *testing.T, raw string) bool {
	t.Helper()
	// input[] is heterogeneous: a message's content is EITHER a plain string or a
	// block list, and function_call / function_call_output items are neither. So
	// this walks it dynamically rather than through a fixed struct, which is also
	// what keeps it robust to the adapter emitting item kinds this test does not
	// care about.
	var body struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("unmarshal request body: %v (body=%s)", err, raw)
	}
	for i := len(body.Input) - 1; i >= 0; i-- {
		blocks, ok := body.Input[i]["content"].([]any)
		if !ok {
			continue // a string-content message or a non-message item
		}
		for j := len(blocks) - 1; j >= 0; j-- {
			blk, ok := blocks[j].(map[string]any)
			if !ok || blk["type"] != "input_text" {
				continue
			}
			bp, ok := blk["prompt_cache_breakpoint"].(map[string]any)
			return ok && bp["mode"] == "explicit"
		}
	}
	return false
}

// TestADR_0346_BreakpointCacheReadE2E is AC1.7: the whole point of ADR 0346,
// driven end to end offline through the REAL openai adapter.
//
// The endpoint is an operator-overridden base URL, which resolves to
// CacheDialectNone via cacheDialectFor. That is the EXACT deployment shape the
// reported incident came from: before ADR 0346 the dialect gate meant such an
// endpoint got no cache ask at all, so a Claude model there re-paid full input
// every single turn. providerConstructor stays nil so nothing is mocked between
// the loop and the HTTP body.
//
// Two turns on one session, because breakpointIndex deliberately marks nothing
// until a prior assistant turn exists (a cache write nobody reads costs MORE
// than sending uncached):
//
//  1. turn 1's body carries NO prompt_cache_breakpoint;
//  2. turn 2's body carries it on the last input_text block;
//  3. turn 2's usage surfaces as non-zero CacheReadTokens on EvResult.
//
// Assertion 3 is what makes this an e2e rather than a marshalling test: it walks
// the cached_tokens the upstream reports back through the adapter's usage
// mapping and out to the client-visible event, so a regression anywhere along
// that path fails here.
func TestADR_0346_BreakpointCacheReadE2E(t *testing.T) {
	ctx := context.Background()

	capture := &breakpointCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		marked := lastInputTextMarked(t, string(raw))
		capture.mu.Lock()
		capture.marked = append(capture.marked, marked)
		capture.bodies = append(capture.bodies, string(raw))
		turn := len(capture.marked)
		capture.mu.Unlock()

		// Report a cache READ only on the marked turn, so a non-zero
		// CacheReadTokens cannot come from anywhere but the cached turn.
		usage := `"usage":{"input_tokens":100,"output_tokens":1}`
		if turn > 1 {
			usage = `"usage":{"input_tokens":100,"output_tokens":1,"input_tokens_details":{"cached_tokens":80}}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.output_text.delta\n"+
			`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`+"\n\n"+
			"event: response.completed\n"+
			`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed",`+usage+`}}`+"\n\n")
	}))
	defer srv.Close()

	built, err := buildIsolated(t, ctx, Config{
		Workspace:             t.TempDir(),
		NoSoul:                true,
		ContextWindowOverride: defaultContextWindowTokens,
		ProviderOverrides: permconfig.ProviderOverrides{
			providerOpenRouter: {BaseURL: srv.URL + "/v1"},
		},
		envDetector:         fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk-test"}),
		liveModelHTTPClient: offlineHTTPClient(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	// The overridden base URL must genuinely be dialect-less, or the test proves
	// nothing about the case ADR 0346 fixed.
	reg, err := buildProviderRegistry(Config{
		OpenRouterKey:     "sk-test",
		ProviderOverrides: permconfig.ProviderOverrides{providerOpenRouter: {BaseURL: srv.URL + "/v1"}},
	}, noEnv)
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	orEntry, _ := reg.Lookup(providerOpenRouter)
	if got := cacheDialectFor(providerOpenRouter, orEntry.baseURL, Config{}); got != "" {
		t.Fatalf("test endpoint resolved dialect %q, want CacheDialectNone — the fixture is not the incident shape", got)
	}

	sess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenRouter, ModelID: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}

	cacheReads := make([]int, 0, 2)
	for turn, userPrompt := range []string{"first", "second"} {
		run, err := svc.StartRun(ctx, sess.ID, userPrompt)
		if err != nil {
			t.Fatalf("StartRun(turn %d): %v", turn+1, err)
		}
		var read int
		for ev := range run.Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
			}
			if ev.Type == session.EvResult && ev.Result != nil {
				read = ev.Result.Usage.CacheReadTokens
			}
		}
		cacheReads = append(cacheReads, read)
	}

	capture.mu.Lock()
	marked := append([]bool(nil), capture.marked...)
	bodies := append([]string(nil), capture.bodies...)
	capture.mu.Unlock()

	if len(marked) < 2 {
		t.Fatalf("captured %d requests, want at least 2 (one per turn)", len(marked))
	}
	if marked[0] {
		t.Errorf("turn 1 carried a prompt_cache_breakpoint; it must mark nothing before an assistant turn exists.\nbody=%s", bodies[0])
	}
	if !marked[1] {
		t.Errorf("turn 2 carried NO prompt_cache_breakpoint on a dialect-less endpoint — this is the ADR 0346 bug.\nbody=%s", bodies[1])
	}
	if cacheReads[1] == 0 {
		t.Errorf("turn 2 EvResult.Usage.CacheReadTokens = 0, want the upstream's 80 cached_tokens (reads = %v)", cacheReads)
	}
}

// TestADR_0346_AnthropicCacheTTLStampedOnOpenRouterAnthropic is AC2.3: the
// --anthropic-cache-ttl an operator sets must reach the openrouter-anthropic
// entry's wire, on EVERY cache_control marker the adapter emits.
//
// The TTL is the concrete reason ADR 0346 registers a second, Messages-speaking
// provider for one OpenRouter credential at all: the Responses protocol cannot
// express a cache lifetime, so a Claude session routed over Responses gets the
// API's default 5m whether the operator asked for an hour or not. If the flag
// did not land here, the new provider would be carrying only half its
// justification.
//
// It drives the REAL anthropic adapter (providerConstructor nil) against an
// Anthropic-Messages SSE handler, and asserts BOTH directions: the flag set
// stamps "ttl":"1h" on every marker, and the flag UNSET emits no ttl key at all
// rather than a hardcoded default.
func TestADR_0346_AnthropicCacheTTLStampedOnOpenRouterAnthropic(t *testing.T) {
	for _, tc := range []struct {
		name, ttl string
		wantTTL   string // "" => no ttl key anywhere in the body
	}{
		{"operator set 1h", "1h", "1h"},
		{"operator set 5m", "5m", "5m"},
		{"flag unset leaves the API default", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var messagesBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				mu.Lock()
				messagesBody = string(raw)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, toolhiveCompletedMessageSSE)
			}))
			defer srv.Close()

			// The OpenRouter OpenAI base is what openrouter-anthropic derives from, so
			// pointing THAT at the test server is what routes the Messages entry here —
			// the same one-credential-two-surfaces wiring the feature ships.
			cfg := orCfg()
			cfg.LLMMaxAttempts = 1
			cfg.AnthropicCacheTTL = tc.ttl
			cfg.ProviderOverrides = providerOverridesFor(providerOpenRouter, srv.URL+"/v1")
			reg, err := buildProviderRegistry(cfg, noEnv)
			if err != nil {
				t.Fatalf("buildProviderRegistry: %v", err)
			}
			entry, ok := reg.Lookup(providerOpenRouterAnthropic)
			if !ok {
				t.Fatal("openrouter-anthropic entry missing")
			}
			if entry.baseURL != srv.URL {
				t.Fatalf("openrouter-anthropic baseURL = %q, want %q (the derived Anthropic base)", entry.baseURL, srv.URL)
			}
			// A MULTI-TURN request with a layered system prompt, not driveStream's
			// single bare message: that shape fires only the top-level marker, and a
			// TTL test that sees one marker cannot tell "uniform" from "stamped once".
			// This shape fires three of the four ADR 0100 slots (see the floor
			// assertion below), so the uniformity check has something to check.
			if err := driveCacheRichStream(entry.provider); err != nil {
				t.Fatalf("drive stream: %v", err)
			}

			mu.Lock()
			body := messagesBody
			mu.Unlock()
			if body == "" {
				t.Fatal("no Messages request captured")
			}

			// The floor is load-bearing for the uniformity assertion below: with ONE
			// marker it cannot distinguish "stamped uniformly" from "stamped once and
			// missed the rest", which is the drift worth catching. Three is what a
			// realistic conversation produces, and they land in three DIFFERENT parts
			// of the body — system[], messages[], and the request root — so a TTL
			// plumbed into only one of them fails here.
			//
			// Not four: the second conversation anchor (leadingFragmentEnd) resolves
			// only when the history opens with an injected turn-0 fragment, and
			// ADR 0043 made those ephemeral, so a normal conversation never has one.
			markers := countCacheControlMarkers(t, body)
			if markers < 3 {
				t.Fatalf("%d cache_control marker(s) on the wire, want >= 3 "+
					"(StablePrefix + the previous-turn anchor + top-level).\nbody=%s", markers, body)
			}
			withTTL := countCacheControlTTL(t, body, tc.wantTTL)
			if tc.wantTTL == "" {
				if withTTL != 0 {
					t.Errorf("%d marker(s) carried a ttl with the flag unset; the API default must not be hardcoded.\nbody=%s", withTTL, body)
				}
				return
			}
			if withTTL != markers {
				t.Errorf("%d of %d cache_control markers carried ttl=%q; the TTL must be uniform across every marker.\nbody=%s",
					withTTL, markers, tc.wantTTL, body)
			}
		})
	}
}

// countCacheControlMarkers counts every cache_control object in an Anthropic
// Messages request body, and countCacheControlTTL counts how many of those
// carry the given ttl. They walk the decoded JSON rather than substring-matching
// the raw bytes, so a ttl appearing anywhere ELSE in the body cannot be
// miscounted as a marker's.
func countCacheControlMarkers(t *testing.T, raw string) int {
	t.Helper()
	total := 0
	walkCacheControl(t, raw, func(map[string]any) { total++ })
	return total
}

func countCacheControlTTL(t *testing.T, raw, ttl string) int {
	t.Helper()
	n := 0
	walkCacheControl(t, raw, func(cc map[string]any) {
		if got, ok := cc["ttl"].(string); ok && got == ttl {
			n++
		}
	})
	return n
}

func walkCacheControl(t *testing.T, raw string, visit func(map[string]any)) {
	t.Helper()
	var body any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("unmarshal Messages body: %v (body=%s)", err, raw)
	}
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			for key, child := range v {
				if key == "cache_control" {
					if cc, ok := child.(map[string]any); ok {
						visit(cc)
						continue
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(body)
}

// driveCacheRichStream issues one Stream call carrying a layered system prompt
// and a multi-turn conversation (user -> assistant -> tool -> user), which is
// what makes the anthropic adapter spread its cache_control markers across
// system[], messages[] and the request root rather than emitting only the
// top-level automatic one.
func driveCacheRichStream(p port.LLMProvider) error {
	seq, err := p.Stream(context.Background(), port.LLMRequest{
		Model: "claude-sonnet-4-6",
		System: prompt.Layered{
			StablePrefix:   "You are a coding agent.",
			VolatileSuffix: "Current directory: /tmp",
		},
		Messages: []session.Message{
			session.NewUserMessage("summarise the architecture"),
			session.NewAssistantMessage("Reading now.", "", []session.ToolCall{
				{ID: "call_1", Name: "Read", Args: json.RawMessage(`{"file_path":"/a"}`)},
			}),
			session.NewToolMessage(session.NewToolResult("call_1", "package a")),
			session.NewUserMessage("now the tests"),
		},
	})
	if err != nil {
		return err
	}
	for _, e := range seq {
		if e != nil {
			return e
		}
	}
	return nil
}
