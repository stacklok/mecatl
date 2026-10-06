package microvm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
)

const (
	repositoryMountTag        = "mecatl-repository-logical"
	repositoryObjectMountTag  = "mecatl-git-objects"
	repositoryRollbackTimeout = 5 * time.Second
	// RepositoryGuestMountRoot is the only guest namespace containing logical worktrees.
	RepositoryGuestMountRoot = "/run/mecatl/repositories"
)

// RepositoryLaunchReconciler proves historical launch attempts dead before replacement.
type RepositoryLaunchReconciler interface {
	Reconcile(context.Context, string) (LaunchReconcileResult, error)
}

// RepositoryRuntimeConfig wires the repository-scoped production adapters to
// the lowest hypervisor and guest-transport seams.
type RepositoryRuntimeConfig struct {
	Backend          GoMicroVMBackend
	DialGuest        GuestDialer
	DialControl      GuestDialer
	UnixEndpoint     bool
	EndpointRoot     string
	Network          *NetworkController
	GuestEgress      GuestEgressPolicy
	ArtifactPolicy   string
	Artifacts        map[ArtifactKind]ArtifactRequest
	LaunchReconciler RepositoryLaunchReconciler
	Observer         *OperationsObserver
}

type repositoryRuntimeGeneration struct {
	record         RepositoryVMRecord
	instance       GoMicroVMInstance
	issuer         *control.CapabilityIssuer
	authorityKey   []byte
	network        NetworkHandle
	objectSnapshot string
	controlMu      sync.Mutex
	nextSequence   uint64
	pending        *guestagent.RepositoryControlRequest
	registrations  map[string]*repositoryHostRegistration
}

type repositoryHostRegistration struct {
	binding     control.Binding
	incarnation string
	registered  bool
	tearingDown bool
}

type repositoryRuntimeAttacher interface {
	AttachRepository(RepositoryVMRecord, RepositoryBootAuthority) error
}

type repositoryRuntimeAborter interface {
	Abort(context.Context, RepositoryVMRecord) error
}

// RepositoryRuntime is both the concrete singleton VM runtime and logical guest
// registrar used by production microvmd composition.
type RepositoryRuntime struct {
	backend          GoMicroVMBackend
	dial             GuestDialer
	controlDial      GuestDialer
	unixEndpoint     bool
	network          *NetworkController
	guestEgress      GuestEgressPolicy
	observer         *OperationsObserver
	launchReconciler RepositoryLaunchReconciler
	rollbackTimeout  time.Duration
	mu               sync.Mutex
	vms              map[string]*repositoryRuntimeGeneration
	listeners        map[string]*net.UnixListener
}

// NewRepositoryRuntime constructs the production repository adapters. There is
// deliberately no host filesystem or command-runner fallback.
func NewRepositoryRuntime(cfg RepositoryRuntimeConfig) (*RepositoryRuntime, error) {
	if cfg.Backend == nil || cfg.Network == nil || (cfg.UnixEndpoint && (cfg.DialGuest != nil || cfg.DialControl != nil)) || (!cfg.UnixEndpoint && (cfg.DialGuest == nil || cfg.DialControl == nil)) {
		return nil, errors.New("repository microvm runtime is not fully configured")
	}
	if backend, ok := cfg.Backend.(*LibkrunBackend); ok && (backend == nil || backend.launchOwnership == nil) {
		return nil, ErrLaunchOwnershipUnsupported
	}
	if cfg.LaunchReconciler == nil {
		cfg.LaunchReconciler, _ = cfg.Backend.(RepositoryLaunchReconciler)
	}
	runtime := &RepositoryRuntime{backend: cfg.Backend, dial: cfg.DialGuest, controlDial: cfg.DialControl, unixEndpoint: cfg.UnixEndpoint, network: cfg.Network, guestEgress: cloneGuestEgressPolicy(cfg.GuestEgress), observer: cfg.Observer, launchReconciler: cfg.LaunchReconciler, rollbackTimeout: repositoryRollbackTimeout, vms: make(map[string]*repositoryRuntimeGeneration), listeners: make(map[string]*net.UnixListener)}
	if cfg.UnixEndpoint {
		runtime.dial = runtime.acceptGuest
		runtime.controlDial = runtime.acceptGuest
	}
	return runtime, nil
}

