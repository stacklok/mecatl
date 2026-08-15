package client

//revive:disable:exported // skills.go declares the complete proto-free UI skill surface

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/learning"
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
	Project, PublicationStatus, PublicationError                                  string
	Generation                                                                    uint64
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
	Skills      []LearnedSkill
	Project     string
	Generations map[string]uint64
	RequestID   uint64
	Err         error
}
type LearnedSkillMsg struct {
	Skill              *LearnedSkill
	Project            string
	Generation         uint64
	SelectedSkillID    string
	SelectedOwnerAgent string
	SelectedVersion    string
	ExpectedRevision   string
	TargetVersion      string
	Action             string
	RequestID          uint64
	PublicationStatus  string
	PublicationError   string
	Err                error
}
type SkillChangesMsg struct {
	Changes []SkillChange
	Err     error
}
type SkillDiffMsg struct {
	Diff      string
	Project   string
	SkillID   string
	Version   string
	RequestID uint64
	Err       error
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
	ListLearnedSkills(context.Context, string) ([]LearnedSkill, error)
	GetLearnedSkill(context.Context, string, string, string, string) (LearnedSkill, error)
	MutateLearnedSkill(context.Context, string, LearnedSkill) (LearnedSkill, error)
	RollbackLearnedSkill(context.Context, LearnedSkill) (LearnedSkill, error)
	ListSkillChanges(context.Context, string) ([]SkillChange, error)
	DiffLearnedSkill(context.Context, LearnedSkill) (string, error)
}

func (c *Client) ListLearnedSkills(ctx context.Context, project string) ([]LearnedSkill, error) {
	values, _, err := c.ListLearnedSkillsPage(ctx, project)
	return values, err
}

