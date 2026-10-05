package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type deadlineEvidencePreparer struct {
	delay         time.Duration
	calls         int
	ignoreContext bool
	deadline      time.Time
	err           error
}

func (p *deadlineEvidencePreparer) PrepareReviewEvidence(ctx context.Context, _ ReviewEvidencePreparation) (PreparedReviewEvidence, error) {
	p.calls++
	p.deadline, _ = ctx.Deadline()
	if p.ignoreContext {
		<-time.After(p.delay)
		return PreparedReviewEvidence{Complete: true}, nil
	}
	select {
	case <-time.After(p.delay):
		return PreparedReviewEvidence{Complete: true}, nil
	case <-ctx.Done():
		if p.err != nil {
			return PreparedReviewEvidence{}, p.err
		}
		return PreparedReviewEvidence{}, ctx.Err()
	}
}

type enforcingDeadlineReviewer struct{ terminalFailureReviewer }

func (r *enforcingDeadlineReviewer) GuardrailReviewPolicy(_ string, job ReviewJob, _ bool) (bool, bool) {
	return job == r.job, true
}

func TestReviewPreparationTimeoutHonorsEnforcement(t *testing.T) {
	for _, job := range []ReviewJob{ReviewJobAction, ReviewJobInbound} {
		t.Run(string(job), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				executions := 0
				catalog := tool.NewCatalog()
				catalog.MustRegister(revisionActionTool{executions: &executions})
				reviewer := &enforcingDeadlineReviewer{terminalFailureReviewer: terminalFailureReviewer{job: job}}
				engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done")), Catalog: catalog, Policy: terminalFailurePolicy{job: job}, ToolReviewer: reviewer, ReviewEvidencePreparer: &deadlineEvidencePreparer{delay: 91 * time.Second}})
				env := memEnv("/ws")
				sess := session.New("enforced", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
				found := false
				for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "act"}).Events() {
					if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.Guardrail.ReasonCode == string(ReviewFailureTimeout) {
						found = true
						if ev.Hook.Guardrail.Disposition == "pass_advisory" {
							t.Fatal("enforcing timeout passed advisory")
						}
					}
				}
				wantExec := 0
				if job == ReviewJobInbound {
					wantExec = 1
				}
				if !found || executions != wantExec || reviewer.reviews != 0 || reviewer.recorded == nil {
					t.Fatalf("found=%t executions=%d reviews=%d recorded=%v", found, executions, reviewer.reviews, reviewer.recorded)
				}
			})
		})
	}
}

type deadlineGrantReviewer struct {
	terminalFailureReviewer
	digestDelay time.Duration
	lookupDelay time.Duration
	records     int
}

func (r *deadlineGrantReviewer) GrantDigest(ToolReviewRequest) (string, bool) {
	time.Sleep(r.digestDelay)
	return "grant", true
}
func (r *deadlineGrantReviewer) AllowsGrant(string) bool {
	time.Sleep(r.lookupDelay)
	return true
}
func (*deadlineGrantReviewer) ArmGrant(string, string) {}
func (r *deadlineGrantReviewer) RecordGuardrailReviewFailure(result ToolReviewResult, err error) {
	r.records++
	r.terminalFailureReviewer.RecordGuardrailReviewFailure(result, err)
}

