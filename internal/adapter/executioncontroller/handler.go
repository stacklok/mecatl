// Package executioncontroller implements the authenticated provider gRPC boundary.
//
//nolint:revive // Generated gRPC method names define this private handler surface.
package executioncontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
}

// GrantSigner configures short-lived environment grant issuance.
type GrantSigner struct {
	KeyID            string
	PrivateKey       ed25519.PrivateKey
	Issuer, Audience string
	Lifetime         time.Duration
}

// HandlerConfig configures authentication and capability grants.
type HandlerConfig struct {
	Clients     map[string]ClientPolicy
	Signer      GrantSigner
	Verifier    executionenv.GrantVerifier
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
}

// Backend implements provider-side authorization state and executor dispatch.
type Backend interface {
	ValidateProfile(context.Context, string) (Profile, error)
	Ensure(context.Context, string, string, string, string, string) (Allocation, error)
	Attach(context.Context, executionenv.EnvironmentRef, string, string, string) (Allocation, error)
	ReleaseReference(context.Context, executionenv.EnvironmentRef, string, string, string) error
	File(context.Context, string, string, executionenv.FileRequest) (executionenv.FileResponse, error)
	StartCommand(context.Context, string, string, executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error)
	CommandStatus(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error)
	CancelCommand(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error)
}

type runClaimValidator interface {
	ValidateRunClaim(context.Context, string, string, executionenv.RequestContext) error
}

type lifecycleBackend interface {
	EnsurePending(context.Context, string, string, string, string, string, string) (Allocation, error)
	AcquireRun(context.Context, executionenv.EnvironmentRef, string, string, string, string, string, time.Duration) (executionenv.RunClaim, error)
	RenewRun(context.Context, executionenv.EnvironmentRef, string, string, executionenv.RunClaimRequest) (executionenv.RunClaim, error)
	ReleaseRun(context.Context, executionenv.EnvironmentRef, string, string, executionenv.RunClaimRequest) error
	CommitReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	AbortReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	ReserveSuccessor(context.Context, executionenv.EnvironmentRef, string, string, string, string, string) error
	PrepareReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	ConfirmReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	CancelReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error
	ListReferenceIntents(context.Context, string, string, int) ([]executionenv.ReferenceIntent, error)
	FindReferenceIntent(context.Context, executionenv.EnvironmentRef, string, string, string) (executionenv.ReferenceIntent, error)
}

type ownerIntentBackend interface {
	EnsurePendingOwned(context.Context, string, string, executionenv.Owner, string, string, string, string) (Allocation, error)
	ListReferenceIntentsForClient(context.Context, string, int) ([]executionenv.ReferenceIntent, error)
}

type adminLifecycleBackend interface {
	ReplaceExecutor(context.Context, adminLifecycleRequest) error
	RetireExact(context.Context, adminLifecycleRequest) error
	RecoverEnvironment(context.Context, adminLifecycleRequest) error
	DeleteRetiredEnvironment(context.Context, adminLifecycleRequest) error
	MigrateEnvironment(context.Context, adminLifecycleRequest) error
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
	id, err := canonicalClientIdentity(leaf)
	if err != nil {
		return authenticatedClient{}, wireError(executionenv.CodeUnauthenticated, false)
	}
	policy, ok := h.cfg.Clients[id]
	if !ok {
		return authenticatedClient{}, wireError(executionenv.CodeUnauthenticated, false)
	}
	return authenticatedClient{id: id, policy: policy}, nil
}

func canonicalClientIdentity(c *x509.Certificate) (string, error) {
	if len(c.URIs) != 1 {
		return "", errors.New("certificate must have exactly one URI SAN")
	}
	ids := make([]string, 0, 1)
	for _, u := range c.URIs {
		if u == nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			continue
		}
		v := *u
		v.Scheme, v.Host = strings.ToLower(v.Scheme), strings.ToLower(v.Host)
		ids = append(ids, v.String())
	}
	if len(ids) != 1 {
		return "", errors.New("certificate must have exactly one canonical URI SAN")
	}
	return ids[0], nil
}

