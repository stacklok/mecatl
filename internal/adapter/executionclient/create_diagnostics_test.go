package executionclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type pendingCreateBackend struct {
	integrationBackend
	calls     atomic.Int64
	waiting   chan struct{}
	release   chan struct{}
	retryable bool
}

func (b *pendingCreateBackend) Attach(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding string) (executioncontroller.Allocation, error) {
	if b.calls.Add(1) <= 3 {
		return executioncontroller.Allocation{}, &executionenv.Error{Code: executionenv.CodeNotReady, Retryable: b.retryable, Message: "PRIVATE-ERROR-SENTINEL"}
	}
	close(b.waiting)
	select {
	case <-b.release:
		return b.integrationBackend.Attach(ctx, ref, client, owner, binding)
	case <-ctx.Done():
		return executioncontroller.Allocation{}, ctx.Err()
	}
}

func TestRemoteCreateAttachPollingDiagnosticsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name              string
		retryable, cancel bool
	}{
		{"ready", true, false}, {"not-ready-nonretryable", false, false}, {"cancel", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &pendingCreateBackend{waiting: make(chan struct{}), release: make(chan struct{}), retryable: tc.retryable}
			fx := startFixture(t, backend, nil)
			defer fx.stop()
			client, err := New(fx.endpoint, fx.clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			provider, err := NewProvider(client, "coding")
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			store := memstore.New()
			engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Model: "offline"})
			svc, err := server.NewService(server.Config{
				Engine: engine, Store: store, PlacementProvider: provider, ExecutionAccess: provider,
				PlacementScope: "fixture", NewID: func() session.SessionID { return "fixed" },
				Diagnostics: slogdiag.New(&logs, true, port.LevelDebug),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ctx = session.WithPrincipal(ctx, &session.Principal{Issuer: "PRIVATE-ISSUER", Subject: "PRIVATE-OWNER", GrantType: session.GrantTypeUser})
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`)).WithContext(ctx)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { server.NewHTTPHandler(svc).ServeHTTP(response, req); close(done) }()
			select {
			case <-backend.waiting:
			case <-ctx.Done():
				t.Fatal("Attach polling did not reach gated call")
			}
			if tc.cancel {
				cancel()
			} else {
				close(backend.release)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("create did not return after release/cancel")
			}
			_, loadErr := store.Load(t.Context(), "fixed")
			if tc.cancel {
				if !errors.Is(loadErr, port.ErrSessionNotFound) || response.Code == http.StatusCreated {
					t.Fatal("cancelled create persisted")
				}
			} else if loadErr != nil || response.Code != http.StatusCreated {
				t.Fatalf("ready create status=%d error=%v", response.Code, loadErr)
			}
			if strings.Contains(logs.String(), "PRIVATE") || strings.Contains(logs.String(), "env-real") || strings.Contains(logs.String(), "/workspace") {
				t.Fatal("private data leaked")
			}
			reasons := map[string]int{}
			stages := map[string]bool{}
			var finalCalls float64
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var row map[string]any
				if err := json.Unmarshal([]byte(line), &row); err != nil {
					t.Fatal(err)
				}
				if row["msg"] != "remote create stage" {
					continue
				}
				stage, _ := row["stage"].(string)
				stages[stage] = true
				if stage == "attach_poll" {
					reasons[row["reason"].(string)]++
				}
				if stage == "attach_poll_end" {
					finalCalls = row["calls"].(float64)
				}
			}
			reason := "not_ready_retryable"
			if !tc.retryable {
				reason = "not_ready_nonretryable"
			}
			if reasons[reason] != 1 || finalCalls != 4 {
				t.Fatalf("polling not bounded/discriminating: reasons=%v calls=%v", reasons, finalCalls)
			}
			for _, stage := range []string{"http_handler", "session_id_probe", "bind_ensure", "attach_poll", "attach_poll_end"} {
				if !stages[stage] {
					t.Errorf("missing stage %s", stage)
				}
			}
			if !tc.cancel && (!stages["session_persist"] || !stages["reference_commit"] || !stages["http_response"]) {
				t.Fatal("missing persistence/commit/response stages")
			}
			if tc.cancel && (stages["session_persist"] || stages["reference_commit"]) {
				t.Fatal("cancel reached publication")
			}
		})
	}
}
