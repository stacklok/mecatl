package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type rollbackLifecycleClient struct {
	response client.LearnedSkill
	calls    int
}

func (*rollbackLifecycleClient) ListLearnedSkills(context.Context, string) ([]client.LearnedSkill, error) {
	return nil, nil
}
func (*rollbackLifecycleClient) GetLearnedSkill(context.Context, string, string, string, string) (client.LearnedSkill, error) {
	return client.LearnedSkill{}, nil
}
func (*rollbackLifecycleClient) MutateLearnedSkill(context.Context, string, client.LearnedSkill) (client.LearnedSkill, error) {
	return client.LearnedSkill{}, nil
}
func (f *rollbackLifecycleClient) RollbackLearnedSkill(_ context.Context, source client.LearnedSkill) (client.LearnedSkill, error) {
	f.calls++
	if source.Version != "v2" || source.Revision != "r2" || source.Supersedes != "v1" {
		return client.LearnedSkill{}, errors.New("unexpected rollback source")
	}
	return f.response, nil
}
func (*rollbackLifecycleClient) ListSkillChanges(context.Context, string) ([]client.SkillChange, error) {
	return nil, nil
}
func (*rollbackLifecycleClient) DiffLearnedSkill(context.Context, client.LearnedSkill) (string, error) {
	return "", nil
}
func (*rollbackLifecycleClient) ListSkills(context.Context) ([]client.Skill, error) { return nil, nil }

func TestLearnedSkillRollbackAcceptsTargetVersionExactlyOnce(t *testing.T) {
	active := client.LearnedSkill{Project: "/project", ID: "skill-1", Name: "review", OwnerAgent: "agent", Version: "v2", Revision: "r2", State: "active", Supersedes: "v1", Generation: 7}
	target := client.LearnedSkill{Project: "/project", ID: "skill-1", Name: "review", OwnerAgent: "agent", Version: "v1", Revision: "r3", State: "active", Generation: 8, PublicationStatus: "published"}
	lifecycle := &rollbackLifecycleClient{response: target}
	m := Model{deps: Deps{Skills: lifecycle, Theme: theme.New("aztec", theme.AztecPalette())}, skills: skillsState{
		view: skillsDetail, detail: &active, learned: []client.LearnedSkill{active}, project: "/project",
		generations: map[string]uint64{"/project": 7}, requestID: 41,
	}}

	msg := client.RollbackLearnedSkillCmd(context.Background(), lifecycle, active, 41)()
	unrelated := msg.(client.LearnedSkillMsg)
	unrelated.Skill = &client.LearnedSkill{Project: "/project", ID: "skill-1", OwnerAgent: "agent", Version: "v9", Revision: "bad"}
	unrelated.PublicationError = "conflict"
	updated, handled := m.updateSkillsMsg(unrelated)
	unchanged := updated.(Model)
	if !handled || unchanged.skills.detail.Version != "v2" || unchanged.skills.err != nil || unchanged.skills.generations["/project"] != 7 {
		t.Fatalf("unrelated rollback response changed selected source: detail=%#v err=%v generations=%v", unchanged.skills.detail, unchanged.skills.err, unchanged.skills.generations)
	}

	updated, handled = m.updateSkillsMsg(msg)
	got := updated.(Model)
	if !handled || lifecycle.calls != 1 || got.skills.detail == nil || got.skills.detail.Version != "v1" || got.skills.detail.Revision != "r3" || got.skills.generations["/project"] != 8 || got.skills.detail.PublicationStatus != "published" {
		t.Fatalf("rollback not committed: handled=%v calls=%d detail=%#v generations=%v", handled, lifecycle.calls, got.skills.detail, got.skills.generations)
	}
	if len(got.skills.learned) != 1 || got.skills.learned[0].Version != "v1" {
		t.Fatalf("inventory did not move to rollback target: %#v", got.skills.learned)
	}
	if view := stripANSIstr(renderSkillsOverlay(got.deps.Theme, got.skills, client.Capabilities{LearnedSkills: true}, defaultHelpKeys(), 100, 40)); !strings.Contains(view, "version: v1") {
		t.Fatalf("rollback target not shown in detail:\n%s", view)
	}

	// The committed response is no longer valid once the detail moved off its v2/r2
	// source. A replay must not alter state or surface a conflict over the successful
	// rollback.
	stale := msg.(client.LearnedSkillMsg)
	stale.PublicationError = "conflict"
	replayed, _ := got.updateSkillsMsg(stale)
	got = replayed.(Model)
	if got.skills.detail.Version != "v1" || got.skills.detail.Revision != "r3" || got.skills.err != nil || got.skills.generations["/project"] != 8 {
		t.Fatalf("stale rollback response changed committed state: detail=%#v err=%v generations=%v", got.skills.detail, got.skills.err, got.skills.generations)
	}
}

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

func TestLearnedSkillDetailLabelsActivationAssurance(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for operation, want := range map[string]string{"activate_validated": "state: active(validated)", "activate": "state: active(evaluated)"} {
		skill := client.LearnedSkill{Name: "learned", State: "active", Receipts: []client.SkillChange{{Operation: operation, ToState: "active"}}}
		out := stripANSIstr(renderLearnedSkillDetail(th, skill, ""))
		if !strings.Contains(out, want) {
			t.Fatalf("operation %s missing %q:\n%s", operation, want, out)
		}
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
