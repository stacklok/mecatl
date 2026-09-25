package executionclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type nativeContextInstructions string

func (s nativeContextInstructions) Assemble(context.Context) ([]session.Message, error) {
	return []session.Message{session.NewUserMessage(string(s))}, nil
}

type contextPublicationBackend struct {
	integrationBackend
	failCommit      bool
	commits, aborts atomic.Int64
}

func (b *contextPublicationBackend) CommitReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	b.commits.Add(1)
	if b.failCommit {
		return &executionenv.Error{Code: executionenv.CodeInternal, Message: "publication response lost"}
	}
	return nil
}

func (b *contextPublicationBackend) AbortReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	b.aborts.Add(1)
	return nil
}

func TestNativeBuildPreservesIndependentContext(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		compatibility, failCommit bool
	}{
		{name: "selected"},
		{name: "published despite commit failure", failCommit: true},
		{name: "compatibility", compatibility: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
				t.Setenv(key, t.TempDir())
			}
			backend := &contextPublicationBackend{failCommit: tc.failCommit}
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
			ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
			var requests []port.LLMRequest
			cfg := app.Config{
				UseMock: true, MockProvider: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })}, mockllm.TextTurn("done")),
				Workspace: t.TempDir(), StoreDir: t.TempDir(), UserModelDir: t.TempDir(), MemoryDir: t.TempDir(), NoSoul: true,
				RemoteExecution: true, TrustProject: true, Headless: true, EnableCommands: true, OwnershipEnforced: true,
				PlacementProvider: provider, PlacementScope: "native-test",
			}
			for name, body := range map[string]string{
				"AGENTS.md":                  "POISON_HOST_INSTRUCTIONS",
				".claude/rules/poison.md":    "POISON_HOST_RULES",
				".claude/commands/poison.md": "POISON_HOST_COMMAND",
			} {
				path := filepath.Join(cfg.Workspace, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			memoryStore, err := memory.New(cfg.MemoryDir)
			if err != nil {
				t.Fatal(err)
			}
			callerMemory := memory.NewCallerStore(memoryStore, true)
			for root, marker := range map[string]string{cfg.Workspace: "POISON_HOST_MEMORY", "/workspace": "SELECTED_REMOTE_MEMORY"} {
				memoryCtx := memory.WithWorkspace(ctx, root)
				if _, err := callerMemory.Remember(memoryCtx, tool.MemoryEntry{Key: "project/fixture", Value: marker, Description: marker}, tool.MemoryCurrent{}); err != nil {
					t.Fatal(err)
				}
				index, err := callerMemory.Index(memoryCtx)
				if err != nil || len(index) != 1 || index[0].Description != marker {
					t.Fatalf("memory fixture not readable: %v, %v", index, err)
				}
			}
			var binds, closes atomic.Int64
			if tc.compatibility {
				cfg.CommandsDir = "operator-commands"
				if err := os.MkdirAll(filepath.Join(cfg.Workspace, cfg.CommandsDir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.Workspace, cfg.CommandsDir, "selected.md"), []byte("INDEPENDENT_COMMAND_BODY"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				source := memfs.NewWorkspace("/independent-context")
				if _, err := source.CreateFile(t.Context(), "commands/selected.md", []byte("INDEPENDENT_COMMAND_BODY")); err != nil {
					t.Fatal(err)
				}
				settings := filepath.Join(t.TempDir(), "settings.yaml")
				if err := os.WriteFile(settings, []byte(`harness_context:
  enabled_sources: [independent, pvc]
  kinds:
    instructions: {sources: [independent], mode: combine}
    commands: {sources: [independent, pvc], mode: combine}
    rules: {sources: [], mode: combine}
    skills: {sources: [], mode: combine}
    agent_defs: {sources: [], mode: combine}
`), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg.PermissionConfigs = []string{settings}
				cfg.HarnessInstructionSources = []app.HarnessSourceRegistration[prompt.InstructionAssembler]{
					{ID: "independent", Provenance: app.HarnessProvenancePolicy{Fixed: "explicit"}, Bind: func(context.Context, app.HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
						return nativeContextInstructions("INDEPENDENT_INSTRUCTIONS"), nil, nil
					}},
				}
				cfg.HarnessCommandSources = []app.HarnessSourceRegistration[server.CommandSourceBinding]{
					{ID: "independent", Scope: app.HarnessSourceScopePrincipal, Provenance: app.HarnessProvenancePolicy{Fixed: "explicit"}, Bind: func(_ context.Context, scope app.HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
						if scope.SessionID == "" || scope.Principal == nil {
							t.Error("source bound without session identity")
						}
						if scope.AcquireExecutionWorkspace != nil {
							t.Error("independent source received execution workspace capability")
						}
						binds.Add(1)
						return prompt.NewDirCommandExpander(source, "commands"), func() error { closes.Add(1); return nil }, nil
					}},
					{ID: "pvc", Scope: app.HarnessSourceScopePrincipal, UsesExecutionWorkspace: true, Provenance: app.HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, app.HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
						t.Error("native execution source admitted")
						return prompt.NoopExpander{}, nil, nil
					}},
				}
			}
			built, err := app.Build(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if tc.failCommit {
				if !errors.Is(err, server.ErrInternal) || sess == nil {
					t.Fatalf("create=%v, %v; want persisted session and publication error", sess, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			stored, err := built.Service.GetSession(ctx, sess.ID)
			if err != nil || stored.EnvironmentRef != sess.EnvironmentRef {
				t.Fatalf("session not persisted: %v", err)
			}
			if backend.commits.Load() != 1 || backend.aborts.Load() != 0 || closes.Load() != 0 {
				t.Fatalf("published context retired: commits=%d aborts=%d closes=%d", backend.commits.Load(), backend.aborts.Load(), closes.Load())
			}
			commands, err := built.Service.ListCommandsForSession(ctx, sess.ID)
			if err != nil || len(commands) != 1 || commands[0].Name != "selected" {
				t.Fatalf("independent listing=%v, %v", commands, err)
			}
			if backend.ensureCalls != 1 || backend.attachCalls != 1 || backend.fileCalls != 0 {
				t.Fatalf("context discovery touched native provider: ensure=%d attach=%d files=%d", backend.ensureCalls, backend.attachCalls, backend.fileCalls)
			}
			run, err := built.Service.StartRun(ctx, sess.ID, "/selected")
			if err != nil {
				t.Fatal(err)
			}
			for range run.Events() {
			}
			built.Service.FinishRun(sess.ID, run)
			if backend.ensureCalls != 1 || backend.attachCalls != 2 || backend.fileCalls != 0 {
				t.Fatalf("run lazily touched native provider: ensure=%d attach=%d files=%d", backend.ensureCalls, backend.attachCalls, backend.fileCalls)
			}
			if len(requests) != 1 {
				t.Fatalf("model requests=%d", len(requests))
			}
			data, err := json.Marshal(requests[0])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "INDEPENDENT_COMMAND_BODY") || strings.Contains(string(data), "POISON_HOST_") {
				t.Fatal("model request lost selected command or admitted host poison")
			}
			if !tc.compatibility && !strings.Contains(string(data), "INDEPENDENT_INSTRUCTIONS") {
				t.Fatal("native factory dropped independent instructions")
			}
			if !strings.Contains(string(data), "SELECTED_REMOTE_MEMORY") {
				t.Fatal("remote memory positive control missing")
			}
			if !strings.Contains(requests[0].System.StablePrefix, "persistent remote Kubernetes workspace") || !strings.Contains(requests[0].System.StablePrefix, "explicitly selected independent commands") {
				t.Fatal("native factory lost system posture or independent command affordance")
			}
			built.Service.CloseSession(sess.ID)
			built.Close()
			if !tc.compatibility && (binds.Load() != 1 || closes.Load() != 1) {
				t.Fatalf("binding lifecycle binds=%d closes=%d", binds.Load(), closes.Load())
			}
			if backend.aborts.Load() != 0 {
				t.Fatal("ambiguous published reference was aborted")
			}
		})
	}
}
