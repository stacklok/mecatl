package skillfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// TestFSSourceAssetDirsPerSkillDedupCanonical pins the AssetDirs computation
// (the old skillReadRoots, moved home): PER-SKILL canonical dirs, de-duplicated,
// never the whole source dir, and path-less skills contribute nothing.
func TestFSSourceAssetDirsPerSkillDedupCanonical(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: a\n---\nA\n")
	writeSkill(t, dir, "beta", "---\nname: beta\ndescription: b\n---\nB\n")

	src, _, err := NewFSSource(context.Background(), DirSource{Dir: dir})
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	roots := src.AssetDirs()
	wantAlpha, _ := resolveRoot(filepath.Join(dir, "alpha"))
	wantBeta, _ := resolveRoot(filepath.Join(dir, "beta"))
	if len(roots) != 2 || roots[0] != wantAlpha || roots[1] != wantBeta {
		t.Errorf("AssetDirs = %v, want [%q %q] (per-skill, name-ordered)", roots, wantAlpha, wantBeta)
	}
	sourceDir, _ := resolveRoot(dir)
	for _, r := range roots {
		if r == sourceDir {
			t.Errorf("the skills SOURCE dir %q must never be an asset dir (per-skill granularity)", sourceDir)
		}
	}

	// AssetDir agrees with AssetDirs entry-wise.
	got, ok := src.AssetDir("alpha")
	if !ok || got != wantAlpha {
		t.Errorf("AssetDir(alpha) = %q, %v; want %q, true", got, ok, wantAlpha)
	}
	if _, ok := src.AssetDir("ghost"); ok {
		t.Error("AssetDir(unknown) must report false")
	}
}

// TestFSSourceAssetDirResolvesSymlinkAlias is the FSSource twin of the old
// skillReadRoots symlink test: a discovery path crossing a symlink alias must
// yield the RESOLVED canonical dir (the osfs allowlist key), never the raw
// alias form.
func TestFSSourceAssetDirResolvesSymlinkAlias(t *testing.T) {
	realDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeSkill(t, alias, "aliased", "---\nname: aliased\ndescription: via alias\n---\nBODY\n")

	src, _, err := NewFSSource(context.Background(), DirSource{Dir: alias})
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	resolved, err := resolveRoot(filepath.Join(realDir, "aliased"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rawDir := filepath.Clean(filepath.Join(alias, "aliased"))
	if rawDir == resolved {
		t.Fatalf("test setup did not produce a divergent alias: raw %q == resolved %q", rawDir, resolved)
	}
	if got, ok := src.AssetDir("aliased"); !ok || got != resolved {
		t.Errorf("AssetDir = %q, %v; want the RESOLVED dir %q", got, ok, resolved)
	}
	roots := src.AssetDirs()
	for _, r := range roots {
		if r == rawDir {
			t.Errorf("AssetDirs carries the raw symlink-alias dir %q; want only resolved dirs %v", rawDir, roots)
		}
	}
}

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
