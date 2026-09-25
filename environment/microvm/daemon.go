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
	"sync/atomic"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

// LifecycleProtocolVersion is the local microvmd management protocol version.
const LifecycleProtocolVersion uint16 = 4

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
	// LifecycleChildDelete force-cleans an exact delegated child generation.
	LifecycleChildDelete LifecycleOperation = "child-delete"
	// lifecycleShutdown asks the authenticated owner to stop this daemon. It is
	// manager-private and deliberately absent from public lifecycle clients.
	lifecycleShutdown LifecycleOperation = "shutdown"
)

var errLifecycleProtocol = errors.New("invalid microvmd lifecycle protocol request")

// LifecycleRequest is one bounded request on the authenticated daemon socket.
// Create binds the requested owner/session and returns the allocated ref/generation;
// every operation on an existing environment requires the complete Binding.
type LifecycleRequest struct {
	Version       uint16             `json:"version"`
	Operation     LifecycleOperation `json:"operation"`
	Binding       control.Binding    `json:"binding"`
	AcquisitionID string             `json:"acquisition_id,omitempty"`
	Provision     *ProvisionRequest  `json:"provision,omitempty"`
	Payload       json.RawMessage    `json:"payload,omitempty"`
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
	Binding       control.Binding      `json:"binding,omitempty"`
	AcquisitionID string               `json:"acquisition_id,omitempty"`
	Created       *LifecycleCreated    `json:"created,omitempty"`
	Stream        *LifecycleExecStream `json:"stream,omitempty"`
	Payload       json.RawMessage      `json:"payload,omitempty"`
	ErrorCode     string               `json:"error_code,omitempty"`
	ErrorText     string               `json:"error,omitempty"`
	Err           error                `json:"-"`
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
	Child              control.Binding `json:"child"`
	ChildAcquisitionID string          `json:"child_acquisition_id"`
}

// RepositoryPlacement contains one repository-scoped logical attachment request.
// Its artifact snapshot callback is consumed only by the registry's first-launch path.
type RepositoryPlacement struct {
	Request LogicalEnvironmentRequest
	Status  EnforcedProfileStatus
}

// RepositoryPlacementBuilder resolves daemon-owned profile and immutable artifact
// policy into one repository-scoped logical attachment request.
type RepositoryPlacementBuilder func(context.Context, ProvisionRequest) (RepositoryPlacement, error)

