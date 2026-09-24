package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

//nolint:revive // Test helpers consistently take testing.T first.
func harnessRun(t *testing.T, b *Built, ctx context.Context, id session.SessionID, text string) []session.Event {
	t.Helper()
	run, err := b.Service.StartRun(ctx, id, text)
	if err != nil {
		t.Fatal(err)
	}
	var events []session.Event
	for ev := range run.Events() {
		events = append(events, ev)
	}
	b.Service.FinishRun(id, run)
	return events
}

//nolint:revive // Test helpers consistently take testing.T first.
func harnessCreate(t *testing.T, b *Built, ctx context.Context) session.SessionID {
	t.Helper()
	s, err := b.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return s.ID
}
func harnessSeed(t *testing.T, ws *memfs.Workspace, path, body string) {
	t.Helper()
	if err := ws.Write(t.Context(), path, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

type harnessPlacement struct{ *compositionPlacementProvider }

func (p *harnessPlacement) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	binding, err := p.compositionPlacementProvider.Bind(ctx, req)
	if err == nil {
		binding.Environment = tool.MustEnvironment(binding.Ref, binding.Environment.Workspace(), memledger.New(), binding.Environment.CommandRunner())
	}
	return binding, err
}
func (p *harnessPlacement) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	binding, err := p.compositionPlacementProvider.Reattach(ctx, req)
	if err == nil {
		binding.Environment = tool.MustEnvironment(binding.Ref, binding.Environment.Workspace(), memledger.New(), binding.Environment.CommandRunner())
	}
	return binding, err
}
func harnessVirtualPlacement(ws tool.Workspace) *harnessPlacement {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "virtual-execution", Revision: "v1"}
	return &harnessPlacement{&compositionPlacementProvider{binding: server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, ws, memledger.New(), nil)}}}
}

