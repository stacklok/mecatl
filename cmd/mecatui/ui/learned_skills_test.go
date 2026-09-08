package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
	m := Model{deps: Deps{Skills: lifecycle, Theme: theme.New("aztec", theme.AztecPalette())}, modal: &skillsState{
		view: skillsDetail, detail: &active, learned: []client.LearnedSkill{active}, project: "/project",
		generations: map[string]uint64{"/project": 7}, requestID: 41,
	}}
	st := m.modal.(*skillsState)

	msg := client.RollbackLearnedSkillCmd(context.Background(), lifecycle, active, 41)()
	unrelated := msg.(client.LearnedSkillMsg)
	unrelated.Skill = &client.LearnedSkill{Project: "/project", ID: "skill-1", OwnerAgent: "agent", Version: "v9", Revision: "bad"}
	unrelated.PublicationError = "conflict"
	_, handled, _ := st.HandleMsg(unrelated)
	if !handled || st.detail.Version != "v2" || st.err != nil || st.generations["/project"] != 7 {
		t.Fatalf("unrelated rollback response changed selected source: detail=%#v err=%v generations=%v", st.detail, st.err, st.generations)
	}

	_, handled, _ = st.HandleMsg(msg)
	if !handled || lifecycle.calls != 1 || st.detail == nil || st.detail.Version != "v1" || st.detail.Revision != "r3" || st.generations["/project"] != 8 || st.detail.PublicationStatus != "published" {
		t.Fatalf("rollback not committed: handled=%v calls=%d detail=%#v generations=%v", handled, lifecycle.calls, st.detail, st.generations)
	}
	if len(st.learned) != 1 || st.learned[0].Version != "v1" {
		t.Fatalf("inventory did not move to rollback target: %#v", st.learned)
	}
	body, _ := st.Render(100, 40)
	if view := stripANSIstr(body); !strings.Contains(view, "version: v1") {
		t.Fatalf("rollback target not shown in detail:\n%s", view)
	}

	// The committed response is no longer valid once the detail moved off its v2/r2
	// source. A replay must not alter state or surface a conflict over the successful
	// rollback.
	stale := msg.(client.LearnedSkillMsg)
	stale.PublicationError = "conflict"
	_, _, _ = st.HandleMsg(stale)
	if st.detail.Version != "v1" || st.detail.Revision != "r3" || st.err != nil || st.generations["/project"] != 8 {
		t.Fatalf("stale rollback response changed committed state: detail=%#v err=%v generations=%v", st.detail, st.err, st.generations)
	}
}

func TestLearnedSkillDetailSanitizesAndShowsLifecycleActions(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	skill := client.LearnedSkill{ID: "id", Name: "review\x1b[31m", OwnerAgent: "agent", State: "staged", Version: "v2", Revision: "r2", Supersedes: "v1", Body: "body\nforged", EvidenceCount: 2, Evaluations: []client.SkillEvaluation{{Verdict: "pass", FixtureIDs: []string{"f1"}}}, Receipts: []client.SkillChange{{Operation: "stage", FromState: "evaluated", ToState: "staged"}}}
	body, _ := (&skillsState{view: skillsDetail, detail: &skill, deps: surfaceDeps{theme: th, caps: client.Capabilities{LearnedSkills: true}, marks: defaultHelpKeys()}}).Render(100, 40)
	out := stripANSIstr(body)
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
		out := stripANSIstr(renderLearnedSkillDetail(th, skill, "", 100))
		if !strings.Contains(out, want) {
			t.Fatalf("operation %s missing %q:\n%s", operation, want, out)
		}
	}
}

func TestLearnedSkillChangeReceiptSetsNonModalStatus(t *testing.T) {
	m := Model{deps: Deps{Theme: theme.New("aztec", theme.AztecPalette())}}
	updated, handled := m.updateSkillChangesMsg(client.SkillChangesMsg{Changes: []client.SkillChange{{ID: "receipt-1"}}})
	got := updated.(Model)
	if !handled || !strings.Contains(stripANSIstr(got.statusMsg), "learned-skill change receipt") || got.modal != nil {
		t.Fatalf("handled=%v status=%q modal=%v", handled, got.statusMsg, got.modal)
	}
}

