package grpcdriver

//revive:disable:exported // methods implement the documented SkillRepository contracts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

// SkillRepository adapts the learned-skill lifecycle to a remote driver.
type SkillRepository struct {
	client driverv1.SkillRepositoryServiceClient
}

var _ learning.SkillRepository = (*SkillRepository)(nil)

// ValidatedSkillRepository adds the optional validated-activation capability.
type ValidatedSkillRepository struct{ *SkillRepository }

var _ learning.ValidatedSkillActivator = (*ValidatedSkillRepository)(nil)

func NewSkillRepository(conn grpc.ClientConnInterface) *SkillRepository {
	return &SkillRepository{client: driverv1.NewSkillRepositoryServiceClient(conn)}
}

func NewValidatedSkillRepository(conn grpc.ClientConnInterface) *ValidatedSkillRepository {
	return &ValidatedSkillRepository{SkillRepository: NewSkillRepository(conn)}
}

func (r *SkillRepository) Generation(ctx context.Context, partition learning.SkillPartition) (learning.SkillGeneration, error) {
	resp, err := r.client.ListSkillVersions(ctx, &driverv1.ListSkillVersionsRequest{Partition: skillPartitionToProto(partition), Limit: 1})
	if err != nil {
		return 0, skillStatusToErr(ctx, err)
	}
	return learning.SkillGeneration(resp.GetGeneration()), nil
}

func (r *SkillRepository) CreateDraft(ctx context.Context, partition learning.SkillPartition, owner string, bundle learning.SkillBundle, provenance learning.SkillProvenance) (learning.SkillVersion, error) {
	resp, err := r.client.CreateSkillDraft(ctx, &driverv1.CreateSkillDraftRequest{Partition: skillPartitionToProto(partition), OwnerAgent: owner, Bundle: skillBundleToProto(bundle), Provenance: skillProvenanceToProto(provenance)})
	return skillResponse(ctx, resp, err)
}

func (r *SkillRepository) Get(ctx context.Context, partition learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID) (learning.SkillVersion, bool, error) {
	resp, err := r.client.GetSkillVersion(ctx, &driverv1.GetSkillVersionRequest{Partition: skillPartitionToProto(partition), OwnerAgent: owner, Id: string(id), Version: string(version)})
	if err != nil {
		return learning.SkillVersion{}, false, skillStatusToErr(ctx, err)
	}
	if !resp.GetFound() {
		if resp.GetVersion() != nil {
			return learning.SkillVersion{}, false, errors.New("grpcdriver: skill miss included a version")
		}
		return learning.SkillVersion{}, false, nil
	}
	value, err := skillVersionFromProto(resp.GetVersion())
	return value, err == nil, err
}

func (r *SkillRepository) List(ctx context.Context, partition learning.SkillPartition, query learning.SkillList) (learning.SkillPage, error) {
	resp, err := r.client.ListSkillVersions(ctx, &driverv1.ListSkillVersionsRequest{Partition: skillPartitionToProto(partition), After: string(query.After), Limit: int32(query.Limit), Name: query.Name, State: string(query.State), OwnerAgent: query.OwnerAgent}) // #nosec G115 -- domain page limit is bounded
	if err != nil {
		return learning.SkillPage{}, skillStatusToErr(ctx, err)
	}
	page := learning.SkillPage{Versions: make([]learning.SkillVersion, 0, len(resp.GetVersions())), Next: learning.SkillID(resp.GetNext()), Generation: learning.SkillGeneration(resp.GetGeneration())}
	for _, wire := range resp.GetVersions() {
		value, decodeErr := skillVersionFromProto(wire)
		if decodeErr != nil {
			return learning.SkillPage{}, decodeErr
		}
		page.Versions = append(page.Versions, value)
	}
	return page, nil
}

func (r *SkillRepository) RecordEvaluation(ctx context.Context, partition learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, revision learning.Revision, evaluation learning.SkillEvaluation) (learning.SkillVersion, error) {
	resp, err := r.client.RecordSkillEvaluation(ctx, &driverv1.RecordSkillEvaluationRequest{Mutation: skillMutationToProto(partition, owner, id, version, revision), Evaluation: skillEvaluationToProto(evaluation)})
	return skillResponse(ctx, resp, err)
}

