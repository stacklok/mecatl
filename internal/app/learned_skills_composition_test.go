package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/skillfs"
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
	sess, err := built1.Service.CreateSession(aliceCtx, session.ModeDefault, session.Limits{})
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

func TestADR_0295_ReplicaHydrationConvergesAcrossReplacementAndRollback(t *testing.T) {
	workspace := t.TempDir()
	storeDir := t.TempDir()
	userModelDir := t.TempDir()
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: alice.Issuer, Subject: "bob", GrantType: session.GrantTypeUser}
	aliceCtx := session.WithPrincipal(context.Background(), alice)
	bobCtx := session.WithPrincipal(context.Background(), bob)
	partition := learning.SkillPartition{Principal: reflectionPrincipal(alice)}

	replicaA, err := skillstore.New(filepath.Join(userModelDir, "learned-skills"))
	if err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("skill-v1", "Skill", []byte(`{"name":"replica-skill"}`))), mockllm.TextTurn("v1 done"),
		mockllm.ToolCallTurn(session.NewToolCall("skill-bob", "Skill", []byte(`{"name":"replica-skill"}`))), mockllm.TextTurn("bob done"),
		mockllm.ToolCallTurn(session.NewToolCall("skill-project", "Skill", []byte(`{"name":"project-secret"}`))), mockllm.TextTurn("project done"),
		mockllm.ToolCallTurn(session.NewToolCall("skill-v2", "Skill", []byte(`{"name":"replica-skill"}`))), mockllm.TextTurn("v2 done"),
		mockllm.ToolCallTurn(session.NewToolCall("skill-archived", "Skill", []byte(`{"name":"replica-skill"}`))), mockllm.TextTurn("archive done"),
		mockllm.ToolCallTurn(session.NewToolCall("skill-rollback", "Skill", []byte(`{"name":"replica-skill"}`))), mockllm.TextTurn("rollback done"),
	)
	replicaB, err := Build(context.Background(), Config{
		Workspace: workspace, Model: "mock", StoreDir: storeDir, UserModelDir: userModelDir,
		NoSoul: true, Headless: true, OwnershipEnforced: true, AllowAllTools: true, MockProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer replicaB.Close()
	aliceSession, err := replicaB.Service.CreateSession(aliceCtx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	bobSession, err := replicaB.Service.CreateSession(bobCtx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	v1 := activateCompositionSkill(t, replicaA, partition, "replica-skill", "replica body v1")
	if got := runCompositionSkill(aliceCtx, t, replicaB, aliceSession.ID); !strings.Contains(got, "replica body v1") {
		t.Fatalf("cold replica result=%q, want active v1", got)
	}
	if got := runCompositionSkill(bobCtx, t, replicaB, bobSession.ID); !strings.Contains(got, "unknown skill") || strings.Contains(got, "replica body") {
		t.Fatalf("foreign partition result=%q, want absence without Alice content", got)
	}
	activateCompositionSkill(t, replicaA, learning.SkillPartition{Principal: partition.Principal, Project: workspace}, "project-secret", "non-admitted project body")
	if got := runCompositionSkill(aliceCtx, t, replicaB, aliceSession.ID); !strings.Contains(got, "unknown skill") || strings.Contains(got, "project body") {
		t.Fatalf("non-admitted project result=%q, want no project partition content", got)
	}

	v2 := activateCompositionSkill(t, replicaA, partition, "replica-skill", "replica body v2")
	if got := runCompositionSkill(aliceCtx, t, replicaB, aliceSession.ID); !strings.Contains(got, "replica body v2") || strings.Contains(got, "replica body v1") {
		t.Fatalf("replacement result=%q, want wholly v2", got)
	}
	_, err = replicaA.Archive(context.Background(), partition, "reflection", v2.ID, v2.Version, v2.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if got := runCompositionSkill(aliceCtx, t, replicaB, aliceSession.ID); !strings.Contains(got, "unknown skill") || strings.Contains(got, "replica body") {
		t.Fatalf("archive result=%q, want invalidated partition entry", got)
	}
	v3 := activateCompositionSkill(t, replicaA, partition, "replica-skill", "replica body v3")
	if _, err := replicaA.Rollback(context.Background(), partition, "reflection", v3.ID, v3.Revision, v1.Version); err != nil {
		t.Fatal(err)
	}
	if got := runCompositionSkill(aliceCtx, t, replicaB, aliceSession.ID); !strings.Contains(got, "replica body v1") || strings.Contains(got, "replica body v2") {
		t.Fatalf("rollback result=%q, want wholly restored v1", got)
	}

	catalog := skillfs.NewAtomicCatalog(nil, nil, nil)
	newer := map[learning.SkillPartition]learning.SkillGeneration{partition: 100}
	stale := map[learning.SkillPartition]learning.SkillGeneration{partition: 99}
	if !catalog.RefreshPartitionsAtGeneration(newer, []learning.SkillVersion{v1}) {
		t.Fatal("newer authoritative publication was rejected")
	}
	if catalog.RefreshPartitionsAtGeneration(stale, []learning.SkillVersion{v2}) {
		t.Fatal("delayed old publication unexpectedly replaced a newer generation")
	}
	if catalog.ClearPartitionsAtGeneration(stale) {
		t.Fatal("delayed old invalidation unexpectedly revoked a newer generation")
	}
	view := catalog.View(partition)
	if len(view.Metas) != 1 || view.Generation != 100 || view.Metas[0].Metadata["mecatl.active_version"] != string(v1.Version) {
		t.Fatalf("delayed operation changed newer snapshot: generation=%d metas=%+v", view.Generation, view.Metas)
	}
}

func runCompositionSkill(ctx context.Context, t *testing.T, built *Built, id session.SessionID) string {
	t.Helper()
	run, err := built.Service.StartRunContent(ctx, id, "use the replica skill", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	for event := range run.Events() {
		if event.Type == session.EvToolResult && event.ToolResult != nil {
			result = event.ToolResult.Content
		}
	}
	return result
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
