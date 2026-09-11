// Package microvmmanager owns idempotent readiness and explicit diagnostics for
// the user-local microvmd lifecycle. It supplies safe paths and process operations
// without weakening microvmd's absolute-path or operator-policy validation.
package microvmmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	microvmclient "github.com/stacklok/mecatl/internal/adapter/microvm"
)

const (
	// Alias is the built-in local profile selected through ordinary configuration.
	Alias = "microvm-local"
	// DarwinSocketPathLimit is kept below Darwin's sockaddr_un.sun_path bound.
	DarwinSocketPathLimit = 104
	// longestRuntimeSocketSuffix is the longest socket path microvmd derives below
	// RuntimeDir: the hosted network socket for a repository generation.
	longestRuntimeSocketSuffix = "generations/repository-0000000000000000-00000000.sock.network/hosted-net.sock"
)

// ReadinessStage is one bounded, secret-free phase of EnsureReady.
type ReadinessStage string

// Readiness stages are emitted in transaction order.
const (
	StagePrepare   ReadinessStage = "prepare"
	StagePreflight ReadinessStage = "preflight"
	StageDownload  ReadinessStage = "download"
	StageVerify    ReadinessStage = "verify"
	StageInstall   ReadinessStage = "install"
	StageDaemon    ReadinessStage = "daemon"
	StageSocket    ReadinessStage = "socket"
	StageReconcile ReadinessStage = "reconcile"
	StageHealth    ReadinessStage = "health"
	StageReady     ReadinessStage = "ready"
)

// ReadinessObserver receives only closed stage identifiers and constant display
// text. Release metadata, paths, credentials, and process output are never sent.
type ReadinessObserver func(ReadinessStage, string)

type readinessObserverKey struct{}

func readinessText(stage ReadinessStage) (string, bool) {
	switch stage {
	case StagePrepare:
		return "Preparing local microVM readiness", true
	case StagePreflight:
		return "Checking host prerequisites", true
	case StageDownload:
		return "Downloading microVM components (up to about 2 GiB)", true
	case StageVerify:
		return "Verifying downloaded components", true
	case StageInstall:
		return "Installing and configuring the local microVM", true
	case StageDaemon:
		return "Starting or reusing the microVM daemon", true
	case StageSocket:
		return "Waiting for the microVM daemon socket", true
	case StageReconcile:
		return "Reconciling existing microVM state", true
	case StageHealth:
		return "Running microVM health checks", true
	case StageReady:
		return "Local microVM is ready", true
	default:
		return "", false
	}
}

// WithReadinessObserver returns a context that observes EnsureReady stages.
func WithReadinessObserver(ctx context.Context, observer ReadinessObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, readinessObserverKey{}, observer)
}

// ReportReadinessStage reports one stage to an observer carried by ctx. It is
// exported so command-root test doubles can preserve the observer contract.
func ReportReadinessStage(ctx context.Context, stage ReadinessStage) {
	observer, _ := ctx.Value(readinessObserverKey{}).(ReadinessObserver)
	text, ok := readinessText(stage)
	if observer != nil && ok {
		observer(stage, text)
	}
}

// HostPaths supplies environment-independent inputs for DefaultPaths.
type HostPaths struct {
	Home, XDGConfigHome, XDGDataHome, XDGStateHome, XDGRuntimeDir string
	UID                                                           int
	GOOS                                                          string
}

// Paths are the absolute, user-owned manager paths.
type Paths struct {
	StateDir, RuntimeDir, Socket, DataDir, ConfigFile, UserSettings string
	DaemonBinary                                                    string
}

// DefaultPaths resolves XDG paths without requiring any XDG variable. The
// runtime directory uses a short /tmp path when any derived daemon socket would
// exceed Darwin's Unix-socket bound.
func DefaultPaths(host HostPaths) (Paths, error) {
	if host.Home == "" || !filepath.IsAbs(host.Home) || host.UID < 0 {
		return Paths{}, errors.New("microVM manager requires an absolute home and valid uid")
	}
	config := xdgBase(host.XDGConfigHome, filepath.Join(host.Home, ".config"))
	data := xdgBase(host.XDGDataHome, filepath.Join(host.Home, ".local", "share"))
	state := xdgBase(host.XDGStateHome, filepath.Join(host.Home, ".local", "state"))
	if config == "" || data == "" || state == "" {
		return Paths{}, errors.New("XDG paths must be absolute")
	}
	dataDir := filepath.Join(data, "mecatl", "microvm")
	runtimeDir := ""
	if filepath.IsAbs(host.XDGRuntimeDir) {
		candidate := filepath.Join(host.XDGRuntimeDir, "mecatl-microvm")
		if len(filepath.Join(candidate, longestRuntimeSocketSuffix)) < DarwinSocketPathLimit {
			runtimeDir = candidate
		}
	}
	if runtimeDir == "" {
		runtimeDir = filepath.Join(string(filepath.Separator)+"tmp", "mv-"+strconv.Itoa(host.UID))
	}
	paths := Paths{
		StateDir: filepath.Join(state, "mecatl", "microvm"), RuntimeDir: runtimeDir,
		Socket: filepath.Join(runtimeDir, "microvmd.sock"), DataDir: dataDir,
		ConfigFile:   filepath.Join(config, "mecatl", "microvmd.json"),
		UserSettings: filepath.Join(config, "mecatl", "settings.yaml"),
		DaemonBinary: filepath.Join(dataDir, "bin", "mecatl-microvmd"),
	}
	if err := validatePaths(paths); err != nil {
		return Paths{}, err
	}
	return paths, nil
}

