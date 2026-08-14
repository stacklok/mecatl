package skillfs

import (
	"context"
	"errors"
	"fmt"
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

func TestFSSourceInventoryCountBoundRejectsWholeInventory(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "crowded", "---\nname: crowded\ndescription: many assets\n---\nBODY\n")
	skillDir := filepath.Join(dir, "crowded")
	for i := 0; i < maxSkillInventoryEntries; i++ {
		name := filepath.Join(skillDir, fmt.Sprintf("asset-%04d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatalf("write boundary asset %d: %v", i, err)
		}
	}
	src, _, err := NewFSSource(context.Background(), DirSource{Dir: dir})
	if err != nil {
		t.Fatalf("NewFSSource at count boundary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "overflow.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	assets, err := src.ListSkillAssets(context.Background(), "crowded")
	if err == nil || assets != nil || !strings.Contains(err.Error(), "exceeds 1024 entries") {
		t.Fatalf("ListSkillAssets overflow = (%v, %v), want nil and bounded count error", assets, err)
	}
	if fresh, _, err := NewFSSource(context.Background(), DirSource{Dir: dir}); err == nil || fresh != nil {
		t.Fatalf("NewFSSource overflow = (%v, %v), want construction failure during HasAssets discovery", fresh, err)
	}
}

func TestFSSourceInventoryNameBytesBoundRejectsWholeInventory(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "verbose", "---\nname: verbose\ndescription: long names\n---\nBODY\n")
	skillDir := filepath.Join(dir, "verbose")
	const stemBytes = 240
	// Stay below the entry bound while crossing the independently carried 32 KiB
	// aggregate logical-name limit.
	for i := 0; i < maxSkillInventoryNameBytes/(stemBytes+8)+1; i++ {
		name := fmt.Sprintf("%04d-%s.txt", i, strings.Repeat("n", stemBytes))
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write long-name asset %d: %v", i, err)
		}
	}
	if src, _, err := NewFSSource(context.Background(), DirSource{Dir: dir}); err == nil || src != nil || !strings.Contains(err.Error(), "exceed 32768 bytes") {
		t.Fatalf("NewFSSource name-byte overflow = (%v, %v), want bounded construction failure", src, err)
	}

	// Rebuild a source just under the byte limit, then mutate the directory past
	// it to prove the lazy ListSkillAssets path rejects rather than returning a
	// partial prefix.
	under := t.TempDir()
	writeSkill(t, under, "verbose", "---\nname: verbose\ndescription: long names\n---\nBODY\n")
	underDir := filepath.Join(under, "verbose")
	nameBytes := 0
	i := 0
	for {
		name := fmt.Sprintf("%04d-%s.txt", i, strings.Repeat("n", stemBytes))
		if nameBytes+len(name) > maxSkillInventoryNameBytes-16 {
			break
		}
		if err := os.WriteFile(filepath.Join(underDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		nameBytes += len(name)
		i++
	}
	src, _, err := NewFSSource(context.Background(), DirSource{Dir: under})
	if err != nil {
		t.Fatalf("NewFSSource below name-byte boundary: %v", err)
	}
	overflowName := strings.Repeat("z", 200) + ".txt"
	if err := os.WriteFile(filepath.Join(underDir, overflowName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	assets, err := src.ListSkillAssets(context.Background(), "verbose")
	if err == nil || assets != nil || !strings.Contains(err.Error(), "exceed 32768 bytes") {
		t.Fatalf("ListSkillAssets name-byte overflow = (%v, %v), want nil and bounded error", assets, err)
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