// Start boots exactly the already-materialized repository rootfs and exports the
// repository logical namespace once. Individual worktrees are addressed only by
// guest-visible paths below RepositoryGuestMountRoot.
func (r *RepositoryRuntime) Start(ctx context.Context, record RepositoryVMRecord, verified VerifiedArtifacts, authority RepositoryBootAuthority) (_ RuntimeStatus, retErr error) { //nolint:gocyclo // explicit ownership transaction
	listenerOwned := false
	if r.unixEndpoint {
		if err := r.listenGuest(record.Endpoint); err != nil {
			return RuntimeStatus{}, err
		}
		listenerOwned = true
	}
	logicalRoot := filepath.Join(filepath.Dir(record.RootFSPath), "logical")
	networkDir := record.Endpoint + ".network"
	networkDirOwned := false
	var network NetworkHandle
	var instance GoMicroVMInstance
	owned := &repositoryRuntimeGeneration{record: record, authorityKey: authority.bytes(), nextSequence: 1, registrations: make(map[string]*repositoryHostRegistration)}
	committed := false
	defer func() {
		if committed {
			return
		}
		retErr = errors.Join(retErr, r.rollback(ctx, record, owned, networkDir, networkDirOwned, listenerOwned))
	}()
	objectSnapshot, err := snapshotRepositoryObjects(ctx, record.GitCommonDirectory, filepath.Dir(record.RootFSPath))
	if err != nil {
		return RuntimeStatus{}, err
	}
	owned.objectSnapshot = objectSnapshot
	if err := os.Mkdir(networkDir, 0o700); err != nil {
		return RuntimeStatus{}, err
	}
	networkDirOwned = true
	// The provider belongs to the repository generation, not the create RPC.
	// Abort owns its cancellation after the request context is gone.
	network, err = r.network.start(context.WithoutCancel(ctx), r.guestEgress, networkDir)
	if err != nil {
		return RuntimeStatus{}, err
	}
	owned.network = network
	if source, ok := network.Provider.(egressDenialSource); ok && r.observer != nil {
		r.observer.trackEgressDenials(record.VMID, source)
	}
	launch := GoMicroVMLaunch{
		EnvironmentID: repositoryLaunchEnvironmentID(record.RepositoryKey), VMID: record.VMID, Endpoint: record.Endpoint, Generation: record.bootGeneration(), PlacementGeneration: record.Generation,
		RepositoryOwner: record.Owner, RepositoryKey: record.RepositoryKey,
		RuntimePath: verified.Runtime.Path, FirmwarePath: verified.Firmware.Path, ImagePath: record.RootFSPath,
		Network: network.Provider, NetworkSocket: network.SocketPath,
		VsockPort:     control.GuestControlPort,
		Mounts:        repositoryMounts(logicalRoot, objectSnapshot),
		CapabilityKey: authority.bytes(), Verified: true, HostReadOnly: true,
		DisableIPv6: r.guestEgress.tightened(),
	}
	instance, err = r.backend.Start(ctx, launch)
	if err != nil {
		return RuntimeStatus{}, err
	}
	owned.instance = instance
	if err := instance.WaitReady(ctx); err != nil {
		return RuntimeStatus{}, err
	}
	status, err := instance.Status(ctx)
	if err != nil {
		return RuntimeStatus{}, err
	}
	issuer, err := control.NewCapabilityIssuer(authority.bytes())
	if err != nil {
		return RuntimeStatus{}, err
	}
	owned.issuer = issuer
	r.mu.Lock()
	if r.vms[record.VMID] != nil {
		r.mu.Unlock()
		return RuntimeStatus{}, ErrRepositoryVMInconsistent
	}
	r.vms[record.VMID] = owned
	r.mu.Unlock()

	// The guest serves authenticated control only after all privileged startup
	// work, including requested IPv6 disablement, has succeeded. Prove that point
	// before this generation can be committed ready.
	verifiedStatus := status
	challenge, err := authority.healthChallenge(record)
	if err == nil {
		var response RepositoryHealthResponse
		response, err = r.Health(ctx, record, challenge)
		if err == nil {
			err = authority.VerifyHealth(record, challenge, response)
		}
		if err == nil {
			err = validateProvisionalRuntime(record, response.Status)
			verifiedStatus = response.Status
		}
	}
	if err != nil {
		if r.guestEgress.tightened() {
			return RuntimeStatus{}, fmt.Errorf("verify authenticated repository guest startup and IPv6 disablement: %w", err)
		}
		return RuntimeStatus{}, fmt.Errorf("verify authenticated repository guest startup: %w", err)
	}
	committed = true
	return verifiedStatus, nil
}