func (r *SkillRepository) Stage(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, v learning.VersionID, rev learning.Revision) (learning.SkillVersion, error) {
	resp, err := r.client.StageSkill(ctx, skillMutationToProto(p, owner, id, v, rev))
	return skillResponse(ctx, resp, err)
}
func (r *SkillRepository) Activate(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, v learning.VersionID, rev learning.Revision) (learning.SkillVersion, error) {
	resp, err := r.client.ActivateSkill(ctx, skillMutationToProto(p, owner, id, v, rev))
	return skillResponse(ctx, resp, err)
}
func (r *ValidatedSkillRepository) ActivateValidated(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, v learning.VersionID, rev learning.Revision) (learning.SkillVersion, error) {
	resp, err := r.client.ActivateValidatedSkill(ctx, skillMutationToProto(p, owner, id, v, rev))
	return skillResponse(ctx, resp, err)
}
func (r *SkillRepository) Reject(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, v learning.VersionID, rev learning.Revision) (learning.SkillVersion, error) {
	resp, err := r.client.RejectSkill(ctx, skillMutationToProto(p, owner, id, v, rev))
	return skillResponse(ctx, resp, err)
}
func (r *SkillRepository) Archive(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, v learning.VersionID, rev learning.Revision) (learning.SkillVersion, error) {
	resp, err := r.client.ArchiveSkill(ctx, skillMutationToProto(p, owner, id, v, rev))
	return skillResponse(ctx, resp, err)
}
func (r *SkillRepository) Rollback(ctx context.Context, p learning.SkillPartition, owner string, id learning.SkillID, rev learning.Revision, target learning.VersionID) (learning.SkillVersion, error) {
	resp, err := r.client.RollbackSkill(ctx, &driverv1.RollbackSkillRequest{Partition: skillPartitionToProto(p), OwnerAgent: owner, Id: string(id), ExpectedRevision: string(rev), TargetVersion: string(target)})
	return skillResponse(ctx, resp, err)
}

func skillMutationToProto(p learning.SkillPartition, owner string, id learning.SkillID, version learning.VersionID, revision learning.Revision) *driverv1.SkillMutationRequest {
	return &driverv1.SkillMutationRequest{Partition: skillPartitionToProto(p), OwnerAgent: owner, Id: string(id), Version: string(version), ExpectedRevision: string(revision)}
}

func skillResponse(ctx context.Context, resp *driverv1.SkillVersionResponse, err error) (learning.SkillVersion, error) {
	if err != nil {
		return learning.SkillVersion{}, skillStatusToErr(ctx, err)
	}
	return skillVersionFromProto(resp.GetVersion())
}

func skillPartitionToProto(p learning.SkillPartition) *driverv1.LearnedSkillPartition {
	return &driverv1.LearnedSkillPartition{Principal: session.ToValidUTF8(p.Principal), Project: session.ToValidUTF8(p.Project)}
}
func skillPartitionFromProto(p *driverv1.LearnedSkillPartition) learning.SkillPartition {
	if p == nil {
		return learning.SkillPartition{}
	}
	return learning.SkillPartition{Principal: p.GetPrincipal(), Project: p.GetProject()}
}
func skillBundleToProto(b learning.SkillBundle) *driverv1.LearnedSkillBundle {
	return &driverv1.LearnedSkillBundle{Name: session.ToValidUTF8(b.Name), Description: session.ToValidUTF8(b.Description), Body: session.ToValidUTF8(b.Body)}
}
func skillBundleFromProto(b *driverv1.LearnedSkillBundle) learning.SkillBundle {
	if b == nil {
		return learning.SkillBundle{}
	}
	return learning.SkillBundle{Name: b.GetName(), Description: b.GetDescription(), Body: b.GetBody()}
}

