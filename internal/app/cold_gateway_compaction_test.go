package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	coldGatewayProvider     = "stacklok-gateway"
	coldGatewayModel        = "gpt-5.6-sol"
	coldGatewayDefaultModel = "gpt-5.6-terra"
	coldGatewayPlanModel    = "terra-1.1m"
	coldGatewayWindow       = 1_050_000
	coldGatewayPlanWindow   = 1_100_000
	coldGatewayListing      = `{"data":[{"id":"gpt-5.6-sol","display_name":"GPT-5.6 Sol","context_window":1050000},{"id":"gpt-5.6-terra","display_name":"GPT-5.6 Terra","context_window":1050000},{"id":"terra-1.1m","display_name":"Terra 1.1M","context_window":1100000}]}`
)

type coldGatewayModelServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
	status  int
	body    string
	calls   int
	once    sync.Once
}

func newColdGatewayModelServer(t *testing.T) *coldGatewayModelServer {
	t.Helper()
	f := &coldGatewayModelServer{
		status: http.StatusOK,
		body:   coldGatewayListing,
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("model listing path = %q, want /v1/models", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.calls++
		entered, release, responseStatus, responseBody := f.entered, f.release, f.status, f.body
		f.mu.Unlock()
		if release != nil {
			f.once.Do(func() { close(entered) })
			<-release
		}
		w.WriteHeader(responseStatus)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *coldGatewayModelServer) setResponse(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *coldGatewayModelServer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *coldGatewayModelServer) hold() (<-chan struct{}, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entered = make(chan struct{})
	f.release = make(chan struct{})
	return f.entered, sync.OnceFunc(func() { close(f.release) })
}

type coldGatewayRequest struct {
	model         string
	messages      int
	messageBytes  int
	historyTokens int
}

type coldGatewayCapture struct {
	mu       sync.Mutex
	requests []coldGatewayRequest
	observed chan struct{}
}

func newColdGatewayCapture() *coldGatewayCapture {
	return &coldGatewayCapture{observed: make(chan struct{}, 1)}
}

func (c *coldGatewayCapture) observe(req port.LLMRequest) {
	bytes := 0
	for _, m := range req.Messages {
		bytes += len(m.Text) + len(m.Reasoning)
	}
	c.mu.Lock()
	c.requests = append(c.requests, coldGatewayRequest{
		model:         req.Model,
		messages:      len(req.Messages),
		messageBytes:  bytes,
		historyTokens: agent.HeuristicTokenCounter{}.CountMessages(req.Messages),
	})
	c.mu.Unlock()
	select {
	case c.observed <- struct{}{}:
	default:
	}
}

func (c *coldGatewayCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func (c *coldGatewayCapture) last(t *testing.T) coldGatewayRequest {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		t.Fatal("fake inference provider was not called")
	}
	return c.requests[len(c.requests)-1]
}

func coldGatewayConfig(t *testing.T, fixture *coldGatewayModelServer, storeDir, workspace, memoryDir string, syncRefresh bool, capture *coldGatewayCapture) Config {
	t.Helper()
	operator := writeOperatorSettingsFile(t, "providers:\n  "+coldGatewayProvider+":\n    base_url: "+fixture.server.URL+"/v1\n    default_model: "+coldGatewayDefaultModel+"\n    api_flavor: openai-chat-completions\n    auth:\n      method: api_key\n")
	return Config{
		Workspace:               workspace,
		MemoryDir:               memoryDir,
		UserModelDir:            memoryDir + "/usermodel",
		LearningMode:            learning.Off,
		StoreDir:                storeDir,
		NoSoul:                  true,
		NoShell:                 true,
		PermissionsConventional: true,
		permConfigEnv:           isolatedPermConfigEnv(t),
		PermissionConfigs:       []string{operator},
		envDetector:             fakeEnv(nil),
		DefaultProvider:         coldGatewayProvider,
		DefaultModel:            coldGatewayDefaultModel,
		CustomProviderAPIKeys:   map[string]string{coldGatewayProvider: "offline-placeholder"},
		liveModelHTTPClient:     fixture.server.Client(),
		liveModelRefreshSync:    syncRefresh,
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			turns := make([]mockllm.Turn, 40)
			for i := range turns {
				turns[i] = mockllm.TextTurn("offline answer")
			}
			return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(capture.observe)}, turns...)
		},
	}
}

