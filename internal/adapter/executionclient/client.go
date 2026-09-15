// Package executionclient implements the private mTLS client and placement adapter
// for the independently deployed Kubernetes execution provider.
package executionclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

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

// Client is a bounded client for the private execution-provider HTTP API.
type Client struct {
	endpoint *url.URL
	http     *http.Client
}

// New constructs a production mTLS execution-provider client.
func New(endpoint string, tlsConfig *tls.Config) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("execution client: endpoint must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	if tlsConfig == nil || len(tlsConfig.Certificates) == 0 || tlsConfig.RootCAs == nil {
		return nil, errors.New("execution client: production mTLS configuration is required")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	tr := &http.Transport{TLSClientConfig: tlsConfig.Clone(), DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second}
	return &Client{endpoint: u, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// NewWithHTTPClient is a hermetic test seam; production callers use New.
func NewWithHTTPClient(endpoint string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" || hc == nil {
		return nil, errors.New("execution client: invalid endpoint or HTTP client")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return &Client{endpoint: u, http: hc}, nil
}

// Close releases pooled provider connections.
func (c *Client) Close() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String()+executionenv.BasePath+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("execution provider request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, executionenv.MaxJSONBody+1))
	if err != nil {
		return fmt.Errorf("execution provider response failed: %w", err)
	}
	if len(data) > executionenv.MaxJSONBody {
		return errors.New("execution provider response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope executionenv.ErrorResponse
		if executionenv.DecodeStrict(data, &envelope) == nil && envelope.Error != nil {
			return envelope.Error
		}
		return fmt.Errorf("execution provider returned HTTP %d", resp.StatusCode)
	}
	if err := executionenv.DecodeStrict(data, out); err != nil {
		return fmt.Errorf("execution provider returned invalid response: %w", err)
	}
	return nil
}

// ValidateProfile validates a provider profile without allocating an environment.
func (c *Client) ValidateProfile(ctx context.Context, profile string) (executionenv.ValidateProfileResponse, error) {
	var out executionenv.ValidateProfileResponse
	err := c.post(ctx, "/profiles/validate", executionenv.ValidateProfileRequest{Profile: profile}, &out)
	return out, err
}

// Ensure idempotently allocates or resolves the environment for a binding.
func (c *Client) Ensure(ctx context.Context, binding, profile string, owner executionenv.Owner) (executionenv.EnsureEnvironmentResponse, error) {
	var out executionenv.EnsureEnvironmentResponse
	err := c.post(ctx, "/environments/ensure", executionenv.EnsureEnvironmentRequest{BindingID: binding, Profile: profile, Owner: owner}, &out)
	return out, err
}

// Attach obtains a fresh grant for an exact environment reference.
func (c *Client) Attach(ctx context.Context, req executionenv.AttachEnvironmentRequest) (executionenv.AttachEnvironmentResponse, error) {
	var out executionenv.AttachEnvironmentResponse
	err := c.post(ctx, "/environments/attach", req, &out)
	return out, err
}

// File executes one authorized filesystem operation.
func (c *Client) File(ctx context.Context, req executionenv.FileRequest) (executionenv.FileResponse, error) {
	var out executionenv.FileResponse
	err := c.post(ctx, "/files", req, &out)
	return out, err
}

// StartCommand executes one foreground command.
func (c *Client) StartCommand(ctx context.Context, req executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error) {
	var out executionenv.CommandStartResponse
	err := c.post(ctx, "/commands/start", req, &out)
	return out, err
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
	out, err := w.credentials.client.File(ctx, executionenv.FileRequest{Context: credentials, Operation: op, Path: path, Destination: dest, Pattern: pattern, Data: data, Version: version, Limit: executionenv.MaxListEntries})
	if isDefinitiveAuthDenial(err) && readOnlyFileOperation(op) {
		credentials, refreshErr := w.credentials.refresh(ctx)
		if refreshErr != nil {
			return executionenv.FileResponse{}, mapFileError(path, refreshErr)
		}
		out, err = w.credentials.client.File(ctx, executionenv.FileRequest{Context: credentials, Operation: op, Path: path, Destination: dest, Pattern: pattern, Data: data, Version: version, Limit: executionenv.MaxListEntries})
	}
	return out, mapFileError(path, err)
}

func isDefinitiveAuthDenial(err error) bool {
	var remote *executionenv.Error
	return errors.As(err, &remote) && (remote.Code == executionenv.CodePermissionDenied || remote.Code == executionenv.CodeUnauthenticated)
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
