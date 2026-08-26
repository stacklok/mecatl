package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestListenerScopedWorkspaceAuthority_Scenario2_LocalClientWorkspacePreserved(t *testing.T) {
	root := t.TempDir()
	selected := t.TempDir()
	built, err := Build(context.Background(), Config{
		Workspace:           root,
		WorkspaceAuthority:  server.WorkspaceAuthorityClientSelected,
		NoSoul:              true,
		MemoryDir:           t.TempDir(),
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-test"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(Config, string, string, string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("done"))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), selected, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession(%q): %v", selected, err)
	}
	if sess.Workspace != selected {
		t.Fatalf("session workspace = %q, want client-selected %q", sess.Workspace, selected)
	}
}

func TestADR_0032_WorktreeBindingRemainsClientSelectableOnly(t *testing.T) {
	root := t.TempDir()
	built, err := Build(context.Background(), Config{
		Workspace:              root,
		WorkspaceAuthority:     server.WorkspaceAuthorityServerAssigned,
		AuthoritativeWorkspace: root,
		NoSoul:                 true,
		MemoryDir:              t.TempDir(),
		envDetector:            fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-test"}),
		liveModelHTTPClient:    offlineHTTPClient(),
		providerConstructor: func(Config, string, string, string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("done"))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	worktrees, err := built.Service.ListWorktrees(context.Background(), "")
	if err != nil {
		t.Fatalf("ListWorktrees(empty): %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("ListWorktrees(empty) = %+v, want inert empty result for server-assigned workspace", worktrees)
	}
}
