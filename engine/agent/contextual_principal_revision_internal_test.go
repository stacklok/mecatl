package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type revisionBlockingReviewer struct {
	entered  chan ToolReviewRequest
	release  chan struct{}
	mu       sync.Mutex
	requests []ToolReviewRequest
}

func (r *revisionBlockingReviewer) Review(_ context.Context, req ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	r.entered <- req
	<-r.release
	return ToolReviewResult{Assessment: ReviewAcceptable}, nil
}

type revisionReadTool struct{ executions *int }

func (revisionReadTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Inspect", Description: "inspect", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (revisionReadTool) ReadOnly() bool { return true }
func (t revisionReadTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	*t.executions++
	return session.NewToolResult(call.ID, "executed"), nil
}

type admissionBlockingPolicy struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (p *admissionBlockingPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	// Two initial sibling authorizations precede the first final reauthorization.
	if call == 3 {
		p.entered <- struct{}{}
		<-p.release
	}
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (*admissionBlockingPolicy) Learn(session.SessionID, session.ToolCall) {}

func TestReadBatchFinalAdmissionRejectsContextChange(t *testing.T) {
	reviewer := &revisionGrantReviewer{}
	root := newReviewRoot(reviewer, nil, nil)
	root.establishInstructions(nil)
	root.refreshTasks([]session.Message{{Role: session.RoleUser, Text: "old root task", UserPromptProvenance: session.UserPromptProvenancePrincipal}})
	executions := 0
	catalog := tool.NewCatalog()
	catalog.MustRegister(revisionReadTool{executions: &executions})
	policy := &admissionBlockingPolicy{entered: make(chan struct{}, 1), release: make(chan struct{})}
	calls := []session.ToolCall{
		session.NewToolCall("read-1", "Inspect", json.RawMessage(`{}`)),
		session.NewToolCall("read-2", "Inspect", json.RawMessage(`{}`)),
	}
	engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("done")), Catalog: catalog, Policy: policy, Role: "subagent"})
	env := memEnv("/ws")
	sess := session.New("batch-admission", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	run := engine.Run(context.Background(), sess, env, RunRequest{Text: "worker goal", reviewRoot: root, reviewIsolated: true})
	<-policy.entered
	root.refreshTasks([]session.Message{
		{Role: session.RoleUser, Text: "old root task", UserPromptProvenance: session.UserPromptProvenancePrincipal},
		{Role: session.RoleUser, Text: "committed root steer", UserPromptProvenance: session.UserPromptProvenancePrincipal},
	})
	close(policy.release)
	stale := 0
	for ev := range run.Events() {
		if ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, "review context changed") {
			stale++
		}
	}
	if executions != 0 || stale != 2 {
		t.Fatalf("executions=%d stale results=%d, want 0/2", executions, stale)
	}
}

type revisionActionTool struct{ executions *int }