func xdgBase(value, fallback string) string {
	if value == "" {
		return fallback
	}
	if !filepath.IsAbs(value) {
		return ""
	}
	return filepath.Clean(value)
}

// Release identifies a bundle by an immutable, out-of-band SHA-256 digest.
type Release struct {
	URL, SHA256 string
	bundlePath  string
	bundleFile  *os.File
}

// Policy is the daemon-enforced local profile policy. Bootstrap writes it only
// to microvmd's strict config; the host alias contains only enabled+endpoint.
type Policy struct {
	PolicyRevision, CertificateIdentity, OIDCIssuer string
	PublicKey, PublicKeyIdentity                    string
	publicKey                                       []byte
	RequiredAttestations                            map[string]string
	GuestEgressMode                                 string
	GuestAllow                                      []EgressRule
	Admission                                       map[string]int64
	Resources                                       map[string]string
}

// EgressRule is one daemon-enforced guest destination allowance.
type EgressRule struct {
	Hostname string
	Port     uint16
	Protocol uint8
}

// ReadyRequest identifies the manager-owned release and daemon policy that
// ordinary use must converge before provisioning a microVM environment.
type ReadyRequest struct {
	Release Release
	Policy  Policy
}

// Artifact is one installer-projected verified admission artifact.
type Artifact struct {
	Kind               string `json:"kind"`
	Reference          string `json:"reference"`
	Digest             string `json:"digest"`
	ManifestDigest     string `json:"manifest_digest,omitempty"`
	DiscoveryReference string `json:"discovery_reference,omitempty"`
	ResolutionEvidence string `json:"resolution_evidence,omitempty"`
	Platform           string `json:"platform,omitempty"`
	Path               string `json:"path"`
	Provenance         string `json:"provenance"`
	SigstoreBundle     string `json:"sigstore_bundle"`
}

// InstalledArtifacts is the existing release installer's strict projection.
type InstalledArtifacts struct {
	Schema    string     `json:"schema,omitempty"`
	Artifacts []Artifact `json:"artifacts"`
}

// DaemonInfo is the authenticated serving-daemon identity used for exact reuse.
type DaemonInfo = microvmclient.DaemonInfo

// Operations is the OS/network/process boundary. DefaultOperations is the real
// implementation; tests can keep the complete manager journey offline.
type Operations interface {
	Preflight(context.Context, Paths) error
	Download(context.Context, Release, string) (string, error)
	Verify(context.Context, Release, string) error
	Install(context.Context, string, string) (InstalledArtifacts, error)
	Running(context.Context, Paths) (bool, error)
	DaemonInfo(context.Context, Paths) (DaemonInfo, error)
	Start(context.Context, Paths) error
	WaitSocket(context.Context, string) error
	Doctor(context.Context, Paths) (string, error)
	Stop(context.Context, Paths) error
}

type lifecycleClient interface {
	Inventory(context.Context, string, ...microvmclient.InventoryRequest) (microvmclient.InventoryPage, error)
	Reconcile(context.Context, string) error
	DeleteGeneration(context.Context, microvmclient.GenerationBinding) (microvmclient.DeleteResult, error)
}

// Manager coordinates readiness and daemon lifecycle operations.
type Manager struct {
	paths     Paths
	ops       Operations
	lifecycle lifecycleClient
	mu        sync.Mutex
}

// New constructs a manager over explicit paths and operations.
func New(paths Paths, ops Operations, lifecycle ...lifecycleClient) *Manager {
	manager := &Manager{paths: paths, ops: ops}
	if len(lifecycle) > 0 {
		manager.lifecycle = lifecycle[0]
	}
	return manager
}