// DaemonConfig wires the authenticated protocol to durable lifecycle seams.
type DaemonConfig struct {
	Control                *control.Service
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

type lifecycleAcquisition struct {
	binding control.Binding
	conn    net.Conn
}

type lifecycleRefPhase uint8

const (
	refPhaseActive lifecycleRefPhase = iota
	refPhaseAcquiring
	refPhaseDetaching
	refPhasePendingDetach
	refPhaseDeleting
	refPhasePendingDelete
	refPhaseReconciling
)

type lifecycleRefOwnership struct {
	binding  control.Binding
	resolves int
	owners   map[string]struct{}
	pins     int
	phase    lifecycleRefPhase
	changed  chan struct{}
	drained  chan struct{}
}

var errAcquisitionInUse = errors.New("microvmd acquisition is in use")

// Daemon owns the local management protocol. Hypervisor creation remains
// exclusively behind Lifecycle -> VMRuntime.
type Daemon struct {
	control                  *control.Service
	observer                 *OperationsObserver
	info                     DaemonInfo
	repositoryAttachments    *RepositoryAttachmentManager
	repositoryProvisioner    RepositoryPlacementBuilder
	repositoryStartupErr     error
	repositoryMu             sync.Mutex
	repositoryBindings       map[string]repositoryDaemonBinding
	beforeAcquisitionPublish func(context.Context)
	ownershipMu              sync.Mutex
	acquisitions             map[string]lifecycleAcquisition
	refOwnership             map[string]*lifecycleRefOwnership
	inventoryKey             [sha256.Size]byte
	shutdown                 chan struct{}
	shutdownOnce             sync.Once
}

// NewDaemon constructs the fail-closed local lifecycle service.
func NewDaemon(cfg DaemonConfig) (*Daemon, error) {
	if cfg.Control == nil || cfg.RepositoryAttachments == nil {
		return nil, errors.New("repository microvmd lifecycle service is not fully configured")
	}
	daemon := &Daemon{
		control: cfg.Control, observer: cfg.Observer, info: cfg.Info,
		repositoryAttachments: cfg.RepositoryAttachments, repositoryProvisioner: cfg.RepositoryProvisioner,
		repositoryStartupErr: cfg.RepositoryStartupError,
		repositoryBindings:   make(map[string]repositoryDaemonBinding),
		acquisitions:         make(map[string]lifecycleAcquisition),
		refOwnership:         make(map[string]*lifecycleRefOwnership),
		shutdown:             make(chan struct{}),
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

// ServeConn authenticates the peer and serves either one bounded operation or
// one retained acquisition followed by its terminal release frame.
func (d *Daemon) ServeConn(ctx context.Context, conn net.Conn) error {
	if d == nil || d.control == nil {
		return errors.New("microvmd lifecycle service is not configured")
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	if err := d.control.Authenticate(conn); err != nil {
		return newLifecycleServeError("connection", lifecycleErrorCode(err), err)
	}
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	var request LifecycleRequest
	if err := codec.Read(conn, &request); err != nil {
		return newLifecycleServeError("connection", "transport", err)
	}
	if request.Version != LifecycleProtocolVersion {
		return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(errLifecycleProtocol))
	}
	if request.Operation == LifecycleCreate || request.Operation == LifecycleResolve || request.Operation == LifecycleFork {
		return d.serveAcquisition(ctx, conn, codec, request)
	}
	if request.Operation == LifecycleDetach || request.Operation == LifecycleChildDelete || (request.Operation == LifecycleDelete && request.AcquisitionID != "") {
		return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(control.ErrBindingMismatch))
	}

	var unpin func()
	deleteGate := false
	if request.AcquisitionID != "" {
		var err error
		unpin, err = d.pinAcquisition(request.Binding, request.AcquisitionID)
		if err != nil {
			return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(err))
		}
		defer unpin()
		if request.Operation == LifecycleMerge {
			var payload ChildMergePayload
			if json.Unmarshal(request.Payload, &payload) != nil || payload.ChildAcquisitionID == "" {
				return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(errLifecycleProtocol))
			}
			unpinChild, pinErr := d.pinAcquisition(payload.Child, payload.ChildAcquisitionID)
			if pinErr != nil {
				return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(pinErr))
			}
			defer unpinChild()
		}
	} else if operationRequiresAcquisition(request.Operation) {
		return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(control.ErrBindingMismatch))
	} else if request.Operation == LifecycleDelete {
		if err := d.beginUnownedDeletion(request.Binding); err != nil {
			return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(err))
		}
		deleteGate = true
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
	if deleteGate {
		d.finishUnownedDeletion(request.Binding, response.Err)
	}
	if err := write(response); err != nil {
		return newLifecycleServeError(request.Operation, "transport", err)
	}
	if response.Err != nil {
		return newLifecycleServeError(request.Operation, response.ErrorCode, response.Err)
	}
	if request.Operation == lifecycleShutdown {
		d.shutdownOnce.Do(func() { close(d.shutdown) })
	}
	return nil
}

func operationRequiresAcquisition(operation LifecycleOperation) bool {
	switch operation {
	case LifecycleWorkspace, LifecycleExec, LifecycleMerge:
		return true
	default:
		return false
	}
}

func (d *Daemon) writeServeResponse(codec control.Codec, conn net.Conn, operation LifecycleOperation, response LifecycleResponse) error {
	if err := codec.Write(conn, response); err != nil {
		return newLifecycleServeError(operation, "transport", err)
	}
	if response.Err != nil {
		return newLifecycleServeError(operation, response.ErrorCode, response.Err)
	}
	return nil
}

