package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// mapSource is an in-memory tool.SkillSource test double with a read counter
// (mutex-guarded so concurrent-Provision tests run clean under -race) and a
// one-shot transient-fault injector, so the materializer's lazy/retry/once
// discipline is observable.
type mapSource struct {
	metas  []tool.SkillMeta
	bodies map[string]string
	assets map[string][]tool.SkillAsset
	data   map[string]map[string][]byte

	mu       sync.Mutex
	reads    int
	failNext error // returned by the NEXT ReadSkillAsset, then cleared
}

func (s *mapSource) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func (s *mapSource) ListSkills(context.Context) ([]tool.SkillMeta, error) { return s.metas, nil }

func (s *mapSource) SkillBody(_ context.Context, name string) (string, error) {
	b, ok := s.bodies[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
	}
	return b, nil
}

func (s *mapSource) ListSkillAssets(_ context.Context, name string) ([]tool.SkillAsset, error) {
	if _, ok := s.bodies[name]; !ok {
		return nil, fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
	}
	return s.assets[name], nil
}

func (s *mapSource) ReadSkillAsset(_ context.Context, skill, asset string) ([]byte, error) {
	s.mu.Lock()
	s.reads++
	if err := s.failNext; err != nil {
		s.failNext = nil
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	if d, ok := s.data[skill][asset]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("%w: %q/%q", tool.ErrSkillAssetNotFound, skill, asset)
}

// failingAssetSource serves one skill whose bundle is INVALID (a traversal
// logical name) — the materializer must fail its activation — plus an
// asset-less sibling that must stay activatable.
type failingAssetSource struct{}

func (failingAssetSource) ListSkills(context.Context) ([]tool.SkillMeta, error) {
	return []tool.SkillMeta{
		{Name: "evil", Description: "bad bundle", Origin: tool.SkillOriginDriver, HasAssets: true},
		{Name: "good", Description: "fine", Origin: tool.SkillOriginDriver},
	}, nil
}

func (failingAssetSource) SkillBody(_ context.Context, name string) (string, error) {
	switch name {
	case "evil", "good":
		return "BODY of " + name, nil
	}
	return "", fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
}

func (failingAssetSource) ListSkillAssets(_ context.Context, name string) ([]tool.SkillAsset, error) {
	if name == "evil" {
		return []tool.SkillAsset{{Name: "../escape.sh", Size: 4, Executable: true}}, nil
	}
	if name == "good" {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
}

func (failingAssetSource) ReadSkillAsset(_ context.Context, skill, asset string) ([]byte, error) {
	return nil, fmt.Errorf("%w: %q/%q", tool.ErrSkillAssetNotFound, skill, asset)
}

func newMapSource() *mapSource {
	return &mapSource{
		metas: []tool.SkillMeta{
			{Name: "bundle", Description: "with payloads", Origin: tool.SkillOriginDriver, HasAssets: true},
			{Name: "lean", Description: "asset-less", Origin: tool.SkillOriginDriver},
		},
		bodies: map[string]string{"bundle": "BUNDLE BODY", "lean": "LEAN BODY"},
		assets: map[string][]tool.SkillAsset{
			"bundle": {
				{Name: "references/deep/api.md", Size: 9},
				{Name: "scripts/run.sh", Size: 14, Executable: true},
			},
		},
		data: map[string]map[string][]byte{
			"bundle": {
				"references/deep/api.md": []byte("api notes"),
				"scripts/run.sh":         []byte("#!/bin/sh\nok\n"),
			},
		},
	}
}

// TestMaterializerProvisionsBundle pins the happy path: payloads land at
// <base>/<skill>/<logical-name> with the executable bit honored, and a second
// Provision is idempotent (no re-transfer — per-skill once).
func TestMaterializerProvisionsBundle(t *testing.T) {
	src := newMapSource()
	base := t.TempDir()
	mat := NewAssetMaterializer(src, base)

	dir, err := mat.Provision(context.Background(), "bundle")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if dir != filepath.Join(base, "bundle") {
		t.Errorf("dir = %q, want %q", dir, filepath.Join(base, "bundle"))
	}
	gotMD, err := os.ReadFile(filepath.Join(dir, "references", "deep", "api.md"))
	if err != nil || string(gotMD) != "api notes" {
		t.Errorf("materialized text asset = %q, %v", gotMD, err)
	}
	info, err := os.Stat(filepath.Join(dir, "scripts", "run.sh"))
	if err != nil {
		t.Fatalf("stat script: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("executable payload lost its executable bit")
	}
	mdInfo, _ := os.Stat(filepath.Join(dir, "references", "deep", "api.md"))
	if mdInfo.Mode()&0o111 != 0 {
		t.Error("text payload must NOT be executable")
	}

	readsAfterFirst := src.readCount()
	dir2, err := mat.Provision(context.Background(), "bundle")
	if err != nil || dir2 != dir {
		t.Fatalf("Provision #2 = %q, %v; want the cached dir", dir2, err)
	}
	if got := src.readCount(); got != readsAfterFirst {
		t.Errorf("Provision #2 re-transferred (%d → %d reads); the success latch must hold", readsAfterFirst, got)
	}
}

// TestMaterializerLazyAndAssetless pins LAZY transfer (zero reads before the
// first Provision) and the asset-less outcome (dir "", nothing on disk).
func TestMaterializerLazyAndAssetless(t *testing.T) {
	src := newMapSource()
	base := t.TempDir()
	mat := NewAssetMaterializer(src, base)
	if got := src.readCount(); got != 0 {
		t.Fatalf("construction transferred %d assets; materialization must be lazy", got)
	}

	dir, err := mat.Provision(context.Background(), "lean")
	if err != nil {
		t.Fatalf("Provision(asset-less): %v", err)
	}
	if dir != "" {
		t.Errorf("asset-less skill dir = %q, want \"\" (the Base-directory block is omitted)", dir)
	}
	if got := src.readCount(); got != 0 {
		t.Errorf("asset-less Provision read %d assets, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(base, "lean")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("asset-less skill must leave nothing on disk, stat err = %v", err)
	}
}

// TestMaterializerRejectsInvalidNames pins containment: a bundle carrying a
// traversal/absolute/backslash logical name fails the WHOLE activation and
// writes nothing outside (or inside — never partial).
func TestMaterializerRejectsInvalidNames(t *testing.T) {
	for _, bad := range []string{"../escape.md", "/abs.md", "a\\b.md", "a/./b.md", ""} {
		t.Run(bad, func(t *testing.T) {
			src := newMapSource()
			src.assets["bundle"] = []tool.SkillAsset{
				{Name: "references/deep/api.md", Size: 9},
				{Name: bad, Size: 1},
			}
			base := t.TempDir()
			mat := NewAssetMaterializer(src, base)
			if _, err := mat.Provision(context.Background(), "bundle"); err == nil {
				t.Fatalf("Provision with bundled name %q must fail the activation", bad)
			}
			// NEVER partial: the valid sibling asset must not survive the failure.
			if _, err := os.Stat(filepath.Join(base, "bundle")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("failed bundle left a partial dir behind (stat err = %v)", err)
			}
		})
	}
}

// TestMaterializerRejectsHostileSkillName pins the per-skill dir containment:
// a driver-supplied skill name cannot escape the cache base.
func TestMaterializerRejectsHostileSkillName(t *testing.T) {
	src := newMapSource()
	mat := NewAssetMaterializer(src, t.TempDir())
	for _, bad := range []string{"../evil", "a/b", `a\b`, "..", "", "line\nbreak", "line\u2028break"} {
		if _, err := mat.Provision(context.Background(), bad); err == nil {
			t.Errorf("Provision(%q) must reject a non-segment skill name", bad)
		}
	}
}

// TestMaterializerEnforcesCaps pins the byte ceilings on the ACTUAL bytes
// read: one payload over the per-asset cap, or a bundle over the bundle cap,
// fails the activation (model-addressable) and leaves nothing behind.
func TestMaterializerEnforcesCaps(t *testing.T) {
	src := newMapSource()
	// Advertise a small size but DELIVER over-cap bytes: the enforcement must
	// be on what arrives, not what was promised.
	src.assets["bundle"] = []tool.SkillAsset{{Name: "big.bin", Size: 1}}
	src.data["bundle"]["big.bin"] = make([]byte, maxAssetBytes+1)
	base := t.TempDir()
	mat := NewAssetMaterializer(src, base)
	_, err := mat.Provision(context.Background(), "bundle")
	if err == nil || !strings.Contains(err.Error(), "per-file cap") {
		t.Fatalf("over-cap asset must fail the activation, got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(base, "bundle")); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("failed bundle left a partial dir behind (stat err = %v)", serr)
	}

	// The once-guard caches the deterministic rejection (idempotent failure).
	_, err2 := mat.Provision(context.Background(), "bundle")
	if err2 == nil || err2.Error() != err.Error() {
		t.Errorf("Provision #2 after a rejection = %v, want the cached rejection %v", err2, err)
	}
}

// TestMaterializerTransientErrorRetries pins the latch semantics' OTHER half:
// a transport-class fault (here a one-shot read error; a ctx cancel behaves
// the same) is returned but NOT latched — the next Provision retries the
// whole transfer and succeeds, with the bundle on disk. The cache is
// build-scoped and shared, so a permanent latch on a transient blip would
// brick the skill process-wide.
func TestMaterializerTransientErrorRetries(t *testing.T) {
	src := newMapSource()
	src.failNext = errors.New("driver hiccup: connection reset")
	base := t.TempDir()
	mat := NewAssetMaterializer(src, base)

	if _, err := mat.Provision(context.Background(), "bundle"); err == nil {
		t.Fatal("Provision #1 must surface the injected transient fault")
	}
	if _, serr := os.Stat(filepath.Join(base, "bundle")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("a failed transfer must leave nothing behind, stat err = %v", serr)
	}

	dir, err := mat.Provision(context.Background(), "bundle")
	if err != nil {
		t.Fatalf("Provision #2 after a transient fault must RETRY and succeed, got %v", err)
	}
	if got, rerr := os.ReadFile(filepath.Join(dir, "references", "deep", "api.md")); rerr != nil || string(got) != "api notes" {
		t.Errorf("retried bundle not on disk: %q, %v", got, rerr)
	}
}

// TestMaterializerDeterministicRejectionStaysLatched pins the permanent half:
// a deterministic bundle rejection (an invalid logical name) is latched —
// even after the source "fixes" its listing, Provision keeps returning the
// original rejection (the bundle the harness judged is the snapshot's).
func TestMaterializerDeterministicRejectionStaysLatched(t *testing.T) {
	src := newMapSource()
	src.assets["bundle"] = []tool.SkillAsset{{Name: "../escape.md", Size: 1}}
	mat := NewAssetMaterializer(src, t.TempDir())

	_, err := mat.Provision(context.Background(), "bundle")
	if err == nil || !errors.Is(err, errBundleRejected) {
		t.Fatalf("Provision(invalid bundle) = %v, want an errBundleRejected rejection", err)
	}

	// The source heals itself; the latch must hold regardless.
	src.assets["bundle"] = []tool.SkillAsset{{Name: "ok.md", Size: 2}}
	src.data["bundle"]["ok.md"] = []byte("ok")
	if _, err2 := mat.Provision(context.Background(), "bundle"); err2 == nil || err2.Error() != err.Error() {
		t.Errorf("Provision after a deterministic rejection = %v, want the latched rejection %v", err2, err)
	}
}

// TestMaterializerEnforcesAssetCountCap pins the bundle FILE-COUNT cap: a
// listing over maxBundleAssets fails the activation (deterministic, latched)
// before a single byte transfers, leaving nothing behind.
func TestMaterializerEnforcesAssetCountCap(t *testing.T) {
	src := newMapSource()
	many := make([]tool.SkillAsset, maxBundleAssets+1)
	for i := range many {
		many[i] = tool.SkillAsset{Name: fmt.Sprintf("a/%d.txt", i), Size: 1}
	}
	src.assets["bundle"] = many
	base := t.TempDir()
	mat := NewAssetMaterializer(src, base)

	_, err := mat.Provision(context.Background(), "bundle")
	if err == nil || !strings.Contains(err.Error(), "file bundle cap") || !errors.Is(err, errBundleRejected) {
		t.Fatalf("over-count bundle must fail deterministically, got %v", err)
	}
	if got := src.readCount(); got != 0 {
		t.Errorf("count cap must trip BEFORE any transfer, read %d assets", got)
	}
	if _, serr := os.Stat(filepath.Join(base, "bundle")); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("failed bundle left a partial dir behind (stat err = %v)", serr)
	}
}

// TestMaterializerEnforcesNameDepthCap pins the logical-name SEGMENT-DEPTH
// cap: a pathologically nested name fails the activation (deterministic),
// never a partial bundle; a name at exactly the cap passes.
func TestMaterializerEnforcesNameDepthCap(t *testing.T) {
	deep := strings.TrimSuffix(strings.Repeat("d/", maxAssetNameDepth), "/") // exactly maxAssetNameDepth segments
	tooDeep := "x/" + deep                                                   // one over

	src := newMapSource()
	src.assets["bundle"] = []tool.SkillAsset{
		{Name: deep, Size: 1},
		{Name: tooDeep, Size: 1},
	}
	src.data["bundle"][deep] = []byte("k")
	src.data["bundle"][tooDeep] = []byte("k")
	base := t.TempDir()
	mat := NewAssetMaterializer(src, base)

	_, err := mat.Provision(context.Background(), "bundle")
	if err == nil || !strings.Contains(err.Error(), "segment cap") || !errors.Is(err, errBundleRejected) {
		t.Fatalf("over-deep name must fail deterministically, got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(base, "bundle")); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("failed bundle left a partial dir behind (stat err = %v)", serr)
	}

	// At exactly the cap the bundle provisions.
	src2 := newMapSource()
	src2.assets["bundle"] = []tool.SkillAsset{{Name: deep, Size: 1}}
	src2.data["bundle"][deep] = []byte("k")
	if _, err := NewAssetMaterializer(src2, t.TempDir()).Provision(context.Background(), "bundle"); err != nil {
		t.Errorf("a name at exactly %d segments must pass, got %v", maxAssetNameDepth, err)
	}
}

// TestMaterializerProvisionConcurrentOnce pins the per-skill serialization
// under -race: two concurrent Provision calls for the same skill yield the
// same dir and transfer the bundle exactly ONCE (the source's read count is
// one bundle's worth).
func TestMaterializerProvisionConcurrentOnce(t *testing.T) {
	src := newMapSource()
	mat := NewAssetMaterializer(src, t.TempDir())

	type out struct {
		dir string
		err error
	}
	results := make(chan out, 2)
	for range 2 {
		go func() {
			dir, err := mat.Provision(context.Background(), "bundle")
			results <- out{dir, err}
		}()
	}
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent Provision errs: %v / %v", first.err, second.err)
	}
	if first.dir != second.dir {
		t.Errorf("concurrent Provision dirs diverge: %q vs %q", first.dir, second.dir)
	}
	if got, want := src.readCount(), len(src.assets["bundle"]); got != want {
		t.Errorf("source read %d assets, want exactly one bundle's worth (%d)", got, want)
	}
}

// TestSourceActivatorReturnsAssets pins the driver-path Assets population: the
// source activator calls ListSkillAssets on the port and caches the full
// Activation (including Assets) so repeat activations re-render from memory.
func TestSourceActivatorReturnsAssets(t *testing.T) {
	src := newMapSource()
	mat := NewAssetMaterializer(src, t.TempDir())
	act := NewSourceActivator(src, mat)

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

	// Asset-less skill yields nil Assets.
	got, err = act.Activate(context.Background(), "lean")
	if err != nil {
		t.Fatalf("Activate(lean): %v", err)
	}
	if len(got.Assets) != 0 {
		t.Errorf("asset-less skill should have nil Assets, got %v", got.Assets)
	}
}