// EnsureReady idempotently installs or validates the configured release, starts
// or reuses the exact compatible daemon, and reconciles its durable state. The
// inter-process manager lock serializes the complete transaction. Readiness is
// observed state: this method never edits the operator settings file.
func (m *Manager) EnsureReady(ctx context.Context, request ReadyRequest) (string, error) { //nolint:gocyclo // ordered readiness transaction
	ReportReadinessStage(ctx, StagePrepare)
	m.mu.Lock()
	defer m.mu.Unlock()
	if request.Release.bundleFile != nil {
		defer func() { _ = request.Release.bundleFile.Close() }()
	}
	if m.ops == nil {
		return "", readinessError(StagePrepare, errors.New("microVM manager operations are not configured"))
	}
	if err := preparePaths(m.paths); err != nil {
		return "", readinessError(StagePrepare, err)
	}
	unlock, err := lockManager(m.paths.StateDir)
	if err != nil {
		return "", readinessError(StagePrepare, err)
	}
	defer unlock()
	if len(request.Policy.publicKey) != 0 {
		if bytesSHA256(request.Policy.publicKey) != request.Policy.PublicKeyIdentity {
			return "", readinessError(StagePrepare, errors.New("local microVM release public-key identity mismatch"))
		}
		request.Policy.PublicKey = filepath.Join(m.paths.DataDir, "trust", "development-"+strings.TrimPrefix(request.Policy.PublicKeyIdentity, "sha256:")+".pub")
	}
	if err := validateReleasePolicy(request.Release, request.Policy); err != nil {
		return "", readinessError(StagePrepare, err)
	}
	ReportReadinessStage(ctx, StagePreflight)
	if err := m.ops.Preflight(ctx, m.paths); err != nil {
		return "", readinessError(StagePreflight, fmt.Errorf("microVM preflight: %w", err))
	}

	running, err := m.ops.Running(ctx, m.paths)
	if err != nil {
		ReportReadinessStage(ctx, StageDaemon)
		return "", readinessError(StageDaemon, fmt.Errorf("inspect existing microvmd runtime: %w", err))
	}
	configured := regularFile(m.paths.ConfigFile)
	if configured || running {
		ReportReadinessStage(ctx, StageDaemon)
		if !configured {
			return "", readinessError(StageDaemon, errors.New("microvmd is serving without the manager-owned configuration; existing runtime was left unchanged"))
		}
		if err := configuredRequestCompatible(m.paths.ConfigFile, request.Release, request.Policy); err != nil {
			return "", readinessError(StageDaemon, fmt.Errorf("existing microvmd configuration is incompatible with the requested release or policy; existing runtime was left unchanged: %w", err))
		}
		if !running {
			return "", readinessError(StageDaemon, errors.New("configured microvmd is not serving; refusing to replace or restart existing repository runtime"))
		}
		expected, err := expectedDaemonInfo(m.paths)
		if err != nil {
			return "", readinessError(StageDaemon, fmt.Errorf("validate installed microvmd identity without changing it: %w", err))
		}
		serving, err := m.ops.DaemonInfo(ctx, m.paths)
		if err != nil {
			return "", readinessError(StageDaemon, fmt.Errorf("query serving microvmd identity; existing runtime was left unchanged: %w", err))
		}
		if !serving.Equal(expected) {
			return "", readinessError(StageDaemon, errors.New("serving microvmd identity is incompatible with its installed release, policy, process, or runtime state; existing runtime was left unchanged"))
		}
	} else {
		fresh, err := managerRuntimeFresh(m.paths)
		if err != nil {
			return "", readinessError(StageDaemon, err)
		}
		if !fresh {
			ReportReadinessStage(ctx, StageDaemon)
			return "", readinessError(StageDaemon, errors.New("unconfigured microvmd has existing repository runtime state; refusing to overwrite or delete it"))
		}
		ReportReadinessStage(ctx, StageDownload)
		manifest, err := m.ops.Download(ctx, request.Release, filepath.Join(m.paths.DataDir, "download"))
		if err != nil {
			return "", readinessError(StageDownload, fmt.Errorf("download release bundle: %w", err))
		}
		ReportReadinessStage(ctx, StageVerify)
		if err := m.ops.Verify(ctx, request.Release, manifest); err != nil {
			return "", readinessError(StageVerify, fmt.Errorf("verify release bundle: %w", err))
		}
		ReportReadinessStage(ctx, StageInstall)
		installed, err := m.ops.Install(ctx, manifest, filepath.Join(m.paths.DataDir, "verified"))
		if err != nil {
			return "", readinessError(StageInstall, fmt.Errorf("install verified release bundle: %w", err))
		}
		if err := validateInstalled(installed, m.paths.DataDir); err != nil {
			return "", readinessError(StageInstall, err)
		}
		if len(request.Policy.publicKey) != 0 {
			if err := atomicWrite(request.Policy.PublicKey, request.Policy.publicKey); err != nil {
				return "", readinessError(StageInstall, fmt.Errorf("materialize local microVM release public key: %w", err))
			}
		}
		if err := writeDaemonConfig(m.paths.ConfigFile, m.paths, request.Release, request.Policy, installed); err != nil {
			return "", readinessError(StageInstall, err)
		}
		ReportReadinessStage(ctx, StageDaemon)
		if err := m.ops.Start(ctx, m.paths); err != nil {
			return "", readinessError(StageDaemon, fmt.Errorf("start microvmd: %w", err))
		}
	}
	ReportReadinessStage(ctx, StageSocket)
	if err := m.ops.WaitSocket(ctx, m.paths.Socket); err != nil {
		return "", readinessError(StageSocket, fmt.Errorf("wait for owner-only microvmd socket: %w", err))
	}
	if m.lifecycle != nil {
		ReportReadinessStage(ctx, StageReconcile)
		if err := m.lifecycle.Reconcile(ctx, "local"); err != nil {
			return "", readinessError(StageReconcile, fmt.Errorf("reconcile existing microVM state: %w", err))
		}
	}
	ReportReadinessStage(ctx, StageHealth)
	if _, err := m.ops.Doctor(ctx, m.paths); err != nil {
		return "", readinessError(StageHealth, fmt.Errorf("microvmd doctor: %w", err))
	}
	ReportReadinessStage(ctx, StageReady)
	return "unix://" + m.paths.Socket, nil
}

