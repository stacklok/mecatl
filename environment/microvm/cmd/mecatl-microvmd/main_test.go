package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stacklok/go-microvm/extract"

	"github.com/stacklok/mecatl/environment/microvm"
)

func TestStrictDaemonDecoderAcceptsManagerConfigContracts(t *testing.T) {
	for _, name := range []string{"daemon-config-keyless.json", "daemon-config-development.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "..", "internal", "adapter", "microvmmanager", "testdata", name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := decodeDaemonConfig(data)
			if err != nil {
				t.Fatalf("production strict decoder rejected manager config contract: %v", err)
			}
			if len(cfg.Profiles) != 1 || cfg.Profiles["microvm-local"] != (profileConfig{}) {
				t.Fatalf("decoded profiles = %#v", cfg.Profiles)
			}
		})
	}
}

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
	}
	report := microvm.NewDoctor(checker).Run(context.Background())
	if report.Ready() {
		t.Fatal("doctor accepted corrupt evidence, unavailable network, and inconsistent profiles")
	}
	for _, check := range []microvm.ReadinessCheck{microvm.CheckHypervisor, microvm.CheckControlSocket, microvm.CheckNetwork, microvm.CheckProfiles} {
		if calls[check] != 1 {
			t.Fatalf("probe %s calls = %d, want 1", check, calls[check])
		}
	}
	if artifactCalls.Load() != 1 {
		t.Fatalf("artifact verifier calls = %d, want one complete-set verification", artifactCalls.Load())
	}
	text := report.String()
	for _, want := range []string{"corrupt signature evidence", "hosted provider unavailable", "profile policy inconsistent", "remediation:"} {
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
		Profiles: map[string]profileConfig{"locked-down": {}},
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
		Profiles: map[string]profileConfig{"locked-down": {}},
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

func TestRepositoryArtifactSnapshotFactoryUsesLockedValidatedView(t *testing.T) {
	verified := microvm.VerifiedArtifacts{Runtime: microvm.VerifiedArtifact{Path: "/verified/runtime"}}
	locked := microvm.VerifiedArtifacts{Runtime: microvm.VerifiedArtifact{Path: "/snapshot/runtime"}}
	releases := 0
	verifier := &recordingRepositoryArtifactVerifier{verified: verified, locked: locked, release: func() { releases++ }}
	requests := map[microvm.ArtifactKind]microvm.ArtifactRequest{microvm.ArtifactRuntime: {Kind: microvm.ArtifactRuntime}}

	got, release, err := repositoryArtifactSnapshot(verifier, requests)(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime.Path != locked.Runtime.Path || verifier.verifyCalls != 1 || verifier.lockCalls != 1 || release == nil {
		t.Fatalf("snapshot factory = got:%+v verify:%d lock:%d release:%v", got, verifier.verifyCalls, verifier.lockCalls, release != nil)
	}
	release()
	if releases != 1 {
		t.Fatalf("snapshot release calls = %d, want 1", releases)
	}
}

func TestRepositoryArtifactSnapshotFactoryWithProductionProvisioner(t *testing.T) {
	t.Run("mutation between verify and lock fails before boot", func(t *testing.T) {
		root := t.TempDir()
		requests, resolver, policy := repositoryArtifactFixture(t, root)
		provisioner := microvm.NewProvisioner(microvm.NewVerifiedCache(filepath.Join(root, "cache")), resolver, policy, nil, nil)
		verifier := &mutatingRepositoryArtifactVerifier{Provisioner: provisioner}
		runtime := &repositoryFactoryRuntime{}
		repository := repositoryFactoryGitFixture(t, root)
		registry, err := microvm.OpenRepositoryVMRegistry(filepath.Join(root, "state"), runtime)
		if err != nil {
			t.Fatal(err)
		}
		_, err = registry.Ensure(t.Context(), microvm.RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: repositoryArtifactSnapshot(verifier, requests)})
		if !errors.Is(err, microvm.ErrCorruptCacheEntry) || runtime.starts != 0 {
			t.Fatalf("mutated verified cache Ensure = %v, starts = %d; want fail-closed before boot", err, runtime.starts)
		}
	})

	t.Run("snapshot remains private through consumption and is released on failure", func(t *testing.T) {
		root := t.TempDir()
		requests, resolver, policy := repositoryArtifactFixture(t, root)
		provisioner := microvm.NewProvisioner(microvm.NewVerifiedCache(filepath.Join(root, "cache")), resolver, policy, nil, nil)
		runtime := &repositoryFactoryRuntime{startErr: errors.New("controlled post-acquire boot failure")}
		repository := repositoryFactoryGitFixture(t, root)
		registry, err := microvm.OpenRepositoryVMRegistry(filepath.Join(root, "state"), runtime)
		if err != nil {
			t.Fatal(err)
		}
		_, err = registry.Ensure(t.Context(), microvm.RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: repositoryArtifactSnapshot(provisioner, requests)})
		if !errors.Is(err, runtime.startErr) || runtime.starts != 1 {
			t.Fatalf("post-acquire failure = %v, starts = %d", err, runtime.starts)
		}
		for _, path := range runtime.snapshotPaths {
			if !strings.Contains(path, string(filepath.Separator)+"staging"+string(filepath.Separator)+"launch-") {
				t.Fatalf("runtime consumed non-private artifact path %q", path)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed generation retained private snapshot %q: %v", path, statErr)
			}
		}
	})

	t.Run("healthy generation reuses without reacquiring artifacts", func(t *testing.T) {
		root := t.TempDir()
		requests, resolver, policy := repositoryArtifactFixture(t, root)
		provisioner := microvm.NewProvisioner(microvm.NewVerifiedCache(filepath.Join(root, "cache")), resolver, policy, nil, nil)
		runtime := &repositoryFactoryRuntime{}
		repository := repositoryFactoryGitFixture(t, root)
		registry, err := microvm.OpenRepositoryVMRegistry(filepath.Join(root, "state"), runtime)
		if err != nil {
			t.Fatal(err)
		}
		factory := repositoryArtifactSnapshot(provisioner, requests)
		if _, err := registry.Ensure(t.Context(), microvm.RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: factory}); err != nil {
			t.Fatal(err)
		}
		callsAfterBoot := resolver.calls.Load()
		if _, err := registry.Ensure(t.Context(), microvm.RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: factory}); err != nil {
			t.Fatal(err)
		}
		if runtime.starts != 1 || resolver.calls.Load() != callsAfterBoot {
			t.Fatalf("healthy reuse starts/resolver calls = %d/%d, want 1/%d", runtime.starts, resolver.calls.Load(), callsAfterBoot)
		}
	})
}

