// Package executioncontroller implements the authenticated provider gRPC boundary.
//
//nolint:revive // Generated gRPC method names define this private handler surface.
package executioncontroller

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// ClientPolicy defines capabilities assigned to an authenticated provider client.
type ClientPolicy struct {
	MayAttestOwner, Administrator bool
	AdministratorFor              []string
	ExecutionTemplates            []string
}

// HandlerConfig configures authenticated client policy.
type HandlerConfig struct {
	Clients     map[string]ClientPolicy
	Ready       func() bool
	Security    *SecurityManager
	Diagnostics port.Diagnostics
}

// Profile is the externally visible immutable execution profile.
type Profile struct {
	Name, Digest                  string
	MaxFileBytes, MaxCommandBytes int64
	MaxCommandDuration            time.Duration
	Capabilities                  []string
}

// Allocation is an exact environment allocation and authorization binding.
type Allocation struct {
	Environment                  executionenv.EnvironmentRef
	Epoch, GrantGeneration       uint64
	OwnerHash, BindingID, Client string
	Ready                        bool
	AdmittedCapabilities         []string
}

// Backend implements provider-side authorization state.
type Backend interface {
	Attach(context.Context, executionenv.EnvironmentRef, string, string, string) (Allocation, error)
	ReleaseReference(context.Context, executionenv.EnvironmentRef, string, string, string) error
}

type lifecycleBackend interface {
	AcquireRun(context.Context, executionenv.EnvironmentRef, string, string, string, string, string, time.Duration) (executionenv.RunClaim, error)
	RenewRun(context.Context, executionenv.EnvironmentRef, string, string, executionenv.RunClaimRequest) (executionenv.RunClaim, error)
	ReleaseRun(context.Context, executionenv.EnvironmentRef, string, string, executionenv.RunClaimRequest) error
	CommitReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	AbortReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	ReserveSuccessor(
		context.Context,
		executionenv.EnvironmentRef,
		string,
		string,
		string,
		string,
		string,
	) ([]string, error)
	PrepareReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	ConfirmReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	CancelReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	ListReferenceIntents(context.Context, string, string, int) ([]executionenv.ReferenceIntent, error)
	FindReferenceIntent(context.Context, executionenv.EnvironmentRef, string, string, string) (executionenv.ReferenceIntent, error)
}

type ownerIntentBackend interface {
	ListReferenceIntentsForClient(context.Context, string, int) ([]executionenv.ReferenceIntent, error)
}

type adminLifecycleBackend interface {
	ReplaceExecutor(context.Context, adminLifecycleRequest) error
	RetireExact(context.Context, adminLifecycleRequest) error
	RecoverEnvironment(context.Context, adminLifecycleRequest) error
	DeleteRetiredEnvironment(context.Context, adminLifecycleRequest) error
	RevokeEnvironment(context.Context, adminLifecycleRequest, uint64) (uint64, error)
}

// Handler is the authenticated private execution-provider gRPC service.
type Handler struct {
	executionv1.UnimplementedExecutionProviderServiceServer
	cfg                   HandlerConfig
	backend               Backend
	lifecycleBackend      lifecycleBackend
	adminLifecycleBackend adminLifecycleBackend
}

// NewHandler constructs the authenticated private gRPC service.
func NewHandler(cfg HandlerConfig, b Backend) *Handler {
	lifecycle, _ := b.(lifecycleBackend)
	adminLifecycle, _ := b.(adminLifecycleBackend)
	return &Handler{cfg: cfg, backend: b, lifecycleBackend: lifecycle, adminLifecycleBackend: adminLifecycle}
}

type authenticatedClient struct {
	id     string
	policy ClientPolicy
}