func readinessError(stage ReadinessStage, err error) error {
	return fmt.Errorf("microvm-local readiness failed during %s: %w; correct the reported problem and retry ordinary use", stage, err)
}

func configuredRequestCompatible(path string, release Release, policy Policy) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read existing microvmd config: %w", err)
	}
	var current map[string]any
	if err := json.Unmarshal(data, &current); err != nil {
		return fmt.Errorf("decode existing microvmd config: %w", err)
	}
	desired := map[string]any{
		"release_identity":      "sha256:" + release.SHA256,
		"policy_revision":       policy.PolicyRevision,
		"required_attestations": policy.RequiredAttestations,
		"guest_egress":          map[string]any{"Mode": policy.GuestEgressMode, "Allow": policy.GuestAllow},
		"admission":             policy.Admission,
		"profiles":              map[string]any{Alias: map[string]any{"resources": policy.Resources}},
	}
	if policy.CertificateIdentity != "" {
		desired["certificate_identity"], desired["oidc_issuer"] = policy.CertificateIdentity, policy.OIDCIssuer
	} else {
		desired["public_key"], desired["public_key_identity"] = policy.PublicKey, policy.PublicKeyIdentity
	}
	normalized, err := json.Marshal(desired)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(normalized, &desired); err != nil {
		return err
	}
	for key, want := range desired {
		if got, ok := current[key]; !ok || !reflect.DeepEqual(got, want) {
			return fmt.Errorf("configured %s differs from the requested value", key)
		}
	}
	if policy.CertificateIdentity != "" {
		for _, key := range []string{"public_key", "public_key_identity"} {
			if _, present := current[key]; present {
				return fmt.Errorf("configured %s conflicts with requested trust policy", key)
			}
		}
	} else {
		for _, key := range []string{"certificate_identity", "oidc_issuer"} {
			if _, present := current[key]; present {
				return fmt.Errorf("configured %s conflicts with requested trust policy", key)
			}
		}
	}
	return nil
}

func managerRuntimeFresh(paths Paths) (bool, error) {
	for _, path := range []string{
		paths.DaemonBinary,
		filepath.Join(paths.DataDir, "verified"),
		filepath.Join(paths.DataDir, "cache"),
		filepath.Join(paths.StateDir, "microvmd.pid"),
		filepath.Join(paths.StateDir, "microvmd.process.json"),
		paths.ConfigFile,
		paths.Socket,
	} {
		if _, err := os.Lstat(path); err == nil {
			return false, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("inspect existing microvmd runtime state: %w", err)
		}
	}
	entries, err := os.ReadDir(paths.StateDir)
	if err != nil {
		return false, fmt.Errorf("inspect microvmd state directory: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() != "manager.lock" {
			return false, nil
		}
	}
	entries, err = os.ReadDir(paths.RuntimeDir)
	if err != nil {
		return false, fmt.Errorf("inspect microvmd runtime directory: %w", err)
	}
	return len(entries) == 0, nil
}

// Generation is one exact owner-scoped daemon generation.
type Generation struct {
	SessionID, EnvironmentID, Ref, WorktreePath, State, Error string
	Generation                                                uint32
	Health                                                    microvmclient.GenerationHealth
}

// StatusRequest selects one bounded daemon inventory page.
type StatusRequest struct {
	PageSize     int
	Continuation string
}