func (r *RepositoryRuntime) rollback(parent context.Context, record RepositoryVMRecord, owned *repositoryRuntimeGeneration, networkDir string, networkDirOwned, listenerOwned bool) error {
	timeout := r.rollbackTimeout
	if timeout <= 0 {
		timeout = repositoryRollbackTimeout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()

	var stopErr, cleanupBootErr error
	if owned.instance != nil {
		if err := owned.instance.Stop(ctx); err != nil {
			stopErr = fmt.Errorf("stop repository VM during rollback: %w", err)
		} else if err := owned.instance.CleanupBoot(ctx); err != nil {
			cleanupBootErr = fmt.Errorf("clean repository VM boot data during rollback: %w", err)
		}
	}
	if stopErr != nil || cleanupBootErr != nil {
		r.mu.Lock()
		existing := r.vms[record.VMID]
		if existing == nil {
			r.vms[record.VMID] = owned
		} else if existing != owned {
			cleanupBootErr = errors.Join(cleanupBootErr, ErrRepositoryVMInconsistent)
		}
		r.mu.Unlock()
		return errors.Join(stopErr, cleanupBootErr)
	}

	r.mu.Lock()
	if r.vms[record.VMID] == owned {
		delete(r.vms, record.VMID)
	}
	r.mu.Unlock()
	if owned.network.Provider != nil {
		if r.observer != nil {
			r.observer.untrackEgressDenials(record.VMID)
		}
		owned.network.Provider.Stop()
	}
	var cleanupErr error
	if owned.objectSnapshot != "" {
		if err := removeObjectSnapshot(owned.objectSnapshot); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove repository Git object snapshot during rollback: %w", err))
		}
	}
	if networkDirOwned {
		if err := os.RemoveAll(networkDir); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove repository network directory during rollback: %w", err))
		}
	}
	if listenerOwned {
		r.closeGuestListener(record.Endpoint)
	}
	return errors.Join(stopErr, cleanupErr)
}

// Abort discards a started generation only after VM teardown is confirmed.
func (r *RepositoryRuntime) Abort(ctx context.Context, record RepositoryVMRecord) error {
	r.mu.Lock()
	owned := r.vms[record.VMID]
	r.mu.Unlock()
	if owned == nil {
		return ErrEnvironmentUnavailable
	}
	if owned.record.Owner != record.Owner || owned.record.RepositoryKey != record.RepositoryKey || owned.record.Generation != record.Generation || owned.record.VMID != record.VMID || owned.record.Endpoint != record.Endpoint || owned.record.RootFSPath != record.RootFSPath || owned.record.AuthorityDigest != record.AuthorityDigest {
		return ErrRepositoryVMInconsistent
	}
	return r.rollback(ctx, record, owned, record.Endpoint+".network", true, r.unixEndpoint)
}

// Health performs guest-origin proof of the host's unpredictable challenge.
func (r *RepositoryRuntime) Health(ctx context.Context, record RepositoryVMRecord, challenge RepositoryHealthChallenge) (RepositoryHealthResponse, error) {
	r.mu.Lock()
	generation := r.vms[record.VMID]
	listener := r.listeners[record.Endpoint]
	r.mu.Unlock()
	if r.unixEndpoint && listener == nil {
		return RepositoryHealthResponse{}, errors.New("repository guest listener is not owned by this daemon process")
	}
	if generation == nil || generation.instance == nil || generation.network.Provider == nil || generation.network.SocketPath == "" {
		return RepositoryHealthResponse{}, errors.New("repository network backend is not live in this daemon process")
	}
	if generation.network.Provider.SocketPath() != generation.network.SocketPath {
		return RepositoryHealthResponse{}, errors.New("repository network backend endpoint changed")
	}
	status, err := generation.instance.Status(ctx)
	if err != nil {
		return RepositoryHealthResponse{}, err
	}
	wireStatus := guestagent.RepositoryHealthStatus{Live: status.Live, Generation: status.Generation, VMID: status.VMID, PID: status.PID, ProcessIdentity: status.ProcessIdentity, Endpoint: status.Endpoint}
	wireChallenge := guestagent.RepositoryHealthChallenge{Owner: challenge.Owner, RepositoryKey: challenge.RepositoryKey, VMID: challenge.VMID, Generation: challenge.Generation, Nonce: challenge.Nonce, Status: wireStatus}
	response, err := r.control(ctx, record, guestagent.RepositoryControlRequest{Operation: guestagent.RepositoryHealth, Health: &wireChallenge})
	if err != nil {
		return RepositoryHealthResponse{}, err
	}
	return RepositoryHealthResponse{Status: status, MAC: response.HealthMAC}, nil
}