func (h *Handler) client(ctx context.Context) (authenticatedClient, error) {
	if h.backend == nil || (h.cfg.Ready != nil && !h.cfg.Ready()) {
		return authenticatedClient{}, wireError(executionenv.CodeNotReady, true)
	}
	chain, handshakeVerified, err := clientCertificates(ctx)
	if err != nil || h.cfg.Security == nil && !handshakeVerified {
		return authenticatedClient{}, wireError(executionenv.CodeUnauthenticated, false)
	}
	leaf := chain[0]
	if h.cfg.Security != nil {
		if !h.cfg.Security.Ready() {
			return authenticatedClient{}, wireError(executionenv.CodeNotReady, true)
		}
		id, policy, err := h.cfg.Security.authorize(ctx, chain)
		if err != nil {
			return authenticatedClient{}, securityAuthorizationError(err)
		}
		return authenticatedClient{id: id, policy: policy}, nil
	}
	id, err := executionenv.ClientIdentity(leaf)
	if err != nil {
		return authenticatedClient{}, wireError(executionenv.CodeUnauthenticated, false)
	}
	policy, ok := h.cfg.Clients[id]
	if !ok {
		return authenticatedClient{}, wireError(executionenv.CodeUnauthenticated, false)
	}
	return authenticatedClient{id: id, policy: policy}, nil
}

func (h *Handler) lifecycle() (lifecycleBackend, error) {
	if h.lifecycleBackend == nil {
		return nil, wireError(executionenv.CodeNotReady, false)
	}
	return h.lifecycleBackend, nil
}

// AttachEnvironment exactly reattaches to a durable reference.
func (h *Handler) AttachEnvironment(ctx context.Context, q *executionv1.AttachEnvironmentRequest) (*executionv1.AttachEnvironmentResponse, error) {
	c, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	rc, ok := attachContext(q.GetContext())
	if q == nil || q.Purpose != executionenv.PurposeSession || !c.policy.MayAttestOwner || !ok {
		return nil, wireError(executionenv.CodePermissionDenied, false)
	}
	oh := ownerHash(rc.Owner)
	a, err := h.backend.Attach(ctx, rc.Environment, c.id, oh, rc.BindingID)
	if err != nil {
		return nil, backendError(err)
	}
	return attachResponse(a), nil
}

