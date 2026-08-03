package app

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
)

// TestPromptConfigCarriesNoOutputEconomyToneDelta proves the REMOVED output-economy
// "terse" tone delta (ADR 0041, superseded) does NOT come back via promptConfig:
// composition no longer carries an OutputEconomy field, so promptConfig leaves
// Tone empty and Build falls through to the always-on defaultTone — which already
// carries the investigation-depth / minimum-code / safety / read-before-edit /
// trust-boundary clauses. The "terse" answer-length clause must NOT appear in the
// built system prompt for any configuration.
func TestPromptConfigCarriesNoOutputEconomyToneDelta(t *testing.T) {
	pc := promptConfig(Config{Model: "m"}, "")
	if pc.Tone != "" {
		t.Errorf("promptConfig Tone must be empty (no output-economy override); got %q", pc.Tone)
	}
	built := prompt.Build(pc)
	if strings.Contains(built.StablePrefix, "answer in at most a few sentences") {
		t.Errorf("the removed terse answer-length clause must NOT appear in the built prompt\nprefix=%q", built.StablePrefix)
	}
	if strings.Contains(built.VolatileSuffix, "answer in at most a few sentences") {
		t.Errorf("the removed terse answer-length clause leaked into the volatile suffix\nsuffix=%q", built.VolatileSuffix)
	}

	// The surviving defaultTone safety/correctness guidance remains in the StablePrefix.
	for _, want := range []string{
		"It does NOT mean: read less",
		"Never cut these to hit a smaller line count",
		"Prefer Edit (emit only the change) over Write",
	} {
		if !strings.Contains(built.StablePrefix, want) {
			t.Errorf("defaultTone clause missing %q\nprefix=%q", want, built.StablePrefix)
		}
	}
}