func (c *Client) ListLearnedSkillsPage(ctx context.Context, project string) ([]LearnedSkill, map[string]uint64, error) {
	projects := []string{""}
	if project != "" {
		projects = append(projects, project)
	}
	var out []LearnedSkill
	generations := make(map[string]uint64, len(projects))
	for _, partition := range projects {
		cursor := ""
		for {
			resp, err := c.svc.ListLearnedSkills(ctx, &mecatlv1.ListLearnedSkillsRequest{Project: partition, Cursor: cursor, Limit: learning.MaxSkillPageSize})
			if err != nil {
				return nil, generations, err
			}
			generations[partition] = resp.GetGeneration()
			for _, value := range resp.GetSkills() {
				skill := mapLearnedSkill(value)
				skill.Project, skill.Generation = resp.GetProject(), resp.GetGeneration()
				out = append(out, skill)
			}
			if resp.GetNextCursor() == "" {
				break
			}
			cursor = resp.GetNextCursor()
		}
	}
	return out, generations, nil
}
func (c *Client) GetLearnedSkill(ctx context.Context, project, id, owner, version string) (LearnedSkill, error) {
	resp, err := c.svc.GetLearnedSkill(ctx, &mecatlv1.GetLearnedSkillRequest{Project: project, Id: id, OwnerAgent: owner, Version: version})
	if err != nil {
		return LearnedSkill{}, err
	}
	out := mapLearnedSkill(resp.GetSkill())
	out.Project, out.Generation = resp.GetProject(), resp.GetGeneration()
	return out, nil
}
func (c *Client) MutateLearnedSkill(ctx context.Context, action string, skill LearnedSkill) (LearnedSkill, error) {
	req := &mecatlv1.MutateLearnedSkillRequest{Project: skill.Project, Id: skill.ID, OwnerAgent: skill.OwnerAgent, Version: skill.Version, ExpectedRevision: skill.Revision}
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
	out := mapLearnedSkill(resp.GetSkill())
	out.Project, out.Generation = resp.GetProject(), resp.GetGeneration()
	out.PublicationStatus, out.PublicationError = resp.GetPublicationStatus(), resp.GetPublicationError()
	return out, nil
}
func (c *Client) RollbackLearnedSkill(ctx context.Context, skill LearnedSkill) (LearnedSkill, error) {
	resp, err := c.svc.RollbackLearnedSkill(ctx, &mecatlv1.RollbackLearnedSkillRequest{Project: skill.Project, Id: skill.ID, OwnerAgent: skill.OwnerAgent, TargetVersion: skill.Supersedes, ExpectedRevision: skill.Revision})
	if err != nil {
		return LearnedSkill{}, err
	}
	out := mapLearnedSkill(resp.GetSkill())
	out.Project, out.Generation = resp.GetProject(), resp.GetGeneration()
	out.PublicationStatus, out.PublicationError = resp.GetPublicationStatus(), resp.GetPublicationError()
	return out, nil
}
func (c *Client) DiffLearnedSkill(ctx context.Context, skill LearnedSkill) (string, error) {
	if skill.Supersedes == "" {
		return "", nil
	}
	resp, err := c.svc.DiffLearnedSkillVersions(ctx, &mecatlv1.DiffLearnedSkillVersionsRequest{Project: skill.Project, Id: skill.ID, OwnerAgent: skill.OwnerAgent, FromVersion: skill.Supersedes, ToVersion: skill.Version})
	if err != nil {
		return "", err
	}
	return resp.GetDiff(), nil
}
func (c *Client) ListSkillChanges(ctx context.Context, project string) ([]SkillChange, error) {
	projects := []string{""}
	if project != "" {
		projects = append(projects, project)
	}
	var out []SkillChange
	for _, partition := range projects {
		cursor := ""
		for {
			resp, err := c.svc.ListSkillChanges(ctx, &mecatlv1.ListSkillChangesRequest{Project: partition, Cursor: cursor, Limit: learning.MaxSkillPageSize})
			if err != nil {
				return nil, err
			}
			for _, r := range resp.GetChanges() {
				out = append(out, mapSkillChange(r))
			}
			if resp.GetNextCursor() == "" {
				break
			}
			cursor = resp.GetNextCursor()
		}
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

type learnedSkillPager interface {
	ListLearnedSkillsPage(context.Context, string) ([]LearnedSkill, map[string]uint64, error)
}

func ListLearnedSkillsCmd(ctx context.Context, c LearnedSkillClient, project string, requestID uint64) tea.Cmd {
	return func() tea.Msg {
		if pager, ok := c.(learnedSkillPager); ok {
			values, generations, err := pager.ListLearnedSkillsPage(ctx, project)
			return LearnedSkillsMsg{Skills: values, Project: project, Generations: generations, RequestID: requestID, Err: err}
		}
		values, err := c.ListLearnedSkills(ctx, project)
		generations := make(map[string]uint64, 2)
		for _, value := range values {
			generations[value.Project] = max(generations[value.Project], value.Generation)
		}
		return LearnedSkillsMsg{Skills: values, Project: project, Generations: generations, RequestID: requestID, Err: err}
	}
}
func GetLearnedSkillCmd(ctx context.Context, c LearnedSkillClient, s LearnedSkill, requestID uint64) tea.Cmd {
	return func() tea.Msg {
		value, err := c.GetLearnedSkill(ctx, s.Project, s.ID, s.OwnerAgent, s.Version)
		return LearnedSkillMsg{Skill: &value, Project: s.Project, Generation: value.Generation, SelectedSkillID: s.ID, SelectedOwnerAgent: s.OwnerAgent, SelectedVersion: s.Version, RequestID: requestID, Err: err}
	}
}
func MutateLearnedSkillCmd(ctx context.Context, c LearnedSkillClient, action string, s LearnedSkill, requestID uint64) tea.Cmd {
	return func() tea.Msg {
		value, err := c.MutateLearnedSkill(ctx, action, s)
		return LearnedSkillMsg{Skill: &value, Project: s.Project, Generation: value.Generation, SelectedSkillID: s.ID, SelectedOwnerAgent: s.OwnerAgent, SelectedVersion: s.Version, ExpectedRevision: s.Revision, TargetVersion: s.Version, Action: action, RequestID: requestID, PublicationStatus: value.PublicationStatus, PublicationError: value.PublicationError, Err: err}
	}
}
func RollbackLearnedSkillCmd(ctx context.Context, c LearnedSkillClient, s LearnedSkill, requestID uint64) tea.Cmd {
	return func() tea.Msg {
		value, err := c.RollbackLearnedSkill(ctx, s)
		return LearnedSkillMsg{Skill: &value, Project: s.Project, Generation: value.Generation, SelectedSkillID: s.ID, SelectedOwnerAgent: s.OwnerAgent, SelectedVersion: s.Version, ExpectedRevision: s.Revision, TargetVersion: s.Supersedes, Action: "rollback", RequestID: requestID, PublicationStatus: value.PublicationStatus, PublicationError: value.PublicationError, Err: err}
	}
}

func ListSkillChangesCmd(ctx context.Context, c LearnedSkillClient, project string) tea.Cmd {
	return func() tea.Msg {
		values, err := c.ListSkillChanges(ctx, project)
		return SkillChangesMsg{Changes: values, Err: err}
	}
}

func DiffLearnedSkillCmd(ctx context.Context, c LearnedSkillClient, s LearnedSkill, requestID uint64) tea.Cmd {
	return func() tea.Msg {
		value, err := c.DiffLearnedSkill(ctx, s)
		return SkillDiffMsg{Diff: value, Project: s.Project, SkillID: s.ID, Version: s.Version, RequestID: requestID, Err: err}
	}
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
