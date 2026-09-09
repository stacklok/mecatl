package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm"
)

func TestDaemonArtifactExtractionIgnoresCallerUmask(t *testing.T) {
	const helperPathEnv = "MECATL_TEST_DAEMON_UMASK_PATH"
	if path := os.Getenv(helperPathEnv); path != "" {
		_ = syscall.Umask(0o077)
		setDeterministicArtifactUmask()
		if err := os.WriteFile(path, []byte("artifact"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	path := filepath.Join(t.TempDir(), "artifact")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonArtifactExtractionIgnoresCallerUmask$")
	cmd.Env = append(os.Environ(), helperPathEnv+"="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("umask helper: %v: %s", err, output)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("artifact mode = %#o, want 0644", info.Mode().Perm())
	}
}

func TestHypervisorAccessReportsEffectiveIdentity(t *testing.T) {
	t.Parallel()
	err := checkLinuxKVMAccess(func(string, int, os.FileMode) (*os.File, error) {
		return nil, os.ErrPermission
	}, 1001, 65534)
	if err == nil || !strings.Contains(err.Error(), "uid=1001 euid=65534") || !strings.Contains(err.Error(), "O_RDWR") {
		t.Fatalf("KVM access error did not identify the effective opener: %v", err)
	}
}

func TestDoctorRunsProductionReadinessProbes(t *testing.T) {
	t.Parallel()
	calls := make(map[microvm.ReadinessCheck]int)
	var artifactCalls atomic.Int32
	checker := &doctorChecker{
		artifacts:        doctorArtifactVerifier{calls: &artifactCalls, err: errors.New("corrupt signature evidence")},
		hypervisorProbe:  func(context.Context) error { calls[microvm.CheckHypervisor]++; return nil },
		controlPeerProbe: func(context.Context) error { calls[microvm.CheckControlSocket]++; return nil },
		networkProbe: func(context.Context) error {
			calls[microvm.CheckNetwork]++
			return errors.New("hosted provider unavailable")
		},
		profileProbe: func(context.Context) ([]string, error) {
			calls[microvm.CheckProfiles]++
			return nil, errors.New("profile policy inconsistent")
		},
		staleProbe: func(context.Context) (int, error) { calls[microvm.CheckStaleResources]++; return 1, nil },
	}
	report := microvm.NewDoctor(checker).Run(context.Background())
	if report.Ready() {
		t.Fatal("doctor accepted corrupt evidence, unavailable network, and inconsistent profiles")
	}
	for _, check := range []microvm.ReadinessCheck{microvm.CheckHypervisor, microvm.CheckControlSocket, microvm.CheckNetwork, microvm.CheckProfiles, microvm.CheckStaleResources} {
		if calls[check] != 1 {
			t.Fatalf("probe %s calls = %d, want 1", check, calls[check])
		}
	}
	if artifactCalls.Load() != 1 {
		t.Fatalf("artifact verifier calls = %d, want one complete-set verification", artifactCalls.Load())
	}
	text := report.String()
	for _, want := range []string{"corrupt signature evidence", "hosted provider unavailable", "profile policy inconsistent", "stale resources", "remediation:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestDoctorArtifactVerificationSharedAcrossConcurrentReadinessChecks(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	verificationErr := errors.New("shared artifact verification failure")
	checker := doctorChecker{artifacts: doctorArtifactVerifier{calls: &calls, err: verificationErr}}

	start := make(chan struct{})
	results := make(chan error, 2)
	var checks sync.WaitGroup
	for _, check := range []microvm.ReadinessCheck{microvm.CheckRuntime, microvm.CheckFirmware} {
		checks.Add(1)
		go func() {
			defer checks.Done()
			<-start
			results <- checker.Check(context.Background(), check)
		}()
	}
	close(start)
	checks.Wait()
	close(results)

	if calls.Load() != 1 {
		t.Fatalf("artifact verifier calls = %d, want 1", calls.Load())
	}
	for err := range results {
		if !errors.Is(err, verificationErr) {
			t.Fatalf("readiness check error = %v, want shared %v", err, verificationErr)
		}
	}
}

func TestDoctorDoesNotConsultSupersededLegacyRegistry(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	registry, err := microvm.OpenFileRegistry(filepath.Join(stateDir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	record := microvm.EnvironmentRecord{
		State: microvm.EnvironmentReady, EnvironmentID: "legacy-only", Generation: 1,
		Ref:  microvm.EnvironmentRef{Kind: microvm.Kind, ID: "legacy-only@1"},
		VMID: "vm-legacy", Endpoint: filepath.Join(stateDir, "missing.sock"),
	}
	if err := registry.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	stale, err := (&doctorChecker{stateDir: stateDir}).StaleResources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("legacy-only stale generations = %d, want ignored", stale)
	}
}

func TestDoctorProcessStartIdentityDistinguishesHealthyAndReusedPID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	identity, err := microvm.ProcessStartIdentity(ctx, os.Getpid())
	if err != nil {
		t.Fatalf("read current process identity: %v", err)
	}
	endpoint := shortPrivateMicrovmdSocketPath(t, "guest.sock")
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	record := microvm.EnvironmentRecord{
		State: microvm.EnvironmentReady, EnvironmentID: "healthy", Generation: 1,
		Ref: microvm.EnvironmentRef{Kind: microvm.Kind, ID: "healthy@1"}, VMID: "vm-healthy",
		Endpoint: endpoint, RunnerPID: os.Getpid(), ProcessIdentity: identity,
	}
	if staleReadyGeneration(ctx, record) {
		t.Fatal("doctor marked a healthy real process generation stale")
	}
	record.ProcessIdentity = identity + "-reused"
	if !staleReadyGeneration(ctx, record) {
		t.Fatal("doctor accepted a reused PID with a stale process-start identity")
	}
}

func TestInvariant_microvmd_execution_image_requires_exact_brood_resolution(t *testing.T) {
	t.Parallel()
	manifest := "sha256:" + strings.Repeat("a", 64)
	valid := artifactConfig{
		Kind: microvm.ArtifactExecutionImage, Reference: "ghcr.io/stacklok/brood-box/base@" + manifest,
		Digest: "sha256:" + strings.Repeat("c", 64), ManifestDigest: manifest,
		DiscoveryReference: "ghcr.io/stacklok/brood-box/base:latest",
		ResolutionEvidence: "sha256:" + strings.Repeat("d", 64), Platform: "linux/amd64",
		Provenance: "/evidence/statement", SigstoreBundle: "/evidence/bundle",
	}
	if isOCI, err := validateArtifactConfig(valid); err != nil || !isOCI {
		t.Fatalf("exact Brood resolution rejected: isOCI=%v err=%v", isOCI, err)
	}
	for name, mutate := range map[string]func(*artifactConfig){
		"missing":        func(config *artifactConfig) { config.ResolutionEvidence = "" },
		"wrong platform": func(config *artifactConfig) { config.Platform = "darwin/arm64" },
		"wrong source":   func(config *artifactConfig) { config.DiscoveryReference = "ghcr.io/attacker/base:latest" },
	} {
		t.Run(name, func(t *testing.T) {
			entry := valid
			mutate(&entry)
			if _, err := validateArtifactConfig(entry); err == nil {
				t.Fatal("invalid Brood resolution was admitted")
			}
		})
	}
}

func TestDoctorRejectsInconsistentDaemonProfile(t *testing.T) {
	t.Parallel()
	profiles, err := (&doctorChecker{cfg: daemonConfig{PolicyRevision: "policy-v1"}}).Profiles(context.Background())
	if err == nil || len(profiles) != 0 || !strings.Contains(err.Error(), "trust policy") {
		t.Fatalf("inconsistent profile result = profiles:%v err:%v", profiles, err)
	}
}

func TestDoctorAcceptsFinalSigstorePolicyAndDaemonProfiles(t *testing.T) {
	t.Parallel()
	attestations := map[microvm.ArtifactKind]string{}
	artifacts := make([]artifactConfig, 0, 4)
	for _, kind := range []microvm.ArtifactKind{microvm.ArtifactRuntime, microvm.ArtifactFirmware, microvm.ArtifactExecutionImage, microvm.ArtifactGuestAgent} {
		attestations[kind] = "https://slsa.dev/provenance/v1"
		artifact := artifactConfig{
			Kind: kind, Reference: "registry.example/" + string(kind), Digest: "sha256:pinned",
			Provenance: "/evidence/" + string(kind) + ".intoto.jsonl", SigstoreBundle: "/evidence/" + string(kind) + ".sigstore.json",
		}
		if kind == microvm.ArtifactExecutionImage {
			artifact.ManifestDigest = "sha256:" + strings.Repeat("a", 64)
			artifact.Reference = "ghcr.io/stacklok/brood-box/base@" + artifact.ManifestDigest
			artifact.DiscoveryReference = "ghcr.io/stacklok/brood-box/base:latest"
			artifact.ResolutionEvidence = "sha256:" + strings.Repeat("d", 64)
			artifact.Platform = "linux/amd64"
		}
		artifacts = append(artifacts, artifact)
	}
	checker := doctorChecker{cfg: daemonConfig{
		PolicyRevision:      "policy-v1",
		CertificateIdentity: "https://github.com/stacklok/mecatl/.github/workflows/release.yml@refs/tags/v1",
		OIDCIssuer:          "https://token.actions.githubusercontent.com", Attestations: attestations, Artifacts: artifacts,
		Profiles: map[string]profileConfig{"locked-down": {Resources: map[string]string{"cpus": "2", "memory": "4GiB"}}},
	}}
	profiles, err := checker.Profiles(context.Background())
	if err != nil {
		t.Fatalf("final daemon policy rejected: %v", err)
	}
	if len(profiles) != 1 || profiles[0] != "locked-down" {
		t.Fatalf("profiles = %v, want daemon-owned locked-down alias", profiles)
	}
}

func TestDoctorAcceptsPinnedPublicKeyPolicy(t *testing.T) {
	t.Parallel()
	attestations := map[microvm.ArtifactKind]string{}
	artifacts := make([]artifactConfig, 0, 4)
	for _, kind := range []microvm.ArtifactKind{microvm.ArtifactRuntime, microvm.ArtifactFirmware, microvm.ArtifactExecutionImage, microvm.ArtifactGuestAgent} {
		attestations[kind] = "https://slsa.dev/provenance/v1"
		artifact := artifactConfig{Kind: kind, Reference: string(kind) + "@sha256:pinned", Digest: "sha256:pinned", Provenance: "/evidence/statement", SigstoreBundle: "/evidence/bundle"}
		if kind == microvm.ArtifactExecutionImage {
			artifact.ManifestDigest = "sha256:" + strings.Repeat("a", 64)
			artifact.Reference = "ghcr.io/stacklok/brood-box/base@" + artifact.ManifestDigest
			artifact.DiscoveryReference = "ghcr.io/stacklok/brood-box/base:latest"
			artifact.ResolutionEvidence = "sha256:" + strings.Repeat("d", 64)
			artifact.Platform = "linux/amd64"
		}
		artifacts = append(artifacts, artifact)
	}
	checker := doctorChecker{cfg: daemonConfig{
		PolicyRevision: "local-e2e-v1",
		PublicKey:      "/private/e2e/cosign.pub", PublicKeyIdentity: microvm.PublicKeyIdentity([]byte("public-key")),
		Attestations: attestations, Artifacts: artifacts,
		Profiles: map[string]profileConfig{"locked-down": {Resources: map[string]string{"cpus": "1", "memory": "256MiB"}}},
	}}
	if profiles, err := checker.Profiles(context.Background()); err != nil || len(profiles) != 1 {
		t.Fatalf("public-key policy profiles = %v, %v", profiles, err)
	}

	checker.cfg.CertificateIdentity = "unexpected-keyless-identity"
	checker.cfg.OIDCIssuer = "https://issuer.example"
	if _, err := checker.Profiles(context.Background()); err == nil {
		t.Fatal("ambiguous public-key and keyless policy was accepted")
	}
}

func TestDoctorHealthyConfigurationPasses(t *testing.T) {
	t.Parallel()
	report := microvm.NewDoctor(readinessCheckerWithHealthyDefaults(&doctorChecker{})).Run(context.Background())
	if !report.Ready() {
		t.Fatalf("healthy doctor failed:\n%s", report.String())
	}
}

type doctorArtifactVerifier struct {
	calls *atomic.Int32
	err   error
}

func (v doctorArtifactVerifier) Verify(context.Context, map[microvm.ArtifactKind]microvm.ArtifactRequest) (microvm.VerifiedArtifacts, string, error) {
	v.calls.Add(1)
	return microvm.VerifiedArtifacts{}, "", v.err
}

func readinessCheckerWithHealthyDefaults(checker *doctorChecker) *doctorChecker {
	if checker.artifactProbe == nil {
		checker.artifactProbe = func(context.Context, microvm.ReadinessCheck) error { return nil }
	}
	if checker.hypervisorProbe == nil {
		checker.hypervisorProbe = func(context.Context) error { return nil }
	}
	if checker.controlPeerProbe == nil {
		checker.controlPeerProbe = func(context.Context) error { return nil }
	}
	if checker.networkProbe == nil {
		checker.networkProbe = func(context.Context) error { return nil }
	}
	if checker.profileProbe == nil {
		checker.profileProbe = func(context.Context) ([]string, error) { return []string{"locked-down"}, nil }
	}
	if checker.staleProbe == nil {
		checker.staleProbe = func(context.Context) (int, error) { return 0, nil }
	}
	return checker
}
