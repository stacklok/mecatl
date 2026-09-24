package mcpbrokergrpc

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

const maxContinuityRequestIDBytes = 128

func continuityGuardFromWire(in *brokerv1.ContinuityGuard) (mcpbroker.ContinuityGuard, error) {
	return continuityGuardFromWireMode(in, true)
}

func stageContinuityGuardFromWire(in *brokerv1.ContinuityGuard) (mcpbroker.ContinuityGuard, error) {
	return continuityGuardFromWireMode(in, false)
}

func continuityGuardFromWireMode(in *brokerv1.ContinuityGuard, requireProfile bool) (mcpbroker.ContinuityGuard, error) {
	if in == nil || !mcpbroker.ValidLogicalSessionID(session.SessionID(in.GetSessionId())) {
		return mcpbroker.ContinuityGuard{}, invalid("malformed continuity guard")
	}
	incarnation := session.IncarnationID(in.GetSessionIncarnation())
	if !incarnation.Valid() || len(in.GetOwnerPartition()) != 32 || len(in.GetWorkloadPartition()) != 32 || (requireProfile && (len(in.GetProfileDigest()) != 32 || len(in.GetProviders()) == 0 || len(in.GetProviders()) > mcpbroker.MaxContinuityProviders)) || (!requireProfile && (len(in.GetProfileDigest()) != 0 || len(in.GetProviders()) != 0)) {
		return mcpbroker.ContinuityGuard{}, invalid("malformed continuity guard")
	}
	out := mcpbroker.ContinuityGuard{SessionID: session.SessionID(in.GetSessionId()), SessionIncarnation: incarnation, Providers: append([]string(nil), in.GetProviders()...)}
	copy(out.OwnerPartition[:], in.GetOwnerPartition())
	copy(out.WorkloadPartition[:], in.GetWorkloadPartition())
	copy(out.ProfileDigest[:], in.GetProfileDigest())
	if zeroPartition(out.OwnerPartition) || zeroPartition(out.WorkloadPartition) || (requireProfile && zeroPartition(out.ProfileDigest)) {
		return mcpbroker.ContinuityGuard{}, invalid("malformed continuity guard")
	}
	for i, provider := range out.Providers {
		if !mcpbroker.ValidContinuityProvider(provider) || (i > 0 && provider <= out.Providers[i-1]) {
			return mcpbroker.ContinuityGuard{}, invalid("malformed continuity guard")
		}
	}
	return out, nil
}

func zeroPartition(value [32]byte) bool {
	return subtle.ConstantTimeCompare(value[:], make([]byte, 32)) == 1
}

func continuityAssertionFromWire(now time.Time, in *brokerv1.CustodyAssertion) (mcpbroker.CustodyAssertion, error) {
	if in == nil || in.GetAttemptDeadline() == nil || !in.GetAttemptDeadline().IsValid() || !validRecoveryReference(in.GetRecoveryReference()) {
		return mcpbroker.CustodyAssertion{}, invalid("malformed continuity assertion")
	}
	guard, err := continuityGuardFromWire(in.GetGuard())
	if err != nil {
		return mcpbroker.CustodyAssertion{}, err
	}
	deadline := in.GetAttemptDeadline().AsTime()
	if !mcpbroker.ValidContinuityAttemptDeadline(now, deadline) {
		return mcpbroker.CustodyAssertion{}, invalid("malformed continuity assertion")
	}
	return mcpbroker.CustodyAssertion{Guard: guard, RecoveryReference: in.GetRecoveryReference(), AttemptDeadline: deadline}, nil
}

func validRecoveryReference(value string) bool {
	if len(value) != session.BrokerRecoveryReferenceBytes || !utf8.ValidString(value) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}
