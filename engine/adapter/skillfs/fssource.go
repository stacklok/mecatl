package skillfs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/stacklok/mecatl/engine/tool"
)

// FSSource is the FILESYSTEM implementation of the tool.SkillSource port: a
// snapshot of the skills discovered from the composed Source list (explicit
// dirs + conventional locations), serving each skill as a LOGICAL BUNDLE —
// metadata, body, and auxiliary payloads addressed by logical name. The port
// carries no path/dir/root concept; the path business an FS deployment still
// needs (the per-skill read-root allowlist, the activation base directory) is
// exposed as adapter-public NON-PORT methods (AssetDir/AssetDirs) consumed
// only by the composition layer.
//
// SNAPSHOT SEMANTICS: discovery runs ONCE at construction (NewMultiSource over
// the given sources) and the metadata + bodies are retained in memory, so
// ListSkills/SkillBody are stable for the life of the source — the build-once
// trust-gate invariant (an untrusted workspace's project tier is gated at
// SOURCE CONSTRUCTION, in ResolveSources). Asset listing/reading consults the
// disk lazily per call, confined to each skill's own directory via os.OpenRoot
// (symlink containment parity with the osfs Workspace): symlinked entries are
// skipped, SKILL.md itself is excluded (it is the body, not a payload), and a
// logical name is the slash-relative walk path inside the skill's directory.
type FSSource struct {
	skills map[string]Skill  // by activation name
	list   []Skill           // name-sorted snapshot (the discovered value objects)
	metas  []tool.SkillMeta  // name-sorted port-shaped snapshot
	dirs   map[string]string // name → canonical per-skill dir ("" when the skill has no source path)
	roots  []string          // unique canonical per-skill dirs, in name order
}

// compile-time assertion that FSSource satisfies the port.
var _ tool.SkillSource = (*FSSource)(nil)

// NewFSSource resolves the given sources ONCE (highest-precedence first, the
// NewMultiSource collision rule) and returns the snapshot source plus the
// aggregated discovery diagnostics. A genuine discovery fault returns a
// non-nil error (with the diagnostics gathered so far); an absent dir is
// simply "no skills" (opt-in), exactly as before.
func NewFSSource(ctx context.Context, sources ...Source) (*FSSource, []SkipError, error) {
	discovered, skips, err := NewMultiSource(sources...).Skills(ctx)
	if err != nil {
		return nil, skips, err
	}
	src := &FSSource{
		skills: make(map[string]Skill, len(discovered)),
		metas:  make([]tool.SkillMeta, 0, len(discovered)),
		dirs:   make(map[string]string, len(discovered)),
	}
	seen := make(map[string]bool, len(discovered))
	// MultiSource returns the merged set name-sorted and de-duplicated already;
	// keep that order so the metas snapshot and AssetDirs are deterministic.
	for _, sk := range discovered {
		src.skills[sk.Name] = sk
		src.list = append(src.list, sk)
		dir := skillBaseDir(sk.Path)
		src.dirs[sk.Name] = dir
		if dir != "" && !seen[dir] {
			seen[dir] = true
			src.roots = append(src.roots, dir)
		}
		origin := sk.Origin
		if origin == "" {
			// A hand-constructed Source that did not stamp a tier: an
			// operator-configured location.
			origin = tool.SkillOriginExplicit
		}
		assets, aerr := src.listAssets(sk.Name)
		src.metas = append(src.metas, tool.SkillMeta{
			Name:        sk.Name,
			Description: sk.Description,
			Origin:      origin,
			HasAssets:   aerr == nil && len(assets) > 0,
		})
	}
	return src, skips, nil
}

// ListSkills returns the name-sorted, unique metadata snapshot.
func (s *FSSource) ListSkills(_ context.Context) ([]tool.SkillMeta, error) {
	out := make([]tool.SkillMeta, len(s.metas))
	copy(out, s.metas)
	return out, nil
}

// SkillBody returns the named skill's full instruction body from the
// construction-time snapshot (bodies are retained; no re-read).
func (s *FSSource) SkillBody(_ context.Context, name string) (string, error) {
	sk, ok := s.skills[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
	}
	return sk.Body, nil
}

// ListSkillAssets enumerates the named skill's auxiliary payloads: every
// regular file under the skill's directory except SKILL.md, named by its
// slash-relative walk path. Symlinked entries (files or directories) are
// skipped — containment parity with the osfs Workspace's symlink posture.
func (s *FSSource) ListSkillAssets(_ context.Context, name string) ([]tool.SkillAsset, error) {
	if _, ok := s.skills[name]; !ok {
		return nil, fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
	}
	return s.listAssets(name)
}