func (d *Daemon) serveAcquisition(ctx context.Context, conn net.Conn, codec control.Codec, request LifecycleRequest) (retErr error) {
	if (request.Operation == LifecycleCreate || request.Operation == LifecycleResolve) && request.AcquisitionID != "" {
		return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(control.ErrBindingMismatch))
	}
	var unpin func()
	if request.Operation == LifecycleFork {
		var err error
		unpin, err = d.pinAcquisition(request.Binding, request.AcquisitionID)
		if err != nil {
			return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(err))
		}
		defer func() {
			if unpin != nil {
				unpin()
			}
		}()
	}
	acquireCtx, cancelAcquire := context.WithCancel(ctx)
	defer cancelAcquire()
	type readResult struct {
		request LifecycleRequest
		err     error
	}
	read := make(chan readResult, 1)
	readerDone := make(chan struct{})
	defer func() { _ = conn.Close(); <-readerDone }()
	var replying atomic.Bool
	go func() {
		defer close(readerDone)
		var terminal LifecycleRequest
		err := codec.Read(conn, &terminal)
		read <- readResult{request: terminal, err: err}
		if !replying.Load() {
			cancelAcquire()
		}
	}()

	reservedResolve := false
	if request.Operation == LifecycleResolve {
		var err error
		reservedResolve, err = d.beginResolve(acquireCtx, request.Binding)
		if err != nil {
			return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(err))
		}
		defer func() {
			if reservedResolve {
				retErr = errors.Join(retErr, d.abortResolve(ctx, request.Binding))
			}
		}()
	}

	response := d.handleAuthenticated(acquireCtx, request)
	if unpin != nil {
		unpin()
		unpin = nil
	}
	if response.Err != nil {
		return d.writeServeResponse(codec, conn, request.Operation, response)
	}
	if d.beforeAcquisitionPublish != nil {
		d.beforeAcquisitionPublish(acquireCtx)
	}
	published := false
	defer func() {
		if !published && (request.Operation == LifecycleCreate || request.Operation == LifecycleFork) {
			cleanupErr := d.beginUnownedDeletion(response.Binding)
			if cleanupErr == nil {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), repositoryRollbackTimeout)
				_, cleanupErr = d.repositoryOperation(cleanupCtx, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: response.Binding})
				cancel()
				d.finishUnownedDeletion(response.Binding, cleanupErr)
			}
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()
	select {
	case early := <-read:
		if early.err != nil {
			return newLifecycleServeError(request.Operation, "transport", early.err)
		}
		return d.writeServeResponse(codec, conn, early.request.Operation, lifecycleFailure(errLifecycleProtocol))
	default:
	}

	replying.Store(true)
	id, err := d.publishAcquisition(conn, response.Binding)
	if err != nil {
		return d.writeServeResponse(codec, conn, request.Operation, lifecycleFailure(err))
	}
	published = true
	reservedResolve = false
	response.AcquisitionID = id
	if err := codec.Write(conn, response); err != nil {
		destroy := request.Operation == LifecycleCreate || request.Operation == LifecycleFork
		_, _ = d.releaseAcquisition(ctx, conn, response.Binding, id, destroy)
		return newLifecycleServeError(request.Operation, "transport", err)
	}

	terminalResult := <-read
	if terminalResult.err != nil {
		_, releaseErr := d.releaseAcquisition(ctx, conn, response.Binding, id, false)
		if releaseErr != nil {
			return newLifecycleServeError(request.Operation, lifecycleErrorCode(releaseErr), releaseErr)
		}
		return nil
	}
	terminal := terminalResult.request
	if terminal.Version != LifecycleProtocolVersion || terminal.Binding != response.Binding || terminal.AcquisitionID != id ||
		(terminal.Operation != LifecycleDetach && terminal.Operation != LifecycleDelete && terminal.Operation != LifecycleChildDelete) || terminal.Provision != nil || len(terminal.Payload) != 0 {
		_, _ = d.releaseAcquisition(ctx, conn, response.Binding, id, false)
		return d.writeServeResponse(codec, conn, terminal.Operation, lifecycleFailure(control.ErrBindingMismatch))
	}
	terminalResponse, releaseErr := d.releaseAcquisition(ctx, conn, response.Binding, id, terminal.Operation != LifecycleDetach)
	if releaseErr != nil {
		terminalResponse = lifecycleFailure(releaseErr)
	}
	terminalResponse.Binding = response.Binding
	terminalResponse.AcquisitionID = id
	return d.writeServeResponse(codec, conn, terminal.Operation, terminalResponse)
}

func notifyRefState(state *lifecycleRefOwnership) {
	if state.changed != nil {
		close(state.changed)
	}
	state.changed = make(chan struct{})
}

