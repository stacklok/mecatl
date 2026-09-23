package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// codexCompositionTransport is the offline subscription backend for the full
// composition scenario. Model inventory and inference share the real immutable
// request policy, while the scripted Responses stream performs a genuine Read
// tool round trip through provider/openai's translator.
type codexCompositionTransport struct {
	mu             sync.Mutex
	responseBodies [][]byte
}

func (t *codexCompositionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/models") {
		return codexHTTPResponse(req, http.StatusOK, "application/json", `{"models":[{"slug":"gpt-5","display_name":"GPT-5","visibility":"list","input_modalities":["text"],"supported_reasoning_levels":[{"effort":"high"}]}]}`), nil
	}
	if !strings.HasSuffix(req.URL.Path, "/responses") {
		return nil, fmt.Errorf("unexpected Codex request path %q", req.URL.Path)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.responseBodies = append(t.responseBodies, append([]byte(nil), body...))
	call := len(t.responseBodies)
	t.mu.Unlock()

	var stream string
	switch call {
	case 1:
		stream = "event: response.output_item.done\n" +
			`data: {"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"item_read","type":"function_call","status":"completed","name":"Read","arguments":"{\"path\":\"note.txt\"}","call_id":"call_read"}}` + "\n\n" +
			"event: response.completed\n" +
			`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"
	case 2:
		stream = "event: response.output_text.delta\n" +
			`data: {"type":"response.output_text.delta","sequence_number":0,"item_id":"message_done","output_index":0,"content_index":0,"delta":"COMPOSED-CODEX-DONE"}` + "\n\n" +
			"event: response.output_item.done\n" +
			`data: {"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{"id":"message_done","type":"message","status":"completed","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"COMPOSED-CODEX-DONE","annotations":[]}]}}` + "\n\n" +
			"event: response.completed\n" +
			`data: {"type":"response.completed","sequence_number":2,"response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}` + "\n\n"
	default:
		return nil, fmt.Errorf("unexpected Codex inference request %d", call)
	}
	return codexHTTPResponse(req, http.StatusOK, "text/event-stream", stream), nil
}

func codexHTTPResponse(req *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func (t *codexCompositionTransport) bodies() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]byte, len(t.responseBodies))
	for i := range t.responseBodies {
		out[i] = append([]byte(nil), t.responseBodies[i]...)
	}
	return out
}

