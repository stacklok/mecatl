package ui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"
)

func TestMecatuiCompactToolCards_Scenario2_ConfiguredRowsSurviveRendererRebuild(t *testing.T) {
	const configuredRows = 1
	m := newTestModelFromDeps(Deps{
		Theme:                   aztec(),
		ThemeAutoDetect:         true,
		CollapsedToolResultRows: configuredRows,
		Ctx:                     t.Context(),
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	assertBudget := func(name string, b *block) {
		t.Helper()
		rows := preparedRegionText(m.rend.prepareToolCard(b, false), blocks.RegionResult)
		if len(rows) != configuredRows+1 || !strings.Contains(rows[configuredRows], "expand") {
			t.Errorf("%s collapsed rows = %q, want %d row plus expansion marker", name, rows, configuredRows)
		}
	}
	assertBudget("text", &block{kind: blockTool, toolName: "Read", resolved: true, resultBody: "one\ntwo\nthree"})
	assertBudget("error", &block{kind: blockTool, toolName: "Shell", resolved: true, resultError: true, resultBody: "one\ntwo\nthree"})
	assertBudget("summarized JSON", &block{kind: blockTool, toolName: "Fetch", resolved: true, resultBody: `{"html_url":"https://example.com/item","id":7,"status":"open","state":"ready","padding":"` + strings.Repeat("x", 700) + `"}`})
	artifact := &block{kind: blockTool, toolName: "Fetch", resolved: true, resultBlocks: []client.ContentBlock{
		{Kind: client.ContentBlockResourceLink, Name: "artifact-one", URL: "https://example.com/one"},
		{Kind: client.ContentBlockImage, MimeType: "image/png"},
	}}
	assertBudget("typed artifact", artifact)

	expanded := strings.Join(preparedRegionText(m.rend.prepareToolCard(artifact, true), blocks.RegionResult), "\n")
	if !strings.Contains(expanded, "artifact-one") || !strings.Contains(expanded, "image/png") || strings.Contains(expanded, "expand") {
		t.Errorf("expanded typed result was changed by collapsed budget: %q", expanded)
	}

	mm, _ := m.Update(tea.BackgroundColorMsg{Color: color.White})
	m = mm.(Model)
	if m.rend.collapsedToolResultRows != configuredRows {
		t.Fatalf("theme rebuild reset collapsed rows to %d, want %d", m.rend.collapsedToolResultRows, configuredRows)
	}
	assertBudget("text after theme rebuild", &block{kind: blockTool, toolName: "Read", resolved: true, resultBody: "one\ntwo\nthree"})

	stored := newSessionsPanelState()
	stored.deps = surfaceDeps{theme: aztec(), marks: defaultHelpKeys()}
	stored.view = sessionsTranscript
	stored.loading = false
	stored.transcript.addTool("call-1", "Read", `{}`)
	stored.transcript.resolveTool("call-1", strings.Join([]string{
		"stored-row-01", "stored-row-02", "stored-row-03", "stored-row-04",
		"stored-row-05", "stored-row-06", "stored-row-07", "stored-row-08",
		"stored-row-09", "stored-row-10", "stored-row-11", "stored-row-12",
		"stored-row-13", "stored-row-14",
	}, "\n"), false)
	storedView, _ := stored.Render(80, 100)
	storedPlain := stripANSIstr(storedView)
	if got := strings.Count(storedPlain, "stored-row-"); got != maxToolResultLines {
		t.Errorf("stored transcript rendered %d result body rows, want legacy %d:\n%s", got, maxToolResultLines, storedPlain)
	}
	if !strings.Contains(storedPlain, "stored-row-12") || !strings.Contains(storedPlain, "expand") {
		t.Errorf("stored transcript did not render 12 body rows plus expansion marker:\n%s", storedPlain)
	}
	if strings.Contains(storedPlain, "stored-row-13") || strings.Contains(storedPlain, "stored-row-14") {
		t.Errorf("stored transcript rendered content beyond its legacy 12-row preview:\n%s", storedPlain)
	}

	diff := &block{kind: blockTool, toolName: "Write", toolArgs: `{"path":"out.txt","content":"one\ntwo\nthree"}`}
	if got := len(preparedRegionText(m.rend.prepareToolCard(diff, false), blocks.RegionArguments)); got <= configuredRows {
		t.Errorf("diff rows = %d, collapsed result setting changed the independent diff body", got)
	}
	if maxTraceEntries != 12 {
		t.Errorf("delegation trace cap = %d, want its independent limit", maxTraceEntries)
	}
	approval := stripANSIstr(renderApprovalModalWithRenderer(m.rend, pendingAsk{
		Tool: "Edit", Args: `{"path":"main.go","old_string":"one\ntwo","new_string":"three\nfour"}`, Reason: "review",
	}, false, 80, 24))
	for _, want := range []string{"- one", "- two", "+ three", "+ four"} {
		if !strings.Contains(approval, want) {
			t.Errorf("approval changed by collapsed result setting, missing %q: %q", want, approval)
		}
	}
}