func skillProvenanceToProto(p learning.SkillProvenance) *driverv1.LearnedSkillProvenance {
	out := &driverv1.LearnedSkillProvenance{Origin: string(p.Origin), ValidationDisposition: string(p.ValidationDisposition), ProposalIds: make([]string, len(p.ProposalIDs)), EvidenceRefs: make([]*driverv1.LearnedSkillEvidenceRef, len(p.EvidenceRefs)), Signals: make([]*driverv1.LearnedSkillSignal, len(p.Signals))}
	for i, id := range p.ProposalIDs {
		out.ProposalIds[i] = string(id)
	}
	for i, ref := range p.EvidenceRefs {
		out.EvidenceRefs[i] = skillEvidenceToProto(ref)
	}
	for i, signal := range p.Signals {
		wire := &driverv1.LearnedSkillSignal{Kind: string(signal.Kind), Evidence: make([]*driverv1.LearnedSkillEvidenceRef, len(signal.Evidence))}
		for j, ref := range signal.Evidence {
			wire.Evidence[j] = skillEvidenceToProto(ref)
		}
		out.Signals[i] = wire
	}
	return out
}
func skillProvenanceFromProto(p *driverv1.LearnedSkillProvenance) learning.SkillProvenance {
	if p == nil {
		return learning.SkillProvenance{}
	}
	out := learning.SkillProvenance{Origin: learning.SkillProvenanceOrigin(p.GetOrigin()), ValidationDisposition: learning.ValidationDisposition(p.GetValidationDisposition()), ProposalIDs: make([]learning.ProposalID, len(p.GetProposalIds())), EvidenceRefs: make([]learning.EvidenceRef, len(p.GetEvidenceRefs())), Signals: make([]learning.Signal, len(p.GetSignals()))}
	for i, id := range p.GetProposalIds() {
		out.ProposalIDs[i] = learning.ProposalID(id)
	}
	for i, ref := range p.GetEvidenceRefs() {
		out.EvidenceRefs[i] = skillEvidenceFromProto(ref)
	}
	for i, signal := range p.GetSignals() {
		out.Signals[i] = learning.Signal{Kind: learning.SignalKind(signal.GetKind()), Evidence: make([]learning.EvidenceRef, len(signal.GetEvidence()))}
		for j, ref := range signal.GetEvidence() {
			out.Signals[i].Evidence[j] = skillEvidenceFromProto(ref)
		}
	}
	return out
}
func skillEvidenceToProto(ref learning.EvidenceRef) *driverv1.LearnedSkillEvidenceRef {
	out := &driverv1.LearnedSkillEvidenceRef{SessionId: string(ref.SessionID), Locator: string(ref.Locator), Ordinal: int32(ref.Ordinal), ToolCallId: string(ref.ToolCallID), Digest: ref.Digest} // #nosec G115 -- validated evidence ordinal is bounded by source input
	if ref.EventSeq != nil {
		value := *ref.EventSeq
		out.EventSeq = &value
	}
	return out
}
func skillEvidenceFromProto(ref *driverv1.LearnedSkillEvidenceRef) learning.EvidenceRef {
	if ref == nil {
		return learning.EvidenceRef{}
	}
	out := learning.EvidenceRef{SessionID: session.SessionID(ref.GetSessionId()), Locator: learning.EvidenceLocator(ref.GetLocator()), Ordinal: int(ref.GetOrdinal()), ToolCallID: session.ToolCallID(ref.GetToolCallId()), Digest: ref.GetDigest()}
	if ref.EventSeq != nil {
		value := ref.GetEventSeq()
		out.EventSeq = &value
	}
	return out
}

