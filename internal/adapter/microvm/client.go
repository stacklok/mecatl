// Package microvm provides the root module's thin UDS adapter for microvmd.
// It deliberately duplicates only the versioned JSON wire shapes and does not
// import the nested runtime module.
package microvm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	// DaemonProtocolVersion is the exact local management protocol spoken by this client.
	DaemonProtocolVersion                    uint16 = 4
	protocolVersion                                 = DaemonProtocolVersion
	maxMessageBytes                                 = 1 << 20
	maxInventoryPageSize                            = 64
	defaultInventoryPageSize                        = 50
	maxInventoryTokenBytes                          = 1024
	cleanupPhaseTimeout                             = 5 * time.Second
	kindMicroVM                                     = session.EnvironmentKind("microvm")
	publicGuestRoot                                 = "/workspace"
	operationDelete                                 = "delete"
	operationChildDelete                            = "child-delete"
	repositoryLogicalRootUnavailableCategory        = "repository_logical_root_unavailable"
)

type repositoryLogicalRootUnavailableError struct{}

func (repositoryLogicalRootUnavailableError) Error() string {
	return "microvmd: repository logical root is unavailable"
}
func (repositoryLogicalRootUnavailableError) EnvironmentLifecycleCategory() string {
	return repositoryLogicalRootUnavailableCategory
}

// ErrRepositoryLogicalRootUnavailable is returned when an authenticated guest
// cannot attach the prepared repository worktree. It carries no daemon detail.
var (
	ErrRepositoryLogicalRootUnavailable error = repositoryLogicalRootUnavailableError{}
	errRollbackRetained                       = errors.New("microvmd retained dirty unpublished placement for recovery")
)

type binding struct {
	Owner         string `json:"owner"`
	SessionID     string `json:"session_id"`
	EnvironmentID string `json:"environment_id"`
	Ref           string `json:"ref"`
	Generation    uint32 `json:"generation"`
}

type provisionRequest struct {
	Owner          string `json:"owner"`
	SessionID      string `json:"session_id"`
	Profile        string `json:"profile"`
	SourceCheckout string `json:"source_checkout"`
}

type lifecycleRequest struct {
	Version       uint16            `json:"version"`
	Operation     string            `json:"operation"`
	Binding       binding           `json:"binding"`
	AcquisitionID string            `json:"acquisition_id,omitempty"`
	Provision     *provisionRequest `json:"provision,omitempty"`
	Payload       json.RawMessage   `json:"payload,omitempty"`
}

type environmentRef struct {
	Kind string
	ID   string
}
type created struct {
	Ref          environmentRef `json:"ref"`
	Generation   uint32         `json:"generation"`
	HostWorktree string         `json:"host_worktree"`
	GuestRoot    string         `json:"guest_root"`
	Profile      string         `json:"profile"`
	GuestEgress  string         `json:"guest_egress"`
	HostEgress   string         `json:"host_egress"`
}
type execStreamFrame struct {
	Channel string `json:"channel"`
	Data    []byte `json:"data"`
}

type lifecycleResponse struct {
	Binding       binding          `json:"binding,omitempty"`
	AcquisitionID string           `json:"acquisition_id,omitempty"`
	Created       *created         `json:"created,omitempty"`
	Stream        *execStreamFrame `json:"stream,omitempty"`
	Payload       json.RawMessage  `json:"payload,omitempty"`
	ErrorCode     string           `json:"error_code,omitempty"`
	ErrorText     string           `json:"error,omitempty"`
}

// Client speaks the authenticated local management protocol. Authentication is
// performed by microvmd from kernel peer credentials before it decodes a frame.
type Client struct {
	endpoint                   string
	sourceCheckout             string
	profile                    string
	scope                      server.PlacementScope
	readiness                  func(context.Context) error
	cleanupTimeout             time.Duration
	acquisitionDial            func(context.Context, string, string) (net.Conn, error)
	acquisitionResponseDecoded func()
	acquisitionAfterFunc       func(context.Context, func()) func() bool
}

type acquisition struct {
	client  *Client
	conn    net.Conn
	binding binding
	id      string
	once    sync.Once
	err     error
}

func (a *acquisition) terminal(operation string) error {
	if a == nil {
		return nil
	}
	a.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), a.client.cleanupTimeout)
		defer cancel()
		stop := context.AfterFunc(ctx, func() { _ = a.conn.Close() })
		defer stop()
		request := lifecycleRequest{Version: protocolVersion, Operation: operation, Binding: a.binding, AcquisitionID: a.id}
		if err := writeFrame(a.conn, request); err != nil {
			a.err = err
			_ = a.conn.Close()
			return
		}
		var response lifecycleResponse
		if err := readFrame(a.conn, &response); err != nil {
			a.err = err
			_ = a.conn.Close()
			return
		}
		_ = a.conn.Close()
		if response.ErrorCode != "" {
			a.err = fmt.Errorf("microvmd %s: %s", response.ErrorCode, response.ErrorText)
			return
		}
		if response.Binding != a.binding || response.AcquisitionID != a.id {
			a.err = errors.New("microvmd terminal response changed acquisition")
			return
		}
		if operation == operationDelete || operation == operationChildDelete {
			var result DeleteResult
			if len(response.Payload) == 0 {
				a.err = errors.New("microvmd terminal cleanup omitted result; cleanup status is unknown")
				return
			}
			if err := json.Unmarshal(response.Payload, &result); err != nil {
				a.err = fmt.Errorf("decode microvmd terminal cleanup result: %w", err)
				return
			}
			if result.WorktreeRetained {
				a.err = errRollbackRetained
			}
		}
	})
	return a.err
}

