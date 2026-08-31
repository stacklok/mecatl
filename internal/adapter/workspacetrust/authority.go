package workspacetrust

import (
	"encoding/json"
	"path/filepath"
	"strings"

	yaml "github.com/goccy/go-yaml"
)

// authority.go answers "does this workspace carry a project AUTHORITY SET worth
// gating?" (Workspace-Trust feature, Phase 2c — the mecatui first-encounter
// prompt). A NOT-trusted workspace with NO project authority set has nothing to
// gate, so the pre-TUI prompt is SKIPPED (the operator is not nagged for a repo
// that contributes no project-tier soul/agents/commands/skills/allows). The
// prompt fires only when there IS something a trust grant would admit.
//
// # The authority set
//
// HasProjectAuthority reports true when ANY of these is present under the
// workspace root:
//
//   - the project SOUL              <ws>/.mecatl/soul.md
//   - a project AGENT/COMMAND/SKILL def under any anchorDirs dir (the SAME
//     project-tier dirs the identity anchor folds — agents/skills/commands)
//   - a project mecatl settings.yaml/settings.local.yaml with a non-empty
//     `permissions.allow:` list (the project-supplied ALLOW rules the trust gate
//     admits; deny/ask are honoured regardless, so they are NOT authority worth
//     prompting on)
//   - a project Claude settings.json/settings.local.json with a non-empty
//     `{"permissions":{"allow":[...]}}` list — the SAME files permconfig imports
//     when ImportClaudePermissions is on (the mecatui embedded server default), so a
//     repo whose only project ALLOW rules live in .claude/settings.json still has
//     admittable authority and MUST trigger the prompt (the .claude omission was a
//     trigger-surface false-negative — it failed safe to untrusted, but silently
//     withheld those allows without ever asking).
//
// A settings file's PRESENCE alone is not authority — only its ALLOW rules are,
// because deny/ask apply trusted or not. This tracks the admission gate: the prompt
// fires when a trust grant would change what is admitted. (One conservative
// over-approximation: permconfig demotes a Claude `WebFetch(domain:...)` allow to
// Ask, so a .claude allow list consisting ONLY of such specs would be admitted as
// Ask, not Allow — this probe still counts it as authority and prompts. That is the
// safe direction: it can only ever cause an extra prompt for a repo that does carry
// project allow specs, never a missed one.)
//
// # Why here (not the anchor)
//
// This is a PRESENCE probe, not the drift anchor: it short-circuits on the first
// member found (it does not hash anything), and it ADDS the settings.yaml allow
// check that the anchor deliberately EXCLUDES (MUST-FIX 1 — settings.yaml is not
// drift-anchored because permissions churn). It reuses the anchorIO seam so it is
// offline-testable on the same in-memory tree the anchor tests use.

// The project-tier permission config files, relative to the workspace root. They
// mirror permconfig's projectFile* constants (the mecatl YAML pair AND the
// Claude-Code JSON pair permconfig imports under ImportClaudePermissions); kept as
// local constants (cheap strings) to avoid coupling this adapter leaf to permconfig.
// Only their ALLOW list counts as authority (deny/ask apply regardless).
const (
	projectSettingsRel            = ".mecatl/settings.yaml"
	projectSettingsLocalRel       = ".mecatl/settings.local.yaml"
	projectClaudeSettingsRel      = ".claude/settings.json"
	projectClaudeSettingsLocalRel = ".claude/settings.local.json"
)

// allowProbeYAML is the SUBSET of a project mecatl settings.yaml this probe parses:
// just the `permissions.allow:` list. It mirrors permconfig.Config/Permissions but
// reads only the allow bucket — the one bucket the trust gate admits. YAML ignores
// keys a struct does not declare, so this coexists with the full permconfig parse.
type allowProbeYAML struct {
	Permissions struct {
		Allow []string `yaml:"allow"`
	} `yaml:"permissions"`
}

// allowProbeJSON is the SUBSET of a Claude-Code settings.json this probe parses:
// just `{"permissions":{"allow":[...]}}`. It mirrors permconfig's claudeSettings
// shape (encoding/json); other keys are ignored.
type allowProbeJSON struct {
	Permissions struct {
		Allow []string `json:"allow"`
	} `json:"permissions"`
}

