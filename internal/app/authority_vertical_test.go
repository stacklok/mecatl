package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestADR_0233_AuthorityEvaluator_VerticalSlice exercises the ordinary Build →
// Service path. It keeps evaluator-outage injection at the engine adapter seam:
// production composition deliberately selects only configured evaluator adapters.
func TestADR_0233_AuthorityEvaluator_VerticalSlice(t *testing.T) {
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser})
	workspace := t.TempDir()
	storeDir := filepath.Join(t.TempDir(), "sessions")
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("readable\n"), 0o600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	mcpURL, remoteCalls := authorityVerticalMCPServer(t)
	agentsDir := t.TempDir()
	definition := "---\nname: code-reviewer\ndescription: code reviewer\nmodel: inherit\ntools: [Read, Grep]\nmcpServers:\n  - name: slack\n    url: " + mcpURL + "\n---\nReview code carefully.\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "code-reviewer.md"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write agent definition: %v", err)
	}

	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"review","agent":"code-reviewer"}`))),
		// The direct MCP call is a stale disclosure: this per-def catalog contains
		// it, but the parent's composed root never held the inline-server capability.
		mockllm.ToolCallTurn(session.NewToolCall("stale", "mcp__slack__post_message", []byte(`{"text":"no"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("read", "Read", []byte(`{"path":"README.md"}`))),
		mockllm.TextTurn("review complete"),
		mockllm.TextTurn("parent complete"),
	)
	cfg := Config{
		Workspace:     workspace,
		StoreDir:      storeDir,
		AgentsDirs:    []string{agentsDir},
		MockProvider:  provider,
		NoSoul:        true,
		AllowAllTools: true,
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	parent, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		built.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	if root, bound := parent.BoundAuthority(); !bound || len(root.CapabilitySet.Tools) == 0 || root.CapabilitySet.RemainingDelegationDepth == 0 {
		built.Close()
		t.Fatalf("minted root authority = %+v, bound=%t; want a usable authority", root, bound)
	}

	run, err := built.Service.StartRun(ctx, parent.ID, "delegate")
	if err != nil {
		built.Close()
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent complete" {
		built.Close()
		t.Fatalf("terminal text = %q, want parent complete", got)
	}

	childID := session.SessionID("subagent-" + string(parent.ID) + "-delegate")
	child, err := built.Service.GetSession(ctx, childID)
	if err != nil {
		built.Close()
		t.Fatalf("GetSession(%q): %v", childID, err)
	}
	childAuthority, bound := child.BoundAuthority()
	if !bound {
		built.Close()
		t.Fatal("managed specialist child has no derived authority")
	}
	sort.Strings(childAuthority.CapabilitySet.Tools)
	if got, want := childAuthority.CapabilitySet.Tools, []string{"Grep", "Read"}; !sameStrings(got, want) {
		built.Close()
		t.Fatalf("child tools = %v, want %v", got, want)
	}
	if childAuthority.CapabilitySet.RemainingDelegationDepth != 0 {
		built.Close()
		t.Fatalf("child depth = %d, want one hop spent", childAuthority.CapabilitySet.RemainingDelegationDepth)
	}
	if !child.Owner.SameIdentity(parent.Owner) {
		built.Close()
		t.Fatalf("child owner = %+v, want parent owner %+v", child.Owner, parent.Owner)
	}
	assertAuthorityVerticalResults(t, child, remoteCalls)
	built.Close()
	assertAuthorityVerticalMetaTarget(ctx, t, workspace, mcpURL, remoteCalls)

	// A new process reads the durable child snapshot, then the persisted parent is
	// narrowed before it attempts a resume. The old child must not gain the parent's
	// former Grep capability merely because it was created before the narrowing.
	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("reopen session store: %v", err)
	}
	persistedChild, err := store.Load(ctx, childID)
	if err != nil {
		t.Fatalf("load child after restart: %v", err)
	}
	persistedAuthority, persistedBound := persistedChild.BoundAuthority()
	if !persistedBound || !sameStrings(persistedAuthority.CapabilitySet.Tools, childAuthority.CapabilitySet.Tools) {
		t.Fatalf("durable child authority = %+v bound=%t, want %+v", persistedAuthority, persistedBound, childAuthority)
	}
	persistedParent, err := store.Load(ctx, parent.ID)
	if err != nil {
		t.Fatalf("load parent after restart: %v", err)
	}
	parentAuthority, parentBound := persistedParent.BoundAuthority()
	if !parentBound {
		t.Fatal("durable parent has no authority")
	}
	narrowedParent := authoritySnapshotWithNarrowedTools(t, persistedParent, parentAuthority.CapabilitySet.Tools, "Grep")
	if err := store.Save(ctx, narrowedParent); err != nil {
		t.Fatalf("save narrowed parent: %v", err)
	}

	resumedProvider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("resume", "Subagent", []byte(`{"prompt":"continue","resume":"`+string(childID)+`"}`))),
		mockllm.TextTurn("resume refused"),
	)
	cfg.MockProvider = resumedProvider
	resumedBuild, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build after restart: %v", err)
	}
	defer resumedBuild.Close()
	run, err = resumedBuild.Service.StartRun(ctx, parent.ID, "resume")
	if err != nil {
		t.Fatalf("StartRun after restart: %v", err)
	}
	if got := drainRun(run); got != "resume refused" {
		t.Fatalf("resume terminal text = %q, want resume refused", got)
	}
	resumedParent, err := resumedBuild.Service.GetSession(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetSession resumed parent: %v", err)
	}
	assertAuthorityVerticalResumeRefusal(t, resumedParent)
}

