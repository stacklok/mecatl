// Command mecatl-microvmd runs the local authenticated microVM lifecycle daemon.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/stacklok/go-microvm/extract"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/environment/microvm"
	"github.com/stacklok/mecatl/environment/microvm/control"
)

type daemonConfig struct {
	ReleaseIdentity     string                          `json:"release_identity"`
	BinaryIdentity      string                          `json:"binary_identity"`
	PolicyRevision      string                          `json:"policy_revision"`
	ArtifactCache       string                          `json:"artifact_cache,omitempty"`
	RuntimeDir          string                          `json:"runtime_dir,omitempty"`
	CertificateIdentity string                          `json:"certificate_identity,omitempty"`
	OIDCIssuer          string                          `json:"oidc_issuer,omitempty"`
	PublicKey           string                          `json:"public_key,omitempty"`
	PublicKeyIdentity   string                          `json:"public_key_identity,omitempty"`
	Attestations        map[microvm.ArtifactKind]string `json:"required_attestations"`
	Artifacts           []artifactConfig                `json:"artifacts"`
	GuestEgress         microvm.GuestEgressPolicy       `json:"guest_egress"`
	Profiles            map[string]profileConfig        `json:"profiles"`
	loadedConfigDigest  string
}

type profileConfig struct{}

var daemonAfterOwnership func()

type artifactConfig struct {
	Kind               microvm.ArtifactKind `json:"kind"`
	Reference          string               `json:"reference"`
	Digest             string               `json:"digest"`
	ManifestDigest     string               `json:"manifest_digest,omitempty"`
	DiscoveryReference string               `json:"discovery_reference,omitempty"`
	ResolutionEvidence string               `json:"resolution_evidence,omitempty"`
	Platform           string               `json:"platform,omitempty"`
	Path               string               `json:"path,omitempty"`
	Provenance         string               `json:"provenance"`
	SigstoreBundle     string               `json:"sigstore_bundle"`
}

func artifactRequest(config artifactConfig) microvm.ArtifactRequest {
	return microvm.ArtifactRequest{
		Kind: config.Kind, Reference: config.Reference, Digest: config.Digest, ManifestDigest: config.ManifestDigest,
		DiscoveryReference: config.DiscoveryReference, ResolutionEvidence: config.ResolutionEvidence, Platform: config.Platform,
	}
}

type configuredResolver struct {
	local       map[string]microvm.ResolvedArtifact
	ociEvidence map[string]microvm.VerificationEvidence
	oci         *microvm.OCIExecutionImageResolver
}

func (r *configuredResolver) Resolve(ctx context.Context, request microvm.ArtifactRequest) (microvm.ResolvedArtifact, error) {
	if request.Kind == microvm.ArtifactExecutionImage && request.ManifestDigest != "" {
		if r.oci == nil {
			return microvm.ResolvedArtifact{}, errors.New("OCI execution-image cache is not configured")
		}
		return r.oci.Resolve(ctx, request)
	}
	artifact, ok := r.local[request.Reference]
	if !ok || artifact.Kind != request.Kind || artifact.Digest != request.Digest {
		return microvm.ResolvedArtifact{}, microvm.ErrDigestMismatch
	}
	return artifact, nil
}

func (r *configuredResolver) configureOCI(cacheRoot string) {
	r.oci = microvm.NewOCIExecutionImageResolver(cacheRoot, nil, r.ociEvidence)
}

type stderrDiagnostics struct{}

func (stderrDiagnostics) Log(_ context.Context, level port.Level, msg string, args ...any) {
	_, _ = fmt.Fprintln(os.Stderr, append([]any{"microvmd", level, msg}, args...)...)
}
func (d stderrDiagnostics) With(...any) port.Diagnostics { return d }

