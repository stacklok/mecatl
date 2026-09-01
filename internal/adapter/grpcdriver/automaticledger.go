package grpcdriver

//revive:disable:exported // methods implement the documented AutomaticAdmissionLedger contract

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/learning"
)

// AutomaticAdmissionLedger adapts distributed automatic accounting to a
// remote infrastructure driver.
type AutomaticAdmissionLedger struct {
	client driverv1.AutomaticAdmissionLedgerServiceClient
}

var _ learning.AutomaticAdmissionLedger = (*AutomaticAdmissionLedger)(nil)

// NewAutomaticAdmissionLedger wraps an established driver connection.
func NewAutomaticAdmissionLedger(conn grpc.ClientConnInterface) *AutomaticAdmissionLedger {
	return &AutomaticAdmissionLedger{client: driverv1.NewAutomaticAdmissionLedgerServiceClient(conn)}
}

func (l *AutomaticAdmissionLedger) Reserve(ctx context.Context, req learning.AutomaticReservationRequest) (learning.AutomaticReservation, error) {
	resp, err := l.client.ReserveAutomaticAdmission(ctx, automaticRequestToProto(req))
	return automaticResponse(ctx, resp, err)
}

func (l *AutomaticAdmissionLedger) Get(ctx context.Context, id learning.AutomaticReservationID) (learning.AutomaticReservation, bool, error) {
	resp, err := l.client.GetAutomaticReservation(ctx, &driverv1.GetAutomaticReservationRequest{Id: string(id)})
	if err != nil {
		return learning.AutomaticReservation{}, false, automaticStatusToErr(ctx, err)
	}
	if !resp.GetFound() {
		if resp.GetReservation() != nil {
			return learning.AutomaticReservation{}, false, errors.New("grpcdriver: automatic reservation miss included a record")
		}
		return learning.AutomaticReservation{}, false, nil
	}
	record, err := automaticReservationFromProto(resp.GetReservation())
	return record, err == nil, err
}

func (l *AutomaticAdmissionLedger) Reassign(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion) (learning.AutomaticReservation, error) {
	resp, err := l.client.ReassignAutomaticReservation(ctx, &driverv1.ReassignAutomaticReservationRequest{
		Id: string(id), ExpectedVersion: string(expected),
	})
	return automaticResponse(ctx, resp, err)
}

func (l *AutomaticAdmissionLedger) Retain(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fence learning.AutomaticReservationFence) (learning.AutomaticReservation, error) {
	resp, err := l.client.RetainAutomaticReservation(ctx, automaticResolveToProto(id, expected, fence))
	return automaticResponse(ctx, resp, err)
}

func (l *AutomaticAdmissionLedger) Reclaim(ctx context.Context, id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fence learning.AutomaticReservationFence) (learning.AutomaticReservation, error) {
	resp, err := l.client.ReclaimAutomaticReservation(ctx, automaticResolveToProto(id, expected, fence))
	return automaticResponse(ctx, resp, err)
}

func automaticRequestToProto(req learning.AutomaticReservationRequest) *driverv1.AutomaticReservationRequest {
	return &driverv1.AutomaticReservationRequest{
		Id: string(req.ID), AttemptId: string(req.AttemptID), Principal: string(req.Principal), Digest: string(req.Digest),
		AdmissionClass: string(req.Class), Tokens: req.Tokens, ExpectedPolicyRevision: string(req.ExpectedPolicyRevision),
	}
}

func automaticResolveToProto(id learning.AutomaticReservationID, expected learning.AutomaticReservationVersion, fence learning.AutomaticReservationFence) *driverv1.ResolveAutomaticReservationRequest {
	wireFence := automaticFenceToProto(fence)
	if wireFence == nil {
		wireFence = &driverv1.AutomaticReservationFence{}
	}
	return &driverv1.ResolveAutomaticReservationRequest{
		Id: string(id), ExpectedVersion: string(expected), Fence: wireFence,
	}
}

