package agent_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

type permissionReviewPolicy struct {
	provenance governance.AskProvenance
	learns     int
}

func (p *permissionReviewPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Ask, Reason: "test permission ask", AskProvenance: p.provenance}
}
func (p *permissionReviewPolicy) Learn(session.SessionID, session.ToolCall) { p.learns++ }

type permissionReviewer struct {
	mu         sync.Mutex
	assessment agent.ReviewAssessment
	err        error
	calls      int
	requests   []agent.ToolReviewRequest
	entered    chan struct{}
	waitForCtx bool
	action     bool
	actionErr  error
	usageByJob map[agent.ReviewJob]session.AuxiliaryUsage
}

func (r *permissionReviewer) Review(ctx context.Context, req agent.ToolReviewRequest, _ agent.ReviewEvidenceSource) (agent.ToolReviewResult, session.AuxiliaryUsage, error) {
	r.mu.Lock()
	r.calls++
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.waitForCtx {
		<-ctx.Done()
		return agent.ToolReviewResult{Assessment: agent.ReviewUnresolved}, session.AuxiliaryUsage{}, ctx.Err()
	}
	if req.Job == agent.ReviewJobAction && r.actionErr != nil {
		return agent.ToolReviewResult{Assessment: agent.ReviewUnresolved}, session.AuxiliaryUsage{}, r.actionErr
	}
	return agent.ToolReviewResult{Assessment: r.assessment}, r.usageByJob[req.Job], r.err
}

func (*permissionReviewer) GuardrailPermissionReviewEligible(session.ToolCall) bool { return true }
func (r *permissionReviewer) GuardrailReviewPolicy(_ string, job agent.ReviewJob, operationalFailure bool) (bool, bool) {
	return r.action && job == agent.ReviewJobAction, !operationalFailure
}
func (r *permissionReviewer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}
func (r *permissionReviewer) jobs() []agent.ReviewJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	jobs := make([]agent.ReviewJob, len(r.requests))
	for i := range r.requests {
		jobs[i] = r.requests[i].Job
	}
	return jobs
}

func TestBuiltinFloorPermissionReviewRunsOnChildLoopAndAllowsOnce(t *testing.T) {
	bash := &fakeShell{}
	policy := &permissionReviewPolicy{provenance: governance.AskProvenanceBuiltinSubstitutionFloor}
	permissionUsage := session.Usage{InputTokens: 3}
	reviewer := &permissionReviewer{
		assessment: agent.ReviewAcceptable, action: true,
		usageByJob: map[agent.ReviewJob]session.AuxiliaryUsage{
			agent.ReviewJobPermission: {Buckets: map[session.UsageKind]session.TokenUsage{
				session.UsageKindGuardrail: {Total: permissionUsage, Models: map[string]session.Usage{"provider/permission-review": permissionUsage}},
			}},
		},
	}
	child := agent.NewEngine(agent.Deps{
		LLM:          mockllm.New(substitutionAskTurns(2)...),
		Catalog:      catalogWith(t, bash),
		Policy:       policy,
		ToolReviewer: reviewer,
		Role:         "subagent",
	})
	sess := newSession(t, session.Limits{})
	events := drainWithTimeout(t, child.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "inspect"}))

	if got := len(bash.ran()); got != 2 {
		t.Fatalf("executions = %d, want 2", got)
	}
	if reviewer.count() != 4 || policy.learns != 0 {
		t.Fatalf("reviews=%d learns=%d, want 4/0", reviewer.count(), policy.learns)
	}
	if got := sess.TokenUsageSnapshot()[session.UsageKindGuardrail].Models["provider/permission-review"]; got.InputTokens != 6 {
		t.Fatalf("substitution-floor permission usage = %+v, want two reviews attributed to provider/permission-review", got)
	}
	for _, ev := range events {
		if ev.Type == session.EvPermissionAsk {
			t.Fatal("accepted permission review surfaced an ordinary permission ask")
		}
	}
	wantJobs := []agent.ReviewJob{agent.ReviewJobPermission, agent.ReviewJobAction, agent.ReviewJobPermission, agent.ReviewJobAction}
	if got := reviewer.jobs(); !reflect.DeepEqual(got, wantJobs) {
		t.Fatalf("review jobs = %v, want %v", got, wantJobs)
	}
}

type permissionReviewHook struct {
	entered chan struct{}
	release chan struct{}
	mutated []byte
}

