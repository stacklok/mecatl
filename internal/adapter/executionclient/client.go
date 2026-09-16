// Package executionclient implements the private mTLS client and placement adapter
// for the independently deployed Kubernetes execution provider.
package executionclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpccredentials "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

const (
	remoteRoot                     = "/workspace"
	authorityResolveRequestTimeout = 10 * time.Second
)

// TLSFiles names the runtime-only mTLS material mounted into mecak8s.
type TLSFiles struct{ CA, Cert, Key string }

// LoadTLSConfig loads provider trust and client identity at process startup.
func LoadTLSConfig(files TLSFiles) (*tls.Config, error) {
	if files.CA == "" || files.Cert == "" || files.Key == "" {
		return nil, errors.New("execution client: CA, certificate, and key are required")
	}
	ca, err := os.ReadFile(files.CA)
	if err != nil {
		return nil, fmt.Errorf("execution client: load CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("execution client: CA contains no certificates")
	}
	cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
	if err != nil {
		return nil, fmt.Errorf("execution client: load client identity: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{cert}}, nil
}

// Client is a bounded client for the private execution-provider gRPC API.
type Client struct {
	conn *grpc.ClientConn
	rpc  executionv1.ExecutionProviderServiceClient
}

// New constructs a production mTLS execution-provider client. Endpoint is a
// plain host:port authority; URI schemes and alternate resolvers are rejected.
func New(endpoint string, tlsConfig *tls.Config) (*Client, error) {
	if strings.Contains(endpoint, "://") || strings.ContainsAny(endpoint, "/?#@") {
		return nil, errors.New("execution client: endpoint must be host:port")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || port == "" {
		return nil, errors.New("execution client: endpoint must be host:port")
	}
	if tlsConfig == nil || len(tlsConfig.Certificates) == 0 || tlsConfig.RootCAs == nil {
		return nil, errors.New("execution client: production mTLS configuration is required")
	}
	cfg := tlsConfig.Clone()
	if cfg.MinVersion < tls.VersionTLS13 {
		cfg.MinVersion = tls.VersionTLS13
	}
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(grpccredentials.NewTLS(cfg)),
		grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(executionenv.MaxMessageBytes), grpc.MaxCallSendMsgSize(executionenv.MaxMessageBytes)),
	)
	if err != nil {
		return nil, fmt.Errorf("execution client: connect: %w", err)
	}
	return &Client{conn: conn, rpc: executionv1.NewExecutionProviderServiceClient(conn)}, nil
}

// Close releases the provider connection.
func (c *Client) Close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

// ValidateProfile validates a provider profile without allocating an environment.
func (c *Client) ValidateProfile(ctx context.Context, profile string) (executionenv.ValidateProfileResponse, error) {
	v, err := c.rpc.ValidateProfile(ctx, &executionv1.ValidateProfileRequest{Profile: profile})
	if err != nil {
		return executionenv.ValidateProfileResponse{}, decodeError(ctx, err)
	}
	return executionenv.ValidateProfileResponse{Profile: v.Profile, Digest: v.Digest, Capabilities: v.Capabilities, MaxFileBytes: v.MaxFileBytes, MaxCommandBytes: v.MaxCommandBytes, MaxCommandDurationMillis: v.MaxCommandDurationMillis}, nil
}

// Ensure idempotently allocates or resolves the environment for a binding.
func (c *Client) Ensure(ctx context.Context, binding, profile string, owner executionenv.Owner) (executionenv.EnsureEnvironmentResponse, error) {
	v, err := c.rpc.EnsureEnvironment(ctx, &executionv1.EnsureEnvironmentRequest{BindingId: binding, Profile: profile, Owner: ownerToProto(owner)})
	if err != nil {
		return executionenv.EnsureEnvironmentResponse{}, decodeError(ctx, err)
	}
	return ensureFromProto(v)
}

// Attach obtains a fresh grant for an exact environment reference.
func (c *Client) Attach(ctx context.Context, req executionenv.AttachEnvironmentRequest) (executionenv.AttachEnvironmentResponse, error) {
	v, err := c.rpc.AttachEnvironment(ctx, &executionv1.AttachEnvironmentRequest{Context: contextToProto(req.Context), Purpose: req.Purpose})
	if err != nil {
		return executionenv.AttachEnvironmentResponse{}, decodeError(ctx, err)
	}
	return attachFromProto(v)
}

// File executes one authorized filesystem operation.
func (c *Client) File(ctx context.Context, req executionenv.FileRequest) (executionenv.FileResponse, error) {
	if req.Limit < 0 || req.Limit > executionenv.MaxListEntries {
		return executionenv.FileResponse{}, &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "invalid file request limit"}
	}
	v, err := c.rpc.Files(ctx, &executionv1.FileRequest{Context: contextToProto(req.Context), Operation: fileOperationToProto(req.Operation), Path: req.Path, Destination: req.Destination, Pattern: req.Pattern, Data: req.Data, Version: []byte(req.Version), Limit: int32(req.Limit)}) //nolint:gosec // bounded above
	if err != nil {
		return executionenv.FileResponse{}, decodeError(ctx, err)
	}
	return fileFromProto(v), nil
}

// StartCommand executes one foreground command.
func (c *Client) StartCommand(ctx context.Context, req executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error) {
	v, err := c.rpc.StartCommand(ctx, &executionv1.CommandStartRequest{Context: contextToProto(req.Context), Command: req.Command, TimeoutMillis: req.TimeoutMillis})
	if err != nil {
		return executionenv.CommandStartResponse{}, decodeError(ctx, err)
	}
	return commandStartFromProto(v)
}

// CommandStatus queries a command after the request has been authorized.
func (c *Client) CommandStatus(ctx context.Context, req executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	v, err := c.rpc.CommandStatus(ctx, &executionv1.CommandQueryRequest{Context: contextToProto(req.Context), CommandId: req.CommandID, Offset: req.Offset})
	if err != nil {
		return executionenv.CommandStatusResponse{}, decodeError(ctx, err)
	}
	return commandStatusFromProto(v)
}

// CancelCommand cancels a command after the request has been authorized.
func (c *Client) CancelCommand(ctx context.Context, req executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	v, err := c.rpc.CancelCommand(ctx, &executionv1.CommandQueryRequest{Context: contextToProto(req.Context), CommandId: req.CommandID, Offset: req.Offset})
	if err != nil {
		return executionenv.CommandStatusResponse{}, decodeError(ctx, err)
	}
	return commandStatusFromProto(v)
}

// ReleaseReference releases one durable binding reference.
func (c *Client) ReleaseReference(ctx context.Context, req executionenv.ReferenceReleaseRequest) error {
	_, err := c.rpc.ReleaseReference(ctx, &executionv1.ReleaseReferenceRequest{Context: contextToProto(req.Context)})
	return decodeError(ctx, err)
}

// RetireEnvironment requests administrative retirement.
func (c *Client) RetireEnvironment(ctx context.Context, req executionenv.RetireEnvironmentRequest) error {
	_, err := c.rpc.RetireEnvironment(ctx, &executionv1.RetireEnvironmentRequest{Context: contextToProto(req.Context)})
	return decodeError(ctx, err)
}

func decodeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	st, ok := status.FromError(err)
	if !ok || len(st.Details()) != 1 {
		return sanitizedProviderError()
	}
	detail, ok := st.Details()[0].(*executionv1.ErrorDetail)
	if !ok {
		return sanitizedProviderError()
	}
	code := executionenv.ErrorCode(detail.Code)
	if !code.Valid() || st.Code() != grpcCodeForError(code) || (detail.Retryable && code != executionenv.CodeNotReady && code != executionenv.CodeUnauthenticated) {
		return sanitizedProviderError()
	}
	return &executionenv.Error{Code: code, Message: "execution provider request failed", Retryable: detail.Retryable}
}