func (d *Daemon) beginResolve(ctx context.Context, binding control.Binding) (bool, error) {
	if err := claimValidate(binding); err != nil {
		return false, control.ErrBindingMismatch
	}
	if err := d.repositoryAttachments.validateBinding(binding, false); err != nil {
		return false, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		d.ownershipMu.Lock()
		state := d.refOwnership[binding.Ref]
		if state == nil {
			d.refOwnership[binding.Ref] = &lifecycleRefOwnership{binding: binding, resolves: 1, owners: make(map[string]struct{}), phase: refPhaseAcquiring, changed: make(chan struct{})}
			d.ownershipMu.Unlock()
			return true, nil
		}
		if state.binding != binding {
			d.ownershipMu.Unlock()
			return false, control.ErrBindingMismatch
		}
		switch state.phase {
		case refPhaseActive:
			state.resolves++
			d.ownershipMu.Unlock()
			return true, nil
		case refPhasePendingDelete:
			d.ownershipMu.Unlock()
			return false, control.ErrBindingMismatch
		case refPhasePendingDetach:
			if state.pins != 0 {
				changed := state.changed
				d.ownershipMu.Unlock()
				select {
				case <-changed:
					continue
				case <-ctx.Done():
					return false, ctx.Err()
				}
			}
			state.phase = refPhaseReconciling
			notifyRefState(state)
			d.ownershipMu.Unlock()
			_, err := d.repositoryOperation(ctx, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: binding})
			d.ownershipMu.Lock()
			if d.refOwnership[binding.Ref] == state {
				if err == nil {
					delete(d.refOwnership, binding.Ref)
					close(state.changed)
				} else {
					state.phase = refPhasePendingDetach
					notifyRefState(state)
				}
			}
			d.ownershipMu.Unlock()
			if err != nil {
				return false, err
			}
		case refPhaseAcquiring, refPhaseDetaching, refPhaseDeleting, refPhaseReconciling:
			changed := state.changed
			d.ownershipMu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
	}
}

func (d *Daemon) abortResolve(ctx context.Context, binding control.Binding) error {
	d.ownershipMu.Lock()
	state := d.refOwnership[binding.Ref]
	if state == nil || state.binding != binding || state.resolves == 0 {
		d.ownershipMu.Unlock()
		return nil
	}
	state.resolves--
	if state.resolves != 0 || len(state.owners) != 0 {
		d.ownershipMu.Unlock()
		return nil
	}
	if d.repositoryAttachment(binding) == nil {
		delete(d.refOwnership, binding.Ref)
		close(state.changed)
		d.ownershipMu.Unlock()
		return nil
	}
	_, err := d.finishRelease(ctx, binding, state, false, false)
	return err
}

func (d *Daemon) publishAcquisition(conn net.Conn, binding control.Binding) (string, error) {
	if err := claimValidate(binding); err != nil {
		return "", control.ErrBindingMismatch
	}
	var raw [16]byte
	for {
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		id := fmt.Sprintf("%x", raw[:])
		d.ownershipMu.Lock()
		if _, exists := d.acquisitions[id]; exists {
			d.ownershipMu.Unlock()
			continue
		}
		if d.repositoryAttachment(binding) == nil {
			d.ownershipMu.Unlock()
			return "", control.ErrBindingMismatch
		}
		state := d.refOwnership[binding.Ref]
		if state == nil {
			state = &lifecycleRefOwnership{binding: binding, owners: make(map[string]struct{}), phase: refPhaseActive, changed: make(chan struct{})}
			d.refOwnership[binding.Ref] = state
		} else if state.binding != binding {
			d.ownershipMu.Unlock()
			return "", control.ErrBindingMismatch
		} else if state.phase == refPhaseAcquiring {
			state.phase = refPhaseActive
			notifyRefState(state)
		} else if state.phase != refPhaseActive {
			d.ownershipMu.Unlock()
			return "", control.ErrBindingMismatch
		}
		if state.resolves != 0 {
			state.resolves--
		}
		state.owners[id] = struct{}{}
		d.acquisitions[id] = lifecycleAcquisition{binding: binding, conn: conn}
		d.ownershipMu.Unlock()
		return id, nil
	}
}

