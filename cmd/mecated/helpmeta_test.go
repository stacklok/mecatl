package main

import (
	"testing"
)

// The REAL "every public flag has metadata + every metadata key names a real
// flag" invariant lives in cli_ux_test.go as TestFlagMetaCompletenessOverRealFlagSet,
// which drives the validateFlagMeta seam over the FULL parseFlagsModeOut FlagSet
// (not a synthetic subset). The server-boundary exclusion is itself verified
// end-to-end over the full FlagSet by TestAcpHelpAllExcludesServerBoundaryOverFullFlagSet.

// TestCommonFlagCountIsBounded verifies the common help stays at a reasonable
// size (~15-20 flags) so the task-oriented presentation is scannable.
func TestCommonFlagCountIsBounded(t *testing.T) {
	common := commonFlagNames(modeServe)
	count := len(common)
	if count < 12 {
		t.Errorf("serve common flags = %d, want at least 12 (too few common flags)", count)
	}
	if count > 25 {
		t.Errorf("serve common flags = %d, want at most 25 (common help is too large)", count)
	}
	// ACP common should have fewer flags (no server-boundary).
	acpCommon := commonFlagNames(modeACP)
	if len(acpCommon) >= count {
		t.Errorf("acp common flags (%d) should be fewer than serve common flags (%d)", len(acpCommon), count)
	}
}

// TestServeCommonHelpExcludesExpertFlags verifies expert/advanced flags are
// absent from the common help.
func TestServeCommonHelpExcludesExpertFlags(t *testing.T) {
	common := commonFlagNames(modeServe)
	expertExamples := []string{
		"llm-max-attempts",
		"otlp-endpoint",
		"scheduler-tick-interval",
		"driver-tls",
		"context-window-override",
		"child-retention",
		"subagent-ask-reviewer",
	}
	for _, name := range expertExamples {
		if common[name] {
			t.Errorf("expert flag %q should NOT be in common help", name)
		}
	}
}

// TestHelpAllFlagHasMetadata ensures --help-all itself has a metadata entry
// so it appears correctly in help renderers.
func TestHelpAllFlagHasMetadata(t *testing.T) {
	meta, ok := flagMetaByFlag["help-all"]
	if !ok {
		t.Error("--help-all flag has no metadata entry")
	}
	if meta.acp != acpInclude {
		t.Errorf("--help-all acp = %q, want include", meta.acp)
	}
}