func validContinuityRequestID(value string) bool {
	if value == "" || len(value) > maxContinuityRequestIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
func (s *Server) verifyContinuityPeer(ctx context.Context, guard mcpbroker.ContinuityGuard) error {
	principal := session.PrincipalFromContext(ctx)
	if principal == nil {
		return status.Error(codes.PermissionDenied, "broker session is not available")
	}
	partition, err := mcpbroker.ContinuityPrincipalPartition(mcpbroker.ContinuityPartitionWorkload, principal)
	if err != nil || subtle.ConstantTimeCompare(partition[:], guard.WorkloadPartition[:]) != 1 {
		return status.Error(codes.PermissionDenied, "broker session is not available")
	}
	return nil
}

func continuityReceiptKeyFor(ctx context.Context, operation, requestID string) (continuityReceiptKey, error) {
	principal := session.PrincipalFromContext(ctx)
	partition, err := mcpbroker.ContinuityPrincipalPartition(mcpbroker.ContinuityPartitionWorkload, principal)
	if err != nil {
		return continuityReceiptKey{}, status.Error(codes.PermissionDenied, "broker session is not available")
	}
	return continuityReceiptKey{workload: partition, operation: operation, request: requestID}, nil
}

func stageReceiptIdentity(ctx context.Context, req *brokerv1.StageCredentialCustodyRequest, guard mcpbroker.ContinuityGuard, ref mcpbroker.WorkspaceEnrollmentRef) (continuityReceiptKey, [32]byte, error) {
	key, err := continuityReceiptKeyFor(ctx, "stage/v1", req.GetRequestId())
	if err != nil {
		return continuityReceiptKey{}, [32]byte{}, err
	}
	parts := continuityGuardParts([3][32]byte{guard.OwnerPartition, guard.WorkloadPartition}, string(guard.SessionID), string(guard.SessionIncarnation), nil)
	parts = append(parts, []byte("stage/v1"), []byte(req.GetBrokerIncarnation()), []byte(req.GetHandle()), []byte(ref.ID), continuityUint32Part(ref.RequiredServices), continuityTimePart(ref.ExpiresAt), continuityTimePart(req.GetAttemptDeadline().AsTime()))
	return key, continuityDigest(parts...), nil
}

func recoverReceiptIdentity(ctx context.Context, req *brokerv1.RecoverCredentialAttachmentRequest, assertion mcpbroker.CustodyAssertion) (continuityReceiptKey, [32]byte, error) {
	key, err := continuityReceiptKeyFor(ctx, "recover/v1", req.GetRequestId())
	if err != nil {
		return continuityReceiptKey{}, [32]byte{}, err
	}
	guard := assertion.Guard
	parts := continuityGuardParts([3][32]byte{guard.OwnerPartition, guard.WorkloadPartition, guard.ProfileDigest}, string(guard.SessionID), string(guard.SessionIncarnation), guard.Providers)
	parts = append(parts, []byte("recover/v1"), []byte(assertion.RecoveryReference), continuityTimePart(assertion.AttemptDeadline))
	return key, continuityDigest(parts...), nil
}

func continuityAttemptContext(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		return context.WithDeadline(ctx, parent)
	}
	return context.WithDeadline(ctx, deadline)
}

func (s *Server) StageCredentialCustody(ctx context.Context, req *brokerv1.StageCredentialCustodyRequest) (*brokerv1.StageCredentialCustodyResponse, error) {
	if !validContinuityRequestID(req.GetRequestId()) {
		return nil, invalid("malformed continuity request")
	}
	guard, err := stageContinuityGuardFromWire(req.GetGuard())
	if err != nil {
		return nil, err
	}
	if err = s.verifyContinuityPeer(ctx, guard); err != nil {
		return nil, err
	}
	deadline := req.GetAttemptDeadline()
	if deadline == nil || !deadline.IsValid() || !mcpbroker.ValidContinuityAttemptDeadline(s.clock.Now(), deadline.AsTime()) {
		return nil, invalid("malformed continuity request")
	}
	ref, err := workspaceRefFromWire(req.GetCompletedEnrollment())
	if err != nil {
		return nil, err
	}
	ctx, cancel := continuityAttemptContext(ctx, deadline.AsTime())
	defer cancel()
	a, release, callErr := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
	if callErr != nil {
		return nil, callErr
	}
	defer release()
	if a.logicalID != guard.SessionID || a.binding == "" || a.binding != string(a.sessionHandle.Binding()) {
		return nil, continuityUnavailable()
	}
	stager, ok := a.sessionHandle.(mcpbroker.CredentialCustodyStager)
	if !ok {
		return nil, continuityUnavailable()
	}
	key, digest, err := stageReceiptIdentity(ctx, req, guard, ref)
	if err != nil {
		return nil, err
	}
	receipt, leader, err := s.reserveContinuityReceipt(key, digest, deadline.AsTime(), stageReceiptReservationBytes)
	if err != nil {
		return nil, err
	}
	if !leader {
		if err := s.awaitContinuityReceipt(ctx, receipt); err != nil {
			return nil, err
		}
		return cloneStageReceipt(receipt.stage), receipt.err
	}
	if err := ctx.Err(); err != nil {
		callErr := status.FromContextError(err).Err()
		s.discardContinuityReceipt(key, receipt, callErr)
		return nil, callErr
	}

	var response *brokerv1.StageCredentialCustodyResponse
	receiptExpiry := deadline.AsTime()
	if out, stageErr := stager.StageCredentialCustody(ctx, req.GetRequestId(), guard, ref, deadline.AsTime()); stageErr != nil {
		callErr = brokerStatus(stageErr)
	} else if !validRecoveryReference(out.RecoveryReference) || out.ExpiresAt.IsZero() || zeroPartition(out.ProfileDigest) || len(out.Providers) == 0 || len(out.Providers) > mcpbroker.MaxContinuityProviders {
		callErr = continuityUnavailable()
	} else {
		for i, provider := range out.Providers {
			if !mcpbroker.ValidContinuityProvider(provider) || (i > 0 && provider <= out.Providers[i-1]) {
				callErr = continuityUnavailable()
				break
			}
		}
		if callErr == nil {
			response = &brokerv1.StageCredentialCustodyResponse{RecoveryReference: out.RecoveryReference, CustodyExpiresAt: timestamppb.New(out.ExpiresAt), ProfileDigest: append([]byte(nil), out.ProfileDigest[:]...), Providers: append([]string(nil), out.Providers...)}
		}
		if proto.Size(response) > stageReceiptReservationBytes {
			response = nil
			callErr = continuityUnavailable()
		} else if out.ExpiresAt.Before(receiptExpiry) {
			receiptExpiry = out.ExpiresAt
		}
	}
	s.finishContinuityReceipt(key, receipt, response, nil, callErr, receiptExpiry)
	return response, callErr
}

