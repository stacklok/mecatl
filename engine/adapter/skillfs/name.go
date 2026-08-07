package skillfs

import "regexp"

// nameRE is the single shared skill activation-name grammar:
// ^[a-z0-9][a-z0-9_-]{0,63}$ — lowercase letters/digits, underscore, hyphen;
// 1-64 chars; must start with a lowercase letter or digit.
//
// This is deliberately the LAXER grammar, NOT the agentskills.io strict form
// (which is hyphens-only). The underscore is kept so existing drafted and
// discovered skills continue to validate. It blocks whitespace, control
// characters, uppercase, and path-traversal name attacks (".."/"."/"/"). The
// draft write path (internal/adapter/skills/drafter.go) already used this exact
// regex; discovery now shares it via ValidSkillName so read and write paths
// enforce the ONE grammar.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidSkillName reports whether name is a valid skill activation name under
// the shared grammar: lowercase letters/digits/underscore/hyphen, 1-64 chars,
// starting with a lowercase letter or digit. It mirrors the
// tool.ValidSkillAssetName idiom — the ONE validator every skill consumer
// (discovery, draft, promote) shares.
//
// Exported so the writable SkillDraft half (internal/adapter/skills, via the
// alias) routes its name validation through the SAME grammar the read-only
// discovery core uses — a single source of truth, not two regexes to drift.
func ValidSkillName(name string) bool {
	return nameRE.MatchString(name)
}
