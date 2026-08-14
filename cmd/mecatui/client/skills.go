package client

//revive:disable:exported // skills.go declares the complete proto-free UI skill surface

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The skills-inventory discovery surface: a plain client-owned struct mirroring
// the proto SkillInfo message, the unary RPC wrapper that maps proto → the
// struct, and the tea.Cmd constructor the ui's /skills panel calls. As with the
// MCP and slash-command surfaces, NO proto type leaks past this file — the ui
// renders purely from the structs and msgs below, and the mapping is exercised
// offline against a fake client.

// Skill is one discovered skill (proto SkillInfo, proto-free): its activation
// name and a short one-line description. Metadata only — discovery carries no
// body; activation is a run-path concern handled server-side by the Skill tool.
type Skill struct {
	Name          string
	Description   string
	AgentOwned    bool
	OwnerAgent    string
	ActiveVersion string
}

type LearnedSkill struct {
	ID, Name, Version, Revision, State, OwnerAgent, Description, Body, Supersedes string
	EvidenceCount                                                                 int
	Evaluations                                                                   []SkillEvaluation
	Receipts                                                                      []SkillChange
}
type SkillEvaluation struct {
	Verdict                     string
	FixtureIDs                  []string
	Baseline, Treatment, Reason string
}
type SkillChange struct {
	ID, Operation, FromState, ToState, Verdict string
	InspectAvailable, UndoAvailable            bool
}
type LearnedSkillsMsg struct {
	Skills []LearnedSkill
	Err    error
}
type LearnedSkillMsg struct {
	Skill *LearnedSkill
	Err   error
}
type SkillChangesMsg struct {
	Changes []SkillChange
	Err     error
}
type SkillDiffMsg struct {
	Diff string
	Err  error
}

// SkillsMsg carries a ListSkills result for the /skills panel. Err is set on
// failure; the panel surfaces it rather than silently degrading, mirroring the
// MCP panel's error handling.
type SkillsMsg struct {
	Skills []Skill
	Err    error
}

// ListSkills lists the resolved skills inventory (a startup snapshot server-side).
func (c *Client) ListSkills(ctx context.Context) ([]Skill, error) {
	resp, err := c.svc.ListSkills(ctx, &mecatlv1.ListSkillsRequest{})
	if err != nil {
		return nil, err
	}
	return mapSkills(resp.GetSkills()), nil
}

// mapSkills maps proto SkillInfos to the plain structs (nil-safe).
func mapSkills(in []*mecatlv1.SkillInfo) []Skill {
	out := make([]Skill, 0, len(in))
	for _, s := range in {
		out = append(out, Skill{Name: s.GetName(), Description: s.GetDescription(), AgentOwned: s.GetAgentOwned(), OwnerAgent: s.GetOwnerAgent(), ActiveVersion: s.GetActiveVersion()})
	}
	return out
}

// LearnedSkillClient is the optional lifecycle half of the existing /skills panel.
type LearnedSkillClient interface {
	ListLearnedSkills(context.Context) ([]LearnedSkill, error)
	GetLearnedSkill(context.Context, string, string, string) (LearnedSkill, error)
	MutateLearnedSkill(context.Context, string, LearnedSkill) (LearnedSkill, error)
	RollbackLearnedSkill(context.Context, LearnedSkill) (LearnedSkill, error)
	ListSkillChanges(context.Context) ([]SkillChange, error)
	DiffLearnedSkill(context.Context, LearnedSkill) (string, error)
}

