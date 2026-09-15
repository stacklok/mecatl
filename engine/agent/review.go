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
	// Continuation is the next opaque handle for the same versioned evidence
	// object. Empty means this page is final.
	Continuation string
	Complete     bool
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
	Continuation          string
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
	// Authorize verifies one evidence-source call against the originating run's
	// effective catalog, mode, and bound authority. Preparers must call it before
	// touching backend metadata or content; model/reviewer input cannot replace it.
	Authorize func(context.Context, session.ToolCall) error
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

// ReviewPolicyProvider declares configured applicability and enforcement. The
// toolName and job identify the candidate rule; operationalFailure selects its
// checker-down posture. The returns report whether review applies and whether a
// finding or unresolved assessment is enforced. A ToolReviewer without this
// capability retains action-only enforcement.
type ReviewPolicyProvider interface {
	GuardrailReviewPolicy(toolName string, job ReviewJob, operationalFailure bool) (applies, enforce bool)
}

// ReviewMetadataProvider projects machine-safe metadata for the rule selected by
// toolName and job. Empty return values mean no metadata is available.
type ReviewMetadataProvider interface {
	GuardrailReviewMetadata(toolName string, job ReviewJob) (ruleID, ruleOrigin, providerID, modelID string)
}

// ReviewGrantStore is the optional exact-action repeat-grant seam. GrantDigest
// must bind the exact session, environment revision, caller authority, effective
// call, target, and every eligible versioned dependency under a purpose-separated
// keyed digest; false means repeat is ineligible. AllowsGrant tests that digest.
// ArmGrant stores only that digest for the named session and must not widen its
// scope or persist it beyond the implementation's documented session lifetime.
type ReviewGrantStore interface {
	GrantDigest(request ToolReviewRequest) (digest string, eligible bool)
	AllowsGrant(digest string) bool
	ArmGrant(digest, sessionID string)
}

// ToolReviewer performs one contextual tool review.
type ToolReviewer interface {
	Review(context.Context, ToolReviewRequest, ReviewEvidenceSource) (ToolReviewResult, error)
}

// ReviewDetail is transient, owner-authorized human display detail. RootSessionID
// is the owning delegation-root run; SessionID is the reviewed root or child.
// Sinks must scope child relations and cleanup to RootSessionID, never infer a
// child to be its own root.
type ReviewDetail struct {
	RootSessionID, SessionID                     session.SessionID
	ReviewID, Concern, SourceDisplay, NextAction string
}

// ReviewDetailSink publishes transient review detail under the explicit root-run
// ownership carried by ReviewDetail.RootSessionID.
type ReviewDetailSink interface {
	PublishReviewDetail(context.Context, ReviewDetail)
}