func TestActionPreparationFailureRecordedBeforeAssessment(t *testing.T) {
	for _, stage := range []string{"preparation", "late_preparation", "grant_digest", "grant_lookup", "before_checker", "parent_cancelled"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				reviewer := &deadlineGrantReviewer{terminalFailureReviewer: terminalFailureReviewer{job: ReviewJobAction}}
				preparer := &deadlineEvidencePreparer{}
				switch stage {
				case "preparation", "late_preparation", "parent_cancelled":
					preparer.delay = 91 * time.Second
					preparer.ignoreContext = stage == "late_preparation"
				case "grant_digest":
					reviewer.digestDelay = 91 * time.Second
				case "grant_lookup":
					reviewer.lookupDelay = 91 * time.Second
				}
				if stage == "parent_cancelled" {
					timer := time.AfterFunc(time.Second, cancel)
					defer timer.Stop()
				}
				r := &Run{reviewRoot: newReviewRoot(reviewer, preparer, nil)}
				env := memEnv("/ws")
				sess := session.New("prep-health", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
				assessment := NewEngine(Deps{}).prepareActionAssessment(ctx, r, sess, env, session.NewToolCall("call", "Act", []byte(`{}`)))
				defer assessment.cancel()
				wantRecords := 1
				if stage == "before_checker" || stage == "parent_cancelled" {
					wantRecords = 0
				}
				if reviewer.records != wantRecords {
					t.Errorf("preparation recorded %d failures, want %d before checker starts", reviewer.records, wantRecords)
				}
				if stage == "before_checker" {
					if !assessment.grantHit {
						t.Fatal("expected grant before deadline")
					}
					time.Sleep(91 * time.Second)
				}
				assessAction(ctx, r, &assessment)
				if stage != "parent_cancelled" {
					wantRecords = 1
					if reviewFailureCode(assessment.err) != ReviewFailureTimeout || assessment.result.Assessment != ReviewUnresolved || assessment.grantHit {
						t.Errorf("expired assessment=%+v", assessment)
					}
				}
				if reviewer.records != wantRecords || reviewer.reviews != 0 {
					t.Fatalf("records=%d want %d; checker calls=%d", reviewer.records, wantRecords, reviewer.reviews)
				}
			})
		})
	}
}

type terminalContextEvidenceError struct{ error }

func (e terminalContextEvidenceError) Unwrap() error                      { return e.error }
func (terminalContextEvidenceError) GuardrailReviewTerminalFailure() bool { return true }

func TestPreparationTerminalContextErrorIsNotDowngraded(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				preparer := &deadlineEvidencePreparer{delay: 91 * time.Second, err: terminalContextEvidenceError{cause}}
				env := memEnv("/ws")
				sess := session.New("terminal-context", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
				_, err := NewEngine(Deps{}).prepareReviewEvidence(ctx, &Run{reviewRoot: newReviewRoot(nil, preparer, nil)}, sess, env, ToolReviewRequest{}, nil)
				var terminal GuardrailReviewTerminalFailure
				if !errors.As(err, &terminal) || !terminal.GuardrailReviewTerminalFailure() {
					t.Fatalf("terminal error downgraded: %v", err)
				}
			})
		})
	}
}

type cancelledReviewChecker struct{ terminalFailureReviewer }

func (r *cancelledReviewChecker) Review(ctx context.Context, _ ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, session.AuxiliaryUsage, error) {
	r.reviews++
	<-ctx.Done()
	return ToolReviewResult{Assessment: ReviewUnresolved}, session.AuxiliaryUsage{}, ctx.Err()
}

func TestResolveInboundCancellationOnlyWithholdsReviewedResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	call := session.NewToolCall("call", "Act", []byte(`{}`))
	original := session.NewToolResultWithParts(call.ID, "original result", []session.Content{{BlockKind: session.BlockText, Text: "original part"}})
	engine := NewEngine(Deps{Interactive: true})
	for _, applies := range []bool{false, true} {
		name := "unreviewed"
		if applies {
			name = "reviewed"
		}
		t.Run(name, func(t *testing.T) {
			// Neither early return may publish review events or asks.
			got, cancelled, _ := engine.resolveInbound(ctx, &Run{diag: port.NopDiagnostics{}}, nil, memEnv("/ws"), 0, call, original, inboundAssessment{applies: applies})
			want := original
			if applies {
				want = session.NewToolError(call.ID, withheldResultText+": review was cancelled")
			}
			if cancelled != applies || !reflect.DeepEqual(got, want) {
				t.Fatalf("result=%+v cancelled=%t, want %+v cancelled=%t", got, cancelled, want, applies)
			}
		})
	}
}

