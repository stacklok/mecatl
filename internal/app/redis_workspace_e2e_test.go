package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestRedisWorkspaceBuiltEngineExercisesAllFileToolsAcrossSamePrincipalSessions(t *testing.T) {
	mr := miniredis.RunT(t)
	var mu sync.Mutex
	var systems []string
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		mu.Lock()
		systems = append(systems, req.System.StablePrefix)
		mu.Unlock()
	})},
		mockllm.ToolCallTurn(session.ToolCall{ID: "write-1", Name: "Write", Args: json.RawMessage(`{"path":"docs/note.txt","content":"alpha\n"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "read-1", Name: "Read", Args: json.RawMessage(`{"path":"docs/note.txt"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "edit-1", Name: "Edit", Args: json.RawMessage(`{"path":"docs/note.txt","old_string":"alpha","new_string":"beta"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "grep-1", Name: "Grep", Args: json.RawMessage(`{"pattern":"beta","path":"**/*.txt"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "glob-1", Name: "Glob", Args: json.RawMessage(`{"pattern":"**/*.txt"}`)}),
		mockllm.TextTurn("first session done"),
		mockllm.ToolCallTurn(session.ToolCall{ID: "read-2", Name: "Read", Args: json.RawMessage(`{"path":"docs/note.txt"}`)}),
		mockllm.TextTurn("second session done"),
	)
	built, err := buildIsolated(t, context.Background(), Config{
		RedisURL:            mr.Addr(),
		RedisAllowPlaintext: true,
		RedisFilesystem:     true,
		RedisReadLedger:     true,
		MockProvider:        provider,
		NoSoul:              true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	principal := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(context.Background(), principal)
	first, err := built.Service.CreateSessionWithProfile(ctx, session.ModeAccept, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatal(err)
	}
	second, err := built.Service.CreateSessionWithProfile(ctx, session.ModeAccept, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatal(err)
	}
	if first.EnvironmentRef.Kind != redisEnvironmentKind || first.EnvironmentRef != second.EnvironmentRef {
		t.Fatalf("same-principal environment refs: first=%+v second=%+v", first.EnvironmentRef, second.EnvironmentRef)
	}

	firstResults := runRedisWorkspaceE2E(ctx, t, built.Service, first.ID, "exercise every file tool")
	for _, id := range []session.ToolCallID{"write-1", "read-1", "edit-1", "grep-1", "glob-1"} {
		result, ok := firstResults[id]
		if !ok {
			t.Fatalf("missing ToolResult for %s", id)
		}
		if result.IsError {
			t.Fatalf("%s failed: %s", id, result.Content)
		}
	}
	if !strings.Contains(firstResults["read-1"].Content, "alpha") {
		t.Fatalf("Read result = %q, want alpha", firstResults["read-1"].Content)
	}
	if !strings.Contains(firstResults["grep-1"].Content, "docs/note.txt") || !strings.Contains(firstResults["grep-1"].Content, "beta") {
		t.Fatalf("Grep result = %q", firstResults["grep-1"].Content)
	}
	if !strings.Contains(firstResults["glob-1"].Content, "docs/note.txt") {
		t.Fatalf("Glob result = %q", firstResults["glob-1"].Content)
	}

	secondResults := runRedisWorkspaceE2E(ctx, t, built.Service, second.ID, "read the other session's file")
	if result, ok := secondResults["read-2"]; !ok || result.IsError || !strings.Contains(result.Content, "beta") {
		t.Fatalf("same-principal cross-session Read = %+v, present=%v", result, ok)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(systems) == 0 || !strings.Contains(systems[0], redisWorkspacePostureNote) {
		t.Fatal("built engine system prompt omitted Redis workspace posture")
	}
}

// TestRedisWorkspaceRootListDirIsNotDeniedByAuthority pins the fix for
// authorityWorkspaceResource deriving an authority identity for ListDir's
// documented root spelling ("."): the Redis-backed Workspace's
// AuthorityResourcePath used to reject "." as a path escape, so a root
// ListDir was denied by the authority evaluator before Execute ever ran, even
// though ReadDir itself has always accepted "." as the workspace root.
func TestRedisWorkspaceRootListDirIsNotDeniedByAuthority(t *testing.T) {
	mr := miniredis.RunT(t)
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "list-root", Name: "ListDir", Args: json.RawMessage(`{"path":"."}`)}),
		mockllm.TextTurn("done"),
	)
	built, err := buildIsolated(t, context.Background(), Config{
		RedisURL:            mr.Addr(),
		RedisAllowPlaintext: true,
		RedisFilesystem:     true,
		RedisReadLedger:     true,
		MockProvider:        provider,
		NoSoul:              true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	principal := &session.Principal{Issuer: "https://issuer.example", Subject: "root-listdir", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(context.Background(), principal)
	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeAccept, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatal(err)
	}

	results := runRedisWorkspaceE2E(ctx, t, built.Service, sess.ID, "list the workspace root")
	result, ok := results["list-root"]
	if !ok {
		t.Fatal("missing ToolResult for list-root")
	}
	if result.IsError {
		t.Fatalf("root ListDir was denied before execution: %s", result.Content)
	}
}

func TestRedisWorkspaceExternalLookingListDirNeverFallsBackToHost(t *testing.T) {
	mr := miniredis.RunT(t)
	hostDir := t.TempDir()
	const sentinel = "host-only-listdir-sentinel.txt"
	if err := os.WriteFile(filepath.Join(hostDir, sentinel), []byte("host-only"), 0o600); err != nil {
		t.Fatalf("write host sentinel: %v", err)
	}
	args, err := json.Marshal(map[string]string{"path": hostDir})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("list-external", "ListDir", args)),
		mockllm.TextTurn("done"),
	)
	built, err := buildIsolated(t, context.Background(), Config{
		RedisURL:            mr.Addr(),
		RedisAllowPlaintext: true,
		RedisFilesystem:     true,
		RedisReadLedger:     true,
		MockProvider:        provider,
		NoSoul:              true,
		Posture:             PostureYolo,
		PostureFlagSet:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	principal := &session.Principal{Issuer: "https://issuer.example", Subject: "redis-external-listdir", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(context.Background(), principal)
	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatal(err)
	}
	results := runRedisWorkspaceE2E(ctx, t, built.Service, sess.ID, "list an external-looking directory")
	result, ok := results["list-external"]
	if !ok {
		t.Fatal("missing ToolResult for list-external")
	}
	if strings.Contains(result.Content, sentinel) {
		t.Fatalf("Redis ListDir exposed host sentinel: %q", result.Content)
	}
}

func runRedisWorkspaceE2E(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, prompt string) map[session.ToolCallID]session.ToolResult {
	t.Helper()
	run, err := svc.StartRun(ctx, id, prompt)
	if err != nil {
		t.Fatal(err)
	}
	results := make(map[session.ToolCallID]session.ToolResult)
	for _, event := range runEvents(run) {
		if event.Type == session.EvToolResult && event.ToolResult != nil {
			results[event.ToolResult.CallID] = *event.ToolResult
		}
	}
	return results
}
