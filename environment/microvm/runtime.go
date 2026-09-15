package microvm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	gomicrovm "github.com/stacklok/go-microvm"
	"github.com/stacklok/go-microvm/extract"
	gomicrovmlibkrun "github.com/stacklok/go-microvm/hypervisor/libkrun"
	gomicrovmimage "github.com/stacklok/go-microvm/image"
	gomicrovmnet "github.com/stacklok/go-microvm/net"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
	"github.com/stacklok/mecatl/environment/microvm/virtiofs"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

// GuestDialer opens the one vsock-backed host endpoint for a guest generation.
type GuestDialer func(context.Context, string) (io.ReadWriteCloser, error)

// CapabilityKeySource returns fresh per-boot guest capability key material.
type CapabilityKeySource func() ([]byte, error)

// GoMicroVMLaunch is the fully resolved, verified launch description handed to
// the hypervisor backend. Artifact paths are cache-admitted immutable payloads.
type GoMicroVMLaunch struct {
	EnvironmentID   string
	VMID            string
	Endpoint        string
	Generation      uint32
	RepositoryOwner string
	RepositoryKey   string
	Prepared        *worktree.Prepared
	RuntimePath     string
	FirmwarePath    string
	ImagePath       string
	NetworkSocket   string
	Network         gomicrovmnet.Provider
	VsockPort       uint32
	Mounts          []gomicrovm.VirtioFSMount
	Preboot         GuestPrebootArtifact
	CapabilityKey   []byte
	CPUs            uint32
	MemoryMiB       uint32
	Verified        bool
	HostReadOnly    bool
	DisableIPv6     bool
	// RepositoryRootFS tells the production backend to boot the already-private,
	// already-materialized singleton rootfs without cloning it per logical session.
	RepositoryRootFS bool
}

// GoMicroVMInstance is the concrete runtime handle retained by microvmd.
type GoMicroVMInstance interface {
	WaitReady(context.Context) error
	Status(context.Context) (RuntimeStatus, error)
	Stop(context.Context) error
	Remove(context.Context) error
}

// GoMicroVMBackend is the hypervisor seam. The production implementation below
// calls go-microvm; deterministic tests replace only this lowest-level seam.
type GoMicroVMBackend interface {
	Start(context.Context, GoMicroVMLaunch) (GoMicroVMInstance, error)
	Open(context.Context, EnvironmentRecord) (GoMicroVMInstance, error)
}

// GoMicroVMRuntimeConfig configures the concrete VMRuntime adapter.
type GoMicroVMRuntimeConfig struct {
	Backend           GoMicroVMBackend
	Network           *NetworkController
	GuestEgress       GuestEgressPolicy
	DialGuest         GuestDialer
	UnixGuestEndpoint bool
	CapabilityKey     CapabilityKeySource
	Observer          *OperationsObserver
}

type runtimeGeneration struct {
	instance   GoMicroVMInstance
	network    gomicrovmnet.Provider
	networkDir string
	issuer     *control.CapabilityIssuer
	services   *guestagent.Services
	binding    control.Binding
	endpoint   string
}

// GoMicroVMRuntime composes verified artifacts, virtio-fs, hosted networking,
// vsock, transferable capabilities, and the unified guest Workspace/exec stream.
type GoMicroVMRuntime struct {
	backend       GoMicroVMBackend
	network       *NetworkController
	guestEgress   GuestEgressPolicy
	dialGuest     GuestDialer
	unixEndpoint  bool
	capabilityKey CapabilityKeySource
	observer      *OperationsObserver

	mu          sync.Mutex
	generations map[string]*runtimeGeneration
	listeners   map[string]*net.UnixListener
}

