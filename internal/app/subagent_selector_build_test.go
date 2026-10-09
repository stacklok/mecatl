package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestBuildWritableSubagentCrossProviderSelection drives the generic Subagent
// through the real Build service. The child selector must mint an openrouter
// engine rather than reuse the openai parent engine, even for direct-write work.
func TestBuildWritableSubagentCrossProviderSelection(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const childModel = "anthropic/claude-sonnet-4.5"

	var (
		mu       sync.Mutex
		childReq []port.LLMRequest
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace:     workspace,
		NoSoul:        true,
		AllowAllTools: true,
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     "sk-test",
			"OPENROUTER_API_KEY": "sk-test",
		}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			if id == providerOpenRouter {
				return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
					mu.Lock()
					childReq = append(childReq, req)
					mu.Unlock()
				})},
					mockllm.ToolCallTurn(session.NewToolCall("write", "Write", []byte(`{"path":"child.txt","content":"written by selected provider\n"}`))),
					mockllm.TextTurn("child complete"),
				)
			}
			return mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"write child.txt","mode":"read-write","provider":"openrouter","model":"`+childModel+`"}`))),
				mockllm.TextTurn("parent complete"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "delegate")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent complete" {
		t.Fatalf("run result = %q, want parent complete", got)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "child.txt")); err != nil || string(got) != "written by selected provider\n" {
		t.Fatalf("selected writable child did not write the parent workspace: content=%q err=%v", got, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(childReq) != 2 {
		t.Fatalf("openrouter child requests = %d, want 2 (Write call and final response)", len(childReq))
	}
	for i, req := range childReq {
		if req.Model != childModel {
			t.Errorf("openrouter child request %d model = %q, want %q", i, req.Model, childModel)
		}
		if !strings.Contains(req.System.Render(), "model: "+childModel) {
			t.Errorf("openrouter child request %d system prompt is not bound to selected model %q", i, childModel)
		}
	}
}

// TestBuildNoFSSubagentCrossProviderSelection proves generic Subagent selection
// remains provider-aware in a no-FS profile while the selected child's model
// request still receives the file-less catalog.
func TestBuildNoFSSubagentCrossProviderSelection(t *testing.T) {
	ctx := context.Background()
	const childModel = "anthropic/claude-sonnet-4.5"

	var (
		mu       sync.Mutex
		childReq []port.LLMRequest
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace: t.TempDir(),
		MemoryDir: t.TempDir(),
		NoSoul:    true,
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     "sk-test",
			"OPENROUTER_API_KEY": "sk-test",
		}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			if id == providerOpenRouter {
				return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
					mu.Lock()
					childReq = append(childReq, req)
					mu.Unlock()
				})},
					mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"file_path":"main.go"}`)}),
					mockllm.TextTurn("file-less child complete"),
				)
			}
			return mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"inspect without files","provider":"openrouter","model":"`+childModel+`"}`))),
				mockllm.TextTurn("parent complete"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, defaultLimits(), server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile(no-fs): %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "delegate")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent complete" {
		t.Fatalf("run result = %q, want parent complete", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(childReq) != 2 {
		t.Fatalf("openrouter child requests = %d, want 2 (Read attempt and final response)", len(childReq))
	}
	for i, req := range childReq {
		if req.Model != childModel {
			t.Errorf("openrouter child request %d model = %q, want %q", i, req.Model, childModel)
		}
		if !strings.Contains(req.System.Render(), "model: "+childModel) {
			t.Errorf("openrouter child request %d system prompt is not bound to selected model %q", i, childModel)
		}
		for _, spec := range req.Tools {
			if spec.Name == "Read" || spec.Name == "Write" || spec.Name == "Shell" {
				t.Errorf("no-FS selected child request %d offers %q", i, spec.Name)
			}
		}
	}
}