// TestOpenAICodexCompositionScenario is AC8.1's real-boundary proof: Build
// registers both billing identities, an explicit Codex session is rebuilt by
// sessionEngineFactory, and the native Responses adapter completes a streamed
// Read tool turn. The API-key OpenAI provider remains a distinct selectable
// session and never receives the subscription credential.
func TestOpenAICodexCompositionScenario(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("tool-result-from-workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := &codexCompositionTransport{}
	built, err := buildIsolated(t, ctx, Config{
		Workspace:             workspace,
		NoSoul:                true,
		DefaultProvider:       providerOpenAICodex,
		OpenAICodexCredential: codexRegistryCredential(t),
		openAICodexNow:        func() time.Time { return codexRegistryNow },
		openAICodexTransport:  transport,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-api-openai"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		liveModelRefreshSync:  true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	models := built.Service.ListModels(ctx)
	var sawCodex, sawAPI bool
	for _, model := range models {
		switch model.GetProviderId() {
		case providerOpenAICodex:
			sawCodex = sawCodex || model.GetId() == "gpt-5"
		case providerOpenAI:
			sawAPI = true
		}
	}
	if !sawCodex || !sawAPI {
		t.Fatalf("ListModels provider inventory = Codex:%t API-OpenAI:%t, want both independently selectable", sawCodex, sawAPI)
	}

	codexSession, err := built.Service.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenAICodex, ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("create Codex session: %v", err)
	}
	run, err := built.Service.StartRun(ctx, codexSession.ID, "read note.txt")
	if err != nil {
		t.Fatalf("StartRun(Codex): %v", err)
	}
	if got := drainRun(run); got != "COMPOSED-CODEX-DONE" {
		t.Fatalf("Codex terminal text = %q, want COMPOSED-CODEX-DONE", got)
	}
	bodies := transport.bodies()
	if len(bodies) != 2 {
		t.Fatalf("Codex /responses calls = %d, want tool request + continuation", len(bodies))
	}
	if !bytes.Contains(bodies[1], []byte("tool-result-from-workspace")) {
		t.Fatalf("continuation request omitted the real Read result: %s", bodies[1])
	}

	apiSession, err := built.Service.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5"})
	if err != nil {
		t.Fatalf("create API OpenAI session: %v", err)
	}
	if got := built.Service.ResolvedModel(apiSession.ID); got.ProviderID != providerOpenAI {
		t.Fatalf("API OpenAI resolved provider = %q, want %q", got.ProviderID, providerOpenAI)
	}
}

// TestOpenAICodexRemintAndInheritance is AC8.2's session-factory proof. A
// Codex selector whose effort and live image capability differ from the shared
// default invokes the one remint closure exactly once; the main engine and its
// default Subagent then share that reminted Codex provider. The no-FS arm proves
// the same selector retains the profile's exact file-tool exclusions.
func TestOpenAICodexRemintAndInheritance(t *testing.T) {
	ctx := context.Background()
	apiProvider := mockllm.New(mockllm.TextTurn("WRONG-API-PROVIDER"))
	var (
		mu       sync.Mutex
		remints  int
		efforts  []string
		capsSeen []port.ProviderCapabilities
		models   []string
	)
	codexProvider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithCapabilities(port.ProviderCapabilities{Image: true}),
		mockllm.WithRequestObserver(func(req port.LLMRequest) {
			mu.Lock()
			models = append(models, req.Model)
			mu.Unlock()
		}),
	},
		mockllm.ToolCallTurn(session.NewToolCall("parent-sub", "Subagent", json.RawMessage(`{"prompt":"report provider inheritance"}`))),
		mockllm.TextTurn("CODEX-CHILD"),
		mockllm.TextTurn("CODEX-PARENT"),
	)
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenAI: {id: providerOpenAI, provider: apiProvider, available: true},
			providerOpenAICodex: {
				id:          providerOpenAICodex,
				provider:    codexProvider,
				available:   true,
				defaultCaps: port.ProviderCapabilities{Image: false},
				remint: func(effort string, caps port.ProviderCapabilities) port.LLMProvider {
					mu.Lock()
					defer mu.Unlock()
					remints++
					efforts = append(efforts, effort)
					capsSeen = append(capsSeen, caps)
					return codexProvider
				},
			},
		},
		defaultID: providerOpenAI,
		meta:      newMetadataFixture(),
	}
	reg.meta.setMetadataFixture(map[string][]modelEntry{
		providerOpenAICodex: {{
			ID: "codex-image", InputModalities: []string{"text", "image"}, Reasoning: true, ToolCall: true,
		}},
	})
	cfg := Config{Model: "api-default", NoSoul: true, AllowAllTools: true}
	store := memstore.New()
	factory := sessionEngineFactory(cfg, reg, apiProvider, store,
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	result, err := factory(ctx, server.ProviderSelector{
		ProviderID: providerOpenAICodex, ModelID: "codex-image", ReasoningEffort: "high",
	}, nil, server.ProfileDefault, "/ws", session.ModeDefault)
	if err != nil {
		t.Fatalf("sessionEngineFactory: %v", err)
	}
	defer func() { _ = result.Close() }()
	if result.ProviderID != providerOpenAICodex || result.ModelID != "codex-image" || result.ReasoningEffort != "high" || !result.Capabilities.Image {
		t.Fatalf("resolved Codex result = provider:%q model:%q effort:%q caps:%+v", result.ProviderID, result.ModelID, result.ReasoningEffort, result.Capabilities)
	}
	mu.Lock()
	if remints != 1 || !reflect.DeepEqual(efforts, []string{"high"}) || len(capsSeen) != 1 || !capsSeen[0].Image {
		mu.Unlock()
		t.Fatalf("remints = %d efforts=%v caps=%+v, want one combined effort/capability remint", remints, efforts, capsSeen)
	}
	mu.Unlock()

	sess := session.New("codex-inherit", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 8}, time.Unix(0, 0))
	if got := drainRun(result.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "delegate"})); got != "CODEX-PARENT" {
		t.Fatalf("parent final = %q, want CODEX-PARENT (child must inherit selected Codex provider)", got)
	}
	mu.Lock()
	gotModels := append([]string(nil), models...)
	mu.Unlock()
	if len(gotModels) != 3 {
		t.Fatalf("Codex provider requests = %v, want parent → child → parent", gotModels)
	}
	for _, model := range gotModels {
		if model != "codex-image" {
			t.Fatalf("inherited request models = %v, want every request on codex-image", gotModels)
		}
	}

	t.Run("Codex no-FS selector retains exact profile", func(t *testing.T) {
		var offered []string
		noFSProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			for _, spec := range req.Tools {
				offered = append(offered, spec.Name)
			}
		})}, mockllm.TextTurn("NOFS-CODEX"))
		reg2 := &providerRegistry{
			entries: map[string]providerEntry{providerOpenAICodex: {
				id: providerOpenAICodex, provider: noFSProvider, available: true,
			}},
			defaultID: providerOpenAICodex,
			meta:      newMetadataFixture(),
		}
		factory2 := sessionEngineFactory(Config{Model: "codex-image", NoSoul: true}, reg2, noFSProvider,
			memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
			prompt.RootAssembler{}, catalogAssets{}, nil)
		res, err := factory2(ctx, server.ProviderSelector{ProviderID: providerOpenAICodex, ModelID: "codex-image"},
			nil, server.ProfileNoFS, "", session.ModePlan)
		if err != nil {
			t.Fatalf("no-FS factory: %v", err)
		}
		defer func() { _ = res.Close() }()
		noFSEnv := testEnvironment(nofs.New(), nil)
		noFSSess := session.New("codex-nofs", session.ModePlan, noFSEnv.Ref(), session.Limits{MaxTurns: 3}, time.Unix(0, 0))
		if got := drainRun(res.Engine.Run(ctx, noFSSess, noFSEnv, agent.RunRequest{Text: "answer without files"})); got != "NOFS-CODEX" {
			t.Fatalf("no-FS result = %q", got)
		}
		sort.Strings(offered)
		for _, forbidden := range noFSExcludedTools {
			if i := sort.SearchStrings(offered, forbidden); i < len(offered) && offered[i] == forbidden {
				t.Errorf("Codex no-FS catalog exposed forbidden tool %q: %v", forbidden, offered)
			}
		}
		for _, required := range []string{"Subagent", "InspectSubagent", "SubagentStatus", "PresentPlan"} {
			if i := sort.SearchStrings(offered, required); i >= len(offered) || offered[i] != required {
				t.Errorf("Codex no-FS catalog omitted portable tool %q: %v", required, offered)
			}
		}
	})
}

