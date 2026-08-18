package client

//revive:disable:exported // complete proto-free manual-dream surface is declared as one unit

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

const (
	DreamTargetProjectMemory = "project_memory"
	DreamTargetUserModel     = "user_model"
	DreamDecisionApply       = "apply"
	DreamDecisionDismiss     = "dismiss"
)

type DreamTargetCapability struct {
	Generate, Decide  bool
	UnavailableReason string
}

type ManualDreamCapabilities struct {
	ProjectMemory DreamTargetCapability
	UserModel     DreamTargetCapability
}

type DreamParticipant struct {
	Key, Value, Description string
}

type DreamReplacement struct {
	Value, Description string
}

type DreamOperation struct {
	Kind                   string
	Survivor               DreamParticipant
	Sources                []DreamParticipant
	Replacement            DreamReplacement
	Reason                 string
	ExactDuplicateEligible bool
}

type DreamPlan struct {
	ID, Target                         string
	ExpiresAt                          time.Time
	PlannedOperationCount, SourceCount int
	Operations                         []DreamOperation
}

type DreamReceipt struct {
	ID, Target, Disposition                       string
	Planned, Applied, Conflicted, Skipped, Failed int
}

type DreamMsg struct {
	Plan       *DreamPlan
	Receipt    *DreamReceipt
	Err        error
	Generation uint64
	RequestID  uint64
}

type DreamClient interface {
	GenerateDreamPlan(context.Context, string) (DreamPlan, error)
	DecideDreamPlan(context.Context, string, string) (DreamReceipt, error)
}

func (c *Client) GenerateDreamPlan(ctx context.Context, target string) (DreamPlan, error) {
	resp, err := c.svc.GenerateDreamPlan(ctx, &mecatlv1.GenerateDreamPlanRequest{Target: target})
	if err != nil {
		return DreamPlan{}, err
	}
	return mapDreamPlan(resp.GetPlan()), nil
}

func (c *Client) DecideDreamPlan(ctx context.Context, id, decision string) (DreamReceipt, error) {
	resp, err := c.svc.DecideDreamPlan(ctx, &mecatlv1.DecideDreamPlanRequest{PlanId: id, Decision: decision})
	if err != nil {
		return DreamReceipt{}, err
	}
	return mapDreamReceipt(resp.GetReceipt()), nil
}

func GenerateDreamPlanCmd(ctx context.Context, c DreamClient, target string, generation, requestID uint64) tea.Cmd {
	return func() tea.Msg {
		plan, err := c.GenerateDreamPlan(ctx, target)
		return DreamMsg{Plan: &plan, Err: err, Generation: generation, RequestID: requestID}
	}
}

func DecideDreamPlanCmd(ctx context.Context, c DreamClient, id, decision string, generation, requestID uint64) tea.Cmd {
	return func() tea.Msg {
		receipt, err := c.DecideDreamPlan(ctx, id, decision)
		return DreamMsg{Receipt: &receipt, Err: err, Generation: generation, RequestID: requestID}
	}
}

type DreamDecisionErrorKind uint8

const (
	DreamDecisionUnknown DreamDecisionErrorKind = iota
	DreamDecisionInProgress
	DreamDecisionConflict
	DreamDecisionTerminalConflict
	DreamDecisionPlanGone
)

func ClassifyDreamDecisionError(err error) DreamDecisionErrorKind {
	switch grpcstatus.Code(err) {
	case codes.Aborted:
		return DreamDecisionInProgress
	case codes.FailedPrecondition:
		return DreamDecisionConflict
	case codes.AlreadyExists:
		return DreamDecisionTerminalConflict
	case codes.NotFound:
		return DreamDecisionPlanGone
	default:
		return DreamDecisionUnknown
	}
}

func IsDreamPlanGone(err error) bool {
	return ClassifyDreamDecisionError(err) == DreamDecisionPlanGone
}

func IsDreamDecisionConflict(err error) bool {
	kind := ClassifyDreamDecisionError(err)
	return kind == DreamDecisionConflict || kind == DreamDecisionTerminalConflict
}

func mapDreamPlan(p *mecatlv1.DreamReviewPlan) DreamPlan {
	if p == nil {
		return DreamPlan{}
	}
	out := DreamPlan{ID: validText(p.GetId()), Target: validText(p.GetTarget()), PlannedOperationCount: int(p.GetPlannedOperationCount()), SourceCount: int(p.GetPlannedSourceCount())}
	if t := p.GetExpiresAt(); t != nil && t.IsValid() {
		out.ExpiresAt = t.AsTime()
	}
	for _, operation := range p.GetOperations() {
		op := DreamOperation{Kind: validText(operation.GetKind()), Survivor: mapDreamParticipant(operation.GetSurvivor()), Reason: validText(operation.GetReason()), ExactDuplicateEligible: operation.GetExactDuplicateEligible()}
		if replacement := operation.GetReplacement(); replacement != nil {
			op.Replacement = DreamReplacement{Value: validText(replacement.GetValue()), Description: validText(replacement.GetDescription())}
		}
		for _, source := range operation.GetSources() {
			op.Sources = append(op.Sources, mapDreamParticipant(source))
		}
		out.Operations = append(out.Operations, op)
	}
	return out
}

func mapDreamParticipant(p *mecatlv1.DreamParticipant) DreamParticipant {
	if p == nil {
		return DreamParticipant{}
	}
	return DreamParticipant{Key: validText(p.GetKey()), Value: validText(p.GetValue()), Description: validText(p.GetDescription())}
}

func mapDreamReceipt(r *mecatlv1.DreamReceipt) DreamReceipt {
	if r == nil {
		return DreamReceipt{}
	}
	return DreamReceipt{ID: validText(r.GetId()), Target: validText(r.GetTarget()), Disposition: validText(r.GetDisposition()), Planned: int(r.GetPlannedSourceCount()), Applied: int(r.GetAppliedSourceCount()), Conflicted: int(r.GetConflictedSourceCount()), Skipped: int(r.GetSkippedSourceCount()), Failed: int(r.GetFailedSourceCount())}
}

func validText(value string) string { return strings.ToValidUTF8(value, "\uFFFD") }
