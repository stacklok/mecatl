package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/skillstore"
)

func isolateConfig(t testing.TB, cfg Config) Config {
	t.Helper()
	if cfg.UserModelDir == "" {
		cfg.UserModelDir = t.TempDir()
	}
	return cfg
}

//nolint:revive // Test helpers conventionally keep testing.TB first.
func buildIsolated(t testing.TB, ctx context.Context, cfg Config) (*Built, error) {
	t.Helper()
	return Build(ctx, isolateConfig(t, cfg))
}

func TestBuildIsolatedIgnoresAmbientUserModel(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	repository, err := skillstore.New(filepath.Join(xdg, userModelSubdir, "learned-skills"))
	if err != nil {
		t.Fatal(err)
	}
	activateCompositionSkill(t, repository, learning.SkillPartition{Principal: reflectionPrincipal(nil)}, "ambient-poison", "must not be loaded")

	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if hasCompositionSkill(built.Service.ListSkills(t.Context()), "ambient-poison") {
		t.Fatal("isolated Build fixture loaded a learned skill from ambient XDG config")
	}
}

func TestBuildIsolatedPreservesExplicitUserModelDir(t *testing.T) {
	userModelDir := t.TempDir()
	repository, err := skillstore.New(filepath.Join(userModelDir, "learned-skills"))
	if err != nil {
		t.Fatal(err)
	}
	activateCompositionSkill(t, repository, learning.SkillPartition{Principal: reflectionPrincipal(nil)}, "explicit-fixture", "must be loaded")

	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: t.TempDir(), UseMock: true, NoSoul: true,
		UserModelDir: userModelDir, MockProvider: mockllm.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if !hasCompositionSkill(built.Service.ListSkills(t.Context()), "explicit-fixture") {
		t.Fatal("isolated Build fixture replaced an explicitly supplied user-model directory")
	}
}
