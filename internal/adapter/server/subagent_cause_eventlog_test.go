package server_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestEventLogRecordsSubagentFailureCause is issue #319's DURABLE-LOG acceptance: after a
// delegation dies on a provider failure, an operator must be able to recover WHY from the
// parent session's persisted `.events.jsonl` ALONE — no live process, no child transcript.
//
// The proof is deliberately restart-shaped: the run is driven through a Service backed by
// a jsonlstore EventLog, then the log is re-opened as a FRESH jsonlstore over the same
// directory and read back. Recovering the cause from that read is what "recoverable from
// the parent's .events.jsonl alone" means.
//
// MUTATION-KILL: dropping drainChildObserved's `cause = ev.Result.Error` read (or the
// `Cause:` field from the EvSubagentEnd emit) empties the persisted cause and the
// assertion below fails.
func TestEventLogRecordsSubagentFailureCause(t *testing.T) {
	const causeText = "upstream 503: model overloaded"
	dir := t.TempDir()

	store, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}

	// The child dies on a mid-stream provider error after a first chunk — the terminal,
	// never-retried shape a real broken stream takes.
	childEngine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ErrorTurn(errors.New(causeText), mockllm.TextChunk("thinking"))),
		Catalog: tool.NewCatalog(),
		Model:   "child-model",
	})
	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewSubagentTool(childEngine))
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(call("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	engine := agent.NewEngine(agent.Deps{LLM: parentLLM, Catalog: cat,
		// Allow-all so the delegation runs unprompted: the ask path is not what this test
		// is about (see eventlog_test.go for the approval-lifecycle gates).
		Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil),
		Model:  "test-model"})

	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  store,

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: parentLLM.Capabilities(),
		EventLog:            store,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	for {
		resp, rerr := stream.Recv()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			t.Fatalf("Recv: %v", rerr)
		}
		if resp.GetEvent().GetType() == "result" {
			_ = stream.CloseSend()
		}
	}

	// "Restart": a FRESH store over the same directory, so the read comes off the
	// persisted `.events.jsonl` sidecar and nothing in memory.
	reopened, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore reopen: %v", err)
	}
	logged := readEventLog(t, reopened, sess.ID)

	ends := 0
	cause := ""
	for _, ev := range logged {
		if ev.Type == session.EvSubagentEnd && ev.Subagent != nil {
			ends++
			cause = ev.Subagent.Cause
		}
	}
	if ends != 1 {
		t.Fatalf("want exactly one persisted subagent.end, got %d (events: %v)", ends, typeNames(logged))
	}
	if !strings.Contains(cause, causeText) {
		t.Fatalf("the persisted subagent.end must carry the failure cause %q, got %q", causeText, cause)
	}
}