func TestDoctorHealthyConfigurationPasses(t *testing.T) {
	t.Parallel()
	report := microvm.NewDoctor(readinessCheckerWithHealthyDefaults(&doctorChecker{})).Run(context.Background())
	if !report.Ready() {
		t.Fatalf("healthy doctor failed:\n%s", report.String())
	}
}

type recordingRepositoryArtifactVerifier struct {
	verified, locked       microvm.VerifiedArtifacts
	release                func()
	verifyCalls, lockCalls int
}

func (v *recordingRepositoryArtifactVerifier) Verify(context.Context, map[microvm.ArtifactKind]microvm.ArtifactRequest) (microvm.VerifiedArtifacts, string, error) {
	v.verifyCalls++
	return v.verified, "", nil
}

func (v *recordingRepositoryArtifactVerifier) LockAndValidate(_ context.Context, got microvm.VerifiedArtifacts) (microvm.VerifiedArtifacts, func(), error) {
	v.lockCalls++
	if got.Runtime.Path != v.verified.Runtime.Path {
		return microvm.VerifiedArtifacts{}, nil, errors.New("factory did not pass verified artifacts to snapshot lock")
	}
	return v.locked, v.release, nil
}

type mutatingRepositoryArtifactVerifier struct {
	*microvm.Provisioner
}

func (v *mutatingRepositoryArtifactVerifier) Verify(ctx context.Context, requests map[microvm.ArtifactKind]microvm.ArtifactRequest) (microvm.VerifiedArtifacts, string, error) {
	verified, revision, err := v.Provisioner.Verify(ctx, requests)
	if err == nil {
		err = os.WriteFile(filepath.Join(verified.Runtime.Path, "artifact.bin"), []byte("mutated after verification"), 0o700)
	}
	return verified, revision, err
}

type repositoryFactoryResolver struct {
	artifacts map[microvm.ArtifactKind]microvm.ResolvedArtifact
	calls     atomic.Int32
}

func (r *repositoryFactoryResolver) Resolve(_ context.Context, request microvm.ArtifactRequest) (microvm.ResolvedArtifact, error) {
	r.calls.Add(1)
	artifact, ok := r.artifacts[request.Kind]
	if !ok {
		return microvm.ResolvedArtifact{}, errors.New("artifact not found")
	}
	return artifact, nil
}

