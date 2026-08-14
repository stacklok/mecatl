package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// validateSkillDraftConfig enforces the SkillDraft trust boundary at build time
// when the feature is enabled (SkillsDraftDir set). It is fatal on a misconfig that
// would let the model reach the quarantine through a tool, never a silent
// degradation.
//
// The boundary is STRUCTURAL, not a permission rule. Write/Edit are confined by the
// osfs Workspace to the workspace root, so a quarantine dir OUTSIDE that root is
// unreachable by them. We therefore require the quarantine to live outside the
// workspace (fatal otherwise) and to be disjoint from every active skills dir
// (fatal on overlap — an overlapping quarantine would let a draft masquerade as a
// promoted, trusted skill).
//
// NOTE on Bash: absent the deferred OS-level sandbox, the Bash tool can write to
// ANY absolute path and so can reach any quarantine/skills dir regardless of
// location. The structural boundary therefore covers Write/Edit only; the residual
// Bash path is the same big-hammer capability Bash already grants (it can write any
// file), gated by Ask, and is the wrap point for the deferred sandbox.
// warnSkillDraftResiduals logs a loud warning when SkillDraft and Bash are enabled
// together so the operator knows the boundary is fully structural only shell-less
// or sandboxed.
func validateSkillDraftConfig(cfg Config) error {
	if cfg.SkillsDraftDir == "" {
		return nil
	}

	// Canonicalize through the SAME resolver the osfs Workspace uses to confine
	// Write/Edit (abs + EvalSymlinks). filepath.Abs alone diverges on a symlinked
	// workspace and would let a dir we deem "outside" actually resolve inside the
	// model-writable os.Root — so this MUST match the enforcement layer exactly.
	quarantine, err := osfs.ResolveRoot(cfg.SkillsDraftDir)
	if err != nil {
		return fmt.Errorf("skills-draft-dir %q: %w", cfg.SkillsDraftDir, err)
	}
	workspace, err := osfs.ResolveRoot(cfg.Workspace)
	if err != nil {
		return fmt.Errorf("workspace %q: %w", cfg.Workspace, err)
	}

	// The quarantine MUST be outside the workspace root, so the model's
	// workspace-confined Write/Edit cannot reach it. Inside-workspace is fatal.
	if quarantine == workspace || dirsOverlap(workspace, quarantine) {
		return fmt.Errorf("skills-draft-dir %q must be OUTSIDE the workspace root %q: "+
			"Write/Edit are confined to the workspace, so an in-workspace quarantine would be model-writable, "+
			"defeating the draft→promote trust boundary", quarantine, workspace)
	}

	// The quarantine MUST be disjoint from every active skills dir, else a draft
	// could land in (or shadow) the trusted catalog without an operator promote.
	for _, ad := range activeSkillDirs(cfg) {
		if dirsOverlap(quarantine, ad) {
			return fmt.Errorf("skills-draft-dir %q overlaps an active skills dir %q: "+
				"the quarantine must be disjoint from every skills dir / conventional skills location", quarantine, ad)
		}
	}
	return nil
}

// warnSkillDraftResiduals logs the residual (non-structural) parts of the SkillDraft
// trust boundary so an operator deploys it knowingly. It is advisory only —
// validateSkillDraftConfig has already enforced the structural invariants. Two
// residuals exist because mecatl has no OS-level sandbox yet:
//   - Bash (when enabled) can write to ANY absolute path, so it can reach the
//     quarantine or active catalog regardless of location — the structural boundary
//     covers Write/Edit only.
//   - An active skills dir INSIDE the workspace is reachable by the model's
//     Write/Edit (Ask-gated), so the trusted catalog is not write-isolated from the
//     model; placing active skills OUTSIDE the workspace makes that boundary
//     structural too.
func warnSkillDraftResiduals(cfg Config) {
	if cfg.SkillsDraftDir == "" {
		return
	}
	if !cfg.NoBash && cfg.Shell != "" {
		cfg.diag().Log(context.Background(), port.LevelWarn, "SkillDraft trust boundary is structural for Write/Edit only: Bash is enabled and (absent an OS sandbox) can write to any path, so it can reach the skills trees. For a fully structural boundary, run shell-less (no bash) or under an OS sandbox.")
	}
	workspace, err := osfs.ResolveRoot(cfg.Workspace)
	if err != nil {
		return
	}
	for _, ad := range activeSkillDirs(cfg) {
		if ad == workspace || dirsOverlap(workspace, ad) {
			cfg.diag().Log(context.Background(), port.LevelWarn, "an active skills dir is INSIDE the workspace and is reachable by the model's Write/Edit (Ask-gated); place active skills OUTSIDE the workspace so promoted skills cannot be planted directly by the model",
				"active_skills_dir", ad, "workspace", workspace)
		}
	}
}

// activeSkillDirs returns the cleaned directory paths of every active skills Source
// (explicit dirs plus the conventional locations when SkillsConventional is set), so
// the overlap check covers the same trees the catalog serves.
func activeSkillDirs(cfg Config) []string {
	// Build through skillResolveOptions (the single choke point) so the draft-overlap
	// check covers exactly the trees the catalog serves and inherits the Phase-2a
	// project-tier trust gate by construction.
	sources := skills.ResolveSources(skillResolveOptions(cfg))
	var dirs []string
	for _, s := range sources {
		if ds, ok := s.(skills.DirSource); ok && ds.Dir != "" {
			// Canonicalize identically to the Workspace confinement + the quarantine
			// check (abs + EvalSymlinks) so overlap/containment comparisons can't drift.
			if resolved, err := osfs.ResolveRoot(ds.Dir); err == nil {
				dirs = append(dirs, resolved)
			} else {
				dirs = append(dirs, filepath.Clean(ds.Dir))
			}
		}
	}
	return dirs
}

// dirsOverlap reports whether a and b are the same directory or one contains the
// other. It compares cleaned paths via filepath.Rel so a nested relationship in
// either direction counts as overlap.
func dirsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		if err != nil {
			return false
		}
		return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
	}
	return contains(a, b) || contains(b, a)
}