func codexPersistenceConfig(t *testing.T, workspace, storeDir, defaultProvider string, replies map[string]string) Config {
	t.Helper()
	models := &codexModelsTransport{body: `{"models":[{"slug":"gpt-5","display_name":"GPT-5","visibility":"list"}]}`}
	return Config{
		Workspace:             workspace,
		StoreDir:              storeDir,
		MemoryDir:             filepath.Join(storeDir, "memory"),
		Model:                 "gpt-5",
		DefaultProvider:       defaultProvider,
		NoSoul:                true,
		OpenAICodexCredential: codexRegistryCredential(t),
		openAICodexNow:        func() time.Time { return codexRegistryNow },
		openAICodexTransport:  models,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-api-openai"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn(replies[id]))
		},
	}
}

// TestOpenAICodexExplicitSelectorRehydrates proves AC8.3's narrow persistence
// contract with two complete Build instances over the same jsonlstore. The
// first process persists the explicit selector; the second has an empty
// in-memory engine registry and must reconstruct the Codex engine from those
// labels before it can run the turn.
func TestOpenAICodexExplicitSelectorRehydrates(t *testing.T) {
	ctx := context.Background()
	workspace, storeDir := t.TempDir(), t.TempDir()
	cfg1 := codexPersistenceConfig(t, workspace, storeDir, providerOpenAI, map[string]string{
		providerOpenAI: "API-BEFORE", providerOpenAICodex: "CODEX-BEFORE",
	})
	built1, err := buildIsolated(t, ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenAICodex, ModelID: "gpt-5", ReasoningEffort: "high"})
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if sess.ProviderID != providerOpenAICodex || sess.ModelID != "gpt-5" || sess.ReasoningEffort != "high" {
		built1.Close()
		t.Fatalf("persisted selector labels = provider:%q model:%q effort:%q", sess.ProviderID, sess.ModelID, sess.ReasoningEffort)
	}
	built1.Close()

	cfg2 := codexPersistenceConfig(t, workspace, storeDir, providerOpenAI, map[string]string{
		providerOpenAI: "API-AFTER", providerOpenAICodex: "CODEX-AFTER",
	})
	built2, err := buildIsolated(t, ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()
	run, err := built2.Service.StartRun(ctx, sess.ID, "after restart")
	if err != nil {
		t.Fatalf("StartRun after restart: %v", err)
	}
	if got := drainRun(run); got != "CODEX-AFTER" {
		t.Fatalf("rehydrated selector routed to %q, want CODEX-AFTER", got)
	}
	if got := built2.Service.ResolvedModel(sess.ID); got.ProviderID != providerOpenAICodex || got.ModelID != "gpt-5" || got.ReasoningEffort != "high" {
		t.Fatalf("rehydrated resolved model = %+v", got)
	}
}

