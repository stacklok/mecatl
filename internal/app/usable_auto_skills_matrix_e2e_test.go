package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type stockSkillEvaluator struct {
	verdict learning.EvaluationVerdict
	err     error
}

func (e stockSkillEvaluator) Evaluate(context.Context, learning.SkillEvaluationRequest) (learning.SkillEvaluation, error) {
	if e.err != nil {
		return learning.SkillEvaluation{}, e.err
	}
	return learning.SkillEvaluation{Verdict: e.verdict, FixtureIDs: []string{"fixture-1"}, Baseline: "before", Treatment: "after"}, nil
}

func TestUsableAutoSkillsStockBuildPolicyMatrix(t *testing.T) {
	secretErr := errors.New("secret evaluator detail must not escape")
	cases := []struct {
		name              string
		mode              learning.Mode
		policy            learning.SkillActivationPolicy
		evaluator         learning.SkillEvaluator
		projectPolicy     bool
		trustProject      bool
		wantState         learning.SkillState
		wantOperation     string
		wantActivity      learning.ActivityKind
		wantActivityCount int
		wantPublished     bool
	}{
		{name: "omitted validated nil evaluator", mode: learning.Auto, trustProject: true, wantState: learning.SkillActive, wantOperation: "activate_validated", wantActivity: learning.ActivitySkillActivatedValidated, wantPublished: true},
		{name: "explicit abstain", mode: learning.Auto, policy: learning.SkillActivationValidated, evaluator: stockSkillEvaluator{verdict: learning.EvaluationAbstain}, trustProject: true, wantState: learning.SkillActive, wantOperation: "activate_validated", wantActivity: learning.ActivitySkillActivatedValidated, wantPublished: true},
		{name: "explicit pass", mode: learning.Auto, policy: learning.SkillActivationValidated, evaluator: stockSkillEvaluator{verdict: learning.EvaluationPass}, trustProject: true, wantState: learning.SkillActive, wantOperation: "activate", wantActivity: learning.ActivitySkillActivatedEvaluated, wantPublished: true},
		{name: "explicit fail", mode: learning.Auto, policy: learning.SkillActivationValidated, evaluator: stockSkillEvaluator{verdict: learning.EvaluationFail}, trustProject: true, wantState: learning.SkillRejected, wantOperation: "record_evaluation", wantActivity: learning.ActivitySkillRejected},
		{name: "evaluator error retry", mode: learning.Auto, policy: learning.SkillActivationValidated, evaluator: stockSkillEvaluator{err: secretErr}, trustProject: true, wantState: learning.SkillRejected, wantOperation: "record_evaluation", wantActivity: learning.ActivityFailed, wantActivityCount: 2},
		{name: "explicit evaluated", mode: learning.Auto, policy: learning.SkillActivationEvaluated, trustProject: true, wantState: learning.SkillStaged, wantOperation: "stage", wantActivity: learning.ActivitySkillStaged},
		{name: "review", mode: learning.Review, policy: learning.SkillActivationValidated, trustProject: true, wantState: learning.SkillStaged, wantOperation: "stage", wantActivity: learning.ActivitySkillStaged},
		{name: "off", mode: learning.Off, trustProject: true},
		{name: "trusted project tighten", mode: learning.Auto, policy: learning.SkillActivationValidated, projectPolicy: true, trustProject: true, wantState: learning.SkillStaged, wantOperation: "stage", wantActivity: learning.ActivitySkillStaged},
		{name: "untrusted project ignored", mode: learning.Auto, policy: learning.SkillActivationValidated, projectPolicy: true, trustProject: false, wantState: learning.SkillStaged, wantOperation: "stage", wantActivity: learning.ActivitySkillStaged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.projectPolicy {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			}
			workspace := t.TempDir()
			if tc.projectPolicy {
				if err := os.MkdirAll(filepath.Join(workspace, ".mecatl"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(workspace, ".mecatl", "settings.yaml"), []byte("learning:\n  skills:\n    activation: evaluated\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var metricsMu sync.Mutex
			var activities []learning.Activity
			diag := newCapturingDiagnostics()
			turns := []mockllm.Turn{mockllm.TextTurn("I completed and verified the workflow.")}
			if tc.mode != learning.Off {
				turns = append(turns, mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"procedure","name":"matrix-skill","title":"Matrix skill","body":"Follow the exact matrix procedure.","evidence":["m:0"]}]}`))
			}
			turns = append(turns,
				mockllm.ToolCallTurn(session.NewToolCall("matrix-use", "Skill", []byte(`{"name":"matrix-skill"}`))),
				mockllm.TextTurn("matrix use complete"),
			)
			provider := mockllm.New(turns...)
			built, err := Build(context.Background(), Config{
				Model: "test-model", Workspace: workspace, TrustProject: tc.trustProject, Headless: !tc.trustProject, PermissionsConventional: tc.projectPolicy,
				LearningMode: tc.mode, SkillActivationPolicy: tc.policy, SkillEvaluator: tc.evaluator,
				UserModelDir: t.TempDir(), MemoryDir: t.TempDir(), Diagnostics: diag,
				LearningMetricsEmitter: func(activity learning.Activity) {
					metricsMu.Lock()
					activities = append(activities, activity)
					metricsMu.Unlock()
				},
				envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "test-key"}),
				liveModelHTTPClient: offlineHTTPClient(),
				providerConstructor: func(Config, string, string, string) port.LLMProvider { return provider },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: "matrix", GrantType: session.GrantTypeUser})
			sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			run, err := built.Service.StartRun(ctx, sess.ID, "Learn this procedure after completing and verifying it")
			if err != nil {
				t.Fatal(err)
			}
			for event := range run.Events() {
				if event.Type == session.EvPermissionAsk && event.Ask != nil {
					run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
				}
				if event.Type == session.EvResult && event.Result != nil && event.Result.Stop == session.StopError {
					t.Fatalf("run failed: %s", event.Result.Error)
				}
			}

			var listed *mecatlv1.ListLearnedSkillsResponse
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				listed, err = built.Service.ListLearnedSkills(ctx, &mecatlv1.ListLearnedSkillsRequest{Project: workspace})
				if err != nil {
					t.Fatal(err)
				}
				if tc.wantState == "" {
					time.Sleep(100 * time.Millisecond)
					break
				}
				if len(listed.GetSkills()) == 1 {
					got := listed.GetSkills()[0]
					if learning.SkillState(got.GetState()) == tc.wantState && len(got.GetReceipts()) > 0 && got.GetReceipts()[len(got.GetReceipts())-1].GetOperation() == tc.wantOperation {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if tc.wantState == "" {
				if len(listed.GetSkills()) != 0 {
					t.Fatalf("off produced learned skills: %+v", listed.GetSkills())
				}
			} else {
				if len(listed.GetSkills()) != 1 {
					proposals, proposalErr := built.Service.ListLearningProposals(ctx, "", "", 50, workspace)
					t.Fatalf("learned skills = %+v provider_calls=%d proposals=%+v proposal_err=%v diagnostics=%v", listed.GetSkills(), provider.Calls(), proposals, proposalErr, diag.capturedStrings())
				}
				got := listed.GetSkills()[0]
				if learning.SkillState(got.GetState()) != tc.wantState || len(got.GetReceipts()) == 0 || got.GetReceipts()[len(got.GetReceipts())-1].GetOperation() != tc.wantOperation {
					t.Fatalf("skill = %+v, want state=%s operation=%s", got, tc.wantState, tc.wantOperation)
				}
			}
			second, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			secondRun, err := built.Service.StartRun(ctx, second.ID, "Use matrix-skill if it is available")
			if err != nil {
				t.Fatal(err)
			}
			var skillResult *session.ToolResult
			var secondRunErr string
			for event := range secondRun.Events() {
				if event.Type == session.EvPermissionAsk && event.Ask != nil {
					secondRun.Approve(event.Ask.AskID, session.VerdictAllowOnce)
				}
				if event.Type == session.EvToolResult && event.ToolResult != nil && event.ToolResult.CallID == "matrix-use" {
					resultCopy := *event.ToolResult
					skillResult = &resultCopy
				}
				if event.Type == session.EvResult && event.Result != nil && event.Result.Stop == session.StopError {
					secondRunErr = event.Result.Error
				}
			}
			if secondRunErr != "" {
				t.Fatalf("publication probe run failed: %s", secondRunErr)
			}
			if skillResult == nil {
				t.Fatal("publication probe produced no Skill result")
			}
			if tc.wantPublished {
				if skillResult.IsError || skillResult.Content != "Skill: matrix-skill\n\nFollow the exact matrix procedure." {
					t.Fatalf("published Skill result = %+v", skillResult)
				}
			} else if !skillResult.IsError || !strings.Contains(skillResult.Content, "unknown skill") {
				t.Fatalf("unpublished Skill result = %+v", skillResult)
			}

			metricsMu.Lock()
			var lifecycle []learning.Activity
			for _, activity := range activities {
				switch activity.Kind {
				case learning.ActivitySkillActivatedValidated, learning.ActivitySkillActivatedEvaluated, learning.ActivitySkillStaged, learning.ActivitySkillRejected, learning.ActivityFailed:
					lifecycle = append(lifecycle, activity)
				}
			}
			metricsMu.Unlock()
			wantActivityCount := tc.wantActivityCount
			if wantActivityCount == 0 && tc.wantActivity != "" {
				wantActivityCount = 1
			}
			if tc.wantActivity == "" {
				if len(lifecycle) != 0 {
					t.Fatalf("lifecycle activities = %+v, want none", lifecycle)
				}
			} else if len(lifecycle) != wantActivityCount {
				t.Fatalf("lifecycle activities = %+v, want %d %s", lifecycle, wantActivityCount, tc.wantActivity)
			} else {
				for _, activity := range lifecycle {
					if activity.Kind != tc.wantActivity || activity.Count != 1 {
						t.Fatalf("lifecycle activities = %+v, want %d %s count=1", lifecycle, wantActivityCount, tc.wantActivity)
					}
				}
			}
			if tc.name == "evaluator error retry" {
				retried, listErr := built.Service.ListLearnedSkills(ctx, &mecatlv1.ListLearnedSkillsRequest{Project: workspace})
				if listErr != nil || len(retried.GetSkills()) != 1 || retried.GetSkills()[0].GetState() != string(learning.SkillRejected) || retried.GetSkills()[0].GetEvaluations()[0].GetVerdict() != string(learning.EvaluationError) {
					t.Fatalf("durable evaluator-error retry = %+v err=%v", retried, listErr)
				}
				for _, line := range diag.capturedStrings() {
					if strings.Contains(line, secretErr.Error()) {
						t.Fatalf("diagnostics leaked evaluator detail: %q", line)
					}
				}
			}
		})
	}
}