func sanitizedProviderError() error {
	return &executionenv.Error{Code: executionenv.CodeInternal, Message: "execution provider request failed"}
}

func grpcCodeForError(code executionenv.ErrorCode) codes.Code {
	switch code {
	case executionenv.CodeInvalidArgument:
		return codes.InvalidArgument
	case executionenv.CodeUnauthenticated:
		return codes.Unauthenticated
	case executionenv.CodePermissionDenied:
		return codes.PermissionDenied
	case executionenv.CodeNotFound:
		return codes.NotFound
	case executionenv.CodeAlreadyExists:
		return codes.AlreadyExists
	case executionenv.CodeConflict, executionenv.CodeVersionMismatch:
		return codes.Aborted
	case executionenv.CodeNotReady, executionenv.CodeFenceUnknown:
		return codes.Unavailable
	case executionenv.CodeResourceExhausted:
		return codes.ResourceExhausted
	default:
		return codes.Internal
	}
}
func ownerToProto(o executionenv.Owner) *executionv1.Owner {
	return &executionv1.Owner{Issuer: o.Issuer, Subject: o.Subject}
}
func refToProto(r executionenv.EnvironmentRef) *executionv1.EnvironmentRef {
	return &executionv1.EnvironmentRef{Id: r.ID, Revision: r.Revision}
}
func contextToProto(v executionenv.RequestContext) *executionv1.RequestContext {
	return &executionv1.RequestContext{Environment: refToProto(v.Environment), Owner: ownerToProto(v.Owner), BindingId: v.BindingID, Epoch: v.Epoch, Grant: v.Grant}
}
func refFromProto(r *executionv1.EnvironmentRef) executionenv.EnvironmentRef {
	return executionenv.EnvironmentRef{ID: r.GetId(), Revision: r.GetRevision()}
}
func ensureFromProto(v *executionv1.EnsureEnvironmentResponse) (executionenv.EnsureEnvironmentResponse, error) {
	expiry, err := checkedTime(v.GetGrantExpiresAt())
	return executionenv.EnsureEnvironmentResponse{Environment: refFromProto(v.GetEnvironment()), Epoch: v.GetEpoch(), Ready: v.GetReady(), Grant: v.GetGrant(), GrantExpiresAt: expiry}, err
}
func attachFromProto(v *executionv1.AttachEnvironmentResponse) (executionenv.AttachEnvironmentResponse, error) {
	expiry, err := checkedTime(v.GetGrantExpiresAt())
	return executionenv.AttachEnvironmentResponse{Environment: refFromProto(v.GetEnvironment()), Epoch: v.GetEpoch(), Ready: v.GetReady(), Grant: v.GetGrant(), GrantExpiresAt: expiry}, err
}
func checkedTime(v interface {
	CheckValid() error
	AsTime() time.Time
}) (time.Time, error) {
	if v == nil {
		return time.Time{}, errors.New("execution provider omitted timestamp")
	}
	if err := v.CheckValid(); err != nil {
		return time.Time{}, errors.New("execution provider returned invalid timestamp")
	}
	return v.AsTime(), nil
}
func fileOperationToProto(op executionenv.Operation) executionv1.FileOperation {
	switch op {
	case executionenv.OpFileRead:
		return executionv1.FileOperation_FILE_OPERATION_READ
	case executionenv.OpFileResolveAuthority:
		return executionv1.FileOperation_FILE_OPERATION_RESOLVE_AUTHORITY
	case executionenv.OpFileStat:
		return executionv1.FileOperation_FILE_OPERATION_STAT
	case executionenv.OpFileCreate:
		return executionv1.FileOperation_FILE_OPERATION_CREATE
	case executionenv.OpFileReplace:
		return executionv1.FileOperation_FILE_OPERATION_REPLACE
	case executionenv.OpFileList:
		return executionv1.FileOperation_FILE_OPERATION_LIST
	case executionenv.OpFileRemove:
		return executionv1.FileOperation_FILE_OPERATION_REMOVE
	case executionenv.OpFileRename:
		return executionv1.FileOperation_FILE_OPERATION_RENAME
	case executionenv.OpFileCopy:
		return executionv1.FileOperation_FILE_OPERATION_COPY
	case executionenv.OpFileGlob:
		return executionv1.FileOperation_FILE_OPERATION_GLOB
	case executionenv.OpFileGrep:
		return executionv1.FileOperation_FILE_OPERATION_GREP
	}
	return executionv1.FileOperation_FILE_OPERATION_UNSPECIFIED
}
func fileFromProto(v *executionv1.FileResponse) executionenv.FileResponse {
	r := executionenv.FileResponse{Data: v.GetData(), Version: string(v.GetVersion()), Paths: v.GetPaths(), AuthorityTarget: v.GetAuthorityTarget(), AuthorityWorkspace: v.GetAuthorityWorkspace()}
	if v.Info != nil {
		x := fileInfoFromProto(v.Info)
		r.Info = &x
	}
	for _, x := range v.Entries {
		r.Entries = append(r.Entries, fileInfoFromProto(x))
	}
	for _, x := range v.Matches {
		r.Matches = append(r.Matches, executionenv.GrepMatch{Path: x.Path, Line: int(x.Line), Text: x.Text})
	}
	return r
}
func fileInfoFromProto(v *executionv1.FileInfo) executionenv.FileInfo {
	var mt time.Time
	if v.ModTime != nil && v.ModTime.CheckValid() == nil {
		mt = v.ModTime.AsTime()
	}
	return executionenv.FileInfo{Name: v.Name, Size: v.Size, Mode: v.Mode, ModTime: mt, IsDir: v.IsDir}
}
func commandStartFromProto(v *executionv1.CommandStartResponse) (executionenv.CommandStartResponse, error) {
	if v == nil {
		return executionenv.CommandStartResponse{}, sanitizedProviderError()
	}
	state, ok := commandStateFromProto(v.GetState())
	if !ok {
		return executionenv.CommandStartResponse{}, sanitizedProviderError()
	}
	result, err := commandStatusFromProto(v.GetResult())
	if err != nil {
		return executionenv.CommandStartResponse{}, err
	}
	return executionenv.CommandStartResponse{CommandID: v.GetCommandId(), State: state, Result: result}, nil
}
func commandStatusFromProto(v *executionv1.CommandStatusResponse) (executionenv.CommandStatusResponse, error) {
	if v == nil {
		return executionenv.CommandStatusResponse{}, sanitizedProviderError()
	}
	state, ok := commandStateFromProto(v.State)
	if !ok {
		return executionenv.CommandStatusResponse{}, sanitizedProviderError()
	}
	return executionenv.CommandStatusResponse{CommandID: v.CommandId, State: state, ExitCode: int(v.ExitCode), Stdout: v.Stdout, Stderr: v.Stderr, NextOffset: v.NextOffset, Truncated: v.Truncated, TerminalReceipt: v.TerminalReceipt}, nil
}
func commandStateFromProto(v executionv1.CommandState) (executionenv.CommandState, bool) {
	switch v {
	case executionv1.CommandState_COMMAND_STATE_RUNNING:
		return executionenv.CommandRunning, true
	case executionv1.CommandState_COMMAND_STATE_SUCCEEDED:
		return executionenv.CommandSucceeded, true
	case executionv1.CommandState_COMMAND_STATE_FAILED:
		return executionenv.CommandFailed, true
	case executionv1.CommandState_COMMAND_STATE_CANCELLED:
		return executionenv.CommandCancelled, true
	case executionv1.CommandState_COMMAND_STATE_FENCE_UNKNOWN:
		return executionenv.CommandFenceUnknown, true
	default:
		return "", false
	}
}