func (h *permissionReviewHook) Run(ctx context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase != governance.PhasePreToolUse || ev.CallID != "c1" {
		return governance.HookOutcome{}, nil
	}
	close(h.entered)
	select {
	case <-h.release:
		return governance.HookOutcome{Mutated: h.mutated}, nil
	case <-ctx.Done():
		return governance.HookOutcome{}, ctx.Err()
	}
}

type permissionReviewShell struct {
	fakeShell
	readOnly bool
}

func (s *permissionReviewShell) ReadOnly() bool { return s.readOnly }

func TestPermissionReviewFreshnessAtExecutionAdmission(t *testing.T) {
	for _, path := range []string{"serial", "read_batch"} {
		for _, action := range []string{"acceptable", "checker_down_warn", "not_applicable"} {
			for _, change := range []string{"stable", "root_steer", "root_steer_and_mutation"} {
				t.Run(path+"/"+action+"/"+change, func(t *testing.T) {
					bash := &permissionReviewShell{readOnly: path == "read_batch"}
					policy := &permissionReviewPolicy{provenance: governance.AskProvenanceBuiltinSubstitutionFloor}
					reviewer := &permissionReviewer{assessment: agent.ReviewAcceptable, action: action != "not_applicable"}
					if action == "checker_down_warn" {
						reviewer.actionErr = errors.New("checker unavailable")
					}
					hook := &permissionReviewHook{entered: make(chan struct{}), release: make(chan struct{})}
					if change == "root_steer_and_mutation" {
						hook.mutated = []byte(`{"command":"cat $(new-target)"}`)
					}
					child := agent.NewEngine(agent.Deps{
						LLM: mockllm.New(
							mockllm.ToolCallTurn(toolCall("c1", "Shell", `{"command":"cat $(zap)"}`)),
							mockllm.ToolCallTurn(toolCall("c2", "Shell", `{"command":"cat $(zap)"}`)),
							mockllm.TextTurn("done"),
						),
						Catalog: catalogWith(t, bash), Policy: policy, ToolReviewer: reviewer, Hooks: hook, Role: "subagent",
					})
					run := child.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "original task"})
					t.Cleanup(run.Cancel)
					<-hook.entered
					if change != "stable" {
						agent.RefreshReviewTasksForTest(run, []session.Message{{Role: session.RoleUser, Text: "changed task", UserPromptProvenance: session.UserPromptProvenancePrincipal}})
					}
					close(hook.release)
					events := drainWithTimeout(t, run)

					wantRuns, wantPermissions := 2, 2
					if change == "root_steer" {
						wantRuns = 1 // Only c2 has renewed permission for the new root.
					}
					if hook.mutated != nil {
						wantPermissions++
					}
					if got := len(bash.ran()); got != wantRuns || policy.learns != 0 {
						t.Fatalf("executions=%d learns=%d, want %d/0", got, policy.learns, wantRuns)
					}
					var permissions, actions int
					for _, req := range reviewer.requests {
						switch req.Job {
						case agent.ReviewJobPermission:
							permissions++
						case agent.ReviewJobAction:
							actions++
						}
						var tasks []string
						for _, fact := range req.PrincipalFacts {
							if fact.Kind == "genuine_user_task" {
								tasks = append(tasks, fact.Statement)
							}
						}
						// A standalone child's goal is not root authority: the initial
						// review binds the real generation zero with no principal tasks.
						if change == "stable" || (req.Job == agent.ReviewJobPermission && permissions == 1) {
							if len(tasks) != 0 {
								t.Fatalf("child goal became root authority: %v", tasks)
							}
						} else if len(tasks) != 1 || tasks[0] != "changed task" {
							t.Fatalf("%s review did not see advanced root: %v", req.Job, tasks)
						}
						if hook.mutated != nil && req.EffectiveCall.ID == "c1" && (req.Job == agent.ReviewJobAction || permissions == 2) && string(req.EffectiveCall.Args) != string(hook.mutated) {
							t.Fatalf("renewed review did not bind effective args: %+v", req)
						}
					}
					wantActions := 2
					if !reviewer.action {
						wantActions = 0
					}
					if permissions != wantPermissions || actions != wantActions {
						t.Fatalf("permission/action reviews=%d/%d, want %d/%d", permissions, actions, wantPermissions, wantActions)
					}
					var stale, advisory int
					for _, ev := range events {
						if ev.Type == session.EvPermissionAsk {
							t.Fatal("unexpected ordinary permission ask")
						}
						if ev.ToolResult != nil && ev.ToolResult.CallID == "c1" && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "review context changed") {
							stale++
						}
						if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.Guardrail.Disposition == "pass_advisory" {
							advisory++
						}
					}
					if (stale == 1) != (change == "root_steer") || (action == "checker_down_warn" && advisory != 2) {
						t.Fatalf("stale rejections=%d advisory actions=%d", stale, advisory)
					}
				})
			}
		}
	}
}