func runColdGatewayPrompt(t *testing.T, built *Built, id session.SessionID, prompt string) []session.Event {
	t.Helper()
	run, err := built.Service.StartRun(context.Background(), id, prompt)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	events := runEvents(run)
	built.Service.FinishRun(id, run)
	return events
}

type coldGatewayRunResult struct {
	events []session.Event
	err    error
}

// startColdGatewayPrompt lets the refresh remain blocked until the inference
// request is observable. The timeout in the caller is only a safeguard: once run
// admission waits for live metadata, it releases the refresh so the regression
// cannot deadlock on the desired implementation.
func startColdGatewayPrompt(ctx context.Context, built *Built, id session.SessionID, prompt string) <-chan coldGatewayRunResult {
	result := make(chan coldGatewayRunResult, 1)
	go func() {
		run, err := built.Service.StartRun(ctx, id, prompt)
		if err != nil {
			result <- coldGatewayRunResult{err: err}
			return
		}
		var events []session.Event
		for ev := range run.Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
			}
			events = append(events, ev)
		}
		built.Service.FinishRun(id, run)
		result <- coldGatewayRunResult{events: events}
	}()
	return result
}

func receiveColdGatewayRun(t *testing.T, ch <-chan coldGatewayRunResult) coldGatewayRunResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cold gateway run")
		return coldGatewayRunResult{}
	}
}

func waitColdGatewayEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for model listing request")
	}
}

func coldGatewayEventFacts(events []session.Event) (compactions int, archivedMessages int, manifestWindow int, manifestModel string) {
	for _, ev := range events {
		switch ev.Type {
		case session.EvCompaction:
			compactions++
		case session.EvCompactionArchive:
			if ev.CompactionArchive != nil {
				archivedMessages = len(ev.CompactionArchive.Replaced)
			}
		case session.EvRequestManifest:
			if ev.RequestManifest != nil {
				manifestWindow = ev.RequestManifest.ContextWindow
				manifestModel = ev.RequestManifest.Model
			}
		}
	}
	return
}

func primeColdGatewaySession(t *testing.T, fixture *coldGatewayModelServer, storeDir, workspace, memoryDir string) session.SessionID {
	t.Helper()
	capture := newColdGatewayCapture()
	built, err := buildIsolated(t, context.Background(), coldGatewayConfig(t, fixture, storeDir, workspace, memoryDir, true, capture))
	if err != nil {
		t.Fatalf("warm Build: %v", err)
	}
	sess, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 3}, serveradapter.ProviderSelector{ProviderID: coldGatewayProvider, ModelID: coldGatewayModel})
	if err != nil {
		built.Close()
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	var events []session.Event
	// With 11 prompts (22 persisted messages), keep=6 yields initial cut 16;
	// snapping back over three recent user turns moves it to 10. The huge turn at
	// message index 2 therefore remains in the summarizable head.
	prompts := []string{
		"small pinned first prompt",
		strings.Repeat("0123456789abcdef", 37_500),
	}
	for i := 0; i < 9; i++ {
		prompts = append(prompts, fmt.Sprintf("small follow-up %02d", i+1))
	}
	for _, prompt := range prompts {
		events = append(events, runColdGatewayPrompt(t, built, sess.ID, prompt)...)
	}
	compactions, archived, window, model := coldGatewayEventFacts(events)
	req := capture.last(t)
	t.Logf("primed warm session: window=%d model=%s request_model=%s message_bytes=%d history_tokens=%d compactions=%d archived_messages=%d", window, model, req.model, req.messageBytes, req.historyTokens, compactions, archived)
	if window != coldGatewayWindow || model != coldGatewayModel || req.model != coldGatewayModel || compactions != 0 {
		built.Close()
		t.Fatalf("warm prime did not preserve the large first prompt: window=%d model=%q request=%+v compactions=%d", window, model, req, compactions)
	}
	built.Close()
	return sess.ID
}

