package ui

import (
	"strings"
	"testing"
)

func TestQuieterToolcallsResidualHintsRouteToTheirAvailableViews(t *testing.T) {
	r := newTestRenderer()
	longResult := strings.Repeat("result\n", maxToolResultLines+1)

	for name, got := range map[string]string{
		"truncated result": r.renderTypedToolResult(longResult, false, nil, 0),
		"truncated args": func() string {
			out, ok := r.summarizeArgs(mustJSON(t, map[string]any{"body": longBody()}))
			if !ok {
				t.Fatal("long arguments should summarize")
			}
			return out
		}(),
	} {
		got = stripANSIstr(got)
		if !strings.Contains(got, "ctrl+t inspect") {
			t.Errorf("%s hint = %q, want Toolcalls inspection", name, got)
		}
		if strings.Contains(got, "expand") {
			t.Errorf("%s advertises retired expansion: %q", name, got)
		}
	}

	subagent := stripANSIstr(r.renderSubagentPresentation(subagentCardPresentation{current: "Grep", toolCount: 1}, 0))
	team := stripANSIstr(r.renderTeamPresentation(teamCardPresentation{lanes: []teamLane{{name: "lead", lead: true}}}, 0))
	for name, got := range map[string]string{"subagent": subagent, "team": team} {
		if !strings.Contains(got, "f6 agents") {
			t.Errorf("%s trace hint = %q, want Agents chord", name, got)
		}
		if strings.Contains(got, "ctrl+t trace") {
			t.Errorf("%s advertises retired trace expansion: %q", name, got)
		}
	}

	preview := stripANSIstr(renderResourcePreview(aztec(), mcpState{preview: longResult}, defaultHelpKeys()))
	if !strings.Contains(preview, "+1 more line") {
		t.Errorf("truncated resource preview = %q, want line count", preview)
	}
	if strings.Contains(preview, "ctrl+t") || strings.Contains(preview, "expand") {
		t.Errorf("resource preview advertises unavailable expansion: %q", preview)
	}
}
