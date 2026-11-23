package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// effortUpstream is a fake LLM endpoint: it serves a model listing and records
// the reasoning effort each generation request carries, for the Responses, Chat
// Completions and Anthropic Messages protocols.
type effortUpstream struct {
	listing string
	mu      sync.Mutex
	order   []string // "list" or "generate:<model>", in arrival order
	efforts []effortCall
}

type effortCall struct{ model, effort string }

func (u *effortUpstream) record(entry string, call *effortCall) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.order = append(u.order, entry)
	if call != nil {
		u.efforts = append(u.efforts, *call)
	}
}

func (u *effortUpstream) calls() []effortCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]effortCall(nil), u.efforts...)
}

func (u *effortUpstream) arrivals() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.order...)
}

func (u *effortUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		u.record("list", nil)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, u.listing)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	model, _ := body["model"].(string)
	call := effortCall{model: model}
	w.Header().Set("Content-Type", "text/event-stream")
	switch {
	case strings.HasSuffix(r.URL.Path, "/responses"):
		if reasoning, ok := body["reasoning"].(map[string]any); ok {
			call.effort, _ = reasoning["effort"].(string)
		}
		_, _ = io.WriteString(w, "event: response.output_text.delta\n"+
			`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`+"\n\n"+
			"event: response.completed\n"+
			`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		call.effort, _ = body["reasoning_effort"].(string)
		_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"ok"}}]}`+"\n\n"+
			`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"finish_reason":"stop","delta":{}}]}`+"\n\n"+"data: [DONE]\n\n")
	case strings.HasSuffix(r.URL.Path, "/messages"):
		if output, ok := body["output_config"].(map[string]any); ok {
			call.effort, _ = output["effort"].(string)
		}
		_, _ = io.WriteString(w, "event: message_start\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"`+model+`","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`+"\n\n"+
			"event: content_block_start\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
			"event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`+"\n\n"+
			"event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n"+
			"event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n"+
			"event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
	default:
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		return
	}
	u.record("generate:"+model, &call)
}

// effortGatewayBuild builds mecatl against one custom provider, "gateway", of
// the given API flavor, served by an effortUpstream with listing.
func effortGatewayBuild(t *testing.T, flavor, defaultModel, listing string, tune func(*Config)) (*Built, *effortUpstream) {
	t.Helper()
	upstream := &effortUpstream{listing: listing}
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	cfg := Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), MemoryDir: t.TempDir(), UserModelDir: t.TempDir(), NoSoul: true,
		DefaultProvider: "gateway", DefaultModel: defaultModel, LLMMaxAttempts: 1,
		ProviderDefinitions: permconfig.ProviderDefinitions{"gateway": {ID: "gateway", BaseURL: srv.URL, DefaultModel: defaultModel,
			APIFlavor: flavor, Auth: permconfig.ProviderAuth{Method: "api_key"}}},
		CustomProviderAPIKeys: map[string]string{"gateway": "fixture-key"}, envDetector: fakeEnv(nil),
		liveModelHTTPClient: srv.Client(), liveModelRefreshSync: true,
	}
	if tune != nil {
		tune(&cfg)
	}
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)
	return built, upstream
}

// effortTurn creates a session on the gateway model at effort, runs one turn,
// and returns the echoed effort and the effort the request carried.
func effortTurn(t *testing.T, built *Built, upstream *effortUpstream, model, effort string) (echo, sent string) {
	t.Helper()
	ctx := context.Background()
	sess, err := built.Service.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "gateway", ModelID: model, ReasoningEffort: effort})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	return effortRun(t, built, upstream, sess.ID)
}

