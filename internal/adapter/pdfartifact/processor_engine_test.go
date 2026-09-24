package pdfartifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type artifactResultTool struct{ result session.ToolResult }

func (artifactResultTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "PDF", Description: "returns a PDF", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (artifactResultTool) ReadOnly() bool { return true }
func (t artifactResultTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	result := t.result
	result.CallID = call.ID
	return result, nil
}

type artifactResultAudit struct {
	mu     sync.Mutex
	result session.ToolResult
}

func (a *artifactResultAudit) ToolCall(_ session.SessionID, _ session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	a.mu.Lock()
	a.result = result
	a.mu.Unlock()
}
func (a *artifactResultAudit) last() session.ToolResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result
}

type removePDFHook struct{}

func (removePDFHook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePostToolUse {
		return governance.HookOutcome{Mutated: json.RawMessage(`{"content":"hook removed PDF"}`)}, nil
	}
	return governance.HookOutcome{}, nil
}

func runArtifactResult(t *testing.T, processor port.ToolResultProcessor, result session.ToolResult, hooks port.HookRunner) (session.ToolResult, session.ToolResult, session.ToolResult, session.ToolResult) {
	t.Helper()
	var modelResult session.ToolResult
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		for _, message := range req.Messages {
			if message.ToolResult != nil {
				modelResult = *message.ToolResult
			}
		}
	})}, mockllm.ToolCallTurn(session.NewToolCall("call-1", "PDF", json.RawMessage(`{}`))), mockllm.TextTurn("done"))
	catalog := tool.NewCatalog()
	catalog.MustRegister(artifactResultTool{result: result})
	audit := &artifactResultAudit{}
	engine := agent.NewEngine(agent.Deps{LLM: provider, Catalog: catalog, Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Hooks: hooks, ToolResultProcessor: processor, ToolCallRecorder: audit, Model: "test-model"})
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	sess := session.New("pdf-result-owner", session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	var eventResult session.ToolResult
	for ev := range engine.Run(t.Context(), sess, env, agent.RunRequest{Text: "make PDF"}).Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			eventResult = *ev.ToolResult
		}
	}
	var recorded session.ToolResult
	for _, message := range sess.Conversation.Messages {
		if message.ToolResult != nil {
			recorded = *message.ToolResult
		}
	}
	return eventResult, recorded, audit.last(), modelResult
}

func assertArtifactViews(t *testing.T, pdf []byte, views ...session.ToolResult) {
	t.Helper()
	digest := sha256.Sum256(pdf)
	wantSHA256 := hex.EncodeToString(digest[:])
	encoded := base64.StdEncoding.EncodeToString(pdf)
	var id string
	for index, view := range views {
		if view.CallID != "call-1" || view.IsError || len(view.Parts) != 3 || view.Parts[1].BlockKind != session.BlockPDFArtifact {
			t.Fatalf("view %d = %+v", index, view)
		}
		block := view.Parts[1]
		if index == 0 {
			id = block.ArtifactID
		}
		if id == "" || block.ArtifactID != id || block.Name != "artifact.pdf" || block.Size != int64(len(pdf)) ||
			block.SHA256 != wantSHA256 || block.MIMEType != "application/pdf" || len(block.Data) != 0 || block.URL != "" ||
			bytes.Contains([]byte(view.Content), pdf) || strings.Contains(view.Content, encoded) {
			t.Fatalf("view %d leaked or changed PDF metadata: %+v", index, view)
		}
	}
}

func assertPDFErrorViews(t *testing.T, pdf []byte, views ...session.ToolResult) {
	t.Helper()
	for index, view := range views {
		if view.CallID != "call-1" || !view.IsError || len(view.Parts) != 0 || len(view.Content) > 100 ||
			strings.Contains(view.Content, string(pdf)) || strings.Contains(view.Content, base64.StdEncoding.EncodeToString(pdf)) {
			t.Fatalf("view %d leaked failed PDF: %+v", index, view)
		}
	}
}