func main() {
	if handled, err := microvm.RunLaunchOwnerChild(os.Args[1:]); handled {
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	stateDir := flag.String("state-dir", "", "private absolute daemon state directory")
	socketPath := flag.String("socket", "", "private absolute lifecycle Unix socket")
	configPath := flag.String("config", "", "operator-owned artifact and network configuration")
	doctor := flag.Bool("doctor", false, "check microVM runtime readiness and exit")
	flag.Parse()
	var err error
	if *doctor {
		err = runDoctor(*stateDir, *socketPath, *configPath)
	} else {
		err = run(*stateDir, *socketPath, *configPath)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func setDeterministicArtifactUmask() {
	// OCI extraction must preserve image modes independently of the shell that
	// launched this long-lived daemon. Private manager state is explicitly 0600/0700.
	_ = syscall.Umask(0o022)
}

func run(stateDir, socketPath, configPath string) error { //nolint:gocyclo // explicit fail-closed composition validation
	setDeterministicArtifactUmask()
	if !filepath.IsAbs(stateDir) || !filepath.IsAbs(socketPath) || !filepath.IsAbs(configPath) {
		return errors.New("state-dir, socket, and config must be absolute")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil { // #nosec G703 -- absolute operator-configured private state root validated above.
		return err
	}
	ownershipLock, err := acquireDaemonOwnership(stateDir)
	if err != nil {
		return err
	}
	defer ownershipLock.close()
	if daemonAfterOwnership != nil {
		daemonAfterOwnership()
	}
	_, _ = fmt.Fprintf(os.Stderr, "microvmd process identity uid=%d euid=%d gid=%d egid=%d\n", os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid())
	cfg, resolver, policy, err := loadDaemonConfig(configPath)
	if err != nil {
		return err
	}
	info, err := servingDaemonInfo(cfg, socketPath)
	if err != nil {
		return err
	}
	if cfg.BinaryIdentity != info.BinaryIdentity {
		return errors.New("running microvmd binary does not match configured binary identity")
	}
	uid := os.Getuid()
	if uid < 0 || uint64(uid) > uint64(^uint32(0)) {
		return errors.New("daemon account UID is outside the supported range")
	}
	controlService, err := control.NewService(control.ServiceConfig{AccountUID: uint32(uid)}) // #nosec G115 -- range checked above.
	if err != nil {
		return err
	}
	runtimeDir := cfg.RuntimeDir
	if runtimeDir == "" {
		runtimeDir = filepath.Join(stateDir, "vsock")
	} else if !filepath.IsAbs(runtimeDir) {
		return errors.New("runtime_dir must be absolute")
	}
	runtimeArtifactDir := filepath.Join(stateDir, "runtime-artifacts")
	for _, dir := range []string{runtimeDir, runtimeArtifactDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec G703 -- derived from the validated state root or validated absolute runtime_dir.
			return err
		}
	}
	artifactRequests := make(map[microvm.ArtifactKind]microvm.ArtifactRequest, len(cfg.Artifacts))
	for _, artifact := range cfg.Artifacts {
		artifactRequests[artifact.Kind] = artifactRequest(artifact)
	}
	artifactCache := cfg.ArtifactCache
	if artifactCache == "" {
		artifactCache = filepath.Join(stateDir, "artifacts")
	} else if !filepath.IsAbs(artifactCache) {
		return errors.New("artifact_cache must be absolute")
	}
	resolver.configureOCI(filepath.Join(artifactCache, "oci"))
	observer := microvm.NewOperationsObserver(stderrDiagnostics{})
	artifactVerifier := microvm.NewProvisioner(microvm.NewVerifiedCache(artifactCache), resolver, policy, nil, nil, observer)
	launcherPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve microvmd launcher: %w", err)
	}
	ownership, err := microvm.NewLaunchOwnership(microvm.LaunchOwnershipConfig{
		Root: filepath.Join(stateDir, "launch-attempts"), LauncherPath: launcherPath,
	})
	if err != nil {
		return fmt.Errorf("configure runner launch ownership: %w", err)
	}
	backend, err := microvm.NewLibkrunBackend(runtimeArtifactDir, ownership)
	if err != nil {
		return fmt.Errorf("configure repository libkrun backend: %w", err)
	}
	network := microvm.NewHostedBootNetworkController()
	repositories, err := microvm.NewRepositoryComposition(filepath.Join(stateDir, "repositories"), microvm.RepositoryRuntimeConfig{
		Backend: backend, Network: network, GuestEgress: cfg.GuestEgress, ArtifactPolicy: policy.Revision,
		Artifacts: artifactRequests, LaunchReconciler: ownership, Observer: observer, UnixEndpoint: true, EndpointRoot: runtimeDir,
	})
	if err != nil {
		return err
	}
	repositoryProvisioner := func(_ context.Context, request microvm.ProvisionRequest) (microvm.RepositoryPlacement, error) {
		if request.Owner == "" || request.SessionID == "" || request.Profile == "" || request.SourceCheckout == "" {
			return microvm.RepositoryPlacement{}, errors.New("incomplete microvm provision request")
		}
		if _, ok := cfg.Profiles[request.Profile]; !ok {
			return microvm.RepositoryPlacement{}, errors.New("unknown microvm environment profile")
		}
		if _, ok := artifactRequests[microvm.ArtifactExecutionImage]; !ok {
			return microvm.RepositoryPlacement{}, errors.New("microvm daemon has no execution image")
		}
		if _, ok := artifactRequests[microvm.ArtifactGuestAgent]; !ok {
			return microvm.RepositoryPlacement{}, errors.New("microvm daemon has no independently admitted guest agent")
		}
		status := microvm.EnforcedProfileStatus{Profile: request.Profile, GuestEgress: cfg.GuestEgress.Status(), HostEgress: "not constrained: LLM providers, WebFetch, WebSearch, MCP, hooks, OCI pulls, telemetry"}
		return microvm.RepositoryPlacement{
			Request: microvm.LogicalEnvironmentRequest{Owner: request.Owner, Checkout: request.SourceCheckout, Artifacts: repositoryArtifactSnapshot(artifactVerifier, artifactRequests)},
			Status:  status,
		}, nil
	}
	root, err := microvm.NewRuntimeDaemon(microvm.RuntimeDaemonConfig{
		Control: controlService, Observer: observer, Info: info,
		Repository: repositories, RepositoryProvisioner: repositoryProvisioner,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil { // #nosec G703 -- socketPath is an absolute operator-configured daemon endpoint.
		return err
	}
	if err := removeExistingSocket(socketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	if err := os.Chmod(socketPath, 0o600); err != nil { // #nosec G703 -- socketPath is the validated absolute daemon endpoint.
		return err
	}
	defer func() { _ = os.Remove(socketPath) }() // #nosec G703 -- removes only the validated endpoint this process bound.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return root.Serve(ctx, listener)
}

type repositoryArtifactVerifier interface {
	Verify(context.Context, map[microvm.ArtifactKind]microvm.ArtifactRequest) (microvm.VerifiedArtifacts, string, error)
	LockAndValidate(context.Context, microvm.VerifiedArtifacts) (microvm.VerifiedArtifacts, func(), error)
}

func repositoryArtifactSnapshot(verifier repositoryArtifactVerifier, requests map[microvm.ArtifactKind]microvm.ArtifactRequest) microvm.RepositoryArtifactSnapshot {
	return func(ctx context.Context) (microvm.VerifiedArtifacts, func(), error) {
		verified, _, err := verifier.Verify(ctx, requests)
		if err != nil {
			return microvm.VerifiedArtifacts{}, nil, err
		}
		return verifier.LockAndValidate(ctx, verified)
	}
}

type doctorChecker struct {
	stateDir   string
	socketPath string
	cfg        daemonConfig
	artifacts  microvm.ArtifactVerifier
	requests   map[microvm.ArtifactKind]microvm.ArtifactRequest

	artifactProbe    func(context.Context, microvm.ReadinessCheck) error
	artifactOnce     sync.Once
	artifactErr      error
	hypervisorProbe  func(context.Context) error
	controlPeerProbe func(context.Context) error
	networkProbe     func(context.Context) error
	profileProbe     func(context.Context) ([]string, error)
}

func runDoctor(stateDir, socketPath, configPath string) error {
	setDeterministicArtifactUmask()
	if !filepath.IsAbs(stateDir) || !filepath.IsAbs(socketPath) || !filepath.IsAbs(configPath) {
		return errors.New("state-dir, socket, and config must be absolute")
	}
	cfg, resolver, policy, err := loadDaemonConfig(configPath)
	if err != nil {
		return err
	}
	artifactCache := cfg.ArtifactCache
	if artifactCache == "" {
		artifactCache = filepath.Join(stateDir, "artifacts")
	} else if !filepath.IsAbs(artifactCache) {
		return errors.New("artifact_cache must be absolute")
	}
	resolver.configureOCI(filepath.Join(artifactCache, "oci"))
	requests := make(map[microvm.ArtifactKind]microvm.ArtifactRequest, len(cfg.Artifacts))
	for _, artifact := range cfg.Artifacts {
		requests[artifact.Kind] = artifactRequest(artifact)
	}
	checker := &doctorChecker{
		stateDir: stateDir, socketPath: socketPath, cfg: cfg, requests: requests,
		artifacts: microvm.NewProvisioner(microvm.NewVerifiedCache(artifactCache), resolver, policy, nil, nil),
	}
	report := microvm.NewDoctor(checker).Run(context.Background())
	if runtime.GOOS == "linux" {
		_, _ = fmt.Fprintf(os.Stdout, "microvmd doctor identity uid=%d euid=%d gid=%d egid=%d hypervisor=/dev/kvm flags=O_RDWR userns=CLONE_NEWUSER\n", os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid())
	} else {
		_, _ = fmt.Fprintf(os.Stdout, "microvmd doctor identity uid=%d euid=%d hypervisor=Hypervisor.framework\n", os.Getuid(), os.Geteuid())
	}
	_, _ = fmt.Fprint(os.Stdout, report.String())
	if !report.Ready() {
		return errors.New("microvmd doctor found failed readiness checks")
	}
	return nil
}

func (d *doctorChecker) Check(ctx context.Context, check microvm.ReadinessCheck) error {
	switch check {
	case microvm.CheckHypervisor:
		if d.hypervisorProbe != nil {
			return d.hypervisorProbe(ctx)
		}
		return checkHypervisorAccess(ctx)
	case microvm.CheckRuntime, microvm.CheckFirmware:
		if d.artifactProbe != nil {
			return d.artifactProbe(ctx, check)
		}
		d.artifactOnce.Do(func() {
			if d.artifacts == nil {
				d.artifactErr = errors.New("artifact verifier is not configured")
				return
			}
			_, _, d.artifactErr = d.artifacts.Verify(ctx, d.requests)
		})
		return d.artifactErr
	case microvm.CheckControlSocket:
		if d.controlPeerProbe != nil {
			return d.controlPeerProbe(ctx)
		}
		return checkControlPeer(ctx, d.socketPath)
	case microvm.CheckNetwork:
		if d.networkProbe != nil {
			return d.networkProbe(ctx)
		}
		runtimeDir := d.cfg.RuntimeDir
		if runtimeDir == "" {
			runtimeDir = filepath.Join(d.stateDir, "vsock")
		}
		return checkNetworkProvider(ctx, d.cfg.GuestEgress, runtimeDir)
	default:
		return fmt.Errorf("unknown readiness check %q", check)
	}
}

func checkHypervisorAccess(ctx context.Context) error {
	if runtime.GOOS == "linux" {
		if err := checkLinuxUserNamespaceCapability(ctx); err != nil {
			return err
		}
		return checkLinuxKVMAccess(os.OpenFile, os.Getuid(), os.Geteuid())
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("unsupported host platform %s", runtime.GOOS)
	}
	output, err := exec.CommandContext(ctx, "sysctl", "-n", "kern.hv_support").Output()
	if err != nil {
		return fmt.Errorf("query Hypervisor.framework support: %w", err)
	}
	if strings.TrimSpace(string(output)) != "1" {
		return errors.New("hypervisor.framework is unavailable")
	}
	return nil
}

func checkLinuxKVMAccess(openFile func(string, int, os.FileMode) (*os.File, error), uid, euid int) error {
	file, err := openFile("/dev/kvm", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open /dev/kvm with O_RDWR as uid=%d euid=%d: %w", uid, euid, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close /dev/kvm opened with O_RDWR as uid=%d euid=%d: %w", uid, euid, err)
	}
	return nil
}

func checkControlPeer(ctx context.Context, socketPath string) error {
	info, err := os.Lstat(socketPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("control endpoint is not a private Unix socket")
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect to microvmd control peer: %w", err)
	}
	defer func() { _ = conn.Close() }()
	uid := os.Getuid()
	if uid < 0 || uint64(uid) > uint64(^uint32(0)) {
		return errors.New("daemon account UID is outside the supported range")
	}
	service, err := control.NewService(control.ServiceConfig{AccountUID: uint32(uid)}) // #nosec G115 -- range checked above.
	if err != nil {
		return err
	}
	if err := service.Authenticate(conn); err != nil {
		return fmt.Errorf("authenticate microvmd control peer: %w", err)
	}
	return nil
}

func checkNetworkProvider(ctx context.Context, policy microvm.GuestEgressPolicy, stateDir string) error {
	networkDir := filepath.Join(stateDir, "doctor-network")
	if err := os.MkdirAll(networkDir, 0o700); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(networkDir) }()
	controller := microvm.NewHostedBootNetworkController()
	handle, err := controller.StartForDoctor(ctx, policy, networkDir)
	if err != nil {
		return err
	}
	handle.Provider.Stop()
	return nil
}

func (d *doctorChecker) Profiles(ctx context.Context) ([]string, error) {
	if d.profileProbe != nil {
		return d.profileProbe(ctx)
	}
	if !validDaemonTrustPolicy(d.cfg) {
		return nil, errors.New("daemon profile has incomplete or ambiguous Sigstore trust policy")
	}
	artifacts := make(map[microvm.ArtifactKind]artifactConfig, len(d.cfg.Artifacts))
	for _, artifact := range d.cfg.Artifacts {
		artifacts[artifact.Kind] = artifact
	}
	for _, kind := range []microvm.ArtifactKind{microvm.ArtifactRuntime, microvm.ArtifactFirmware, microvm.ArtifactExecutionImage, microvm.ArtifactGuestAgent} {
		artifact, ok := artifacts[kind]
		if !ok || artifact.Reference == "" || artifact.Digest == "" || artifact.Provenance == "" || artifact.SigstoreBundle == "" || d.cfg.Attestations[kind] == "" {
			return nil, fmt.Errorf("daemon profile has inconsistent %s Sigstore policy", kind)
		}
		if kind == microvm.ArtifactExecutionImage && !validBroodResolution(artifact) {
			return nil, errors.New("daemon execution image lacks exact Brood discovery resolution evidence")
		}
	}
	if len(d.cfg.Profiles) == 0 {
		return nil, errors.New("no daemon environment profiles are configured")
	}
	profiles := make([]string, 0, len(d.cfg.Profiles))
	for name := range d.cfg.Profiles {
		if name == "" {
			return nil, errors.New("daemon environment profile name is empty")
		}
		profiles = append(profiles, name)
	}
	return profiles, nil
}

func servingDaemonInfo(cfg daemonConfig, socketPath string) (microvm.DaemonInfo, error) {
	if !strings.HasPrefix(cfg.ReleaseIdentity, "sha256:") || len(cfg.ReleaseIdentity) != 71 || !strings.HasPrefix(cfg.BinaryIdentity, "sha256:") || len(cfg.BinaryIdentity) != 71 || cfg.loadedConfigDigest == "" {
		return microvm.DaemonInfo{}, errors.New("daemon config omits release or binary identity")
	}
	binaryPath, err := os.Executable()
	if err != nil {
		return microvm.DaemonInfo{}, fmt.Errorf("resolve running microvmd binary: %w", err)
	}
	binaryIdentity, err := fileIdentity(binaryPath)
	if err != nil {
		return microvm.DaemonInfo{}, fmt.Errorf("hash running microvmd binary: %w", err)
	}
	profiles := make([]string, 0, len(cfg.Profiles))
	for profile := range cfg.Profiles {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	return microvm.DaemonInfo{ProtocolVersion: microvm.LifecycleProtocolVersion, ReleaseIdentity: cfg.ReleaseIdentity, BinaryIdentity: binaryIdentity, ConfigDigest: cfg.loadedConfigDigest, PolicyRevision: cfg.PolicyRevision, Profiles: profiles, Socket: socketPath}, nil
}

func fileIdentity(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- identity is computed for an explicit executable or operator config path.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func removeExistingSocket(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- caller supplies the validated absolute daemon socket path.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("refusing to replace non-socket microvmd endpoint")
	}
	return os.Remove(path) // #nosec G703 -- removes only a socket at the validated absolute daemon endpoint.
}

func validDaemonTrustPolicy(cfg daemonConfig) bool {
	if cfg.PolicyRevision == "" {
		return false
	}
	keyless := cfg.CertificateIdentity != "" && cfg.OIDCIssuer != "" && cfg.PublicKey == "" && cfg.PublicKeyIdentity == ""
	keyed := cfg.CertificateIdentity == "" && cfg.OIDCIssuer == "" && filepath.IsAbs(cfg.PublicKey) && cfg.PublicKeyIdentity != ""
	return keyless || keyed
}

func validBroodResolution(entry artifactConfig) bool {
	return entry.DiscoveryReference == "ghcr.io/stacklok/brood-box/base:latest" &&
		validSHA256(entry.ManifestDigest) && validSHA256(entry.ResolutionEvidence) &&
		(entry.Platform == "linux/amd64" || entry.Platform == "linux/arm64")
}

func validSHA256(value string) bool {
	hexValue := strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(hexValue)
	return err == nil && len(decoded) == sha256.Size && value == "sha256:"+hexValue && value == strings.ToLower(value)
}

func validateArtifactConfig(entry artifactConfig) (bool, error) {
	isOCI := entry.Kind == microvm.ArtifactExecutionImage && entry.ManifestDigest != ""
	if isOCI && !validBroodResolution(entry) {
		return false, errors.New("execution image requires exact Brood discovery resolution evidence")
	}
	if !filepath.IsAbs(entry.Provenance) || !filepath.IsAbs(entry.SigstoreBundle) || entry.Reference == "" ||
		(!isOCI && !filepath.IsAbs(entry.Path)) || (isOCI && entry.Path != "") {
		return false, fmt.Errorf("invalid %s artifact source or evidence path", entry.Kind)
	}
	return isOCI, nil
}

func decodeDaemonConfig(data []byte) (daemonConfig, error) {
	var cfg daemonConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return daemonConfig{}, err
	}
	return cfg, nil
}

func loadDaemonConfig(path string) (daemonConfig, *configuredResolver, microvm.TrustPolicy, error) {
	data, err := os.ReadFile(path) // #nosec G304,G703 -- explicit operator-owned absolute configuration path.
	if err != nil {
		return daemonConfig{}, nil, microvm.TrustPolicy{}, err
	}
	cfg, err := decodeDaemonConfig(data)
	if err != nil {
		return daemonConfig{}, nil, microvm.TrustPolicy{}, err
	}
	digest := sha256.Sum256(data)
	cfg.loadedConfigDigest = "sha256:" + hex.EncodeToString(digest[:])
	if !validDaemonTrustPolicy(cfg) {
		return daemonConfig{}, nil, microvm.TrustPolicy{}, errors.New("incomplete or ambiguous Sigstore trust policy")
	}
	var verifier microvm.EvidenceVerifier
	if cfg.PublicKey != "" {
		publicKey, readErr := os.ReadFile(cfg.PublicKey) // #nosec G304 -- operator-owned absolute trust-root path.
		if readErr != nil {
			return daemonConfig{}, nil, microvm.TrustPolicy{}, fmt.Errorf("read Sigstore public key: %w", readErr)
		}
		if microvm.PublicKeyIdentity(publicKey) != cfg.PublicKeyIdentity {
			return daemonConfig{}, nil, microvm.TrustPolicy{}, errors.New("sigstore public-key identity mismatch")
		}
		verifier, err = microvm.NewSigstoreKeyVerifier(publicKey)
	} else {
		verifier = microvm.NewSigstoreVerifier()
	}
	if err != nil {
		return daemonConfig{}, nil, microvm.TrustPolicy{}, errors.New("incomplete Sigstore trust policy")
	}
	policy := microvm.TrustPolicy{
		Revision: cfg.PolicyRevision, CertificateIdentity: cfg.CertificateIdentity,
		OIDCIssuer: cfg.OIDCIssuer, PublicKeyIdentity: cfg.PublicKeyIdentity,
		Verifier: verifier, RequiredAttestations: cfg.Attestations,
	}
	resolver := &configuredResolver{
		local:       make(map[string]microvm.ResolvedArtifact, len(cfg.Artifacts)),
		ociEvidence: make(map[string]microvm.VerificationEvidence),
	}
	for _, entry := range cfg.Artifacts {
		isOCI, validateErr := validateArtifactConfig(entry)
		if validateErr != nil {
			return daemonConfig{}, nil, microvm.TrustPolicy{}, validateErr
		}
		statement, err := os.ReadFile(entry.Provenance) // #nosec G304 -- operator-owned absolute configuration path.
		if err != nil {
			return daemonConfig{}, nil, microvm.TrustPolicy{}, fmt.Errorf("read %s provenance: %w", entry.Kind, err)
		}
		bundle, err := os.ReadFile(entry.SigstoreBundle) // #nosec G304 -- operator-owned absolute configuration path.
		if err != nil {
			return daemonConfig{}, nil, microvm.TrustPolicy{}, fmt.Errorf("read %s Sigstore bundle: %w", entry.Kind, err)
		}
		evidence := microvm.VerificationEvidence{Bundle: bundle, Attestation: microvm.Attestation{
			PredicateType: cfg.Attestations[entry.Kind], SubjectDigest: entry.Digest, Statement: statement,
		}}
		if isOCI {
			resolver.ociEvidence[entry.Reference] = evidence
			continue
		}
		resolver.local[entry.Reference] = microvm.ResolvedArtifact{
			Kind: entry.Kind, Digest: entry.Digest, Source: extract.Dir(entry.Path), Evidence: evidence,
		}
	}
	return cfg, resolver, policy, nil
}
