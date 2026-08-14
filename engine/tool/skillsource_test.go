package tool

import "testing"

// TestValidSkillAssetName pins the ONE shared logical-name validator: a valid
// name is non-empty, slash-separated, relative, with no empty/"."/".."
// segments, no backslash, and no NUL — every SkillSource implementation and
// consumer (FS source, Skill tool, driver server wrapper) shares exactly this
// grammar, so a name that escapes a skill's namespace is rejected everywhere.
func TestValidSkillAssetName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"single segment", "SKILL_NOTES.md", true},
		{"two segments", "references/api.md", true},
		{"script", "scripts/run.sh", true},
		{"deep multi-segment", "references/deep/nested/notes.md", true},
		{"dot inside a segment", "a.b/c.d", true},
		{"leading-dot file", ".env.example", true},

		{"empty", "", false},
		{"dot-dot traversal", "../x", false},
		{"embedded dot-dot", "a/../b", false},
		{"trailing dot-dot", "a/..", false},
		{"absolute (leading separator)", "/abs", false},
		{"backslash", "a\\b", false},
		{"current-dir segment", "a/./b", false},
		{"lone dot", ".", false},
		{"lone dot-dot", "..", false},
		{"trailing slash (empty segment)", "a/", false},
		{"double slash (empty segment)", "a//b", false},
		{"NUL byte", "a\x00b", false},
		{"newline", "a\nb", false},
		{"carriage return", "a\rb", false},
		{"ASCII control", "a\tb", false},
		{"Unicode control NEL", "a\u0085b", false},
		{"Unicode line separator", "a\u2028b", false},
		{"Unicode paragraph separator", "a\u2029b", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidSkillAssetName(c.in); got != c.want {
				t.Errorf("ValidSkillAssetName(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