// Status reports manager state without starting or recreating anything.
type Status struct {
	Configured, Running bool
	Socket              string
	GuestEgress         string
	Generations         []Generation
	Continuation        string
}

// Status inspects daemon configuration, health, and one owner inventory page without mutation.
func (m *Manager) Status(ctx context.Context, requests ...StatusRequest) (Status, error) {
	if len(requests) > 1 {
		return Status{}, errors.New("microVM status accepts one page request")
	}
	request := StatusRequest{PageSize: 50}
	if len(requests) == 1 {
		request = requests[0]
	}
	if request.PageSize <= 0 {
		request.PageSize = 50
	}
	if request.PageSize > 64 {
		request.PageSize = 64
	}
	configured := regularFile(m.paths.ConfigFile)
	running, err := m.ops.Running(ctx, m.paths)
	status := Status{Configured: configured, Running: running, Socket: m.paths.Socket}
	if err != nil {
		return status, fmt.Errorf("inspect microVM daemon state: %w", err)
	}
	if !configured {
		if running {
			return status, errors.New("microvmd is serving without manager-owned configuration")
		}
		return status, nil
	}
	selection, err := readGuestEgressPolicy(m.paths.ConfigFile)
	if err != nil {
		return status, fmt.Errorf("inspect configured microVM policy: %w", err)
	}
	status.GuestEgress = GuestEgressSummary(selection)
	if _, err := expectedDaemonInfo(m.paths); err != nil {
		return status, fmt.Errorf("inspect configured microVM identity: %w", err)
	}
	if !running {
		return status, errors.New("configured microvmd is not serving")
	}
	if _, err := m.ops.Doctor(ctx, m.paths); err != nil {
		return status, fmt.Errorf("inspect serving microVM health and identity: %w", err)
	}
	if m.lifecycle == nil {
		return status, nil
	}
	page, err := m.lifecycle.Inventory(ctx, "local", microvmclient.InventoryRequest{PageSize: request.PageSize, Continuation: request.Continuation})
	if err != nil {
		return status, fmt.Errorf("inspect microVM inventory: %w", err)
	}
	status.Continuation = page.Continuation
	status.Generations = make([]Generation, len(page.Entries))
	for i, entry := range page.Entries {
		status.Generations[i] = Generation{SessionID: entry.SessionID, EnvironmentID: entry.EnvironmentID, Ref: entry.Ref, Generation: entry.Generation, WorktreePath: entry.WorktreePath, State: entry.State, Health: entry.Health, Error: entry.Error}
	}
	return status, nil
}

// Doctor reports host preflight and backend health without downloading,
// installing, starting, or changing configuration.
func (m *Manager) Doctor(ctx context.Context) (string, error) {
	if m.ops == nil {
		return "scope: current OS principal on this execution host\nhost preflight: failed: manager operations are not configured\nbackend: not checked\n", errors.New("microVM manager operations are not configured")
	}

	var report strings.Builder
	_, _ = fmt.Fprintln(&report, "scope: current OS principal on this execution host")
	var failures []error
	preflightErr := m.ops.Preflight(ctx, m.paths)
	if preflightErr != nil {
		_, _ = fmt.Fprintf(&report, "host preflight: failed: %v\n", preflightErr)
		failures = append(failures, fmt.Errorf("microVM preflight: %w", preflightErr))
	} else {
		_, _ = fmt.Fprintln(&report, "host preflight: passed")
	}

	configured := regularFile(m.paths.ConfigFile)
	running, runningErr := m.ops.Running(ctx, m.paths)
	switch {
	case runningErr != nil:
		_, _ = fmt.Fprintf(&report, "backend: unhealthy: inspect daemon state: %v\n", runningErr)
		failures = append(failures, fmt.Errorf("inspect microVM daemon state: %w", runningErr))
	case !configured && running:
		_, _ = fmt.Fprintln(&report, "backend: unhealthy: daemon is serving without manager-owned configuration")
		failures = append(failures, errors.New("orphaned microVM daemon runtime"))
	case !configured:
		_, _ = fmt.Fprintln(&report, "backend: ready to configure on first use")
	case configured:
		if _, err := expectedDaemonInfo(m.paths); err != nil {
			_, _ = fmt.Fprintf(&report, "backend: unhealthy: configured state is invalid: %v\n", err)
			failures = append(failures, fmt.Errorf("microVM configured state is invalid: %w", err))
			break
		}
		if !running {
			_, _ = fmt.Fprintln(&report, "backend: configured; daemon not running")
			failures = append(failures, errors.New("microVM daemon is not running"))
			break
		}
		daemonReport, err := m.ops.Doctor(ctx, m.paths)
		if err != nil {
			_, _ = fmt.Fprintf(&report, "backend: unhealthy: %v\n", err)
			failures = append(failures, fmt.Errorf("microVM backend is unhealthy: %w", err))
		} else {
			_, _ = fmt.Fprintln(&report, "backend: healthy")
		}
		if daemonReport != "" {
			_, _ = io.WriteString(&report, daemonReport)
			if !strings.HasSuffix(daemonReport, "\n") {
				_, _ = fmt.Fprintln(&report)
			}
		}
	}

	if preflightErr != nil {
		_, _ = fmt.Fprintln(&report, "next: fix the failed host prerequisite, then rerun doctor")
	} else if !configured && !running && runningErr == nil {
		_, _ = fmt.Fprintln(&report, "next: select microvm-local to configure the backend on first use:")
	} else if len(failures) > 0 {
		_, _ = fmt.Fprintln(&report, "next: inspect and repair the existing local daemon state; ordinary use will not replace or restart it automatically")
	} else {
		_, _ = fmt.Fprintln(&report, "next: select microvm-local as the deployment default:")
	}
	_, _ = fmt.Fprintln(&report, "  embedded mecatui operator settings:")
	_, _ = fmt.Fprintln(&report, "    execution:")
	_, _ = fmt.Fprintln(&report, "      default_placement: microvm-local")
	_, _ = fmt.Fprintln(&report, "  guest IPv4 egress defaults to permissive; before first use set execution.microvm.guest_egress.mode to deny-all or allowlist")
	_, _ = fmt.Fprintln(&report, `  mecated serve --headless --default-placement microvm-local; then POST /v1/sessions with {}`)
	_, _ = fmt.Fprintln(&report, "doctor is read-only; it never downloads, installs, or starts microvmd")
	return report.String(), errors.Join(failures...)
}

