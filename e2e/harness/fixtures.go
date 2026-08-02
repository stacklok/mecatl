//go:build e2e

package harness

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFixtures lays the checked-in templates (e2e/fixtures/) out into the
// ephemeral root the local mecated is spawned against:
//
//	fixtures/skills/greet/SKILL.md     → <root>/home/.claude/skills/greet/SKILL.md       (user-global lane)
//	fixtures/skills/repo-fact/SKILL.md → <root>/workspace/.claude/skills/repo-fact/...   (workspace lane)
//	fixtures/agents/fruit-reader.md    → <root>/workspace/.claude/agents/fruit-reader.md (workspace lane)
//	fixtures/soul.md                   → <root>/soul/soul.md                             (--soul-file)
//	fixtures/permissions.yaml          → <root>/permissions.yaml                         (--permission-config)
//	fixtures/workspace/*               → <root>/workspace/                               (repo content)
//
// Fixture content is harness-authored and deterministic; the specs assert on
// EVENTS and side effects, never on model prose about the fixtures.
func WriteFixtures(repoRoot, root string) error {
	fx := filepath.Join(repoRoot, "e2e", "fixtures")
	copies := []struct{ src, dst string }{
		{filepath.Join(fx, "skills", "greet", "SKILL.md"), filepath.Join(root, "home", ".claude", "skills", "greet", "SKILL.md")},
		{filepath.Join(fx, "skills", "repo-fact", "SKILL.md"), filepath.Join(root, "workspace", ".claude", "skills", "repo-fact", "SKILL.md")},
		{filepath.Join(fx, "agents", "fruit-reader.md"), filepath.Join(root, "workspace", ".claude", "agents", "fruit-reader.md")},
		{filepath.Join(fx, "soul.md"), filepath.Join(root, "soul", "soul.md")},
		{filepath.Join(fx, "permissions.yaml"), filepath.Join(root, "permissions.yaml")},
		{filepath.Join(fx, "workspace", "README.md"), filepath.Join(root, "workspace", "README.md")},
		{filepath.Join(fx, "workspace", "FRUIT.txt"), filepath.Join(root, "workspace", "FRUIT.txt")},
		// Sized input the compaction scenario Reads to grow conversation history
		// deterministically (~2250 conversation-tokens per Read).
		{filepath.Join(fx, "workspace", "compaction-input.txt"), filepath.Join(root, "workspace", "compaction-input.txt")},
	}
	for _, c := range copies {
		if err := copyFile(c.src, c.dst); err != nil {
			return fmt.Errorf("fixture %s: %w", c.src, err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
