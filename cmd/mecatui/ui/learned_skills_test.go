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

func TestLearnedSkillResponsesUsePartitionEpochAndRowVersion(t *testing.T) {
	m := Model{skills: skillsState{view: skillsPanel, project: "/project", requestID: 9}}
	global := client.LearnedSkill{ID: "global", Version: "v1", Revision: "r1", Project: "", Generation: 4}
	project := client.LearnedSkill{ID: "project", Version: "v2", Revision: "r2", Project: "/project", Generation: 5}
	updated, handled := m.updateSkillsMsg(client.LearnedSkillsMsg{
		Skills: []client.LearnedSkill{global, project}, Project: "/project",
		Generations: map[string]uint64{"": 4, "/project": 5}, RequestID: 9,
	})
	got := updated.(Model)
	if !handled || got.skills.generations[""] != 4 || got.skills.generations["/project"] != 5 {
		t.Fatalf("partition generations not retained: %#v", got.skills.generations)
	}

	// Opening the global row at generation 4 remains valid even though the project
	// partition is already at 5. A single max epoch incorrectly dropped this.
	got.skills.requestID = 10
	updated, handled = got.updateSkillsMsg(client.LearnedSkillMsg{Skill: &global, Project: "", Generation: 4, SelectedSkillID: "global", SelectedVersion: "v1", RequestID: 10})
	got = updated.(Model)
	if !handled || got.skills.detail == nil || got.skills.detail.ID != "global" {
		t.Fatalf("valid lower-generation global row did not open: %#v", got.skills.detail)
	}

	mutated := global
	mutated.Revision, mutated.Generation = "r3", 5
	got.skills.requestID = 11
	updated, _ = got.updateSkillsMsg(client.LearnedSkillMsg{Skill: &mutated, Project: "", Generation: 5, SelectedSkillID: "global", SelectedVersion: "v1", RequestID: 11})
	got = updated.(Model)
	if got.skills.detail.Revision != "r3" || got.skills.generations[""] != 5 || got.skills.generations["/project"] != 5 {
		t.Fatalf("partition mutation not accepted independently: detail=%#v generations=%v", got.skills.detail, got.skills.generations)
	}

	stale := global
	stale.Revision = "stale"
	updated, _ = got.updateSkillsMsg(client.LearnedSkillMsg{Skill: &stale, Project: "", Generation: 4, SelectedSkillID: "global", SelectedVersion: "v1", RequestID: 11})
	got = updated.(Model)
	if got.skills.detail.Revision != "r3" {
		t.Fatalf("stale partition response replaced detail: %#v", got.skills.detail)
	}

	wrongRow := mutated
	wrongRow.ID = "other"
	updated, _ = got.updateSkillsMsg(client.LearnedSkillMsg{Skill: &wrongRow, Project: "", Generation: 6, SelectedSkillID: "global", SelectedVersion: "v1", RequestID: 11})
	if updated.(Model).skills.detail.Revision != "r3" {
		t.Fatal("mismatched response row replaced selected detail")
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
