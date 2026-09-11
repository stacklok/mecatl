package workspacetrust

import (
	"testing"
)

// TestHasProjectAuthorityEmptyWorkspaceFalse asserts a bare/empty workspace
// carries no authority (no prompt, fail-safe quiet).
func TestHasProjectAuthorityEmptyWorkspaceFalse(t *testing.T) {
	r := New()
	if r.HasProjectAuthority("") {
		t.Fatal("HasProjectAuthority(\"\") = true, want false")
	}
	ws := realDir(t, t.TempDir(), "empty-repo")
	if r.HasProjectAuthority(ws) {
		t.Fatal("HasProjectAuthority on an empty repo = true, want false (nothing to gate)")
	}
}

// TestHasProjectAuthoritySoul asserts a project soul is authority worth gating.
func TestHasProjectAuthoritySoul(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/soul.md", "project persona")
	if !New().HasProjectAuthority(ws) {
		t.Fatal("project soul not detected as authority")
	}
}

// TestHasProjectAuthorityAgent asserts a project-tier agent def is authority.
func TestHasProjectAuthorityAgent(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".claude/agents/reviewer.md", "agent body")
	if !New().HasProjectAuthority(ws) {
		t.Fatal("project agent def not detected as authority")
	}
}

// TestHasProjectAuthorityCommand asserts a project-tier command def is authority.
func TestHasProjectAuthorityCommand(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/commands/deploy.md", "command body")
	if !New().HasProjectAuthority(ws) {
		t.Fatal("project command def not detected as authority")
	}
}

// TestHasProjectAuthoritySkill asserts a project-tier skill def is authority.
func TestHasProjectAuthoritySkill(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/skills/x/SKILL.md", "skill body")
	if !New().HasProjectAuthority(ws) {
		t.Fatal("project skill def not detected as authority")
	}
}

// TestHasProjectAuthorityAllowRule asserts a project settings.yaml with a non-empty
// permissions.allow list is authority (the ALLOW rules the trust gate admits).
func TestHasProjectAuthorityAllowRule(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/settings.yaml", "permissions:\n  allow:\n    - Read\n")
	if !New().HasProjectAuthority(ws) {
		t.Fatal("project settings.yaml allow rule not detected as authority")
	}
}

// TestHasProjectAuthorityAllowLocalFile asserts the local settings file also counts.
func TestHasProjectAuthorityAllowLocalFile(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/settings.local.yaml", "permissions:\n  allow:\n    - Read\n")
	if !New().HasProjectAuthority(ws) {
		t.Fatal("project settings.local.yaml allow rule not detected as authority")
	}
}

// TestHasProjectAuthorityDenyOnlyNotAuthority asserts a project settings.yaml with
// only DENY/ASK rules (no allow) is NOT authority worth prompting on — deny/ask are
// honoured trusted or not, so a trust grant would change nothing.
func TestHasProjectAuthorityDenyOnlyNotAuthority(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/settings.yaml", "permissions:\n  deny:\n    - Shell\n  ask:\n    - Write\n")
	if New().HasProjectAuthority(ws) {
		t.Fatal("a deny/ask-only project settings.yaml was treated as authority; only ALLOW rules are")
	}
}

// TestHasProjectAuthorityEmptyAllowNotAuthority asserts an empty/blank allow list is
// not authority.
func TestHasProjectAuthorityEmptyAllowNotAuthority(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".mecatl/settings.yaml", "permissions:\n  allow: []\n")
	if New().HasProjectAuthority(ws) {
		t.Fatal("an empty allow list was treated as authority")
	}
}

// TestHasProjectAuthorityClaudeAllowRule asserts a Claude .claude/settings.json with
// a non-empty permissions.allow list is authority — the SAME file permconfig imports
// under ImportClaudePermissions (the mecatui default). The .claude omission was the
// trigger-surface false-negative this fix closes.
func TestHasProjectAuthorityClaudeAllowRule(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".claude/settings.json", `{"permissions":{"allow":["Read"]}}`)
	if !New().HasProjectAuthority(ws) {
		t.Fatal("project .claude/settings.json allow rule not detected as authority")
	}
}

// TestHasProjectAuthorityClaudeAllowLocalFile asserts the Claude LOCAL settings file
// also counts.
func TestHasProjectAuthorityClaudeAllowLocalFile(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".claude/settings.local.json", `{"permissions":{"allow":["Read"]}}`)
	if !New().HasProjectAuthority(ws) {
		t.Fatal("project .claude/settings.local.json allow rule not detected as authority")
	}
}

// TestHasProjectAuthorityClaudeEmptyAllowNotAuthority asserts a Claude settings.json
// with an empty allow list (and only deny/ask) is NOT authority.
func TestHasProjectAuthorityClaudeEmptyAllowNotAuthority(t *testing.T) {
	ws := realDir(t, t.TempDir(), "repo")
	writeFile(t, ws, ".claude/settings.json", `{"permissions":{"allow":[],"deny":["Shell"]}}`)
	if New().HasProjectAuthority(ws) {
		t.Fatal("a Claude settings.json empty allow list was treated as authority")
	}
}