func TestColdGatewayRestartDoesNotCompactBeforeLiveContextWindow(t *testing.T) {
	ctx := context.Background()

	t.Run("refresh ready control", func(t *testing.T) {
		fixture := newColdGatewayModelServer(t)
		root := t.TempDir()
		id := primeColdGatewaySession(t, fixture, root+"/store", root+"/workspace", root+"/memory")
		capture := newColdGatewayCapture()
		built, err := buildIsolated(t, ctx, coldGatewayConfig(t, fixture, root+"/store", root+"/workspace", root+"/memory", true, capture))
		if err != nil {
			t.Fatalf("ready restart Build: %v", err)
		}
		defer built.Close()

		before, err := built.Service.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("GetSession before ready run: %v", err)
		}
		events := runColdGatewayPrompt(t, built, id, "resumed prompt with metadata ready")
		compactions, archived, window, model := coldGatewayEventFacts(events)
		req := capture.last(t)
		after, err := built.Service.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("GetSession after ready run: %v", err)
		}
		t.Logf("ready restart: window=%d model=%s request_model=%s history_tokens=%d compactions=%d archived_messages=%d", window, model, req.model, req.historyTokens, compactions, archived)
		if window != coldGatewayWindow || model != coldGatewayModel || req.model != coldGatewayModel || compactions != 0 || archived != 0 || req.historyTokens < 150_000 || len(after.Conversation.Messages) != len(before.Conversation.Messages)+2 {
			t.Fatalf("ready restart changed large history: window=%d model=%q request=%+v compactions=%d archived=%d messages=%d, want %d", window, model, req, compactions, archived, len(after.Conversation.Messages), len(before.Conversation.Messages)+2)
		}
	})

	t.Run("refresh in flight", func(t *testing.T) {
		fixture := newColdGatewayModelServer(t)
		root := t.TempDir()
		id := primeColdGatewaySession(t, fixture, root+"/store", root+"/workspace", root+"/memory")
		entered, release := fixture.hold()
		capture := newColdGatewayCapture()
		admitted := make(chan struct {
			provider string
			model    string
		}, 1)
		cfg := coldGatewayConfig(t, fixture, root+"/store", root+"/workspace", root+"/memory", false, capture)
		cfg.awaitContextWindowObserver = func(provider, model string) {
			select {
			case admitted <- struct {
				provider string
				model    string
			}{provider: provider, model: model}:
			default:
			}
		}
		built, err := buildIsolated(t, ctx, cfg)
		if err != nil {
			release()
			t.Fatalf("cold restart Build: %v", err)
		}
		defer func() {
			release()
			built.Close()
		}()
		waitColdGatewayEntered(t, entered)

		before, err := built.Service.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("GetSession before cold run: %v", err)
		}
		beforeAsk, beforePending := before.PendingAsk()
		beforeRetryMetadata, beforeRetry := before.FailedStepRetryPending()
		coldEcho := built.Service.ResolvedModel(id).ContextWindow

		resultCh := startColdGatewayPrompt(ctx, built, id, "cold-admitted prompt must wait for metadata")
		var admission struct {
			provider string
			model    string
		}
		select {
		case admission = <-admitted:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for Build-wired context-window admission")
		}
		if admission.provider != coldGatewayProvider || admission.model != coldGatewayModel {
			t.Fatalf("cold admission selected provider/model = %q/%q, want %q/%q", admission.provider, admission.model, coldGatewayProvider, coldGatewayModel)
		}
		if capture.count() != 0 {
			t.Fatalf("inference calls before refresh release = %d, want 0", capture.count())
		}
		afterAdmission, err := built.Service.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("GetSession while cold admission is waiting: %v", err)
		}
		afterAsk, afterPending := afterAdmission.PendingAsk()
		afterRetryMetadata, afterRetry := afterAdmission.FailedStepRetryPending()
		if len(afterAdmission.Conversation.Messages) != len(before.Conversation.Messages) ||
			afterPending != beforePending || !reflect.DeepEqual(afterAsk, beforeAsk) ||
			afterRetry != beforeRetry || afterRetryMetadata != beforeRetryMetadata {
			t.Fatalf("cold admission mutated durable work before metadata release: messages=%d->%d pending=%v->%v retry=(%v,%v)->(%v,%v)",
				len(before.Conversation.Messages), len(afterAdmission.Conversation.Messages), beforeAsk, afterAsk,
				beforeRetryMetadata, beforeRetry, afterRetryMetadata, afterRetry)
		}

		release()
		admittedRun := receiveColdGatewayRun(t, resultCh)
		if admittedRun.err != nil {
			t.Fatalf("cold-admitted run error = %v", admittedRun.err)
		}
		events := admittedRun.events
		compactions, archived, window, model := coldGatewayEventFacts(events)
		req := capture.last(t)
		after, err := built.Service.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("GetSession after cold run: %v", err)
		}
		healed := built.Service.ResolvedModel(id).ContextWindow
		t.Logf("cold restart: initial_echo=%d window=%d healed=%d model=%s request_model=%s history_tokens=%d compactions=%d archived_messages=%d messages=%d", coldEcho, window, healed, model, req.model, req.historyTokens, compactions, archived, len(after.Conversation.Messages))

		if coldEcho != 0 {
			t.Errorf("cold provisional echo = %d, want 0", coldEcho)
		}
		if healed != coldGatewayWindow {
			t.Errorf("context window after refresh = %d, want %d", healed, coldGatewayWindow)
		}
		if window != coldGatewayWindow || model != coldGatewayModel || req.model != coldGatewayModel || compactions != 0 || archived != 0 || req.historyTokens < 150_000 || len(after.Conversation.Messages) != len(before.Conversation.Messages)+2 {
			t.Fatalf("cold restart compacted before live metadata was ready: window=%d model=%q request=%+v compactions=%d archived=%d messages=%d, want window=%d model=%q no archive and %d messages", window, model, req, compactions, archived, len(after.Conversation.Messages), coldGatewayWindow, coldGatewayModel, len(before.Conversation.Messages)+2)
		}
	})
}

