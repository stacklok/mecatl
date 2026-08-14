package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// These helpers drive the real production seam and project the metadata/body
// shapes needed by trust-gate and preload tests.

func seamForTest(t *testing.T, cfg Config) skillSeam {
	t.Helper()
	seam, err := resolveSkillSeam(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("resolveSkillSeam: %v", err)
	}
	if seam.close != nil {
		t.Cleanup(seam.close)
	}
	return seam
}

// resolveSkillsForTest returns the seam's discovered skills in the legacy
// []skills.Skill shape (name/description/body — assertions read names).
func resolveSkillsForTest(t *testing.T, cfg Config) []skills.Skill {
	t.Helper()
	seam := seamForTest(t, cfg)
	return skillValues(seam.metas, seam.index)
}

// resolveSkillIndexForTest returns the seam's name→body preload index.
func resolveSkillIndexForTest(t *testing.T, cfg Config) skillIndex {
	t.Helper()
	return seamForTest(t, cfg).index
}