func automaticFenceToProto(fence learning.AutomaticReservationFence) *driverv1.AutomaticReservationFence {
	if !fence.Valid() {
		return nil
	}
	return &driverv1.AutomaticReservationFence{Generation: uint64(fence.Generation), ExpiresAt: timestamppb.New(fence.ExpiresAt)}
}

func automaticFenceFromProto(wire *driverv1.AutomaticReservationFence, required bool) (learning.AutomaticReservationFence, error) {
	if wire == nil {
		if required {
			return learning.AutomaticReservationFence{}, learning.ErrInvalidAutomaticReservation
		}
		return learning.AutomaticReservationFence{}, nil
	}
	if wire.GetGeneration() == 0 && wire.GetExpiresAt() == nil {
		return learning.AutomaticReservationFence{}, nil
	}
	expires, err := automaticTime(wire.GetExpiresAt())
	if err != nil {
		return learning.AutomaticReservationFence{}, err
	}
	fence := learning.AutomaticReservationFence{Generation: learning.AutomaticReservationGeneration(wire.GetGeneration()), ExpiresAt: expires}
	if !fence.Valid() {
		return learning.AutomaticReservationFence{}, learning.ErrInvalidAutomaticReservation
	}
	return fence, nil
}

func automaticReservationToProto(r learning.AutomaticReservation) *driverv1.AutomaticReservation {
	return &driverv1.AutomaticReservation{
		Id: string(r.ID), AttemptId: string(r.AttemptID), Version: string(r.Version), Principal: string(r.Principal), Digest: string(r.Digest),
		AdmissionClass: string(r.Class), PolicyRevision: string(r.PolicyRevision), Tokens: r.Tokens, Charge: string(r.Charge), AttemptCreated: r.AttemptCreated,
		ReservedAt: timestamppb.New(r.ReservedAt), ChargeExpiresAt: timestamppb.New(r.ChargeExpiresAt), DedupeExpiresAt: timestamppb.New(r.DedupeExpiresAt),
		ClaimDuration: durationpb.New(r.ClaimDuration), Fence: automaticFenceToProto(r.Fence),
	}
}

func automaticReservationFromProto(wire *driverv1.AutomaticReservation) (learning.AutomaticReservation, error) {
	if wire == nil || wire.GetClaimDuration() == nil || wire.GetClaimDuration().CheckValid() != nil {
		return learning.AutomaticReservation{}, errors.New("grpcdriver: invalid automatic reservation")
	}
	reserved, err := automaticTime(wire.GetReservedAt())
	if err != nil {
		return learning.AutomaticReservation{}, err
	}
	chargeExpires, err := automaticTime(wire.GetChargeExpiresAt())
	if err != nil {
		return learning.AutomaticReservation{}, err
	}
	dedupeExpires, err := automaticTime(wire.GetDedupeExpiresAt())
	if err != nil {
		return learning.AutomaticReservation{}, err
	}
	fence, err := automaticFenceFromProto(wire.GetFence(), false)
	if err != nil {
		return learning.AutomaticReservation{}, err
	}
	r := learning.AutomaticReservation{
		ID: learning.AutomaticReservationID(wire.GetId()), AttemptID: learning.AttemptID(wire.GetAttemptId()), Version: learning.AutomaticReservationVersion(wire.GetVersion()),
		Principal: learning.AttemptPartition(wire.GetPrincipal()), Digest: learning.CanonicalDigest(wire.GetDigest()), Class: learning.AdmissionClass(wire.GetAdmissionClass()),
		PolicyRevision: learning.AutomaticAdmissionPolicyRevision(wire.GetPolicyRevision()), Tokens: wire.GetTokens(), Charge: learning.AutomaticChargeDisposition(wire.GetCharge()), AttemptCreated: wire.GetAttemptCreated(), ReservedAt: reserved,
		ChargeExpiresAt: chargeExpires, DedupeExpiresAt: dedupeExpires, ClaimDuration: wire.GetClaimDuration().AsDuration(), Fence: fence,
	}
	if err = r.Validate(); err != nil {
		return learning.AutomaticReservation{}, fmt.Errorf("grpcdriver: invalid automatic reservation: %w", err)
	}
	return r, nil
}