func skillEvaluationToProto(e learning.SkillEvaluation) *driverv1.LearnedSkillEvaluation {
	return &driverv1.LearnedSkillEvaluation{Verdict: string(e.Verdict), FixtureIds: append([]string(nil), e.FixtureIDs...), Baseline: session.ToValidUTF8(e.Baseline), Treatment: session.ToValidUTF8(e.Treatment), Reason: session.ToValidUTF8(e.Reason), At: skillTimeToProto(e.At)}
}
func skillEvaluationFromProto(e *driverv1.LearnedSkillEvaluation) learning.SkillEvaluation {
	if e == nil {
		return learning.SkillEvaluation{}
	}
	return learning.SkillEvaluation{Verdict: learning.EvaluationVerdict(e.GetVerdict()), FixtureIDs: append([]string(nil), e.GetFixtureIds()...), Baseline: e.GetBaseline(), Treatment: e.GetTreatment(), Reason: e.GetReason(), At: skillTimeFromProto(e.GetAt())}
}
func skillReceiptToProto(r learning.SkillReceipt) *driverv1.LearnedSkillReceipt {
	return &driverv1.LearnedSkillReceipt{Operation: session.ToValidUTF8(r.Operation), From: string(r.From), To: string(r.To), Version: string(r.Version), At: skillTimeToProto(r.At)}
}
func skillReceiptFromProto(r *driverv1.LearnedSkillReceipt) learning.SkillReceipt {
	if r == nil {
		return learning.SkillReceipt{}
	}
	return learning.SkillReceipt{Operation: r.GetOperation(), From: learning.SkillState(r.GetFrom()), To: learning.SkillState(r.GetTo()), Version: learning.VersionID(r.GetVersion()), At: skillTimeFromProto(r.GetAt())}
}

func skillVersionToProto(v learning.SkillVersion) *driverv1.LearnedSkillVersion {
	out := &driverv1.LearnedSkillVersion{Id: string(v.ID), Version: string(v.Version), Revision: string(v.Revision), State: string(v.State), OwnerAgent: session.ToValidUTF8(v.OwnerAgent), Partition: skillPartitionToProto(v.Partition), Bundle: skillBundleToProto(v.Bundle), Provenance: skillProvenanceToProto(v.Provenance), Disposition: string(v.Disposition), Evaluations: make([]*driverv1.LearnedSkillEvaluation, len(v.Evaluations)), Receipts: make([]*driverv1.LearnedSkillReceipt, len(v.Receipts)), Supersedes: string(v.Supersedes), CreatedAt: skillTimeToProto(v.CreatedAt), UpdatedAt: skillTimeToProto(v.UpdatedAt)}
	for i, e := range v.Evaluations {
		out.Evaluations[i] = skillEvaluationToProto(e)
	}
	for i, receipt := range v.Receipts {
		out.Receipts[i] = skillReceiptToProto(receipt)
	}
	return out
}
func skillVersionFromProto(v *driverv1.LearnedSkillVersion) (learning.SkillVersion, error) {
	if v == nil {
		return learning.SkillVersion{}, errors.New("grpcdriver: skill response omitted version")
	}
	out := learning.SkillVersion{ID: learning.SkillID(v.GetId()), Version: learning.VersionID(v.GetVersion()), Revision: learning.Revision(v.GetRevision()), State: learning.SkillState(v.GetState()), OwnerAgent: v.GetOwnerAgent(), Partition: skillPartitionFromProto(v.GetPartition()), Bundle: skillBundleFromProto(v.GetBundle()), Provenance: skillProvenanceFromProto(v.GetProvenance()), Disposition: learning.ValidationDisposition(v.GetDisposition()), Evaluations: make([]learning.SkillEvaluation, len(v.GetEvaluations())), Receipts: make([]learning.SkillReceipt, len(v.GetReceipts())), Supersedes: learning.VersionID(v.GetSupersedes()), CreatedAt: skillTimeFromProto(v.GetCreatedAt()), UpdatedAt: skillTimeFromProto(v.GetUpdatedAt())}
	for i, e := range v.GetEvaluations() {
		out.Evaluations[i] = skillEvaluationFromProto(e)
	}
	for i, receipt := range v.GetReceipts() {
		out.Receipts[i] = skillReceiptFromProto(receipt)
	}
	if err := validateWireSkillVersion(out); err != nil {
		return learning.SkillVersion{}, fmt.Errorf("grpcdriver: invalid skill version: %w", err)
	}
	return out, nil
}
func validateWireSkillVersion(v learning.SkillVersion) error {
	if v.ID == "" || v.Version == "" || v.Revision == "" || !v.State.Valid() || v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() || len(v.Evaluations) > learning.MaxSkillEvaluations || len(v.Receipts) > learning.MaxSkillReceipts {
		return learning.ErrInvalidSkill
	}
	if err := learning.ValidateSkillPartition(v.Partition, v.OwnerAgent); err != nil {
		return err
	}
	if err := learning.ValidateSkillBundle(v.Bundle); err != nil {
		return err
	}
	if err := learning.ValidateSkillProvenance(v.Provenance); err != nil {
		return err
	}
	for _, e := range v.Evaluations {
		if err := learning.ValidateSkillEvaluation(e); err != nil {
			return err
		}
	}
	for _, receipt := range v.Receipts {
		if receipt.Operation == "" || !receipt.To.Valid() || receipt.Version == "" {
			return learning.ErrInvalidSkill
		}
	}
	return nil
}
func skillTimeToProto(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}
func skillTimeFromProto(value *timestamppb.Timestamp) time.Time {
	if value == nil || value.CheckValid() != nil {
		return time.Time{}
	}
	return value.AsTime()
}

func skillStatusToErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	st, ok := status.FromError(err)
	if !ok {
		return errors.New("grpcdriver: skill repository driver request failed")
	}
	for _, detail := range st.Details() {
		if typed, ok := detail.(*driverv1.SkillRepositoryErrorDetail); ok {
			if sentinel := skillErrorSentinel(typed.GetCode()); sentinel != nil {
				return sentinel
			}
		}
	}
	return status.Error(st.Code(), "skill repository driver request failed")
}
func skillErrorSentinel(code driverv1.SkillRepositoryErrorCode) error {
	switch code {
	case driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_INVALID:
		return learning.ErrInvalidSkill
	case driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_NOT_FOUND:
		return learning.ErrSkillNotFound
	case driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_CONFLICT:
		return learning.ErrSkillConflict
	case driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_TRANSITION:
		return learning.ErrSkillTransition
	case driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_LIMIT:
		return learning.ErrSkillLimit
	case driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_OWNER_MISMATCH:
		return learning.ErrSkillOwnerMismatch
	case driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_NAME_COLLISION:
		return learning.ErrSkillNameCollision
	case driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_CURSOR:
		return learning.ErrSkillCursor
	default:
		return nil
	}
}
func skillErrorCode(err error) (driverv1.SkillRepositoryErrorCode, codes.Code) {
	switch {
	case errors.Is(err, learning.ErrInvalidSkill):
		return driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_INVALID, codes.InvalidArgument
	case errors.Is(err, learning.ErrSkillNotFound):
		return driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_NOT_FOUND, codes.NotFound
	case errors.Is(err, learning.ErrSkillConflict):
		return driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_CONFLICT, codes.Aborted
	case errors.Is(err, learning.ErrSkillTransition):
		return driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_TRANSITION, codes.FailedPrecondition
	case errors.Is(err, learning.ErrSkillLimit):
		return driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_LIMIT, codes.ResourceExhausted
	case errors.Is(err, learning.ErrSkillOwnerMismatch):
		return driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_OWNER_MISMATCH, codes.PermissionDenied
	case errors.Is(err, learning.ErrSkillNameCollision):
		return driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_NAME_COLLISION, codes.AlreadyExists
	case errors.Is(err, learning.ErrSkillCursor):
		return driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_CURSOR, codes.InvalidArgument
	default:
		return driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_UNSPECIFIED, codes.Internal
	}
}
func skillRepositoryStatus(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "skill repository operation cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "skill repository operation timed out")
	}
	code, grpcCode := skillErrorCode(err)
	if code == driverv1.SkillRepositoryErrorCode_SKILL_REPOSITORY_ERROR_CODE_UNSPECIFIED {
		return status.Error(grpcCode, "skill repository operation failed")
	}
	st := status.New(grpcCode, skillErrorSentinel(code).Error())
	withDetails, detailErr := st.WithDetails(&driverv1.SkillRepositoryErrorDetail{Code: code})
	if detailErr != nil {
		return status.Error(codes.Internal, "skill repository operation failed")
	}
	return withDetails.Err()
}
