package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/skillstore"
)

func TestBuildRestartHydratesCallerBoundLearnedSkillIntoListAndTool(t *testing.T) {
	workspace := t.TempDir()
	storeDir := t.TempDir()
	userModelDir := t.TempDir()
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: alice.Issuer, Subject: "bob", GrantType: session.GrantTypeUser}
	aliceCtx := session.WithPrincipal(context.Background(), alice)
	bobCtx := session.WithPrincipal(context.Background(), bob)
	partition := learning.SkillPartition{Principal: reflectionPrincipal(alice)}

	repository, err := skillstore.New(filepath.Join(userModelDir, "learned-skills"))
	if err != nil {
		t.Fatal(err)
	}
	activateCompositionSkill(t, repository, partition, "caller-recovery", "Read the durable caller-bound procedure after restart.")

	config := func(provider *mockllm.Provider) Config {
		return Config{
			Workspace: workspace, Model: "mock", StoreDir: storeDir, UserModelDir: userModelDir,
			NoSoul: true, OwnershipEnforced: true, AllowAllTools: true, MockProvider: provider,
		}
	}
	built1, err := Build(context.Background(), config(mockllm.New(mockllm.TextTurn("created"))))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := built1.Service.CreateSession(aliceCtx, workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		built1.Close()
		t.Fatal(err)
	}
	built1.Close()

	built2, err := Build(context.Background(), config(mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("skill-1", "Skill", []byte(`{"name":"caller-recovery"}`))),
		mockllm.TextTurn("done"),
	)))
	if err != nil {
		t.Fatal(err)
	}
	defer built2.Close()

	if !hasCompositionSkill(built2.Service.ListSkills(aliceCtx), "caller-recovery") {
		t.Fatal("authenticated caller list lost its durable learned-skill partition after restart")
	}
	if hasCompositionSkill(built2.Service.ListSkills(bobCtx), "caller-recovery") {
		t.Fatal("foreign caller list exposed Alice's learned-skill partition")
	}
	run, err := built2.Service.StartRunContent(aliceCtx, sess.ID, "use the recovered skill", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	for event := range run.Events() {
		if event.Type == session.EvToolResult && event.ToolResult != nil {
			result = event.ToolResult.Content
		}
	}
	if !strings.Contains(result, "durable caller-bound procedure") || strings.Contains(result, "unknown tool") {
		t.Fatalf("rehydrated Skill result=%q", result)
	}
}

func activateCompositionSkill(t *testing.T, repository learning.SkillRepository, partition learning.SkillPartition, name, body string) learning.SkillVersion {
	t.Helper()
	ctx := context.Background()
	value, err := repository.CreateDraft(ctx, partition, "reflection", learning.SkillBundle{Name: name, Description: "Caller recovery", Body: body}, learning.SkillProvenance{Origin: learning.SkillProvenanceLegacyModel})
	if err == nil {
		value, err = repository.RecordEvaluation(ctx, partition, "reflection", value.ID, value.Version, value.Revision, learning.SkillEvaluation{Verdict: learning.EvaluationPass, FixtureIDs: []string{"fixture"}})
	}
	if err == nil {
		value, err = repository.Stage(ctx, partition, "reflection", value.ID, value.Version, value.Revision)
	}
	if err == nil {
		value, err = repository.Activate(ctx, partition, "reflection", value.ID, value.Version, value.Revision)
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func hasCompositionSkill(skills []*mecatlv1.SkillInfo, name string) bool {
	for _, skill := range skills {
		if skill.GetName() == name {
			return true
		}
	}
	return false
}