// AttachRepository restores capability issuance only for a generation whose VM
// and network backend are still owned by this daemon process. A daemon restart
// cannot safely reconstruct go-microvm's in-process hosted network provider.
func (r *RepositoryRuntime) AttachRepository(record RepositoryVMRecord, authority RepositoryBootAuthority) error {
	issuer, err := control.NewCapabilityIssuer(authority.bytes())
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing := r.vms[record.VMID]
	if existing == nil || existing.instance == nil || existing.network.Provider == nil || existing.network.SocketPath == "" {
		return ErrEnvironmentUnavailable
	}
	existing.issuer = issuer
	existing.authorityKey = authority.bytes()
	return nil
}

// Register installs one boot-scoped registration transactionally through the
// separately authenticated data-plane connection.
func (r *RepositoryRuntime) Register(ctx context.Context, record RepositoryVMRecord, binding control.Binding, mount RepositoryGuestMount) (*guestagent.Services, error) {
	expectedHostRoot := filepath.Join(filepath.Dir(record.RootFSPath), "logical", filepath.FromSlash(strings.TrimPrefix(binding.AssignedRoot, RepositoryGuestMountRoot+"/")))
	if mount.HostPath == "" || mount.GuestPath != binding.AssignedRoot || !filepath.IsAbs(mount.HostPath) || filepath.Clean(mount.HostPath) != expectedHostRoot || !guestLogicalRoot(binding.AssignedRoot) {
		return nil, control.ErrBindingMismatch
	}
	generation, err := r.generation(record)
	if err != nil {
		return nil, err
	}
	generation.controlMu.Lock()
	defer generation.controlMu.Unlock()
	registration := generation.registrations[binding.Ref]
	if registration != nil && registration.binding != binding {
		return nil, control.ErrBindingMismatch
	}
	if registration != nil && registration.tearingDown {
		if err := r.unregisterLocked(ctx, record, generation, registration); err != nil {
			return nil, err
		}
		registration = nil
	}
	if registration == nil {
		incarnation, err := randomRegistrationIncarnation()
		if err != nil {
			return nil, err
		}
		registration = &repositoryHostRegistration{binding: binding, incarnation: incarnation}
		generation.registrations[binding.Ref] = registration
	}
	if !registration.registered {
		request := guestagent.RepositoryControlRequest{Operation: guestagent.RepositoryRegister, Binding: binding, Incarnation: registration.incarnation}
		if err := r.mutateLocked(ctx, record, generation, request); err != nil {
			return nil, err
		}
	}
	stream, err := r.authenticatedGuest(ctx, record, guestagent.RepositoryChannelData)
	if err == nil {
		var services *guestagent.Services
		services, err = guestagent.ConnectRepository(ctx, stream, binding, registration.incarnation)
		if err == nil {
			return services, nil
		}
		_ = stream.Close()
	}
	registration.tearingDown = true
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cleanupTimeout())
	rollbackErr := r.unregisterLocked(rollbackCtx, record, generation, registration)
	cancel()
	return nil, errors.Join(err, rollbackErr)
}

// Unregister retries the exact pending teardown and is idempotent after a
// confirmed removal.
func (r *RepositoryRuntime) Unregister(ctx context.Context, record RepositoryVMRecord, binding control.Binding) error {
	generation, err := r.generation(record)
	if err != nil {
		return err
	}
	generation.controlMu.Lock()
	defer generation.controlMu.Unlock()
	registration := generation.registrations[binding.Ref]
	if registration == nil {
		return nil
	}
	if registration.binding != binding {
		return control.ErrBindingMismatch
	}
	registration.tearingDown = true
	return r.unregisterLocked(ctx, record, generation, registration)
}

