// Package main (mecademo) is the mecatl end-to-end demo driver. Its core,
// RunScenario, drives the real agent.Engine through a scripted session that
// proves the whole shape of the loop — an auto-allowed tool call, a tool call
// that requires approval (and is approved), and a final assistant message — and
// returns every streamed session.Event so the same scenario backs both the
// printed CLI output and the hermetic e2e test.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/tools"
)

// demoWorkspaceRoot is the root the in-memory demo workspace is mounted at.
const demoWorkspaceRoot = "/workspace"

// demoEnvironmentRevision identifies the in-memory demo environment.
const demoEnvironmentRevision = "in-tree-v1"

// demoFilePath is the file the scenario seeds and the model Reads.
const demoFilePath = "greeting.txt"

// demoFileContent is the seeded file body, surfaced through the Read tool result.
const demoFileContent = "hello from the mecatl demo workspace\n"

// demoModel is the default model identifier stamped into requests and the
// prompt env when the caller does not override it (the offline mockllm path).
const demoModel = "mock-model"

// RunScenario drives one full offline session against the provided LLMProvider
// and returns every emitted session.Event in order. The caller supplies the
// provider so the same scenario runs against mockllm (offline default) or the
// real OpenAI adapter, plus the model identifier to stamp into requests (the
// live adapter requires a real model ID; the offline mock ignores it). It
// auto-approves the single permission ask the script raises, simulating a
// client clicking "allow", so the loop resumes to a final result.
//
// It is the single source of truth for the demo: main prints these events, the
// e2e test asserts over them.
func RunScenario(ctx context.Context, provider port.LLMProvider, model string) ([]session.Event, error) {
	if model == "" {
		model = demoModel
	}
	// Workspace: an in-memory FS seeded with one file so Read returns content.
	ws := memfs.NewWorkspace(demoWorkspaceRoot)
	if err := ws.Write(ctx, demoFilePath, []byte(demoFileContent)); err != nil {
		return nil, fmt.Errorf("seed workspace: %w", err)
	}

	engine := buildEngine(provider, model)

	sess := session.New(
		"demo-session",
		session.ModeDefault,
		session.EnvironmentRef{Kind: session.EnvKindLocal, ID: demoWorkspaceRoot, Revision: demoEnvironmentRevision},
		session.Limits{MaxTurns: 8, MaxToolCalls: 16, MaxConsecutiveFailures: 3},
		time.Now(),
	)

	run := engine.Run(ctx, sess, tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: demoWorkspaceRoot, Revision: demoEnvironmentRevision}, ws, nil), agent.RunRequest{Text: "Read greeting.txt and then save a note, then summarize."})

	var events []session.Event
	for ev := range run.Events() {
		events = append(events, ev)
		// Simulate a client approving the permission ask so the loop resumes.
		// AllowAlways exercises the learned-permission path: the policy records a
		// per-session allow for this exact tool+pattern, so a repeat would not ask.
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowAlways)
		}
	}
	return events, nil
}

// buildEngine assembles the agent.Engine for the demo with the always-available
// tool catalog (the demo exercises only Read/Write, so it runs shell-less: no
// Bash tool is registered), the default deny/ask/allow policy (Read auto-allowed,
// Write asks), no hooks, an in-memory store, and a deterministic prompt config.
func buildEngine(provider port.LLMProvider, model string) *agent.Engine {
	cat := tool.NewCatalog()
	for _, t := range tools.All() {
		cat.MustRegister(t)
	}

	policy := permpolicy.NewPolicy([]governance.Rule{
		{Scope: governance.ScopeManaged, Tool: "Read", Effect: governance.Allow},
		{Scope: governance.ScopeManaged, Tool: "Grep", Effect: governance.Allow},
		{Scope: governance.ScopeManaged, Tool: "Glob", Effect: governance.Allow},
		{Scope: governance.ScopeManaged, Tool: "Write", Effect: governance.Ask},
		{Scope: governance.ScopeManaged, Tool: "Edit", Effect: governance.Ask},
	}, permstore.New())

	return agent.NewEngine(agent.Deps{
		LLM:     provider,
		Catalog: cat,
		Policy:  policy,
		Hooks:   hookexec.New(nil),
		Store:   memstore.New(),
		PromptConfig: prompt.Config{
			Env: prompt.Env{
				Cwd:   demoWorkspaceRoot,
				OS:    "linux",
				Model: model,
				Date:  "2026-05-29",
				Mode:  string(session.ModeDefault),
			},
		},
		Model: model,
	})
}

