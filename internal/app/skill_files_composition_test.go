package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/skillstore"
)

// SPEC: through the real daemon wiring, a caller reads the files of their own published learned
// skill (exactly one, "SKILL.md", holding its body) and never another caller's learned skill.
func TestBuildSkillFilesResolveThroughTheCallersOwnView(t *testing.T) {
	userModelDir := t.TempDir()
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: alice.Issuer, Subject: "bob", GrantType: session.GrantTypeUser}
	aliceCtx := session.WithPrincipal(context.Background(), alice)
	bobCtx := session.WithPrincipal(context.Background(), bob)

	repository, err := skillstore.New(filepath.Join(userModelDir, "learned-skills"))
	if err != nil {
		t.Fatal(err)
	}
	activateCompositionSkill(t, repository, learning.SkillPartition{Principal: reflectionPrincipal(alice)}, "alice-only", "Alice's durable procedure.")

	built, err := Build(context.Background(), Config{
		Workspace: t.TempDir(), Model: "mock", StoreDir: t.TempDir(), UserModelDir: userModelDir,
		NoSoul: true, OwnershipEnforced: true, AllowAllTools: true, MockProvider: mockllm.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	files, err := built.Service.ListSkillFiles(aliceCtx, "alice-only")
	if err != nil {
		t.Fatalf("alice list: %v", err)
	}
	if len(files) != 1 || files[0].GetName() != "SKILL.md" {
		t.Fatalf("a learned skill must list exactly SKILL.md, got %v", files)
	}
	body, err := built.Service.ReadSkillFile(aliceCtx, "alice-only", "SKILL.md")
	if err != nil || body != "Alice's durable procedure." {
		t.Fatalf("alice read = %q, %v", body, err)
	}

	if _, err := built.Service.ListSkillFiles(bobCtx, "alice-only"); err == nil {
		t.Fatal("bob listed alice's learned skill")
	}
	if _, err := built.Service.ReadSkillFile(bobCtx, "alice-only", "SKILL.md"); err == nil {
		t.Fatal("bob read alice's learned skill")
	}
}
