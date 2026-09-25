package client

import (
	"context"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// GuardrailCoverage is the owner-authorized effective checker view for one session.
type GuardrailCoverage struct {
	Enabled                           bool
	CheckerProviderID, CheckerModelID string
	Entries                           []GuardrailCoverageEntry
}

// GuardrailCoverageEntry is one effective tool and review-direction row.
type GuardrailCoverageEntry struct {
	Tool, Phase, Job, Mode, RuleID, RuleOrigin, Inspection, Reason string
}

// GuardrailCoverageMsg carries an asynchronous coverage response.
type GuardrailCoverageMsg struct {
	Coverage  GuardrailCoverage
	Err       error
	SessionID string
	RequestID uint64
	Posture   bool
}

// GuardrailReviewDetail is bounded owner-authorized live display detail.
type GuardrailReviewDetail struct{ ReviewID, Concern, SourceDisplay, NextAction string }

// GuardrailReviewDetailMsg carries an asynchronous detail response.
type GuardrailReviewDetailMsg struct {
	ReviewID string
	Detail   GuardrailReviewDetail
	Err      error
}

// ListGuardrailCoverage returns effective coverage for one session.
func (c *Client) ListGuardrailCoverage(ctx context.Context, sessionID string) (GuardrailCoverage, error) {
	resp, err := c.svc.ListGuardrailCoverage(withSessionAffinity(ctx, sessionID), &mecatlv1.ListGuardrailCoverageRequest{SessionId: sessionID})
	if err != nil {
		return GuardrailCoverage{}, err
	}
	out := GuardrailCoverage{Enabled: resp.GetEnabled(), CheckerProviderID: resp.GetCheckerProviderId(), CheckerModelID: resp.GetCheckerModelId()}
	for _, entry := range resp.GetEntries() {
		out.Entries = append(out.Entries, GuardrailCoverageEntry{Tool: entry.GetTool(), Phase: entry.GetPhase(), Job: guardrailJob(entry.GetJob()), Mode: entry.GetMode(), RuleID: entry.GetRuleId(), RuleOrigin: entry.GetRuleOrigin(), Inspection: guardrailInspection(entry.GetInspection()), Reason: entry.GetReason()})
	}
	return out, nil
}

// GetGuardrailReviewDetail returns bounded live detail for one review.
func (c *Client) GetGuardrailReviewDetail(ctx context.Context, sessionID, reviewID string) (GuardrailReviewDetail, error) {
	resp, err := c.svc.GetGuardrailReviewDetail(withSessionAffinity(ctx, sessionID), &mecatlv1.GetGuardrailReviewDetailRequest{SessionId: sessionID, ReviewId: reviewID})
	if err != nil {
		return GuardrailReviewDetail{}, err
	}
	return GuardrailReviewDetail{ReviewID: resp.GetReviewId(), Concern: resp.GetConcern(), SourceDisplay: resp.GetSourceDisplay(), NextAction: resp.GetNextAction()}, nil
}

// GuardrailClient is the TUI's contextual guardrail status surface.
type GuardrailClient interface {
	ListGuardrailCoverage(context.Context, string) (GuardrailCoverage, error)
	GetGuardrailReviewDetail(context.Context, string, string) (GuardrailReviewDetail, error)
}

// ListGuardrailCoverageCmd loads coverage without blocking the TUI update loop.
func ListGuardrailCoverageCmd(ctx context.Context, c interface {
	ListGuardrailCoverage(context.Context, string) (GuardrailCoverage, error)
}, sessionID string, requestID uint64, posture bool) tea.Cmd {
	return func() tea.Msg {
		coverage, err := c.ListGuardrailCoverage(ctx, sessionID)
		return GuardrailCoverageMsg{Coverage: coverage, Err: err, SessionID: sessionID, RequestID: requestID, Posture: posture}
	}
}

// GetGuardrailReviewDetailCmd loads detail without blocking the TUI update loop.
func GetGuardrailReviewDetailCmd(ctx context.Context, c interface {
	GetGuardrailReviewDetail(context.Context, string, string) (GuardrailReviewDetail, error)
}, sessionID, reviewID string) tea.Cmd {
	return func() tea.Msg {
		detail, err := c.GetGuardrailReviewDetail(ctx, sessionID, reviewID)
		return GuardrailReviewDetailMsg{ReviewID: reviewID, Detail: detail, Err: err}
	}
}

func guardrailJob(v mecatlv1.GuardrailJob) string {
	switch v {
	case mecatlv1.GuardrailJob_GUARDRAIL_JOB_ACTION:
		return "action"
	case mecatlv1.GuardrailJob_GUARDRAIL_JOB_INBOUND:
		return "inbound"
	}
	return string(SessionKindUnknown)
}
func guardrailInspection(v mecatlv1.GuardrailInspection) string {
	switch v {
	case mecatlv1.GuardrailInspection_GUARDRAIL_INSPECTION_COMPLETE:
		return "complete"
	case mecatlv1.GuardrailInspection_GUARDRAIL_INSPECTION_OPERATIONAL_FAILURE:
		return "operational_failure"
	}
	return string(SessionKindUnknown)
}
func guardrailAssessment(v mecatlv1.GuardrailAssessment) string {
	switch v {
	case mecatlv1.GuardrailAssessment_GUARDRAIL_ASSESSMENT_ACCEPTABLE:
		return "acceptable"
	case mecatlv1.GuardrailAssessment_GUARDRAIL_ASSESSMENT_PROHIBITED:
		return "prohibited"
	case mecatlv1.GuardrailAssessment_GUARDRAIL_ASSESSMENT_UNRESOLVED:
		return "unresolved"
	}
	return string(SessionKindUnknown)
}
func guardrailDisposition(v mecatlv1.GuardrailDisposition) string {
	switch v {
	case mecatlv1.GuardrailDisposition_GUARDRAIL_DISPOSITION_EXECUTE:
		return "execute"
	case mecatlv1.GuardrailDisposition_GUARDRAIL_DISPOSITION_ASK_ACTION:
		return "ask_action"
	case mecatlv1.GuardrailDisposition_GUARDRAIL_DISPOSITION_WITHHOLD_RESULT:
		return "withhold_result"
	case mecatlv1.GuardrailDisposition_GUARDRAIL_DISPOSITION_RELEASE_RESULT:
		return "release_result"
	case mecatlv1.GuardrailDisposition_GUARDRAIL_DISPOSITION_DENY:
		return "deny"
	case mecatlv1.GuardrailDisposition_GUARDRAIL_DISPOSITION_PASS_ADVISORY:
		return "pass_advisory"
	case mecatlv1.GuardrailDisposition_GUARDRAIL_DISPOSITION_CONTINUE_WARNING:
		return "continue_warning"
	}
	return string(SessionKindUnknown)
}
