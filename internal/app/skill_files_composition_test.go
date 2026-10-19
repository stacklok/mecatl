package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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

	// Isolation must be "not found", so an unrelated failure cannot pass as isolation.
	if _, err := built.Service.ListSkillFiles(bobCtx, "alice-only"); !errors.Is(err, tool.ErrSkillNotFound) {
		t.Fatalf("bob listing alice's learned skill: err = %v, want ErrSkillNotFound", err)
	}
	if _, err := built.Service.ReadSkillFile(bobCtx, "alice-only", "SKILL.md"); !errors.Is(err, tool.ErrSkillNotFound) {
		t.Fatalf("bob reading alice's learned skill: err = %v, want ErrSkillNotFound", err)
	}
}

// SPEC: a skill that ListSkills shows can always be opened. With no learned-skill store (no resolvable
// user-model directory, as in a container with no HOME or XDG directory) the deployment's skills still
// list and read their files, through the same static source ListSkills falls back to.
// This test controls HOME and XDG itself, so it is nonparallel; the second case is the positive
// control with an explicit store.
func TestBuildSkillFilesWithoutALearnedSkillStore(t *testing.T) {
	skills := t.TempDir()
	dir := filepath.Join(skills, "deploy")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":          "---\nname: deploy\ndescription: Roll out safely.\n---\nRoll out.\n",
		"references/api.md": "API notes",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, userModelDir := range map[string]string{"no learned-skill store": "", "explicit learned-skill store": t.TempDir()} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			built, err := Build(context.Background(), Config{
				Workspace: t.TempDir(), Model: "mock", StoreDir: t.TempDir(), UserModelDir: userModelDir,
				NoSoul: true, AllowAllTools: true, MockProvider: mockllm.New(), SkillsDirs: []string{skills},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			ctx := context.Background()
			if !hasCompositionSkill(built.Service.ListSkills(ctx), "deploy") {
				t.Fatal("ListSkills does not show the deployment skill")
			}
			files, err := built.Service.ListSkillFiles(ctx, "deploy")
			if err != nil || len(files) != 2 || files[0].GetName() != "SKILL.md" || files[1].GetName() != "references/api.md" {
				t.Fatalf("ListSkillFiles = %v, err %v", files, err)
			}
			if text, err := built.Service.ReadSkillFile(ctx, "deploy", "references/api.md"); err != nil || text != "API notes" {
				t.Fatalf("ReadSkillFile = %q, err %v", text, err)
			}
		})
	}
}