func TestBuildHTTPColdRestartUsesPersistedProviderDefaultPlanIdentity(t *testing.T) {
	ctx := context.Background()
	fixture := newColdGatewayModelServer(t)
	root := t.TempDir()

	warmCfg := coldGatewayConfig(t, fixture, root+"/store", root+"/workspace", root+"/memory", true, newColdGatewayCapture())
	warmCfg.ModelSlots = map[string]string{slotPlan: coldGatewayPlanModel}
	warm, err := buildIsolated(t, ctx, warmCfg)
	if err != nil {
		t.Fatalf("warm Build: %v", err)
	}
	warmHTTP := httptest.NewServer(serveradapter.NewHTTPHandler(warm.Service))
	createBody := strings.NewReader(`{"mode":"plan","provider_id":"` + coldGatewayProvider + `"}`)
	createResp, err := http.Post(warmHTTP.URL+"/v1/sessions", "application/json", createBody)
	if err != nil {
		t.Fatalf("POST create session: %v", err)
	}
	var created struct {
		SessionID     string `json:"session_id"`
		ResolvedModel struct {
			ProviderID    string `json:"provider_id"`
			ModelID       string `json:"model_id"`
			ContextWindow int64  `json:"context_window"`
		} `json:"resolved_model"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		createResp.Body.Close()
		t.Fatalf("decode create session: %v", err)
	}
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", createResp.StatusCode, http.StatusCreated)
	}
	if created.ResolvedModel.ProviderID != coldGatewayProvider || created.ResolvedModel.ModelID != coldGatewayPlanModel {
		t.Fatalf("created resolved model = %+v, want provider-only default re-resolved to plan model %q", created.ResolvedModel, coldGatewayPlanModel)
	}
	warmHTTP.Close()
	warm.Close()

	entered, release := fixture.hold()
	capture := newColdGatewayCapture()
	admitted := make(chan struct{ provider, model string }, 1)
	coldCfg := coldGatewayConfig(t, fixture, root+"/store", root+"/workspace", root+"/memory", false, capture)
	coldCfg.ModelSlots = map[string]string{slotPlan: coldGatewayPlanModel}
	coldCfg.awaitContextWindowObserver = func(provider, model string) {
		admitted <- struct{ provider, model string }{provider, model}
	}
	coldCfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		turn := mockllm.ChunksTurn(
			mockllm.TextChunk("offline plan answer"),
			mockllm.UsageChunk(session.Usage{InputTokens: 7, OutputTokens: 3}),
			mockllm.DoneChunk(session.StopEndTurn),
		)
		return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(capture.observe)}, turn)
	}
	cold, err := buildIsolated(t, ctx, coldCfg)
	if err != nil {
		release()
		t.Fatalf("cold Build: %v", err)
	}
	defer func() {
		release()
		cold.Close()
	}()
	waitColdGatewayEntered(t, entered)
	coldHTTP := httptest.NewServer(serveradapter.NewHTTPHandler(cold.Service))
	defer coldHTTP.Close()

	type sessionProjection struct {
		ResolvedModel struct {
			ProviderID    string `json:"provider_id"`
			ModelID       string `json:"model_id"`
			ContextWindow int64  `json:"context_window"`
		} `json:"resolved_model"`
		TokenUsage map[string]struct {
			Models map[string]struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"models"`
		} `json:"token_usage"`
	}
	getSession := func() sessionProjection {
		t.Helper()
		resp, err := http.Get(coldHTTP.URL + "/v1/sessions/" + created.SessionID)
		if err != nil {
			t.Fatalf("GET session: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET session status = %d, want 200", resp.StatusCode)
		}
		var got sessionProjection
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode GET session: %v", err)
		}
		return got
	}

	before := getSession()
	if before.ResolvedModel.ProviderID != coldGatewayProvider || before.ResolvedModel.ModelID != coldGatewayPlanModel || before.ResolvedModel.ContextWindow != 0 {
		t.Fatalf("cold GET resolved model = %+v, want persisted provider-only selector + plan slot with provisional window", before.ResolvedModel)
	}
	if capture.count() != 0 {
		t.Fatalf("read-only GET made %d inference calls, want 0", capture.count())
	}
	select {
	case got := <-admitted:
		t.Fatalf("read-only GET entered run admission for %q/%q", got.provider, got.model)
	default:
	}

	promptDone := make(chan error, 1)
	go func() {
		resp, err := http.Post(coldHTTP.URL+"/v1/sessions/"+created.SessionID+"/prompt", "application/json", strings.NewReader(`{"text":"use the persisted plan model"}`))
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err == nil && resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("prompt status = %d", resp.StatusCode)
			}
		}
		promptDone <- err
	}()
	var admission struct{ provider, model string }
	select {
	case admission = <-admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for selected-engine context-window admission")
	}
	if admission.provider != coldGatewayProvider || admission.model != coldGatewayPlanModel {
		t.Fatalf("admission identity = %q/%q, want %q/%q", admission.provider, admission.model, coldGatewayProvider, coldGatewayPlanModel)
	}
	if capture.count() != 0 {
		t.Fatalf("inference calls before selected-model metadata settled = %d, want 0", capture.count())
	}
	release()
	select {
	case err := <-promptDone:
		if err != nil {
			t.Fatalf("POST prompt: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP prompt completion")
	}
	request := capture.last(t)
	if request.model != coldGatewayPlanModel {
		t.Fatalf("inference model = %q, want %q", request.model, coldGatewayPlanModel)
	}
	after := getSession()
	if after.ResolvedModel.ContextWindow != coldGatewayPlanWindow {
		t.Fatalf("healed context window = %d, want %d", after.ResolvedModel.ContextWindow, coldGatewayPlanWindow)
	}
	usage := after.TokenUsage[string(session.UsageKindMain)].Models[coldGatewayProvider+"/"+coldGatewayPlanModel]
	if usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("selected-model usage = %+v, want input=7 output=3", usage)
	}
}