// DeleteRequest identifies one exact local-owner generation for permanent deletion.
type DeleteRequest struct {
	SessionID  string
	Ref        string
	Generation uint32
}

// DeleteResult reports clean worktree removal or dirty retention.
type DeleteResult struct {
	WorktreePath     string
	WorktreeRetained bool
}

// ValidateDeleteRequest checks the complete generation selector before any
// destructive confirmation is shown.
func ValidateDeleteRequest(request DeleteRequest) error {
	at := strings.LastIndexByte(request.Ref, '@')
	if request.SessionID == "" || at <= 0 || request.Generation == 0 || request.Ref[at+1:] != strconv.FormatUint(uint64(request.Generation), 10) {
		return errors.New("microVM delete requires matching --session, --ref, and --generation")
	}
	return nil
}

// Delete permanently destroys one exact generation. It never stops the daemon or
// removes verified installation state shared by other sessions.
func (m *Manager) Delete(ctx context.Context, request DeleteRequest) (DeleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lifecycle == nil {
		return DeleteResult{}, errors.New("microVM lifecycle client is not configured")
	}
	if err := ValidateDeleteRequest(request); err != nil {
		return DeleteResult{}, err
	}
	at := strings.LastIndexByte(request.Ref, '@')
	claim := microvmclient.GenerationBinding{
		Owner: "local", SessionID: request.SessionID, EnvironmentID: request.Ref[:at], Ref: request.Ref, Generation: request.Generation,
	}
	result, err := m.lifecycle.DeleteGeneration(ctx, claim)
	if err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{WorktreePath: result.WorktreePath, WorktreeRetained: result.WorktreeRetained}, nil
}

// HostFallbackEnabled is deliberately false: manager failures never redirect a
// selected microVM session to host filesystem or shell execution.
func (*Manager) HostFallbackEnabled() bool { return false }