// Provider adapts the execution service to server placement binding.
type Provider struct {
	client  *Client
	profile string
}

// NewProvider binds a client to one operator-selected profile.
func NewProvider(client *Client, profile string) (*Provider, error) {
	if client == nil || profile == "" {
		return nil, errors.New("execution client: client and profile are required")
	}
	return &Provider{client: client, profile: profile}, nil
}

// ValidatePlacement performs side-effect-free profile preflight.
func (p *Provider) ValidatePlacement(ctx context.Context) error {
	_, err := p.client.ValidateProfile(ctx, p.profile)
	return mapPlacementError(err)
}

func ownerOf(principal *session.Principal) (executionenv.Owner, error) {
	if principal == nil || principal.Issuer == "" || principal.Subject == "" {
		return executionenv.Owner{}, server.ErrPlacementNotFound
	}
	return executionenv.Owner{Issuer: principal.Issuer, Subject: principal.Subject}, nil
}

// Bind allocates the environment for the final session binding.
func (p *Provider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if !req.Selector.IsDefault() || req.BindingID == "" {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	owner, err := ownerOf(req.Principal)
	if err != nil {
		return server.PlacementBinding{}, err
	}
	ensured, err := p.client.Ensure(ctx, string(req.BindingID), p.profile, owner)
	if err != nil {
		return server.PlacementBinding{}, mapPlacementError(err)
	}
	if ensured.Environment.ID == "" || ensured.Environment.Revision == "" {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return p.waitForBinding(ctx, owner, string(req.BindingID), ensured.Environment)
}

// Reattach exactly resolves a persisted remote environment for its binding.
func (p *Provider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.BindingID == "" {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	owner, err := ownerOf(req.Principal)
	if err != nil {
		return server.PlacementBinding{}, err
	}
	ref := executionenv.EnvironmentRef{ID: req.Ref.ID, Revision: req.Ref.Revision}
	attached, err := p.client.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: string(req.BindingID)}, Purpose: executionenv.PurposeSession})
	if err != nil {
		return server.PlacementBinding{}, mapPlacementError(err)
	}
	if toSessionRef(attached.Environment) != req.Ref {
		return server.PlacementBinding{}, server.ErrPlacementChanged
	}
	return p.binding(owner, string(req.BindingID), attached)
}