// New validates endpoint and constructs a thin lifecycle client.
func New(endpoint string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "unix" || u.Host != "" || u.Path == "" {
		return nil, errors.New("microvmd endpoint must be an absolute unix:// path")
	}
	return &Client{endpoint: u.Path, cleanupTimeout: cleanupPhaseTimeout}, nil
}

// NewPlacementProvider constructs a deployment-owned microVM default placement.
func NewPlacementProvider(endpoint, sourceCheckout, profile string, scope server.PlacementScope, readiness func(context.Context) error) (*Client, error) {
	client, err := New(endpoint)
	if err != nil {
		return nil, err
	}
	if sourceCheckout == "" || profile == "" || scope == "" {
		return nil, errors.New("microvm placement requires source checkout, profile, and scope")
	}
	client.sourceCheckout = sourceCheckout
	client.profile = profile
	client.scope = scope
	client.readiness = readiness
	return client, nil
}

// Bind provisions either the trusted deployment default or explicit no-FS attenuation.
func (c *Client) Bind(ctx context.Context, request server.PlacementBindRequest) (server.PlacementBinding, error) {
	if request.Scope != c.scope {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	if request.Selector.IsNoFS() {
		return noFSBinding()
	}
	if !request.Selector.IsDefault() {
		return server.PlacementBinding{}, server.ErrInvalidPlacementSelection
	}
	if c.sourceCheckout == "" {
		return server.PlacementBinding{}, server.ErrInvalidPlacementBinding
	}
	if c.readiness != nil {
		if err := c.readiness(ctx); err != nil {
			return server.PlacementBinding{}, fmt.Errorf("microvm readiness: %w", err)
		}
	}
	return c.provisionPlacement(ctx, request.Principal)
}

// Reattach restores only the exact persisted microVM generation.
func (c *Client) Reattach(ctx context.Context, request server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if request.Scope != c.scope {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	if request.Ref.Kind == session.EnvKindNoFS {
		binding, err := noFSBinding()
		if err != nil || binding.Ref != request.Ref {
			return server.PlacementBinding{}, server.ErrPlacementNotFound
		}
		return binding, nil
	}
	claim, err := bindingForRef(request.Ref, request.Principal)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	if c.sourceCheckout == "" {
		return server.PlacementBinding{}, server.ErrInvalidPlacementBinding
	}
	if err := ctx.Err(); err != nil {
		return server.PlacementBinding{}, err
	}
	if c.readiness != nil {
		if err := c.readiness(ctx); err != nil {
			return server.PlacementBinding{}, fmt.Errorf("microvm readiness: %w", err)
		}
	}
	// Resolve may recover a cold repository VM, including artifact validation and
	// boot. Like Bind, the whole acquisition uses the caller's cancellation budget.
	response, owner, err := c.acquire(ctx, lifecycleRequest{
		Version: protocolVersion, Operation: "resolve", Binding: claim,
		Provision: &provisionRequest{Owner: claim.Owner, SessionID: claim.SessionID, Profile: c.profile, SourceCheckout: c.sourceCheckout},
	})
	if err != nil {
		if owner != nil {
			err = errors.Join(err, owner.terminal("detach"))
		}
		return server.PlacementBinding{}, fmt.Errorf("microvmd resolve phase failed: %w", err)
	}
	if response.Binding != claim {
		_ = owner.terminal("detach")
		return server.PlacementBinding{}, errors.New("microvmd resolved a different environment generation")
	}
	if err := ctx.Err(); err != nil {
		return server.PlacementBinding{}, errors.Join(err, owner.terminal("detach"))
	}
	return c.placementBinding(request.Ref, claim, owner, nil), nil
}

func noFSBinding() (server.PlacementBinding, error) {
	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "no-fs", Revision: "nofs-v1"}
	env, err := tool.NewEnvironment(ref, nofs.New(), memledger.New(), nil)
	if err != nil {
		return server.PlacementBinding{}, err
	}
	return server.PlacementBinding{Ref: ref, Environment: env, Metadata: server.PlacementMetadata{Label: "No filesystem"}}, nil
}

func (c *Client) provisionPlacement(ctx context.Context, principal *session.Principal) (server.PlacementBinding, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return server.PlacementBinding{}, fmt.Errorf("mint microvm placement identity: %w", err)
	}
	placementID := hex.EncodeToString(entropy[:])
	owner := ownerName(principal)
	response, ownerConn, err := c.acquire(ctx, lifecycleRequest{
		Version: protocolVersion, Operation: "create", Binding: binding{Owner: owner, SessionID: placementID},
		Provision: &provisionRequest{Owner: owner, SessionID: placementID, Profile: c.profile, SourceCheckout: c.sourceCheckout},
	})
	if err != nil {
		safeCleanupTarget := response.Binding.Owner == owner && response.Binding.SessionID == placementID &&
			strings.HasPrefix(response.Binding.EnvironmentID, "logical-") && canonicalBinding(response.Binding)
		if safeCleanupTarget {
			cleanup := func() error { return c.boundedRollbackPlacement(ctx, response.Binding) }
			if ownerConn != nil {
				cleanup = func() error { return ownerConn.terminal(operationDelete) }
			}
			return server.PlacementBinding{}, invalidPlacementResponseError(err, cleanup())
		}
		if ownerConn != nil {
			err = errors.Join(err, ownerConn.terminal("detach"))
		}
		return server.PlacementBinding{}, unsafePlacementResponseError(err)
	}
	safeCleanupTarget := response.Binding.Owner == owner && response.Binding.SessionID == placementID &&
		strings.HasPrefix(response.Binding.EnvironmentID, "logical-") && canonicalBinding(response.Binding)
	primary := errors.New("microvmd create returned incomplete or inconsistent placement metadata")
	if !validCreatedPlacement(response.Created, response.Binding, c.profile) {
		if safeCleanupTarget {
			return server.PlacementBinding{}, invalidPlacementResponseError(primary, ownerConn.terminal(operationDelete))
		}
		_ = ownerConn.terminal("detach")
		return server.PlacementBinding{}, unsafePlacementResponseError(primary)
	}
	if !safeCleanupTarget {
		_ = ownerConn.terminal("detach")
		return server.PlacementBinding{}, unsafePlacementResponseError(errors.New("microvmd create binding mismatch"))
	}
	ref := refForBinding(response.Binding)
	return c.placementBinding(ref, response.Binding, ownerConn, ownerConn), nil
}

