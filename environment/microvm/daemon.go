package microvm

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/environment/microvm/control"
)

// LifecycleProtocolVersion is the local microvmd management protocol version.
const LifecycleProtocolVersion uint16 = 3

// LifecycleOperation is one closed management operation.
type LifecycleOperation string

const (
	// LifecycleInfo returns the authenticated serving daemon identity and loaded policy.
	LifecycleInfo LifecycleOperation = "info"
	// LifecycleCreate provisions and durably registers one new generation.
	LifecycleCreate LifecycleOperation = "create"
	// LifecycleResolve reattaches one exact ready generation.
	LifecycleResolve LifecycleOperation = "resolve"
	// LifecycleInspect verifies one exact ready runtime identity.
	LifecycleInspect LifecycleOperation = "inspect"
	// LifecycleDetach drops process-local handles without changing durable state.
	LifecycleDetach LifecycleOperation = "detach"
	// LifecycleDelete tombstones and destroys one exact generation.
	LifecycleDelete LifecycleOperation = "delete"
	// LifecycleWorkspace proxies bounded guest filesystem calls.
	LifecycleWorkspace LifecycleOperation = "workspace"
	// LifecycleExec proxies bounded guest command execution.
	LifecycleExec LifecycleOperation = "exec"
	// LifecycleFork creates one isolated child generation from the bound parent.
	LifecycleFork LifecycleOperation = "fork"
	// LifecycleMerge conflict-checks and applies one bound child to its parent.
	LifecycleMerge LifecycleOperation = "merge"
	// LifecycleMetrics exports the fixed-dimension operations snapshot.
	LifecycleMetrics LifecycleOperation = "metrics"
	// LifecycleInventory returns one bounded owner-filtered generation inventory.
	LifecycleInventory LifecycleOperation = "inventory"
	// LifecycleReconcile re-drives durable cleanup without provisioning a generation.
	LifecycleReconcile LifecycleOperation = "reconcile"
	// LifecycleChildDelete force-cleans an exact delegated child generation.
	LifecycleChildDelete LifecycleOperation = "child-delete"
)

var errLifecycleProtocol = errors.New("invalid microvmd lifecycle protocol request")

// LifecycleRequest is one bounded request on the authenticated daemon socket.
// Create binds the requested owner/session and returns the allocated ref/generation;
// every operation on an existing environment requires the complete Binding.
type LifecycleRequest struct {
	Version   uint16             `json:"version"`
	Operation LifecycleOperation `json:"operation"`
	Binding   control.Binding    `json:"binding"`
	Create    *CreateRequest     `json:"create,omitempty"`
	Provision *ProvisionRequest  `json:"provision,omitempty"`
	Payload   json.RawMessage    `json:"payload,omitempty"`
}

// ProvisionRequest is the thin root-module create shape. The daemon expands
// operator-owned artifact and resource policy before entering Lifecycle.Create.
type ProvisionRequest struct {
	Owner          string `json:"owner"`
	SessionID      string `json:"session_id"`
	Profile        string `json:"profile"`
	SourceCheckout string `json:"source_checkout"`
}

// DaemonInfo is the authenticated identity of the process serving this socket.
type DaemonInfo struct {
	ProtocolVersion uint16   `json:"protocol_version"`
	ReleaseIdentity string   `json:"release_identity"`
	BinaryIdentity  string   `json:"binary_identity"`
	ConfigDigest    string   `json:"config_digest"`
	PolicyRevision  string   `json:"policy_revision"`
	Profiles        []string `json:"profiles"`
	Socket          string   `json:"socket"`
}

// Equal reports an exact compatibility match, including profile order.
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

// LifecycleCreated is the non-resource-handle create result carried on the wire.
type LifecycleCreated struct {
	Ref          EnvironmentRef `json:"ref"`
	Generation   uint32         `json:"generation"`
	HostWorktree string         `json:"host_worktree"`
	GuestRoot    string         `json:"guest_root"`
	Profile      string         `json:"profile"`
	GuestEgress  string         `json:"guest_egress"`
	HostEgress   string         `json:"host_egress"`
}

// LifecycleExecStream is one ordered stdout or stderr chunk preceding the final response.
type LifecycleExecStream struct {
	Channel string `json:"channel"`
	Data    []byte `json:"data"`
}