func (p *Provider) waitForBinding(ctx context.Context, owner executionenv.Owner, binding string, ref executionenv.EnvironmentRef) (server.PlacementBinding, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		attached, err := p.client.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: binding}, Purpose: executionenv.PurposeSession})
		if err == nil && attached.Ready {
			if attached.Environment != ref {
				return server.PlacementBinding{}, server.ErrPlacementChanged
			}
			return p.binding(owner, binding, attached)
		}
		if err != nil {
			var remote *executionenv.Error
			if !errors.As(err, &remote) || (remote.Code != executionenv.CodeNotReady && !remote.Retryable) {
				return server.PlacementBinding{}, mapPlacementError(err)
			}
		}
		select {
		case <-ctx.Done():
			return server.PlacementBinding{}, server.ErrPlacementUnavailable
		case <-ticker.C:
		}
	}
}

func (p *Provider) binding(owner executionenv.Owner, binding string, attached executionenv.AttachEnvironmentResponse) (server.PlacementBinding, error) {
	if !attached.Ready || attached.Environment.ID == "" || attached.Environment.Revision == "" || attached.Epoch == 0 || attached.Grant == "" || attached.GrantExpiresAt.IsZero() {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	credentials := &grantContext{client: p.client, context: executionenv.RequestContext{Environment: attached.Environment, Owner: owner, BindingID: binding, Epoch: attached.Epoch, Grant: attached.Grant}, expiresAt: attached.GrantExpiresAt}
	ws := &workspace{credentials: credentials}
	runner := &runner{credentials: credentials}
	sref := toSessionRef(attached.Environment)
	env, err := tool.NewEnvironment(sref, ws, memledger.New(), runner)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{Environment: env, Ref: sref, Metadata: server.PlacementMetadata{Kind: "kubernetes", Label: "Remote Kubernetes workspace", Revision: attached.Environment.Revision}}, nil
}
func toSessionRef(ref executionenv.EnvironmentRef) session.EnvironmentRef {
	return session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: ref.ID, Revision: ref.Revision}
}
func mapPlacementError(err error) error {
	if err == nil {
		return nil
	}
	var remote *executionenv.Error
	if errors.As(err, &remote) {
		switch remote.Code {
		case executionenv.CodeNotFound, executionenv.CodePermissionDenied, executionenv.CodeUnauthenticated:
			return server.ErrPlacementNotFound
		case executionenv.CodeConflict, executionenv.CodeVersionMismatch:
			return server.ErrPlacementChanged
		case executionenv.CodeInvalidArgument:
			return server.ErrInvalidPlacementSelection
		}
	}
	return server.ErrPlacementUnavailable
}