// NewGoMicroVMRuntime constructs a fail-closed concrete runtime. No host
// Workspace or command-runner fallback is installed.
func NewGoMicroVMRuntime(cfg GoMicroVMRuntimeConfig) (*GoMicroVMRuntime, error) {
	if cfg.Backend == nil || cfg.Network == nil || cfg.CapabilityKey == nil || (cfg.DialGuest == nil) == !cfg.UnixGuestEndpoint {
		return nil, errors.New("go-microvm runtime is not fully configured")
	}
	runtime := &GoMicroVMRuntime{
		backend: cfg.Backend, network: cfg.Network, guestEgress: cfg.GuestEgress,
		dialGuest: cfg.DialGuest, unixEndpoint: cfg.UnixGuestEndpoint, capabilityKey: cfg.CapabilityKey, observer: cfg.Observer,
		generations: make(map[string]*runtimeGeneration), listeners: make(map[string]*net.UnixListener),
	}
	if cfg.UnixGuestEndpoint {
		runtime.dialGuest = runtime.acceptGuest
	}
	return runtime, nil
}

// Create implements VMRuntime.Create.
func (r *GoMicroVMRuntime) Create(ctx context.Context, request VMCreateRequest) error {
	if err := validateConcreteCreate(request); err != nil {
		return err
	}
	if r.unixEndpoint {
		if err := r.listenGuest(request.Endpoint); err != nil {
			return err
		}
		defer func() {
			if _, err := r.generation(EnvironmentRef{Kind: Kind, ID: request.Preboot.Config.Binding.Ref}); err != nil {
				r.closeGuestListener(request.Endpoint)
			}
		}()
	}
	plan, err := virtiofs.Plan(request.Prepared, nil)
	if err != nil {
		return fmt.Errorf("plan go-microvm virtio-fs: %w", err)
	}
	if err := plan.PrepareWorkloadAccess(); err != nil {
		return fmt.Errorf("prepare go-microvm workload access: %w", err)
	}
	mounts := plan.GoMicroVMMounts()
	hostReadOnly := false
	for _, mount := range mounts {
		hostReadOnly = hostReadOnly || mount.ReadOnly
	}
	if !hostReadOnly {
		return errors.New("go-microvm launch has no host-enforced read-only mount")
	}
	networkDir := request.Endpoint + ".network"
	if err := os.Mkdir(networkDir, 0o700); err != nil {
		return fmt.Errorf("create microvm network directory: %w", err)
	}
	// The provider belongs to the environment generation, not the create RPC.
	// Stop/Remove own its cancellation after the request context is gone.
	network, err := r.network.start(context.WithoutCancel(ctx), r.guestEgress, networkDir)
	if err != nil {
		_ = os.RemoveAll(networkDir)
		return err
	}
	key, err := r.capabilityKey()
	if err != nil {
		network.Provider.Stop()
		_ = os.RemoveAll(networkDir)
		return fmt.Errorf("generate microvm guest capability key: %w", err)
	}
	issuer, err := control.NewCapabilityIssuer(key)
	if err != nil {
		network.Provider.Stop()
		_ = os.RemoveAll(networkDir)
		return err
	}
	preboot := request.Preboot
	preboot.Config.DisableIPv6 = r.guestEgress.tightened()
	launch := GoMicroVMLaunch{
		EnvironmentID: request.EnvironmentID, VMID: request.VMID, Endpoint: request.Endpoint,
		Generation: request.Generation, Prepared: request.Prepared,
		RuntimePath: request.Verified.Runtime.Path, FirmwarePath: request.Verified.Firmware.Path,
		ImagePath: request.Verified.ExecutionImage.Path, NetworkSocket: network.SocketPath,
		Network: network.Provider, VsockPort: control.GuestControlPort, Mounts: mounts,
		Preboot: preboot, CapabilityKey: append([]byte(nil), key...),
		CPUs: positiveUint32(request.Resources.CPU), MemoryMiB: bytesToMiB(request.Resources.RAMBytes),
		Verified: true, HostReadOnly: true, DisableIPv6: r.guestEgress.tightened(),
	}
	instance, err := r.backend.Start(ctx, launch)
	if err != nil {
		network.Provider.Stop()
		_ = os.RemoveAll(networkDir)
		return fmt.Errorf("start verified go-microvm instance: %w", err)
	}
	binding := request.Preboot.Config.Binding
	r.mu.Lock()
	if _, exists := r.generations[binding.Ref]; exists {
		r.mu.Unlock()
		_ = instance.Stop(context.WithoutCancel(ctx))
		_ = instance.Remove(context.WithoutCancel(ctx))
		network.Provider.Stop()
		_ = os.RemoveAll(networkDir)
		return ErrEnvironmentStale
	}
	r.generations[binding.Ref] = &runtimeGeneration{instance: instance, network: network.Provider, networkDir: networkDir, issuer: issuer, binding: binding, endpoint: request.Endpoint}
	r.mu.Unlock()
	if source, ok := network.Provider.(egressDenialSource); ok && r.observer != nil {
		r.observer.trackEgressDenials(binding.Ref, source)
	}
	return nil
}