func (s *Server) CommitCredentialCustody(ctx context.Context, req *brokerv1.CommitCredentialCustodyRequest) (*brokerv1.CommitCredentialCustodyResponse, error) {
	assertion, err := continuityAssertionFromWire(time.Now(), req.GetAssertion())
	if err != nil {
		return nil, err
	}
	if err = s.verifyContinuityPeer(ctx, assertion.Guard); err != nil {
		return nil, err
	}
	ctx, cancel := continuityAttemptContext(ctx, assertion.AttemptDeadline)
	defer cancel()
	service, ok := s.service.(mcpbroker.CredentialContinuityService)
	if !ok {
		return nil, continuityUnavailable()
	}
	if err = service.CommitCredentialCustody(ctx, assertion); err != nil {
		return nil, brokerStatus(err)
	}
	return &brokerv1.CommitCredentialCustodyResponse{}, nil
}

func (s *Server) RecoverCredentialAttachment(ctx context.Context, req *brokerv1.RecoverCredentialAttachmentRequest) (*brokerv1.RecoverCredentialAttachmentResponse, error) {
	if !validContinuityRequestID(req.GetRequestId()) {
		return nil, invalid("malformed continuity request")
	}
	assertion, err := continuityAssertionFromWire(time.Now(), req.GetAssertion())
	if err != nil {
		return nil, err
	}
	if err = s.verifyContinuityPeer(ctx, assertion.Guard); err != nil {
		return nil, err
	}
	ctx, cancel := continuityAttemptContext(ctx, assertion.AttemptDeadline)
	defer cancel()
	service, ok := s.service.(mcpbroker.CredentialContinuityService)
	if !ok {
		return nil, continuityUnavailable()
	}
	key, digest, err := recoverReceiptIdentity(ctx, req, assertion)
	if err != nil {
		return nil, err
	}
	receipt, leader, err := s.reserveContinuityReceipt(key, digest, assertion.AttemptDeadline, recoverReceiptReservationBytes)
	if err != nil {
		return nil, err
	}
	if !leader {
		if err := s.awaitContinuityReceipt(ctx, receipt); err != nil {
			return nil, err
		}
		return cloneRecoverReceipt(receipt.recover), receipt.err
	}
	if err := ctx.Err(); err != nil {
		callErr := status.FromContextError(err).Err()
		s.discardContinuityReceipt(key, receipt, callErr)
		return nil, callErr
	}

	var response *brokerv1.RecoverCredentialAttachmentResponse
	var recoveredAttachment mcpbroker.SessionHandle
	var publishedHandle string
	var publishedOwner *sessionOwner
	var callErr error
	if out, recoverErr := service.RecoverCredentialAttachment(ctx, assertion, req.GetRequestId()); recoverErr != nil {
		callErr = brokerStatus(recoverErr)
	} else if out.Attachment == nil {
		callErr = continuityUnavailable()
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		s.discardUnpublishedHandle(out.Attachment, mcpbroker.AttachRecoveredProvisional)
		callErr = status.FromContextError(ctxErr).Err()
	} else if desc, tools, descriptorErr := descriptors(out.Attachment.Tools()); descriptorErr != nil {
		s.discardUnpublishedHandle(out.Attachment, mcpbroker.AttachRecoveredProvisional)
		callErr = invalid("invalid recovered attachment")
	} else if handle, handleErr := newHandle(); handleErr != nil {
		s.discardUnpublishedHandle(out.Attachment, mcpbroker.AttachRecoveredProvisional)
		callErr = status.Error(codes.Internal, "mint attachment handle")
	} else if principal, owner, bindErr := s.bindSession(ctx, assertion.Guard.SessionID); bindErr != nil {
		s.discardUnpublishedHandle(out.Attachment, mcpbroker.AttachRecoveredProvisional)
		callErr = bindErr
	} else {
		s.mu.Lock()
		if ctxErr := ctx.Err(); ctxErr != nil {
			s.mu.Unlock()
			s.finishSessionBind(assertion.Guard.SessionID, owner, false)
			s.discardUnpublishedHandle(out.Attachment, mcpbroker.AttachRecoveredProvisional)
			callErr = status.FromContextError(ctxErr).Err()
		} else if s.closed || len(s.handles) >= s.cfg.MaxHandles {
			s.mu.Unlock()
			s.finishSessionBind(assertion.Guard.SessionID, owner, false)
			s.discardUnpublishedHandle(out.Attachment, mcpbroker.AttachRecoveredProvisional)
			callErr = continuityUnavailable()
		} else {
			_, enrollment := out.Attachment.(mcpbroker.WorkspaceEnrollmentHandle)
			s.handles[handle] = &serverHandle{sessionHandle: out.Attachment, principal: principal, logicalID: assertion.Guard.SessionID, owner: owner, binding: string(out.Attachment.Binding()), tools: tools, expiresAt: time.Now().Add(s.cfg.HandleIdleTimeout), changed: make(chan struct{}), receipts: make(map[session.ToolCallID]*executeReceipt)}
			s.mu.Unlock()
			s.finishSessionBind(assertion.Guard.SessionID, owner, true)
			recoveredAttachment = out.Attachment
			publishedHandle = handle
			publishedOwner = owner
			response = &brokerv1.RecoverCredentialAttachmentResponse{Binding: string(out.Attachment.Binding()), Handle: handle, Tools: desc, BrokerIncarnation: s.instanceID, WorkspaceEnrollment: enrollment}
		}
	}
	if response != nil && proto.Size(response) > recoverReceiptReservationBytes {
		s.discardRecoveredPublication(assertion.Guard.SessionID, publishedHandle, publishedOwner)
		s.discardUnpublishedHandle(recoveredAttachment, mcpbroker.AttachRecoveredProvisional)
		response = nil
		callErr = continuityUnavailable()
	}
	s.finishContinuityReceipt(key, receipt, nil, response, callErr, assertion.AttemptDeadline)
	return response, callErr
}

