package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestBuildBindsServerOwnedDefaultPlacement(t *testing.T) {
	root := t.TempDir()
	selected := t.TempDir()
	built, err := Build(context.Background(), Config{
		Workspace: root, NoSoul: true, MemoryDir: t.TempDir(),
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

	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.EnvironmentRef.ID != root || sess.EnvironmentRef.ID == selected {
		t.Fatalf("session placement = %+v, want exact private server-owned root", sess.EnvironmentRef)
	}
}