// mockProvider returns the canned, offline LLMProvider that scripts the demo's
// three-act session:
//
//  1. assistant text + an auto-allowed Read tool call,
//  2. assistant text + a Write tool call that requires approval (permission.ask),
//  3. a final assistant message that ends the turn (terminal result).
//
// Each turn's tool call references the seeded file so the tools execute against
// real workspace content.
func mockProvider() *mockllm.Provider {
	readCall := session.NewToolCall(
		"call-read-1",
		"Read",
		json.RawMessage(fmt.Sprintf(`{"path":%q}`, demoFilePath)),
	)
	writeCall := session.NewToolCall(
		"call-write-1",
		"Write",
		json.RawMessage(`{"path":"note.txt","content":"reviewed the greeting\n"}`),
	)

	return mockllm.New(
		// Turn 1: prose + an auto-allowed Read.
		mockllm.ChunksTurn(
			mockllm.TextChunk("I'll read the greeting file first."),
			mockllm.ToolCallChunk(readCall),
			mockllm.UsageChunk(session.Usage{InputTokens: 1200, OutputTokens: 40, CacheReadTokens: 1000}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		// Turn 2: prose + a Write that must be approved.
		mockllm.ChunksTurn(
			mockllm.TextChunk("Now I'll save a short note, which needs your approval."),
			mockllm.ToolCallChunk(writeCall),
			mockllm.UsageChunk(session.Usage{InputTokens: 1400, OutputTokens: 55, CacheReadTokens: 1200}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		// Turn 3: final summary, no tools -> terminal result.
		mockllm.ChunksTurn(
			mockllm.TextChunk("Done: I read greeting.txt and saved note.txt."),
			mockllm.UsageChunk(session.Usage{InputTokens: 1500, OutputTokens: 30, CacheReadTokens: 1400}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
}

// RunTeamScenario drives a fully-offline 2-member agent team (a lead + a worker)
// through the real agent.Supervisor and returns its consolidated report. It proves
// the new aggregation shape end to end: the worker records a finding to the shared
// ledger, the lead's FINAL synthesis turn consolidates that finding into the team's
// deliverable, and that synthesis — not a bare per-member concatenation — is the
// returned report. It is offline (mockllm + memfs) so `go run ./cmd/mecademo` shows
// the new deliverable without a network.
func RunTeamScenario(ctx context.Context) (agent.TeamOutcome, error) {
	tm := team.New("demo-team")
	base := memfs.NewWorkspace(demoWorkspaceRoot)
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)

	recordFinding := session.NewToolCall("w1", "RecordFinding",
		json.RawMessage(`{"finding":"greeting.txt reads cleanly; no encoding issues"}`))
	scripts := map[string]*mockllm.Provider{
		"lead": mockllm.New(
			mockllm.TextTurn("Delegating the inspection to the worker."),
			mockllm.TextTurn("Consolidated report: the worker confirmed greeting.txt reads cleanly; nothing to fix."),
		),
		"worker": mockllm.New(
			mockllm.ToolCallTurn(recordFinding),
			mockllm.TextTurn("Inspection complete; finding recorded."),
		),
	}
	factory := func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov, ok := scripts[spec.Name]
		if !ok {
			return agent.MemberBuild{}
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: hookexec.New(nil), Model: demoModel,
		})}
	}

	baseEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: demoWorkspaceRoot, Revision: demoEnvironmentRevision}, base, nil)
	sup := agent.NewSupervisor(tm, baseEnv, factory,
		agent.WithTeamGoal("verify the demo greeting file is intact"),
		agent.WithMemberStore(memstore.New()),
		agent.WithMemberSessionPrefix("team-demo"),
		agent.WithMaxRounds(6))
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "coordinate the verification"}); err != nil {
		return agent.TeamOutcome{}, fmt.Errorf("enrol lead: %w", err)
	}
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "worker", InitialPrompt: "inspect greeting.txt and report"}); err != nil {
		return agent.TeamOutcome{}, fmt.Errorf("enrol worker: %w", err)
	}
	return sup.Run(ctx, nil), nil
}