func (d *Daemon) pinAcquisition(binding control.Binding, id string) (func(), error) {
	if id == "" {
		return nil, control.ErrBindingMismatch
	}
	d.ownershipMu.Lock()
	acquisition, ok := d.acquisitions[id]
	state := d.refOwnership[binding.Ref]
	if !ok || acquisition.binding != binding || state == nil || state.phase != refPhaseActive {
		d.ownershipMu.Unlock()
		return nil, control.ErrBindingMismatch
	}
	state.pins++
	d.ownershipMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			d.ownershipMu.Lock()
			state.pins--
			if state.pins == 0 && state.drained != nil {
				close(state.drained)
				state.drained = nil
			}
			notifyRefState(state)
			d.ownershipMu.Unlock()
		})
	}, nil
}

func (d *Daemon) beginUnownedDeletion(binding control.Binding) error {
	if err := claimValidate(binding); err != nil {
		return control.ErrBindingMismatch
	}
	if err := d.repositoryAttachments.validateBinding(binding, true); err != nil {
		return err
	}
	d.ownershipMu.Lock()
	defer d.ownershipMu.Unlock()
	state := d.refOwnership[binding.Ref]
	if state != nil && state.binding != binding {
		return control.ErrBindingMismatch
	}
	if state != nil && (len(state.owners) != 0 || state.pins != 0 || state.resolves != 0 || (state.phase != refPhaseActive && state.phase != refPhasePendingDelete && state.phase != refPhasePendingDetach)) {
		return errAcquisitionInUse
	}
	if state == nil {
		state = &lifecycleRefOwnership{binding: binding, owners: make(map[string]struct{}), changed: make(chan struct{})}
		d.refOwnership[binding.Ref] = state
	}
	state.phase = refPhaseDeleting
	notifyRefState(state)
	return nil
}

func (d *Daemon) finishUnownedDeletion(binding control.Binding, err error) {
	d.ownershipMu.Lock()
	state := d.refOwnership[binding.Ref]
	if state != nil && len(state.owners) == 0 && state.pins == 0 && state.phase == refPhaseDeleting {
		if err == nil {
			delete(d.refOwnership, binding.Ref)
			close(state.changed)
		} else {
			state.phase = refPhasePendingDelete
			notifyRefState(state)
		}
	}
	d.ownershipMu.Unlock()
}

func (d *Daemon) releaseAcquisition(ctx context.Context, conn net.Conn, binding control.Binding, id string, destroy bool) (LifecycleResponse, error) {
	d.ownershipMu.Lock()
	acquisition, ok := d.acquisitions[id]
	state := d.refOwnership[binding.Ref]
	if !ok || acquisition.binding != binding || acquisition.conn != conn || state == nil || state.phase != refPhaseActive {
		d.ownershipMu.Unlock()
		return LifecycleResponse{}, control.ErrBindingMismatch
	}
	delete(d.acquisitions, id)
	delete(state.owners, id)
	if destroy && (len(state.owners) != 0 || state.resolves != 0) {
		d.ownershipMu.Unlock()
		return LifecycleResponse{}, errAcquisitionInUse
	}
	inUse := destroy && state.pins != 0
	if inUse {
		destroy = false
	}
	if len(state.owners) != 0 || state.resolves != 0 {
		d.ownershipMu.Unlock()
		return LifecycleResponse{}, nil
	}
	return d.finishRelease(ctx, binding, state, destroy, inUse)
}

// finishRelease consumes ownershipMu and keeps the exact-ref gate until cleanup finishes.
func (d *Daemon) finishRelease(ctx context.Context, binding control.Binding, state *lifecycleRefOwnership, destroy, inUse bool) (LifecycleResponse, error) {
	if destroy {
		state.phase = refPhaseDeleting
	} else {
		state.phase = refPhaseDetaching
	}
	notifyRefState(state)
	if state.pins != 0 {
		state.drained = make(chan struct{})
		drained := state.drained
		d.ownershipMu.Unlock()
		select {
		case <-drained:
		case <-ctx.Done():
			d.ownershipMu.Lock()
			if d.refOwnership[binding.Ref] == state {
				state.phase = refPhasePendingDetach
				notifyRefState(state)
			}
			d.ownershipMu.Unlock()
			return LifecycleResponse{}, ctx.Err()
		}
	} else {
		d.ownershipMu.Unlock()
	}

	request := LifecycleRequest{Version: LifecycleProtocolVersion, Binding: binding, Operation: LifecycleDetach}
	if destroy {
		request.Operation = LifecycleDelete
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, repositoryRollbackTimeout)
	defer cancel()
	response, err := d.repositoryOperation(cleanupCtx, request)
	d.ownershipMu.Lock()
	if d.refOwnership[binding.Ref] == state {
		if err == nil {
			delete(d.refOwnership, binding.Ref)
			close(state.changed)
		} else if destroy {
			state.phase = refPhasePendingDelete
			notifyRefState(state)
		} else {
			state.phase = refPhasePendingDetach
			notifyRefState(state)
		}
	}
	d.ownershipMu.Unlock()
	if err == nil && inUse {
		return response, errAcquisitionInUse
	}
	return response, err
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
	if request.Version == LifecycleProtocolVersion && (request.Operation == LifecycleCreate || request.Operation == LifecycleResolve || request.Operation == LifecycleFork) {
		return lifecycleFailure(errLifecycleProtocol)
	}
	return d.handleAuthenticated(ctx, request)
}