// TestZeroSelectorStillFollowsDeploymentDefault is AC8.3's non-expansion guard:
// a session with no persisted selector remains floating. After a restart with a
// different deployment default, its next turn uses that new default rather than
// being silently pinned to the provider that happened to be active at creation.
func TestZeroSelectorStillFollowsDeploymentDefault(t *testing.T) {
	ctx := context.Background()
	workspace, storeDir := t.TempDir(), t.TempDir()
	built1, err := buildIsolated(t, ctx, codexPersistenceConfig(t, workspace, storeDir, providerOpenAI, map[string]string{
		providerOpenAI: "API-BEFORE", providerOpenAICodex: "CODEX-BEFORE",
	}))
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ProviderID != "" || sess.ModelID != "" || sess.ReasoningEffort != "" {
		built1.Close()
		t.Fatalf("zero selector gained persisted labels: provider:%q model:%q effort:%q", sess.ProviderID, sess.ModelID, sess.ReasoningEffort)
	}
	built1.Close()

	built2, err := buildIsolated(t, ctx, codexPersistenceConfig(t, workspace, storeDir, providerOpenAICodex, map[string]string{
		providerOpenAI: "API-AFTER", providerOpenAICodex: "CODEX-NEW-DEFAULT",
	}))
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()
	run, err := built2.Service.StartRun(ctx, sess.ID, "follow the new deployment default")
	if err != nil {
		t.Fatalf("StartRun after restart: %v", err)
	}
	if got := drainRun(run); got != "CODEX-NEW-DEFAULT" {
		t.Fatalf("zero-selector session routed to %q, want the new Codex deployment default", got)
	}
	if got := built2.Service.ResolvedModel(sess.ID); got.ProviderID != providerOpenAICodex {
		t.Fatalf("zero-selector resolved provider after restart = %q, want %q", got.ProviderID, providerOpenAICodex)
	}
}

