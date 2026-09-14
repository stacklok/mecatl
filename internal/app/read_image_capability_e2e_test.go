package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestReadImageUsesResolvedLiveModalities(t *testing.T) {
	const (
		unknownModel  = "gateway/metadata-omitted"
		textOnlyModel = "gateway/text-only"
		visionModel   = "gateway/vision"
		emptyModel    = "gateway/explicit-empty"
		genericModel  = "custom-metadata-omitted"
	)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "pixel.png"), positivePNG, 0o600); err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		bodies = map[string][][]byte{}
	)
	completed := "event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		var envelope struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		bodies[envelope.Model] = append(bodies[envelope.Model], append([]byte(nil), raw...))
		call := len(bodies[envelope.Model])
		mu.Unlock()

		var stream string
		if call == 1 {
			stream = "event: response.output_item.done\n" +
				`data: {"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"item_read","type":"function_call","status":"completed","name":"Read","arguments":"{\"path\":\"pixel.png\"}","call_id":"call_read"}}` + "\n\n" + completed
		} else {
			stream = "event: response.output_text.delta\n" +
				`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"done"}` + "\n\n" + completed
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer httpServer.Close()

	liveClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"` + unknownModel + `"},{"id":"` + textOnlyModel + `","architecture":{"input_modalities":["text"]}},{"id":"` + visionModel + `","architecture":{"input_modalities":["text","image"]}},{"id":"` + emptyModel + `","architecture":{"input_modalities":[]}}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	built, err := Build(context.Background(), Config{
		Workspace:       workspace,
		NoSoul:          true,
		DefaultProvider: providerOpenRouter,
		ProviderOverrides: permconfig.ProviderOverrides{
			providerOpenRouter: {BaseURL: httpServer.URL + "/v1"},
		},
		envDetector:          fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk-test"}),
		liveModelHTTPClient:  liveClient,
		liveModelRefreshSync: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	runRead := func(b *Built, providerID, model string) session.SessionID {
		t.Helper()
		sess, err := b.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, defaultLimits(), server.ProviderSelector{ProviderID: providerID, ModelID: model})
		if err != nil {
			t.Fatalf("create %s/%s: %v", providerID, model, err)
		}
		run, err := b.Service.StartRun(context.Background(), sess.ID, "read pixel.png")
		if err != nil {
			t.Fatalf("start %s/%s: %v", providerID, model, err)
		}
		for ev := range run.Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
			}
		}
		b.Service.FinishRun(sess.ID, run)
		return sess.ID
	}
	openRouterSessions := make(map[string]session.SessionID)
	for _, model := range []string{unknownModel, textOnlyModel, visionModel, emptyModel} {
		openRouterSessions[model] = runRead(built, providerOpenRouter, model)
	}

	harness := server.NewHarnessServer(built.Service)
	assertSnapshotImage := func(id session.SessionID, want bool) {
		t.Helper()
		resp, getErr := harness.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: string(id)})
		if getErr != nil {
			t.Fatalf("GetSession %s: %v", id, getErr)
		}
		if got := resp.GetSession().GetSessionCapabilities().GetImage(); got != want {
			t.Fatalf("GetSession %s image = %t, want %t", id, got, want)
		}
	}
	clearID, err := built.Service.ClearSessionSuccessor(context.Background(), openRouterSessions[textOnlyModel], server.SuccessorPlacement{})
	if err != nil {
		t.Fatalf("clear text-only session: %v", err)
	}
	assertSnapshotImage(clearID, false)
	forkID, err := built.Service.ForkSessionSuccessor(context.Background(), server.ForkSuccessorRequest{
		Source: openRouterSessions[textOnlyModel], ProviderID: providerOpenRouter, ModelID: visionModel,
	})
	if err != nil {
		t.Fatalf("fork onto vision model: %v", err)
	}
	assertSnapshotImage(forkID, true)

	genericLiveClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"` + genericModel + `"}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	genericCfg := Config{
		Workspace: workspace, StoreDir: t.TempDir(), MemoryDir: t.TempDir(), NoSoul: true, DefaultProvider: "gateway", DefaultModel: genericModel,
		ProviderDefinitions: map[string]permconfig.ProviderDefinition{
			"gateway": {ID: "gateway", BaseURL: httpServer.URL + "/v1", DefaultModel: genericModel, APIFlavor: "openai-responses", Auth: permconfig.ProviderAuth{Method: "api_key"}},
		},
		CustomProviderAPIKeys: map[string]string{"gateway": "sk-test"},
		liveModelHTTPClient:   genericLiveClient, liveModelRefreshSync: true,
	}
	generic, err := Build(context.Background(), genericCfg)
	if err != nil {
		t.Fatalf("Build generic gateway: %v", err)
	}
	genericID := runRead(generic, "gateway", genericModel)
	generic.Close()
	restarted, err := Build(context.Background(), genericCfg)
	if err != nil {
		t.Fatalf("restart generic gateway: %v", err)
	}
	defer restarted.Close()
	resp, err := server.NewHarnessServer(restarted.Service).GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: string(genericID)})
	if err != nil {
		t.Fatalf("GetSession after restart: %v", err)
	}
	if !resp.GetSession().GetSessionCapabilities().GetImage() {
		t.Fatal("composition-backed resolver lost generic gateway adapter image capability after restart")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, model := range []string{unknownModel, textOnlyModel, visionModel, emptyModel, genericModel} {
		if len(bodies[model]) != 2 {
			t.Fatalf("%s request count = %d, want 2", model, len(bodies[model]))
		}
	}
	for _, model := range []string{unknownModel, visionModel, genericModel} {
		if !strings.Contains(string(bodies[model][1]), "input_image") {
			t.Fatalf("%s request dropped Read image: %s", model, bodies[model][1])
		}
	}
	for _, model := range []string{textOnlyModel, emptyModel} {
		if strings.Contains(string(bodies[model][1]), "input_image") {
			t.Fatalf("%s request included Read image: %s", model, bodies[model][1])
		}
	}
}