func (revisionActionTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Act", Description: "act", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (revisionActionTool) ReadOnly() bool { return false }
func (t revisionActionTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	*t.executions++
	return session.NewToolResult(call.ID, "executed"), nil
}

type revisionGrantReviewer struct {
	mu    sync.Mutex
	armed int
}

func (*revisionGrantReviewer) Review(context.Context, ToolReviewRequest, ReviewEvidenceSource) (ToolReviewResult, error) {
	return ToolReviewResult{Assessment: ReviewAcceptable}, nil
}
func (*revisionGrantReviewer) GrantDigest(ToolReviewRequest) (string, bool) { return "digest", true }
func (*revisionGrantReviewer) AllowsGrant(string) bool                      { return false }
func (r *revisionGrantReviewer) ArmGrant(string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armed++
}

type blockingRevisionPreparer struct {
	entered chan struct{}
	release chan struct{}
}

func (p *blockingRevisionPreparer) PrepareReviewEvidence(context.Context, ReviewEvidencePreparation) (PreparedReviewEvidence, error) {
	p.entered <- struct{}{}
	<-p.release
	return PreparedReviewEvidence{Complete: true}, nil
}

func TestPostActionGrantRejectsPrincipalChangeDuringEvidencePreparation(t *testing.T) {
	reviewer := &revisionGrantReviewer{}
	preparer := &blockingRevisionPreparer{entered: make(chan struct{}, 1), release: make(chan struct{})}
	root := newReviewRoot(reviewer, preparer, nil)
	root.establishInstructions(nil)
	root.refreshTasks([]session.Message{{Role: session.RoleUser, Text: "approved task", UserPromptProvenance: session.UserPromptProvenancePrincipal}})
	_, _, approvedRevision := root.principalSnapshotWithRevision()
	engine := NewEngine(Deps{Catalog: tool.NewCatalog()})
	run := &Run{reviewRoot: root}
	env := memEnv("/ws")
	sess := session.New("grant-revision", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	call := session.NewToolCall("call", "Act", json.RawMessage(`{}`))
	done := make(chan struct{})
	go func() {
		engine.armPostActionGrant(context.Background(), run, sess, env, call, true, approvedRevision, reviewer, true, session.NewToolResult(call.ID, "ok"), nil, false)
		close(done)
	}()
	<-preparer.entered
	root.refreshTasks([]session.Message{{Role: session.RoleUser, Text: "changed task", UserPromptProvenance: session.UserPromptProvenancePrincipal}})
	close(preparer.release)
	<-done
	reviewer.mu.Lock()
	defer reviewer.mu.Unlock()
	if reviewer.armed != 0 {
		t.Fatalf("stale approval armed %d grants", reviewer.armed)
	}
}

func TestPostActionGrantStableRevisionArmsOnce(t *testing.T) {
	reviewer := &revisionGrantReviewer{}
	root := newReviewRoot(reviewer, nil, nil)
	root.establishInstructions(nil)
	root.refreshTasks([]session.Message{{Role: session.RoleUser, Text: "stable task", UserPromptProvenance: session.UserPromptProvenancePrincipal}})
	_, _, revision := root.principalSnapshotWithRevision()
	engine := NewEngine(Deps{Catalog: tool.NewCatalog()})
	run := &Run{reviewRoot: root}
	env := memEnv("/ws")
	sess := session.New("grant-stable", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	call := session.NewToolCall("call", "Act", json.RawMessage(`{}`))
	engine.armPostActionGrant(context.Background(), run, sess, env, call, true, revision, reviewer, true, session.NewToolResult(call.ID, "ok"), nil, false)
	if reviewer.armed != 1 {
		t.Fatalf("stable approval armed %d grants, want 1", reviewer.armed)
	}
}

func TestActionReviewRejectsRootPrincipalRevisionChange(t *testing.T) {
	reviewer := &revisionBlockingReviewer{entered: make(chan ToolReviewRequest, 1), release: make(chan struct{})}
	root := newReviewRoot(reviewer, nil, nil, 1)
	oldTask := session.Message{Role: session.RoleUser, Text: "old task", UserPromptProvenance: session.UserPromptProvenancePrincipal}
	root.refreshTasks([]session.Message{oldTask})

	executions := 0
	catalog := tool.NewCatalog()
	catalog.MustRegister(revisionActionTool{executions: &executions})
	call := session.NewToolCall("act-1", "Act", json.RawMessage(`{}`))
	engine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")),
		Catalog: catalog,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Role:    "subagent",
	})
	env := memEnv("/ws")
	sess := session.New("revision-root", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	run := engine.Run(context.Background(), sess, env, RunRequest{Text: "worker goal", reviewRoot: root, reviewIsolated: true})

	first := <-reviewer.entered
	if len(first.PrincipalFacts) == 0 || first.PrincipalFacts[0].Statement != "old task" {
		t.Fatalf("first review principal facts = %+v", first.PrincipalFacts)
	}
	root.refreshTasks([]session.Message{
		oldTask,
		{Role: session.RoleUser, Text: "accepted root steer", UserPromptProvenance: session.UserPromptProvenancePrincipal},
	})
	close(reviewer.release)

	var staleResult string
	for event := range run.Events() {
		if event.ToolResult != nil && event.ToolResult.CallID == call.ID {
			staleResult = event.ToolResult.Content
		}
	}
	if executions != 0 {
		t.Fatalf("stale action executed %d times", executions)
	}
	if !strings.Contains(staleResult, "review context changed") || !strings.Contains(staleResult, "retry") {
		t.Fatalf("stale result = %q", staleResult)
	}

	if err := sess.Reopen(); err != nil {
		t.Fatal(err)
	}
	reviewer.entered = make(chan ToolReviewRequest, 1)
	reviewer.release = make(chan struct{})
	secondCall := session.NewToolCall("act-2", "Act", json.RawMessage(`{}`))
	engine = NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(secondCall), mockllm.TextTurn("done")), Catalog: catalog, Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Role: "subagent"})
	run = engine.Run(context.Background(), sess, env, RunRequest{Text: "worker retry", reviewRoot: root, reviewIsolated: true})
	second := <-reviewer.entered
	if len(second.PrincipalFacts) == 0 || second.PrincipalFacts[0].Statement != "accepted root steer" {
		t.Fatalf("second review principal facts = %+v", second.PrincipalFacts)
	}
	close(reviewer.release)
	for range run.Events() {
	}
	if executions != 1 {
		t.Fatalf("freshly reviewed action executions = %d, want 1", executions)
	}
}