func validateConcreteCreate(request VMCreateRequest) error {
	if request.Prepared == nil || request.Endpoint == "" || !filepath.IsAbs(request.Endpoint) || request.Preboot.Config.Binding.Ref == "" {
		return errors.New("go-microvm create request is incomplete")
	}
	for _, artifact := range request.Verified.All() {
		if artifact.Path == "" || !filepath.IsAbs(artifact.Path) {
			return fmt.Errorf("verified %s artifact has no absolute execution path", artifact.Kind)
		}
		if _, err := os.Stat(artifact.Path); err != nil {
			return fmt.Errorf("open verified %s artifact: %w", artifact.Kind, err)
		}
	}
	return nil
}

func positiveUint32(value int64) uint32 {
	if value <= 0 {
		return 0
	}
	if value > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(value)
}

func bytesToMiB(bytes int64) uint32 {
	const mib = int64(1 << 20)
	if bytes <= 0 {
		return 0
	}
	value := (bytes + mib - 1) / mib
	if value > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(value)
}

// WaitReady waits for the exact process-local generation and its guest vsock
// endpoint. A live runner alone is not ready for protocol negotiation.
func (r *GoMicroVMRuntime) WaitReady(ctx context.Context, record EnvironmentRecord) error {
	generation, err := r.generation(record.Ref)
	if err != nil {
		return err
	}
	if !r.unixEndpoint {
		return generation.instance.WaitReady(ctx)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := generation.instance.WaitReady(ctx); err != nil {
			return err
		}
		if info, statErr := os.Lstat(generation.endpoint); statErr == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect microvm guest endpoint: %w", statErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Inspect returns the generation-fenced runtime identity.
func (r *GoMicroVMRuntime) Inspect(ctx context.Context, record EnvironmentRecord) (RuntimeStatus, error) {
	generation, err := r.ensureGeneration(ctx, record)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return generation.instance.Status(ctx)
}

// Negotiate opens the one authenticated multiplexed guest stream.
func (r *GoMicroVMRuntime) Negotiate(ctx context.Context, endpoint string, binding control.Binding) (control.Agreement, error) {
	generation, err := r.generation(EnvironmentRef{Kind: Kind, ID: binding.Ref})
	if err != nil || generation.binding != binding || endpoint != generation.endpoint {
		return control.Agreement{}, control.ErrBindingMismatch
	}
	capability, err := generation.issuer.Issue(binding)
	if err != nil {
		return control.Agreement{}, err
	}
	stream, err := r.dialGuest(ctx, endpoint)
	if err != nil {
		return control.Agreement{}, fmt.Errorf("dial microvm guest vsock endpoint: %w", err)
	}
	services, err := guestagent.Connect(ctx, stream, binding, capability)
	if err != nil {
		_ = stream.Close()
		return control.Agreement{}, err
	}
	r.mu.Lock()
	generation.services = services
	r.mu.Unlock()
	return services.Agreement(), nil
}

// Services returns the already-negotiated complete guest Workspace/exec pair.
func (r *GoMicroVMRuntime) Services(ref EnvironmentRef) (*guestagent.Services, error) {
	generation, err := r.generation(ref)
	if err != nil {
		return nil, err
	}
	if generation.services == nil {
		return nil, ErrEnvironmentUnavailable
	}
	return generation.services, nil
}

// Reattach resolves the exact generation through the backend without creating it.
func (r *GoMicroVMRuntime) Reattach(ctx context.Context, record EnvironmentRecord) error {
	generation, err := r.ensureGeneration(ctx, record)
	if err != nil {
		return err
	}
	if generation.services != nil {
		return nil
	}
	if generation.issuer == nil || generation.endpoint != record.Endpoint {
		return ErrEnvironmentUnavailable
	}
	_, err = r.Negotiate(ctx, record.Endpoint, bindingForRecord(record))
	return err
}

// Detach drops only the process-local data-plane handle.
func (r *GoMicroVMRuntime) Detach(_ context.Context, record EnvironmentRecord) error {
	generation, err := r.generation(record.Ref)
	if err != nil {
		return err
	}
	if generation.services != nil {
		err = generation.services.Close()
		generation.services = nil
	}
	return err
}

// Destroy stops and removes the exact generation and its explicit network provider.
func (r *GoMicroVMRuntime) Destroy(ctx context.Context, record EnvironmentRecord) error {
	generation, err := r.ensureGeneration(ctx, record)
	if err != nil {
		if errors.Is(err, ErrEnvironmentUnavailable) {
			return nil
		}
		return err
	}
	if generation.services != nil {
		_ = generation.services.Close()
	}
	err = errors.Join(generation.instance.Stop(ctx), generation.instance.Remove(ctx))
	if generation.network != nil {
		if r.observer != nil {
			r.observer.untrackEgressDenials(record.Ref.ID)
		}
		generation.network.Stop()
	}
	if generation.networkDir != "" {
		err = errors.Join(err, os.RemoveAll(generation.networkDir))
	}
	r.closeGuestListener(record.Endpoint)
	r.mu.Lock()
	delete(r.generations, record.Ref.ID)
	r.mu.Unlock()
	return err
}

func (r *GoMicroVMRuntime) generation(ref EnvironmentRef) (*runtimeGeneration, error) {
	if ref.Kind != Kind || ref.ID == "" {
		return nil, ErrInvalidEnvironmentRef
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	generation := r.generations[ref.ID]
	if generation == nil {
		return nil, ErrEnvironmentUnavailable
	}
	return generation, nil
}

func (r *GoMicroVMRuntime) ensureGeneration(ctx context.Context, record EnvironmentRecord) (*runtimeGeneration, error) {
	generation, err := r.generation(record.Ref)
	if err == nil {
		if record.ProcessIdentity != "" || record.RunnerPID != 0 {
			status, statusErr := generation.instance.Status(ctx)
			if statusErr != nil {
				return nil, statusErr
			}
			if identityErr := validateRuntimeIdentity(record, status); identityErr != nil {
				return nil, identityErr
			}
		}
		return generation, nil
	}
	instance, openErr := r.backend.Open(ctx, record)
	if openErr != nil {
		return nil, errors.Join(ErrEnvironmentUnavailable, openErr)
	}
	status, statusErr := instance.Status(ctx)
	if statusErr != nil {
		return nil, errors.Join(ErrEnvironmentUnavailable, statusErr)
	}
	if identityErr := validateRuntimeIdentity(record, status); identityErr != nil {
		return nil, identityErr
	}
	generation = &runtimeGeneration{instance: instance, binding: bindingForRecord(record), endpoint: record.Endpoint}
	r.mu.Lock()
	r.generations[record.Ref.ID] = generation
	r.mu.Unlock()
	return generation, nil
}

// LibkrunBackend is the production go-microvm backend. Runtime and firmware
// artifacts are supplied as explicit environment paths consumed by the pinned
// runner; the execution image is an already-verified rootfs.
type LibkrunBackend struct {
	mu               sync.Mutex
	instances        map[string]*libkrunInstance
	ownedArtifactDir string
	userNamespaceUID int
	userNamespaceGID int
}

// NewLibkrunBackend constructs the production backend. ownedArtifactDir is an
// executable daemon-owned filesystem used to retain generation runtime libraries.
func NewLibkrunBackend(ownedArtifactDir ...string) *LibkrunBackend {
	workload := guestexec.DefaultWorkloadIdentity()
	backend := &LibkrunBackend{
		instances:        make(map[string]*libkrunInstance),
		userNamespaceUID: int(workload.UID), userNamespaceGID: int(workload.GID),
	}
	if len(ownedArtifactDir) > 0 {
		backend.ownedArtifactDir = ownedArtifactDir[0]
	}
	return backend
}

// Start launches go-microvm with explicit rootfs, network, vsock, and v0.0.40
// host-enforced ReadOnly virtio-fs options.
func (b *LibkrunBackend) Start(ctx context.Context, launch GoMicroVMLaunch) (GoMicroVMInstance, error) {
	if !launch.Verified || launch.Network == nil || !launch.HostReadOnly {
		return nil, errors.New("refusing incomplete go-microvm launch")
	}
	endpointParent := filepath.Dir(launch.Endpoint)
	ownedArtifactParent := b.ownedArtifactDir
	if ownedArtifactParent == "" {
		ownedArtifactParent = endpointParent
	}
	runtimePath, firmwarePath, ownedArtifactsRoot, err := prepareOwnedRuntimeArtifacts(
		launch.RuntimePath, launch.FirmwarePath, ownedArtifactParent, launch.EnvironmentID,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare generation-owned microvm artifacts: %w", err)
	}
	rootfsPath := filepath.Join(endpointParent, "rootfs-"+launch.EnvironmentID)
	if launch.RepositoryRootFS {
		rootfsPath = launch.ImagePath
		if err := writeRepositoryGuestBootConfig(rootfsPath, launch); err != nil {
			_ = os.RemoveAll(ownedArtifactsRoot)
			return nil, fmt.Errorf("prepare repository guest boot config: %w", err)
		}
	} else if err := prepareExecutionRootFS(launch.ImagePath, rootfsPath, launch.Preboot, launch.CapabilityKey); err != nil {
		_ = os.RemoveAll(ownedArtifactsRoot)
		return nil, fmt.Errorf("prepare private microvm execution rootfs: %w", err)
	}
	backend := gomicrovmlibkrun.NewBackend(
		gomicrovmlibkrun.WithRuntime(extract.Dir(runtimePath)),
		gomicrovmlibkrun.WithFirmware(extract.Dir(firmwarePath)),
		gomicrovmlibkrun.WithCacheDir(filepath.Join(endpointParent, "runtime-cache")),
		// Linux maps the fixed guest workload IDs to the daemon's real IDs. The
		// runner gains only namespace-local SETUID/SETGID, which lets virtio-fs
		// create files as workload 65532 without host CAP_CHOWN or sudo.
		gomicrovmlibkrun.WithUserNamespaceUID(uint32(b.userNamespaceUID), uint32(b.userNamespaceGID)), // #nosec G115 -- supported OS user IDs are uint32 values.
	)
	opts := []gomicrovm.Option{
		gomicrovm.WithName(launch.VMID), gomicrovm.WithRootFSPath(rootfsPath),
		gomicrovm.WithDataDir(filepath.Join(endpointParent, "data-"+launch.EnvironmentID)),
		gomicrovm.WithBackend(backend),
		gomicrovm.WithInitOverride("/usr/local/bin/mecatl-guest-agent"),
		gomicrovm.WithNetProvider(alreadyStartedProvider{Provider: launch.Network}),
		gomicrovm.WithVirtioFS(launch.Mounts...), gomicrovm.WithVsock(launch.VsockPort, launch.Endpoint),
		gomicrovm.WithoutSSH(),
	}
	if launch.CPUs > 0 {
		opts = append(opts, gomicrovm.WithCPUs(launch.CPUs))
	}
	if launch.MemoryMiB > 0 {
		opts = append(opts, gomicrovm.WithMemory(launch.MemoryMiB))
	}
	vm, err := gomicrovm.Run(ctx, "", opts...)
	if err != nil {
		if !launch.RepositoryRootFS {
			_ = os.RemoveAll(rootfsPath)
		}
		_ = os.RemoveAll(ownedArtifactsRoot)
		return nil, err
	}
	instance := &libkrunInstance{vm: vm, launch: launch, rootfsPath: rootfsPath, ownedArtifactsRoot: ownedArtifactsRoot}
	b.mu.Lock()
	b.instances[launch.EnvironmentID] = instance
	b.mu.Unlock()
	return instance, nil
}

func prepareOwnedRuntimeArtifacts(runtimeSource, firmwareSource, parent, environmentID string) (runtimePath, firmwarePath, ownedRoot string, retErr error) {
	ownedRoot = filepath.Join(parent, "artifacts-"+environmentID)
	if err := os.Mkdir(ownedRoot, 0o700); err != nil {
		return "", "", "", err
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(ownedRoot)
		}
	}()
	runtimePath = filepath.Join(ownedRoot, "runtime")
	if err := cloneRootFS(runtimeSource, runtimePath); err != nil {
		return "", "", "", err
	}
	firmwarePath = filepath.Join(ownedRoot, "firmware")
	if err := cloneRootFS(firmwareSource, firmwarePath); err != nil {
		return "", "", "", err
	}
	return runtimePath, firmwarePath, ownedRoot, nil
}

// Open resolves a process-local handle or reconstructs a destruction-only handle
// after daemon restart. It never provisions a replacement.
func (b *LibkrunBackend) Open(ctx context.Context, record EnvironmentRecord) (GoMicroVMInstance, error) {
	b.mu.Lock()
	instance := b.instances[record.EnvironmentID]
	b.mu.Unlock()
	if instance != nil {
		if instance.launch.Generation != record.Generation || instance.launch.VMID != record.VMID {
			return nil, ErrEnvironmentUnavailable
		}
		return instance, nil
	}
	if record.RunnerPID <= 0 || record.ProcessIdentity == "" || record.Endpoint == "" || record.EnvironmentID == "" || record.VMID == "" {
		return nil, ErrEnvironmentUnavailable
	}
	identity, err := ProcessStartIdentity(ctx, record.RunnerPID)
	if err != nil {
		return nil, errors.Join(ErrEnvironmentUnavailable, err)
	}
	if identity != record.ProcessIdentity {
		return nil, ErrRuntimeIdentityMismatch
	}
	endpoint, err := os.Lstat(record.Endpoint)
	if err != nil || endpoint.Mode()&os.ModeSocket == 0 || endpoint.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(ErrRuntimeIdentityMismatch, err)
	}
	ownedArtifactParent := b.ownedArtifactDir
	if ownedArtifactParent == "" {
		ownedArtifactParent = filepath.Dir(record.Endpoint)
	}
	return &orphanLibkrunInstance{
		record: record, rootfsPath: filepath.Join(filepath.Dir(record.Endpoint), "rootfs-"+record.EnvironmentID),
		dataPath: filepath.Join(filepath.Dir(record.Endpoint), "data-"+record.EnvironmentID), networkPath: record.Endpoint + ".network",
		ownedArtifactsRoot: filepath.Join(ownedArtifactParent, "artifacts-"+record.EnvironmentID),
	}, nil
}

type alreadyStartedProvider struct{ gomicrovmnet.Provider }

func (alreadyStartedProvider) Start(context.Context, gomicrovmnet.Config) error { return nil }

type repositoryGuestBootConfig struct {
	Owner         string `json:"Owner"`
	RepositoryKey string `json:"RepositoryKey"`
	VMID          string `json:"VMID"`
	Endpoint      string `json:"Endpoint"`
	Generation    uint32 `json:"Generation"`
}

type guestBootConfig struct {
	GuestPrebootConfig
	CapabilityKey string                     `json:"capability_key"`
	Repository    *repositoryGuestBootConfig `json:"repository,omitempty"`
}

func writeRepositoryGuestBootConfig(rootfs string, launch GoMicroVMLaunch) error {
	if launch.RepositoryOwner == "" || launch.RepositoryKey == "" || launch.VMID == "" || launch.Generation == 0 || len(launch.CapabilityKey) < 32 {
		return errors.New("repository guest boot config is incomplete")
	}
	if err := secureRootFSMkdir(rootfs, filepath.Join("etc", "mecatl")); err != nil {
		return err
	}
	payload, err := json.Marshal(guestBootConfig{
		GuestPrebootConfig: GuestPrebootConfig{DisableIPv6: launch.DisableIPv6, AgentEndpoint: launch.Endpoint, Capabilities: control.RequiredCapabilities(), MaxMessageBytes: control.DefaultMaxMessageBytes},
		CapabilityKey:      base64.RawStdEncoding.EncodeToString(launch.CapabilityKey),
		Repository:         &repositoryGuestBootConfig{Owner: launch.RepositoryOwner, RepositoryKey: launch.RepositoryKey, VMID: launch.VMID, Endpoint: launch.Endpoint, Generation: launch.Generation},
	})
	if err != nil {
		return err
	}
	target := filepath.Join(rootfs, strings.TrimPrefix(GuestPrebootConfigPath, "/"))
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- target is confined beneath the validated private rootfs.
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(payload, '\n'))
	return errors.Join(writeErr, file.Close())
}

