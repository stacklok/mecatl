package microvm

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/go-microvm/extract"
)

func TestInvariant_artifact_observer_counts_only_attempted_verifications(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sources := filepath.Join(root, "sources")
	if err := os.Mkdir(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	public, private := testKey()
	requests, resolver := testArtifactSet(t, sources, private, "builder@example.com")
	resolved := resolver.artifacts[ArtifactRuntime]
	resolved.Evidence.Bundle[0] ^= 0xff
	resolver.artifacts[ArtifactRuntime] = resolved
	observer := NewOperationsObserver(nil)
	provisioner := NewProvisioner(NewVerifiedCache(filepath.Join(root, "cache")), resolver, testPolicy("policy-v1", public), nil, nil, observer)

	if _, _, err := provisioner.Verify(context.Background(), requests); err == nil {
		t.Fatal("corrupt runtime evidence unexpectedly verified")
	}
	snapshot := observer.Snapshot()
	if snapshot.ArtifactVerifications[ArtifactRuntime][OutcomeFailure] != 1 || snapshot.ArtifactVerifications[ArtifactFirmware][OutcomeFailure] != 0 || snapshot.ArtifactVerifications[ArtifactExecutionImage][OutcomeFailure] != 0 {
		t.Fatalf("artifact verification counters are not attempt-truthful: %+v", snapshot.ArtifactVerifications)
	}
}

func TestMicroVMEnvironments_Scenario2_VerifiedArtifactsBoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	pub, private := testKey()
	policy := testPolicy("policy-7", pub)
	requests, resolver := testArtifactSet(t, root, private, "builder@example.com")
	launcher := &recordingLauncher{}
	metadata := &memoryMetadataStore{}
	provisioner := NewProvisioner(NewVerifiedCache(filepath.Join(root, "cache")), resolver, policy, launcher, metadata)

	got, err := provisioner.Provision(ctx, "session-1", requests)
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if got.VMID != "vm-1" {
		t.Fatalf("VMID = %q, want vm-1", got.VMID)
	}
	if got.PolicyRevision != policy.Revision {
		t.Fatalf("PolicyRevision = %q, want %q", got.PolicyRevision, policy.Revision)
	}
	if len(got.Artifacts) != 4 {
		t.Fatalf("artifact metadata count = %d, want 4", len(got.Artifacts))
	}
	for _, kind := range []ArtifactKind{ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent} {
		want := requests[kind].Digest
		if got.Artifacts[kind] != want {
			t.Errorf("metadata digest for %s = %q, want %q", kind, got.Artifacts[kind], want)
		}
		if launcher.artifacts.ByKind(kind).Digest != want {
			t.Errorf("launched digest for %s = %q, want %q", kind, launcher.artifacts.ByKind(kind).Digest, want)
		}
	}
	if !launcher.sourcesUsable.Load() {
		t.Error("launcher did not receive usable private artifact sources")
	}
	persisted, ok := metadata.bySession["session-1"]
	if !ok {
		t.Fatal("durable metadata was not saved")
	}
	if persisted.PolicyRevision != policy.Revision {
		t.Errorf("persisted policy revision = %q, want %q", persisted.PolicyRevision, policy.Revision)
	}
}

func TestInvariant_verified_artifact_launch_uses_private_snapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	pub, private := testKey()
	policy := testPolicy("policy-7", pub)
	requests, resolver := testArtifactSet(t, root, private, "builder@example.com")
	provisioner := NewProvisioner(NewVerifiedCache(filepath.Join(root, "cache")), resolver, policy, nil, nil)
	artifacts, _, err := provisioner.Verify(ctx, requests)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	launchArtifacts, release, err := provisioner.LockAndValidate(ctx, artifacts)
	if err != nil {
		t.Fatalf("LockAndValidate() error = %v", err)
	}
	defer release()
	if launchArtifacts.Runtime.Path == artifacts.Runtime.Path {
		t.Fatal("launch runtime still names the mutable verified-cache entry")
	}
	original := filepath.Join(artifacts.Runtime.Path, "artifact.bin")
	if err := os.WriteFile(original, []byte("same-account-post-validation-mutation"), 0o600); err != nil {
		t.Fatalf("plant non-cooperating cache mutation: %v", err)
	}
	launched, err := os.ReadFile(filepath.Join(launchArtifacts.Runtime.Path, "artifact.bin"))
	if err != nil {
		t.Fatalf("read private launch snapshot: %v", err)
	}
	if string(launched) != "runtime-complete" {
		t.Fatalf("launched runtime bytes = %q, want verified bytes", launched)
	}
}