func validateReleasePolicy(release Release, policy Policy) error { //nolint:gocyclo // explicit closed policy validation
	remote := strings.HasPrefix(release.URL, "https://") && release.bundlePath == ""
	localDevelopment := release.URL == "" && filepath.IsAbs(release.bundlePath)
	if (!remote && !localDevelopment) || len(release.SHA256) != 64 || strings.Trim(release.SHA256, "0123456789abcdef") != "" {
		return errors.New("release requires exactly one authenticated source and a lowercase SHA-256 digest")
	}
	if policy.PolicyRevision == "" {
		return errors.New("policy requires a revision")
	}
	if policy.GuestEgressMode != "permissive" && policy.GuestEgressMode != "deny-all" && policy.GuestEgressMode != "allowlist" {
		return errors.New("policy requires permissive, deny-all, or allowlist guest egress")
	}
	if ((policy.GuestEgressMode == "permissive" || policy.GuestEgressMode == "deny-all") && len(policy.GuestAllow) != 0) || (policy.GuestEgressMode == "allowlist" && len(policy.GuestAllow) == 0) {
		return errors.New("guest egress mode and allowlist disagree")
	}
	seenRules := make(map[EgressRule]struct{}, len(policy.GuestAllow))
	for _, rule := range policy.GuestAllow {
		if err := validateEgressRule(rule); err != nil {
			return errors.New("guest egress allowlist contains an invalid destination")
		}
		if rule.Hostname != strings.ToLower(strings.TrimSuffix(rule.Hostname, ".")) {
			return errors.New("guest egress allowlist contains a non-canonical hostname")
		}
		if _, ok := seenRules[rule]; ok {
			return errors.New("guest egress allowlist contains a duplicate destination")
		}
		seenRules[rule] = struct{}{}
	}
	for _, kind := range []string{"runtime", "firmware", "execution-image", "guest-agent"} {
		if policy.RequiredAttestations[kind] == "" {
			return fmt.Errorf("policy omitted required %s attestation", kind)
		}
	}
	keyless := policy.CertificateIdentity != "" || policy.OIDCIssuer != ""
	keyed := policy.PublicKey != "" || policy.PublicKeyIdentity != ""
	if keyless == keyed || (keyless && (policy.CertificateIdentity == "" || policy.OIDCIssuer == "")) || (keyed && (!filepath.IsAbs(policy.PublicKey) || !strings.HasPrefix(policy.PublicKeyIdentity, "sha256:"))) {
		return errors.New("policy requires exactly one complete Sigstore trust mode")
	}
	return nil
}

func validateInstalled(installed InstalledArtifacts, installRoot string) error {
	seen := map[string]bool{}
	for _, artifact := range installed.Artifacts {
		if seen[artifact.Kind] {
			return errors.New("installer returned duplicate artifact kind")
		}
		if err := validateInstalledArtifact(artifact, installRoot); err != nil {
			return err
		}
		seen[artifact.Kind] = true
	}
	for _, kind := range []string{"runtime", "firmware", "execution-image", "guest-agent"} {
		if !seen[kind] {
			return fmt.Errorf("installer omitted %s artifact", kind)
		}
	}
	return nil
}

func validateInstalledArtifact(artifact Artifact, installRoot string) error { //nolint:gocyclo // one fail-closed validation chain keeps artifact and OCI-lineage checks atomic
	isOCI := artifact.Kind == "execution-image" && artifact.ManifestDigest != ""
	if (!isOCI && !pathWithin(installRoot, artifact.Path)) || (isOCI && artifact.Path != "") {
		return errors.New("installer returned invalid or out-of-root artifact paths")
	}
	if artifact.Provenance == "" || !pathWithin(installRoot, artifact.Provenance) || artifact.SigstoreBundle == "" || !pathWithin(installRoot, artifact.SigstoreBundle) {
		return errors.New("installer returned out-of-root artifact evidence")
	}
	if !validSHA256(artifact.Digest) {
		return errors.New("installer returned incomplete immutable artifact evidence")
	}
	if isOCI {
		if !validSHA256(artifact.ManifestDigest) || !strings.HasSuffix(artifact.Reference, "@"+artifact.ManifestDigest) || !canonicalOCIReference(artifact.Reference) ||
			artifact.DiscoveryReference != "ghcr.io/stacklok/brood-box/base:latest" || !validSHA256(artifact.ResolutionEvidence) ||
			(artifact.Platform != "linux/amd64" && artifact.Platform != "linux/arm64") {
			return errors.New("installer returned mutable or inconsistent Brood execution image resolution")
		}
		return nil
	}
	if artifact.ManifestDigest != "" || artifact.DiscoveryReference != "" || artifact.ResolutionEvidence != "" || artifact.Platform != "" || !strings.HasSuffix(artifact.Reference, "@"+artifact.Digest) {
		return errors.New("installer returned incomplete immutable artifact evidence")
	}
	return nil
}

func validSHA256(value string) bool {
	digestHex := strings.TrimPrefix(value, "sha256:")
	return len(digestHex) == 64 && strings.Trim(digestHex, "0123456789abcdef") == "" && value == "sha256:"+digestHex
}

func canonicalOCIReference(value string) bool {
	at := strings.LastIndexByte(value, '@')
	if at <= 0 || strings.Count(value, "@") != 1 || strings.ToLower(value[:at]) != value[:at] {
		return false
	}
	repo := value[:at]
	lastSlash := strings.LastIndexByte(repo, '/')
	return strings.Contains(repo, "/") && !strings.ContainsAny(repo, " \t\r\n") && !strings.Contains(repo[lastSlash+1:], ":")
}