func prepareExecutionRootFS(source, destination string, preboot GuestPrebootArtifact, key []byte) (retErr error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || source == destination {
		return errors.New("execution rootfs paths must be distinct absolute paths")
	}
	if err := cloneRootFS(source, destination); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	return prebootRootFSHook(preboot, key)(destination, nil)
}

func cloneRootFS(source, destination string) error {
	return copyTree(source, destination)
}

func secureRootFSMkdir(root, relative string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("private execution rootfs is not a directory")
	}
	current := root
	for _, component := range []string{"etc", "mecatl"} {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe execution rootfs parent %s", relative)
		}
	}
	return os.Chmod(current, 0o700) // #nosec G302 -- directory is deliberately owner-only; secret file remains 0600.
}

func prebootRootFSHook(preboot GuestPrebootArtifact, key []byte) gomicrovm.RootFSHook {
	return func(rootfsPath string, _ *gomicrovmimage.OCIConfig) error {
		if preboot.RootfsPath != GuestPrebootConfigPath {
			return errors.New("invalid guest preboot config path")
		}
		target := filepath.Join(rootfsPath, "etc", "mecatl", "guest-agent.json")
		if err := secureRootFSMkdir(rootfsPath, filepath.Join("etc", "mecatl")); err != nil {
			return err
		}
		payload, err := json.Marshal(guestBootConfig{GuestPrebootConfig: preboot.Config, CapabilityKey: base64.RawStdEncoding.EncodeToString(key)})
		if err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(append(payload, '\n'))
		return errors.Join(writeErr, file.Close())
	}
}

