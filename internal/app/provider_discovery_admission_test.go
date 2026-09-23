package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type discoveryEventCapture struct {
	mu     sync.Mutex
	events []session.Event
}

func (c *discoveryEventCapture) Emit(_ context.Context, ev session.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *discoveryEventCapture) snapshot() []session.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]session.Event(nil), c.events...)
}

func nativeAdmissionConfig(t *testing.T, root string, listings *atomic.Int32, capture *coldGatewayCapture) Config {
	t.Helper()
	cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "offline-fixture"}, nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/base/v1/models" {
			return nil, fmt.Errorf("unexpected offline request path %q", r.URL.Path)
		}
		listings.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"native-model","context_window":1050000},{"id":"new-default","context_window":1050000}]}`))}, nil
	}))
	cfg.MockProvider = nil
	cfg.DefaultProvider, cfg.DefaultModel = "native", "native-model"
	cfg.Workspace, cfg.StoreDir = root+"/workspace", root+"/store"
	cfg.MemoryDir, cfg.UserModelDir = root+"/memory", root+"/usermodel"
	cfg.NoSoul, cfg.NoShell, cfg.NoUserModel = true, true, true
	cfg.LearningMode = learning.Off
	cfg.permConfigEnv = isolatedPermConfigEnv(t)
	cfg.envDetector = fakeEnv(nil)
	cfg.liveModelRefreshSync = true
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		turns := make([]mockllm.Turn, 16)
		for i := range turns {
			turns[i] = mockllm.TextTurn("offline answer")
		}
		return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(capture.observe)}, turns...)
	}
	return cfg
}

func TestProviderModelDiscovery_Scenario3_ColdResumeFirstPrompt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	var listings atomic.Int32
	warmCfg := nativeAdmissionConfig(t, root, &listings, newColdGatewayCapture())
	// Prime valid history independently of the discovery gate being tested below.
	warmCfg.ContextWindowOverride = coldGatewayWindow
	warm, err := buildIsolated(t, ctx, warmCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	sel := server.ProviderSelector{ProviderID: "native", ModelID: "native-model"}
	sess, err := warm.Service.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{MaxTurns: 3}, sel)
	if err != nil {
		t.Fatal(err)
	}
	prompts := []string{"genuine first instruction", strings.Repeat("0123456789abcdef", 37_500)}
	for i := range 9 {
		prompts = append(prompts, fmt.Sprintf("genuine follow-up %d", i))
	}
	for _, text := range prompts {
		runColdGatewayPrompt(t, warm, sess.ID, text)
	}
	before, err := warm.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	history := session.CloneMessages(before.Conversation.Messages)
	tokens := agent.HeuristicTokenCounter{}.CountMessages(history)
	if len(history) != 22 || tokens <= 128000 || tokens >= coldGatewayWindow/2 {
		t.Fatalf("fixture does not exercise premature compaction: messages=%d tokens=%d", len(history), tokens)
	}
	warm.Close()
	listings.Store(0)
	capture := newColdGatewayCapture()
	events := &discoveryEventCapture{}
	cfg := nativeAdmissionConfig(t, root, &listings, capture)
	cfg.Sink = events
	cold, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	if listings.Load() != 0 {
		t.Fatal("cold Build discovered demand-only native provider")
	}
	client, closeClient := learningHarnessClient(t, cold.Service, nil)
	defer closeClient()
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const prompt = "first cold prompt, no picker"
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: prompt}}}); err != nil {
		t.Fatal(err)
	}
	inits := 0
	for {
		response, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("first prompt rejected: %v", err)
		}
		if response.GetEvent().GetType() == string(session.EvSessionInit) {
			inits++
		}
	}
	compactions, archives, window, model := coldGatewayEventFacts(events.snapshot())
	if inits != 1 || listings.Load() != 1 || capture.count() != 1 || window != coldGatewayWindow || model != sel.ModelID || compactions != 0 || archives != 0 {
		t.Fatalf("cold run: init=%d listings=%d inference=%d window=%d model=%q compactions=%d archives=%d", inits, listings.Load(), capture.count(), window, model, compactions, archives)
	}
	if req := capture.last(t); req.historyTokens < tokens || req.model != sel.ModelID {
		t.Fatalf("first inference lost history: %+v", req)
	}
	after, err := cold.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Conversation.Messages) != len(history)+2 || !reflect.DeepEqual(after.Conversation.Messages[:len(history)], history) {
		t.Fatal("cold run rewrote genuine transcript")
	}
	count := 0
	for _, m := range after.Conversation.Messages {
		if m.Role == session.RoleUser && m.Text == prompt {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("recorded cold prompt %d times", count)
	}
	if got := cold.Service.ResolvedModel(sess.ID); got.ProviderID != sel.ProviderID || got.ModelID != sel.ModelID || got.ContextWindow != int64(window) {
		t.Fatalf("engine/echo mismatch: %+v", got)
	}
}

func TestProviderModelDiscovery_Scenario3_IdentityAndContextProjection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	var listings atomic.Int32
	first, err := buildIsolated(t, ctx, nativeAdmissionConfig(t, root, &listings, newColdGatewayCapture()))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	var ids []session.SessionID
	selectors := []server.ProviderSelector{{ProviderID: "native", ModelID: "native-model"}, {}}
	for _, sel := range selectors {
		sess, err := first.Service.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{}, sel)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
		runColdGatewayPrompt(t, first, sess.ID, "before restart")
	}
	first.Close()
	listings.Store(0)
	cfg := nativeAdmissionConfig(t, root, &listings, newColdGatewayCapture())
	cfg.DefaultModel = "new-default"
	definition := cfg.ProviderDefinitions["native"]
	definition.DefaultModel = "new-default"
	cfg.ProviderDefinitions["native"] = definition
	events := &discoveryEventCapture{}
	cfg.Sink = events
	second, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	client, closeClient := learningHarnessClient(t, second.Service, nil)
	defer closeClient()
	for i, id := range ids {
		got, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: string(id)})
		if err != nil {
			t.Fatal(err)
		}
		wantModel := "native-model"
		if i == 1 {
			wantModel = "new-default"
		}
		if rm := got.GetSession().GetResolvedModel(); rm.GetProviderId() != "native" || rm.GetModelId() != wantModel || rm.GetContextWindow() != 0 {
			t.Fatalf("cold identity/evidence = %+v, want native/%s unknown", rm, wantModel)
		}
	}
	if listings.Load() != 0 {
		t.Fatal("snapshot reads performed discovery")
	}
	for i, id := range ids {
		runEvents := runColdGatewayPrompt(t, second, id, "after restart")
		_, _, window, model := coldGatewayEventFacts(runEvents)
		wantModel := "native-model"
		if i == 1 {
			wantModel = "new-default"
		}
		if window != coldGatewayWindow || model != wantModel || second.Service.ResolvedModel(id).ContextWindow != int64(window) {
			t.Fatalf("admitted context/model = %d/%s", window, model)
		}
		saved, err := second.Service.GetSession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if saved.ProviderID != selectors[i].ProviderID || saved.ModelID != selectors[i].ModelID {
			t.Fatalf("discovery rewrote durable selector: %q/%q", saved.ProviderID, saved.ModelID)
		}
	}
	if listings.Load() != 1 {
		t.Fatalf("restart listing calls=%d, want fresh discovery once", listings.Load())
	}
}

func TestProviderModelDiscovery_Scenario3_AuthoritativeUnknownEcho(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lister := &fakeLister{models: []modelEntry{{ID: "other-model"}}}
		d := discoveryFixture(t, map[string]providerEntry{"native": {nativeEndpoint: true, lister: lister}})
		if _, err := d.request(context.Background(), "native", discoveryAdmission); err != nil {
			t.Fatal(err)
		}
		seed := d.reg.echoWindowResolver(Config{}, "native", "uncatalogued")()
		if seed != 128000 {
			t.Fatalf("successful omission seed = %d", seed)
		}
		store := memstore.New()
		var inference atomic.Int32
		svc, err := newTestServerService(server.Config{
			Engine:               agent.NewEngine(agent.Deps{LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { inference.Add(1) })}, mockllm.TextTurn("wrong")), Catalog: tool.NewCatalog(), Store: store}),
			Store:                store,
			DefaultResolvedModel: server.ResolvedModel{ProviderID: "native", ModelID: "uncatalogued", ContextWindow: int64(seed)},
			ResolveContextWindow: func(p, m string) int64 { return int64(d.reg.echoWindowResolver(Config{}, p, m)()) },
			AwaitContextWindow:   func(ctx context.Context, p, m string) error { return awaitContextWindow(ctx, d.reg, p, m) },
		})
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if svc.ResolvedModel(sess.ID).ContextWindow != int64(seed) {
			t.Fatal("initial omission fallback was not echoed")
		}
		synctest.Wait()
		time.Sleep(discoveryCooldown)
		lister.err = errors.New("offline latest failure")
		if _, err := d.request(context.Background(), "native", discoveryPicker); err != nil {
			t.Fatal(err)
		}
		if got := svc.ResolvedModel(sess.ID).ContextWindow; got != 0 {
			t.Errorf("latest failure echo = %d, want authoritative zero over seed %d", got, seed)
		}
		run, err := svc.StartRun(context.Background(), sess.ID, "must not record")
		if run != nil {
			for range run.Events() {
			}
			svc.FinishRun(sess.ID, run)
		}
		if !errors.Is(err, server.ErrContextWindowUnavailable) {
			t.Errorf("failed admission = %v", err)
		}
		saved, err := svc.GetSession(context.Background(), sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if inference.Load() != 0 || len(saved.Conversation.Messages) != 0 {
			t.Fatal("failed discovery admitted side effects")
		}
	})
}

func TestProviderModelDiscovery_Scenario3_RecoveryWithoutPicker(t *testing.T) {
	for _, provider := range []string{"native", providerOpenAI} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty=%t", provider, empty), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var calls atomic.Int32
					d := discoveryFixture(t, map[string]providerEntry{
						provider: {nativeEndpoint: provider == "native", lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) {
							if calls.Add(1) == 1 {
								if empty {
									return nil, nil
								}
								return nil, errors.New("offline failure")
							}
							return []modelEntry{{ID: "uncatalogued", ContextLimit: coldGatewayWindow}}, nil
						})},
						"other": {lister: &fakeLister{models: []modelEntry{{ID: "other", ContextLimit: 999999}}}},
					})
					if _, err := d.request(context.Background(), "other", discoveryStartup); err != nil {
						t.Fatal(err)
					}
					if got := resolveModelWindow(Config{}, d.snapshot(), provider, "uncatalogued"); got.admissible || got.tokens != 0 || calls.Load() != 0 {
						t.Fatalf("other startup admitted unattempted target: %+v", got)
					}
					store := memstore.New()
					var inference atomic.Int32
					eng := agent.NewEngine(agent.Deps{LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { inference.Add(1) })}, mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Model: "uncatalogued", Store: store, ContextWindow: d.reg.windowResolver(Config{}, provider, "uncatalogued")})
					svc, err := newTestServerService(server.Config{Engine: eng, Store: store, DefaultResolvedModel: server.ResolvedModel{ProviderID: provider, ModelID: "uncatalogued", ContextWindow: 128000}, ResolveContextWindow: func(p, m string) int64 { return int64(d.reg.echoWindowResolver(Config{}, p, m)()) }, AwaitContextWindow: func(ctx context.Context, p, m string) error { return awaitContextWindow(ctx, d.reg, p, m) }})
					if err != nil {
						t.Fatal(err)
					}
					defer svc.Close()
					sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
					if err != nil {
						t.Fatal(err)
					}
					for range 2 {
						run, err := svc.StartRun(context.Background(), sess.ID, "not admitted")
						if run != nil {
							for range run.Events() {
							}
							svc.FinishRun(sess.ID, run)
						}
						if !errors.Is(err, server.ErrContextWindowUnavailable) {
							t.Errorf("admission = %v, want retryable unavailable", err)
						}
					}
					saved, err := svc.GetSession(context.Background(), sess.ID)
					if err != nil {
						t.Fatal(err)
					}
					if calls.Load() != 1 || inference.Load() != 0 || len(saved.Conversation.Messages) != 0 || svc.ResolvedModel(sess.ID).ContextWindow != 0 {
						t.Fatalf("rejection effects: calls=%d inference=%d messages=%d echo=%+v", calls.Load(), inference.Load(), len(saved.Conversation.Messages), svc.ResolvedModel(sess.ID))
					}
					synctest.Wait()
					time.Sleep(discoveryCooldown) // Virtual clock advance, not a scheduling sleep.
					run, err := svc.StartRun(context.Background(), sess.ID, "retry without picker")
					if err != nil {
						t.Fatal(err)
					}
					for range run.Events() {
					}
					svc.FinishRun(sess.ID, run)
					if calls.Load() != 2 || inference.Load() != 1 || eng.ContextWindow() != coldGatewayWindow || svc.ResolvedModel(sess.ID).ContextWindow != coldGatewayWindow {
						t.Fatalf("recovery: calls=%d inference=%d engine=%d echo=%+v", calls.Load(), inference.Load(), eng.ContextWindow(), svc.ResolvedModel(sess.ID))
					}
				})
			})
		}
	}
}