func TestADR_0357_HarnessContext_Scenario1_UnselectedExecutionContentIgnored(t *testing.T) {
	source := memfs.NewWorkspace("/selected")
	harnessSeed(t, source, "AGENTS.md", "SELECTED")
	harnessSeed(t, source, ".mecatl/commands/review.md", "SELECTED-COMMAND")
	cfg := hcConfiguredFiles(t, source)
	cfg.AgentsConventional = true
	cfg.SkillsConventional = true
	cfg.EnableCommands = true
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "AGENTS.md"), []byte("UNSELECTED"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCommand(t, cfg.Workspace, ".mecatl/commands", "review", "UNSELECTED-COMMAND")
	writeSkill(t, filepath.Join(cfg.Workspace, ".mecatl/skills"), "unselected-skill", "unselected", "UNSELECTED-SKILL")
	writeAgentDef(t, filepath.Join(cfg.Workspace, ".mecatl/agents"), "unselected-agent", "unselected", "UNSELECTED-AGENT")
	if err := os.MkdirAll(filepath.Join(cfg.Workspace, ".mecatl/rules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Workspace, ".mecatl/rules/rule.md"), []byte("UNSELECTED-RULE"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("done"))
	b, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := harnessCreate(t, b, t.Context())
	commands, err := b.Service.ListCommandsForSession(t.Context(), id)
	if err != nil || len(commands) != 1 || commands[0].Name != "review" {
		t.Fatalf("commands=%v,%v", commands, err)
	}
	harnessRun(t, b, t.Context(), id, "/review")
	if len(requests) != 1 {
		t.Fatalf("requests=%d", len(requests))
	}
	var content strings.Builder
	for _, m := range requests[0].Messages {
		content.WriteString(m.Text)
	}
	for _, spec := range requests[0].Tools {
		content.WriteString(spec.Description)
	}
	if strings.Contains(content.String(), "UNSELECTED") || strings.Contains(content.String(), "unselected-") {
		t.Fatal("execution-only content entered request or catalog")
	}
	if !strings.Contains(content.String(), "SELECTED-COMMAND") {
		t.Fatal("positive source command absent")
	}
	if len(b.Service.ListAgents(t.Context())) != 0 || len(b.Service.ListSkills(t.Context())) != 0 {
		t.Fatal("unselected customization reached inventory")
	}
}

func TestADR_0357_HarnessContext_Scenario1_ExplicitExecutionFileSource(t *testing.T) {
	ws := memfs.NewWorkspace("/virtual-not-a-host-directory")
	harnessSeed(t, ws, "AGENTS.md", "EXPLICIT-EXECUTION-CONTEXT")
	harnessSeed(t, ws, ".mecatl/commands/review.md", "EXPLICIT-EXECUTION-COMMAND")
	harnessSeed(t, ws, "data.txt", "ACTUAL-EXECUTION-DATA")
	cfg := hcConfiguredFiles(t, ws)
	cfg.PlacementProvider = harnessVirtualPlacement(ws)
	cfg.AllowAllTools = true
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"data.txt"}`)}), mockllm.TextTurn("done"))
	b, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := harnessCreate(t, b, t.Context())
	events := harnessRun(t, b, t.Context(), id, "/review")
	var read bool
	for _, ev := range events {
		if ev.ToolResult != nil && ev.ToolResult.CallID == "read" {
			read = !ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "ACTUAL-EXECUTION-DATA")
		}
	}
	if !read {
		t.Fatal("execution tool did not use the actual virtual backend")
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	var text strings.Builder
	for _, m := range requests[0].Messages {
		text.WriteString(m.Text)
	}
	if !strings.Contains(text.String(), "EXPLICIT-EXECUTION-CONTEXT") || !strings.Contains(text.String(), "EXPLICIT-EXECUTION-COMMAND") {
		t.Fatal("explicit execution-file sources were lost or reopened as host paths")
	}
}

func TestADR_0357_HarnessContext_Scenario2_SourceOnlyDiscovery(t *testing.T) {
	ws := memfs.NewWorkspace("/selected")
	harnessSeed(t, ws, ".mecatl/commands/review.md", "selected")
	cfg := hcConfiguredFiles(t, ws)
	placement := harnessVirtualPlacement(memfs.NewWorkspace("/execution"))
	cfg.PlacementProvider = placement
	cfg.OwnershipEnforced = true
	b, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	alice := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser})
	id := harnessCreate(t, b, alice)
	placement.err = server.ErrPlacementUnavailable
	before := len(placement.reattachCalls)
	commands, err := b.Service.ListCommandsForSession(alice, id)
	if err != nil || len(commands) != 1 {
		t.Fatalf("source-only discovery=%v,%v", commands, err)
	}
	if len(placement.reattachCalls) != before {
		t.Fatal("command listing reattached unrelated execution")
	}
	if _, err := b.Service.ListCommandsForSession(bob, id); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign discovery=%v", err)
	}
}

func TestADR_0357_HarnessContext_Scenario3_SourceReadsDoNotAuthorizeEdits(t *testing.T) {
	ws := memfs.NewWorkspace("/same-files")
	harnessSeed(t, ws, "AGENTS.md", "original")
	cfg := hcConfiguredFiles(t, ws)
	cfg.PlacementProvider = harnessVirtualPlacement(ws)
	cfg.AllowAllTools = true
	cfg.MockProvider = mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "source-only", Name: "Edit", Args: json.RawMessage(`{"path":"AGENTS.md","old_string":"original","new_string":"changed"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "read", Name: "Read", Args: json.RawMessage(`{"path":"AGENTS.md"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "read-authorized", Name: "Edit", Args: json.RawMessage(`{"path":"AGENTS.md","old_string":"original","new_string":"changed"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "read-current", Name: "Read", Args: json.RawMessage(`{"path":"AGENTS.md"}`)}), mockllm.TextTurn("first done"),
		mockllm.ToolCallTurn(session.ToolCall{ID: "other-session", Name: "Edit", Args: json.RawMessage(`{"path":"AGENTS.md","old_string":"changed","new_string":"wrong"}`)}), mockllm.TextTurn("second done"))
	b, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	first := harnessCreate(t, b, t.Context())
	second := harnessCreate(t, b, t.Context())
	events := harnessRun(t, b, t.Context(), first, "edit")
	events = append(events, harnessRun(t, b, t.Context(), second, "edit")...)
	seen := map[session.ToolCallID]bool{}
	for _, ev := range events {
		if ev.ToolResult != nil {
			seen[ev.ToolResult.CallID] = ev.ToolResult.IsError
		}
	}
	for _, id := range []session.ToolCallID{"source-only", "other-session"} {
		if failed, ok := seen[id]; !ok || !failed {
			t.Fatalf("%s mutation was authorized by source/other-session evidence: %v", id, seen)
		}
	}
	if failed, ok := seen["read-authorized"]; !ok || failed {
		t.Fatalf("real execution Read did not authorize its Edit: %v", seen)
	}
	data, err := ws.Read(t.Context(), "AGENTS.md")
	if err != nil || string(data) != "changed" {
		t.Fatalf("file=%q,%v", data, err)
	}
}
