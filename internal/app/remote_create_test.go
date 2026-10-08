package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestRemoteHTTPCreateRealOpenRouterDoesNotWaitForDiscoveryOrInfer(t *testing.T) {
	fakeRulesEnv(t, t.TempDir(), t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce, startedOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	live := &http.Client{Transport: nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})}
	var inference atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		inference.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer endpoint.Close()
	ref := session.EnvironmentRef{Kind: "kubernetes", ID: "private-ref-sentinel", Revision: "private-revision-sentinel"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/workspace"), memledger.New(), nil)
	diag := &attrCapturingDiag{}
	binds := 0
	built, err := buildIsolated(t, ctx, Config{
		// No selector, no launch root and no mock: /workspace must use the real
		// per-session factory, exactly like the native live create request.
		RemoteExecution: true, PlacementScope: "remote", PlacementProvider: remoteFactoryPlacement{env: env, binds: &binds},
		DefaultProvider: "openrouter", Model: "anthropic/claude-haiku-4.5", NoSoul: true, NoUserModel: true,
		OpenRouterKey: "synthetic-offline-key", envDetector: fakeEnv(nil), Diagnostics: diag,
		ProviderOverrides:   permconfig.ProviderOverrides{providerOpenRouter: {BaseURL: endpoint.URL + "/v1"}},
		liveModelHTTPClient: live,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	// Release discovery before closing Build, including the failure path.
	defer unblock()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("actual registry did not start live discovery")
	}
	principal := &session.Principal{Issuer: "private-issuer-sentinel", Subject: "private-owner-sentinel", GrantType: session.GrantTypeUser}
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"mode":"default","limits":{"max_turns":8,"max_tool_calls":20,"max_consecutive_failures":3}}`))
	req = req.WithContext(session.WithPrincipal(ctx, principal))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { server.NewHTTPHandler(built.Service).ServeHTTP(response, req); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("create did not unwind after discovery release and cancellation")
		}
		t.Fatalf("create blocked behind discovery; stages: %s", diag.dump())
	}
	if response.Code != http.StatusCreated || binds != 1 {
		t.Fatalf("create status=%d binds=%d", response.Code, binds)
	}
	var result struct {
		ID    string `json:"session_id"`
		Model struct {
			Provider string `json:"provider_id"`
		} `json:"resolved_model"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Model.Provider != "openrouter" || result.ID == "" {
		t.Fatal("not a real OpenRouter session response")
	}
	persisted, err := built.Service.GetSession(req.Context(), session.SessionID(result.ID))
	if err != nil || persisted.EnvironmentRef != ref || len(persisted.Conversation.Messages) != 0 || inference.Load() != 0 {
		t.Fatal("create inferred, recorded a prompt, or lost exact placement")
	}
	stages := diag.dump()
	for _, contract := range []string{"stage http_handler reason begin", "stage session_id_probe reason ok", "stage engine_factory reason ok", "stage session_persist reason ok", "stage http_response reason ok"} {
		if !strings.Contains(stages, contract) {
			t.Errorf("actual built factory missing diagnostic %q", contract)
		}
	}
	for _, line := range strings.Split(stages, "\n") {
		if strings.Contains(line, "remote create stage") && (strings.Contains(line, "sentinel") || strings.Contains(line, "/workspace") || strings.Contains(line, "synthetic-offline-key")) {
			t.Fatal("private create data reached diagnostics")
		}
	}
}