// AcquireRun takes environment-wide execution ownership.
func (h *Handler) AcquireRun(ctx context.Context, q *executionv1.AcquireRunRequest) (*executionv1.RunClaimResponse, error) {
	c, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	owner, ok := ownerFromProto(q.GetOwner())
	ref := refFromProto(q.GetEnvironment())
	if q == nil || !c.policy.MayAttestOwner || !ok || !validRef(ref) || !validBinding(q.BindingId) || !validIdentity(q.RunId) || !validOperationID(q.OperationId) || q.TtlMillis < executionenv.MinRunTTL.Milliseconds() || q.TtlMillis > executionenv.MaxRunTTL.Milliseconds() {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	lifecycle, lifecycleErr := h.lifecycle()
	if lifecycleErr != nil {
		return nil, lifecycleErr
	}
	claim, err := lifecycle.AcquireRun(ctx, ref, c.id, ownerHash(owner), q.BindingId, q.RunId, q.OperationId, time.Duration(q.TtlMillis)*time.Millisecond)
	if err != nil {
		return nil, backendError(err)
	}
	return runClaimResponse(claim), nil
}

func (h *Handler) RenewRun(ctx context.Context, q *executionv1.RenewRunRequest) (*executionv1.RunClaimResponse, error) {
	c, owner, req, err := h.runClaimRequest(ctx, q.GetEnvironment(), q.GetOwner(), q.GetBindingId(), q.GetRunId(), q.GetClaimId(), q.GetEpoch(), q.GetGrantGeneration(), q.GetOperationId(), q.GetTtlMillis())
	if err != nil {
		return nil, err
	}
	lifecycle, lifecycleErr := h.lifecycle()
	if lifecycleErr != nil {
		return nil, lifecycleErr
	}
	claim, backendErr := lifecycle.RenewRun(ctx, req.Environment, c.id, owner, req)
	if backendErr != nil {
		return nil, backendError(backendErr)
	}
	return runClaimResponse(claim), nil
}

func (h *Handler) ReleaseRun(ctx context.Context, q *executionv1.ReleaseRunRequest) (*emptypb.Empty, error) {
	c, owner, req, err := h.runClaimRequest(ctx, q.GetEnvironment(), q.GetOwner(), q.GetBindingId(), q.GetRunId(), q.GetClaimId(), q.GetEpoch(), q.GetGrantGeneration(), q.GetOperationId(), executionenv.DefaultRunTTL.Milliseconds())
	if err != nil {
		return nil, err
	}
	lifecycle, lifecycleErr := h.lifecycle()
	if lifecycleErr != nil {
		return nil, lifecycleErr
	}
	if backendErr := lifecycle.ReleaseRun(ctx, req.Environment, c.id, owner, req); backendErr != nil {
		return nil, backendError(backendErr)
	}
	return &emptypb.Empty{}, nil
}

func (h *Handler) runClaimRequest(ctx context.Context, pRef *executionv1.EnvironmentRef, pOwner *executionv1.Owner, binding, runID, claimID string, epoch, generation uint64, operationID string, ttlMillis int64) (authenticatedClient, string, executionenv.RunClaimRequest, error) {
	c, err := h.client(ctx)
	if err != nil {
		return c, "", executionenv.RunClaimRequest{}, err
	}
	owner, ok := ownerFromProto(pOwner)
	ref := refFromProto(pRef)
	if !c.policy.MayAttestOwner || !ok || !validRef(ref) || !validBinding(binding) || !validIdentity(runID) || !validIdentity(claimID) || epoch == 0 || epoch > math.MaxInt64 || generation == 0 || generation > math.MaxInt64 || !validOperationID(operationID) || ttlMillis < executionenv.MinRunTTL.Milliseconds() || ttlMillis > executionenv.MaxRunTTL.Milliseconds() {
		return c, "", executionenv.RunClaimRequest{}, wireError(executionenv.CodeInvalidArgument, false)
	}
	return c, ownerHash(owner), executionenv.RunClaimRequest{Environment: ref, Owner: owner, BindingID: binding, RunID: runID, ClaimID: claimID, Epoch: epoch, GrantGeneration: generation, OperationID: operationID, TTL: time.Duration(ttlMillis) * time.Millisecond}, nil
}

func runClaimResponse(claim executionenv.RunClaim) *executionv1.RunClaimResponse {
	return &executionv1.RunClaimResponse{Environment: refToProto(claim.Environment), BindingId: valid(claim.BindingID), RunId: valid(claim.RunID), ClaimId: valid(claim.ClaimID), Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, ExpiresAt: timestamppb.New(claim.ExpiresAt)}
}

func (h *Handler) referenceRequest(ctx context.Context, q *executionv1.ReferenceMutationRequest) (authenticatedClient, string, executionenv.EnvironmentRef, string, string, error) {
	c, err := h.client(ctx)
	if err != nil {
		return c, "", executionenv.EnvironmentRef{}, "", "", err
	}
	owner, ok := ownerFromProto(q.GetOwner())
	ref := refFromProto(q.GetEnvironment())
	if q == nil || !c.policy.MayAttestOwner || !ok || !validRef(ref) || !validBinding(q.BindingId) || !validOperationID(q.OperationId) {
		return c, "", ref, "", "", wireError(executionenv.CodeInvalidArgument, false)
	}
	return c, ownerHash(owner), ref, q.BindingId, q.OperationId, nil
}
func (h *Handler) referenceMutation(ctx context.Context, q *executionv1.ReferenceMutationRequest, operation executionenv.Operation) (*emptypb.Empty, error) {
	c, owner, ref, binding, operationID, err := h.referenceRequest(ctx, q)
	if err != nil {
		return nil, err
	}
	lifecycle, lifecycleErr := h.lifecycle()
	if lifecycleErr != nil {
		return nil, lifecycleErr
	}
	switch operation {
	case executionenv.OpReferenceCommit:
		err = lifecycle.CommitReference(ctx, ref, c.id, owner, binding, operationID)
	case executionenv.OpReferenceAbort:
		err = lifecycle.AbortReference(ctx, ref, c.id, owner, binding, operationID)
	case executionenv.OpReferenceDeletePrepare:
		err = lifecycle.PrepareReferenceDelete(ctx, ref, c.id, owner, binding, operationID)
	case executionenv.OpReferenceDeleteConfirm:
		err = lifecycle.ConfirmReferenceDelete(ctx, ref, c.id, owner, binding, operationID)
	case executionenv.OpReferenceDeleteCancel:
		err = lifecycle.CancelReferenceDelete(ctx, ref, c.id, owner, binding, operationID)
	default:
		err = &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "invalid reference operation"}
	}
	if err != nil {
		return nil, backendError(err)
	}
	return &emptypb.Empty{}, nil
}
func (h *Handler) CommitReference(ctx context.Context, q *executionv1.ReferenceMutationRequest) (*emptypb.Empty, error) {
	return h.referenceMutation(ctx, q, executionenv.OpReferenceCommit)
}
func (h *Handler) AbortReference(ctx context.Context, q *executionv1.ReferenceMutationRequest) (*emptypb.Empty, error) {
	return h.referenceMutation(ctx, q, executionenv.OpReferenceAbort)
}
func (h *Handler) PrepareReferenceDelete(ctx context.Context, q *executionv1.ReferenceMutationRequest) (*emptypb.Empty, error) {
	return h.referenceMutation(ctx, q, executionenv.OpReferenceDeletePrepare)
}
func (h *Handler) ConfirmReferenceDelete(ctx context.Context, q *executionv1.ReferenceMutationRequest) (*emptypb.Empty, error) {
	return h.referenceMutation(ctx, q, executionenv.OpReferenceDeleteConfirm)
}
func (h *Handler) CancelReferenceDelete(ctx context.Context, q *executionv1.ReferenceMutationRequest) (*emptypb.Empty, error) {
	return h.referenceMutation(ctx, q, executionenv.OpReferenceDeleteCancel)
}