func TestLearnedSkillResponsesUsePartitionEpochAndRowVersion(t *testing.T) {
	m := Model{modal: &skillsState{view: skillsPanel, project: "/project", requestID: 9}}
	global := client.LearnedSkill{ID: "global", Version: "v1", Revision: "r1", Project: "", Generation: 4}
	project := client.LearnedSkill{ID: "project", Version: "v2", Revision: "r2", Project: "/project", Generation: 5}
	st := m.modal.(*skillsState)
	_, handled, _ := st.HandleMsg(client.LearnedSkillsMsg{
		Skills: []client.LearnedSkill{global, project}, Project: "/project",
		Generations: map[string]uint64{"": 4, "/project": 5}, RequestID: 9,
	})
	if !handled || st.generations[""] != 4 || st.generations["/project"] != 5 {
		t.Fatalf("partition generations not retained: %#v", st.generations)
	}

	// Opening the global row at generation 4 remains valid even though the project
	// partition is already at 5. A single max epoch incorrectly dropped this.
	st.requestID = 10
	_, handled, _ = st.HandleMsg(client.LearnedSkillMsg{Skill: &global, Project: "", Generation: 4, SelectedSkillID: "global", SelectedVersion: "v1", RequestID: 10})
	if !handled || st.detail == nil || st.detail.ID != "global" {
		t.Fatalf("valid lower-generation global row did not open: %#v", st.detail)
	}

	mutated := global
	mutated.Revision, mutated.Generation = "r3", 5
	st.requestID = 11
	_, _, _ = st.HandleMsg(client.LearnedSkillMsg{Skill: &mutated, Project: "", Generation: 5, SelectedSkillID: "global", SelectedVersion: "v1", RequestID: 11})
	if st.detail.Revision != "r3" || st.generations[""] != 5 || st.generations["/project"] != 5 {
		t.Fatalf("partition mutation not accepted independently: detail=%#v generations=%v", st.detail, st.generations)
	}

	stale := global
	stale.Revision = "stale"
	_, _, _ = st.HandleMsg(client.LearnedSkillMsg{Skill: &stale, Project: "", Generation: 4, SelectedSkillID: "global", SelectedVersion: "v1", RequestID: 11})
	if st.detail.Revision != "r3" {
		t.Fatalf("stale partition response replaced detail: %#v", st.detail)
	}

	wrongRow := mutated
	wrongRow.ID = "other"
	_, _, _ = st.HandleMsg(client.LearnedSkillMsg{Skill: &wrongRow, Project: "", Generation: 6, SelectedSkillID: "global", SelectedVersion: "v1", RequestID: 11})
	if st.detail.Revision != "r3" {
		t.Fatal("mismatched response row replaced selected detail")
	}
}

func TestLearnedSkillDetailShowsStaleErrorNonModally(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	skill := client.LearnedSkill{Name: "x"}
	body, _ := (&skillsState{view: skillsDetail, detail: &skill, err: errors.New("revision conflict"), deps: surfaceDeps{theme: th, marks: defaultHelpKeys()}}).Render(80, 30)
	out := stripANSIstr(body)
	if !strings.Contains(out, "revision conflict") || !strings.Contains(out, "press esc, then enter to refresh") {
		t.Fatal(out)
	}
}

// getLifecycleClient counts the Get call so the panel's enter→GetLearnedSkill
// arm can assert the RPC fired.
type getLifecycleClient struct {
	calls int
}

func (*getLifecycleClient) ListLearnedSkills(context.Context, string) ([]client.LearnedSkill, error) {
	return nil, nil
}
func (f *getLifecycleClient) GetLearnedSkill(_ context.Context, _, _, _, _ string) (client.LearnedSkill, error) {
	f.calls++
	return client.LearnedSkill{}, nil
}
func (*getLifecycleClient) MutateLearnedSkill(context.Context, string, client.LearnedSkill) (client.LearnedSkill, error) {
	return client.LearnedSkill{}, nil
}
func (*getLifecycleClient) RollbackLearnedSkill(context.Context, client.LearnedSkill) (client.LearnedSkill, error) {
	return client.LearnedSkill{}, nil
}
func (*getLifecycleClient) ListSkillChanges(context.Context, string) ([]client.SkillChange, error) {
	return nil, nil
}
func (*getLifecycleClient) DiffLearnedSkill(context.Context, client.LearnedSkill) (string, error) {
	return "", nil
}
func (*getLifecycleClient) ListSkills(context.Context) ([]client.Skill, error) { return nil, nil }