func TestBuiltinFloorPermissionReviewNonAllowsPreservePermissionFallback(t *testing.T) {
	cases := []struct {
		name       string
		assessment agent.ReviewAssessment
		err        error
	}{
		{name: "prohibited", assessment: agent.ReviewProhibited},
		{name: "unresolved", assessment: agent.ReviewUnresolved},
		{name: "failure", assessment: agent.ReviewUnresolved, err: errors.New("provider failed")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bash := &fakeShell{}
			policy := &permissionReviewPolicy{provenance: governance.AskProvenanceBuiltinSubstitutionFloor}
			reviewer := &permissionReviewer{assessment: tc.assessment, err: tc.err}
			child := agent.NewEngine(agent.Deps{
				LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Shell", `{"command":"cat $(zap)"}`)), mockllm.TextTurn("done")),
				Catalog: catalogWith(t, bash), Policy: policy, ToolReviewer: reviewer, Role: "parallel:branch-1",
			})
			run := child.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "inspect"})
			sawAsk := false
			for ev := range run.Events() {
				if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
					sawAsk = true
					if err := run.Approve(ev.Ask.AskID, session.VerdictDeny); err != nil {
						t.Fatalf("failed to deny preserved permission ask: %v", err)
					}
				}
			}
			if !sawAsk || len(bash.ran()) != 0 || reviewer.count() != 1 || policy.learns != 0 {
				t.Fatalf("ask=%t executions=%d reviews=%d learns=%d", sawAsk, len(bash.ran()), reviewer.count(), policy.learns)
			}
		})
	}
}

func TestPermissionReviewCancellationCompletesWithoutConsumerSelfEmission(t *testing.T) {
	bash := &fakeShell{}
	reviewer := &permissionReviewer{entered: make(chan struct{}, 1), waitForCtx: true}
	child := agent.NewEngine(agent.Deps{
		LLM:          mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Shell", `{"command":"cat $(zap)"}`))),
		Catalog:      catalogWith(t, bash),
		Policy:       &permissionReviewPolicy{provenance: governance.AskProvenanceBuiltinSubstitutionFloor},
		ToolReviewer: reviewer,
		Role:         "member:researcher",
	})
	run := child.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "inspect"})
	<-reviewer.entered
	run.Cancel()
	events := drainWithTimeout(t, run)
	if len(bash.ran()) != 0 || reviewer.count() != 1 {
		t.Fatalf("executions=%d reviews=%d", len(bash.ran()), reviewer.count())
	}
	if got := lastResult(t, events).Stop; got != session.StopCancelled {
		t.Fatalf("stop = %q, want cancelled", got)
	}
}

type configuredSystemAskPolicy struct{}

func (configuredSystemAskPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Ask, Reason: "configured system ask", AskProvenance: governance.AskProvenanceConfigured}
}
func (configuredSystemAskPolicy) Learn(session.SessionID, session.ToolCall) {}

