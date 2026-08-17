package app

import (
	"context"
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

func TestUsableAutoSkillsStockBuildPublishesReflectedProcedure(t *testing.T) {
	workspace := t.TempDir()
	var requestMu sync.Mutex
	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) {
		requestMu.Lock()
		defer requestMu.Unlock()
		requests = append(requests, request)
	})},
		mockllm.TextTurn("I completed and verified the workflow."),
		mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"procedure","name":"verify-go-change","title":"Verify Go changes","body":"Run focused tests, then inspect the diff.","evidence":["m:0"]}]}`),
		mockllm.TextTurn("The learned skill is available."),
	)
	built, err := Build(context.Background(), Config{
		Model: "test-model", Workspace: workspace, TrustProject: true,
		LearningMode: learning.Auto, UserModelDir: t.TempDir(), MemoryDir: t.TempDir(),
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "test-key"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(Config, string, string, string) port.LLMProvider { return provider },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	principal := &session.Principal{Issuer: "test", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(context.Background(), principal)
	sess, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "Learn this procedure after completing and verifying it")
	if err != nil {
		t.Fatal(err)
	}
	_ = drainRun(run)

	deadline := time.Now().Add(3 * time.Second)
	var listed *mecatlv1.ListLearnedSkillsResponse
	for time.Now().Before(deadline) {
		listed, err = built.Service.ListLearnedSkills(ctx, &mecatlv1.ListLearnedSkillsRequest{Project: workspace, State: "active"})
		if err == nil && len(listed.GetSkills()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || listed == nil || len(listed.GetSkills()) != 1 {
		allSkills, allErr := built.Service.ListLearnedSkills(ctx, &mecatlv1.ListLearnedSkillsRequest{Project: workspace})
		proposals, proposalErr := built.Service.ListLearningProposals(ctx, "", "", 50, workspace)
		t.Fatalf("active learned skill = %+v, err=%v, provider_calls=%d, all=%+v, all_err=%v, proposals=%+v, proposal_err=%v", listed, err, provider.Calls(), allSkills, allErr, proposals, proposalErr)
	}
	got := listed.GetSkills()[0]
	if got.GetName() != "verify-go-change" || len(got.GetReceipts()) == 0 || got.GetReceipts()[len(got.GetReceipts())-1].GetOperation() != "activate_validated" {
		t.Fatalf("learned skill = %+v", got)
	}

	second, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := built.Service.StartRun(ctx, second.ID, "Use the learned verification workflow")
	if err != nil {
		t.Fatal(err)
	}
	_ = drainRun(secondRun)
	requestMu.Lock()
	defer requestMu.Unlock()
	if len(requests) < 3 {
		t.Fatalf("provider requests = %d", len(requests))
	}
	found := false
	for _, spec := range requests[len(requests)-1].Tools {
		if spec.Name == "Skill" && strings.Contains(spec.Description, "verify-go-change") {
			found = true
		}
	}
	if !found {
		t.Fatal("next run did not advertise the active learned skill")
	}
}