func TestADR_0233_AuthorityEvaluator_VerticalSlice_Cedar(t *testing.T) {
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser})
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "vendor"), 0o700); err != nil {
		t.Fatalf("create vendor directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "vendor", "blocked.go"), []byte("package vendor\n"), 0o600); err != nil {
		t.Fatalf("write vendor file: %v", err)
	}
	agentsDir := t.TempDir()
	definition := "---\nname: code-reviewer\ndescription: code reviewer\nmodel: inherit\ntools: [Read, Grep]\n---\nReview code carefully.\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "code-reviewer.md"), []byte(definition), 0o600); err != nil {
		t.Fatalf("write agent definition: %v", err)
	}
	policyPath := filepath.Join(t.TempDir(), "authority.cedar")
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	policy := `permit(principal, action, resource);
forbid(principal, action, resource) when { resource.path like "` + filepath.ToSlash(filepath.Join(realWorkspace, "vendor")) + `/*" };`
	if err := os.WriteFile(policyPath, []byte(policy), 0o600); err != nil {
		t.Fatalf("write Cedar policy: %v", err)
	}

	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"review","agent":"code-reviewer"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("read", "Read", []byte(`{"path":"vendor/blocked.go"}`))),
		mockllm.TextTurn("review complete"),
		mockllm.TextTurn("parent complete"),
	)
	built, err := Build(ctx, Config{
		Workspace:            workspace,
		StoreDir:             filepath.Join(t.TempDir(), "sessions"),
		AgentsDirs:           []string{agentsDir},
		MockProvider:         provider,
		NoSoul:               true,
		AllowAllTools:        true,
		AuthorityEvaluator:   "cedar",
		CedarAuthorityPolicy: policyPath,
	})
	if err != nil {
		t.Fatalf("Build(Cedar): %v", err)
	}
	defer built.Close()

	parent, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, parent.ID, "delegate")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent complete" {
		t.Fatalf("terminal text = %q, want parent complete", got)
	}

	child, err := built.Service.GetSession(ctx, session.SessionID("subagent-"+string(parent.ID)+"-delegate"))
	if err != nil {
		t.Fatalf("GetSession(child): %v", err)
	}
	for _, message := range child.Conversation.Messages {
		if message.Role != session.RoleTool || message.ToolResult == nil || message.ToolResult.CallID != "read" {
			continue
		}
		result := *message.ToolResult
		if !result.IsError || !strings.Contains(result.Content, "denied by authority") || !strings.Contains(result.Content, "Cedar") {
			t.Fatalf("Cedar vendor-path result = %+v, want a distinct authority denial", result)
		}
		return
	}
	t.Fatal("missing child Read result")
}

