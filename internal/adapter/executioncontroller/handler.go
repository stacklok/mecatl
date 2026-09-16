// Package executioncontroller implements the authenticated provider gRPC boundary.
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
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// ClientPolicy defines capabilities assigned to an authenticated provider client.
type ClientPolicy struct{ MayAttestOwner, Administrator bool }

// GrantSigner configures short-lived environment grant issuance.
type GrantSigner struct {
	KeyID            string
	PrivateKey       ed25519.PrivateKey
	Issuer, Audience string
	Lifetime         time.Duration
}

// HandlerConfig configures authentication and capability grants.
type HandlerConfig struct {
	Clients  map[string]ClientPolicy
	Signer   GrantSigner
	Verifier executionenv.GrantVerifier
	Ready    func() bool
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
	Epoch                        uint64
	OwnerHash, BindingID, Client string
	Ready                        bool
}

// Backend implements provider-side authorization state and executor dispatch.
type Backend interface {
	ValidateProfile(context.Context, string) (Profile, error)
	Ensure(context.Context, string, string, string, string, string) (Allocation, error)
	Attach(context.Context, executionenv.EnvironmentRef, string, string, string) (Allocation, error)
	ReleaseReference(context.Context, executionenv.EnvironmentRef, string, string, string) error
	Retire(context.Context, executionenv.EnvironmentRef, string) error
	File(context.Context, string, string, executionenv.FileRequest) (executionenv.FileResponse, error)
	StartCommand(context.Context, string, string, executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error)
	CommandStatus(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error)
	CancelCommand(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error)
}

// Handler is the authenticated private execution-provider gRPC service.
type Handler struct {
	executionv1.UnimplementedExecutionProviderServiceServer
	cfg     HandlerConfig
	backend Backend
}

// NewHandler constructs the authenticated private gRPC service.
func NewHandler(cfg HandlerConfig, b Backend) *Handler { return &Handler{cfg: cfg, backend: b} }

type authenticatedClient struct {
	id     string
	policy ClientPolicy
}

func (h *Handler) client(ctx context.Context) (authenticatedClient, error) {
	if h.backend == nil || (h.cfg.Ready != nil && !h.cfg.Ready()) {
		return authenticatedClient{}, wireError(executionenv.CodeNotReady, true)
	}
	p, ok := peer.FromContext(ctx)
	if !ok {
		return authenticatedClient{}, wireError(executionenv.CodeUnauthenticated, false)
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.PeerCertificates) == 0 {
		return authenticatedClient{}, wireError(executionenv.CodeUnauthenticated, false)
	}
	id, err := canonicalClientIdentity(tlsInfo.State.PeerCertificates[0])
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
	if q == nil || !c.policy.MayAttestOwner || !ok || q.BindingId == "" || len(q.BindingId) > executionenv.MaxBindingBytes || q.Profile == "" || len(q.Profile) > 63 {
		return nil, wireError(executionenv.CodePermissionDenied, false)
	}
	oh := ownerHash(owner)
	a, err := h.backend.Ensure(ctx, c.id, oh, q.BindingId, q.Profile, fingerprint(c.id, oh, q.BindingId, q.Profile))
	if err != nil {
		return nil, backendError(err)
	}
	grant, expiry, err := h.sign(a, c.id)
	if err != nil {
		return nil, wireError(executionenv.CodeInternal, false)
	}
	return ensureResponse(a, grant, expiry), nil
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
	grant, expiry, err := h.sign(a, c.id)
	if err != nil {
		return nil, wireError(executionenv.CodeInternal, false)
	}
	return attachResponse(a, grant, expiry), nil
}

// ReleaseReference releases one authorized durable binding reference.
func (h *Handler) ReleaseReference(ctx context.Context, q *executionv1.ReleaseReferenceRequest) (*emptypb.Empty, error) {
	c, owner, rc, err := h.authorize(ctx, q.GetContext(), executionenv.OpReferenceRelease, false)
	if err != nil {
		return nil, err
	}
	if err := h.backend.ReleaseReference(ctx, rc.Environment, c.id, owner, rc.BindingID); err != nil {
		return nil, backendError(err)
	}
	return &emptypb.Empty{}, nil
}