func (h *Handler) ReserveSuccessor(ctx context.Context, q *executionv1.ReserveSuccessorRequest) (*executionv1.ReferenceReservationResponse, error) {
	c, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	owner, ok := ownerFromProto(q.GetOwner())
	ref := refFromProto(q.GetEnvironment())
	if q == nil || !c.policy.MayAttestOwner || !ok || !validRef(ref) || !validBinding(q.SourceBindingId) || !validBinding(q.DestinationBindingId) || q.SourceBindingId == q.DestinationBindingId || !validOperationID(q.OperationId) {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	lifecycle, lifecycleErr := h.lifecycle()
	if lifecycleErr != nil {
		return nil, lifecycleErr
	}
	admitted, err := lifecycle.ReserveSuccessor(
		ctx,
		ref,
		c.id,
		ownerHash(owner),
		q.SourceBindingId,
		q.DestinationBindingId,
		q.OperationId,
	)
	if err != nil {
		return nil, backendError(err)
	}
	return &executionv1.ReferenceReservationResponse{
		Environment:          refToProto(ref),
		AdmittedCapabilities: admitted,
	}, nil
}

func (h *Handler) ListReferenceIntents(ctx context.Context, q *executionv1.ListReferenceIntentsRequest) (*executionv1.ListReferenceIntentsResponse, error) {
	c, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	if q == nil || !c.policy.MayAttestOwner || q.Limit < 0 || q.Limit > maxReferences {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	lifecycle, lifecycleErr := h.lifecycle()
	if lifecycleErr != nil {
		return nil, lifecycleErr
	}
	var intents []executionenv.ReferenceIntent
	exactRef := refFromProto(q.GetEnvironment())
	exactLookup := q.GetEnvironment() != nil || q.GetBindingId() != ""
	if exactLookup {
		owner, ok := ownerFromProto(q.GetOwner())
		if !ok || !validRef(exactRef) || !validBinding(q.GetBindingId()) {
			return nil, wireError(executionenv.CodeInvalidArgument, false)
		}
		intent, findErr := lifecycle.FindReferenceIntent(ctx, exactRef, c.id, ownerHash(owner), q.GetBindingId())
		if findErr != nil {
			return nil, backendError(findErr)
		}
		intent.Owner = owner
		intents = []executionenv.ReferenceIntent{intent}
	} else if q.Owner != nil {
		owner, ok := ownerFromProto(q.Owner)
		if !ok {
			return nil, wireError(executionenv.CodeInvalidArgument, false)
		}
		intents, err = lifecycle.ListReferenceIntents(ctx, c.id, ownerHash(owner), int(q.Limit))
		for i := range intents {
			intents[i].Owner = owner
		}
	} else if owned, ok := h.backend.(ownerIntentBackend); ok {
		intents, err = owned.ListReferenceIntentsForClient(ctx, c.id, int(q.Limit))
	} else {
		return nil, wireError(executionenv.CodeNotReady, true)
	}
	if err != nil {
		return nil, backendError(err)
	}
	out := &executionv1.ListReferenceIntentsResponse{}
	for _, intent := range intents {
		out.Intents = append(out.Intents, &executionv1.ReferenceIntent{Environment: refToProto(intent.Environment), BindingId: valid(intent.BindingID), State: valid(string(intent.State)), OperationId: valid(intent.OperationID), SourceBindingId: valid(intent.SourceBindingID), CreatedAt: timestamppb.New(intent.CreatedAt), Owner: &executionv1.Owner{Issuer: valid(intent.Owner.Issuer), Subject: valid(intent.Owner.Subject)}})
	}
	return out, nil
}

// ReleaseReference releases one authorized durable binding reference.
func (h *Handler) ReleaseReference(ctx context.Context, q *executionv1.ReleaseReferenceRequest) (*emptypb.Empty, error) {
	c, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	p := q.GetContext()
	owner, ok := ownerFromProto(p.GetOwner())
	ref := refFromProto(p.GetEnvironment())
	if q == nil || !c.policy.MayAttestOwner || !ok || !validRef(ref) || !validBinding(p.GetBindingId()) {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	if err := h.backend.ReleaseReference(ctx, ref, c.id, ownerHash(owner), p.GetBindingId()); err != nil {
		return nil, backendError(err)
	}
	return &emptypb.Empty{}, nil
}

// RetireEnvironment starts exact, proof-producing executor retirement while retaining the PVC.
func (h *Handler) RetireEnvironment(ctx context.Context, q *executionv1.RetireEnvironmentRequest) (*emptypb.Empty, error) {
	req, err := h.adminRequest(ctx, q.GetEnvironment(), q.GetOwner(), q.GetExpectedExecutionEpoch(), q.GetExpectedPodUid(), q.GetExpectedPvcUid(), q.GetOperationId())
	if err != nil {
		return nil, err
	}
	if err := h.adminLifecycleBackend.RetireExact(ctx, req); err != nil {
		return nil, backendError(err)
	}
	return &emptypb.Empty{}, nil
}

func (h *Handler) ReplaceExecutor(ctx context.Context, q *executionv1.ReplaceExecutorRequest) (*emptypb.Empty, error) {
	req, err := h.adminRequest(ctx, q.GetEnvironment(), q.GetOwner(), q.GetExpectedExecutionEpoch(), q.GetExpectedPodUid(), q.GetExpectedPvcUid(), q.GetOperationId())
	if err != nil {
		return nil, err
	}
	if err := h.adminLifecycleBackend.ReplaceExecutor(ctx, req); err != nil {
		return nil, backendError(err)
	}
	return &emptypb.Empty{}, nil
}

func (h *Handler) RecoverEnvironment(ctx context.Context, q *executionv1.RecoverEnvironmentRequest) (*emptypb.Empty, error) {
	req, err := h.adminRequest(ctx, q.GetEnvironment(), q.GetOwner(), q.GetExpectedExecutionEpoch(), q.GetExpectedPodUid(), q.GetExpectedPvcUid(), q.GetOperationId())
	if err != nil {
		return nil, err
	}
	if err := h.adminLifecycleBackend.RecoverEnvironment(ctx, req); err != nil {
		var controlled *executionenv.Error
		if errors.As(err, &controlled) && controlled.Code == executionenv.CodeFenceUnknown {
			return nil, wireError(executionenv.CodeFenceUnknown, false)
		}
		return nil, backendError(err)
	}
	return &emptypb.Empty{}, nil
}

func (h *Handler) DeleteRetiredEnvironment(ctx context.Context, q *executionv1.DeleteRetiredEnvironmentRequest) (*emptypb.Empty, error) {
	c, err := h.client(ctx)
	owner, ownerOK := ownerFromProto(q.GetOwner())
	ref := refFromProto(q.GetEnvironment())
	if err != nil {
		return nil, err
	}
	if h.adminLifecycleBackend == nil || !c.policy.Administrator || !ownerOK || !validRef(ref) || !validIdentity(q.GetExpectedPvcUid()) || !validOperationID(q.GetOperationId()) {
		return nil, wireError(executionenv.CodePermissionDenied, false)
	}
	req := adminLifecycleRequest{Environment: ref, OwnerHash: ownerHash(owner), Client: c.id, AdministratorFor: c.policy.AdministratorFor, ExpectedPVCUID: q.GetExpectedPvcUid(), OperationID: q.GetOperationId()}
	if err := h.adminLifecycleBackend.DeleteRetiredEnvironment(ctx, req); err != nil {
		if h.cfg.Diagnostics != nil {
			h.cfg.Diagnostics.Log(ctx, port.LevelWarn, "execution backend failure", "operation", "delete_retired", "reason", backendReasonClass(err))
		}
		return nil, backendError(err)
	}
	return &emptypb.Empty{}, nil
}

func (h *Handler) RevokeEnvironment(ctx context.Context, q *executionv1.RevokeEnvironmentRequest) (*executionv1.RevokeEnvironmentResponse, error) {
	c, err := h.client(ctx)
	owner, ownerOK := ownerFromProto(q.GetOwner())
	ref := refFromProto(q.GetEnvironment())
	if err != nil {
		return nil, err
	}
	if h.adminLifecycleBackend == nil || !c.policy.Administrator || !ownerOK || !validRef(ref) {
		return nil, wireError(executionenv.CodePermissionDenied, false)
	}
	if q.GetExpectedGrantGeneration() == 0 || q.GetExpectedGrantGeneration() > math.MaxInt64 || !validOperationID(q.GetOperationId()) {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	req := adminLifecycleRequest{Environment: ref, OwnerHash: ownerHash(owner), Client: c.id, AdministratorFor: c.policy.AdministratorFor, OperationID: q.GetOperationId()}
	generation, err := h.adminLifecycleBackend.RevokeEnvironment(ctx, req, q.GetExpectedGrantGeneration())
	if err != nil {
		return nil, backendError(err)
	}
	return &executionv1.RevokeEnvironmentResponse{GrantGeneration: generation}, nil
}

func (h *Handler) adminRequest(ctx context.Context, pRef *executionv1.EnvironmentRef, pOwner *executionv1.Owner, epoch uint64, podUID, pvcUID, operationID string) (adminLifecycleRequest, error) {
	c, err := h.client(ctx)
	if err != nil {
		return adminLifecycleRequest{}, err
	}
	owner, ownerOK := ownerFromProto(pOwner)
	ref := refFromProto(pRef)
	if h.adminLifecycleBackend == nil || !c.policy.Administrator || !ownerOK || !validRef(ref) || epoch == 0 || epoch > math.MaxInt64 || !validIdentity(podUID) || !validIdentity(pvcUID) || !validOperationID(operationID) {
		return adminLifecycleRequest{}, wireError(executionenv.CodePermissionDenied, false)
	}
	return adminLifecycleRequest{Environment: ref, OwnerHash: ownerHash(owner), Client: c.id, AdministratorFor: c.policy.AdministratorFor, ExpectedEpoch: epoch, ExpectedPodUID: podUID, ExpectedPVCUID: pvcUID, OperationID: operationID}, nil
}

func requestContextFromProto(p *executionv1.RequestContext) (executionenv.RequestContext, bool) {
	owner, ownerOK := ownerFromProto(p.GetOwner())
	ref := refFromProto(p.GetEnvironment())
	rc := executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: p.GetBindingId(), RunID: p.GetRunId(), ClaimID: p.GetClaimId(), Epoch: p.GetEpoch(), GrantGeneration: p.GetGrantGeneration()}
	ok := p != nil && ownerOK && ref.ID != "" && ref.Revision != "" && len(ref.ID) <= executionenv.MaxIdentityBytes && len(ref.Revision) <= executionenv.MaxIdentityBytes && rc.BindingID != "" && len(rc.BindingID) <= executionenv.MaxBindingBytes && validIdentity(rc.RunID) && validIdentity(rc.ClaimID) && rc.Epoch != 0 && rc.GrantGeneration != 0
	return rc, ok
}
func attachContext(p *executionv1.RequestContext) (executionenv.RequestContext, bool) {
	owner, ownerOK := ownerFromProto(p.GetOwner())
	ref := refFromProto(p.GetEnvironment())
	rc := executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: p.GetBindingId()}
	return rc, p != nil && ownerOK && ref.ID != "" && ref.Revision != "" && len(ref.ID) <= executionenv.MaxIdentityBytes && len(ref.Revision) <= executionenv.MaxIdentityBytes && rc.BindingID != "" && len(rc.BindingID) <= executionenv.MaxBindingBytes
}
func ownerFromProto(p *executionv1.Owner) (executionenv.Owner, bool) {
	o := executionenv.Owner{Issuer: p.GetIssuer(), Subject: p.GetSubject()}
	return o, p != nil && o.Issuer != "" && o.Subject != "" && len(o.Issuer) <= executionenv.MaxIdentityBytes && len(o.Subject) <= executionenv.MaxIdentityBytes
}
func refFromProto(p *executionv1.EnvironmentRef) executionenv.EnvironmentRef {
	return executionenv.EnvironmentRef{ID: p.GetId(), Revision: p.GetRevision()}
}
func refToProto(r executionenv.EnvironmentRef) *executionv1.EnvironmentRef {
	return &executionv1.EnvironmentRef{Id: valid(r.ID), Revision: valid(r.Revision)}
}

func ensureResponse(a Allocation) *executionv1.EnsureEnvironmentResponse {
	return &executionv1.EnsureEnvironmentResponse{
		Environment:          refToProto(a.Environment),
		Epoch:                a.Epoch,
		Ready:                a.Ready,
		GrantGeneration:      a.GrantGeneration,
		AdmittedCapabilities: a.AdmittedCapabilities,
	}
}
func attachResponse(a Allocation) *executionv1.AttachEnvironmentResponse {
	return &executionv1.AttachEnvironmentResponse{
		Environment:          refToProto(a.Environment),
		Epoch:                a.Epoch,
		Ready:                a.Ready,
		GrantGeneration:      a.GrantGeneration,
		AdmittedCapabilities: a.AdmittedCapabilities,
	}
}
func validIdentity(v string) bool    { return v != "" && len(v) <= executionenv.MaxIdentityBytes }
func validBinding(v string) bool     { return v != "" && len(v) <= executionenv.MaxBindingBytes }
func validOperationID(v string) bool { return validIdentity(v) }
func validRef(v executionenv.EnvironmentRef) bool {
	return validIdentity(v.ID) && validIdentity(v.Revision)
}

func ownerHash(o executionenv.Owner) string {
	s := sha256.Sum256([]byte(o.Issuer + "\x00" + o.Subject))
	return hex.EncodeToString(s[:])
}
func fingerprint(fields ...string) string {
	h := sha256.New()
	for _, f := range fields {
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func securityAuthorizationError(err error) error {
	if errors.Is(err, errAuthorityUnavailable) {
		return wireError(executionenv.CodeNotReady, true)
	}
	return wireError(executionenv.CodeUnauthenticated, false)
}

func backendReasonClass(err error) string {
	var controlled *executionenv.Error
	if errors.As(err, &controlled) && controlled.Code.Valid() {
		return string(controlled.Code)
	}
	switch {
	case apierrors.IsInvalid(err):
		return "invalid"
	case apierrors.IsConflict(err):
		return "conflict"
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case apierrors.IsTooManyRequests(err):
		return "rate_limited"
	case apierrors.IsForbidden(err):
		return "forbidden"
	case apierrors.IsUnauthorized(err):
		return "unauthorized"
	case apierrors.IsNotFound(err):
		return "not_found"
	case apierrors.IsAlreadyExists(err):
		return "already_exists"
	case apierrors.IsServiceUnavailable(err):
		return "unavailable"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "other"
	}
}

func backendError(err error) error {
	var refusal *admissionRefusal
	if errors.As(err, &refusal) {
		return wireErrorMessage(refusal.cause.Code, false, refusal.cause.Message)
	}
	var e *executionenv.Error
	if errors.As(err, &e) && e.Code.Valid() {
		return wireError(e.Code, e.Retryable)
	}
	return wireError(executionenv.CodeInternal, false)
}
func wireError(code executionenv.ErrorCode, retry bool) error {
	return wireErrorMessage(code, retry, "")
}
func wireErrorMessage(code executionenv.ErrorCode, retry bool, message string) error {
	if !code.Valid() || (retry && code != executionenv.CodeNotReady && code != executionenv.CodeUnauthenticated) {
		code, retry = executionenv.CodeInternal, false
	}
	grpcCode := codes.Internal
	switch code {
	case executionenv.CodeInvalidArgument:
		grpcCode = codes.InvalidArgument
	case executionenv.CodeUnauthenticated:
		grpcCode = codes.Unauthenticated
	case executionenv.CodePermissionDenied:
		grpcCode = codes.PermissionDenied
	case executionenv.CodeNotFound:
		grpcCode = codes.NotFound
	case executionenv.CodeAlreadyExists:
		grpcCode = codes.AlreadyExists
	case executionenv.CodeConflict, executionenv.CodeVersionMismatch, executionenv.CodeDirectoryNotEmpty:
		grpcCode = codes.Aborted
	case executionenv.CodeNotReady:
		grpcCode = codes.Unavailable
	case executionenv.CodeFenceUnknown:
		grpcCode = codes.FailedPrecondition
	case executionenv.CodeResourceExhausted:
		grpcCode = codes.ResourceExhausted
	}
	if message == "" {
		message = "execution provider request failed"
	}
	if code == executionenv.CodeFenceUnknown {
		message = "execution provider request failed; verify termination independently or use external fencing"
	}
	st := status.New(grpcCode, message)
	with, err := st.WithDetails(&executionv1.ErrorDetail{Code: string(code), Retryable: retry})
	if err == nil {
		st = with
	}
	return st.Err()
}
func valid(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

// TLSConfig returns the provider's TLS 1.3 mutual-authentication policy.
func TLSConfig(server tls.Certificate, clientCAs *x509.CertPool) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs}
}