func effortRun(t *testing.T, built *Built, upstream *effortUpstream, id session.SessionID) (echo, sent string) {
	t.Helper()
	before := len(upstream.calls())
	run, err := built.Service.StartRun(context.Background(), id, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	got := drainRun(run)
	built.Service.FinishRun(id, run)
	if got != "ok" {
		t.Fatalf("turn answered %q, want ok", got)
	}
	calls := upstream.calls()
	if len(calls) != before+1 {
		t.Fatalf("generation requests = %d, want one more than %d", len(calls), before)
	}
	return built.Service.ResolvedModel(id).ReasoningEffort, calls[len(calls)-1].effort
}

const publishingGatewayListing = `{"object":"list","data":[` +
	`{"id":"gw-reasoner","reasoning":{"supported_efforts":["low","medium","high","xhigh"]}},` +
	`{"id":"gw-plain"}]}`

// TestEffortGatewayListingReachesRequest is the #2878 regression: a gateway
// model whose listing publishes levels receives the requested level on initial
// create and after effort forks, and a level it doesn't list is dropped, all
// without a /models visit.
func TestEffortGatewayListingReachesRequest(t *testing.T) {
	built, upstream := effortGatewayBuild(t, "openai-responses", "gw-reasoner", publishingGatewayListing, nil)
	ctx := context.Background()
	sess, err := built.Service.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "gateway", ModelID: "gw-reasoner", ReasoningEffort: "xhigh"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if echo, sent := effortRun(t, built, upstream, sess.ID); echo != "xhigh" || sent != "xhigh" {
		t.Fatalf("create: echo=%q sent=%q, want xhigh/xhigh", echo, sent)
	}
	for _, tc := range []struct{ fork, want string }{{"low", "low"}, {"max", ""}} {
		forked, err := built.Service.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: sess.ID, ReasoningEffort: tc.fork})
		if err != nil {
			t.Fatalf("fork at %s: %v", tc.fork, err)
		}
		if echo, sent := effortRun(t, built, upstream, forked); echo != tc.want || sent != tc.want {
			t.Fatalf("fork at %s: echo=%q sent=%q, want %q", tc.fork, echo, sent, tc.want)
		}
	}
}

func TestEffortGatewayOmissionIsNone(t *testing.T) {
	built, upstream := effortGatewayBuild(t, "openai-responses", "gw-reasoner", publishingGatewayListing, nil)
	if echo, sent := effortTurn(t, built, upstream, "gw-plain", "high"); echo != "" || sent != "" {
		t.Fatalf("echo=%q sent=%q, want none for a model a publishing listing omits", echo, sent)
	}
}

func TestEffortGatewayWithoutLevelsFailsOpen(t *testing.T) {
	built, upstream := effortGatewayBuild(t, "openai-responses", "gw-model", `{"object":"list","data":[{"id":"gw-model"}]}`, nil)
	if echo, sent := effortTurn(t, built, upstream, "gw-model", "high"); echo != "high" || sent != "high" {
		t.Fatalf("echo=%q sent=%q, want high: a listing without levels says nothing", echo, sent)
	}
}

func TestEffortConfiguredNone(t *testing.T) {
	built, upstream := effortGatewayBuild(t, "openai-responses", "gw-model", `{"object":"list","data":[{"id":"gw-model"}]}`, func(cfg *Config) {
		cfg.effortLevels = map[string]map[string][]string{"gateway": {"gw-model": {}}}
	})
	if echo, sent := effortTurn(t, built, upstream, "gw-model", "high"); echo != "" || sent != "" {
		t.Fatalf("echo=%q sent=%q, want none: models.effort_levels says the model takes none", echo, sent)
	}
}

// TestEffortConfiguredDefaultIsNotUnsupported: a provider's configured default
// model with no level evidence is unknown, not unable to reason, so the operator
// default reaches it.
func TestEffortConfiguredDefaultIsNotUnsupported(t *testing.T) {
	built, upstream := effortGatewayBuild(t, "openai-responses", "configured-default", `{"object":"list","data":[{"id":"another-model"}]}`, func(cfg *Config) {
		cfg.ReasoningEffort = "high"
		cfg.ContextWindowOverride = 128000
	})
	if echo, sent := effortTurn(t, built, upstream, "configured-default", ""); echo != "high" || sent != "high" {
		t.Fatalf("echo=%q sent=%q, want the operator default high", echo, sent)
	}
}

func TestEffortChatCompletionsNeedsConfiguredLevels(t *testing.T) {
	const listing = `{"object":"list","data":[{"id":"chat-model"}]}`
	built, upstream := effortGatewayBuild(t, "openai-chat-completions", "chat-model", listing, nil)
	if echo, sent := effortTurn(t, built, upstream, "chat-model", "high"); echo != "" || sent != "" {
		t.Fatalf("unconfigured: echo=%q sent=%q, want none on Chat Completions", echo, sent)
	}
	built, upstream = effortGatewayBuild(t, "openai-chat-completions", "chat-model", listing, func(cfg *Config) {
		cfg.effortLevels = map[string]map[string][]string{"gateway": {"chat-model": {"low", "high"}}}
	})
	if echo, sent := effortTurn(t, built, upstream, "chat-model", "high"); echo != "high" || sent != "high" {
		t.Fatalf("configured: echo=%q sent=%q, want high", echo, sent)
	}
}

