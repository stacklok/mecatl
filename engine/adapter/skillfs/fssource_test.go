package skillfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// TestFSSourceSkipsSymlinkedAssetsAndExcludesSkillMD pins the bundle listing
// posture: SKILL.md is the body (never a payload), and symlinked entries are
// skipped on BOTH the list and read paths (containment parity).
func TestFSSourceSkipsSymlinkedAssetsAndExcludesSkillMD(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "guarded", "---\nname: guarded\ndescription: g\n---\nBODY\n")
	skillDir := filepath.Join(dir, "guarded")
	if err := os.WriteFile(filepath.Join(skillDir, "notes.md"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	// A symlink INSIDE the skill dir pointing outside it: must be invisible.
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(skillDir, "leak.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	src, _, err := NewFSSource(context.Background(), DirSource{Dir: dir})
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	assets, err := src.ListSkillAssets(context.Background(), "guarded")
	if err != nil {
		t.Fatalf("ListSkillAssets: %v", err)
	}
	if len(assets) != 1 || assets[0].Name != "notes.md" {
		t.Errorf("assets = %+v, want exactly [notes.md] (SKILL.md excluded, symlink skipped)", assets)
	}
	if _, err := src.ReadSkillAsset(context.Background(), "guarded", "leak.txt"); !errors.Is(err, tool.ErrSkillAssetNotFound) {
		t.Errorf("reading a symlinked entry = %v, want the not-found sentinel (never content)", err)
	}
	if _, err := src.ReadSkillAsset(context.Background(), "guarded", SkillFileName); !errors.Is(err, tool.ErrSkillAssetNotFound) {
		t.Errorf("reading SKILL.md as an asset = %v, want the not-found sentinel", err)
	}
}

func TestFSSourceReadSkillAssetRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "large", "---\nname: large\ndescription: large asset\n---\nBODY\n")
	asset := filepath.Join(dir, "large", "large.txt")
	if err := os.WriteFile(asset, []byte(strings.Repeat("x", maxSkillAssetBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _, err := NewFSSource(context.Background(), DirSource{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	data, err := src.ReadSkillAsset(context.Background(), "large", "large.txt")
	if err == nil || data != nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("ReadSkillAsset = (%d bytes, %v), want whole-payload rejection", len(data), err)
	}
}

// TestFSSourceOriginTiers pins the tier stamping end to end: ResolveSources'
// tiers reach the port's SkillMeta.Origin, and a tier-less hand-rolled source
// normalizes to "explicit".
func TestFSSourceOriginTiers(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "tiered", "---\nname: tiered\ndescription: t\n---\nB\n")

	src, _, err := NewFSSource(context.Background(), DirSource{Dir: dir, Tier: tool.SkillOriginProject})
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	metas, _ := src.ListSkills(context.Background())
	if len(metas) != 1 || metas[0].Origin != tool.SkillOriginProject {
		t.Errorf("metas = %+v, want Origin=project", metas)
	}

	src2, _, err := NewFSSource(context.Background(), DirSource{Dir: dir})
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	metas2, _ := src2.ListSkills(context.Background())
	if len(metas2) != 1 || metas2[0].Origin != tool.SkillOriginExplicit {
		t.Errorf("metas = %+v, want the zero tier to normalize to explicit", metas2)
	}
}