// TestADR_0104_OpenAICodexSecretSentinels is AC8.5's composition-level
// regression proof for the manually supplied bearer token.
func TestADR_0104_OpenAICodexSecretSentinels(t *testing.T) {
	sentinel := base64.RawURLEncoding.EncodeToString([]byte("MECATL_STEP8_SECRET_SENTINEL_8f3c91"))
	credential, token := codexSentinelCredential(t, sentinel)
	if !strings.Contains(token, sentinel) {
		t.Fatal("precondition: sentinel is not present verbatim in the valid bearer")
	}

	ctx := context.Background()
	workspace, storeDir := t.TempDir(), t.TempDir()
	diag := newCapturingDiagnostics()
	rejecting := &codexRejectingTransport{}
	hooks := &codexSentinelHookCapture{}
	cfg := Config{
		Workspace:             workspace,
		StoreDir:              storeDir,
		MemoryDir:             filepath.Join(storeDir, "memory"),
		Model:                 "gpt-5",
		DefaultProvider:       providerOpenAICodex,
		NoSoul:                true,
		Diagnostics:           diag,
		OpenAICodexCredential: credential,
		openAICodexNow:        func() time.Time { return codexRegistryNow },
		openAICodexTransport:  rejecting,
		liveModelRefreshSync:  true,
		hookRunner:            hooks,
	}
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var artifacts []struct{ name, value string }
	addArtifact := func(name string, value []byte) {
		artifacts = append(artifacts, struct{ name, value string }{name, string(value)})
	}
	addArtifact("credential formatting", []byte(fmt.Sprintf("%v %+v %#v", credential, credential, credential)))

	statuses := built.Service.ProviderStatuses()
	if len(statuses) != 1 || statuses[0].GetProviderId() != providerOpenAICodex || statuses[0].GetState() != statusUnauthorized {
		built.Close()
		t.Fatalf("Codex provider status = %+v, want one unauthorized row", statuses)
	}
	statusJSON, err := json.Marshal(statuses)
	if err != nil {
		built.Close()
		t.Fatalf("marshal provider status: %v", err)
	}
	addArtifact("provider status", statusJSON)

	sess, err := built.Service.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenAICodex, ModelID: "gpt-5"})
	if err != nil {
		built.Close()
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	resp, err := http.Post(srv.URL+"/v1/sessions/"+string(sess.ID)+"/prompt", "application/json", strings.NewReader(`{"text":"reject offline"}`))
	if err != nil {
		srv.Close()
		built.Close()
		t.Fatalf("POST rejected Codex prompt: %v", err)
	}
	relayBody, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	srv.Close()
	if readErr != nil {
		built.Close()
		t.Fatalf("read rejected Codex relay: %v", readErr)
	}
	addArtifact("service relay", relayBody)
	built.Close()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("reopen jsonlstore: %v", err)
	}
	loaded, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session snapshot: %v", err)
	}
	if loaded.State != session.StateFailed {
		t.Fatalf("rejected provider session state = %q, want %q", loaded.State, session.StateFailed)
	}
	if stop, ok := loaded.StopReason(); !ok || stop != session.StopError {
		t.Fatalf("rejected provider session stop = %q, %t; want %q, true", stop, ok, session.StopError)
	}
	snapshotJSON, err := json.Marshal(loaded)
	if err != nil {
		t.Fatalf("marshal loaded session: %v", err)
	}
	addArtifact("loaded session", snapshotJSON)
	eventCount := 0
	sawSanitizedTerminal := false
	for ev, readEventErr := range store.Read(ctx, sess.ID) {
		if readEventErr != nil {
			t.Fatalf("read durable event: %v", readEventErr)
		}
		eventJSON, marshalErr := json.Marshal(ev)
		if marshalErr != nil {
			t.Fatalf("marshal durable event: %v", marshalErr)
		}
		addArtifact(fmt.Sprintf("durable event[%d]", eventCount), eventJSON)
		if ev.Type == session.EvResult && ev.Result != nil {
			if ev.Result.Stop != session.StopError {
				t.Errorf("rejected provider terminal stop = %q, want %q", ev.Result.Stop, session.StopError)
			}
			for _, want := range []string{"manual access token was rejected", "auth.yaml", "restart"} {
				if !strings.Contains(ev.Result.Error, want) {
					t.Errorf("rejected provider terminal error %q missing %q", ev.Result.Error, want)
				}
			}
			if !strings.Contains(ev.Result.Error, sentinel) {
				sawSanitizedTerminal = true
			}
		}
		eventCount++
	}
	if eventCount == 0 {
		t.Fatal("rejected request produced no durable events")
	}
	if !sawSanitizedTerminal {
		t.Fatal("rejected provider run produced no sanitized terminal error event")
	}
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("read store directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, readFileErr := os.ReadFile(filepath.Join(storeDir, entry.Name()))
		if readFileErr != nil {
			t.Fatalf("read persisted artifact %q: %v", entry.Name(), readFileErr)
		}
		addArtifact("persisted "+entry.Name(), body)
	}

	policy, err := openaicodex.NewRequestPolicy(credential, func() time.Time { return codexRegistryNow }, rejecting)
	if err != nil {
		t.Fatalf("NewRequestPolicy: %v", err)
	}
	entry := newOpenAICompatEntry(Config{LLMMaxAttempts: 1}, providerOpenAICodex, "policy-owned", openaicodex.BaseURL,
		codexPolicyOptions(policy)...)
	_, rejectedErr := collectCodexChunks(ctx, t, entry.provider, []session.Message{session.NewUserMessage("reject offline")})
	if rejectedErr == nil {
		t.Fatal("offline 401 request returned nil error")
	}
	addArtifact("rejected request error", []byte(rejectedErr.Error()))

	requests := rejecting.requests()
	if len(requests) < 3 {
		t.Fatalf("captured provider requests = %d, want models plus two rejected Responses calls", len(requests))
	}
	var sawModels, sawResponses bool
	for i, req := range requests {
		if got, want := req.header.Get("Authorization"), "Bearer "+token; got != want {
			t.Errorf("provider request[%d] did not carry the exact permitted bearer boundary", i)
		}
		switch req.path {
		case "/backend-api/codex/models":
			if req.method != http.MethodGet {
				t.Errorf("Codex models request method = %q, want GET", req.method)
			}
			sawModels = true
		case "/backend-api/codex/responses":
			if req.method != http.MethodPost {
				t.Errorf("Codex Responses request method = %q, want POST", req.method)
			}
			sawResponses = true
		default:
			t.Errorf("provider request[%d] escaped permitted Codex paths: %q", i, req.path)
		}
		headersWithoutAuthorization := req.header.Clone()
		headersWithoutAuthorization.Del("Authorization")
		headerJSON, marshalErr := json.Marshal(headersWithoutAuthorization)
		if marshalErr != nil {
			t.Fatalf("marshal provider request headers: %v", marshalErr)
		}
		addArtifact(fmt.Sprintf("provider request headers except Authorization[%d]", i), headerJSON)
		addArtifact(fmt.Sprintf("provider request body[%d]", i), req.body)
	}
	if !sawModels || !sawResponses {
		t.Fatalf("provider boundary coverage = models:%t responses:%t, want both", sawModels, sawResponses)
	}

	hookEvents := hooks.events()
	if len(hookEvents) == 0 {
		t.Fatal("bearer-configured provider run invoked no lifecycle hooks")
	}
	seenHookPhases := map[governance.HookPhase]bool{}
	for i, ev := range hookEvents {
		seenHookPhases[ev.Phase] = true
		hookJSON, marshalErr := json.Marshal(ev)
		if marshalErr != nil {
			t.Fatalf("marshal hook event: %v", marshalErr)
		}
		addArtifact(fmt.Sprintf("hook event[%d]", i), hookJSON)
	}
	for _, phase := range []governance.HookPhase{governance.PhaseSessionStart, governance.PhaseUserPromptSubmit, governance.PhaseStop} {
		if !seenHookPhases[phase] {
			t.Errorf("bearer-configured provider run omitted %s hook", phase)
		}
	}

	// Capture diagnostics only after every rejected request and terminal hook so
	// late-path records participate in the same sentinel scan.
	for i, record := range diag.capturedStrings() {
		addArtifact(fmt.Sprintf("diagnostic[%d]", i), []byte(record))
	}

	for _, artifact := range artifacts {
		if strings.Contains(artifact.value, sentinel) {
			t.Errorf("manual bearer sentinel leaked into %s: %q", artifact.name, artifact.value)
		}
	}
}

