package permconfig

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

// residualSchedulesYAML is an old operator config carrying the removed
// `schedules:` block (issue #233 Phase 2b). The block used a strict per-element
// decoder, so one element carries a TYPO'd key ("cronn") — post-removal even
// that must parse cleanly (the whole subtree is gone; the lenient top-level
// decode ignores it, the same as any other removed YAML key).
const residualSchedulesYAML = `
schedules:
  - name: nightly-review
    cronn: "0 9 * * *"
    prompt: "Review open PRs"
    mutating: false
`

// TestScheduleTool_SettingsSchedulesBlockRemoved pins AC3.1 (schedule-tool
// acceptance plan): no `schedules:` key is honoured from any settings tier.
// The schema is deleted outright, so a residual `schedules:` block in an old
// config file is silently ignored by the lenient top-level decode — no hard
// failure, and no WARN machinery carried for a removed feature (the removal is
// CLEAN: the project-tier "IGNORING a project-tier schedules: block" WARN went
// away WITH the schema, so the diagnostics log carries no schedules line at
// all).
func TestScheduleTool_SettingsSchedulesBlockRemoved(t *testing.T) {
	// The removed strict decoder would REJECT the typo'd key above ("cronn") —
	// a parse that accepts it proves the SchedulesSection strict decode is gone,
	// not merely bypassed.
	if _, err := parseYAML([]byte(residualSchedulesYAML)); err != nil {
		t.Fatalf("a residual schedules: block must parse cleanly under the lenient top-level decode (schema removed); got %v", err)
	}

	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	// (1) OPERATOR tier (CLI explicit): the block is silently ignored — the
	// resolver never captures it and logs nothing about it.
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/schedules.yaml"}, Diagnostics: diag},
		envWithExplicit("/etc/mecatl/schedules.yaml", residualSchedulesYAML))
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file carrying a residual schedules: block")
	}
	if log := buf.String(); strings.Contains(log, "schedules") {
		t.Fatalf("operator-tier residual schedules: block must be silently ignored (no WARN machinery for a removed feature); got log:\n%s", log)
	}

	// (2) PROJECT tier: the old "IGNORING a project-tier schedules: block" WARN
	// is GONE — the whole tier-gate for the removed subtree went with the schema.
	// The project file parses and its other keys still resolve, but nothing in
	// the log mentions schedules.
	buf.Reset()
	rp := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, residualSchedulesYAML)
	_ = rp.Resolve(context.Background(), ws)
	if log := buf.String(); strings.Contains(log, "schedules") {
		t.Fatalf("project-tier residual schedules: block must be silently ignored (the ignore-WARN went away with the schema); got log:\n%s", log)
	}
}
