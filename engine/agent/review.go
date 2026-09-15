package agent

import (
	"context"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ReviewJob identifies the contextual review direction.
type ReviewJob string

// Contextual review jobs.
const (
	ReviewJobAction  ReviewJob = "action"
	ReviewJobInbound ReviewJob = "inbound"
)

// ReviewAssessment is the reviewer's three-state decision.
type ReviewAssessment string

// Contextual review assessments.
const (
	ReviewAcceptable ReviewAssessment = "acceptable"
	ReviewProhibited ReviewAssessment = "prohibited"
	ReviewUnresolved ReviewAssessment = "unresolved"
)

// ReviewPrincipalFact carries one harness-established principal fact.
type ReviewPrincipalFact struct {
	Kind, Ref, Statement string
	PositiveVerdict      bool
}

// ReviewCaller describes the reviewed caller's effective authority.
type ReviewCaller struct {
	Role         string
	Isolated     bool
	Capabilities []string
}

// ReviewTarget identifies the destination implicated by a review.
type ReviewTarget struct{ Kind, Display, DestinationID string }

// ReviewEvidenceMeta advertises one review-local evidence handle.
type ReviewEvidenceMeta struct {
	Handle, Kind, Display, Version string
	Complete                       bool
}

// ReviewCapacity reports the applicable count and byte capacities.
type ReviewCapacity struct {
	MaxEvidenceHandles int
	MaxEvidenceBytes   int64
	MaxTrajectoryFacts int
	MaxTrajectoryBytes int64
}

// ReviewTrajectoryFact carries one harness-established root-run fact.
type ReviewTrajectoryFact struct {
	Call                                          session.ToolCallID
	Ref, Direction, DataClass, TargetID, Decision string
}

// ReviewConcern is one validated reviewer finding.
type ReviewConcern struct{ Ref, Category, Rationale, SourceRef string }

// ReviewEvidenceUse records how read evidence supports an assessment.
type ReviewEvidenceUse struct {
	Handle, Version string
	Supports        []string
}

// ReviewMissingEvidence describes decision-relevant unavailable evidence.
type ReviewMissingEvidence struct{ Ref, Kind, Handle, Reason string }

// ToolReviewRequest is the complete harness-owned contextual review input.
type ToolReviewRequest struct {
	ReviewID               string
	Job                    ReviewJob
	Event                  governance.HookEvent
	EffectiveCall          session.ToolCall
	PrincipalFacts         []ReviewPrincipalFact
	PrincipalFactsComplete bool
	Caller                 ReviewCaller
	Environment            session.EnvironmentRef
	Target                 ReviewTarget
	Evidence               []ReviewEvidenceMeta
	EvidenceComplete       bool
	Trajectory             []ReviewTrajectoryFact
	TrajectoryComplete     bool
	Capacity               ReviewCapacity
}

// ToolReviewResult is one completed contextual assessment. Its zero value is
// unresolved: a nil error never makes an empty Assessment acceptable.
type ToolReviewResult struct {
	Assessment ReviewAssessment
	Concerns   []ReviewConcern
	Evidence   []ReviewEvidenceUse
	Missing    []ReviewMissingEvidence
}

// ReviewEvidenceRequest names one advertised review-local capability.
type ReviewEvidenceRequest struct{ ReviewID, Handle, Version string }

// ReviewEvidence is one bounded evidence preview.
type ReviewEvidence struct {
	Handle, Kind, Version string
	Complete              bool
	Content               string
}

// ReviewEvidenceSource resolves review-local evidence capabilities.
type ReviewEvidenceSource interface {
	ReadReviewEvidence(context.Context, ReviewEvidenceRequest) (ReviewEvidence, error)
}

// ReviewEvidencePreparation is the trusted, immutable input used to mint a
// finite evidence inventory. Owner comes from the admitted Session, never from
// reviewer or model output. Result is non-nil only for an inbound review.
type ReviewEvidencePreparation struct {
	Request     ToolReviewRequest
	Environment tool.Environment
	Owner       *session.Principal
	Result      *session.ToolResult
}

// PreparedReviewEvidence owns one review-local finite capability set. Close is
// called by the Engine after any immediate human wait and binding revalidation.
type PreparedReviewEvidence struct {
	Source   ReviewEvidenceSource
	Evidence []ReviewEvidenceMeta
	Complete bool
	Close    func()
}

// ReviewEvidencePreparer mints finite evidence capabilities under the exact
// admitted environment and caller authority. Implementations must reject an
// unsupported backend rather than reading through Workspace.Root as a host path.
type ReviewEvidencePreparer interface {
	PrepareReviewEvidence(context.Context, ReviewEvidencePreparation) (PreparedReviewEvidence, error)
}

// ReviewPolicyProvider declares configured applicability and enforcement. A
// ToolReviewer without this capability retains action-only enforcement.
type ReviewPolicyProvider interface {
	GuardrailReviewPolicy(string, ReviewJob, bool) (applies, enforce bool)
}

// ReviewMetadataProvider projects machine-safe rule and checker-route metadata.
type ReviewMetadataProvider interface {
	GuardrailReviewMetadata(string, ReviewJob) (ruleID, ruleOrigin, providerID, modelID string)
}

// ReviewGrantStore is the optional session-local exact-action repeat-grant seam.
type ReviewGrantStore interface {
	GrantDigest(ToolReviewRequest) (string, bool)
	AllowsGrant(string) bool
	ArmGrant(string, string)
}

// ToolReviewer performs one contextual tool review.
type ToolReviewer interface {
	Review(context.Context, ToolReviewRequest, ReviewEvidenceSource) (ToolReviewResult, error)
}

// ReviewDetail is transient, owner-authorized human display detail.
type ReviewDetail struct {
	SessionID                                    session.SessionID
	ReviewID, Concern, SourceDisplay, NextAction string
}

// ReviewDetailSink publishes transient review detail under root-run ownership.
type ReviewDetailSink interface {
	PublishReviewDetail(context.Context, ReviewDetail)
}

// rootReviewDetailSink is the optional root-aware lifecycle binding used by the
// in-tree Service. Keeping it optional preserves ReviewDetailSink compatibility
// for engine consumers while preventing child ids from becoming standalone authority.
type rootReviewDetailSink interface {
	PublishReviewDetailForRoot(context.Context, session.SessionID, ReviewDetail)
}
