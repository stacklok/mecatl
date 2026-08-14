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
// metadata, body, and auxiliary payloads addressed by logical name. Paths stay
// entirely inside this adapter; consumers use SkillSource methods only.
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
}

// compile-time assertion that FSSource satisfies the port.
var _ tool.SkillSource = (*FSSource)(nil)

// These bounds intentionally mirror the remote driver inventory contract
// (internal/adapter/grpcdriver). They are carried here because engine is a
// standalone module and must not import the root module.
const (
	maxSkillInventoryEntries   = 1_024
	maxSkillInventoryNameBytes = 32 << 10
)

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
	// MultiSource returns the merged set name-sorted and de-duplicated already;
	// keep that order so the metadata snapshot is deterministic.
	for _, sk := range discovered {
		src.skills[sk.Name] = sk
		src.list = append(src.list, sk)
		dir := skillBaseDir(sk.Path)
		src.dirs[sk.Name] = dir
		origin := sk.Origin
		if origin == "" {
			// A hand-constructed Source that did not stamp a tier: an
			// operator-configured location.
			origin = tool.SkillOriginExplicit
		}
		assets, aerr := src.listAssets(sk.Name)
		if aerr != nil {
			return nil, skips, aerr
		}
		src.metas = append(src.metas, tool.SkillMeta{
			Name:          sk.Name,
			Description:   sk.Description,
			Origin:        origin,
			HasAssets:     len(assets) > 0,
			License:       sk.License,
			Compatibility: sk.Compatibility,
			Metadata:      sk.Metadata,
			AllowedTools:  sk.AllowedTools,
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
	if info.Size() > maxSkillAssetBytes {
		return nil, fmt.Errorf("skills: asset %q/%q is too large (%d bytes; limit %d bytes)", skill, asset, info.Size(), maxSkillAssetBytes)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("%w: %q/%q (%v)", tool.ErrSkillAssetNotFound, skill, asset, err)
	}
	defer func() { _ = f.Close() }() // read-only handle
	// Read at most one byte beyond the model-facing cap. This bounds allocation
	// even if the file grows after Lstat and lets the caller reject the whole
	// payload rather than returning a truncated asset.
	data, err := io.ReadAll(io.LimitReader(f, maxSkillAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("skills: read asset %q/%q: %w", skill, asset, err)
	}
	if len(data) > maxSkillAssetBytes {
		return nil, fmt.Errorf("skills: asset %q/%q is too large (limit %d bytes)", skill, asset, maxSkillAssetBytes)
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
	var (
		assets    []tool.SkillAsset
		nameBytes int
	)
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
		if !tool.ValidSkillAssetName(logical) {
			return fmt.Errorf("skills: invalid logical asset name %q", logical)
		}
		// Enforce the complete-inventory contract while walking, before fetching
		// metadata or growing the result slice. Any overflow rejects the whole
		// inventory; callers never observe a partial prefix.
		if len(assets) >= maxSkillInventoryEntries {
			return fmt.Errorf("skills: asset inventory of %q exceeds %d entries", name, maxSkillInventoryEntries)
		}
		if len(logical) > maxSkillInventoryNameBytes-nameBytes {
			return fmt.Errorf("skills: asset inventory names of %q exceed %d bytes", name, maxSkillInventoryNameBytes)
		}
		nameBytes += len(logical)
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

// skillBaseDir returns the directory containing the skill's SKILL.md, or ""
// when the skill has no source path. It is private adapter state used to list
// and read logical assets; it is never exposed to a consumer.
func skillBaseDir(path string) string {
	if path == "" {
		return ""
	}
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return ""
	}
	return filepath.Clean(dir)
}