type grantContext struct {
	mu        sync.Mutex
	client    *Client
	context   executionenv.RequestContext
	expiresAt time.Time
}

func (g *grantContext) current(ctx context.Context) (executionenv.RequestContext, error) {
	return g.renew(ctx, false)
}

func (g *grantContext) refresh(ctx context.Context) (executionenv.RequestContext, error) {
	return g.renew(ctx, true)
}

func (g *grantContext) renew(ctx context.Context, force bool) (executionenv.RequestContext, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !force && time.Until(g.expiresAt) > 10*time.Second {
		return g.context, nil
	}
	attached, err := g.client.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: g.context.Environment, Owner: g.context.Owner, BindingID: g.context.BindingID}, Purpose: executionenv.PurposeSession})
	if err != nil {
		return executionenv.RequestContext{}, err
	}
	if !attached.Ready || attached.Environment != g.context.Environment || attached.Epoch != g.context.Epoch || attached.Grant == "" || attached.GrantExpiresAt.IsZero() {
		return executionenv.RequestContext{}, server.ErrPlacementChanged
	}
	g.context.Grant = attached.Grant
	g.expiresAt = attached.GrantExpiresAt
	return g.context, nil
}

// workspace is bound to one provider-issued environment grant.
type workspace struct {
	credentials *grantContext
	opMu        sync.Mutex
}