func validCreatedPlacement(created *created, claim binding, profile string) bool {
	return created != nil && created.Ref.Kind == string(kindMicroVM) && created.Ref.ID == claim.Ref &&
		created.Generation == claim.Generation && created.HostWorktree != "" && created.GuestRoot == publicGuestRoot &&
		created.Profile == profile && created.GuestEgress != "" && created.HostEgress != ""
}

func (c *Client) rollbackPlacement(ctx context.Context, claim binding) error {
	response, err := c.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: operationDelete, Binding: claim})
	if err != nil {
		return err
	}
	var result DeleteResult
	if len(response.Payload) == 0 {
		return errors.New("microvmd rollback omitted cleanup result")
	}
	if err := json.Unmarshal(response.Payload, &result); err != nil {
		return fmt.Errorf("decode microvmd rollback result: %w", err)
	}
	if result.WorktreeRetained {
		return errRollbackRetained
	}
	return nil
}

func (c *Client) boundedRollbackPlacement(parent context.Context, claim binding) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), c.cleanupTimeout)
	defer cancel()
	return c.rollbackPlacement(ctx, claim)
}

const placementRecoveryCommand = "run 'mecated microvm status', then delete only the matching row with 'mecated microvm delete --backend microvm-local --attachment-id ATTACHMENT_ID --ref REF --generation GENERATION'"

func unsafePlacementResponseError(primary error) error {
	return fmt.Errorf("%w; automatic cleanup was not authorized because the response did not safely identify only the new placement; %s", primary, placementRecoveryCommand)
}

func invalidPlacementResponseError(primary, cleanupErr error) error {
	switch {
	case cleanupErr == nil:
		return fmt.Errorf("%w; automatic cleanup of the exact new placement completed", primary)
	case errors.Is(cleanupErr, errRollbackRetained):
		return fmt.Errorf("%w; automatic cleanup retained the exact new worktree for recovery; %s", primary, placementRecoveryCommand)
	case errors.Is(cleanupErr, context.DeadlineExceeded):
		return fmt.Errorf("%w; automatic cleanup of the exact new placement timed out and its status is unknown; %s", primary, placementRecoveryCommand)
	default:
		return fmt.Errorf("%w; automatic cleanup of the exact new placement failed and its status is unknown; %s", primary, placementRecoveryCommand)
	}
}

func canonicalBinding(claim binding) bool {
	return claim.Owner != "" && claim.SessionID != "" && claim.EnvironmentID != "" &&
		claim.Ref == claim.EnvironmentID+"@"+strconv.FormatUint(uint64(claim.Generation), 10) && claim.Generation != 0
}

func (c *Client) placementBinding(ref session.EnvironmentRef, claim binding, owner, rollback *acquisition) server.PlacementBinding {
	binding := server.PlacementBinding{
		Ref: ref, Environment: c.environment(ref, claim, owner), Close: func() error { return owner.terminal("detach") },
		GovernanceRoot: c.sourceCheckout,
		Metadata:       server.PlacementMetadata{Kind: string(kindMicroVM), Label: "Local microVM", Revision: ref.Revision},
	}
	if rollback != nil {
		binding.Rollback = func() error { return rollback.terminal(operationDelete) }
	}
	return binding
}

func refForBinding(claim binding) session.EnvironmentRef {
	return session.EnvironmentRef{Kind: kindMicroVM, ID: claim.SessionID + "." + claim.EnvironmentID, Revision: strconv.FormatUint(uint64(claim.Generation), 10)}
}

func bindingForRef(ref session.EnvironmentRef, principal *session.Principal) (binding, error) {
	if ref.Kind != kindMicroVM || ref.ID == "" || ref.Revision == "" {
		return binding{}, errors.New("invalid microvm environment ref")
	}
	dot := strings.IndexByte(ref.ID, '.')
	if dot <= 0 || dot == len(ref.ID)-1 {
		return binding{}, errors.New("invalid microvm environment identity")
	}
	generation, err := strconv.ParseUint(ref.Revision, 10, 32)
	if err != nil || generation == 0 {
		return binding{}, errors.New("invalid microvm environment generation")
	}
	sessionID, environmentID := ref.ID[:dot], ref.ID[dot+1:]
	return binding{Owner: ownerName(principal), SessionID: sessionID, EnvironmentID: environmentID, Ref: environmentID + "@" + ref.Revision, Generation: uint32(generation)}, nil
}

// DaemonInfo is the authenticated identity of the process serving the configured socket.
type DaemonInfo struct {
	ProtocolVersion uint16   `json:"protocol_version"`
	ReleaseIdentity string   `json:"release_identity"`
	BinaryIdentity  string   `json:"binary_identity"`
	ConfigDigest    string   `json:"config_digest"`
	PolicyRevision  string   `json:"policy_revision"`
	Profiles        []string `json:"profiles"`
	Socket          string   `json:"socket"`
}

