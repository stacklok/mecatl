package mcpbrokergrpc

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func continuityGuardToWire(guard mcpbroker.ContinuityGuard) *brokerv1.ContinuityGuard {
	return &brokerv1.ContinuityGuard{SessionId: string(guard.SessionID), SessionIncarnation: string(guard.SessionIncarnation), OwnerPartition: append([]byte(nil), guard.OwnerPartition[:]...), WorkloadPartition: append([]byte(nil), guard.WorkloadPartition[:]...), ProfileDigest: append([]byte(nil), guard.ProfileDigest[:]...), Providers: append([]string(nil), guard.Providers...)}
}
func stageGuardToWire(guard mcpbroker.ContinuityGuard) *brokerv1.ContinuityGuard {
	return &brokerv1.ContinuityGuard{SessionId: string(guard.SessionID), SessionIncarnation: string(guard.SessionIncarnation), OwnerPartition: append([]byte(nil), guard.OwnerPartition[:]...), WorkloadPartition: append([]byte(nil), guard.WorkloadPartition[:]...)}
}

func custodyAssertionToWire(assertion mcpbroker.CustodyAssertion) *brokerv1.CustodyAssertion {
	return &brokerv1.CustodyAssertion{Guard: continuityGuardToWire(assertion.Guard), RecoveryReference: assertion.RecoveryReference, AttemptDeadline: timestamppb.New(assertion.AttemptDeadline)}
}

func (a *clientSessionHandle) StageCredentialCustody(ctx context.Context, requestID string, guard mcpbroker.ContinuityGuard, enrollment mcpbroker.WorkspaceEnrollmentRef, deadline time.Time) (mcpbroker.StagedCredentialCustody, error) {
	if !validContinuityRequestID(requestID) || !enrollment.Valid() || !mcpbroker.ValidContinuityAttemptDeadline(time.Now(), deadline) {
		return mcpbroker.StagedCredentialCustody{}, errors.New("mcpbrokergrpc: malformed continuity stage")
	}
	rpcCtx, cancel := context.WithTimeout(ctx, a.client.cfg.RPCDeadline)
	defer cancel()
	response, err := a.client.rpc.StageCredentialCustody(rpcCtx, &brokerv1.StageCredentialCustodyRequest{RequestId: requestID, Handle: a.handle, BrokerIncarnation: a.instanceID, Guard: stageGuardToWire(guard), CompletedEnrollment: workspaceRefToWire(enrollment), AttemptDeadline: timestamppb.New(deadline)})
	if err != nil {
		return mcpbroker.StagedCredentialCustody{}, continuityClientError(err)
	}
	if !validRecoveryReference(response.GetRecoveryReference()) || response.GetCustodyExpiresAt() == nil || !response.GetCustodyExpiresAt().IsValid() || len(response.GetProfileDigest()) != 32 || len(response.GetProviders()) == 0 || len(response.GetProviders()) > mcpbroker.MaxContinuityProviders {
		return mcpbroker.StagedCredentialCustody{}, errors.New("mcpbrokergrpc: malformed custody stage response")
	}
	var profileDigest [32]byte
	copy(profileDigest[:], response.GetProfileDigest())
	if profileDigest == ([32]byte{}) {
		return mcpbroker.StagedCredentialCustody{}, errors.New("mcpbrokergrpc: malformed custody stage response")
	}
	for i, provider := range response.GetProviders() {
		if !mcpbroker.ValidContinuityProvider(provider) || (i > 0 && provider <= response.GetProviders()[i-1]) {
			return mcpbroker.StagedCredentialCustody{}, errors.New("mcpbrokergrpc: malformed custody stage response")
		}
	}
	return mcpbroker.StagedCredentialCustody{RecoveryReference: response.GetRecoveryReference(), ExpiresAt: response.GetCustodyExpiresAt().AsTime(), ProfileDigest: profileDigest, Providers: append([]string(nil), response.GetProviders()...)}, nil
}

// CommitCredentialCustody sends the Commit continuity RPC to the broker.
func (c *Client) CommitCredentialCustody(ctx context.Context, assertion mcpbroker.CustodyAssertion) error {
	rpcCtx, cancel := context.WithTimeout(ctx, c.cfg.RPCDeadline)
	defer cancel()
	_, err := c.rpc.CommitCredentialCustody(rpcCtx, &brokerv1.CommitCredentialCustodyRequest{Assertion: custodyAssertionToWire(assertion)})
	return continuityClientError(err)
}

// TombstoneCredentialCustody sends the Tombstone continuity RPC to the broker.
func (c *Client) TombstoneCredentialCustody(ctx context.Context, assertion mcpbroker.CustodyAssertion) error {
	rpcCtx, cancel := context.WithTimeout(ctx, c.cfg.RPCDeadline)
	defer cancel()
	_, err := c.rpc.TombstoneCredentialCustody(rpcCtx, &brokerv1.TombstoneCredentialCustodyRequest{Assertion: custodyAssertionToWire(assertion)})
	return continuityClientError(err)
}

// RecoverCredentialAttachment sends the Recover continuity RPC to the broker.
func (c *Client) RecoverCredentialAttachment(ctx context.Context, assertion mcpbroker.CustodyAssertion, requestID string) (mcpbroker.RecoveredCredentialAttachment, error) {
	if !validContinuityRequestID(requestID) {
		return mcpbroker.RecoveredCredentialAttachment{}, errors.New("mcpbrokergrpc: malformed continuity recovery")
	}
	expected := c.brokerInstanceID()
	rpcCtx, cancel := context.WithTimeout(ctx, c.cfg.RPCDeadline)
	defer cancel()
	response, err := c.rpc.RecoverCredentialAttachment(rpcCtx, &brokerv1.RecoverCredentialAttachmentRequest{Assertion: custodyAssertionToWire(assertion), RequestId: requestID})
	if err != nil {
		return mcpbroker.RecoveredCredentialAttachment{}, continuityClientError(err)
	}
	wrapped := &brokerv1.AttachResponse{Binding: response.GetBinding(), Handle: response.GetHandle(), Tools: response.GetTools(), BrokerIncarnation: response.GetBrokerIncarnation(), Outcome: string(mcpbroker.AttachRecoveredProvisional), WorkspaceEnrollment: response.GetWorkspaceEnrollment()}
	attachment, _, err := c.attachResponse(wrapped, true, expected)
	if err != nil {
		c.discardAttachResponse(wrapped)
		return mcpbroker.RecoveredCredentialAttachment{}, err
	}
	return mcpbroker.RecoveredCredentialAttachment{Attachment: attachment}, nil
}

func continuityClientError(err error) error {
	if status.Code(err) == codes.Unimplemented {
		return mcpbroker.ErrContinuityProtocol
	}
	return clientError(err)
}

var _ mcpbroker.CredentialContinuityService = (*Client)(nil)

// CredentialContinuity reports the broker's per-attachment continuity offer. An
// older broker never sets it, so the host keeps the legacy enrollment path.
func (a *clientSessionHandle) CredentialContinuity() bool { return a != nil && a.continuity }

var (
	_ mcpbroker.CredentialCustodyStager        = (*clientSessionHandle)(nil)
	_ mcpbroker.CredentialContinuityAdvertiser = (*clientSessionHandle)(nil)
)