func TestConfiguredSystemScopeAskSkipsBothAutomatedReviewers(t *testing.T) {
	bash := &fakeShell{}
	child := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Shell", `{"command":"cat $(zap)","temp_scope":"system"}`)), mockllm.TextTurn("done")),
		Catalog: catalogWith(t, bash), Policy: configuredSystemAskPolicy{}, Role: "subagent",
	})
	task := agent.NewSubagentTool(child)
	contextual := &permissionReviewer{assessment: agent.ReviewAcceptable}
	adjudicator := &scriptedAdjudicator{script: []adjOutcome{allow("always allow")}}
	parent := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"inspect"}`)), mockllm.TextTurn("done")),
		Catalog: catalogWith(t, task), ToolReviewer: contextual, ChildAskReviewer: adjudicator,
	})
	events := drainWithTimeout(t, parent.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	if contextual.count() != 0 || adjudicator.count() != 0 || len(bash.ran()) != 0 {
		t.Fatalf("contextual reviews=%d adjudicator calls=%d executions=%d", contextual.count(), adjudicator.count(), len(bash.ran()))
	}
	if got := lastResult(t, events).Stop; got == session.StopError {
		t.Fatalf("parent failed: %s", lastResult(t, events).Error)
	}
}

func TestPermissionReviewRootReachesEveryDelegationFamily(t *testing.T) {
	newChild := func(t *testing.T, bash *fakeShell, turns ...mockllm.Turn) *agent.Engine {
		t.Helper()
		return agent.NewEngine(agent.Deps{
			LLM: mockllm.New(turns...), Catalog: catalogWith(t, bash),
			Policy: &permissionReviewPolicy{provenance: governance.AskProvenanceBuiltinSubstitutionFloor}, Role: "worker",
		})
	}
	runParent := func(t *testing.T, parentCall session.ToolCall, delegated tool.Tool, reviewer *permissionReviewer) []session.Event {
		t.Helper()
		parent := newEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.ToolCallTurn(parentCall), mockllm.TextTurn("done")),
			Catalog: catalogWith(t, delegated), ToolReviewer: reviewer,
		})
		return drainWithTimeout(t, parent.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	}

	t.Run("subagent", func(t *testing.T) {
		bash := &fakeShell{}
		reviewer := &permissionReviewer{assessment: agent.ReviewAcceptable}
		child := newChild(t, bash, mockllm.ToolCallTurn(toolCall("c1", "Shell", `{"command":"cat $(zap)"}`)), mockllm.TextTurn("done"))
		events := runParent(t, toolCall("p1", "Subagent", `{"prompt":"inspect"}`), agent.NewSubagentTool(child), reviewer)
		assertDelegationPermissionReview(t, events, bash, reviewer)
	})

	t.Run("parallel", func(t *testing.T) {
		bash := &fakeShell{}
		reviewer := &permissionReviewer{assessment: agent.ReviewAcceptable}
		child := newChild(t, bash, mockllm.ToolCallTurn(toolCall("c1", "Shell", `{"command":"cat $(zap)"}`)), mockllm.TextTurn("done"))
		events := runParent(t, toolCall("p1", "Parallel", `{"tasks":["inspect"]}`), agent.NewParallelTool(child, &memForker{}), reviewer)
		assertDelegationPermissionReview(t, events, bash, reviewer)
	})

	t.Run("team", func(t *testing.T) {
		bash := &fakeShell{}
		reviewer := &permissionReviewer{assessment: agent.ReviewAcceptable}
		factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
			catalog := tool.NewCatalog()
			for _, memberTool := range agent.MemberTools(tm, spec.Name, nil) {
				catalog.MustRegister(memberTool)
			}
			catalog.MustRegister(bash)
			member := agent.NewEngine(agent.Deps{
				LLM: mockllm.New(
					mockllm.ToolCallTurn(toolCall("c1", "Shell", `{"command":"cat $(zap)"}`)),
					mockllm.TextTurn("inspected"), mockllm.TextTurn("synthesis"),
				),
				Catalog: catalog, Policy: &permissionReviewPolicy{provenance: governance.AskProvenanceBuiltinSubstitutionFloor}, Role: "member:" + spec.Name,
			})
			return agent.MemberBuild{Engine: member, IsolateReadOnly: true}
		}
		teamTool := agent.NewTeamTool(factory, agent.WithTeamToolReadOnlyForker(&recordingSubagentForker{}))
		events := runParent(t, toolCall("p1", "Team", `{"goal":"inspect","members":[{"name":"lead","role":"inspect"}]}`), teamTool, reviewer)
		assertDelegationPermissionReview(t, events, bash, reviewer)
	})
}

func assertDelegationPermissionReview(t *testing.T, events []session.Event, bash *fakeShell, reviewer *permissionReviewer) {
	t.Helper()
	if len(bash.ran()) != 1 || reviewer.count() != 1 {
		t.Fatalf("executions=%d reviews=%d, want 1/1", len(bash.ran()), reviewer.count())
	}
	if got := lastResult(t, events).Stop; got == session.StopError {
		t.Fatalf("parent failed: %s", lastResult(t, events).Error)
	}
}
