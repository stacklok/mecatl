package microvm

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	gomicrovm "github.com/stacklok/go-microvm"
	"github.com/stacklok/go-microvm/extract"
	gomicrovmlibkrun "github.com/stacklok/go-microvm/hypervisor/libkrun"
	gomicrovmnet "github.com/stacklok/go-microvm/net"
	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

// GuestDialer opens the one vsock-backed host endpoint for a guest generation.
type GuestDialer func(context.Context, string) (io.ReadWriteCloser, error)

// GoMicroVMLaunch is the fully resolved, verified repository launch description
// handed to the hypervisor backend.
type GoMicroVMLaunch struct {
	EnvironmentID       string
	VMID                string
	Endpoint            string
	Generation          uint32
	PlacementGeneration uint32
	RepositoryOwner     string
	RepositoryKey       string
	RuntimePath         string
	FirmwarePath        string
	ImagePath           string
	NetworkSocket       string
	Network             gomicrovmnet.Provider
	VsockPort           uint32
	Mounts              []gomicrovm.VirtioFSMount
	CapabilityKey       []byte
	Verified            bool
	HostReadOnly        bool
	DisableIPv6         bool
}

// GoMicroVMInstance is the concrete repository runtime handle retained by microvmd.
type GoMicroVMInstance interface {
	WaitReady(context.Context) error
	Status(context.Context) (RuntimeStatus, error)
	Stop(context.Context) error
	CleanupBoot(context.Context) error
}

// GoMicroVMBackend is the repository hypervisor seam.
type GoMicroVMBackend interface {
	Start(context.Context, GoMicroVMLaunch) (GoMicroVMInstance, error)
}

// LibkrunBackend is the production repository go-microvm backend.
type LibkrunBackend struct {
	ownedArtifactDir string
	userNamespaceUID int
	userNamespaceGID int
	launchOwnership  *LaunchOwnership
}

// NewLibkrunBackend constructs the production backend. ownedArtifactDir is an
// executable daemon-owned filesystem used to retain runtime libraries for the VM lifetime.
// Repository backends require durable launch ownership so every provisioned VM
// can be reconciled after the daemon restarts.
func NewLibkrunBackend(ownedArtifactDir string, ownership *LaunchOwnership) (*LibkrunBackend, error) {
	if ownership == nil {
		return nil, ErrLaunchOwnershipUnsupported
	}
	workload := guestexec.DefaultWorkloadIdentity()
	return &LibkrunBackend{
		ownedArtifactDir: ownedArtifactDir,
		userNamespaceUID: int(workload.UID),
		userNamespaceGID: int(workload.GID),
		launchOwnership:  ownership,
	}, nil
}

// Reconcile delegates stable repository launch reconciliation to the Linux owner.
func (b *LibkrunBackend) Reconcile(ctx context.Context, environmentID string) (LaunchReconcileResult, error) {
	if b == nil || b.launchOwnership == nil {
		return LaunchReconcileResult{}, ErrLaunchOwnershipUnsupported
	}
	return b.launchOwnership.Reconcile(ctx, environmentID)
}

// Start launches the repository VM with explicit rootfs, network, vsock, and
// host-enforced read-only Git object mounts.
func (b *LibkrunBackend) Start(ctx context.Context, launch GoMicroVMLaunch) (GoMicroVMInstance, error) {
	if b == nil || b.launchOwnership == nil {
		return nil, ErrLaunchOwnershipUnsupported
	}
	if !launch.Verified || launch.Network == nil || !launch.HostReadOnly || launch.ImagePath == "" {
		return nil, errors.New("refusing incomplete repository go-microvm launch")
	}
	endpointParent := filepath.Dir(launch.Endpoint)
	ownedArtifactParent := b.ownedArtifactDir
	if ownedArtifactParent == "" {
		ownedArtifactParent = endpointParent
	}
	runtimePath, firmwarePath, ownedArtifactsRoot, err := prepareOwnedRuntimeArtifacts(
		launch.RuntimePath, launch.FirmwarePath, ownedArtifactParent, launch.VMID,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare repository-owned microvm artifacts: %w", err)
	}
	if err := writeRepositoryGuestBootConfig(launch.ImagePath, launch); err != nil {
		_ = os.RemoveAll(ownedArtifactsRoot)
		return nil, fmt.Errorf("prepare repository guest boot config: %w", err)
	}
	backendOptions := []gomicrovmlibkrun.Option{
		gomicrovmlibkrun.WithRuntime(extract.Dir(runtimePath)),
		gomicrovmlibkrun.WithFirmware(extract.Dir(firmwarePath)),
		gomicrovmlibkrun.WithCacheDir(filepath.Join(endpointParent, "runtime-cache")),
		gomicrovmlibkrun.WithUserNamespaceUID(uint32(b.userNamespaceUID), uint32(b.userNamespaceGID)), // #nosec G115 -- supported OS user IDs are uint32 values.
		gomicrovmlibkrun.WithSpawner(b.launchOwnership.Spawner(launch.EnvironmentID)),
	}
	backend := gomicrovmlibkrun.NewBackend(backendOptions...)
	dataDir := filepath.Join(endpointParent, "data-"+launch.VMID)
	vm, err := gomicrovm.Run(ctx, "",
		gomicrovm.WithName(launch.VMID),
		gomicrovm.WithRootFSPath(launch.ImagePath),
		gomicrovm.WithDataDir(dataDir),
		gomicrovm.WithBackend(backend),
		gomicrovm.WithInitOverride("/usr/local/bin/mecatl-guest-agent"),
		gomicrovm.WithNetProvider(alreadyStartedProvider{Provider: launch.Network}),
		gomicrovm.WithVirtioFS(launch.Mounts...),
		gomicrovm.WithVsock(launch.VsockPort, launch.Endpoint),
		gomicrovm.WithoutSSH(),
	)
	if err != nil {
		_ = os.RemoveAll(dataDir)
		_ = os.RemoveAll(ownedArtifactsRoot)
		return nil, err
	}
	return &libkrunInstance{vm: vm, launch: launch, dataDir: dataDir, ownedArtifactsRoot: ownedArtifactsRoot}, nil
}

