// Package skills is the in-repo adapter for Agent Skills. The READ-ONLY core
// (discovery, source, Skill tool, snapshot activator) graduated into the
// importable engine module (engine/adapter/skillfs, issue #328) and is
// re-exported here via thin type/func/const aliases; the writable SkillDraft
// half (drafter/promote/assetcache) stays in this package and reaches the core
// through those aliases. See alias.go and the skillfs package doc for the real
// contract.
package skills

import (
	"context"

	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

// alias.go re-exports the READ-ONLY skills core (discovery, source, tool,
// snapshot activator) that graduated into the importable engine module
// (engine/adapter/skillfs, issue #328) so every existing caller — internal/app,
// cmd/mecated, internal/adapter/server, internal/adapter/grpcdriver,
// internal/adapter/soul, internal/adapter/memory, internal/adapter/workspacetrust
// — keeps compiling against the skills package unchanged. The writable
// SkillDraft half (drafter/promote/assetcache) stays in this root package and
// reaches the core through these aliases. The discovery, parsing, tool body,
// and activator bodies live once, in skillfs; these are thin type/func/const
// aliases, not re-implementations, so there is no second copy to drift.

// Skill is a pure value object: one discovered skill's metadata and body. See
// skillfs.Skill.
type Skill = skillfs.Skill

// Source is the pluggable EXTENSIBILITY POINT for where skills come from. See
// skillfs.Source.
type Source = skillfs.Source

// SkipError records one diagnostic from discovery. See skillfs.SkipError.
type SkipError = skillfs.SkipError

// MultiSource composes an ORDERED list of Sources into one. See
// skillfs.MultiSource.
type MultiSource = skillfs.MultiSource

// DirSource is the local-OS-filesystem implementation of Source. See
// skillfs.DirSource.
type DirSource = skillfs.DirSource

// FSSource is the FILESYSTEM implementation of the tool.SkillSource port. See
// skillfs.FSSource.
type FSSource = skillfs.FSSource

// Activator is the seam the Skill tool loads a skill through on activation. See
// skillfs.Activator.
type Activator = skillfs.Activator

// Activation is the load-on-activation payload the Skill tool renders. See
// skillfs.Activation.
type Activation = skillfs.Activation

// ResolveOptions configures the known-path resolver. See
// skillfs.ResolveOptions.
type ResolveOptions = skillfs.ResolveOptions

// Tool is the single model-facing skills tool. See skillfs.Tool.
type Tool = skillfs.Tool

// ToolName is the catalog name of the single skills tool. See
// skillfs.ToolName.
const ToolName = skillfs.ToolName

// SkillFileName is the conventional file every skill directory contains. See
// skillfs.SkillFileName.
const SkillFileName = skillfs.SkillFileName

// DefaultDir is the conventional project-level skills directory. See
// skillfs.DefaultDir.
const DefaultDir = skillfs.DefaultDir

// MaxDescriptionBytes caps a skill's one-line description. See
// skillfs.MaxDescriptionBytes.
const MaxDescriptionBytes = skillfs.MaxDescriptionBytes

// MaxLicenseBytes, MaxCompatibilityBytes, MaxMetadataEntries, and
// MaxMetadataValueBytes are the advisory caps for the optional license/
// compatibility/metadata frontmatter. See the skillfs constants. Exported so
// the remote-driver client (grpcdriver) re-clamps defensively to the SAME caps
// the parser uses.
//
// MaxAllowedTools and MaxAllowedToolNameBytes are the advisory caps for the
// optional `allowed-tools` frontmatter (agentskills.io, Experimental). Same
// export rationale.
const (
	MaxLicenseBytes         = skillfs.MaxLicenseBytes
	MaxCompatibilityBytes   = skillfs.MaxCompatibilityBytes
	MaxMetadataEntries      = skillfs.MaxMetadataEntries
	MaxMetadataValueBytes   = skillfs.MaxMetadataValueBytes
	MaxAllowedTools         = skillfs.MaxAllowedTools
	MaxAllowedToolNameBytes = skillfs.MaxAllowedToolNameBytes
)

// ProjectDirMecatl is the project-level skills dir under the workspace. See
// skillfs.ProjectDirMecatl.
const ProjectDirMecatl = skillfs.ProjectDirMecatl

// ProjectDirClaude is the Claude-Code-compatible project-level skills dir. See
// skillfs.ProjectDirClaude.
const ProjectDirClaude = skillfs.ProjectDirClaude

// NewTool builds the Skill tool over the given skill metadata and activator.
// See skillfs.NewTool.
func NewTool(metas []tool.SkillMeta, act Activator) Tool { return skillfs.NewTool(metas, act) }

// NewFSSource resolves the given sources ONCE and returns the snapshot source
// plus the aggregated discovery diagnostics. See skillfs.NewFSSource.
func NewFSSource(ctx context.Context, sources ...Source) (*FSSource, []SkipError, error) {
	return skillfs.NewFSSource(ctx, sources...)
}

// NewSnapshotActivator returns the FS activator. See skillfs.NewSnapshotActivator.
func NewSnapshotActivator(src *FSSource) Activator { return skillfs.NewSnapshotActivator(src) }

// NewSourceActivator returns the driver activator over the PORT only. See
// skillfs.NewSourceActivator. It is a var re-export (not a wrapper func) because
// skillfs.NewSourceActivator's second parameter is an UNEXPORTED
// assetProvisioner interface; this package's *AssetMaterializer satisfies it
// structurally (Provision), so callers passing a *AssetMaterializer compile
// unchanged. A wrapper would have to name the unexported type.
var NewSourceActivator = skillfs.NewSourceActivator

// NewSkillCommandSource builds a prompt.CommandSource over the resolved skill
// seam so each discovered skill is invocable as /<skill-name>. See
// skillfs.NewSkillCommandSource. Re-exported so the composition layer
// (internal/app) composes the bridge over the SAME seam pieces the Skill tool
// uses, without importing the engine adapter directly.
func NewSkillCommandSource(metas []tool.SkillMeta, act Activator) prompt.CommandSource {
	return skillfs.NewSkillCommandSource(metas, act)
}

// RegisterSource discovers skills from src and, when at least one valid skill
// is found, registers a single Skill tool into cat. See skillfs.RegisterSource.
func RegisterSource(ctx context.Context, cat *tool.Catalog, src Source) ([]Skill, []SkipError, error) {
	return skillfs.RegisterSource(ctx, cat, src)
}

// Register discovers skills under a single directory and registers the Skill
// tool. See skillfs.Register.
func Register(cat *tool.Catalog, dir string) ([]Skill, []SkipError, error) {
	return skillfs.Register(cat, dir)
}

// Discover scans dir for skills. See skillfs.Discover.
func Discover(dir string) ([]Skill, []SkipError, error) { return skillfs.Discover(dir) }

// ResolveSources builds the ORDERED, highest-precedence-first Source list from
// the conventional locations plus any explicit paths. See skillfs.ResolveSources.
func ResolveSources(opts ResolveOptions) []Source { return skillfs.ResolveSources(opts) }

// ScanForInjection scans s for any disallowed instruction-injection / role-
// override marker. See skillfs.ScanForInjection.
func ScanForInjection(s string) (marker string, found bool) { return skillfs.ScanForInjection(s) }

// ParseSkill splits raw into YAML frontmatter and a markdown body and validates
// the required header fields. It is exported so this package's writable half
// (promote.go) re-runs the promotion-gate structural validation through the
// SAME parser the read-only core uses. See skillfs.ParseSkill.
func ParseSkill(raw []byte, path string) (Skill, string, []string) {
	return skillfs.ParseSkill(raw, path)
}

// ValidSkillName reports whether name is a valid skill activation name under
// the shared grammar. It is exported so this package's writable half (the
// DirDrafter's validateName) routes through the SAME validator the read-only
// discovery core uses — a single source of truth, not two regexes to drift.
// See skillfs.ValidSkillName.
func ValidSkillName(name string) bool {
	return skillfs.ValidSkillName(name)
}