func (d *Daemon) handleExecStream(ctx context.Context, request LifecycleRequest, send func(LifecycleExecStream) error) LifecycleResponse {
	if request.Version != LifecycleProtocolVersion || request.Operation != LifecycleExec {
		return lifecycleFailure(errLifecycleProtocol)
	}
	if !strings.HasPrefix(request.Binding.EnvironmentID, "logical-") {
		return lifecycleFailure(ErrEnvironmentUnavailable)
	}
	response, err := d.repositoryExecStream(ctx, request, send)
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

func (d *Daemon) handleStandardRequest(ctx context.Context, request LifecycleRequest) LifecycleResponse {
	var response LifecycleResponse
	var err error
	switch request.Operation {
	case LifecycleInfo:
		if request.Binding != (control.Binding{}) || request.Provision != nil || len(request.Payload) != 0 || d.info.ProtocolVersion != LifecycleProtocolVersion {
			err = errLifecycleProtocol
			break
		}
		if d.repositoryStartupErr != nil {
			err = d.repositoryStartupErr
			break
		}
		response.Payload, err = json.Marshal(d.info)
	case lifecycleShutdown:
		var expected DaemonInfo
		if request.Binding != (control.Binding{}) || request.Provision != nil || len(request.Payload) == 0 || json.Unmarshal(request.Payload, &expected) != nil || !expected.Equal(d.info) {
			err = errLifecycleProtocol
		}
	case LifecycleMetrics:
		if d.observer == nil || request.Binding != (control.Binding{}) || request.Provision != nil {
			err = errLifecycleProtocol
			break
		}
		response.Payload, err = json.Marshal(d.observer.Snapshot())
	case LifecycleInventory:
		response, err = d.inventory(ctx, request)
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
	return d.repositoryInventory(ctx, request.Binding.Owner, pageRequest.PageSize, cursor)
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
		case record.worktreeRetained && !record.deleted:
			entry.Health = GenerationStale
			entry.Error = "dirty schedule worktree retained and exact-reattachable for recovery"
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
		page.Continuation, err = d.encodeInventoryCursor(owner, inventoryCursor{
			SessionID: last.SessionID, Ref: last.Ref,
			Generation: last.Generation, EnvironmentID: last.EnvironmentID,
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

func (d *Daemon) encodeInventoryCursor(owner string, cursor inventoryCursor) (string, error) {
	cursor.Owner = owner
	payload, err := json.Marshal(cursor)
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

func validateOwnerRequest(request LifecycleRequest, allowPayload bool) error {
	want := control.Binding{Owner: request.Binding.Owner}
	if request.Binding.Owner == "" || request.Binding != want || request.Provision != nil || (!allowPayload && len(request.Payload) != 0) {
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

func claimValidate(claim control.Binding) error {
	if claim.Owner == "" || claim.SessionID == "" || claim.EnvironmentID == "" || claim.Ref == "" || claim.Generation == 0 {
		return control.ErrBindingMismatch
	}
	return nil
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
	case errors.Is(err, control.ErrBindingMismatch):
		return "binding_mismatch"
	case errors.Is(err, ErrEnvironmentUnavailable):
		return "unavailable"
	case errors.Is(err, ErrRepositoryLogicalRootUnavailable):
		return "repository_logical_root_unavailable"
	case errors.Is(err, errAcquisitionInUse):
		return "in_use"
	default:
		return "failed_precondition"
	}
}