type codexRejectingTransport struct {
	mu       sync.Mutex
	captured []codexBoundaryRequest
}

type codexBoundaryRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

func (t *codexRejectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
	}
	t.mu.Lock()
	t.captured = append(t.captured, codexBoundaryRequest{
		method: req.Method, path: req.URL.Path, header: req.Header.Clone(), body: append([]byte(nil), body...),
	})
	t.mu.Unlock()
	return codexHTTPResponse(req, http.StatusUnauthorized, "application/json", `{"error":{"message":"manual token rejected"}}`), nil
}

func (t *codexRejectingTransport) requests() []codexBoundaryRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]codexBoundaryRequest, len(t.captured))
	for i, req := range t.captured {
		out[i] = codexBoundaryRequest{
			method: req.method, path: req.path, header: req.header.Clone(), body: append([]byte(nil), req.body...),
		}
	}
	return out
}

type codexSentinelHookCapture struct {
	mu       sync.Mutex
	captured []governance.HookEvent
}

func (h *codexSentinelHookCapture) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.captured = append(h.captured, ev)
	return governance.HookOutcome{}, nil
}

func (h *codexSentinelHookCapture) events() []governance.HookEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]governance.HookEvent(nil), h.captured...)
}

func codexSentinelCredential(t *testing.T, sentinel string) (openaicodex.Credential, string) {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims, err := json.Marshal(map[string]any{
		"exp":                         codexRegistryNow.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-step8-sentinel"},
	})
	if err != nil {
		t.Fatalf("marshal sentinel claims: %v", err)
	}
	token := header + "." + base64.RawURLEncoding.EncodeToString(claims) + "." + sentinel
	credential, err := openaicodex.NewCredential(token, "", "", codexRegistryNow)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	return credential, token
}