func TestMicroVMEnvironments_Scenario2_UnverifiedArtifactsFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pub, private := testKey()

	tests := []struct {
		name   string
		mutate func(map[ArtifactKind]ArtifactRequest, *fakeResolver, *TrustPolicy)
	}{
		{name: "mutable tag only", mutate: func(req map[ArtifactKind]ArtifactRequest, _ *fakeResolver, _ *TrustPolicy) {
			r := req[ArtifactExecutionImage]
			r.Digest = ""
			r.Reference = "registry.example/app:latest"
			req[ArtifactExecutionImage] = r
		}},
		{name: "wrong digest", mutate: func(_ map[ArtifactKind]ArtifactRequest, resolver *fakeResolver, _ *TrustPolicy) {
			r := resolver.artifacts[ArtifactRuntime]
			r.Digest = digestFor("different")
			resolver.artifacts[ArtifactRuntime] = r
		}},
		{name: "unsigned", mutate: func(_ map[ArtifactKind]ArtifactRequest, resolver *fakeResolver, _ *TrustPolicy) {
			r := resolver.artifacts[ArtifactFirmware]
			r.Evidence.Bundle = nil
			resolver.artifacts[ArtifactFirmware] = r
		}},
		{name: "wrong signer", mutate: func(_ map[ArtifactKind]ArtifactRequest, _ *fakeResolver, policy *TrustPolicy) {
			policy.CertificateIdentity = "https://github.com/attacker/repo/.github/workflows/release.yml@refs/tags/v1"
		}},
		{name: "missing attestation", mutate: func(_ map[ArtifactKind]ArtifactRequest, resolver *fakeResolver, _ *TrustPolicy) {
			r := resolver.artifacts[ArtifactExecutionImage]
			r.Evidence.Attestation = Attestation{}
			resolver.artifacts[ArtifactExecutionImage] = r
		}},
		{name: "wrong attestation", mutate: func(_ map[ArtifactKind]ArtifactRequest, resolver *fakeResolver, _ *TrustPolicy) {
			r := resolver.artifacts[ArtifactFirmware]
			r.Evidence.Attestation.PredicateType = "https://example.com/wrong"
			resolver.artifacts[ArtifactFirmware] = r
		}},
		{name: "valid evidence with altered materialized bytes", mutate: func(_ map[ArtifactKind]ArtifactRequest, resolver *fakeResolver, _ *TrustPolicy) {
			r := resolver.artifacts[ArtifactRuntime]
			r.Source = staticSource{content: []byte("altered-after-signing")}
			resolver.artifacts[ArtifactRuntime] = r
		}},
		{name: "revoked identity", mutate: func(_ map[ArtifactKind]ArtifactRequest, _ *fakeResolver, policy *TrustPolicy) {
			policy.RevokedIdentities = map[string]struct{}{policy.CertificateIdentity: {}}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			policy := testPolicy("policy-7", pub)
			requests, resolver := testArtifactSet(t, root, private, "builder@example.com")
			tc.mutate(requests, resolver, &policy)
			launcher := &recordingLauncher{}
			p := NewProvisioner(NewVerifiedCache(filepath.Join(root, "cache")), resolver, policy, launcher, &memoryMetadataStore{})
			if _, err := p.Provision(ctx, "session-bad", requests); err == nil {
				t.Fatal("Provision() error = nil, want fail-closed rejection")
			}
			if launcher.calls.Load() != 0 {
				t.Fatalf("launcher calls = %d, want 0", launcher.calls.Load())
			}
		})
	}

	t.Run("corrupted cache entry", func(t *testing.T) {
		root := t.TempDir()
		policy := testPolicy("policy-7", pub)
		requests, resolver := testArtifactSet(t, root, private, "builder@example.com")
		cache := NewVerifiedCache(filepath.Join(root, "cache"))
		firstLauncher := &recordingLauncher{}
		p := NewProvisioner(cache, resolver, policy, firstLauncher, &memoryMetadataStore{})
		if _, err := p.Provision(ctx, "session-good", requests); err != nil {
			t.Fatalf("initial Provision() error = %v", err)
		}
		verified, _, err := p.Verify(ctx, requests)
		if err != nil {
			t.Fatalf("load admitted artifacts: %v", err)
		}
		runtimePath := verified.Runtime.Path
		runtimeFile := filepath.Join(runtimePath, "artifact.bin")
		if err := os.Chmod(runtimeFile, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(runtimeFile, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		secondLauncher := &recordingLauncher{}
		p = NewProvisioner(cache, resolver, policy, secondLauncher, &memoryMetadataStore{})
		if _, err := p.Provision(ctx, "session-corrupt", requests); !errors.Is(err, ErrCorruptCacheEntry) {
			t.Fatalf("Provision() error = %v, want ErrCorruptCacheEntry", err)
		}
		if secondLauncher.calls.Load() != 0 {
			t.Fatalf("launcher calls = %d, want 0", secondLauncher.calls.Load())
		}
	})

	t.Run("mutated after verification before launch", func(t *testing.T) {
		root := t.TempDir()
		policy := testPolicy("policy-7", pub)
		requests, resolver := testArtifactSet(t, root, private, "builder@example.com")
		launcher := &recordingLauncher{}
		p := NewProvisioner(NewVerifiedCache(filepath.Join(root, "cache")), resolver, policy, launcher, &memoryMetadataStore{})
		artifacts, _, err := p.Verify(ctx, requests)
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		runtimeFile := filepath.Join(artifacts.Runtime.Path, "artifact.bin")
		if err := os.WriteFile(runtimeFile, []byte("mutated-after-verify"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.LockAndValidate(ctx, artifacts); !errors.Is(err, ErrCorruptCacheEntry) {
			t.Fatalf("LockAndValidate() error = %v, want ErrCorruptCacheEntry", err)
		}
		if launcher.calls.Load() != 0 {
			t.Fatalf("launcher calls = %d, want 0", launcher.calls.Load())
		}
	})

	t.Run("stale policy", func(t *testing.T) {
		root := t.TempDir()
		requests, resolver := testArtifactSet(t, root, private, "builder@example.com")
		cache := NewVerifiedCache(filepath.Join(root, "cache"))
		p := NewProvisioner(cache, resolver, testPolicy("policy-7", pub), &recordingLauncher{}, &memoryMetadataStore{})
		if _, err := p.Provision(ctx, "session-old", requests); err != nil {
			t.Fatalf("initial Provision() error = %v", err)
		}
		launcher := &recordingLauncher{}
		p = NewProvisioner(cache, resolver, testPolicy("policy-8", pub), launcher, &memoryMetadataStore{})
		if _, err := p.Provision(ctx, "session-stale", requests); !errors.Is(err, ErrStalePolicy) {
			t.Fatalf("Provision() error = %v, want ErrStalePolicy", err)
		}
		if launcher.calls.Load() != 0 {
			t.Fatalf("launcher calls = %d, want 0", launcher.calls.Load())
		}
	})
}

func TestMicroVMEnvironments_Scenario2_ConcurrentCacheAdmissionIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	pub, private := testKey()
	policy := testPolicy("policy-7", pub)
	requests, resolver := testArtifactSet(t, root, private, "builder@example.com")
	started := make(chan struct{})
	release := make(chan struct{})
	blocking := &blockingSource{content: []byte("runtime-complete"), started: started, release: release}
	runtime := resolver.artifacts[ArtifactRuntime]
	runtime.Source = blocking
	resolver.artifacts[ArtifactRuntime] = runtime
	cache := NewVerifiedCache(filepath.Join(root, "cache"))
	launcher := &recordingLauncher{}
	p := NewProvisioner(cache, resolver, policy, launcher, &memoryMetadataStore{})

	const sessions = 12
	errCh := make(chan error, sessions)
	for i := 0; i < sessions; i++ {
		go func() {
			_, err := p.Provision(ctx, "concurrent", requests)
			errCh <- err
		}()
	}
	<-started
	if launcher.calls.Load() != 0 {
		t.Fatalf("launcher observed pre-verification artifact: calls = %d", launcher.calls.Load())
	}
	close(release)
	for range sessions {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent Provision() error = %v", err)
		}
	}
	if blocking.calls.Load() != 1 {
		t.Fatalf("cold runtime materializations = %d, want 1", blocking.calls.Load())
	}
	if launcher.calls.Load() != sessions {
		t.Fatalf("launcher calls = %d, want %d", launcher.calls.Load(), sessions)
	}
	if launcher.partial.Load() {
		t.Fatal("a launcher observed partial artifact contents")
	}
}

type fakeResolver struct {
	artifacts map[ArtifactKind]ResolvedArtifact
}

func (r *fakeResolver) Resolve(_ context.Context, request ArtifactRequest) (ResolvedArtifact, error) {
	artifact, ok := r.artifacts[request.Kind]
	if !ok {
		return ResolvedArtifact{}, errors.New("not found")
	}
	return artifact, nil
}

type staticSource struct {
	content []byte
	name    string
}

func (s staticSource) Ensure(_ context.Context, cacheDir string) (string, error) {
	dir, err := os.MkdirTemp(cacheDir, "source-")
	if err != nil {
		return "", err
	}
	name := s.name
	if name == "" {
		name = "artifact.bin"
	}
	if err := os.WriteFile(filepath.Join(dir, name), s.content, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

type blockingSource struct {
	content []byte
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	once    sync.Once
}

func (s *blockingSource) Ensure(ctx context.Context, cacheDir string) (string, error) {
	s.calls.Add(1)
	dir, err := os.MkdirTemp(cacheDir, "source-")
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(dir, "artifact.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(s.content[:len(s.content)/2]); err != nil {
		return "", err
	}
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.release:
	}
	if _, err := file.Write(s.content[len(s.content)/2:]); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return dir, nil
}

type recordingLauncher struct {
	calls         atomic.Int32
	partial       atomic.Bool
	sourcesUsable atomic.Bool
	mu            sync.Mutex
	artifacts     VerifiedArtifacts
}

func (l *recordingLauncher) Launch(ctx context.Context, artifacts VerifiedArtifacts) (string, error) {
	for _, artifact := range artifacts.All() {
		name := "artifact.bin"
		if artifact.Kind == ArtifactGuestAgent {
			name = guestAgentArtifactName
		}
		content, err := os.ReadFile(filepath.Join(artifact.Path, name))
		if err != nil || len(content) == 0 || string(content) == "runtime-" {
			l.partial.Store(true)
			return "", errors.New("partial artifact")
		}
		if _, err := artifact.Source.Ensure(ctx, ""); err != nil {
			return "", errors.New("unusable private artifact source")
		}
	}
	l.sourcesUsable.Store(true)
	l.calls.Add(1)
	l.mu.Lock()
	l.artifacts = artifacts
	l.mu.Unlock()
	return "vm-1", nil
}

type memoryMetadataStore struct {
	mu        sync.Mutex
	bySession map[string]EnvironmentMetadata
}

func (s *memoryMetadataStore) Save(_ context.Context, metadata EnvironmentMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bySession == nil {
		s.bySession = make(map[string]EnvironmentMetadata)
	}
	s.bySession[metadata.SessionID] = metadata
	return nil
}

func TestArtifactVerificationAcceptsOnePinnedPublicKeyIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	requests, resolver := testArtifactSet(t, root, nil, "")
	identity := PublicKeyIdentity([]byte("public-key"))
	policy := TrustPolicy{
		Revision:          "key-policy-v1",
		PublicKeyIdentity: identity,
		Verifier:          keyEvidenceVerifier{identity: identity},
		RequiredAttestations: map[ArtifactKind]string{
			ArtifactRuntime: "https://slsa.dev/provenance/v1", ArtifactFirmware: "https://slsa.dev/provenance/v1",
			ArtifactExecutionImage: "https://slsa.dev/provenance/v1", ArtifactGuestAgent: "https://slsa.dev/provenance/v1",
		},
	}
	provisioner := NewProvisioner(NewVerifiedCache(filepath.Join(root, "cache")), resolver, policy, nil, nil)
	if _, _, err := provisioner.Verify(context.Background(), requests); err != nil {
		t.Fatalf("Verify(public-key policy): %v", err)
	}

	policy.PublicKeyIdentity = PublicKeyIdentity([]byte("wrong-key"))
	provisioner = NewProvisioner(NewVerifiedCache(filepath.Join(root, "wrong-cache")), resolver, policy, nil, nil)
	if _, _, err := provisioner.Verify(context.Background(), requests); err == nil {
		t.Fatal("Verify(wrong public-key identity) error = nil")
	}
	policy.CertificateIdentity = "ambiguous"
	policy.OIDCIssuer = "issuer"
	provisioner = NewProvisioner(NewVerifiedCache(filepath.Join(root, "ambiguous-cache")), resolver, policy, nil, nil)
	if _, _, err := provisioner.Verify(context.Background(), requests); err == nil {
		t.Fatal("Verify(ambiguous key and keyless policy) error = nil")
	}
}

func TestArtifactCacheAcceptsContainedSymlinksInExecutionTree(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "target"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	digest, err := digestTree(root)
	if err != nil {
		t.Fatalf("digestTree: %v", err)
	}
	staged := filepath.Join(t.TempDir(), "staged")
	if err := copyTree(root, staged); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	got, err := digestTree(staged)
	if err != nil {
		t.Fatalf("digest staged tree: %v", err)
	}
	if got != digest {
		t.Fatalf("staged digest = %q, want %q", got, digest)
	}
}

func testArtifactSet(t *testing.T, root string, _ ed25519.PrivateKey, _ string) (map[ArtifactKind]ArtifactRequest, *fakeResolver) {
	t.Helper()
	requests := make(map[ArtifactKind]ArtifactRequest)
	artifacts := make(map[ArtifactKind]ResolvedArtifact)
	for _, kind := range []ArtifactKind{ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent} {
		content := []byte(string(kind) + "-complete")
		source := staticSource{content: content}
		if kind == ArtifactGuestAgent {
			source.name = guestAgentArtifactName
		}
		materialized, err := source.Ensure(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := digestTree(materialized)
		if err != nil {
			t.Fatal(err)
		}
		request := ArtifactRequest{Kind: kind, Reference: "registry.example/" + string(kind) + "@" + digest, Digest: digest}
		attestation := Attestation{
			PredicateType: "https://slsa.dev/provenance/v1",
			SubjectDigest: digest,
			Statement:     []byte(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"digest":{"sha256":"` + strings.TrimPrefix(digest, "sha256:") + `"}}],"predicateType":"https://slsa.dev/provenance/v1"}`),
		}
		requests[kind] = request
		artifacts[kind] = ResolvedArtifact{
			Kind: kind, Digest: digest, Source: source,
			Evidence: VerificationEvidence{Bundle: []byte("sigstore-bundle"), Attestation: attestation},
		}
	}
	return requests, &fakeResolver{artifacts: artifacts}
}

func testPolicy(revision string, _ ed25519.PublicKey) TrustPolicy {
	return TrustPolicy{
		Revision:            revision,
		CertificateIdentity: "https://github.com/stacklok/mecatl/.github/workflows/release.yml@refs/tags/v1.0.0",
		OIDCIssuer:          "https://token.actions.githubusercontent.com",
		Verifier:            testEvidenceVerifier{},
		RequiredAttestations: map[ArtifactKind]string{
			ArtifactRuntime: "https://slsa.dev/provenance/v1", ArtifactFirmware: "https://slsa.dev/provenance/v1",
			ArtifactExecutionImage: "https://slsa.dev/provenance/v1", ArtifactGuestAgent: "https://slsa.dev/provenance/v1",
		},
	}
}

type keyEvidenceVerifier struct{ identity string }

func (v keyEvidenceVerifier) Verify(_ context.Context, statement, bundle []byte, identity, issuer string) error {
	if len(statement) == 0 || string(bundle) != "sigstore-bundle" || identity != v.identity || issuer != "" {
		return errors.New("bundle or public-key identity rejected")
	}
	return nil
}

type testEvidenceVerifier struct{}

func (testEvidenceVerifier) Verify(_ context.Context, statement, bundle []byte, identity, issuer string) error {
	if len(statement) == 0 || string(bundle) != "sigstore-bundle" || identity != "https://github.com/stacklok/mecatl/.github/workflows/release.yml@refs/tags/v1.0.0" || issuer != "https://token.actions.githubusercontent.com" {
		return errors.New("bundle or identity rejected")
	}
	return nil
}

func testKey() (ed25519.PublicKey, ed25519.PrivateKey) { return testKeyFromByte(3) }

func testKeyFromByte(b byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = b
	}
	private := ed25519.NewKeyFromSeed(seed)
	return private.Public().(ed25519.PublicKey), private
}

func digestFor(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

var _ extract.Source = staticSource{}