type libkrunInstance struct {
	vm                 *gomicrovm.VM
	launch             GoMicroVMLaunch
	rootfsPath         string
	ownedArtifactsRoot string
}

func (i *libkrunInstance) WaitReady(ctx context.Context) error {
	status, err := i.vm.Status(ctx)
	if err != nil {
		return err
	}
	if !status.Active {
		return ErrEnvironmentUnavailable
	}
	return nil
}
func (i *libkrunInstance) Status(ctx context.Context) (RuntimeStatus, error) {
	status, err := i.vm.Status(ctx)
	if err != nil {
		return RuntimeStatus{}, err
	}
	identity, err := ProcessStartIdentity(ctx, i.vm.RunnerPID())
	if err != nil {
		return RuntimeStatus{}, err
	}
	return RuntimeStatus{Live: status.Active, Generation: i.launch.Generation, VMID: i.launch.VMID, PID: i.vm.RunnerPID(), ProcessIdentity: identity, Endpoint: i.launch.Endpoint}, nil
}
func (i *libkrunInstance) Stop(ctx context.Context) error { return i.vm.Stop(ctx) }
func (i *libkrunInstance) Remove(ctx context.Context) error {
	var rootfsErr error
	if !i.launch.RepositoryRootFS {
		rootfsErr = os.RemoveAll(i.rootfsPath)
	}
	return errors.Join(i.vm.Remove(ctx), rootfsErr, os.RemoveAll(i.ownedArtifactsRoot))
}

