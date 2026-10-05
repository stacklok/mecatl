package agent

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func (r *lateAcceptReviewer) GuardrailReviewPolicy(_ string, job ReviewJob, failure bool) (bool, bool) {
	return job == r.job, r.enforce || !failure
}

type lateAcceptReviewer struct {
	calls     int
	remaining time.Duration
	delay     time.Duration
	job       ReviewJob
	err       error
	enforce   bool
}

func (r *lateAcceptReviewer) Review(ctx context.Context, _ ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, session.AuxiliaryUsage, error) {
	r.calls++
	deadline, _ := ctx.Deadline()
	r.remaining = time.Until(deadline)
	<-time.After(r.delay)
	return ToolReviewResult{Assessment: ReviewAcceptable}, session.AuxiliaryUsage{}, r.err
}

type slowGrantReviewer struct{ siblingBudgetReviewer }

func (*slowGrantReviewer) GrantDigest(ToolReviewRequest) (string, bool) { return "digest", true }
func (*slowGrantReviewer) AllowsGrant(string) bool                      { <-time.After(91 * time.Second); return true }
func (*slowGrantReviewer) ArmGrant(string, string)                      {}

func TestExpiredGrantLookupDoesNotApproveReview(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		executions := 0
		catalog := tool.NewCatalog()
		catalog.MustRegister(revisionActionTool{executions: &executions})
		reviewer := &slowGrantReviewer{}
		engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done")), Catalog: catalog, Policy: terminalFailurePolicy{job: ReviewJobAction}, ToolReviewer: reviewer})
		env := memEnv("/ws")
		sess := session.New("grant-timeout", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
		timedOut := false
		for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "act"}).Events() {
			if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.Guardrail.ReasonCode == string(ReviewFailureTimeout) {
				timedOut = true
			}
		}
		if !timedOut || executions != 1 || len(reviewer.called) != 0 {
			t.Fatalf("timeout=%t executions=%d reviews=%v", timedOut, executions, reviewer.called)
		}
	})
}

func TestObservedTerminalFailureSurvivesReviewDeadline(t *testing.T) {
	for _, job := range []ReviewJob{ReviewJobAction, ReviewJobInbound} {
		t.Run(string(job), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				executions := 0
				catalog := tool.NewCatalog()
				catalog.MustRegister(revisionActionTool{executions: &executions})
				reviewer := &lateAcceptReviewer{job: job, delay: 91 * time.Second, err: terminalReviewFailure(ReviewFailureEvidenceFailure)}
				var working []session.Message
				provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { working = append(working, req.Messages...) })}, mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done"))
				engine := NewEngine(Deps{LLM: provider, Catalog: catalog, Policy: terminalFailurePolicy{job: job}, ToolReviewer: reviewer})
				env := memEnv("/ws")
				sess := session.New("terminal-late", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
				found := false
				var events []session.Event
				for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "act"}).Events() {
					events = append(events, ev)
					if ev.Hook != nil && ev.Hook.Guardrail != nil {
						if ev.Hook.Guardrail.ReasonCode == string(ReviewFailureEvidenceFailure) {
							found = true
							if ev.Hook.Guardrail.Disposition == "pass_advisory" {
								t.Fatal("terminal failure allowed advisory")
							}
						}
					}
				}
				wantExec := 0
				if job == ReviewJobInbound {
					wantExec = 1
					assertInboundDeadlineWithheld(t, events, sess.Conversation.Messages, working)
				}
				if !found || executions != wantExec || reviewer.calls != 1 {
					t.Fatalf("terminal found=%t executions=%d reviews=%d", found, executions, reviewer.calls)
				}
			})
		})
	}
}

func assertInboundDeadlineWithheld(t *testing.T, events []session.Event, history, working []session.Message) {
	t.Helper()
	var streamed []session.Message
	for _, ev := range events {
		if ev.ToolResult != nil {
			streamed = append(streamed, session.Message{ToolResult: ev.ToolResult})
		}
	}
	for name, messages := range map[string][]session.Message{"events": streamed, "history": history, "working model": working} {
		found := false
		for _, message := range messages {
			if result := message.ToolResult; result != nil {
				found = true
				if !result.IsError || !strings.Contains(result.Content, withheldResultText) {
					t.Errorf("%s exposed original result instead of withholding it: %+v", name, result)
				}
			}
		}
		if !found {
			t.Errorf("%s has no synthetic withheld result", name)
		}
	}
}

func TestReviewPreparationAndCheckerShareDeadline(t *testing.T) {
	for _, job := range []ReviewJob{ReviewJobAction, ReviewJobInbound} {
		for _, enforce := range []bool{false, true} {
			name := string(job) + "/warn"
			if enforce {
				name = string(job) + "/enforce"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					executions := 0
					catalog := tool.NewCatalog()
					catalog.MustRegister(revisionActionTool{executions: &executions})
					reviewer := &lateAcceptReviewer{delay: 41 * time.Second, job: job, enforce: enforce}
					preparer := &deadlineEvidencePreparer{delay: 50 * time.Second}
					var working []session.Message
					provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { working = append(working, req.Messages...) })}, mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done"))
					engine := NewEngine(Deps{LLM: provider, Catalog: catalog, Policy: terminalFailurePolicy{job: job}, ToolReviewer: reviewer, ReviewEvidencePreparer: preparer})
					env := memEnv("/ws")
					sess := session.New("budget", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
					var events []session.Event
					var failure bool
					for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "act"}).Events() {
						events = append(events, ev)
						if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.Guardrail.ReasonCode == string(ReviewFailureTimeout) {
							failure = true
						}
					}
					wantExec := 1
					if enforce && job == ReviewJobAction {
						wantExec = 0
					}
					if reviewer.calls != 1 || reviewer.remaining != 40*time.Second || !failure || executions != wantExec {
						t.Fatalf("calls=%d remaining=%v failure=%t executions=%d", reviewer.calls, reviewer.remaining, failure, executions)
					}
					if enforce && job == ReviewJobInbound {
						assertInboundDeadlineWithheld(t, events, sess.Conversation.Messages, working)
					}
				})
			})
		}
	}
}