func automaticTime(value *timestamppb.Timestamp) (time.Time, error) {
	if value == nil || value.CheckValid() != nil {
		return time.Time{}, learning.ErrInvalidAutomaticReservation
	}
	return value.AsTime(), nil
}

func automaticResponse(ctx context.Context, resp *driverv1.AutomaticReservationResponse, err error) (learning.AutomaticReservation, error) {
	if err != nil {
		return learning.AutomaticReservation{}, automaticStatusToErr(ctx, err)
	}
	return automaticReservationFromProto(resp.GetReservation())
}

func automaticStatusToErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	st, ok := status.FromError(err)
	if !ok {
		return errors.New("grpcdriver: automatic ledger driver request failed")
	}
	for _, detail := range st.Details() {
		if typed, ok := detail.(*driverv1.AutomaticLedgerErrorDetail); ok {
			if sentinel := automaticErrorSentinel(typed.GetCode()); sentinel != nil {
				return sentinel
			}
		}
	}
	return status.Error(st.Code(), "automatic ledger driver request failed")
}

func automaticErrorSentinel(code driverv1.AutomaticLedgerErrorCode) error {
	switch code {
	case driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_INVALID:
		return learning.ErrInvalidAutomaticReservation
	case driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_DUPLICATE:
		return learning.ErrAutomaticAdmissionDuplicate
	case driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_COOLDOWN:
		return learning.ErrAutomaticAdmissionCooldown
	case driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_LIMIT:
		return learning.ErrAutomaticAdmissionLimit
	case driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_IDENTITY_CONFLICT:
		return learning.ErrAutomaticReservationConflict
	case driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_NOT_FOUND:
		return learning.ErrAutomaticReservationNotFound
	case driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_VERSION_CONFLICT:
		return learning.ErrAutomaticReservationVersion
	case driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_FENCE_LOST:
		return learning.ErrAutomaticReservationFence
	case driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_STATE:
		return learning.ErrAutomaticReservationState
	default:
		return nil
	}
}

func automaticErrorCode(err error) (driverv1.AutomaticLedgerErrorCode, codes.Code) {
	checks := []struct {
		err  error
		code driverv1.AutomaticLedgerErrorCode
		grpc codes.Code
	}{
		{learning.ErrInvalidAutomaticReservation, driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_INVALID, codes.InvalidArgument},
		{learning.ErrAutomaticAdmissionDuplicate, driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_DUPLICATE, codes.AlreadyExists},
		{learning.ErrAutomaticAdmissionCooldown, driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_COOLDOWN, codes.ResourceExhausted},
		{learning.ErrAutomaticAdmissionLimit, driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_LIMIT, codes.ResourceExhausted},
		{learning.ErrAutomaticReservationConflict, driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_IDENTITY_CONFLICT, codes.AlreadyExists},
		{learning.ErrAutomaticReservationNotFound, driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_NOT_FOUND, codes.NotFound},
		{learning.ErrAutomaticReservationVersion, driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_VERSION_CONFLICT, codes.Aborted},
		{learning.ErrAutomaticReservationFence, driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_FENCE_LOST, codes.OutOfRange},
		{learning.ErrAutomaticReservationState, driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_STATE, codes.FailedPrecondition},
	}
	for _, check := range checks {
		if errors.Is(err, check.err) {
			return check.code, check.grpc
		}
	}
	return driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_UNSPECIFIED, codes.Internal
}

func automaticRepositoryStatus(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "automatic ledger operation cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "automatic ledger operation timed out")
	}
	code, grpcCode := automaticErrorCode(err)
	if code == driverv1.AutomaticLedgerErrorCode_AUTOMATIC_LEDGER_ERROR_CODE_UNSPECIFIED {
		return status.Error(grpcCode, "automatic ledger operation failed")
	}
	st := status.New(grpcCode, automaticErrorSentinel(code).Error())
	withDetails, detailErr := st.WithDetails(&driverv1.AutomaticLedgerErrorDetail{Code: code})
	if detailErr != nil {
		return status.Error(codes.Internal, "automatic ledger operation failed")
	}
	return withDetails.Err()
}
