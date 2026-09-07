package llmresilience

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	openaiadapter "github.com/stacklok/mecatl/provider/openai"
)

func TestADR_0302_VisibleMultipartFailureIsTerminalWithoutReplayOrPersistence(t *testing.T) {
	t.Run("visible multipart failure", func(t *testing.T) {
		body, err := os.ReadFile(filepath.Join("testdata", "openai_visible_multipart_failure.sse"))
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		var requests atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(body)
		}))
		defer srv.Close()

		provider := Wrap(openaiadapter.New(
			openaiadapter.WithAPIKey("test-key"),
			openaiadapter.WithBaseURL(srv.URL+"/v1"),
			openaiadapter.WithRequestOption(option.WithMaxRetries(0)),
		), Config{MaxAttempts: 3, BaseBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond})
		sess := session.New("multipart-failure", session.ModeDefault,
			session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"},
			session.Limits{}, time.Unix(0, 0))
		eng := agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: "gpt-test"})
		workspace := memfs.NewWorkspace("/ws")
		environment := tool.MustEnvironment(sess.EnvironmentRef, workspace, memledger.New(), nil)
		run := eng.Run(context.Background(), sess, environment, agent.RunRequest{Text: "go"})

		var visible strings.Builder
		var result *session.ResultPayload
		for event := range run.Events() {
			if event.Type == session.EvMessageDelta {
				visible.WriteString(event.Text)
			}
			if event.Type == session.EvResult {
				result = event.Result
			}
		}
		if got := visible.String(); got != "first-second" {
			t.Fatalf("visible deltas = %q, want SSE-order concatenation %q", got, "first-second")
		}
		if got := requests.Load(); got != 1 {
			t.Fatalf("HTTP requests = %d, want 1 after visible commit", got)
		}
		if result == nil || result.Stop != session.StopError ||
			result.Disposition != session.RetryDispositionRetryable || result.Progress != session.StreamProgressVisible {
			t.Fatalf("terminal result = %+v, want retryable/visible StopError", result)
		}
		for _, message := range sess.Conversation.Messages {
			if message.Role == session.RoleAssistant {
				t.Fatalf("incomplete assistant was persisted: %+v", message)
			}
		}
	})

	t.Run("precommit failure remains retryable", func(t *testing.T) {
		var requests atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if requests.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"code":"server_error","message":"retry"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w,
				"event: response.output_text.delta\n"+
					`data: {"type":"response.output_text.delta","sequence_number":0,"item_id":"msg_2","output_index":0,"content_index":0,"delta":"recovered"}`+"\n\n"+
					"event: response.completed\n"+
					`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_2","status":"completed","usage":{"input_tokens":1,"input_tokens_details":{},"output_tokens":1,"output_tokens_details":{},"total_tokens":2}}}`+"\n\n")
		}))
		defer srv.Close()

		provider := Wrap(openaiadapter.New(
			openaiadapter.WithAPIKey("test-key"),
			openaiadapter.WithBaseURL(srv.URL+"/v1"),
			openaiadapter.WithRequestOption(option.WithMaxRetries(0)),
		), Config{MaxAttempts: 2, BaseBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond})
		seq, err := provider.Stream(context.Background(), port.LLMRequest{Model: "gpt-test"})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		chunks, err := drain(t, seq)
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		if got := requests.Load(); got != 2 {
			t.Fatalf("HTTP requests = %d, want precommit retry", got)
		}
		if len(chunks) != 3 || chunks[0].Kind != port.ChunkText || chunks[0].Text != "recovered" || chunks[2].Kind != port.ChunkDone {
			t.Fatalf("retry chunks = %+v, want recovered completed turn", chunks)
		}
	})
}