type orphanLibkrunInstance struct {
	record             EnvironmentRecord
	rootfsPath         string
	dataPath           string
	networkPath        string
	ownedArtifactsRoot string
}

func (*orphanLibkrunInstance) WaitReady(context.Context) error {
	return ErrEnvironmentUnavailable
}

func (i *orphanLibkrunInstance) Status(ctx context.Context) (RuntimeStatus, error) {
	identity, err := ProcessStartIdentity(ctx, i.record.RunnerPID)
	if err != nil {
		return RuntimeStatus{}, ErrEnvironmentUnavailable
	}
	if identity != i.record.ProcessIdentity {
		return RuntimeStatus{}, ErrRuntimeIdentityMismatch
	}
	return RuntimeStatus{Live: true, Generation: i.record.Generation, VMID: i.record.VMID, PID: i.record.RunnerPID, ProcessIdentity: identity, Endpoint: i.record.Endpoint}, nil
}

func (i *orphanLibkrunInstance) Stop(ctx context.Context) error {
	identity, err := ProcessStartIdentity(ctx, i.record.RunnerPID)
	if err != nil {
		return nil // already gone
	}
	if identity != i.record.ProcessIdentity {
		return ErrRuntimeIdentityMismatch
	}
	process, err := os.FindProcess(i.record.RunnerPID)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		identity, err = ProcessStartIdentity(ctx, i.record.RunnerPID)
		if err != nil || identity != i.record.ProcessIdentity {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
	}
}