func (h *Handler) lifecycle() (lifecycleBackend, error) {
	if h.lifecycleBackend == nil {
		return nil, wireError(executionenv.CodeNotReady, false)
	}
	return h.lifecycleBackend, nil
}

// ValidateProfile validates one operator-defined execution profile.
func (h *Handler) ValidateProfile(ctx context.Context, q *executionv1.ValidateProfileRequest) (*executionv1.ValidateProfileResponse, error) {
	if _, err := h.client(ctx); err != nil {
		return nil, err
	}
	if q == nil || q.Profile == "" || len(q.Profile) > 63 {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	p, err := h.backend.ValidateProfile(ctx, q.Profile)
	if err != nil {
		return nil, backendError(err)
	}
	return &executionv1.ValidateProfileResponse{Profile: valid(p.Name), Digest: valid(p.Digest), Capabilities: validAll(p.Capabilities), MaxFileBytes: p.MaxFileBytes, MaxCommandBytes: p.MaxCommandBytes, MaxCommandDurationMillis: p.MaxCommandDuration.Milliseconds()}, nil
}

// EnsureEnvironment idempotently resolves or allocates an environment.
func (h *Handler) EnsureEnvironment(ctx context.Context, q *executionv1.EnsureEnvironmentRequest) (*executionv1.EnsureEnvironmentResponse, error) {
	c, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	owner, ok := ownerFromProto(q.GetOwner())
	if q == nil || !c.policy.MayAttestOwner || !ok || q.BindingId == "" || len(q.BindingId) > executionenv.MaxBindingBytes || q.Profile == "" || len(q.Profile) > 63 || !validOperationID(q.OperationId) {
		return nil, wireError(executionenv.CodePermissionDenied, false)
	}
	oh := ownerHash(owner)
	lifecycle, lifecycleErr := h.lifecycle()
	if lifecycleErr != nil {
		return nil, lifecycleErr
	}
	fp := fingerprint(c.id, oh, q.BindingId, q.Profile)
	var a Allocation
	if owned, ok := h.backend.(ownerIntentBackend); ok {
		a, err = owned.EnsurePendingOwned(ctx, c.id, oh, owner, q.BindingId, q.Profile, fp, q.OperationId)
	} else {
		a, err = lifecycle.EnsurePending(ctx, c.id, oh, q.BindingId, q.Profile, fp, q.OperationId)
	}
	if err != nil {
		return nil, backendError(err)
	}
	return ensureResponse(a, "", time.Time{}), nil
}

// AttachEnvironment exactly reattaches and refreshes a short-lived grant.
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
	return attachResponse(a, "", time.Time{}), nil
}

// AcquireRun takes environment-wide execution ownership and issues a run-bound grant.
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
	return h.runClaimResponse(ctx, claim, c.id, ownerHash(owner))
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
	return h.runClaimResponse(ctx, claim, c.id, owner)
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