// TestSkillsEnterGetLearnedSkill drives the panel's enter arm (skills.go
// HandleKey): with a non-empty learned list and a wired lifecycle client, enter
// returns the GetLearnedSkillCmd over the cursor row and bumps requestID via
// nextEpoch. This is the one selection path into the detail view; a regression
// that degrades it to a nil-lifecycle no-op (cmd nil) is caught here.
func TestSkillsEnterGetLearnedSkill(t *testing.T) {
	lifecycle := &getLifecycleClient{}
	learned := []client.LearnedSkill{{Project: "/project", ID: "skill-1", Name: "review", Version: "v2"}}
	m := Model{deps: Deps{Skills: lifecycle, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()}}
	m.modal = &skillsState{
		view:             skillsPanel,
		learned:          learned,
		generations:      map[string]uint64{},
		requestID:        7,
		learnedLifecycle: lifecycle,
		nextEpoch:        func() uint64 { m.skillsEpoch++; return m.skillsEpoch },
	}
	st := m.modal.(*skillsState)

	cmd, handled, closed := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || closed {
		t.Fatalf("enter on a learned row should handle without closing: handled=%v closed=%v", handled, closed)
	}
	if cmd == nil {
		t.Fatal("enter must return the GetLearnedSkillCmd, not nil (the nil-lifecycle no-op arm)")
	}
	if st.requestID != m.skillsEpoch {
		t.Fatalf("requestID %d must equal the Model epoch after nextEpoch, epoch=%d (the bump flowed through the whole Model closure)", st.requestID, m.skillsEpoch)
	}
	msg := cmd()
	if _, ok := msg.(client.LearnedSkillMsg); !ok {
		t.Fatalf("enter should fire GetLearnedSkill → LearnedSkillMsg, got %T", msg)
	}
	if lifecycle.calls != 1 {
		t.Fatalf("GetLearnedSkill calls = %d, want 1", lifecycle.calls)
	}
}

type mutateLifecycleClient struct {
	calls int
}

func (*mutateLifecycleClient) ListLearnedSkills(context.Context, string) ([]client.LearnedSkill, error) {
	return nil, nil
}
func (*mutateLifecycleClient) GetLearnedSkill(context.Context, string, string, string, string) (client.LearnedSkill, error) {
	return client.LearnedSkill{}, nil
}
func (f *mutateLifecycleClient) MutateLearnedSkill(_ context.Context, action string, skill client.LearnedSkill) (client.LearnedSkill, error) {
	f.calls++
	if action != "activate" {
		return client.LearnedSkill{}, errors.New("unexpected action: " + action)
	}
	return skill, nil
}
func (*mutateLifecycleClient) RollbackLearnedSkill(context.Context, client.LearnedSkill) (client.LearnedSkill, error) {
	return client.LearnedSkill{}, nil
}
func (*mutateLifecycleClient) ListSkillChanges(context.Context, string) ([]client.SkillChange, error) {
	return nil, nil
}
func (*mutateLifecycleClient) DiffLearnedSkill(context.Context, client.LearnedSkill) (string, error) {
	return "", nil
}
func (*mutateLifecycleClient) ListSkills(context.Context) ([]client.Skill, error) { return nil, nil }

// TestSkillsDetailActionsFireRPC constructs the detail view via m.modal (the
// production Open shape), drives HandleKey 'a' over the surface's own lifecycle
// collaborator (the ONE assert site lives in runSkills), and asserts the
// MutateLearnedSkillCmd is returned (non-nil), the RPC fires when driven, and
// the requestID was bumped via the nextEpoch closure — so the action arm can't
// silently degrade to a nil-lifecycle no-op.
func TestSkillsDetailActionsFireRPC(t *testing.T) {
	lifecycle := &mutateLifecycleClient{}
	skill := client.LearnedSkill{Project: "/project", ID: "skill-1", Name: "review", Version: "v2", Revision: "r2"}
	m := Model{deps: Deps{Skills: lifecycle, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background()}}
	m.modal = &skillsState{
		view:             skillsDetail,
		detail:           &skill,
		generations:      map[string]uint64{},
		requestID:        41,
		learnedLifecycle: lifecycle,
		nextEpoch:        func() uint64 { m.skillsEpoch++; return m.skillsEpoch },
	}
	st := m.modal.(*skillsState)

	cmd, handled, closed := st.HandleKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !handled || closed {
		t.Fatalf("'a' on the detail view should handle without closing: handled=%v closed=%v", handled, closed)
	}
	if cmd == nil {
		t.Fatal("'a' (activate) must return the MutateLearnedSkillCmd, not nil — a nil cmd is the nil-lifecycle no-op arm")
	}
	if st.requestID != m.skillsEpoch {
		t.Fatalf("requestID %d must equal the Model epoch after nextEpoch, epoch=%d (the bump flowed through the whole Model closure)", st.requestID, m.skillsEpoch)
	}
	msg := cmd()
	if _, ok := msg.(client.LearnedSkillMsg); !ok {
		t.Fatalf("'a' should fire MutateLearnedSkill → LearnedSkillMsg, got %T", msg)
	}
	if lifecycle.calls != 1 {
		t.Fatalf("MutateLearnedSkill calls = %d, want 1", lifecycle.calls)
	}
}