var _ tool.AuthorityResourceResolver = (*workspace)(nil)

func (*workspace) Root() string { return remoteRoot }

// AuthorityResourcePath asks the authenticated executor to derive the same
// physical, confined identity that the subsequent filesystem operation uses.
func (w *workspace) AuthorityResourcePath(path string) (target, root string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), authorityResolveRequestTimeout)
	defer cancel()
	response, err := w.call(ctx, executionenv.OpFileResolveAuthority, path, "", "", nil, "")
	if err != nil {
		return "", "", err
	}
	if response.AuthorityWorkspace != remoteRoot || (response.AuthorityTarget != remoteRoot && !strings.HasPrefix(response.AuthorityTarget, remoteRoot+"/")) {
		return "", "", errors.New("execution provider returned an invalid authority resource identity")
	}
	return response.AuthorityTarget, response.AuthorityWorkspace, nil
}

func (w *workspace) call(ctx context.Context, op executionenv.Operation, path, dest, pattern string, data []byte, version string) (executionenv.FileResponse, error) {
	// The provider's CRD deliberately carries one active operation. The engine may
	// dispatch read-only sibling tools concurrently, so serialize this bound
	// workspace before claiming that provider-side operation slot.
	w.opMu.Lock()
	defer w.opMu.Unlock()
	credentials, err := w.credentials.current(ctx)
	if err != nil {
		return executionenv.FileResponse{}, mapFileError(path, err)
	}
	limit := 0
	if op == executionenv.OpFileList || op == executionenv.OpFileGlob || op == executionenv.OpFileGrep {
		limit = executionenv.MaxListEntries
	}
	request := executionenv.FileRequest{Context: credentials, Operation: op, Path: path, Destination: dest, Pattern: pattern, Data: data, Version: version, Limit: limit}
	out, err := w.credentials.client.File(ctx, request)
	if isDefinitiveAuthDenial(err) && readOnlyFileOperation(op) {
		credentials, refreshErr := w.credentials.refresh(ctx)
		if refreshErr != nil {
			return executionenv.FileResponse{}, mapFileError(path, refreshErr)
		}
		request.Context = credentials
		out, err = w.credentials.client.File(ctx, request)
	}
	return out, mapFileError(path, err)
}

func isDefinitiveAuthDenial(err error) bool {
	var remote *executionenv.Error
	return errors.As(err, &remote) && remote.Code == executionenv.CodeUnauthenticated && remote.Retryable
}