func TestADR_0233_AuthorityEvaluator_Scenario7_CedarDeniesSymlinkedTarget(t *testing.T) {
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser})
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "vendor"), 0o700); err != nil {
		t.Fatalf("create vendor directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "vendor", "blocked.go"), []byte("package vendor\n"), 0o600); err != nil {
		t.Fatalf("write vendor file: %v", err)
	}
	if err := os.Symlink("vendor", filepath.Join(workspace, "review")); err != nil {
		t.Fatalf("create in-workspace symlink: %v", err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	policyPath := filepath.Join(t.TempDir(), "authority.cedar")
	policy := `permit(principal, action, resource);
forbid(principal, action, resource) when { resource.path like "` + filepath.ToSlash(filepath.Join(resolvedWorkspace, "vendor")) + `/*" };`
	if err := os.WriteFile(policyPath, []byte(policy), 0o600); err != nil {
		t.Fatalf("write Cedar policy: %v", err)
	}

	built, err := Build(ctx, Config{
		Workspace:            workspace,
		StoreDir:             filepath.Join(t.TempDir(), "sessions"),
		MockProvider:         mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("read", "Read", []byte(`{"path":"review/blocked.go"}`))), mockllm.TextTurn("complete")),
		NoSoul:               true,
		AllowAllTools:        true,
		AuthorityEvaluator:   "cedar",
		CedarAuthorityPolicy: policyPath,
	})
	if err != nil {
		t.Fatalf("Build(Cedar): %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "read through symlink")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "complete" {
		t.Fatalf("terminal text = %q, want complete", got)
	}

	stored, err := built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	for _, message := range stored.Conversation.Messages {
		if message.Role != session.RoleTool || message.ToolResult == nil || message.ToolResult.CallID != "read" {
			continue
		}
		if !message.ToolResult.IsError || !strings.Contains(message.ToolResult.Content, "denied by authority") || !strings.Contains(message.ToolResult.Content, "Cedar") {
			t.Fatalf("Cedar symlinked vendor result = %+v, want a distinct authority denial", message.ToolResult)
		}
		return
	}
	t.Fatal("missing Read result")
}

func TestADR_0233_AuthorityEvaluator_OwnerlessCompositionUsesLocalEvaluator(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("ownerless readable\n"), 0o600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	built, err := Build(context.Background(), Config{
		Workspace:     workspace,
		StoreDir:      filepath.Join(t.TempDir(), "sessions"),
		MockProvider:  mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("read", "Read", []byte(`{"path":"README.md"}`))), mockllm.TextTurn("done")),
		NoSoul:        true,
		AllowAllTools: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Owner != nil {
		t.Fatalf("ownerless composition session owner = %+v, want nil", sess.Owner)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "read")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "done" {
		t.Fatalf("terminal text = %q, want done", got)
	}
	stored, err := built.Service.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	for _, message := range stored.Conversation.Messages {
		if message.Role == session.RoleTool && message.ToolResult != nil && message.ToolResult.CallID == "read" {
			if message.ToolResult.IsError || !strings.Contains(message.ToolResult.Content, "ownerless readable") {
				t.Fatalf("ownerless local authority result = %+v, want successful Read", message.ToolResult)
			}
			return
		}
	}
	t.Fatal("missing ownerless local Read result")
}

func TestADR_0233_AuthorityEvaluator_OwnerlessCedarSessionFailsClosed(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("not read\n"), 0o600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	policyPath := filepath.Join(t.TempDir(), "authority.cedar")
	if err := os.WriteFile(policyPath, []byte(`permit(principal, action, resource);`), 0o600); err != nil {
		t.Fatalf("write Cedar policy: %v", err)
	}
	built, err := Build(context.Background(), Config{
		Workspace:            workspace,
		StoreDir:             filepath.Join(t.TempDir(), "sessions"),
		MockProvider:         mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("read", "Read", []byte(`{"path":"README.md"}`))), mockllm.TextTurn("done")),
		NoSoul:               true,
		AllowAllTools:        true,
		AuthorityEvaluator:   "cedar",
		CedarAuthorityPolicy: policyPath,
	})
	if err != nil {
		t.Fatalf("Build(Cedar): %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "read")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(run)
	stored, err := built.Service.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	for _, message := range stored.Conversation.Messages {
		if message.Role == session.RoleTool && message.ToolResult != nil && message.ToolResult.CallID == "read" {
			if !message.ToolResult.IsError || !strings.Contains(message.ToolResult.Content, "owner identity is unavailable") {
				t.Fatalf("ownerless Cedar authority result = %+v, want fail-closed identity error", message.ToolResult)
			}
			return
		}
	}
	t.Fatal("missing ownerless Cedar Read result")
}

func authorityVerticalMCPServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	var calls atomic.Int32
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "slack", Version: "v1"}, nil)
	srv.AddTool(&mcpsdk.Tool{Name: "post_message", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls.Add(1)
		return &mcpsdk.CallToolResult{}, nil
	})
	httpSrv := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil))
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL, &calls
}

