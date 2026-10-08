package agent

import (
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestInstructionWarningAggregationPerRun(t *testing.T) {
	prior := session.InstructionSnapshot{}
	snapshot := session.InstructionSnapshot{Scopes: []session.InstructionScope{
		{SourceID: "source", Directory: "one", File: "AGENTS.md", Partial: true},
		{SourceID: "source", Directory: "two", File: "AGENTS.md", Omitted: true},
		{SourceID: "source", Directory: "three", File: "AGENTS.md", Unavailable: true},
		{SourceID: "source", Directory: "four", File: "AGENTS.md", Unavailable: true},
	}}
	warnings := func(r *Run) []string {
		e := &Engine{}
		e.warnInstructionAssembly(r, prior, snapshot, false, false)
		var got []string
		for len(r.events) > 0 {
			got = append(got, (<-r.events).Text)
		}
		return got
	}

	r := &Run{events: make(chan session.Event, 8)}
	if got := warnings(r); len(got) != 2 || got[0] != instructionScopeWarning || got[1] != instructionErrorWarning {
		t.Fatalf("first-run warnings = %q, want one of each fixed reason", got)
	}
	if got := warnings(r); len(got) != 0 {
		t.Fatalf("same-run warnings repeated = %q", got)
	}
	if got := warnings(&Run{events: make(chan session.Event, 8)}); len(got) != 2 || got[0] != instructionScopeWarning || got[1] != instructionErrorWarning {
		t.Fatalf("next-run warnings = %q, want one of each fixed reason", got)
	}
}

func TestChildInstructionWarningProjectionAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event session.Event
		want  bool
	}{
		{"harness warning", session.Event{Type: session.EvHook, Text: instructionScopeWarning, Hook: &session.HookPayload{Phase: "ProjectInstructions", Decision: session.HookAdvisory}}, true},
		{"arbitrary hook", session.Event{Type: session.EvHook, Text: "private data", Hook: &session.HookPayload{Phase: "PostToolUse", Decision: session.HookAdvisory}}, false},
		{"forged phase", session.Event{Type: session.EvHook, Text: "private data", Hook: &session.HookPayload{Phase: "ProjectInstructions", Decision: session.HookAdvisory}}, false},
		{"tool attribution", session.Event{Type: session.EvHook, Text: instructionScopeWarning, Hook: &session.HookPayload{Phase: "ProjectInstructions", Decision: session.HookAdvisory, Tool: "Shell"}}, false},
		{"wrong decision", session.Event{Type: session.EvHook, Text: instructionScopeWarning, Hook: &session.HookPayload{Phase: "ProjectInstructions", Decision: session.HookInfo}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var forwarded []session.Event
			projectChildEvent(func(ev session.Event) { forwarded = append(forwarded, ev) }, tc.event, nil, "parent", "child", 0, session.Usage{})
			if (len(forwarded) == 1) != tc.want {
				t.Fatalf("forwarded=%v, want=%v", forwarded, tc.want)
			}
			if tc.want && (forwarded[0].Text != instructionScopeWarning || forwarded[0].Hook == nil || forwarded[0].Hook.Tool != "" || forwarded[0].Hook.CallID != "") {
				t.Fatalf("unsafe projection: %+v", forwarded[0])
			}
		})
	}
}