// demoBackgroundChildID is the deterministic child session id of the background
// scenario's subagent ("subagent-<parentSessionID>-<callID>" — namespaced by
// the parent session id, review finding 2/issue #368), used by the scripted
// collection turn and asserted by the e2e test.
const demoBackgroundChildID = "subagent-demo-background-session-call-bg-1"

// RunBackgroundScenario drives the fully-offline background-subagent flow
// (BACKGROUND-SUBAGENTS I3a+I3b) through a real agent.Engine: the model starts a
// Subagent with background:true and gets the immediate started-result, a
// SubagentStatus wait parks until the child's terminal lands, the harness
// injects the completion NOTICE (a recorded user message — ids + stop labels
// only) at the next turn boundary, and a SubagentStatus collection delivers the
// child's result body through the tool-result channel. It returns every
// streamed event PLUS the harness notes recorded into the session history
// (notices are history, not events — they are what the MODEL sees at the
// boundary), so main can print the whole flow and the e2e test can assert it.
// It is infallible: the scenario is fully offline and self-contained.
func RunBackgroundScenario(ctx context.Context) ([]session.Event, []string) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)

	// The background child: a one-turn investigator with its own engine.
	childLLM := mockllm.New(
		mockllm.TextTurn("Background check complete: greeting.txt is intact and well-formed."),
	)
	childEngine := agent.NewEngine(agent.Deps{
		LLM: childLLM, Catalog: tool.NewCatalog(), Policy: allow,
		Hooks: hookexec.New(nil), Model: demoModel,
	})

	// The parent: start the child in the background, wait for any child to
	// finish (the roster wait — it does NOT collect, so the next boundary's
	// notice fires), then collect the body after the notice prompts it.
	parentLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("I'll start a background subagent to verify the greeting while I keep this turn."),
			mockllm.ToolCallChunk(session.NewToolCall("call-bg-1", "Subagent",
				json.RawMessage(`{"prompt":"verify the greeting file in the background","background":true}`))),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("Waiting for the background subagent to finish."),
			mockllm.ToolCallChunk(session.NewToolCall("call-bg-wait", "SubagentStatus",
				json.RawMessage(`{"wait_ms":30000}`))),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("The harness notice says it finished — collecting its result."),
			mockllm.ToolCallChunk(session.NewToolCall("call-bg-collect", "SubagentStatus",
				json.RawMessage(`{"agent_id":"`+demoBackgroundChildID+`"}`))),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("Done: the background subagent verified the greeting and I collected its result."),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)

	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewSubagentTool(childEngine))
	cat.MustRegister(agent.NewSubagentStatusTool())
	engine := agent.NewEngine(agent.Deps{
		LLM: parentLLM, Catalog: cat, Policy: allow, Hooks: hookexec.New(nil),
		Store: memstore.New(), Model: demoModel,
	})

	sess := session.New(
		"demo-background-session",
		session.ModeDefault,
		session.EnvironmentRef{Kind: session.EnvKindLocal, ID: demoWorkspaceRoot, Revision: demoEnvironmentRevision},
		session.Limits{MaxTurns: 8, MaxToolCalls: 16, MaxConsecutiveFailures: 3},
		time.Now(),
	)
	bgWS := memfs.NewWorkspace(demoWorkspaceRoot)
	run := engine.Run(ctx, sess, tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: demoWorkspaceRoot, Revision: demoEnvironmentRevision}, bgWS, nil),
		agent.RunRequest{Text: "Verify the greeting in the background, then report."})

	var events []session.Event
	for ev := range run.Events() {
		events = append(events, ev)
	}

	// The injected harness notes live in the recorded HISTORY (they are ordinary
	// user-role messages the model replays), not on the event stream.
	var notes []string
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.HasPrefix(m.Text, "[harness note:") {
			notes = append(notes, m.Text)
		}
	}
	return events, notes
}
