package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// TestScheduleTool_SchedulesCLIRemoved pins AC3.2 (schedule-tool acceptance
// plan): the `mecated schedules <verb>` subcommand group is GONE. There is no
// dispatch and no HTTP client left in the binary, so a bare `mecated schedules`
// falls through to normal daemon startup (the deleted dispatch no longer
// intercepts os.Args[1]=="schedules"), and the usage banner no longer
// advertises the removed subcommand. This test wires the REAL writeTopLevelHelp
// helper so a change that re-adds the subcommand to the banner fails here.
func TestScheduleTool_SchedulesCLIRemoved(t *testing.T) {
	// The usage banner must not advertise a removed subcommand (no presence
	// marker — the removal is clean). Assert against the REAL production help
	// renderer (writeTopLevelHelp) — the same one parseFlags' Usage hook invokes
	// — not a vacuous string check against an output that never named "schedules".
	var buf bytes.Buffer
	writeTopLevelHelp(&buf)
	out := buf.String()
	if strings.Contains(out, "schedules") {
		t.Fatalf("top-level help still advertises a `schedules` subcommand:\n%s", out)
	}
	// Sanity: the real renderer DID render the known commands, so the absence
	// check above is not vacuously true on an empty output.
	for _, want := range []string{"serve", "acp", "import", "config init", "skills promote", "perf-mcp print-config"} {
		if !strings.Contains(out, want) {
			t.Fatalf("top-level help (real renderer) missing %q — the absence check is vacuous:\n%s", want, out)
		}
	}
	// And the help path through parseFlags still resolves to flag.ErrHelp.
	if _, err := parseFlags([]string{"--help"}); err != flag.ErrHelp {
		t.Fatalf("parseFlags(--help) err = %v, want flag.ErrHelp", err)
	}
}

// TestScheduleTool_SchedulerFlagRemoved pins AC2.5 (schedule-tool acceptance
// plan): the old `--scheduler` opt-in flag is DELETED outright — no deprecated
// alias, no no-op shim. With the scheduler now ON by default on any
// schedule-capable store (AC2.1), the opt-in is gone: passing it fails fast as
// an unknown flag at flag-parse (the standard ContinueOnError error, before any
// listener binds). The disable knob is `--no-scheduler` (AC2.2); the migration
// note lives in ADR 0073 + the docs, not in kept code.
func TestScheduleTool_SchedulerFlagRemoved(t *testing.T) {
	for _, argv := range [][]string{{"--scheduler"}, {"--scheduler=true"}} {
		_, err := parseFlags(argv)
		if err == nil {
			t.Fatalf("parseFlags(%v) = nil error, want the unknown-flag startup error (the opt-in flag is DELETED, not deprecated)", argv)
		}
		if !strings.Contains(err.Error(), "flag provided but not defined: -scheduler") {
			t.Fatalf("parseFlags(%v) error = %v, want the standard unknown-flag error for -scheduler", argv, err)
		}
	}
}
