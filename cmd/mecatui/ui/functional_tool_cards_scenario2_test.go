package ui

import (
	"strings"
	"testing"
	"unicode"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestMecatuiFunctionalConversationCards_Scenario2_ReadCardWrapsExactlyOnce(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(defaultBlockIndent + 30)
	_, outerWidth, bodyWidth := r.toolCardLayout()
	result := "read-result-" + strings.Repeat("x", bodyWidth+7) + "\nshort-read-row"
	presentation := toolCardPresentation{name: "Read", arguments: `{"path":"README.md"}`, resolved: true, result: result}

	prepared := r.prepareTypedToolCard(presentation, true)
	if len(prepared.Lines) != len(prepared.Rows) {
		t.Fatalf("prepared lines/rows = %d/%d, want lockstep", len(prepared.Lines), len(prepared.Rows))
	}
	out := strings.Join(prepared.Lines, "\n")
	if got := stripANSIstr(r.prepareTypedToolCard(presentation, true).render()); got != stripANSIstr(out) {
		t.Fatalf("main tool renderer did not use functional preparation\n got: %q\nwant: %q", got, stripANSIstr(out))
	}
	plainRows := strings.Split(stripANSIstr(out), "\n")
	shortAt := -1
	for i, row := range plainRows {
		if maxLineWidth(row) > outerWidth {
			t.Errorf("row %d width exceeds outer card width %d: %q", i, outerWidth, row)
		}
		if strings.Contains(row, "short-read-row") {
			shortAt = i
		}
	}
	if shortAt < 0 {
		t.Fatalf("short source row missing after wrapped Read result:\n%s", stripANSIstr(out))
	}
	for _, row := range plainRows[:shortAt] {
		if strings.TrimSpace(strings.Trim(row, "│╭╮╰╯─ ")) == "" && !strings.ContainsAny(row, "╭╰") {
			t.Errorf("Read result gained a padding-only row from outer-card reflow: %q", row)
		}
	}
}

func TestMecatuiFunctionalConversationCards_Scenario2_ToolVariantsPreserveWidthAndExpansion(t *testing.T) {
	long := strings.Repeat("unbreakable", 24)
	variants := []struct {
		name                        string
		card                        any
		collapsedWant, expandedWant string
	}{
		{"ordinary artifacts", toolCardPresentation{name: "WebFetch", arguments: `{"url":"https://example.test/` + long + `"}`, resolved: true, result: "result-" + long, artifacts: []client.ContentBlock{{Kind: client.ContentBlockResourceLink, Name: "artifact-" + long, URL: "https://example.test/" + long}}}, "result-", "artifact-"},
		{"edit diff", toolCardPresentation{name: "Edit", arguments: `{"path":"` + long + `","old_string":"old-` + long + `","new_string":"new-` + long + `"}`}, "Edit", "+ new-"},
		{"write diff", toolCardPresentation{name: "Write", arguments: `{"path":"` + long + `","content":"written-` + long + `"}`}, "Write", "+ written-"},
		{"subagent projection", subagentCardPresentation{name: "Subagent", goal: "goal-" + long, model: "model-" + long, current: "current-" + long}, "goal-", "model-"},
		{"team projection", teamCardPresentation{name: "Team", lanes: []teamLane{{name: "member-" + long, current: "current-" + long, lead: true}}}, "member-", "current-"},
		{"parallel projection", toolCardPresentation{name: "Parallel", arguments: `{"tasks":["task-` + long + `"]}`}, "Parallel", "task-"},
	}
	prepare := func(r *renderer, card any, expanded bool) preparedToolCard {
		switch p := card.(type) {
		case toolCardPresentation:
			return r.prepareTypedToolCard(p, expanded)
		case subagentCardPresentation:
			return r.prepareSubagentCard(p, expanded)
		case teamCardPresentation:
			return r.prepareTeamCard(p, expanded)
		default:
			panic("unknown card")
		}
	}
	for _, width := range []int{0, 1, defaultBlockIndent + 20, 100, 240} {
		for _, variant := range variants {
			t.Run(variant.name, func(t *testing.T) {
				r := newTestRenderer()
				r.setWidth(width)
				for _, expanded := range []bool{false, true} {
					prepared := prepare(r, variant.card, expanded)
					if len(prepared.Lines) != len(prepared.Rows) {
						t.Fatalf("lines/rows mismatch")
					}
					out := stripANSIstr(prepared.Text())
					want := variant.collapsedWant
					if expanded {
						want = variant.expandedWant
					}
					compact := strings.Map(func(r rune) rune {
						if unicode.IsSpace(r) {
							return -1
						}
						return r
					}, out)
					compactWant := strings.Map(func(r rune) rune {
						if unicode.IsSpace(r) {
							return -1
						}
						return r
					}, want)
					if !strings.Contains(compact, compactWant) {
						t.Errorf("output lost %q:\n%s", want, out)
					}
				}
			})
		}
	}
}