func (i *orphanLibkrunInstance) Remove(context.Context) error {
	return errors.Join(
		removeIfExists(i.record.Endpoint),
		os.RemoveAll(i.networkPath),
		os.RemoveAll(i.dataPath),
		os.RemoveAll(i.rootfsPath),
		os.RemoveAll(i.ownedArtifactsRoot),
	)
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ProcessStartIdentity returns the platform process-start token used by runtime,
// reconciliation, and doctor checks to distinguish a live generation from PID reuse.
func ProcessStartIdentity(ctx context.Context, pid int) (string, error) {
	return runnerProcessIdentity(ctx, pid)
}

func runnerProcessIdentity(ctx context.Context, pid int) (string, error) {
	if pid <= 0 {
		return "", ErrEnvironmentUnavailable
	}
	start, err := platformProcessStartIdentity(ctx, pid)
	if err != nil || start == "" {
		return "", ErrEnvironmentUnavailable
	}
	return fmt.Sprintf("process-start:%d:%s", pid, start), nil
}

func (r *GoMicroVMRuntime) listenGuest(endpoint string) error {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen for microvm guest vsock: %w", err)
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return err
	}
	r.mu.Lock()
	r.listeners[endpoint] = listener
	r.mu.Unlock()
	return nil
}

func (r *GoMicroVMRuntime) acceptGuest(ctx context.Context, endpoint string) (io.ReadWriteCloser, error) {
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

func (r *GoMicroVMRuntime) closeGuestListener(endpoint string) {
	r.mu.Lock()
	listener := r.listeners[endpoint]
	delete(r.listeners, endpoint)
	r.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	_ = os.Remove(endpoint)
}

// UnixGuestDialer opens a pre-existing host endpoint. Production runtimes use
// GoMicroVMRuntimeConfig.UnixGuestEndpoint so the listener exists before boot;
// this helper remains available to clients attaching to an external endpoint.
func UnixGuestDialer(ctx context.Context, endpoint string) (io.ReadWriteCloser, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", endpoint)
}