func (c *Client) ListLearnedSkills(ctx context.Context) ([]LearnedSkill, error) {
	resp, err := c.svc.ListLearnedSkills(ctx, &mecatlv1.ListLearnedSkillsRequest{Limit: 200})
	if err != nil {
		return nil, err
	}
	out := make([]LearnedSkill, 0, len(resp.GetSkills()))
	for _, value := range resp.GetSkills() {
		out = append(out, mapLearnedSkill(value))
	}
	return out, nil
}
func (c *Client) GetLearnedSkill(ctx context.Context, id, owner, version string) (LearnedSkill, error) {
	resp, err := c.svc.GetLearnedSkill(ctx, &mecatlv1.GetLearnedSkillRequest{Id: id, OwnerAgent: owner, Version: version})
	if err != nil {
		return LearnedSkill{}, err
	}
	return mapLearnedSkill(resp.GetSkill()), nil
}
func (c *Client) MutateLearnedSkill(ctx context.Context, action string, skill LearnedSkill) (LearnedSkill, error) {
	req := &mecatlv1.MutateLearnedSkillRequest{Id: skill.ID, OwnerAgent: skill.OwnerAgent, Version: skill.Version, ExpectedRevision: skill.Revision}
	var resp *mecatlv1.MutateLearnedSkillResponse
	var err error
	switch action {
	case "activate":
		resp, err = c.svc.ActivateLearnedSkill(ctx, req)
	case "reject":
		resp, err = c.svc.RejectLearnedSkill(ctx, req)
	case "archive":
		resp, err = c.svc.ArchiveLearnedSkill(ctx, req)
	default:
		return LearnedSkill{}, fmt.Errorf("unknown learned-skill action %q", action)
	}
	if err != nil {
		return LearnedSkill{}, err
	}
	return mapLearnedSkill(resp.GetSkill()), nil
}
func (c *Client) RollbackLearnedSkill(ctx context.Context, skill LearnedSkill) (LearnedSkill, error) {
	resp, err := c.svc.RollbackLearnedSkill(ctx, &mecatlv1.RollbackLearnedSkillRequest{Id: skill.ID, OwnerAgent: skill.OwnerAgent, TargetVersion: skill.Supersedes, ExpectedRevision: skill.Revision})
	if err != nil {
		return LearnedSkill{}, err
	}
	return mapLearnedSkill(resp.GetSkill()), nil
}
func (c *Client) DiffLearnedSkill(ctx context.Context, skill LearnedSkill) (string, error) {
	if skill.Supersedes == "" {
		return "", nil
	}
	resp, err := c.svc.DiffLearnedSkillVersions(ctx, &mecatlv1.DiffLearnedSkillVersionsRequest{Id: skill.ID, OwnerAgent: skill.OwnerAgent, FromVersion: skill.Supersedes, ToVersion: skill.Version})
	if err != nil {
		return "", err
	}
	return resp.GetDiff(), nil
}
func (c *Client) ListSkillChanges(ctx context.Context) ([]SkillChange, error) {
	resp, err := c.svc.ListSkillChanges(ctx, &mecatlv1.ListSkillChangesRequest{Limit: 50})
	if err != nil {
		return nil, err
	}
	out := make([]SkillChange, 0, len(resp.GetChanges()))
	for _, r := range resp.GetChanges() {
		out = append(out, mapSkillChange(r))
	}
	return out, nil
}
func mapSkillChange(r *mecatlv1.SkillChangeReceipt) SkillChange {
	if r == nil {
		return SkillChange{}
	}
	return SkillChange{ID: r.GetId(), Operation: r.GetOperation(), FromState: r.GetFromState(), ToState: r.GetToState(), Verdict: r.GetVerdict(), InspectAvailable: r.GetInspectAvailable(), UndoAvailable: r.GetUndoAvailable()}
}
func mapLearnedSkill(value *mecatlv1.LearnedSkillVersion) LearnedSkill {
	if value == nil {
		return LearnedSkill{}
	}
	out := LearnedSkill{ID: value.GetId(), Name: value.GetName(), Version: value.GetVersion(), Revision: value.GetRevision(), State: value.GetState(), OwnerAgent: value.GetOwnerAgent(), Description: value.GetDescription(), Body: value.GetBody(), Supersedes: value.GetSupersedes(), EvidenceCount: int(value.GetEvidenceCount())}
	for _, e := range value.GetEvaluations() {
		out.Evaluations = append(out.Evaluations, SkillEvaluation{Verdict: e.GetVerdict(), FixtureIDs: append([]string(nil), e.GetFixtureIds()...), Baseline: e.GetBaseline(), Treatment: e.GetTreatment(), Reason: e.GetReason()})
	}
	for _, r := range value.GetReceipts() {
		out.Receipts = append(out.Receipts, SkillChange{ID: r.GetId(), Operation: r.GetOperation(), FromState: r.GetFromState(), ToState: r.GetToState(), Verdict: r.GetVerdict(), InspectAvailable: r.GetInspectAvailable(), UndoAvailable: r.GetUndoAvailable()})
	}
	return out
}
func ListLearnedSkillsCmd(ctx context.Context, c LearnedSkillClient) tea.Cmd {
	return func() tea.Msg {
		values, err := c.ListLearnedSkills(ctx)
		return LearnedSkillsMsg{Skills: values, Err: err}
	}
}
func GetLearnedSkillCmd(ctx context.Context, c LearnedSkillClient, s LearnedSkill) tea.Cmd {
	return func() tea.Msg {
		value, err := c.GetLearnedSkill(ctx, s.ID, s.OwnerAgent, s.Version)
		return LearnedSkillMsg{Skill: &value, Err: err}
	}
}
func MutateLearnedSkillCmd(ctx context.Context, c LearnedSkillClient, action string, s LearnedSkill) tea.Cmd {
	return func() tea.Msg {
		value, err := c.MutateLearnedSkill(ctx, action, s)
		return LearnedSkillMsg{Skill: &value, Err: err}
	}
}
func RollbackLearnedSkillCmd(ctx context.Context, c LearnedSkillClient, s LearnedSkill) tea.Cmd {
	return func() tea.Msg {
		value, err := c.RollbackLearnedSkill(ctx, s)
		return LearnedSkillMsg{Skill: &value, Err: err}
	}
}

func ListSkillChangesCmd(ctx context.Context, c LearnedSkillClient) tea.Cmd {
	return func() tea.Msg {
		values, err := c.ListSkillChanges(ctx)
		return SkillChangesMsg{Changes: values, Err: err}
	}
}

func DiffLearnedSkillCmd(ctx context.Context, c LearnedSkillClient, s LearnedSkill) tea.Cmd {
	return func() tea.Msg { value, err := c.DiffLearnedSkill(ctx, s); return SkillDiffMsg{Diff: value, Err: err} }
}

// SkillLister is the subset of *Client the ui's /skills panel needs. Splitting
// it out keeps the ui injectable with a fake for offline tests; *Client
// satisfies it.
type SkillLister interface {
	ListSkills(ctx context.Context) ([]Skill, error)
}

// ListSkillsCmd fetches the skills inventory off the update goroutine; the
// result (success or error) arrives as a SkillsMsg.
func ListSkillsCmd(ctx context.Context, c SkillLister) tea.Cmd {
	return func() tea.Msg {
		skills, err := c.ListSkills(ctx)
		if err != nil {
			return SkillsMsg{Err: err}
		}
		return SkillsMsg{Skills: skills}
	}
}
