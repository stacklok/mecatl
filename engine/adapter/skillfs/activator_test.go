package skillfs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// TestSnapshotActivatorReturnsAssets pins the FS activator's Assets population:
// a skill with bundled files on disk yields the same logical-name list the FS
// source's ListSkillAssets returns; an asset-less skill yields nil.
func TestSnapshotActivatorReturnsAssets(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "deploy", "---\nname: deploy\ndescription: deploy\n---\nDeploy.\n")
	writeAsset(t, dir, "deploy", "scripts/run.sh", "#!/bin/sh\n")
	writeAsset(t, dir, "deploy", "references/api.md", "notes\n")
	writeSkill(t, dir, "lean", "---\nname: lean\ndescription: no assets\n---\nLean.\n")

	src, skips, err := NewFSSource(context.Background(), DirSource{Dir: dir})
	if err != nil || len(skips) != 0 {
		t.Fatalf("NewFSSource: %v skips=%v", err, skips)
	}
	act := NewSnapshotActivator(src)

	// Skill WITH assets.
	got, err := act.Activate(context.Background(), "deploy")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(got.Assets) != 2 {
		t.Fatalf("expected 2 assets, got %d: %v", len(got.Assets), got.Assets)
	}
	if got.Assets[0].Name != "references/api.md" || got.Assets[1].Name != "scripts/run.sh" {
		t.Errorf("asset names not sorted or wrong: %q, %q", got.Assets[0].Name, got.Assets[1].Name)
	}
	if got.Body != "Deploy." {
		t.Errorf("body = %q, want %q", got.Body, "Deploy.")
	}

	// Skill WITHOUT assets.
	got, err = act.Activate(context.Background(), "lean")
	if err != nil {
		t.Fatalf("Activate(lean): %v", err)
	}
	if len(got.Assets) != 0 {
		t.Errorf("asset-less skill should have nil Assets, got %v", got.Assets)
	}
}

// TestSourceActivatorReturnsAssets pins the driver activator's Assets
// population: the source activator calls ListSkillAssets on the port and
// caches the full Activation (including Assets) so repeat activations
// re-render from memory.
func TestSourceActivatorReturnsAssets(t *testing.T) {
	src := newMockSkillSource()
	act := NewSourceActivator(src, nil)

	got, err := act.Activate(context.Background(), "bundle")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(got.Assets) != 2 {
		t.Fatalf("expected 2 assets, got %d: %v", len(got.Assets), got.Assets)
	}
	if got.Assets[0].Name != "references/deep/api.md" || got.Assets[1].Name != "scripts/run.sh" {
		t.Errorf("asset names wrong: %q, %q", got.Assets[0].Name, got.Assets[1].Name)
	}

	// Cached activation returns the same Assets.
	got2, err := act.Activate(context.Background(), "bundle")
	if err != nil {
		t.Fatalf("Activate #2: %v", err)
	}
	if len(got2.Assets) != 2 || got2.Assets[0].Name != got.Assets[0].Name {
		t.Errorf("cached activation lost Assets: %v", got2.Assets)
	}
}

// TestSourceActivatorListAssetsErrorPropagates pins that a genuine
// ListSkillAssets fault (not ErrSkillNotFound) surfaces as an activation error,
// mirroring how a SkillBody fault is handled.
func TestSourceActivatorListAssetsErrorPropagates(t *testing.T) {
	src := &faultyAssetSource{}
	act := NewSourceActivator(src, nil)
	_, err := act.Activate(context.Background(), "broken")
	if err == nil {
		t.Fatal("a genuine ListSkillAssets fault must surface as an activation error")
	}
	if !errors.Is(err, errTestListAssets) {
		t.Errorf("error should wrap the original fault, got %v", err)
	}
}

var errTestListAssets = errors.New("test: list assets fault")

// faultyAssetSource is a mock SkillSource whose ListSkillAssets returns a
// genuine (non-ErrSkillNotFound) fault for a known skill.
type faultyAssetSource struct{}

func (faultyAssetSource) ListSkills(context.Context) ([]tool.SkillMeta, error) {
	return []tool.SkillMeta{{Name: "broken", Description: "faulty"}}, nil
}

func (faultyAssetSource) SkillBody(_ context.Context, name string) (string, error) {
	if name == "broken" {
		return "BODY", nil
	}
	return "", fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
}

func (faultyAssetSource) ListSkillAssets(_ context.Context, name string) ([]tool.SkillAsset, error) {
	if name == "broken" {
		return nil, errTestListAssets
	}
	return nil, fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
}

func (faultyAssetSource) ReadSkillAsset(_ context.Context, skill, asset string) ([]byte, error) {
	return nil, fmt.Errorf("%w: %q/%q", tool.ErrSkillAssetNotFound, skill, asset)
}

// mockSkillSource is a minimal in-memory SkillSource for activator tests.
type mockSkillSource struct{}

func newMockSkillSource() mockSkillSource { return mockSkillSource{} }

func (mockSkillSource) ListSkills(context.Context) ([]tool.SkillMeta, error) {
	return []tool.SkillMeta{{Name: "bundle", Description: "with assets"}}, nil
}

func (mockSkillSource) SkillBody(_ context.Context, name string) (string, error) {
	if name == "bundle" {
		return "BUNDLE BODY", nil
	}
	return "", fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
}

func (mockSkillSource) ListSkillAssets(_ context.Context, name string) ([]tool.SkillAsset, error) {
	if name == "bundle" {
		return []tool.SkillAsset{
			{Name: "references/deep/api.md", Size: 9},
			{Name: "scripts/run.sh", Size: 14, Executable: true},
		}, nil
	}
	return nil, fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
}

func (mockSkillSource) ReadSkillAsset(_ context.Context, skill, asset string) ([]byte, error) {
	return nil, fmt.Errorf("%w: %q/%q", tool.ErrSkillAssetNotFound, skill, asset)
}

// TestSourceActivatorDoesNotEagerlyReadAssets pins the agentskills.io "enumerate
// but must not eagerly read" contract: the source activator calls ListSkillAssets
// (to populate the Activation.Assets logical-name list), but it must NEVER call
// ReadSkillAsset — the payloads are materialized lazily by the consumer (the
// asset materializer), not by the activator.
func TestSourceActivatorDoesNotEagerlyReadAssets(t *testing.T) {
	// countingSource delegates to mockSkillSource but counts every ReadSkillAsset.
	cs := &countingSource{inner: newMockSkillSource()}
	act := NewSourceActivator(cs, nil)

	act.Activate(context.Background(), "bundle")
	if cs.reads != 0 {
		t.Errorf("activator read %d asset(s) — the activator must enumerate (ListSkillAssets) but NEVER eagerly read (ReadSkillAsset)", cs.reads)
	}
}

// countingSource wraps a tool.SkillSource and counts ReadSkillAsset calls.
type countingSource struct {
	inner tool.SkillSource
	reads int
}

func (c *countingSource) ListSkills(ctx context.Context) ([]tool.SkillMeta, error) {
	return c.inner.ListSkills(ctx)
}

func (c *countingSource) SkillBody(ctx context.Context, name string) (string, error) {
	return c.inner.SkillBody(ctx, name)
}

func (c *countingSource) ListSkillAssets(ctx context.Context, name string) ([]tool.SkillAsset, error) {
	return c.inner.ListSkillAssets(ctx, name)
}

func (c *countingSource) ReadSkillAsset(ctx context.Context, skill, asset string) ([]byte, error) {
	c.reads++
	return c.inner.ReadSkillAsset(ctx, skill, asset)
}