func (r *RepositoryRuntime) unregisterLocked(ctx context.Context, record RepositoryVMRecord, generation *repositoryRuntimeGeneration, registration *repositoryHostRegistration) error {
	request := guestagent.RepositoryControlRequest{Operation: guestagent.RepositoryUnregister, Binding: registration.binding, Incarnation: registration.incarnation}
	return r.mutateLocked(ctx, record, generation, request)
}

func applyRepositoryMutationAcknowledgement(generation *repositoryRuntimeGeneration, request guestagent.RepositoryControlRequest, succeeded bool) {
	registration := generation.registrations[request.Binding.Ref]
	if registration == nil || registration.binding != request.Binding || registration.incarnation != request.Incarnation {
		return
	}
	switch request.Operation {
	case guestagent.RepositoryRegister:
		registration.registered = succeeded
	case guestagent.RepositoryUnregister:
		if succeeded {
			delete(generation.registrations, request.Binding.Ref)
		}
	}
}

func (r *RepositoryRuntime) mutateLocked(ctx context.Context, record RepositoryVMRecord, generation *repositoryRuntimeGeneration, request guestagent.RepositoryControlRequest) error {
	if generation.pending != nil {
		pending := *generation.pending
		if pending.Operation != request.Operation || pending.Binding != request.Binding || pending.Incarnation != request.Incarnation {
			response, err := r.control(ctx, record, pending)
			if response.Sequence == pending.Sequence {
				applyRepositoryMutationAcknowledgement(generation, pending, err == nil)
				generation.pending = nil
				generation.nextSequence++
			}
			if err != nil {
				return err
			}
		} else {
			request = pending
		}
	}
	if request.Sequence == 0 {
		request.Sequence = generation.nextSequence
		copyRequest := request
		generation.pending = &copyRequest
	}
	response, err := r.control(ctx, record, request)
	if response.Sequence == request.Sequence {
		applyRepositoryMutationAcknowledgement(generation, request, err == nil)
		generation.pending = nil
		generation.nextSequence++
	}
	return err
}

func (r *RepositoryRuntime) cleanupTimeout() time.Duration {
	if r.rollbackTimeout > 0 {
		return r.rollbackTimeout
	}
	return repositoryRollbackTimeout
}

func randomRegistrationIncarnation() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("allocate repository registration incarnation: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (r *RepositoryRuntime) generation(record RepositoryVMRecord) (*repositoryRuntimeGeneration, error) {
	r.mu.Lock()
	generation := r.vms[record.VMID]
	r.mu.Unlock()
	if generation == nil {
		return nil, ErrEnvironmentUnavailable
	}
	return generation, nil
}

func (r *RepositoryRuntime) control(ctx context.Context, record RepositoryVMRecord, request guestagent.RepositoryControlRequest) (guestagent.RepositoryControlResponse, error) {
	stream, err := r.authenticatedGuest(ctx, record, guestagent.RepositoryChannelControl)
	if err != nil {
		return guestagent.RepositoryControlResponse{}, err
	}
	defer func() { _ = stream.Close() }()
	stopCancel := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stopCancel()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(stream, request); err != nil {
		return guestagent.RepositoryControlResponse{}, err
	}
	var response guestagent.RepositoryControlResponse
	if err := codec.Read(stream, &response); err != nil {
		return guestagent.RepositoryControlResponse{}, err
	}
	if request.Sequence != 0 && response.Sequence != request.Sequence {
		return guestagent.RepositoryControlResponse{}, errors.New("repository guest control response sequence mismatch")
	}
	if response.ErrorCode != "" {
		if response.ErrorCode == "logical_root_unavailable" {
			return response, ErrRepositoryLogicalRootUnavailable
		}
		return response, fmt.Errorf("repository guest control rejected: %s", response.ErrorCode)
	}
	return response, nil
}

const maxRejectedRepositoryGuestConnections = 16