// Equal reports an exact daemon compatibility match.
func (d DaemonInfo) Equal(other DaemonInfo) bool {
	if d.ProtocolVersion != other.ProtocolVersion || d.ReleaseIdentity != other.ReleaseIdentity || d.BinaryIdentity != other.BinaryIdentity || d.ConfigDigest != other.ConfigDigest || d.PolicyRevision != other.PolicyRevision || d.Socket != other.Socket || len(d.Profiles) != len(other.Profiles) {
		return false
	}
	for i := range d.Profiles {
		if d.Profiles[i] != other.Profiles[i] {
			return false
		}
	}
	return true
}

// DaemonInfo queries the serving process over the kernel-authenticated Unix socket.
func (c *Client) DaemonInfo(ctx context.Context) (DaemonInfo, error) {
	response, err := c.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: "info"})
	if err != nil {
		return DaemonInfo{}, err
	}
	var info DaemonInfo
	if len(response.Payload) == 0 {
		return info, errors.New("microvmd info response omitted payload")
	}
	if err := json.Unmarshal(response.Payload, &info); err != nil {
		return info, fmt.Errorf("decode microvmd info: %w", err)
	}
	if info.ProtocolVersion != protocolVersion || info.ReleaseIdentity == "" || info.BinaryIdentity == "" || info.ConfigDigest == "" || info.PolicyRevision == "" || len(info.Profiles) == 0 || info.Socket != c.endpoint {
		return DaemonInfo{}, errors.New("microvmd returned incomplete or mismatched daemon identity")
	}
	return info, nil
}

// ShutdownDaemon asks the exact authenticated daemon identity to stop gracefully.
// It is an internal manager operation, not a placement or user administration API.
func (c *Client) ShutdownDaemon(ctx context.Context, expected DaemonInfo) error {
	if expected.ProtocolVersion != protocolVersion || expected.ReleaseIdentity == "" || expected.BinaryIdentity == "" || expected.ConfigDigest == "" || expected.PolicyRevision == "" || len(expected.Profiles) == 0 || expected.Socket != c.endpoint {
		return errors.New("microvmd shutdown identity is incomplete or mismatched")
	}
	payload, err := json.Marshal(expected)
	if err != nil {
		return fmt.Errorf("encode microvmd shutdown identity: %w", err)
	}
	response, err := c.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: "shutdown", Payload: payload})
	if err != nil {
		return err
	}
	if response.Binding != (binding{}) || response.Created != nil || response.Stream != nil || len(response.Payload) != 0 {
		return errors.New("microvmd returned malformed shutdown acknowledgement")
	}
	return nil
}

// LifecycleMetrics is the bounded operator snapshot exposed by microvmd. The
// root adapter projects only counters needed by host-side operational checks.
type LifecycleMetrics struct {
	EgressDenials uint64
}

// GenerationHealth is microvmd's bounded operator health classification.
type GenerationHealth string

const (
	// GenerationHealthy means the daemon verified the exact live runtime identity.
	GenerationHealthy GenerationHealth = "healthy"
	// GenerationStale means the durable generation is not currently reattachable.
	GenerationStale GenerationHealth = "stale"
	// GenerationError means the runtime health probe itself failed.
	GenerationError GenerationHealth = "error"
)

// InventoryEntry is one owner-filtered exact daemon generation.
type InventoryEntry struct {
	Owner         string           `json:"owner"`
	SessionID     string           `json:"session_id"`
	EnvironmentID string           `json:"environment_id"`
	Ref           string           `json:"ref"`
	WorktreePath  string           `json:"worktree_path"`
	Generation    uint32           `json:"generation"`
	State         string           `json:"state"`
	Health        GenerationHealth `json:"health"`
	Error         string           `json:"error,omitempty"`
}

// GenerationBinding binds a destructive operation to one exact owner generation.
type GenerationBinding struct {
	Owner         string `json:"owner"`
	SessionID     string `json:"session_id"`
	EnvironmentID string `json:"environment_id"`
	Ref           string `json:"ref"`
	Generation    uint32 `json:"generation"`
}

// DeleteResult reports clean removal or the retained dirty worktree.
type DeleteResult struct {
	WorktreePath     string `json:"worktree_path"`
	WorktreeRetained bool   `json:"worktree_retained"`
}

