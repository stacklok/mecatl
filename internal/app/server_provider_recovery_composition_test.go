package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

func TestServerProviderRecovery_Scenario1_RootAndDirectWriteChildRecoverBeforeTerminal(t *testing.T) {
	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "alpha.txt", "alpha\n")
	gitCommitTest(t, repo, "initial")

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch call {
		case 1:
			writeRecoveryToolTurn(w, "write-1", "Write", `{"path":"beta.txt","content":"written once\n"}`)
		case 2, 4:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"server_error","message":"unavailable"}}`)
		case 3:
			writeRecoveryTextTurn(w, "child recovered")
		case 5:
			writeRecoveryTextTurn(w, "root recovered")
		default:
			t.Errorf("unexpected provider call %d", call)
			writeRecoveryTextTurn(w, "unexpected")
		}
	}))
	defer srv.Close()

	cfg := teamCfg(t)
	cfg.Workspace = repo
	cfg.Model = "test-model"
	cfg.LLMMaxAttempts = 3
	cfg.LLMRecoveryBudget = 2 * time.Second
	cfg.LLMBreakerThreshold = 1
	cfg.LLMBreakerCooldown = 500 * time.Millisecond
	entry := newOpenAICompatEntry(cfg, providerOpenAI, "test", srv.URL+"/v1")
	task, closeFn := buildSubagentTool(context.Background(), cfg,
		regForTest(entry.provider, providerOpenAI, cfg.Model), entry.provider, providerOpenAI, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, catalogAssets{}, false)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}
	res, err := task.Execute(t.Context(), session.NewToolCall("parent-call", "Subagent", []byte(`{"prompt":"write beta","mode":"read-write"}`)), testEnvironment(osfsWSForTest(t, repo), buildCommandRunner(cfg)))
	if err != nil || res.IsError {
		t.Fatalf("direct-write child failed before recovery: result=%+v err=%v", res, err)
	}
	body, err := os.ReadFile(filepath.Join(repo, "beta.txt"))
	if err != nil || string(body) != "written once\n" {
		t.Fatalf("prior child write = %q, %v", body, err)
	}

	seq, err := entry.provider.Stream(t.Context(), port.LLMRequest{Model: cfg.Model, Messages: []session.Message{session.NewUserMessage("root step")}})
	if err != nil {
		t.Fatal(err)
	}
	var rootText strings.Builder
	for chunk, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		if chunk.Kind == port.ChunkText {
			rootText.WriteString(chunk.Text)
		}
	}
	if rootText.String() != "root recovered" || calls.Load() != 5 {
		t.Fatalf("root result=%q provider calls=%d, want recovered/5", &rootText, calls.Load())
	}
}

func writeRecoveryToolTurn(w http.ResponseWriter, callID, name, args string) {
	_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":%q,\"name\":%q,\"arguments\":%q}}\n\n", callID, name, args)
	_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
}

func writeRecoveryTextTurn(w http.ResponseWriter, text string) {
	_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":%q}\n\n", text)
	_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
}