func TestCatalogBackedEmptyDiscoveryRejectsUncataloguedSelectionWithoutMutation(t *testing.T) {
	const model = "openrouter/uncatalogued-selected"
	var listings atomic.Int32
	liveClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		listings.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})}
	root := t.TempDir()
	capture := newColdGatewayCapture()
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace: root + "/workspace", MemoryDir: root + "/memory", StoreDir: root + "/store",
		NoSoul: true, NoShell: true, PermissionsConventional: true,
		permConfigEnv: isolatedPermConfigEnv(t), envDetector: fakeEnv(map[string]string{"OPENROUTER_API_KEY": "offline"}),
		DefaultProvider: providerOpenRouter, DefaultModel: embeddedModels(providerOpenRouter)[0].ID,
		liveModelHTTPClient: liveClient, liveModelRefreshSync: true,
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(capture.observe)}, mockllm.TextTurn("must not run"))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, serveradapter.ProviderSelector{ProviderID: providerOpenRouter, ModelID: model})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	before, err := built.Service.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := built.Service.StartRun(context.Background(), sess.ID, "must not be recorded"); !errors.Is(err, serveradapter.ErrContextWindowUnavailable) {
		t.Fatalf("StartRun = %v, want ErrContextWindowUnavailable", err)
	}
	after, err := built.Service.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if capture.count() != 0 || len(after.Conversation.Messages) != len(before.Conversation.Messages) {
		t.Fatalf("rejected run mutated work: inference=%d messages=%d->%d", capture.count(), len(before.Conversation.Messages), len(after.Conversation.Messages))
	}
	models := built.Service.ListModels(context.Background())
	if listings.Load() == 0 || len(models) == 0 {
		t.Fatalf("embedded picker fallback lost: listing calls=%d models=%d", listings.Load(), len(models))
	}
}