func (h *Handler) runClaimResponse(ctx context.Context, claim executionenv.RunClaim, client, owner string) (*executionv1.RunClaimResponse, error) {
	grant, expiry, err := h.signClaim(ctx, claim, client, owner)
	if err != nil {
		if errors.Is(err, errAuthorityUnavailable) {
			return nil, securityAuthorizationError(err)
		}
		return nil, wireError(executionenv.CodeInternal, false)
	}
	return &executionv1.RunClaimResponse{Environment: refToProto(claim.Environment), BindingId: valid(claim.BindingID), RunId: valid(claim.RunID), ClaimId: valid(claim.ClaimID), Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, Grant: valid(grant), ExpiresAt: timestamppb.New(expiry)}, nil
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
	if err = lifecycle.ReserveSuccessor(ctx, ref, c.id, ownerHash(owner), q.SourceBindingId, q.DestinationBindingId, q.OperationId); err != nil {
		return nil, backendError(err)
	}
	return &executionv1.ReferenceReservationResponse{Environment: refToProto(ref)}, nil
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
	c, owner, rc, err := h.authorize(ctx, q.GetContext(), executionenv.OpReferenceRelease)
	if err != nil {
		return nil, err
	}
	if err := h.backend.ReleaseReference(ctx, rc.Environment, c.id, owner, rc.BindingID); err != nil {
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

func (h *Handler) MigrateEnvironment(ctx context.Context, q *executionv1.MigrateEnvironmentRequest) (*emptypb.Empty, error) {
	req, err := h.adminRequest(ctx, q.GetEnvironment(), q.GetOwner(), 1, q.GetExpectedPodUid(), q.GetExpectedPvcUid(), q.GetOperationId())
	if err != nil {
		return nil, err
	}
	if q == nil || q.ExpectedSchemaVersion == nil || q.GetExpectedSchemaVersion() > 1 {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	req.ExpectedEpoch = 0
	req.ExpectedSchema = int64(q.GetExpectedSchemaVersion())
	if err := h.adminLifecycleBackend.MigrateEnvironment(ctx, req); err != nil {
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

// Files executes one bounded and authorized filesystem operation.
func (h *Handler) Files(ctx context.Context, q *executionv1.FileRequest) (*executionv1.FileResponse, error) {
	if _, err := h.client(ctx); err != nil {
		return nil, err
	}
	op, ok := operationFromProto(q.GetOperation())
	if q == nil || !ok || !validFileRequest(q) {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	c, owner, rc, err := h.authorize(ctx, q.Context, op)
	if err != nil {
		return nil, err
	}
	req := executionenv.FileRequest{Context: rc, Operation: op, Path: q.Path, Destination: q.Destination, Pattern: q.Pattern, Data: q.Data, Version: string(q.Version), Limit: int(q.Limit)}
	out, err := h.backend.File(ctx, c.id, owner, req)
	if err != nil {
		return nil, backendError(err)
	}
	return fileResponse(out), nil
}

// StartCommand executes one authorized foreground command.
func (h *Handler) StartCommand(ctx context.Context, q *executionv1.CommandStartRequest) (*executionv1.CommandStartResponse, error) {
	if _, err := h.client(ctx); err != nil {
		return nil, err
	}
	if q == nil || len(q.Command) == 0 || len(q.Command) > executionenv.MaxCommandBytes || q.TimeoutMillis < 0 {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	c, owner, rc, err := h.authorize(ctx, q.Context, executionenv.OpCommandStart)
	if err != nil {
		return nil, err
	}
	out, err := h.backend.StartCommand(ctx, c.id, owner, executionenv.CommandStartRequest{Context: rc, Command: q.Command, TimeoutMillis: q.TimeoutMillis})
	if err != nil {
		return nil, backendError(err)
	}
	response, ok := commandStartResponse(out)
	if !ok {
		return nil, wireError(executionenv.CodeInternal, false)
	}
	return response, nil
}

// CommandStatus authorizes then reports the detached API as unsupported.
func (h *Handler) CommandStatus(ctx context.Context, q *executionv1.CommandQueryRequest) (*executionv1.CommandStatusResponse, error) {
	return h.commandQuery(ctx, q, executionenv.OpCommandStatus)
}

// CancelCommand authorizes then reports the detached API as unsupported.
func (h *Handler) CancelCommand(ctx context.Context, q *executionv1.CommandQueryRequest) (*executionv1.CommandStatusResponse, error) {
	return h.commandQuery(ctx, q, executionenv.OpCommandCancel)
}
func (h *Handler) commandQuery(ctx context.Context, q *executionv1.CommandQueryRequest, op executionenv.Operation) (*executionv1.CommandStatusResponse, error) {
	_, _, _, err := h.authorize(ctx, q.GetContext(), op)
	if err != nil {
		return nil, err
	}
	if q.CommandId == "" || q.Offset < 0 {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	return nil, status.Error(codes.Unimplemented, "foreground commands have no detached control API")
}

func (h *Handler) authorize(ctx context.Context, p *executionv1.RequestContext, op executionenv.Operation) (authenticatedClient, string, executionenv.RequestContext, error) {
	c, err := h.client(ctx)
	if err != nil {
		return c, "", executionenv.RequestContext{}, err
	}
	rc, ok := requestContextFromProto(p)
	if !ok {
		return c, "", rc, wireError(executionenv.CodePermissionDenied, false)
	}
	oh := ownerHash(rc.Owner)
	verifier := h.cfg.Verifier
	if h.cfg.Security != nil {
		now := h.cfg.Security.now()
		material, materialErr := h.cfg.Security.authoritativeAt(ctx, now)
		if materialErr != nil {
			return c, "", rc, wireError(executionenv.CodeNotReady, true)
		}
		verifier = material.verifierAt(now)
	}
	if _, err := verifier.Verify(rc.Grant, executionenv.GrantExpectation{Client: c.id, OwnerHash: oh, BindingID: rc.BindingID, RunID: rc.RunID, ClaimID: rc.ClaimID, Environment: rc.Environment, Epoch: rc.Epoch, GrantGeneration: rc.GrantGeneration, Operation: op}); err != nil {
		if errors.Is(err, executionenv.ErrGrantExpired) {
			return c, "", rc, wireError(executionenv.CodeUnauthenticated, true)
		}
		return c, "", rc, wireError(executionenv.CodePermissionDenied, false)
	}
	if validator, ok := h.backend.(runClaimValidator); ok {
		if err := validator.ValidateRunClaim(ctx, c.id, oh, rc); err != nil {
			return c, "", rc, backendError(err)
		}
	}
	return c, oh, rc, nil
}

func requestContextFromProto(p *executionv1.RequestContext) (executionenv.RequestContext, bool) {
	owner, ownerOK := ownerFromProto(p.GetOwner())
	ref := refFromProto(p.GetEnvironment())
	rc := executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: p.GetBindingId(), RunID: p.GetRunId(), ClaimID: p.GetClaimId(), Epoch: p.GetEpoch(), GrantGeneration: p.GetGrantGeneration(), Grant: p.GetGrant()}
	ok := p != nil && ownerOK && ref.ID != "" && ref.Revision != "" && len(ref.ID) <= executionenv.MaxIdentityBytes && len(ref.Revision) <= executionenv.MaxIdentityBytes && rc.BindingID != "" && len(rc.BindingID) <= executionenv.MaxBindingBytes && validIdentity(rc.RunID) && validIdentity(rc.ClaimID) && rc.Epoch != 0 && rc.GrantGeneration != 0 && rc.Grant != "" && len(rc.Grant) <= executionenv.MaxGrantBytes
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

func validFileRequest(q *executionv1.FileRequest) bool { //nolint:gocyclo // Closed operation/field matrix is intentionally explicit.
	if len(q.Data) > executionenv.MaxFileBytes || len(q.Path) > executionenv.MaxPathBytes || len(q.Destination) > executionenv.MaxPathBytes || len(q.Pattern) > executionenv.MaxPathBytes || q.Limit < 0 || q.Limit > executionenv.MaxListEntries {
		return false
	}
	path, dest, pattern, data, version, limit := q.Path != "", q.Destination != "", q.Pattern != "", len(q.Data) != 0, len(q.Version) != 0, q.Limit != 0
	switch q.Operation {
	case executionv1.FileOperation_FILE_OPERATION_READ, executionv1.FileOperation_FILE_OPERATION_RESOLVE_AUTHORITY, executionv1.FileOperation_FILE_OPERATION_STAT, executionv1.FileOperation_FILE_OPERATION_REMOVE:
		return path && !dest && !pattern && !data && !version && !limit
	case executionv1.FileOperation_FILE_OPERATION_CREATE:
		return path && !dest && !pattern && !version && !limit
	case executionv1.FileOperation_FILE_OPERATION_REPLACE:
		return path && !dest && !pattern && version && !limit
	case executionv1.FileOperation_FILE_OPERATION_LIST:
		return path && !dest && !pattern && !data && !version
	case executionv1.FileOperation_FILE_OPERATION_RENAME, executionv1.FileOperation_FILE_OPERATION_COPY:
		return path && dest && !pattern && !data && !version && !limit
	case executionv1.FileOperation_FILE_OPERATION_GLOB:
		return !path && !dest && pattern && !data && !version
	case executionv1.FileOperation_FILE_OPERATION_GREP:
		return path && !dest && pattern && !data && !version
	default:
		return false
	}
}

func operationFromProto(op executionv1.FileOperation) (executionenv.Operation, bool) {
	m := map[executionv1.FileOperation]executionenv.Operation{executionv1.FileOperation_FILE_OPERATION_READ: executionenv.OpFileRead, executionv1.FileOperation_FILE_OPERATION_RESOLVE_AUTHORITY: executionenv.OpFileResolveAuthority, executionv1.FileOperation_FILE_OPERATION_STAT: executionenv.OpFileStat, executionv1.FileOperation_FILE_OPERATION_CREATE: executionenv.OpFileCreate, executionv1.FileOperation_FILE_OPERATION_REPLACE: executionenv.OpFileReplace, executionv1.FileOperation_FILE_OPERATION_LIST: executionenv.OpFileList, executionv1.FileOperation_FILE_OPERATION_REMOVE: executionenv.OpFileRemove, executionv1.FileOperation_FILE_OPERATION_RENAME: executionenv.OpFileRename, executionv1.FileOperation_FILE_OPERATION_COPY: executionenv.OpFileCopy, executionv1.FileOperation_FILE_OPERATION_GLOB: executionenv.OpFileGlob, executionv1.FileOperation_FILE_OPERATION_GREP: executionenv.OpFileGrep}
	v, ok := m[op]
	return v, ok
}

func ensureResponse(a Allocation, grant string, expiry time.Time) *executionv1.EnsureEnvironmentResponse {
	return &executionv1.EnsureEnvironmentResponse{Environment: refToProto(a.Environment), Epoch: a.Epoch, Ready: a.Ready, GrantGeneration: a.GrantGeneration, Grant: valid(grant), GrantExpiresAt: timestamppb.New(expiry)}
}
func attachResponse(a Allocation, grant string, expiry time.Time) *executionv1.AttachEnvironmentResponse {
	return &executionv1.AttachEnvironmentResponse{Environment: refToProto(a.Environment), Epoch: a.Epoch, Ready: a.Ready, GrantGeneration: a.GrantGeneration, Grant: valid(grant), GrantExpiresAt: timestamppb.New(expiry)}
}
func fileResponse(v executionenv.FileResponse) *executionv1.FileResponse {
	r := &executionv1.FileResponse{Data: v.Data, Version: []byte(v.Version), Paths: validAll(v.Paths), AuthorityTarget: valid(v.AuthorityTarget), AuthorityWorkspace: valid(v.AuthorityWorkspace)}
	if v.Info != nil {
		r.Info = fileInfoToProto(*v.Info)
	}
	for _, x := range v.Entries {
		r.Entries = append(r.Entries, fileInfoToProto(x))
	}
	for _, x := range v.Matches {
		r.Matches = append(r.Matches, &executionv1.GrepMatch{Path: valid(x.Path), Line: boundedInt32(x.Line), Text: valid(x.Text)})
	}
	return r
}
func fileInfoToProto(v executionenv.FileInfo) *executionv1.FileInfo {
	return &executionv1.FileInfo{Name: valid(v.Name), Size: v.Size, Mode: v.Mode, ModTime: timestamppb.New(v.ModTime), IsDir: v.IsDir}
}
func commandStartResponse(v executionenv.CommandStartResponse) (*executionv1.CommandStartResponse, bool) {
	state, ok := commandStateToProto(v.State)
	if !ok {
		return nil, false
	}
	result, ok := commandStatusResponse(v.Result)
	if !ok {
		return nil, false
	}
	return &executionv1.CommandStartResponse{CommandId: valid(v.CommandID), State: state, Result: result}, true
}
func commandStatusResponse(v executionenv.CommandStatusResponse) (*executionv1.CommandStatusResponse, bool) {
	state, ok := commandStateToProto(v.State)
	if !ok {
		return nil, false
	}
	return &executionv1.CommandStatusResponse{CommandId: valid(v.CommandID), State: state, ExitCode: boundedInt32(v.ExitCode), Stdout: v.Stdout, Stderr: v.Stderr, NextOffset: v.NextOffset, Truncated: v.Truncated, TerminalReceipt: valid(v.TerminalReceipt)}, true
}
func boundedInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v) //nolint:gosec // bounds checked above
}

func commandStateToProto(v executionenv.CommandState) (executionv1.CommandState, bool) {
	switch v {
	case executionenv.CommandRunning:
		return executionv1.CommandState_COMMAND_STATE_RUNNING, true
	case executionenv.CommandSucceeded:
		return executionv1.CommandState_COMMAND_STATE_SUCCEEDED, true
	case executionenv.CommandFailed:
		return executionv1.CommandState_COMMAND_STATE_FAILED, true
	case executionenv.CommandCancelled:
		return executionv1.CommandState_COMMAND_STATE_CANCELLED, true
	case executionenv.CommandFenceUnknown:
		return executionv1.CommandState_COMMAND_STATE_FENCE_UNKNOWN, true
	default:
		return executionv1.CommandState_COMMAND_STATE_UNSPECIFIED, false
	}
}

func validIdentity(v string) bool    { return v != "" && len(v) <= executionenv.MaxIdentityBytes }
func validBinding(v string) bool     { return v != "" && len(v) <= executionenv.MaxBindingBytes }
func validOperationID(v string) bool { return validIdentity(v) }
func validRef(v executionenv.EnvironmentRef) bool {
	return validIdentity(v.ID) && validIdentity(v.Revision)
}

func (h *Handler) signClaim(ctx context.Context, claim executionenv.RunClaim, client, owner string) (string, time.Time, error) {
	signer := h.cfg.Signer
	now := time.Now().UTC()
	var signingDeadline time.Time
	if h.cfg.Security != nil {
		now = h.cfg.Security.now()
		material, err := h.cfg.Security.authoritativeAt(ctx, now)
		if err != nil {
			return "", time.Time{}, err
		}
		signer = material.signer
		signingDeadline = material.activeWindow.verifyUntil
	}
	life := signer.Lifetime
	if life <= 0 {
		life = time.Minute
	}
	expiry := now.Add(life)
	if !signingDeadline.IsZero() && signingDeadline.Before(expiry) {
		expiry = signingDeadline
	}
	if !now.Before(expiry) {
		return "", time.Time{}, errors.New("active signing key is expired")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, err
	}
	ops := []executionenv.Operation{executionenv.OpFileRead, executionenv.OpFileResolveAuthority, executionenv.OpFileStat, executionenv.OpFileCreate, executionenv.OpFileReplace, executionenv.OpFileList, executionenv.OpFileRemove, executionenv.OpFileRename, executionenv.OpFileCopy, executionenv.OpFileGlob, executionenv.OpFileGrep, executionenv.OpCommandStart, executionenv.OpCommandStatus, executionenv.OpCommandCancel}
	grant, err := executionenv.SignGrant(signer.PrivateKey, executionenv.GrantClaims{KeyID: signer.KeyID, Issuer: signer.Issuer, Audience: signer.Audience, Client: client, OwnerHash: owner, BindingID: claim.BindingID, RunID: claim.RunID, ClaimID: claim.ClaimID, Environment: claim.Environment, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, Operations: ops, NotBefore: now, ExpiresAt: expiry, Nonce: hex.EncodeToString(nonce)})
	return grant, expiry, err
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
	var e *executionenv.Error
	if errors.As(err, &e) && e.Code.Valid() {
		return wireError(e.Code, e.Retryable)
	}
	return wireError(executionenv.CodeInternal, false)
}
func wireError(code executionenv.ErrorCode, retry bool) error {
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
	message := "execution provider request failed"
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
func validAll(in []string) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = valid(in[i])
	}
	return out
}

// TLSConfig returns the provider's TLS 1.3 mutual-authentication policy.
func TLSConfig(server tls.Certificate, clientCAs *x509.CertPool) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs}
}
