package tool

import (
	"context"
	"errors"
	"strings"
	"unicode"
)

// SkillOrigin classifies the ADMISSION TIER a skill entered the catalog
// through. It is a tier label, NEVER a location: no implementation may put a
// path, directory, URL, or any locator in it (that is the adapter's private
// business). Enforcement of trust happens at SOURCE CONSTRUCTION time in the
// composition layer (an untrusted workspace's project tier is never
// constructed); Origin exists for observability and inspection only.
type SkillOrigin string

// The CLOSED admission-tier label set — implementations must never mint a new
// label (a consumer that does not recognise one normalizes to Driver).
const (
	SkillOriginExplicit SkillOrigin = "explicit" // operator-configured location/flag
	SkillOriginProject  SkillOrigin = "project"  // workspace-tier (trust-gated at construction)
	SkillOriginUser     SkillOrigin = "user"     // user-tier (never trust-gated)
	SkillOriginDriver   SkillOrigin = "driver"   // operator-configured remote driver
)

// SkillMeta is the always-in-context metadata layer of one skill: what the
// Skill tool's description enumerates. Pure value object — no body, no
// behaviour, NO file/path/root concept.
type SkillMeta struct {
	Name        string      // stable activation key; non-empty
	Description string      // one-line trigger metadata; non-empty, single-line, byte-capped by the source
	Origin      SkillOrigin // admission tier (observability only)
	HasAssets   bool        // whether ListSkillAssets will return at least one asset
	// License is the optional `license` frontmatter field (e.g. "MIT",
	// "Apache-2.0"). ADVISORY/observability metadata only — never trust-bearing,
	// never a gate; empty when the skill omits it. Carried verbatim from the
	// source (byte-capped defensively), never interpreted.
	License string
	// Compatibility is the optional `compatibility` frontmatter field (a free-form
	// advisory string such as "mecatl >= 0.1"). ADVISORY/observability metadata
	// only — never trust-bearing, never enforced as a gate; empty when the skill
	// omits it. Surfaced as an advisory note on activation, never parsed.
	Compatibility string
	// Metadata is the optional `metadata` frontmatter map (string→string), a
	// free-form advisory bag (e.g. {author: stacklok, version: "1"}). ADVISORY/
	// observability metadata only — never trust-bearing; nil/empty when the skill
	// omits it. Values are byte-capped defensively; the whole map drops to nil on
	// an oversized entry/count. Never interpreted by the harness.
	Metadata map[string]string
	// AllowedTools is the optional `allowed-tools` frontmatter field
	// (agentskills.io, Experimental): a list of tool names the skill EXPECTS to
	// use. ADVISORY METADATA ONLY — it names the tools the skill anticipates
	// calling, surfaced as a note on Skill activation so the model learns the
	// author's intent. It is NEVER a permission grant: the permission evaluator
	// (governance/port.PermissionPolicy/engine/agent dispatch) NEVER reads it.
	// Every call still resolves through the normal deny-dominant policy — at
	// every posture, including yolo — so a skill declaring `allowed-tools: "Bash"`
	// does NOT pre-approve, loosen, or auto-approve a Bash call. nil/empty when the
	// skill omits it (a skill without the field renders byte-identically to
	// before). The parser splits the YAML value on whitespace and defensively
	// caps the count at ≤64 names and each name at ≤64 chars (recording a
	// non-fatal warning note on overflow, keeping the parsed prefix).
	AllowedTools []string
}

// SkillAsset describes one auxiliary payload of a skill, addressed by LOGICAL
// name. A logical name is a slash-separated, RELATIVE identifier in the
// skill's own namespace (e.g. "references/api.md", "scripts/run.sh") — the
// exact namespace skill instruction bodies already reference. Not an OS path:
// no leading separator, no "."/".." segments, no backslashes, no NUL
// (ValidSkillAssetName is the single shared validator).
type SkillAsset struct {
	Name       string
	Size       int64 // payload size in bytes (advisory; readers re-enforce caps)
	Executable bool  // payload should carry the executable bit if materialized
}

// Sentinel errors every SkillSource implementation returns (wrapped, so
// errors.Is holds) for an unknown skill (SkillBody/ListSkillAssets) or an
// unknown skill/asset pair (ReadSkillAsset).
var (
	ErrSkillNotFound      = errors.New("tool: skill not found")
	ErrSkillAssetNotFound = errors.New("tool: skill asset not found")
)

// SkillSource is the read-only seam skills cross into the harness: a LOGICAL
// BUNDLE of identity + trigger metadata (ListSkills), an instruction body
// (SkillBody), and auxiliary payloads addressed by logical name
// (ListSkillAssets/ReadSkillAsset). Where a bundle comes from — directories,
// a database, a registry process — is entirely the implementation's private
// business; no path, directory, or root concept appears here, so the engine
// cannot tell a filesystem source from a remote one.
//
// Lifecycle: sources are SNAPSHOT-semantics — ListSkills is stable for the
// life of the source (the harness resolves once at build; there is no watch
// seam, deliberately matching the build-once trust-gate invariant).
type SkillSource interface {
	ListSkills(ctx context.Context) ([]SkillMeta, error)                     // sorted by Name, unique
	SkillBody(ctx context.Context, name string) (string, error)              // unknown → ErrSkillNotFound
	ListSkillAssets(ctx context.Context, name string) ([]SkillAsset, error)  // unknown → ErrSkillNotFound
	ReadSkillAsset(ctx context.Context, skill, asset string) ([]byte, error) // unknown → ErrSkillAssetNotFound; invalid name → error, never content
}

// ValidSkillAssetName reports whether name is a valid LOGICAL asset name:
// non-empty, slash-separated, relative, no empty/"."/".."/ segments, no
// backslash, and no control or line-separator rune — the ONE validator every
// implementation/consumer shares.
func ValidSkillAssetName(name string) bool {
	if name == "" {
		return false
	}
	if strings.ContainsAny(name, "\\\x00") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