func TestColdGatewayDiscoveryFailureRejectsWithoutMutationAndRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "bad gateway", status: http.StatusBadGateway, body: `{"error":"upstream unavailable"}`},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
		{name: "empty", status: http.StatusOK, body: `{"data":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newColdGatewayModelServer(t)
			fixture.setResponse(tc.status, tc.body)
			root := t.TempDir()
			capture := newColdGatewayCapture()
			var discoveryOffset atomic.Int64
			cfg := coldGatewayConfig(t, fixture, root+"/store", root+"/workspace", root+"/memory", true, capture)
			cfg.modelDiscoveryNow = func() time.Time { return time.Now().Add(time.Duration(discoveryOffset.Load())) }
			built, err := buildIsolated(t, context.Background(), cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()

			sess, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 3}, serveradapter.ProviderSelector{ProviderID: coldGatewayProvider, ModelID: coldGatewayModel})
			if err != nil {
				t.Fatalf("CreateSessionWithProvider: %v", err)
			}
			before, err := built.Service.GetSession(context.Background(), sess.ID)
			if err != nil {
				t.Fatalf("GetSession before rejection: %v", err)
			}
			_, err = built.Service.StartRun(context.Background(), sess.ID, "must not be recorded")
			if !errors.Is(err, serveradapter.ErrContextWindowUnavailable) {
				t.Fatalf("StartRun error = %v, want ErrContextWindowUnavailable", err)
			}
			if capture.count() != 0 {
				t.Fatalf("inference calls after rejected admission = %d, want 0", capture.count())
			}
			if fixture.callCount() != 1 {
				t.Fatalf("listing calls after first rejection = %d, want startup request only", fixture.callCount())
			}
			after, err := built.Service.GetSession(context.Background(), sess.ID)
			if err != nil {
				t.Fatalf("GetSession after rejection: %v", err)
			}
			if len(after.Conversation.Messages) != len(before.Conversation.Messages) {
				t.Fatalf("conversation changed on rejected admission: %d -> %d", len(before.Conversation.Messages), len(after.Conversation.Messages))
			}

			fixture.setResponse(http.StatusOK, coldGatewayListing)
			if _, err := built.Service.StartRun(context.Background(), sess.ID, "still cooling down"); !errors.Is(err, serveradapter.ErrContextWindowUnavailable) {
				t.Fatalf("cooldown admission=%v, want unavailable", err)
			}
			if fixture.callCount() != 1 {
				t.Fatal("cooldown started another listing")
			}
			discoveryOffset.Add(int64(discoveryCooldown))
			events := runColdGatewayPrompt(t, built, sess.ID, "retry after discovery recovery")
			_, archived, window, model := coldGatewayEventFacts(events)
			if archived != 0 || window != coldGatewayWindow || model != coldGatewayModel || capture.count() != 1 || fixture.callCount() != 2 {
				t.Fatalf("healthy retry did not use recovered metadata: archive=%d window=%d model=%q inference=%d listings=%d", archived, window, model, capture.count(), fixture.callCount())
			}
		})
	}
}