func assertAuthorityVerticalMetaTarget(ctx context.Context, t *testing.T, workspace, mcpURL string, remoteCalls *atomic.Int32) {
	t.Helper()

	storeDir := t.TempDir()
	cfg := Config{
		Workspace:     workspace,
		StoreDir:      storeDir,
		MCPServers:    []mcp.ServerConfig{{Name: "slack", URL: mcpURL}},
		MockProvider:  mockllm.New(mockllm.TextTurn("unused")),
		NoSoul:        true,
		AllowAllTools: true,
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build meta setup: %v", err)
	}
	parent, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		built.Close()
		t.Fatalf("CreateSession meta setup: %v", err)
	}
	built.Close()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("open meta setup store: %v", err)
	}
	persisted, err := store.Load(ctx, parent.ID)
	if err != nil {
		t.Fatalf("load meta setup parent: %v", err)
	}
	authority, bound := persisted.BoundAuthority()
	if !bound {
		t.Fatal("meta setup parent has no authority")
	}
	target := "mcp__slack__post_message"
	narrowed := authoritySnapshotWithNarrowedTools(t, persisted, authority.CapabilitySet.Tools, target)
	if err := store.Save(ctx, narrowed); err != nil {
		t.Fatalf("save narrowed meta setup parent: %v", err)
	}

	cfg.MockProvider = mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("meta", "CallMcpWithQuery", []byte(`{"server":"slack","tool":"post_message"}`))),
		mockllm.TextTurn("meta refused"),
	)
	resumed, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build meta execution: %v", err)
	}
	defer resumed.Close()
	run, err := resumed.Service.StartRun(ctx, parent.ID, "meta")
	if err != nil {
		t.Fatalf("StartRun meta execution: %v", err)
	}
	if got := drainRun(run); got != "meta refused" {
		t.Fatalf("meta terminal text = %q, want meta refused", got)
	}
	resultParent, err := resumed.Service.GetSession(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetSession meta parent: %v", err)
	}
	for _, message := range resultParent.Conversation.Messages {
		if message.Role != session.RoleTool || message.ToolResult == nil || message.ToolResult.CallID != "meta" {
			continue
		}
		if !message.ToolResult.IsError || !strings.Contains(message.ToolResult.Content, target) || !strings.Contains(message.ToolResult.Content, "denied by authority") {
			t.Fatalf("meta result = %+v, want reconstructed-target authority denial", message.ToolResult)
		}
		if got := remoteCalls.Load(); got != 0 {
			t.Fatalf("remote MCP calls = %d, want 0 after target denial", got)
		}
		return
	}
	t.Fatal("missing meta target denial")
}

func authoritySnapshotWithNarrowedTools(t *testing.T, source *session.Session, tools []string, removed string) *session.Session {
	t.Helper()

	authority, bound := source.BoundAuthority()
	if !bound {
		t.Fatal("cannot narrow an unbound authority")
	}
	authority.CapabilitySet.Tools = removeAuthorityTool(tools, removed)
	narrowed := session.New(source.ID, source.Mode, source.Workspace, source.Limits, source.CreatedAt)
	if err := narrowed.RestoreLabels(source.Owner, authority); err != nil {
		t.Fatalf("stamp narrowed authority snapshot: %v", err)
	}
	return narrowed
}

func removeAuthorityTool(tools []string, target string) []string {
	out := make([]string, 0, len(tools))
	for _, name := range tools {
		if name != target {
			out = append(out, name)
		}
	}
	return out
}

func assertAuthorityVerticalResults(t *testing.T, child *session.Session, remoteCalls *atomic.Int32) {
	t.Helper()

	results := make(map[session.ToolCallID]session.ToolResult)
	for _, message := range child.Conversation.Messages {
		if message.Role == session.RoleTool && message.ToolResult != nil {
			results[message.ToolResult.CallID] = *message.ToolResult
		}
	}
	stale, ok := results["stale"]
	if !ok || !stale.IsError || !strings.Contains(stale.Content, "denied by authority") || strings.Contains(stale.Content, "unknown tool") {
		t.Fatalf("stale disclosure result = %+v, want execute-time authority refusal rather than unknown tool", stale)
	}
	read, ok := results["read"]
	if !ok || read.IsError || !strings.Contains(read.Content, "readable") {
		t.Fatalf("Read result = %+v, want the allowed workspace read", read)
	}
	if got := remoteCalls.Load(); got != 0 {
		t.Fatalf("remote MCP calls = %d, want 0 after target denial", got)
	}
}

func assertAuthorityVerticalResumeRefusal(t *testing.T, parent *session.Session) {
	t.Helper()

	for _, message := range parent.Conversation.Messages {
		if message.Role != session.RoleTool || message.ToolResult == nil || message.ToolResult.CallID != "resume" {
			continue
		}
		if !message.ToolResult.IsError || !strings.Contains(message.ToolResult.Content, "resumed child authority exceeds current parent authority") {
			t.Fatalf("resume result = %+v, want narrowed-parent authority refusal", message.ToolResult)
		}
		return
	}
	t.Fatal("missing resumed-child authority refusal")
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