func cloneRootFS(source, destination string) error { return copyTree(source, destination) }

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
	if err := copyTree(runtimeSource, runtimePath); err != nil {
		return "", "", "", err
	}
	firmwarePath = filepath.Join(ownedRoot, "firmware")
	if err := copyTree(firmwareSource, firmwarePath); err != nil {
		return "", "", "", err
	}
	return runtimePath, firmwarePath, ownedRoot, nil
}

type alreadyStartedProvider struct{ gomicrovmnet.Provider }

func (alreadyStartedProvider) Start(context.Context, gomicrovmnet.Config) error { return nil }

type repositoryGuestBootConfig struct {
	Owner               string `json:"Owner"`
	RepositoryKey       string `json:"RepositoryKey"`
	VMID                string `json:"VMID"`
	Endpoint            string `json:"Endpoint"`
	Generation          uint32 `json:"Generation"`
	PlacementGeneration uint32 `json:"PlacementGeneration"`
}

type guestBootConfig struct {
	GuestPrebootConfig
	CapabilityKey string                     `json:"capability_key"`
	Repository    *repositoryGuestBootConfig `json:"repository,omitempty"`
}

func writeRepositoryGuestBootConfig(rootfs string, launch GoMicroVMLaunch) error {
	if launch.RepositoryOwner == "" || launch.RepositoryKey == "" || launch.VMID == "" || launch.Generation == 0 || launch.PlacementGeneration == 0 || len(launch.CapabilityKey) < 32 {
		return errors.New("repository guest boot config is incomplete")
	}
	if err := secureRootFSMkdir(rootfs); err != nil {
		return err
	}
	payload, err := json.Marshal(guestBootConfig{
		GuestPrebootConfig: GuestPrebootConfig{DisableIPv6: launch.DisableIPv6, AgentEndpoint: launch.Endpoint, Capabilities: control.RequiredCapabilities(), MaxMessageBytes: control.DefaultMaxMessageBytes},
		CapabilityKey:      base64.RawStdEncoding.EncodeToString(launch.CapabilityKey),
		Repository:         &repositoryGuestBootConfig{Owner: launch.RepositoryOwner, RepositoryKey: launch.RepositoryKey, VMID: launch.VMID, Endpoint: launch.Endpoint, Generation: launch.Generation, PlacementGeneration: launch.PlacementGeneration},
	})
	if err != nil {
		return err
	}
	return replaceRepositoryGuestBootConfig(rootfs, append(payload, '\n'))
}

func replaceRepositoryGuestBootConfig(rootfs string, payload []byte) error {
	rootFD, err := unix.Open(rootfs, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(rootFD) }()
	etcFD, err := openatOpaque(rootFD, "etc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(etcFD) }()
	configFD, err := openatOpaque(etcFD, "mecatl", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(configFD) }()
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return err
	}
	name := ".preboot-" + hex.EncodeToString(value[:])
	fd, err := openatOpaque(configFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = unix.Unlinkat(configFD, name, 0) }()
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	target := filepath.Base(GuestPrebootConfigPath)
	if err := unix.Renameat(configFD, name, configFD, target); err != nil {
		return err
	}
	return unix.Fsync(configFD)
}

func secureRootFSMkdir(root string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("private repository rootfs is not a directory")
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
			return errors.New("unsafe repository rootfs config parent")
		}
	}
	return os.Chmod(current, 0o700) // #nosec G302 -- directory is deliberately owner-only; secret file remains 0600.
}

type libkrunInstance struct {
	vm                 *gomicrovm.VM
	launch             GoMicroVMLaunch
	dataDir            string
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
func (i *libkrunInstance) CleanupBoot(context.Context) error {
	return errors.Join(os.RemoveAll(i.dataDir), os.RemoveAll(i.ownedArtifactsRoot))
}

// ProcessStartIdentity returns the platform process-start token used to distinguish PID reuse.
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

// UnixGuestDialer opens a pre-existing host endpoint.
func UnixGuestDialer(ctx context.Context, endpoint string) (io.ReadWriteCloser, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", endpoint)
}