func TestCallerCancellationDoesNotPublishReviewOutage(t *testing.T) {
	for _, job := range []ReviewJob{ReviewJobAction, ReviewJobInbound} {
		for _, stage := range []string{"preparation", "checker"} {
			for _, deadline := range []bool{false, true} {
				name := string(job) + "/" + stage + "/cancel"
				if deadline {
					name = string(job) + "/" + stage + "/deadline"
				}
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						ctx, cancel := context.WithCancel(context.Background())
						if deadline {
							cancel()
							ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
						} else {
							timer := time.AfterFunc(15*time.Second, cancel)
							defer timer.Stop()
						}
						defer cancel()
						executions := 0
						catalog := tool.NewCatalog()
						catalog.MustRegister(revisionActionTool{executions: &executions})
						reviewer := &cancelledReviewChecker{terminalFailureReviewer: terminalFailureReviewer{job: job}}
						var preparer ReviewEvidencePreparer
						if stage == "preparation" {
							preparer = &deadlineEvidencePreparer{delay: 91 * time.Second}
						}
						details := &rootContractSink{}
						engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done")), Catalog: catalog, Policy: terminalFailurePolicy{job: job}, ToolReviewer: reviewer, ReviewEvidencePreparer: preparer, ReviewDetails: details, Interactive: true})
						env := memEnv("/ws")
						sess := session.New("caller-cancel", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
						stopped := false
						for ev := range engine.Run(ctx, sess, env, RunRequest{Text: "act"}).Events() {
							if ev.Result != nil && ev.Result.Stop == session.StopCancelled {
								stopped = true
							}
							if ev.Type == session.EvPermissionAsk || (ev.Hook != nil && ev.Hook.Guardrail != nil) {
								t.Errorf("cancelled review published %+v", ev)
							}
						}
						if !stopped {
							t.Error("caller cancellation did not stop the run")
						}
						if reviewer.recorded != nil || details.got.ReviewID != "" {
							t.Fatalf("cancelled review recorded failure=%v detail=%+v", reviewer.recorded, details.got)
						}
					})
				})
			}
		}
	}
}

func TestReviewInheritsShorterParentDeadlineAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		executions := 0
		catalog := tool.NewCatalog()
		catalog.MustRegister(revisionActionTool{executions: &executions})
		reviewer := &terminalFailureReviewer{job: ReviewJobAction}
		preparer := &deadlineEvidencePreparer{delay: 91 * time.Second}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		parentDeadline, _ := ctx.Deadline()
		engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done")), Catalog: catalog, Policy: terminalFailurePolicy{job: ReviewJobAction}, ToolReviewer: reviewer, ReviewEvidencePreparer: preparer})
		env := memEnv("/ws")
		sess := session.New("parent-budget", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
		for range engine.Run(ctx, sess, env, RunRequest{Text: "act"}).Events() {
		}
		if preparer.calls != 1 || !preparer.deadline.Equal(parentDeadline) || reviewer.reviews != 0 || executions != 0 {
			t.Fatalf("prepares=%d deadline=%v parent=%v reviews=%d executions=%d", preparer.calls, preparer.deadline, parentDeadline, reviewer.reviews, executions)
		}
	})
}

func TestReviewPreparationDeadlineIncludesEvidenceForActionAndInbound(t *testing.T) {
	for _, job := range []ReviewJob{ReviewJobAction, ReviewJobInbound} {
		for _, late := range []bool{false, true} {
			name := string(job)
			if late {
				name += "_late_success"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					executions := 0
					catalog := tool.NewCatalog()
					catalog.MustRegister(revisionActionTool{executions: &executions})
					reviewer := &terminalFailureReviewer{job: job}
					preparer := &deadlineEvidencePreparer{delay: 91 * time.Second, ignoreContext: late}
					engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done")), Catalog: catalog, Policy: terminalFailurePolicy{job: job}, ToolReviewer: reviewer, ReviewEvidencePreparer: preparer})
					env := memEnv("/ws")
					sess := session.New("deadline", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
					var events []session.Event
					for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "act"}).Events() {
						events = append(events, ev)
					}
					if reviewer.reviews != 0 || preparer.calls != 1 || executions != 1 {
						t.Fatalf("reviews=%d prepares=%d executions=%d", reviewer.reviews, preparer.calls, executions)
					}
					phase := governance.PhasePreToolUse
					if job == ReviewJobInbound {
						phase = governance.PhasePostToolUse
					}
					for _, ev := range events {
						if ev.Hook != nil && ev.Hook.Phase == string(phase) && ev.Hook.Guardrail != nil {
							if ev.Hook.Guardrail.ReasonCode != string(ReviewFailureTimeout) || ev.Hook.Guardrail.Disposition != "pass_advisory" {
								t.Fatalf("review=%+v", ev.Hook.Guardrail)
							}
							return
						}
					}
					t.Fatal("missing timeout review")
				})
			})
		}
	}
}
