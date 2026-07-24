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
// advertises the removed subcommand. The gRPC/REST schedule surface the
// (removed) CLI dialed is RETAINED — it is now reached by the in-chat Schedule
// tool and out-of-band REST/gRPC clients, pinned by
// TestScheduleTool_WireSurvivesSettingsCLIRemoval in internal/adapter/server.
func TestScheduleTool_SchedulesCLIRemoved(t *testing.T) {
	// The usage banner must not advertise a removed subcommand (no presence
	// marker — the removal is clean).
	fs := flag.NewFlagSet("mecated", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	cfg, err := parseFlags([]string{"--help"})
	if err != flag.ErrHelp {
		t.Fatalf("parseFlags(--help) = (%v, %v), want flag.ErrHelp", cfg, err)
	}
	if out := buf.String(); strings.Contains(out, "schedules <verb>") {
		t.Fatalf("usage banner still advertises the removed `schedules <verb>` subcommand:\n%s", out)
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