// HasProjectAuthority reports whether workspace carries a project authority set
// worth gating behind a trust prompt (see file doc). It is a method on Reader so
// it is reached through the same handle the fold holds; the receiver carries no
// state the probe needs (the IO seam is the real binding). It is FAIL-SAFE in the
// quiet direction: an unresolvable workspace or any IO/parse failure simply means
// "no authority found here" for that member, never an error — at worst the prompt
// is skipped, which only ever leaves a workspace UNTRUSTED (the safe default).
func (*Reader) HasProjectAuthority(workspace string) bool {
	return hasProjectAuthority(workspace, osAnchorIO)
}

// hasProjectAuthority is HasProjectAuthority with an injectable IO seam, for
// offline tests. It short-circuits on the first member found.
func hasProjectAuthority(workspace string, aio anchorIO) bool {
	if workspace == "" {
		return false
	}
	root, err := realpath(workspace)
	if err != nil {
		// Unresolvable workspace ⇒ treat as no authority (the prompt is skipped and
		// the run stays untrusted — fail-safe).
		return false
	}

	// 1. The project soul.
	if aio.statFile(filepath.Join(root, projectSoulRel)) {
		return true
	}

	// 2. Any project-tier agent/command/skill def under the anchor dirs. We only
	//    need PRESENCE of one regular file, so stop the walk at the first hit.
	for _, dir := range anchorDirs {
		dirAbs := filepath.Join(root, dir)
		found := false
		_ = aio.walk(dirAbs, func(string) error {
			found = true
			return errAuthorityFound // stop the walk on the first member
		})
		if found {
			return true
		}
	}

	// 3. A project mecatl settings.yaml/settings.local.yaml with a non-empty
	//    permissions.allow list (the project ALLOW rules the trust gate admits).
	for _, rel := range []string{projectSettingsRel, projectSettingsLocalRel} {
		if probeHasAllowYAML(filepath.Join(root, rel), aio) {
			return true
		}
	}

	// 4. A project Claude settings.json/settings.local.json with a non-empty
	//    permissions.allow list — the SAME files permconfig imports under
	//    ImportClaudePermissions (the mecatui default), so a repo whose only project
	//    allows live in .claude/settings.json still triggers the prompt.
	for _, rel := range []string{projectClaudeSettingsRel, projectClaudeSettingsLocalRel} {
		if probeHasAllowJSON(filepath.Join(root, rel), aio) {
			return true
		}
	}

	return false
}

// probeHasAllowYAML reports whether the mecatl settings file at path parses to a
// non-empty permissions.allow list. Any IO/parse failure ⇒ false (fail-safe).
func probeHasAllowYAML(path string, aio anchorIO) bool {
	data, ok := readSettingsForProbe(path, aio)
	if !ok {
		return false
	}
	var p allowProbeYAML
	if perr := yaml.Unmarshal(data, &p); perr != nil {
		return false
	}
	return hasNonEmptySpec(p.Permissions.Allow)
}

// probeHasAllowJSON reports whether the Claude settings.json at path parses to a
// non-empty permissions.allow list. Any IO/parse failure ⇒ false (fail-safe).
func probeHasAllowJSON(path string, aio anchorIO) bool {
	data, ok := readSettingsForProbe(path, aio)
	if !ok {
		return false
	}
	var p allowProbeJSON
	if perr := json.Unmarshal(data, &p); perr != nil {
		return false
	}
	return hasNonEmptySpec(p.Permissions.Allow)
}

// readSettingsForProbe stats+reads a (bounded) settings file for the allow probe.
// It returns (data, false) when the file is absent, unreadable, or blank.
func readSettingsForProbe(path string, aio anchorIO) ([]byte, bool) {
	if !aio.statFile(path) {
		return nil, false
	}
	data, err := aio.readFile(path, anchorMaxFileBytes)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil, false
	}
	return data, true
}

// hasNonEmptySpec reports whether allow contains at least one non-blank rule spec.
func hasNonEmptySpec(allow []string) bool {
	for _, a := range allow {
		if strings.TrimSpace(a) != "" {
			return true
		}
	}
	return false
}

// errAuthorityFound stops the anchor walk once the first member is seen. It is
// internal; the caller ignores it (it only needs the boolean side effect). It
// reuses fs.SkipAll like errAnchorTooMany.
var errAuthorityFound = errAnchorTooMany