func pathWithin(root, path string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeDaemonConfig(path string, paths Paths, release Release, policy Policy, installed InstalledArtifacts) error {
	binaryIdentity, err := fileSHA256(paths.DaemonBinary)
	if err != nil {
		return fmt.Errorf("identify installed microvmd binary: %w", err)
	}
	artifacts := make([]map[string]string, 0, len(installed.Artifacts))
	for _, a := range installed.Artifacts {
		artifacts = append(artifacts, map[string]string{
			"kind": a.Kind, "reference": a.Reference, "digest": a.Digest, "manifest_digest": a.ManifestDigest,
			"discovery_reference": a.DiscoveryReference, "resolution_evidence": a.ResolutionEvidence, "platform": a.Platform,
			"path": a.Path, "provenance": a.Provenance, "sigstore_bundle": a.SigstoreBundle,
		})
	}
	cfg := map[string]any{
		"release_identity": "sha256:" + release.SHA256, "binary_identity": binaryIdentity,
		"policy_revision": policy.PolicyRevision, "artifact_cache": filepath.Join(paths.DataDir, "cache"), "runtime_dir": filepath.Join(paths.RuntimeDir, "generations"),
		"required_attestations": policy.RequiredAttestations, "artifacts": artifacts,
		"guest_egress": map[string]any{"Mode": policy.GuestEgressMode, "Allow": policy.GuestAllow}, "admission": policy.Admission,
		"profiles": map[string]any{Alias: map[string]any{"resources": policy.Resources}},
	}
	if policy.CertificateIdentity != "" {
		cfg["certificate_identity"], cfg["oidc_issuer"] = policy.CertificateIdentity, policy.OIDCIssuer
	} else {
		cfg["public_key"], cfg["public_key_identity"] = policy.PublicKey, policy.PublicKeyIdentity
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'))
}

func expectedDaemonInfo(paths Paths) (DaemonInfo, error) {
	data, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		return DaemonInfo{}, err
	}
	var cfg struct {
		ReleaseIdentity string                     `json:"release_identity"`
		BinaryIdentity  string                     `json:"binary_identity"`
		PolicyRevision  string                     `json:"policy_revision"`
		Profiles        map[string]json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DaemonInfo{}, fmt.Errorf("read expected microvmd identity: %w", err)
	}
	profiles := make([]string, 0, len(cfg.Profiles))
	for profile := range cfg.Profiles {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	binaryIdentity, err := fileSHA256(paths.DaemonBinary)
	if err != nil {
		return DaemonInfo{}, err
	}
	if cfg.BinaryIdentity != binaryIdentity {
		return DaemonInfo{}, errors.New("installed microvmd binary changed after config was written")
	}
	return DaemonInfo{ProtocolVersion: microvmclient.DaemonProtocolVersion, ReleaseIdentity: cfg.ReleaseIdentity, BinaryIdentity: binaryIdentity, ConfigDigest: bytesSHA256(data), PolicyRevision: cfg.PolicyRevision, Profiles: profiles, Socket: paths.Socket}, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- manager-owned absolute executable path.
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

func bytesSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func preparePaths(paths Paths) error {
	if err := validatePaths(paths); err != nil {
		return err
	}
	for _, dir := range []string{paths.StateDir, paths.RuntimeDir, paths.DataDir, filepath.Dir(paths.ConfigFile), filepath.Dir(paths.DaemonBinary)} {
		if err := secureMkdirAll(dir); err != nil {
			return err
		}
	}
	for _, file := range []string{paths.ConfigFile, paths.UserSettings} {
		if err := refuseSymlink(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func validatePaths(paths Paths) error {
	for _, path := range []string{paths.StateDir, paths.RuntimeDir, paths.Socket, paths.DataDir, paths.ConfigFile, paths.UserSettings, paths.DaemonBinary} {
		if !filepath.IsAbs(path) {
			return errors.New("all microVM manager paths must be absolute")
		}
	}
	if len(paths.Socket) >= DarwinSocketPathLimit {
		return errors.New("microvmd socket path exceeds Darwin-safe bound")
	}
	return nil
}

func lockManager(stateDir string) (func(), error) {
	info, err := os.Lstat(stateDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("microVM manager state root is not a directory")
	}
	lock, err := os.OpenFile(filepath.Join(stateDir, "manager.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

func secureMkdirAll(path string) error {
	if err := refuseSymlinkAncestors(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- this is an owner-only directory, not a file.
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing non-directory or symlink path %s", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("refusing directory not owned by current user: %s", path)
	}
	return nil
}

func refuseSymlinkAncestors(path string) error {
	clean := filepath.Clean(path)
	for current := clean; current != filepath.Dir(current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink path %s", current)
		}
	}
	return nil
}

func refuseSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink path %s", path)
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	return atomicWriteMode(path, data, 0o600)
}

func atomicWriteMode(path string, data []byte, mode fs.FileMode) error {
	if err := refuseSymlinkAncestors(path); err != nil {
		return err
	}
	if err := secureMkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	if err := refuseSymlink(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".microvm-manager-*")
	if err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}