// RetireEnvironment requests authorized administrative retirement.
func (h *Handler) RetireEnvironment(ctx context.Context, q *executionv1.RetireEnvironmentRequest) (*emptypb.Empty, error) {
	c, owner, rc, err := h.authorize(ctx, q.GetContext(), executionenv.OpRetire, true)
	if err != nil {
		return nil, err
	}
	_ = c
	if err := h.backend.Retire(ctx, rc.Environment, owner); err != nil {
		return nil, backendError(err)
	}
	return &emptypb.Empty{}, nil
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
	c, owner, rc, err := h.authorize(ctx, q.Context, op, false)
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
	c, owner, rc, err := h.authorize(ctx, q.Context, executionenv.OpCommandStart, false)
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
	_, _, _, err := h.authorize(ctx, q.GetContext(), op, false)
	if err != nil {
		return nil, err
	}
	if q.CommandId == "" || q.Offset < 0 {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	return nil, status.Error(codes.Unimplemented, "foreground commands have no detached control API")
}

func (h *Handler) authorize(ctx context.Context, p *executionv1.RequestContext, op executionenv.Operation, admin bool) (authenticatedClient, string, executionenv.RequestContext, error) {
	c, err := h.client(ctx)
	if err != nil {
		return c, "", executionenv.RequestContext{}, err
	}
	if admin && !c.policy.Administrator {
		return c, "", executionenv.RequestContext{}, wireError(executionenv.CodePermissionDenied, false)
	}
	rc, ok := requestContextFromProto(p)
	if !ok {
		return c, "", rc, wireError(executionenv.CodePermissionDenied, false)
	}
	oh := ownerHash(rc.Owner)
	if _, err := h.cfg.Verifier.Verify(rc.Grant, executionenv.GrantExpectation{Client: c.id, OwnerHash: oh, BindingID: rc.BindingID, Environment: rc.Environment, Epoch: rc.Epoch, Operation: op}); err != nil {
		if errors.Is(err, executionenv.ErrGrantExpired) {
			return c, "", rc, wireError(executionenv.CodeUnauthenticated, true)
		}
		return c, "", rc, wireError(executionenv.CodePermissionDenied, false)
	}
	return c, oh, rc, nil
}

func requestContextFromProto(p *executionv1.RequestContext) (executionenv.RequestContext, bool) {
	owner, ownerOK := ownerFromProto(p.GetOwner())
	ref := refFromProto(p.GetEnvironment())
	rc := executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: p.GetBindingId(), Epoch: p.GetEpoch(), Grant: p.GetGrant()}
	ok := p != nil && ownerOK && ref.ID != "" && ref.Revision != "" && len(ref.ID) <= executionenv.MaxIdentityBytes && len(ref.Revision) <= executionenv.MaxIdentityBytes && rc.BindingID != "" && len(rc.BindingID) <= executionenv.MaxBindingBytes && rc.Epoch != 0 && rc.Grant != "" && len(rc.Grant) <= executionenv.MaxGrantBytes
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
	return &executionv1.EnsureEnvironmentResponse{Environment: refToProto(a.Environment), Epoch: a.Epoch, Ready: a.Ready, Grant: valid(grant), GrantExpiresAt: timestamppb.New(expiry)}
}
func attachResponse(a Allocation, grant string, expiry time.Time) *executionv1.AttachEnvironmentResponse {
	return &executionv1.AttachEnvironmentResponse{Environment: refToProto(a.Environment), Epoch: a.Epoch, Ready: a.Ready, Grant: valid(grant), GrantExpiresAt: timestamppb.New(expiry)}
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

func (h *Handler) sign(a Allocation, client string) (string, time.Time, error) {
	life := h.cfg.Signer.Lifetime
	if life <= 0 {
		life = time.Minute
	}
	now, expiry := time.Now().UTC(), time.Now().UTC().Add(life)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, err
	}
	ops := []executionenv.Operation{executionenv.OpAttach, executionenv.OpReferenceRelease, executionenv.OpRetire, executionenv.OpFileRead, executionenv.OpFileResolveAuthority, executionenv.OpFileStat, executionenv.OpFileCreate, executionenv.OpFileReplace, executionenv.OpFileList, executionenv.OpFileRemove, executionenv.OpFileRename, executionenv.OpFileCopy, executionenv.OpFileGlob, executionenv.OpFileGrep, executionenv.OpCommandStart, executionenv.OpCommandStatus, executionenv.OpCommandCancel}
	grant, err := executionenv.SignGrant(h.cfg.Signer.PrivateKey, executionenv.GrantClaims{KeyID: h.cfg.Signer.KeyID, Issuer: h.cfg.Signer.Issuer, Audience: h.cfg.Signer.Audience, Client: client, OwnerHash: a.OwnerHash, BindingID: a.BindingID, Environment: a.Environment, Epoch: a.Epoch, Operations: ops, NotBefore: now, ExpiresAt: expiry, Nonce: hex.EncodeToString(nonce)})
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
	case executionenv.CodeConflict, executionenv.CodeVersionMismatch:
		grpcCode = codes.Aborted
	case executionenv.CodeNotReady, executionenv.CodeFenceUnknown:
		grpcCode = codes.Unavailable
	case executionenv.CodeResourceExhausted:
		grpcCode = codes.ResourceExhausted
	}
	st := status.New(grpcCode, "execution provider request failed")
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