// discardRecoveredPublication removes a provisional recovered handle that could
// not be safely retained for exact receipt replay before aborting it upstream.
func (s *Server) discardRecoveredPublication(id session.SessionID, handle string, owner *sessionOwner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	registered := s.handles[handle]
	if registered == nil || registered.owner != owner {
		return
	}
	delete(s.handles, handle)
	releaseOwnerHandleLocked(owner)
	if s.owners[id] == owner && owner.pending == 0 && owner.handles == 0 && !owner.published && !owner.retiring {
		delete(s.owners, id)
	}
}

func (s *Server) TombstoneCredentialCustody(ctx context.Context, req *brokerv1.TombstoneCredentialCustodyRequest) (*brokerv1.TombstoneCredentialCustodyResponse, error) {
	assertion, err := continuityAssertionFromWire(time.Now(), req.GetAssertion())
	if err != nil {
		return nil, err
	}
	if err = s.verifyContinuityPeer(ctx, assertion.Guard); err != nil {
		return nil, err
	}
	ctx, cancel := continuityAttemptContext(ctx, assertion.AttemptDeadline)
	defer cancel()
	service, ok := s.service.(mcpbroker.CredentialContinuityService)
	if !ok {
		return nil, continuityUnavailable()
	}
	if err = service.TombstoneCredentialCustody(ctx, assertion); err != nil {
		return nil, brokerStatus(err)
	}
	return &brokerv1.TombstoneCredentialCustodyResponse{}, nil
}