// InventoryRequest asks for one bounded owner-scoped page.
type InventoryRequest struct {
	PageSize     int    `json:"page_size,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}

// InventoryPage carries one bounded inventory page and its opaque continuation.
type InventoryPage struct {
	Entries      []InventoryEntry `json:"entries"`
	Continuation string           `json:"continuation,omitempty"`
}

// Inventory retrieves one bounded inventory page visible to owner.
func (c *Client) Inventory(ctx context.Context, owner string, requests ...InventoryRequest) (InventoryPage, error) {
	if owner == "" {
		return InventoryPage{}, errors.New("microvmd inventory owner is required")
	}
	request, err := normalizeInventoryRequest(requests)
	if err != nil {
		return InventoryPage{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return InventoryPage{}, err
	}
	response, err := c.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: "inventory", Binding: binding{Owner: owner}, Payload: payload})
	if err != nil {
		return InventoryPage{}, err
	}
	var page InventoryPage
	if len(response.Payload) == 0 {
		return page, errors.New("microvmd inventory response omitted payload")
	}
	if err := json.Unmarshal(response.Payload, &page); err != nil {
		return page, fmt.Errorf("decode microvmd inventory: %w", err)
	}
	if err := validateInventoryPage(page, request, owner); err != nil {
		return InventoryPage{}, err
	}
	return page, nil
}

func normalizeInventoryRequest(requests []InventoryRequest) (InventoryRequest, error) {
	if len(requests) > 1 {
		return InventoryRequest{}, errors.New("microvmd inventory accepts one page request")
	}
	request := InventoryRequest{PageSize: defaultInventoryPageSize}
	if len(requests) == 1 {
		request = requests[0]
	}
	if request.PageSize <= 0 {
		request.PageSize = defaultInventoryPageSize
	}
	if request.PageSize > maxInventoryPageSize {
		request.PageSize = maxInventoryPageSize
	}
	if len(request.Continuation) > maxInventoryTokenBytes {
		return InventoryRequest{}, errors.New("microvmd inventory continuation exceeds client bound")
	}
	return request, nil
}

func validateInventoryPage(page InventoryPage, request InventoryRequest, owner string) error {
	if len(page.Entries) > request.PageSize || len(page.Entries) > maxInventoryPageSize || len(page.Continuation) > maxInventoryTokenBytes ||
		(len(page.Entries) == 0 && page.Continuation != "") || (page.Continuation == request.Continuation && page.Continuation != "") {
		return errors.New("microvmd inventory returned an invalid bounded page")
	}
	for _, entry := range page.Entries {
		if entry.Owner != owner || entry.SessionID == "" || entry.EnvironmentID == "" || entry.Ref == "" || entry.Generation == 0 || entry.WorktreePath == "" ||
			(entry.Health != GenerationHealthy && entry.Health != GenerationStale && entry.Health != GenerationError) {
			return errors.New("microvmd inventory returned an invalid or cross-owner generation")
		}
	}
	return nil
}

// DeleteGeneration permanently deletes one exact owner/session/ref/generation binding.
func (c *Client) DeleteGeneration(ctx context.Context, claim GenerationBinding) (DeleteResult, error) {
	if claim.Owner == "" || claim.SessionID == "" || claim.EnvironmentID == "" || claim.Ref == "" || claim.Generation == 0 {
		return DeleteResult{}, errors.New("microvmd delete requires a complete generation binding")
	}
	response, err := c.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: operationDelete, Binding: binding(claim)})
	if err != nil {
		return DeleteResult{}, err
	}
	var result DeleteResult
	if len(response.Payload) == 0 {
		return result, errors.New("microvmd delete response omitted cleanup result")
	}
	if err := json.Unmarshal(response.Payload, &result); err != nil {
		return result, fmt.Errorf("decode microvmd delete result: %w", err)
	}
	return result, nil
}

// LifecycleMetrics retrieves the current daemon lifecycle counters without binding the
// request to one environment generation.
func (c *Client) LifecycleMetrics(ctx context.Context) (LifecycleMetrics, error) {
	response, err := c.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: "metrics"})
	if err != nil {
		return LifecycleMetrics{}, err
	}
	var metrics LifecycleMetrics
	if len(response.Payload) == 0 {
		return LifecycleMetrics{}, errors.New("microvmd metrics response omitted payload")
	}
	if err := json.Unmarshal(response.Payload, &metrics); err != nil {
		return LifecycleMetrics{}, fmt.Errorf("decode microvmd lifecycle metrics: %w", err)
	}
	return metrics, nil
}

// Delete permanently destroys the exact persisted generation.
func (c *Client) Delete(ctx context.Context, sess *session.Session) error {
	return c.sessionOperation(ctx, "delete", sess)
}

var (
	_ server.PlacementProvider   = (*Client)(nil)
	_ server.PlacementReattacher = (*Client)(nil)
	_ server.PlacementDeleter    = (*Client)(nil)
	_ tool.EnvironmentForker     = (*Client)(nil)
	_ tool.EnvironmentMerger     = (*Client)(nil)
)

// DeletePlacement removes one exact schedule-owned logical attachment. The
// server receives only whether dirty state was retained; daemon paths stay
// private to the adapter.
func (c *Client) DeletePlacement(ctx context.Context, request server.PlacementDeleteRequest) (server.PlacementDeleteResult, error) {
	if request.Scope != c.scope {
		return server.PlacementDeleteResult{}, server.ErrPlacementNotFound
	}
	claim, err := bindingForRef(request.Ref, request.Principal)
	if err != nil {
		return server.PlacementDeleteResult{}, server.ErrPlacementNotFound
	}
	response, err := c.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: operationDelete, Binding: claim})
	if err != nil {
		return server.PlacementDeleteResult{}, err
	}
	var result DeleteResult
	if len(response.Payload) == 0 {
		return server.PlacementDeleteResult{}, errors.New("microvmd delete response omitted cleanup result")
	}
	if err := json.Unmarshal(response.Payload, &result); err != nil {
		return server.PlacementDeleteResult{}, fmt.Errorf("decode microvmd delete result: %w", err)
	}
	return server.PlacementDeleteResult{Retained: result.WorktreeRetained}, nil
}

// Fork asks microvmd to create a complete isolated child generation.
func (c *Client) Fork(ctx context.Context, base tool.Environment, label string) (tool.Environment, func() error, string, error) {
	claim, parentOwner, err := bindingFromEnvironment(c, base)
	if err != nil {
		return tool.Environment{}, nil, "", err
	}
	payload, err := json.Marshal(struct {
		Label string `json:"label"`
	}{Label: label})
	if err != nil {
		return tool.Environment{}, nil, "", err
	}
	response, childOwner, err := c.acquire(ctx, lifecycleRequest{Version: protocolVersion, Operation: "fork", Binding: claim, AcquisitionID: parentOwner.id, Payload: payload})
	safeChild := response.Binding.Owner == claim.Owner && response.Binding.Generation == claim.Generation &&
		response.Binding.Ref != claim.Ref && strings.HasPrefix(response.Binding.EnvironmentID, "logical-") && canonicalBinding(response.Binding)
	if err != nil {
		if safeChild && childOwner != nil {
			return tool.Environment{}, nil, "", invalidPlacementResponseError(err, childOwner.terminal("child-delete"))
		}
		if childOwner != nil {
			err = errors.Join(err, childOwner.terminal("detach"))
		}
		return tool.Environment{}, nil, "", unsafePlacementResponseError(err)
	}
	validChild := safeChild && response.Binding.SessionID == claim.SessionID+":"+label &&
		response.Created == nil && response.Stream == nil && len(response.Payload) == 0
	if !validChild {
		primary := errors.New("microvmd fork returned an invalid child binding")
		if safeChild {
			return tool.Environment{}, nil, "", invalidPlacementResponseError(primary, childOwner.terminal("child-delete"))
		}
		_ = childOwner.terminal("detach")
		return tool.Environment{}, nil, "", unsafePlacementResponseError(primary)
	}
	childRef := refForBinding(response.Binding)
	child := c.environment(childRef, response.Binding, childOwner)
	cleanup := func() error { return childOwner.terminal("child-delete") }
	return child, cleanup, "", nil
}

// Merge asks microvmd to conflict-check and atomically apply child changes.
func (c *Client) Merge(ctx context.Context, child, parent tool.Environment) error {
	parentClaim, parentOwner, err := bindingFromEnvironment(c, parent)
	if err != nil {
		return err
	}
	childClaim, childOwner, err := bindingFromEnvironment(c, child)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Child              binding `json:"child"`
		ChildAcquisitionID string  `json:"child_acquisition_id"`
	}{Child: childClaim, ChildAcquisitionID: childOwner.id})
	if err != nil {
		return err
	}
	response, err := c.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: "merge", Binding: parentClaim, AcquisitionID: parentOwner.id, Payload: payload})
	if err != nil {
		return err
	}
	if response.Binding != parentClaim {
		return errors.New("microvmd merge response changed parent generation")
	}
	return nil
}

func bindingFromEnvironment(client *Client, env tool.Environment) (binding, *acquisition, error) {
	workspace, ok := env.Workspace().(*workspace)
	if !ok || workspace.client != client || workspace.owner == nil || env.Ref().Kind != kindMicroVM || refForBinding(workspace.binding) != env.Ref() {
		return binding{}, nil, errors.New("environment is not owned by this microvmd client")
	}
	return workspace.binding, workspace.owner, nil
}

func (c *Client) sessionOperation(ctx context.Context, operation string, sess *session.Session) error {
	claim, err := bindingForSession(sess)
	if err != nil {
		return err
	}
	return c.operate(ctx, operation, claim)
}
func (c *Client) operate(ctx context.Context, operation string, claim binding) error {
	response, err := c.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: operation, Binding: claim})
	if err != nil {
		return err
	}
	if response.Binding != claim && operation != "delete" {
		return errors.New("microvmd lifecycle response changed generation")
	}
	return nil
}

func (c *Client) environment(ref session.EnvironmentRef, claim binding, owners ...*acquisition) tool.Environment {
	owner := &acquisition{client: c, binding: claim, id: "00000000000000000000000000000000"}
	if len(owners) != 0 && owners[0] != nil {
		owner = owners[0]
	}
	ws := &workspace{client: c, binding: claim, owner: owner, ledger: make(map[string]tool.FileVersion)}
	return tool.MustEnvironment(ref, ws, memledger.New(), &runner{client: c, binding: claim, owner: owner})
}

func acquisitionID(owner *acquisition) string {
	if owner == nil {
		return ""
	}
	return owner.id
}

func validAcquisitionID(id string) bool {
	if len(id) != 32 || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (c *Client) acquire(ctx context.Context, request lifecycleRequest) (lifecycleResponse, *acquisition, error) {
	var dialer net.Dialer
	dial := dialer.DialContext
	if c.acquisitionDial != nil {
		dial = c.acquisitionDial
	}
	conn, err := dial(ctx, "unix", c.endpoint)
	if err != nil {
		return lifecycleResponse{}, nil, fmt.Errorf("dial microvmd: %w", err)
	}
	afterFunc := context.AfterFunc
	if c.acquisitionAfterFunc != nil {
		afterFunc = c.acquisitionAfterFunc
	}
	stopCancel := afterFunc(ctx, func() { _ = conn.Close() })
	if err := writeFrame(conn, request); err != nil {
		stopCancel()
		_ = conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return lifecycleResponse{}, nil, ctxErr
		}
		return lifecycleResponse{}, nil, err
	}
	var response lifecycleResponse
	if err := readFrame(conn, &response); err != nil {
		stopCancel()
		_ = conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return lifecycleResponse{}, nil, ctxErr
		}
		return lifecycleResponse{}, nil, err
	}
	if response.ErrorCode != "" {
		stopCancel()
		_ = conn.Close()
		return lifecycleResponse{}, nil, fmt.Errorf("microvmd %s: %s", response.ErrorCode, response.ErrorText)
	}
	if !validAcquisitionID(response.AcquisitionID) {
		stopCancel()
		_ = conn.Close()
		return response, nil, errors.New("microvmd returned an invalid acquisition ID")
	}
	if c.acquisitionResponseDecoded != nil {
		c.acquisitionResponseDecoded()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if stopCancel() {
			return response, &acquisition{client: c, conn: conn, binding: response.Binding, id: response.AcquisitionID}, ctxErr
		}
		_ = conn.Close()
		return response, nil, ctxErr
	}
	if !stopCancel() {
		_ = conn.Close()
		return response, nil, ctx.Err()
	}
	return response, &acquisition{client: c, conn: conn, binding: response.Binding, id: response.AcquisitionID}, nil
}

func (c *Client) call(ctx context.Context, request lifecycleRequest) (lifecycleResponse, error) {
	return c.stream(ctx, request, func(execStreamFrame) error {
		return errors.New("microvmd sent a stream frame for a unary request")
	})
}

func (c *Client) stream(ctx context.Context, request lifecycleRequest, receive func(execStreamFrame) error) (lifecycleResponse, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.endpoint)
	if err != nil {
		return lifecycleResponse{}, fmt.Errorf("dial microvmd: %w", err)
	}
	defer func() { _ = conn.Close() }()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	if err := writeFrame(conn, request); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return lifecycleResponse{}, ctxErr
		}
		return lifecycleResponse{}, err
	}
	for {
		var response lifecycleResponse
		if err := readFrame(conn, &response); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return lifecycleResponse{}, ctxErr
			}
			return lifecycleResponse{}, err
		}
		if response.Stream != nil {
			if receive == nil {
				return lifecycleResponse{}, errors.New("microvmd sent an unexpected stream frame")
			}
			if err := receive(*response.Stream); err != nil {
				return lifecycleResponse{}, err
			}
			continue
		}
		if response.ErrorCode != "" {
			if response.ErrorCode == repositoryLogicalRootUnavailableCategory {
				return lifecycleResponse{}, ErrRepositoryLogicalRootUnavailable
			}
			return lifecycleResponse{}, fmt.Errorf("microvmd %s: %s", response.ErrorCode, response.ErrorText)
		}
		return response, nil
	}
}

func writeFrame(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > maxMessageBytes {
		return errors.New("microvmd frame exceeds bound")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload))) // #nosec G115 -- maxMessageBytes is below uint32
	_, err = w.Write(append(header[:], payload...))
	return err
}
func readFrame(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxMessageBytes {
		return errors.New("invalid microvmd frame")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func ownerName(p *session.Principal) string {
	if p == nil {
		return "local"
	}
	payload, _ := json.Marshal([2]string{p.Issuer, p.Subject})
	return string(payload)
}
func bindingForSession(sess *session.Session) (binding, error) {
	if sess == nil {
		return binding{}, errors.New("session is not microvm-backed")
	}
	return bindingForRef(sess.EnvironmentRef, sess.Owner)
}

type workspace struct {
	client  *Client
	binding binding
	owner   *acquisition
	mu      sync.Mutex
	ledger  map[string]tool.FileVersion
}

func (*workspace) Root() string { return publicGuestRoot }

// AuthorityResourcePath projects the same confined guest path used by workspace RPCs.
func (*workspace) AuthorityResourcePath(p string) (target, root string, err error) {
	cleaned := pathpkg.Clean(p)
	if p == "" || pathpkg.IsAbs(p) || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.IndexByte(p, 0) >= 0 {
		return "", publicGuestRoot, errors.New("microvm workspace path escapes the guest root")
	}
	return pathpkg.Join(publicGuestRoot, cleaned), publicGuestRoot, nil
}

type workspaceRequest struct {
	Operation    string `json:"operation"`
	Path         string `json:"path,omitempty"`
	Pattern      string `json:"pattern,omitempty"`
	PathGlob     string `json:"path_glob,omitempty"`
	Data         []byte `json:"data,omitempty"`
	Version      string `json:"version,omitempty"`
	VersionValid bool   `json:"version_valid,omitempty"`
}
type workspaceResponse struct {
	Data         []byte           `json:"data,omitempty"`
	Version      string           `json:"version,omitempty"`
	VersionValid bool             `json:"version_valid,omitempty"`
	Info         *tool.FileInfo   `json:"info,omitempty"`
	Paths        []string         `json:"paths,omitempty"`
	Matches      []tool.GrepMatch `json:"matches,omitempty"`
	ErrorCode    string           `json:"error_code,omitempty"`
}

func (w *workspace) rpc(ctx context.Context, req workspaceRequest) (workspaceResponse, error) {
	payload, _ := json.Marshal(req)
	response, err := w.client.call(ctx, lifecycleRequest{Version: protocolVersion, Operation: "workspace", Binding: w.binding, AcquisitionID: acquisitionID(w.owner), Payload: payload})
	if err != nil {
		return workspaceResponse{}, err
	}
	var out workspaceResponse
	if err := json.Unmarshal(response.Payload, &out); err != nil {
		return out, err
	}
	if out.ErrorCode != "" {
		if out.ErrorCode == "not_found" {
			return out, fs.ErrNotExist
		}
		if out.ErrorCode == "exists" {
			return out, fs.ErrExist
		}
		if out.ErrorCode == "version_mismatch" {
			return out, &tool.VersionMismatchError{Path: req.Path}
		}
		return out, errors.New("microvm workspace operation failed")
	}
	return out, nil
}
func (w *workspace) Read(ctx context.Context, path string) ([]byte, error) {
	r, e := w.rpc(ctx, workspaceRequest{Operation: "read", Path: path})
	return r.Data, e
}
func (w *workspace) ReadVersion(ctx context.Context, path string) ([]byte, tool.FileVersion, error) {
	r, e := w.rpc(ctx, workspaceRequest{Operation: "read", Path: path})
	if e != nil {
		return nil, tool.FileVersion{}, e
	}
	if !r.VersionValid {
		return nil, tool.FileVersion{}, errors.New("microvm workspace omitted version")
	}
	return r.Data, tool.NewFileVersion(r.Version), nil
}
func (w *workspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	r, e := w.rpc(ctx, workspaceRequest{Operation: "stat", Path: path})
	if e != nil || r.Info == nil {
		return tool.FileInfo{}, e
	}
	return *r.Info, nil
}
func (w *workspace) CreateFile(ctx context.Context, path string, data []byte) (tool.FileVersion, error) {
	r, e := w.rpc(ctx, workspaceRequest{Operation: "create", Path: path, Data: data})
	if e != nil {
		return tool.FileVersion{}, e
	}
	if !r.VersionValid {
		return tool.FileVersion{}, errors.New("microvm workspace omitted version")
	}
	return tool.NewFileVersion(r.Version), nil
}
func (w *workspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	token, encodeErr := tool.EncodeFileVersion(old)
	if encodeErr != nil {
		return tool.FileVersion{}, encodeErr
	}
	r, e := w.rpc(ctx, workspaceRequest{Operation: "replace", Path: path, Data: data, Version: token, VersionValid: true})
	if e != nil {
		return tool.FileVersion{}, e
	}
	if !r.VersionValid {
		return tool.FileVersion{}, errors.New("microvm workspace omitted version")
	}
	return tool.NewFileVersion(r.Version), nil
}
func (w *workspace) Glob(ctx context.Context, p string) ([]string, error) {
	r, e := w.rpc(ctx, workspaceRequest{Operation: "glob", Pattern: p})
	return r.Paths, e
}
func (w *workspace) Grep(ctx context.Context, p, g string) ([]tool.GrepMatch, error) {
	r, e := w.rpc(ctx, workspaceRequest{Operation: "grep", Pattern: p, PathGlob: g})
	return r.Matches, e
}
func (w *workspace) RecordRead(path string, v tool.FileVersion) {
	w.mu.Lock()
	w.ledger[tool.LedgerKey("/workspace", path)] = v
	w.mu.Unlock()
}
func (w *workspace) RecordedVersion(path string) (tool.FileVersion, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	v, ok := w.ledger[tool.LedgerKey("/workspace", path)]
	return v, ok
}

type runner struct {
	client  *Client
	binding binding
	owner   *acquisition
}

func (*runner) BoundWorkspaceRoot() string { return publicGuestRoot }

type execResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func (r *runner) Run(ctx context.Context, command string) (tool.CommandResult, error) {
	return r.runResult(ctx, command, "")
}

func (r *runner) RunWithTemporaryScope(ctx context.Context, command string, scope tool.TemporaryScope) (tool.CommandResult, error) {
	return r.runResult(ctx, command, scope)
}

func (r *runner) runResult(ctx context.Context, command string, scope tool.TemporaryScope) (tool.CommandResult, error) {
	var stdout, stderr bytes.Buffer
	exit, err := r.exec(ctx, command, scope, func(frame execStreamFrame) error {
		var target io.Writer
		switch frame.Channel {
		case "stdout":
			target = &stdout
		case "stderr":
			target = &stderr
		default:
			return errors.New("microvmd exec stream has an invalid channel")
		}
		_, writeErr := target.Write(frame.Data)
		return writeErr
	})
	return tool.CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exit}, err
}

func (r *runner) RunStreaming(ctx context.Context, command string, out io.Writer) (int, error) {
	return r.runStreaming(ctx, command, "", out)
}

func (r *runner) RunStreamingWithTemporaryScope(ctx context.Context, command string, scope tool.TemporaryScope, out io.Writer) (int, error) {
	return r.runStreaming(ctx, command, scope, out)
}

func (r *runner) runStreaming(ctx context.Context, command string, scope tool.TemporaryScope, out io.Writer) (int, error) {
	if out == nil {
		return 0, errors.New("microvmd exec stream writer is nil")
	}
	return r.exec(ctx, command, scope, func(frame execStreamFrame) error {
		if frame.Channel != "stdout" && frame.Channel != "stderr" {
			return errors.New("microvmd exec stream has an invalid channel")
		}
		n, err := out.Write(frame.Data)
		if err == nil && n != len(frame.Data) {
			err = io.ErrShortWrite
		}
		return err
	})
}

func (r *runner) exec(ctx context.Context, command string, scope tool.TemporaryScope, receive func(execStreamFrame) error) (int, error) {
	payload, err := json.Marshal(struct {
		Command        string              `json:"command"`
		TemporaryScope tool.TemporaryScope `json:"temporary_scope,omitempty"`
	}{Command: command, TemporaryScope: scope})
	if err != nil {
		return 0, err
	}
	response, err := r.client.stream(ctx, lifecycleRequest{Version: protocolVersion, Operation: "exec", Binding: r.binding, AcquisitionID: acquisitionID(r.owner), Payload: payload}, receive)
	if err != nil {
		return 0, err
	}
	var final execResponse
	if err := json.Unmarshal(response.Payload, &final); err != nil {
		return 0, err
	}
	return final.ExitCode, nil
}

var _ tool.Workspace = (*workspace)(nil)
var _ tool.CommandTemporaryScopeRunner = (*runner)(nil)
var _ tool.CommandTemporaryScopeStreamer = (*runner)(nil)