// LifecycleResponse reports the exact durable generation observed after an operation.
type LifecycleResponse struct {
	Binding   control.Binding      `json:"binding,omitempty"`
	Record    *EnvironmentRecord   `json:"record,omitempty"`
	Created   *LifecycleCreated    `json:"created,omitempty"`
	Stream    *LifecycleExecStream `json:"stream,omitempty"`
	Payload   json.RawMessage      `json:"payload,omitempty"`
	ErrorCode string               `json:"error_code,omitempty"`
	ErrorText string               `json:"error,omitempty"`
	Err       error                `json:"-"`
}

// LifecycleGenerationHealth is the operator-facing health of one exact generation.
type LifecycleGenerationHealth string

const (
	// GenerationHealthy means the exact runtime identity is live.
	GenerationHealthy LifecycleGenerationHealth = "healthy"
	// GenerationStale means the durable generation is not currently reattachable.
	GenerationStale LifecycleGenerationHealth = "stale"
	// GenerationError means the runtime health probe itself failed.
	GenerationError LifecycleGenerationHealth = "error"
)

const (
	defaultInventoryPageSize = 50
	maxInventoryPageSize     = 64
	maxInventoryTokenBytes   = 1024
)

// LifecycleInventoryRequest asks for one deterministic owner-scoped page.
type LifecycleInventoryRequest struct {
	PageSize     int    `json:"page_size,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}

// LifecycleInventoryPage is one bounded page and its opaque continuation.
type LifecycleInventoryPage struct {
	Entries      []LifecycleInventoryEntry `json:"entries"`
	Continuation string                    `json:"continuation,omitempty"`
}

// LifecycleInventoryEntry is the bounded, non-secret lifecycle projection.
type LifecycleInventoryEntry struct {
	Owner         string                    `json:"owner"`
	SessionID     string                    `json:"session_id"`
	EnvironmentID string                    `json:"environment_id"`
	Ref           string                    `json:"ref"`
	WorktreePath  string                    `json:"worktree_path"`
	Generation    uint32                    `json:"generation"`
	State         EnvironmentState          `json:"state"`
	Health        LifecycleGenerationHealth `json:"health"`
	Error         string                    `json:"error,omitempty"`
}

// LifecycleDeleteResult reports whether the exact generation's worktree was removed.
type LifecycleDeleteResult struct {
	WorktreePath     string `json:"worktree_path"`
	WorktreeRetained bool   `json:"worktree_retained"`
}

// ChildForkPayload is the bounded descriptive input for a child fork.
type ChildForkPayload struct {
	Label string `json:"label"`
}

// ChildMergePayload identifies the exact child generation merged into Binding.
type ChildMergePayload struct {
	Child control.Binding `json:"child"`
}

// ChildLifecycle owns daemon-side child creation and conflict-aware merge.
type ChildLifecycle interface {
	Fork(context.Context, EnvironmentRecord, string) (EnvironmentRecord, error)
	Merge(context.Context, EnvironmentRecord, EnvironmentRecord) error
}

// EnvironmentCreator is the lifecycle transaction used by the create RPC.
type EnvironmentCreator interface {
	Create(context.Context, CreateRequest) (CreatedEnvironment, error)
}

// PlacementBuilder expands the thin root request using daemon-owned policy.
type PlacementBuilder func(context.Context, ProvisionRequest) (CreateRequest, error)

// LifecycleReconciler repairs durable lifecycle state without provisioning.
type LifecycleReconciler interface {
	Reconcile(context.Context) error
}

// RepositoryPlacementBuilder resolves daemon-owned profile and immutable artifact
// policy into one repository-scoped logical attachment request.
type RepositoryPlacementBuilder func(context.Context, ProvisionRequest) (LogicalEnvironmentRequest, EnforcedProfileStatus, error)

// DaemonConfig wires the authenticated protocol to durable lifecycle seams.
type DaemonConfig struct {
	Control                *control.Service
	Creator                EnvironmentCreator
	Provisioner            PlacementBuilder
	Registry               ReconcileRegistry
	Runtime                LifecycleRuntime
	Worktrees              WorktreeRetention
	Admission              *AdmissionController
	Children               ChildLifecycle
	Reconciler             LifecycleReconciler
	Observer               *OperationsObserver
	Info                   DaemonInfo
	RepositoryAttachments  *RepositoryAttachmentManager
	RepositoryProvisioner  RepositoryPlacementBuilder
	RepositoryStartupError error
}

type repositoryDaemonBinding struct {
	binding control.Binding
	status  EnforcedProfileStatus
}

// Daemon owns the local management protocol. Hypervisor creation remains
// exclusively behind Lifecycle -> VMRuntime.
type Daemon struct {
	control               *control.Service
	creator               EnvironmentCreator
	provisioner           PlacementBuilder
	registry              ReconcileRegistry
	runtime               LifecycleRuntime
	manager               *EnvironmentManager
	children              ChildLifecycle
	reconciler            LifecycleReconciler
	admission             *AdmissionController
	observer              *OperationsObserver
	info                  DaemonInfo
	repositoryAttachments *RepositoryAttachmentManager
	repositoryProvisioner RepositoryPlacementBuilder
	repositoryStartupErr  error
	repositoryMu          sync.Mutex
	repositoryBindings    map[string]repositoryDaemonBinding
	inventoryKey          [sha256.Size]byte
	mergeLocks            [mergeLockStripes]sync.Mutex
}

// NewDaemon constructs the fail-closed local lifecycle service.
func NewDaemon(cfg DaemonConfig) (*Daemon, error) {
	repositoryOnly := cfg.RepositoryAttachments != nil
	if cfg.Control == nil || (!repositoryOnly && (cfg.Registry == nil || cfg.Runtime == nil || cfg.Worktrees == nil)) {
		return nil, errors.New("microvmd lifecycle service is not fully configured")
	}
	var manager *EnvironmentManager
	if !repositoryOnly {
		manager = NewEnvironmentManagerWithAdmission(cfg.Registry, cfg.Runtime, cfg.Worktrees, cfg.Admission, cfg.Observer)
	}
	daemon := &Daemon{
		control: cfg.Control, creator: cfg.Creator, provisioner: cfg.Provisioner, registry: cfg.Registry, runtime: cfg.Runtime,
		manager:  manager,
		children: cfg.Children, reconciler: cfg.Reconciler, admission: cfg.Admission, observer: cfg.Observer, info: cfg.Info,
		repositoryAttachments: cfg.RepositoryAttachments, repositoryProvisioner: cfg.RepositoryProvisioner,
		repositoryStartupErr: cfg.RepositoryStartupError,
		repositoryBindings:   make(map[string]repositoryDaemonBinding),
	}
	if _, err := rand.Read(daemon.inventoryKey[:]); err != nil {
		return nil, fmt.Errorf("initialize microvmd inventory pagination: %w", err)
	}
	return daemon, nil
}

type lifecycleServeError struct {
	operation LifecycleOperation
	code      string
	cause     error
}

func (e *lifecycleServeError) Error() string { return e.cause.Error() }
func (e *lifecycleServeError) Unwrap() error { return e.cause }

func newLifecycleServeError(operation LifecycleOperation, code string, cause error) error {
	return &lifecycleServeError{operation: operation, code: code, cause: cause}
}

// ServeConn authenticates the peer, then reads and writes one bounded lifecycle
// exchange on a local socket. Exec responses are ordered stream frames followed by
// one final exit response.
func (d *Daemon) ServeConn(ctx context.Context, conn net.Conn) error {
	if d == nil || d.control == nil {
		return errors.New("microvmd lifecycle service is not configured")
	}
	if err := d.control.Authenticate(conn); err != nil {
		return newLifecycleServeError("connection", lifecycleErrorCode(err), err)
	}
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	var request LifecycleRequest
	if err := codec.Read(conn, &request); err != nil {
		return newLifecycleServeError("connection", "transport", err)
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		var probe [1]byte
		_, _ = conn.Read(probe[:])
		cancel()
	}()
	write := func(response LifecycleResponse) error { return codec.Write(conn, response) }
	var response LifecycleResponse
	if request.Operation == LifecycleExec {
		response = d.handleExecStream(requestCtx, request, func(frame LifecycleExecStream) error {
			return write(LifecycleResponse{Stream: &frame})
		})
	} else {
		response = d.handleAuthenticated(requestCtx, request)
	}
	if err := write(response); err != nil {
		return newLifecycleServeError(request.Operation, "transport", err)
	}
	if response.Err != nil {
		return newLifecycleServeError(request.Operation, response.ErrorCode, response.Err)
	}
	return nil
}

// Handle authenticates the peer before examining any caller-controlled identity,
// then binds the operation to the authoritative durable registry record.
func (d *Daemon) Handle(ctx context.Context, conn net.Conn, request LifecycleRequest) LifecycleResponse {
	if d == nil || d.control == nil {
		return lifecycleFailure(errors.New("microvmd lifecycle service is not configured"))
	}
	if err := d.control.Authenticate(conn); err != nil {
		return lifecycleFailure(err)
	}
	return d.handleAuthenticated(ctx, request)
}

func (d *Daemon) handleExecStream(ctx context.Context, request LifecycleRequest, send func(LifecycleExecStream) error) LifecycleResponse {
	if request.Version != LifecycleProtocolVersion || request.Operation != LifecycleExec {
		return lifecycleFailure(errLifecycleProtocol)
	}
	if d.repositoryAttachments != nil && strings.HasPrefix(request.Binding.EnvironmentID, "logical-") {
		response, err := d.repositoryExecStream(ctx, request, send)
		if err != nil {
			return lifecycleFailure(err)
		}
		return response
	}
	record, err := d.boundRecord(ctx, request.Binding)
	if err != nil {
		return lifecycleFailure(err)
	}
	response, err := d.proxyExecStream(ctx, request, record, send)
	if err != nil {
		return lifecycleFailure(err)
	}
	return response
}

func (d *Daemon) handleAuthenticated(ctx context.Context, request LifecycleRequest) LifecycleResponse {
	if request.Version != LifecycleProtocolVersion {
		return lifecycleFailure(errLifecycleProtocol)
	}
	if response, handled := d.handleRepositoryRequest(ctx, request); handled {
		return response
	}
	return d.handleStandardRequest(ctx, request)
}

func (d *Daemon) handleStandardRequest(ctx context.Context, request LifecycleRequest) LifecycleResponse { //nolint:gocyclo // closed protocol routing is clearest as one switch
	if d.repositoryAttachments != nil {
		switch request.Operation {
		case LifecycleInfo, LifecycleInventory, LifecycleReconcile, LifecycleMetrics:
		default:
			return lifecycleFailure(ErrEnvironmentUnavailable)
		}
	}
	var response LifecycleResponse
	var err error
	switch request.Operation {
	case LifecycleInfo:
		if request.Binding != (control.Binding{}) || request.Create != nil || request.Provision != nil || len(request.Payload) != 0 || d.info.ProtocolVersion != LifecycleProtocolVersion {
			err = errLifecycleProtocol
			break
		}
		if d.repositoryStartupErr != nil {
			err = d.repositoryStartupErr
			break
		}
		response.Payload, err = json.Marshal(d.info)
	case LifecycleCreate:
		response, err = d.create(ctx, request)
	case LifecycleResolve, LifecycleInspect, LifecycleDetach, LifecycleDelete, LifecycleChildDelete, LifecycleWorkspace, LifecycleExec:
		response, err = d.existing(ctx, request)
	case LifecycleFork:
		response, err = d.fork(ctx, request)
	case LifecycleMerge:
		response, err = d.merge(ctx, request)
	case LifecycleMetrics:
		if d.observer == nil || request.Binding != (control.Binding{}) || request.Create != nil || request.Provision != nil {
			err = errLifecycleProtocol
			break
		}
		response.Payload, err = json.Marshal(d.observer.Snapshot())
	case LifecycleInventory:
		response, err = d.inventory(ctx, request)
	case LifecycleReconcile:
		err = d.reconcile(ctx, request)
	default:
		err = errLifecycleProtocol
	}
	if err != nil {
		return lifecycleFailure(err)
	}
	return response
}

func (d *Daemon) inventory(ctx context.Context, request LifecycleRequest) (LifecycleResponse, error) {
	if err := validateOwnerRequest(request, true); err != nil {
		return LifecycleResponse{}, err
	}
	pageRequest := LifecycleInventoryRequest{PageSize: defaultInventoryPageSize}
	if len(request.Payload) != 0 {
		if len(request.Payload) > maxInventoryTokenBytes+128 || json.Unmarshal(request.Payload, &pageRequest) != nil {
			return LifecycleResponse{}, errLifecycleProtocol
		}
	}
	if pageRequest.PageSize <= 0 {
		pageRequest.PageSize = defaultInventoryPageSize
	}
	if pageRequest.PageSize > maxInventoryPageSize {
		pageRequest.PageSize = maxInventoryPageSize
	}
	cursor, err := d.decodeInventoryCursor(request.Binding.Owner, pageRequest.Continuation)
	if err != nil {
		return LifecycleResponse{}, err
	}
	if d.repositoryAttachments != nil {
		return d.repositoryInventory(ctx, request.Binding.Owner, pageRequest.PageSize, cursor)
	}
	records, err := d.registry.List(ctx)
	if err != nil {
		return LifecycleResponse{}, err
	}
	records = ownerInventoryRecords(records, request.Binding.Owner)
	sort.Slice(records, func(i, j int) bool { return inventoryRecordLess(records[i], records[j]) })
	start := sort.Search(len(records), func(i int) bool { return inventoryRecordAfter(records[i], cursor) })
	end := min(start+pageRequest.PageSize, len(records))
	entries := make([]LifecycleInventoryEntry, 0, end-start)
	for _, record := range records[start:end] {
		entry := LifecycleInventoryEntry{
			Owner: record.Owner, SessionID: record.SessionID, EnvironmentID: record.EnvironmentID,
			Ref: record.Ref.ID, Generation: record.Generation, WorktreePath: record.WorktreePath, State: record.State,
		}
		if record.State == EnvironmentDestroyed || record.Tombstone {
			entry.Health = GenerationStale
			entry.Error = "orphan generation was identity-checked and destroyed; dirty worktree retained for recovery; explicitly create a new session to continue"
		} else if record.State != EnvironmentReady {
			entry.Health, entry.Error = GenerationStale, "durable generation state "+string(record.State)+" is not ready"
		} else if status, inspectErr := d.runtime.Inspect(ctx, record); inspectErr != nil {
			entry.Health, entry.Error = GenerationError, boundedLifecycleError(inspectErr)
		} else if validateRuntimeIdentity(record, status) != nil {
			entry.Health, entry.Error = GenerationStale, "runtime identity does not match the durable generation"
		} else {
			entry.Health = GenerationHealthy
		}
		entries = append(entries, entry)
	}
	page := LifecycleInventoryPage{Entries: entries}
	if end < len(records) {
		page.Continuation, err = d.encodeInventoryCursor(request.Binding.Owner, records[end-1])
		if err != nil {
			return LifecycleResponse{}, err
		}
	}
	payload, err := json.Marshal(page)
	return LifecycleResponse{Payload: payload}, err
}

func (d *Daemon) repositoryInventory(ctx context.Context, owner string, pageSize int, cursor inventoryCursor) (LifecycleResponse, error) {
	records := d.repositoryAttachments.inventory(owner)
	sort.Slice(records, func(i, j int) bool {
		left, right := records[i].binding, records[j].binding
		if left.SessionID != right.SessionID {
			return left.SessionID < right.SessionID
		}
		if left.Ref != right.Ref {
			return left.Ref < right.Ref
		}
		if left.Generation != right.Generation {
			return left.Generation < right.Generation
		}
		return left.EnvironmentID < right.EnvironmentID
	})
	after := func(record repositoryAttachmentRecord) bool {
		if cursor == (inventoryCursor{}) {
			return true
		}
		binding := record.binding
		if binding.SessionID != cursor.SessionID {
			return binding.SessionID > cursor.SessionID
		}
		if binding.Ref != cursor.Ref {
			return binding.Ref > cursor.Ref
		}
		if binding.Generation != cursor.Generation {
			return binding.Generation > cursor.Generation
		}
		return binding.EnvironmentID > cursor.EnvironmentID
	}
	start := sort.Search(len(records), func(i int) bool { return after(records[i]) })
	end := min(start+pageSize, len(records))
	entries := make([]LifecycleInventoryEntry, 0, end-start)
	type repositoryHealthResult struct{ err error }
	healthByGeneration := make(map[string]repositoryHealthResult)
	for _, record := range records[start:end] {
		binding := record.binding
		entry := LifecycleInventoryEntry{
			Owner: binding.Owner, SessionID: binding.SessionID, EnvironmentID: binding.EnvironmentID,
			Ref: binding.Ref, Generation: record.repository.Generation,
			WorktreePath: record.worktreePath, State: record.repository.State,
		}
		switch {
		case record.deleted && record.worktreeRetained:
			entry.State, entry.Health = EnvironmentDestroyed, GenerationStale
			entry.Error = "logical attachment was deleted; dirty worktree retained for recovery; repository VM deletion is not supported"
		case record.deleted:
			continue
		case record.repository.State != EnvironmentReady:
			entry.Health = GenerationStale
			entry.Error = "repository generation state " + string(record.repository.State) + " is not ready"
		default:
			key := fmt.Sprintf("%s\x00%s\x00%d", record.repository.Owner, record.repository.RepositoryKey, record.repository.Generation)
			health, ok := healthByGeneration[key]
			if !ok {
				health.err = d.repositoryAttachments.health(ctx, record.repository)
				healthByGeneration[key] = health
			}
			switch {
			case health.err == nil:
				entry.Health = GenerationHealthy
			case errors.Is(health.err, ErrRepositoryVMInconsistent):
				entry.Health, entry.Error = GenerationStale, boundedLifecycleError(health.err)
			default:
				entry.Health, entry.Error = GenerationError, boundedLifecycleError(health.err)
			}
		}
		entries = append(entries, entry)
	}
	page := LifecycleInventoryPage{Entries: entries}
	if end < len(records) {
		last := records[end-1].binding
		var err error
		page.Continuation, err = d.encodeInventoryCursor(owner, EnvironmentRecord{
			SessionID: last.SessionID, EnvironmentID: last.EnvironmentID,
			Ref: EnvironmentRef{ID: last.Ref}, Generation: last.Generation,
		})
		if err != nil {
			return LifecycleResponse{}, err
		}
	}
	payload, err := json.Marshal(page)
	return LifecycleResponse{Payload: payload}, err
}

type inventoryCursor struct {
	Owner         string `json:"owner"`
	SessionID     string `json:"session_id"`
	Ref           string `json:"ref"`
	Generation    uint32 `json:"generation"`
	EnvironmentID string `json:"environment_id"`
}

func ownerInventoryRecords(records []EnvironmentRecord, owner string) []EnvironmentRecord {
	out := records[:0]
	for _, record := range records {
		if record.Owner != owner || ((record.State == EnvironmentDestroyed || record.Tombstone) && !record.PreserveWorktree) {
			continue
		}
		out = append(out, record)
	}
	return out
}

func inventoryRecordLess(left, right EnvironmentRecord) bool {
	if left.SessionID != right.SessionID {
		return left.SessionID < right.SessionID
	}
	if left.Ref.ID != right.Ref.ID {
		return left.Ref.ID < right.Ref.ID
	}
	if left.Generation != right.Generation {
		return left.Generation < right.Generation
	}
	return left.EnvironmentID < right.EnvironmentID
}

func inventoryRecordAfter(record EnvironmentRecord, cursor inventoryCursor) bool {
	if cursor == (inventoryCursor{}) {
		return true
	}
	key := EnvironmentRecord{SessionID: cursor.SessionID, Ref: EnvironmentRef{ID: cursor.Ref}, Generation: cursor.Generation, EnvironmentID: cursor.EnvironmentID}
	return inventoryRecordLess(key, record)
}

func (d *Daemon) encodeInventoryCursor(owner string, record EnvironmentRecord) (string, error) {
	payload, err := json.Marshal(inventoryCursor{Owner: owner, SessionID: record.SessionID, Ref: record.Ref.ID, Generation: record.Generation, EnvironmentID: record.EnvironmentID})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, d.inventoryKey[:])
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (d *Daemon) decodeInventoryCursor(owner, token string) (inventoryCursor, error) {
	if token == "" {
		return inventoryCursor{}, nil
	}
	if len(token) > maxInventoryTokenBytes {
		return inventoryCursor{}, errLifecycleProtocol
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return inventoryCursor{}, errLifecycleProtocol
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return inventoryCursor{}, errLifecycleProtocol
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[1] {
		return inventoryCursor{}, errLifecycleProtocol
	}
	mac := hmac.New(sha256.New, d.inventoryKey[:])
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return inventoryCursor{}, errLifecycleProtocol
	}
	var cursor inventoryCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.Owner != owner || cursor.SessionID == "" || cursor.Ref == "" || cursor.Generation == 0 || cursor.EnvironmentID == "" {
		return inventoryCursor{}, errLifecycleProtocol
	}
	return cursor, nil
}

func (d *Daemon) reconcile(ctx context.Context, request LifecycleRequest) error {
	if err := validateOwnerRequest(request, false); err != nil {
		return err
	}
	if d.repositoryAttachments != nil {
		return d.repositoryStartupErr
	}
	if d.reconciler == nil {
		return ErrEnvironmentUnavailable
	}
	return d.reconciler.Reconcile(ctx)
}

func validateOwnerRequest(request LifecycleRequest, allowPayload bool) error {
	want := control.Binding{Owner: request.Binding.Owner}
	if request.Binding.Owner == "" || request.Binding != want || request.Create != nil || request.Provision != nil || (!allowPayload && len(request.Payload) != 0) {
		return control.ErrBindingMismatch
	}
	return nil
}

func boundedLifecycleError(err error) string {
	text := strings.ToValidUTF8(err.Error(), "�")
	runes := []rune(text)
	if len(runes) > 512 {
		text = string(runes[:512])
	}
	return text
}

func (d *Daemon) fork(ctx context.Context, request LifecycleRequest) (LifecycleResponse, error) {
	if d.children == nil {
		return LifecycleResponse{}, ErrEnvironmentUnavailable
	}
	parent, err := d.boundRecord(ctx, request.Binding)
	if err != nil {
		return LifecycleResponse{}, err
	}
	var payload ChildForkPayload
	if json.Unmarshal(request.Payload, &payload) != nil || payload.Label == "" || len(payload.Label) > 256 {
		return LifecycleResponse{}, errLifecycleProtocol
	}
	child, err := d.children.Fork(ctx, parent, payload.Label)
	if err != nil {
		return LifecycleResponse{}, err
	}
	if child.ParentRef != parent.Ref || child.State != EnvironmentReady {
		return LifecycleResponse{}, ErrInvalidFork
	}
	return LifecycleResponse{Binding: bindingForRecord(child), Record: recordPointer(child)}, nil
}

func (d *Daemon) merge(ctx context.Context, request LifecycleRequest) (LifecycleResponse, error) {
	if d.children == nil {
		return LifecycleResponse{}, ErrEnvironmentUnavailable
	}
	if err := claimValidate(request.Binding); err != nil {
		return LifecycleResponse{}, control.ErrBindingMismatch
	}
	parentRef := session.EnvironmentRef{Kind: session.EnvironmentKind(Kind), ID: request.Binding.Ref}
	lock := &d.mergeLocks[parentMergeLock(parentRef)]
	lock.Lock()
	defer lock.Unlock()

	// Binding lookup, child validation, conflict discovery, patch construction,
	// revalidation, and apply are one daemon-owned transaction across clients.
	parent, err := d.boundRecord(ctx, request.Binding)
	if err != nil {
		return LifecycleResponse{}, err
	}
	var payload ChildMergePayload
	if json.Unmarshal(request.Payload, &payload) != nil {
		return LifecycleResponse{}, errLifecycleProtocol
	}
	child, err := d.boundRecord(ctx, payload.Child)
	if err != nil {
		return LifecycleResponse{}, err
	}
	if child.ParentRef != parent.Ref {
		return LifecycleResponse{}, ErrInvalidFork
	}
	if err := d.children.Merge(ctx, parent, child); err != nil {
		return LifecycleResponse{}, err
	}
	return LifecycleResponse{Binding: bindingForRecord(parent), Record: recordPointer(parent)}, nil
}

func (d *Daemon) create(ctx context.Context, request LifecycleRequest) (LifecycleResponse, error) {
	if (request.Create == nil) == (request.Provision == nil) || request.Binding.Owner == "" || request.Binding.SessionID == "" {
		return LifecycleResponse{}, errLifecycleProtocol
	}
	if request.Binding.EnvironmentID != "" || request.Binding.Ref != "" || request.Binding.Generation != 0 {
		return LifecycleResponse{}, control.ErrBindingMismatch
	}
	created, err := d.performCreate(ctx, request)
	if err != nil {
		return LifecycleResponse{}, err
	}
	environmentID, generation, err := parseEnvironmentRef(created.Ref)
	if err != nil || generation != created.Generation {
		return LifecycleResponse{}, ErrEnvironmentStale
	}
	record, err := d.registry.Lookup(ctx, environmentID)
	if err != nil {
		return LifecycleResponse{}, fmt.Errorf("load created microvm generation: %w", err)
	}
	binding := bindingForRecord(record)
	if binding.Owner != request.Binding.Owner || binding.SessionID != request.Binding.SessionID ||
		binding.Ref != created.Ref.ID || binding.Generation != created.Generation {
		return LifecycleResponse{}, control.ErrBindingMismatch
	}
	wireCreated := LifecycleCreated{
		Ref: created.Ref, Generation: created.Generation,
		HostWorktree: created.HostWorktree, GuestRoot: created.GuestRoot,
		Profile: record.ProfileStatus.Profile, GuestEgress: record.ProfileStatus.GuestEgress, HostEgress: record.ProfileStatus.HostEgress,
	}
	return LifecycleResponse{Binding: binding, Record: recordPointer(record), Created: &wireCreated}, nil
}

func (d *Daemon) performCreate(ctx context.Context, request LifecycleRequest) (CreatedEnvironment, error) {
	if request.Provision == nil {
		if d.creator == nil || request.Create.Owner != request.Binding.Owner || request.Create.SessionID != request.Binding.SessionID {
			return CreatedEnvironment{}, control.ErrBindingMismatch
		}
		return d.creator.Create(ctx, *request.Create)
	}
	if d.provisioner == nil || d.creator == nil || request.Provision.Owner != request.Binding.Owner || request.Provision.SessionID != request.Binding.SessionID {
		return CreatedEnvironment{}, control.ErrBindingMismatch
	}
	expanded, err := d.provisioner(ctx, *request.Provision)
	if err != nil {
		return CreatedEnvironment{}, err
	}
	return d.creator.Create(ctx, expanded)
}

func (d *Daemon) existing(ctx context.Context, request LifecycleRequest) (LifecycleResponse, error) {
	var response LifecycleResponse
	record, err := d.boundRecord(ctx, request.Binding)
	if err != nil {
		return LifecycleResponse{}, err
	}
	if request.Operation == LifecycleWorkspace || request.Operation == LifecycleExec {
		return d.proxy(ctx, request, record)
	}
	switch request.Operation {
	case LifecycleResolve:
		record, err = NewReattachingResolver(d.registry, d.runtime).Resolve(ctx, record.Ref, record.Owner)
	case LifecycleInspect:
		var status RuntimeStatus
		status, err = d.runtime.Inspect(ctx, record)
		if err == nil {
			err = validateRuntimeIdentity(record, status)
		}
	case LifecycleDetach:
		err = d.manager.Detach(ctx, record.Ref, record.Owner)
		if d.observer != nil {
			outcome := OutcomeSuccess
			if err != nil {
				outcome = OutcomeFailure
			}
			d.observer.DetachFinished(outcome)
		}
	case LifecycleDelete, LifecycleChildDelete:
		if request.Operation == LifecycleChildDelete {
			err = d.manager.DeleteChild(ctx, record.Ref, record.Owner)
		} else {
			err = d.manager.Delete(ctx, record.Ref, record.Owner, DeleteExplicit)
		}
		if err == nil {
			record, err = d.registry.Lookup(ctx, record.EnvironmentID)
			if err == nil {
				response.Payload, err = json.Marshal(LifecycleDeleteResult{WorktreePath: record.WorktreePath, WorktreeRetained: record.PreserveWorktree || !record.WorktreeDeleted})
			}
		}
	}
	if err != nil {
		return LifecycleResponse{}, err
	}
	response.Binding, response.Record = bindingForRecord(record), recordPointer(record)
	return response, nil
}

func (d *Daemon) boundRecord(ctx context.Context, claim control.Binding) (EnvironmentRecord, error) {
	if err := claimValidate(claim); err != nil {
		return EnvironmentRecord{}, control.ErrBindingMismatch
	}
	record, err := d.registry.Lookup(ctx, claim.EnvironmentID)
	if err != nil {
		return EnvironmentRecord{}, err
	}
	if bindingForRecord(record) != claim {
		return EnvironmentRecord{}, control.ErrBindingMismatch
	}
	if record.State == EnvironmentDestroyed || record.Tombstone {
		return EnvironmentRecord{}, ErrEnvironmentDestroyed
	}
	if record.State != EnvironmentReady {
		return EnvironmentRecord{}, ErrEnvironmentStale
	}
	return record, nil
}

func bindingForRecord(record EnvironmentRecord) control.Binding {
	return control.Binding{Owner: record.Owner, SessionID: record.SessionID, EnvironmentID: record.EnvironmentID, Ref: record.Ref.ID, Generation: record.Generation}
}

func claimValidate(claim control.Binding) error {
	if claim.Owner == "" || claim.SessionID == "" || claim.EnvironmentID == "" || claim.Ref == "" || claim.Generation == 0 {
		return control.ErrBindingMismatch
	}
	return nil
}

func recordPointer(record EnvironmentRecord) *EnvironmentRecord {
	cloned := cloneEnvironmentRecord(record)
	return &cloned
}

func lifecycleFailure(err error) LifecycleResponse {
	if errors.Is(err, ErrRepositoryLogicalRootUnavailable) {
		return LifecycleResponse{
			ErrorCode: "repository_logical_root_unavailable",
			ErrorText: ErrRepositoryLogicalRootUnavailable.Error(),
			Err:       ErrRepositoryLogicalRootUnavailable,
		}
	}
	return LifecycleResponse{ErrorCode: lifecycleErrorCode(err), ErrorText: err.Error(), Err: err}
}

func lifecycleErrorCode(err error) string {
	switch {
	case errors.Is(err, control.ErrUnauthenticatedPeer):
		return "unauthenticated"
	case errors.Is(err, control.ErrBindingMismatch), errors.Is(err, ErrEnvironmentForeign), errors.Is(err, ErrEnvironmentStale):
		return "binding_mismatch"
	case errors.Is(err, ErrEnvironmentUnknown):
		return "not_found"
	case errors.Is(err, ErrEnvironmentDestroyed):
		return "destroyed"
	case errors.Is(err, ErrEnvironmentUnavailable), errors.Is(err, ErrRuntimeIdentityMismatch):
		return "unavailable"
	case errors.Is(err, ErrRepositoryLogicalRootUnavailable):
		return "repository_logical_root_unavailable"
	default:
		return "failed_precondition"
	}
}
