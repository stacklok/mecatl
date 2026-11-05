package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestMessagesStreamRequiresMessageStop(t *testing.T) {
	full := signedRedactedTurnSSE(1)
	for _, tc := range []struct {
		name   string
		before string
		calls  int
	}{
		{name: "empty", before: "event: message_start\n"},
		{name: "buffered-thinking", before: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":3"},
		{name: "completed-tool-block", before: "event: message_delta\n", calls: 1},
		{name: "stop-reason-without-message-stop", before: "event: message_stop\n", calls: 1},
		{name: "complete", calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := full
			if tc.before != "" {
				var found bool
				body, _, found = strings.Cut(full, tc.before)
				if !found {
					t.Fatal("fixture is missing truncation boundary")
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL))
			seq, err := p.Stream(t.Context(), port.LLMRequest{Model: "claude-sonnet-4-6", Messages: []port.LLMMessage{userMessage("read")}})
			if err != nil {
				t.Fatal(err)
			}
			var streamErr error
			calls, replay, done := 0, 0, 0
			for chunk, err := range seq {
				if err != nil {
					if streamErr != nil {
						t.Fatal("multiple stream errors")
					}
					streamErr = err
					continue
				}
				switch chunk.Kind {
				case port.ChunkToolCall:
					calls++
				case port.ChunkReplay:
					replay++
				case port.ChunkDone:
					done++
				}
			}
			if calls != tc.calls {
				t.Fatalf("tentative calls=%d, want %d", calls, tc.calls)
			}
			if tc.before == "" {
				if streamErr != nil || replay != 1 || done != 1 {
					t.Fatalf("complete stream: err=%v replay=%d done=%d", streamErr, replay, done)
				}
			} else if !errors.Is(streamErr, io.ErrUnexpectedEOF) || replay != 0 || done != 0 {
				t.Fatalf("truncated stream: err=%v replay=%d done=%d", streamErr, replay, done)
			}
		})
	}
}

func TestMessagesEngineRejectsTruncatedToolTurn(t *testing.T) {
	for _, before := range []string{"event: message_delta\n", "event: message_stop\n", ""} {
		name := strings.TrimSpace(before)
		if name == "" {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			body := signedRedactedTurnSSE(1)
			if before != "" {
				var found bool
				body, _, found = strings.Cut(body, before)
				if !found {
					t.Fatal("fixture is missing truncation boundary")
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			read := &canonicalReadTool{}
			catalog := tool.NewCatalog()
			catalog.MustRegister(read)
			p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL))
			eng := agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Model: "claude-sonnet-4-6", Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "Read", Effect: governance.Allow}}, nil)})
			env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			s := session.New("truncation", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 1}, time.Unix(0, 0))
			calls, results := 0, 0
			var terminal *session.ResultPayload
			for ev := range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "read"}).Events() {
				switch ev.Type {
				case session.EvToolCall:
					calls++
				case session.EvToolResult:
					results++
				case session.EvResult:
					terminal = ev.Result
				}
			}
			if terminal == nil {
				t.Fatal("missing terminal result")
			}
			if before == "" {
				if terminal.Error != "" || len(read.calls) != 1 || calls != 1 || results != 1 {
					t.Fatalf("complete turn: result=%+v executions=%d calls=%d results=%d", terminal, len(read.calls), calls, results)
				}
				if len(s.Conversation.Messages) != 3 || s.Conversation.Messages[1].Replay == nil || s.Conversation.Messages[1].Replay.Provider == nil {
					t.Fatal("complete turn lost accepted history/private replay")
				}
				return
			}
			if terminal.Stop != session.StopError || terminal.Error == "" || len(read.calls) != 0 || calls != 0 || results != 0 {
				t.Fatalf("truncated turn: result=%+v executions=%d calls=%d results=%d", terminal, len(read.calls), calls, results)
			}
			if len(s.Conversation.Messages) != 1 || s.Conversation.Messages[0].Role != session.RoleUser {
				t.Fatalf("truncated turn entered accepted history: %+v", s.Conversation.Messages)
			}
		})
	}
}

func TestMessagesStreamCancellationIsNotTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, signedRedactedTurnSSE(1))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL))
	seq, err := p.Stream(ctx, port.LLMRequest{Model: "claude-sonnet-4-6", Messages: []port.LLMMessage{userMessage("read")}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for chunk, err := range seq {
		if err != nil {
			t.Fatalf("cancellation reported as stream error: %v", err)
		}
		switch chunk.Kind {
		case port.ChunkToolCall:
			calls++
			cancel()
		case port.ChunkReplay, port.ChunkDone:
			t.Fatal("cancelled stream emitted replay or terminal success")
		}
	}
	if calls != 1 || ctx.Err() != context.Canceled {
		t.Fatal("test did not cancel after a tentative tool call")
	}
}