func readOnlyFileOperation(op executionenv.Operation) bool {
	switch op {
	case executionenv.OpFileRead, executionenv.OpFileResolveAuthority, executionenv.OpFileStat, executionenv.OpFileList, executionenv.OpFileGlob, executionenv.OpFileGrep:
		return true
	default:
		return false
	}
}
func (w *workspace) Read(ctx context.Context, path string) ([]byte, error) {
	r, e := w.call(ctx, executionenv.OpFileRead, path, "", "", nil, "")
	return r.Data, e
}
func (w *workspace) ReadVersion(ctx context.Context, path string) ([]byte, tool.FileVersion, error) {
	r, e := w.call(ctx, executionenv.OpFileRead, path, "", "", nil, "")
	if e != nil {
		return nil, tool.FileVersion{}, e
	}
	return r.Data, tool.DecodeFileVersion(r.Version), nil
}
func (w *workspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	r, e := w.call(ctx, executionenv.OpFileStat, path, "", "", nil, "")
	if e != nil {
		return tool.FileInfo{}, e
	}
	if r.Info == nil {
		return tool.FileInfo{}, errors.New("execution provider omitted file metadata")
	}
	return fileInfo(*r.Info), nil
}
func (w *workspace) CreateFile(ctx context.Context, path string, data []byte) (tool.FileVersion, error) {
	r, e := w.call(ctx, executionenv.OpFileCreate, path, "", "", data, "")
	if e != nil {
		return tool.FileVersion{}, e
	}
	return tool.DecodeFileVersion(r.Version), nil
}
func (w *workspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	v, e := tool.EncodeFileVersion(old)
	if e != nil {
		return tool.FileVersion{}, e
	}
	r, e := w.call(ctx, executionenv.OpFileReplace, path, "", "", data, v)
	if e != nil {
		return tool.FileVersion{}, e
	}
	return tool.DecodeFileVersion(r.Version), nil
}
func (w *workspace) ReadDir(ctx context.Context, path string) ([]tool.FileInfo, error) {
	r, e := w.call(ctx, executionenv.OpFileList, path, "", "", nil, "")
	if e != nil {
		return nil, e
	}
	out := make([]tool.FileInfo, len(r.Entries))
	for i := range r.Entries {
		out[i] = fileInfo(r.Entries[i])
	}
	return out, nil
}
func (w *workspace) Remove(ctx context.Context, path string) error {
	_, e := w.call(ctx, executionenv.OpFileRemove, path, "", "", nil, "")
	return e
}
func (w *workspace) Rename(ctx context.Context, a, b string) error {
	_, e := w.call(ctx, executionenv.OpFileRename, a, b, "", nil, "")
	return e
}
func (w *workspace) CopyFile(ctx context.Context, a, b string) (tool.FileVersion, error) {
	r, e := w.call(ctx, executionenv.OpFileCopy, a, b, "", nil, "")
	if e != nil {
		return tool.FileVersion{}, e
	}
	return tool.DecodeFileVersion(r.Version), nil
}
func (w *workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	r, e := w.call(ctx, executionenv.OpFileGlob, "", "", pattern, nil, "")
	return r.Paths, e
}
func (w *workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	r, e := w.call(ctx, executionenv.OpFileGrep, pathGlob, "", pattern, nil, "")
	if e != nil {
		return nil, e
	}
	out := make([]tool.GrepMatch, len(r.Matches))
	for i, m := range r.Matches {
		out[i] = tool.GrepMatch{Path: m.Path, Line: m.Line, Text: m.Text}
	}
	return out, nil
}
func fileInfo(i executionenv.FileInfo) tool.FileInfo {
	return tool.FileInfo{Name: i.Name, Size: i.Size, Mode: fs.FileMode(i.Mode), ModTime: i.ModTime, IsDir: i.IsDir}
}
func mapFileError(path string, err error) error {
	if err == nil {
		return nil
	}
	var remote *executionenv.Error
	if errors.As(err, &remote) {
		switch remote.Code {
		case executionenv.CodeNotFound:
			return fmt.Errorf("%s: %w", path, fs.ErrNotExist)
		case executionenv.CodeAlreadyExists:
			return fmt.Errorf("%s: %w", path, fs.ErrExist)
		case executionenv.CodeVersionMismatch:
			return &tool.VersionMismatchError{Path: path}
		}
	}
	return err
}

// runner deliberately implements only foreground CommandRunner.
type runner struct {
	credentials *grantContext
}

func (*runner) BoundWorkspaceRoot() string { return remoteRoot }
func (r *runner) Run(ctx context.Context, command string) (tool.CommandResult, error) {
	credentials, err := r.credentials.current(ctx)
	if err != nil {
		return tool.CommandResult{}, err
	}
	resp, err := r.credentials.client.StartCommand(ctx, executionenv.CommandStartRequest{Context: credentials, Command: command})
	if err != nil {
		return tool.CommandResult{}, err
	}
	result := resp.Result
	out := tool.CommandResult{Stdout: session.ToValidUTF8(string(result.Stdout)), Stderr: session.ToValidUTF8(string(result.Stderr)), ExitCode: result.ExitCode}
	switch result.State {
	case executionenv.CommandSucceeded, executionenv.CommandFailed:
		return out, nil
	case executionenv.CommandCancelled:
		return out, context.Canceled
	case executionenv.CommandFenceUnknown:
		return out, errors.New("execution provider lost terminal command receipt; environment fencing is unknown")
	default:
		return out, errors.New("execution provider returned a non-terminal foreground command")
	}
}