func (r *RepositoryRuntime) authenticatedGuest(ctx context.Context, record RepositoryVMRecord, purpose guestagent.RepositoryChannelPurpose) (io.ReadWriteCloser, error) {
	r.mu.Lock()
	generation := r.vms[record.VMID]
	var authorityKey []byte
	if generation != nil {
		authorityKey = append(authorityKey, generation.authorityKey...)
	}
	r.mu.Unlock()
	if generation == nil {
		return nil, ErrEnvironmentUnavailable
	}
	for range maxRejectedRepositoryGuestConnections {
		stream, dialErr := r.dialForPurpose(ctx, record.Endpoint, purpose)
		if dialErr != nil {
			return nil, dialErr
		}
		authErr := guestagent.AuthenticateHostRepositoryChannel(ctx, stream, authorityKey, record.Owner, record.RepositoryKey, record.VMID, record.bootGeneration(), purpose)
		if authErr == nil {
			return stream, nil
		}
		_ = stream.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, guestagent.ErrUnauthenticatedRepositoryChannel
}

func (r *RepositoryRuntime) dialForPurpose(ctx context.Context, endpoint string, purpose guestagent.RepositoryChannelPurpose) (io.ReadWriteCloser, error) {
	if purpose == guestagent.RepositoryChannelControl {
		return r.controlDial(ctx, endpoint)
	}
	return r.dial(ctx, endpoint)
}

func (r *RepositoryRuntime) listenGuest(endpoint string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listeners[endpoint] != nil {
		return ErrRepositoryVMInconsistent
	}
	if err := os.Remove(endpoint); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale repository guest endpoint: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen for repository guest vsock: %w", err)
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return err
	}
	r.listeners[endpoint] = listener
	return nil
}

func (r *RepositoryRuntime) closeGuestListener(endpoint string) {
	r.mu.Lock()
	listener := r.listeners[endpoint]
	delete(r.listeners, endpoint)
	r.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	_ = os.Remove(endpoint)
}

func (r *RepositoryRuntime) acceptGuest(ctx context.Context, endpoint string) (io.ReadWriteCloser, error) {
	r.mu.Lock()
	listener := r.listeners[endpoint]
	r.mu.Unlock()
	if listener == nil {
		return nil, ErrEnvironmentUnavailable
	}
	for {
		if err := listener.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			return nil, err
		}
		conn, err := listener.AcceptUnix()
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			return nil, err
		}
	}
}

func repositoryLaunchEnvironmentID(repositoryKey string) string {
	return "repository-" + repositoryKey
}

// Reconcile proves that every historical runner for this stable repository is
// dead before the registry rotates boot authority and starts a replacement.
func (r *RepositoryRuntime) Reconcile(ctx context.Context, record RepositoryVMRecord) error {
	r.mu.Lock()
	for _, generation := range r.vms {
		if generation != nil && generation.record.RepositoryKey == record.RepositoryKey {
			r.mu.Unlock()
			return ErrLaunchOwnershipUncertain
		}
	}
	r.mu.Unlock()
	if r.launchReconciler == nil {
		return ErrLaunchOwnershipUnsupported
	}
	_, err := r.launchReconciler.Reconcile(ctx, repositoryLaunchEnvironmentID(record.RepositoryKey))
	return err
}

// Shutdown stops process-local VM and network resources while retaining every
// repository rootfs, logical worktree, attachment, and launch record.
func (r *RepositoryRuntime) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	generations := make([]*repositoryRuntimeGeneration, 0, len(r.vms))
	for _, generation := range r.vms {
		generations = append(generations, generation)
	}
	r.mu.Unlock()
	var result error
	for _, generation := range generations {
		if generation == nil {
			continue
		}
		if generation.instance != nil {
			if err := generation.instance.Stop(ctx); err != nil {
				result = errors.Join(result, err)
				continue
			}
			if err := generation.instance.CleanupBoot(ctx); err != nil {
				result = errors.Join(result, err)
				continue
			}
		}
		if generation.network.Provider != nil {
			generation.network.Provider.Stop()
		}
		if generation.objectSnapshot != "" {
			result = errors.Join(result, removeObjectSnapshot(generation.objectSnapshot))
		}
		r.closeGuestListener(generation.record.Endpoint)
		r.mu.Lock()
		delete(r.vms, generation.record.VMID)
		r.mu.Unlock()
	}
	return result
}

func guestLogicalRoot(root string) bool {
	rel, err := filepath.Rel(RepositoryGuestMountRoot, root)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return len(parts) == 2 && validOpaquePathComponent(parts[0]) && parts[1] == "worktree"
}

var _ RepositoryVMRuntime = (*RepositoryRuntime)(nil)
var _ RepositoryGuestRegistrar = (*RepositoryRuntime)(nil)
var _ io.Closer = (*guestagent.Services)(nil)
