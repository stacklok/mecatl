// Package skillfs implements Agent Skills — progressive-disclosure instruction
// units (Claude Code / Agent Skills style) — as an OPT-IN adapter exposing a
// single tool.Tool to the model. This is the READ-ONLY skills core (discovery,
// source, tool, snapshot activator).
//
// This package graduated from internal/adapter/skills into the importable
// engine module (engine/adapter/skillfs) per #328; the root package re-exports
// it via alias and keeps the writable half (drafter/promote/assetcache).
//
// PROGRESSIVE DISCLOSURE (corpus pattern 9, applied to INSTRUCTIONS rather than
// tool schemas): the cheap, always-in-context layer is each skill's METADATA
// header — its name plus a one-line description. The Skill tool's
// Spec().Description enumerates that header for every discovered skill, so it is
// stable across turns and cache-friendly. The expensive layer — a skill's full
// markdown body — loads only when the model ACTIVATES the skill by calling the
// tool with that skill's name; Execute returns the body as the tool result.
//
// LAYERING: this is an adapter. It reads files (discovery is an adapter concern)
// and implements the domain tool.Tool interface; nothing here is imported by a
// domain package. The composition root (cmd/mecated) wires it behind a flag,
// exactly like the memory adapter. A SKILL.md file is YAML frontmatter
// (`name` + `description`) followed by a markdown body, matching the wider
// Agent Skills ecosystem.
//
// TRUST BOUNDARY (the self-improving-skill loop): a skills.Source registered into
// the catalog must serve ONLY operator-controlled content — a SKILL.md steers the
// model like AGENTS.md/CLAUDE.md. The writable SkillDraft tool (DraftTool) lets the
// model PROPOSE a skill, but its Drafter writes ONLY to a QUARANTINE directory that
// is NEVER registered as a catalog Source. Two invariants hold the boundary:
//   - The quarantine dir is required to live OUTSIDE the workspace root, so the
//     model's workspace-confined Write/Edit cannot reach it (enforced by
//     validateSkillDraftConfig in cmd/mecated; fatal on a misconfig). A drafted
//     candidate therefore only ever enters quarantine via the Drafter.
//   - Promotion from quarantine to an active skills dir is an OPERATOR action
//     (`mecated skills promote`, which shows the full candidate, requires
//     confirmation, and verifies `origin: model` provenance), outside the model's
//     reach. The model can never activate its own proposal: author in session N ->
//     operator reviews + promotes -> active in N+1.
//
// RESIDUAL (documented, not silently assumed): absent the deferred OS-level
// sandbox, the Bash tool can write to any absolute path, so the structural boundary
// covers Write/Edit only; cmd/mecated warns when SkillDraft and Bash are enabled
// together. This mirrors mecatl's existing posture that the OS sandbox is the
// deferred wrap point for the command-execution seam.
package skillfs

import "github.com/stacklok/mecatl/engine/tool"

// Skill is a pure value object: one discovered skill's metadata and body. It
// carries no behaviour and no infrastructure types, so it is safe to construct
// in tests and to pass across the adapter boundary.
type Skill struct {
	// Name is the skill's stable identifier, from the frontmatter `name`. It is
	// the value the model passes to the Skill tool to activate this skill, and is
	// what the tool description enumerates.
	Name string
	// Description is the one-line summary from the frontmatter `description`. This
	// is the cheap, always-in-context metadata that steers the model on WHEN to
	// activate the skill.
	Description string
	// Body is the markdown content following the frontmatter: the full
	// instructions that load on activation.
	Body string
	// Path is the source SKILL.md path the skill was discovered at, retained for
	// diagnostics and so a reviewer can trace a skill back to its file. It is
	// ADAPTER-PRIVATE state: it never crosses the tool.SkillSource port (which
	// carries logical bundles only — no path/dir/root concept); the path business
	// it feeds (FSSource.AssetDir/AssetDirs) is adapter-public NON-PORT API
	// consumed only by the composition layer and the same-package snapshot
	// activator.
	Path string
	// Origin is the admission TIER the skill entered through (explicit flag,
	// project tier, user tier, remote driver) — a closed tool.SkillOrigin label,
	// never a location. DirSource stamps it from its Tier; ResolveSources sets
	// the tiers. It backs SkillMeta.Origin on the port.
	Origin tool.SkillOrigin
	// License, Compatibility, Metadata, and AllowedTools mirror the like-named
	// SkillMeta fields and are ADVISORY/observability only — never trust-bearing,
	// never a gate. They are parsed from the optional `license`/`compatibility`/
	// `metadata`/`allowed-tools` SKILL.md frontmatter and carried verbatim
	// (byte-capped defensively); empty/zero when the skill omits them.
	// AllowedTools is the agentskills.io Experimental `allowed-tools` field — a
	// list of tool names the skill EXPECTS to use; it is surfaced as an advisory
	// note on activation and is NEVER a permission grant (calls still resolve
	// through the normal deny-dominant policy).
	License       string
	Compatibility string
	Metadata      map[string]string
	AllowedTools  []string
}
