package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestLearnedSkillDetailSanitizesAndShowsLifecycleActions(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	skill := client.LearnedSkill{ID: "id", Name: "review\x1b[31m", OwnerAgent: "agent", State: "staged", Version: "v2", Revision: "r2", Supersedes: "v1", Body: "body\nforged", EvidenceCount: 2, Evaluations: []client.SkillEvaluation{{Verdict: "pass", FixtureIDs: []string{"f1"}}}, Receipts: []client.SkillChange{{Operation: "stage", FromState: "evaluated", ToState: "staged"}}}
	out := stripANSIstr(renderSkillsOverlay(th, skillsState{view: skillsDetail, detail: &skill}, client.Capabilities{LearnedSkills: true}, defaultHelpKeys(), 100, 40))
	for _, want := range []string{"owner: agent", "evaluation: pass", "history receipts: 1", "a activate", "r rollback"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail omitted %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[31m") {
		t.Fatalf("terminal control survived: %q", out)
	}
}

func TestLearnedSkillChangeReceiptSetsNonModalStatus(t *testing.T) {
	m := Model{deps: Deps{Theme: theme.New("aztec", theme.AztecPalette())}}
	updated, handled := m.updateSkillsMsg(client.SkillChangesMsg{Changes: []client.SkillChange{{ID: "receipt-1"}}})
	got := updated.(Model)
	if !handled || !strings.Contains(stripANSIstr(got.statusMsg), "learned-skill change receipt") || got.skills.view != skillsNone {
		t.Fatalf("handled=%v status=%q view=%v", handled, got.statusMsg, got.skills.view)
	}
}

func TestLearnedSkillDetailShowsStaleErrorNonModally(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	skill := client.LearnedSkill{Name: "x"}
	out := stripANSIstr(renderSkillsOverlay(th, skillsState{view: skillsDetail, detail: &skill, err: errors.New("revision conflict")}, client.Capabilities{}, defaultHelpKeys(), 80, 30))
	if !strings.Contains(out, "revision conflict") || !strings.Contains(out, "press esc, then enter to refresh") {
		t.Fatal(out)
	}
}