// TestEffortAnthropicListedAndDropped: on the Messages protocol, max reaches a
// model whose capabilities.effort lists it and is dropped for one that doesn't.
func TestEffortAnthropicListedAndDropped(t *testing.T) {
	const listing = `{"data":[` +
		`{"id":"model-all","type":"model","display_name":"All","created_at":"2026-01-01T00:00:00Z","capabilities":{"effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":{"supported":true},"max":{"supported":true}}}},` +
		`{"id":"model-three","type":"model","display_name":"Three","created_at":"2026-01-01T00:00:00Z","capabilities":{"effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":{"supported":false},"max":{"supported":false}}}}` +
		`],"has_more":false}`
	built, upstream := effortGatewayBuild(t, "anthropic-messages", "model-all", listing, nil)
	if echo, sent := effortTurn(t, built, upstream, "model-all", "max"); echo != "max" || sent != "max" {
		t.Fatalf("model-all: echo=%q sent=%q, want max", echo, sent)
	}
	if echo, sent := effortTurn(t, built, upstream, "model-three", "max"); echo != "" || sent != "" {
		t.Fatalf("model-three: echo=%q sent=%q, want none", echo, sent)
	}
}

// TestEffortNativeGatewayWithoutModelsVisit: a native OIDC gateway lists models
// only on demand. A session's effort is decided by that listing before its first
// request, with no /models visit: the listing publishes levels and omits the
// session's model, so neither the echo nor the request carries effort. The
// context window is pinned, so only the effort-level wait asks for the listing.
func TestEffortNativeGatewayWithoutModelsVisit(t *testing.T) {
	built, upstream := effortNativeBuild(t, "")
	sess, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "native", ModelID: "native-model", ReasoningEffort: "high"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	echo, sent := effortRun(t, built, upstream, sess.ID)
	if echo != "" || sent != "" {
		t.Fatalf("echo=%q sent=%q, want none: the listing omits the model", echo, sent)
	}
	if got := upstream.arrivals(); len(got) != 2 || got[0] != "list" || got[1] != "generate:native-model" {
		t.Fatalf("gateway requests = %v, want the listing before the first request", got)
	}
}

// effortNativeBuild builds a native OIDC gateway, which lists its models only
// on demand, at the operator default effort, and checks Build didn't list them.
func effortNativeBuild(t *testing.T, operatorEffort string) (*Built, *effortUpstream) {
	t.Helper()
	upstream := &effortUpstream{listing: `{"object":"list","data":[` +
		`{"id":"other-model","reasoning":{"supported_efforts":["low","high"]}},{"id":"native-model"}]}`}
	transport := nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		upstream.ServeHTTP(rec, r)
		resp := rec.Result()
		resp.Request = r
		return resp, nil
	})
	cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "token"}, transport)
	cfg.MockProvider = nil
	cfg.Workspace, cfg.StoreDir, cfg.MemoryDir, cfg.UserModelDir = t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg.NoSoul, cfg.DefaultProvider, cfg.LLMMaxAttempts = true, "native", 1
	cfg.ContextWindowOverride = 128000
	cfg.ReasoningEffort = operatorEffort
	cfg.envDetector = fakeEnv(nil)
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)
	if got := upstream.arrivals(); len(got) != 0 {
		t.Fatalf("Build contacted the native gateway: %v", got)
	}
	return built, upstream
}

// TestEffortAutoSessionDoesNotWaitForTheListing: a session that asks for the
// operator default waits for an unanswered listing before its first request
// only when that default is a level. At Auto no request carries effort.
func TestEffortAutoSessionDoesNotWaitForTheListing(t *testing.T) {
	for _, tc := range []struct {
		operatorEffort string
		want           []string
	}{
		{"", []string{"generate:native-model"}},
		{"high", []string{"list", "generate:native-model"}},
	} {
		t.Run("default="+tc.operatorEffort, func(t *testing.T) {
			built, upstream := effortNativeBuild(t, tc.operatorEffort)
			sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if _, sent := effortRun(t, built, upstream, sess.ID); sent != "" {
				t.Fatalf("sent=%q, want none: the listing omits the model", sent)
			}
			if got := upstream.arrivals(); !slices.Equal(got, tc.want) {
				t.Fatalf("gateway requests = %v, want %v", got, tc.want)
			}
		})
	}
}