// ReadSkillAsset returns one payload's bytes by logical name, confined to the
// skill's own directory via os.OpenRoot. An invalid logical name is an error,
// never content; an unknown skill or asset is the ErrSkillAssetNotFound
// sentinel.
func (s *FSSource) ReadSkillAsset(_ context.Context, skill, asset string) ([]byte, error) {
	if !tool.ValidSkillAssetName(asset) {
		return nil, fmt.Errorf("skills: invalid logical asset name %q (slash-separated, relative, no \".\"/\"..\" segments)", asset)
	}
	if _, ok := s.skills[skill]; !ok {
		return nil, fmt.Errorf("%w: skill %q", tool.ErrSkillAssetNotFound, skill)
	}
	dir := s.dirs[skill]
	if dir == "" || asset == SkillFileName {
		// No payload directory (hand-constructed skill), or the body file itself
		// (a body is not a payload).
		return nil, fmt.Errorf("%w: %q/%q", tool.ErrSkillAssetNotFound, skill, asset)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %q/%q (%v)", tool.ErrSkillAssetNotFound, skill, asset, err)
	}
	defer func() { _ = root.Close() }() // read-only handle
	rel := filepath.FromSlash(asset)
	// Listing skips symlinked entries; reading must agree, so a symlink (even
	// one resolving inside the root) is not a payload.
	info, err := root.Lstat(rel)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q/%q", tool.ErrSkillAssetNotFound, skill, asset)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("%w: %q/%q (%v)", tool.ErrSkillAssetNotFound, skill, asset, err)
	}
	defer func() { _ = f.Close() }() // read-only handle
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("skills: read asset %q/%q: %w", skill, asset, err)
	}
	return data, nil
}

// listAssets walks the skill's directory through os.OpenRoot and returns the
// name-sorted payload descriptors.
func (s *FSSource) listAssets(name string) ([]tool.SkillAsset, error) {
	dir := s.dirs[name]
	if dir == "" {
		return nil, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		// The directory existed at discovery; treat a vanished/unopenable dir as
		// an asset-less skill rather than failing the bundle (the body snapshot
		// is still servable).
		return nil, nil //nolint:nilerr // deliberate fail-soft: bundle stays servable without payloads
	}
	defer func() { _ = root.Close() }() // read-only handle
	var assets []tool.SkillAsset
	werr := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Symlinked entries are SKIPPED (fs.WalkDir does not follow symlinks, so
		// a symlinked directory is never descended either) — containment parity.
		if !d.Type().IsRegular() {
			return nil
		}
		logical := filepath.ToSlash(p)
		if logical == SkillFileName {
			return nil // the body, not a payload
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		assets = append(assets, tool.SkillAsset{
			Name:       logical,
			Size:       info.Size(),
			Executable: info.Mode()&0o111 != 0,
		})
		return nil
	})
	if werr != nil {
		return nil, fmt.Errorf("skills: list assets of %q: %w", name, werr)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	return assets, nil
}

// Discovered returns the name-sorted discovered Skill value objects (the
// adapter shape — metadata + body + path). Adapter-public, NON-PORT: it backs
// the legacy []Skill consumers (RegisterSource's return, the SkillDraft
// novelty snapshot, the ListSkills projection), never the port.
func (s *FSSource) Discovered() []Skill {
	out := make([]Skill, len(s.list))
	copy(out, s.list)
	return out
}

// --- Adapter-public NON-PORT path API (composition-only consumers) ----------

// AssetDir returns the CANONICAL per-skill directory the named skill's bundled
// files live in (the directory holding its SKILL.md), and whether the skill
// has one. It is NOT part of the tool.SkillSource port — the port carries no
// path concept — but FS deployments still serve assets IN PLACE through the
// existing Read/read-roots contract; its only consumers are the composition
// layer and the same-package snapshot activator (the base-directory header).
func (s *FSSource) AssetDir(name string) (string, bool) {
	dir, ok := s.dirs[name]
	if !ok || dir == "" {
		return "", false
	}
	return dir, true
}

// AssetDirs returns the unique, canonicalized PER-SKILL directories of the
// snapshot — the read-only allowed roots every production osfs Workspace is
// constructed with (osfs.WithReadRoots), so the model can Read an activated
// skill's SKILL.md and bundled references/scripts/assets by the absolute path
// the Skill tool's "Base directory" header advertises, even when the skill
// lives outside the workspace (~/.claude/skills/…).
//
// PER-SKILL dirs, never whole source dirs: a shadowed skill's directory or a
// random sibling under ~/.claude/skills must never become readable. The input
// is this source's own snapshot, which went through ResolveSources'
// project-tier trust gate — an untrusted workspace's project-tier skills are
// never discovered, so their dirs never enter this allowlist (trust-gating by
// construction). The SkillDraft quarantine dir can never appear either: it is
// never a Source, so it never yields a discovered skill. Canonicalization
// matches the osfs enforcement layer (osfs.ResolveRoot — abs + EvalSymlinks),
// the same comparison-contract discipline activeSkillDirs follows.
func (s *FSSource) AssetDirs() []string {
	out := make([]string, len(s.roots))
	copy(out, s.roots)
	return out
}

// skillBaseDir returns the CANONICAL directory containing the skill's SKILL.md,
// or "" when the skill has no source path (hand-constructed test values).
// Canonicalization goes through resolveRoot — the EXACT resolver the
// Workspace's read-root allowlist is keyed on — so the path the model is told
// matches the allowlist byte-for-byte even when the discovery path crosses a
// symlink (e.g. /home → /var/home); a cleaned-but-unresolved Dir would advertise
// a path the workspace then refuses, reintroducing the bug for symlinked homes.
func skillBaseDir(path string) string {
	if path == "" {
		return ""
	}
	dir := filepath.Dir(path)
	if resolved, err := resolveRoot(dir); err == nil {
		return resolved
	}
	return filepath.Clean(dir)
}