type repositoryFactorySource struct {
	content []byte
	name    string
}

var _ extract.Source = repositoryFactorySource{}

func (s repositoryFactorySource) Ensure(_ context.Context, cacheDir string) (string, error) {
	dir, err := os.MkdirTemp(cacheDir, "artifact-source-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, s.name), s.content, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

type repositoryFactoryEvidenceVerifier struct{}

func (repositoryFactoryEvidenceVerifier) Verify(_ context.Context, statement, bundle []byte, identity, issuer string) error {
	if len(statement) == 0 || string(bundle) != "test-bundle" || identity != "test-builder" || issuer != "test-issuer" {
		return errors.New("artifact evidence rejected")
	}
	return nil
}

func repositoryArtifactFixture(t *testing.T, root string) (map[microvm.ArtifactKind]microvm.ArtifactRequest, *repositoryFactoryResolver, microvm.TrustPolicy) {
	t.Helper()
	requests := make(map[microvm.ArtifactKind]microvm.ArtifactRequest)
	artifacts := make(map[microvm.ArtifactKind]microvm.ResolvedArtifact)
	for _, kind := range []microvm.ArtifactKind{microvm.ArtifactRuntime, microvm.ArtifactFirmware, microvm.ArtifactExecutionImage, microvm.ArtifactGuestAgent} {
		name := "artifact.bin"
		if kind == microvm.ArtifactGuestAgent {
			name = "mecatl-guest-agent"
		}
		source := repositoryFactorySource{content: []byte(string(kind) + " bytes"), name: name}
		materialized, err := source.Ensure(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := microvm.ArtifactTreeDigest(materialized)
		if err != nil {
			t.Fatal(err)
		}
		request := microvm.ArtifactRequest{Kind: kind, Reference: "test.example/" + string(kind) + "@" + digest, Digest: digest}
		statement := []byte(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"digest":{"sha256":"` + strings.TrimPrefix(digest, "sha256:") + `"}}],"predicateType":"test-predicate"}`)
		requests[kind] = request
		artifacts[kind] = microvm.ResolvedArtifact{Kind: kind, Digest: digest, Source: source, Evidence: microvm.VerificationEvidence{
			Bundle: []byte("test-bundle"), Attestation: microvm.Attestation{PredicateType: "test-predicate", SubjectDigest: digest, Statement: statement},
		}}
	}
	policy := microvm.TrustPolicy{
		Revision: "test-policy", CertificateIdentity: "test-builder", OIDCIssuer: "test-issuer", Verifier: repositoryFactoryEvidenceVerifier{},
		RequiredAttestations: map[microvm.ArtifactKind]string{
			microvm.ArtifactRuntime: "test-predicate", microvm.ArtifactFirmware: "test-predicate",
			microvm.ArtifactExecutionImage: "test-predicate", microvm.ArtifactGuestAgent: "test-predicate",
		},
	}
	return requests, &repositoryFactoryResolver{artifacts: artifacts}, policy
}

func repositoryFactoryGitFixture(t *testing.T, root string) string {
	t.Helper()
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = repository
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-qm", "fixture"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = repository
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return repository
}

type repositoryFactoryRuntime struct {
	starts        int
	startErr      error
	authority     microvm.RepositoryBootAuthority
	status        microvm.RuntimeStatus
	snapshotPaths []string
}

func (r *repositoryFactoryRuntime) Start(_ context.Context, record microvm.RepositoryVMRecord, artifacts microvm.VerifiedArtifacts, authority microvm.RepositoryBootAuthority) (microvm.RuntimeStatus, error) {
	r.starts++
	r.authority = authority
	r.snapshotPaths = nil
	for _, artifact := range artifacts.All() {
		r.snapshotPaths = append(r.snapshotPaths, artifact.Path)
		if _, err := os.Stat(artifact.Path); err != nil {
			return microvm.RuntimeStatus{}, err
		}
	}
	if r.startErr != nil {
		return microvm.RuntimeStatus{}, r.startErr
	}
	r.status = microvm.RuntimeStatus{Live: true, Generation: record.Generation, VMID: record.VMID, PID: 4242, ProcessIdentity: "test-process", Endpoint: record.Endpoint}
	return r.status, nil
}

func (r *repositoryFactoryRuntime) Health(_ context.Context, record microvm.RepositoryVMRecord, challenge microvm.RepositoryHealthChallenge) (microvm.RepositoryHealthResponse, error) {
	return r.authority.HealthResponse(record, challenge, r.status)
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
	return checker
}
