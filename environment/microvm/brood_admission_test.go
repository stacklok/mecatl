package microvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMicroVMRedesign_Scenario4_BroodLatestIsDiscoveryOnly(t *testing.T) {
	t.Parallel()

	release, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(release)
	if !strings.Contains(text, "BROOD_DISCOVERY_REFERENCE: ghcr.io/stacklok/brood-box/base:latest") {
		t.Fatal("release workflow does not name Brood latest as its controlled discovery reference")
	}
	for _, obsolete := range []string{"publish-brood-base:", "publish-guest-tools:", "environment/microvm/images/guest-tools"} {
		if strings.Contains(text, obsolete) {
			t.Fatalf("release workflow still contains obsolete Brood rebuild/derived publication %q", obsolete)
		}
	}
}

func TestMicroVMRedesign_Scenario4_RuntimeConsumesOnlyEndorsedBroodDigest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sources := filepath.Join(root, "sources")
	if err := os.Mkdir(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	requests, resolver := testArtifactSet(t, sources, nil, "")
	execution := requests[ArtifactExecutionImage]
	execution.ManifestDigest = "sha256:" + strings.Repeat("c", 64)
	execution.Reference = "ghcr.io/stacklok/brood-box/base@" + execution.ManifestDigest
	execution.DiscoveryReference = "ghcr.io/stacklok/brood-box/base:latest"
	execution.Platform = "linux/amd64"
	execution.ResolutionEvidence = "sha256:" + strings.Repeat("d", 64)
	requests[ArtifactExecutionImage] = execution

	resolved := resolver.artifacts[ArtifactExecutionImage]
	resolved.ManifestDigest = execution.ManifestDigest
	resolved.Evidence.Attestation.Statement = broodEndorsement(execution)
	resolver.artifacts[ArtifactExecutionImage] = resolved

	policy := testPolicy("brood-policy-v1", nil)
	verify := func(cache string, candidate map[ArtifactKind]ArtifactRequest, candidateResolver *fakeResolver, candidatePolicy TrustPolicy) error {
		_, _, err := NewProvisioner(NewVerifiedCache(filepath.Join(root, cache)), candidateResolver, candidatePolicy, nil, nil).Verify(context.Background(), candidate)
		return err
	}
	provision := func(cache string, candidate map[ArtifactKind]ArtifactRequest, candidateResolver *fakeResolver, candidatePolicy TrustPolicy) (int32, error) {
		launcher := &recordingLauncher{}
		_, err := NewProvisioner(NewVerifiedCache(filepath.Join(root, cache)), candidateResolver, candidatePolicy, launcher, &memoryMetadataStore{}).Provision(context.Background(), "session", candidate)
		return launcher.calls.Load(), err
	}
	launcher := &recordingLauncher{}
	if _, err := NewProvisioner(NewVerifiedCache(filepath.Join(root, "valid")), resolver, policy, launcher, &memoryMetadataStore{}).Provision(context.Background(), "valid-session", requests); err != nil {
		t.Fatalf("provision endorsed Brood platform bytes: %v", err)
	}
	if launcher.calls.Load() != 1 || launcher.artifacts.ExecutionImage.ManifestDigest != execution.ManifestDigest {
		t.Fatalf("launcher consumed %+v, want admitted Brood manifest %s exactly once", launcher.artifacts.ExecutionImage, execution.ManifestDigest)
	}

	cases := map[string]func(map[ArtifactKind]ArtifactRequest, *fakeResolver, *TrustPolicy){
		"changed-resolution": func(candidate map[ArtifactKind]ArtifactRequest, _ *fakeResolver, _ *TrustPolicy) {
			entry := candidate[ArtifactExecutionImage]
			entry.ManifestDigest = "sha256:" + strings.Repeat("e", 64)
			entry.Reference = "ghcr.io/stacklok/brood-box/base@" + entry.ManifestDigest
			candidate[ArtifactExecutionImage] = entry
		},
		"wrong-platform": func(candidate map[ArtifactKind]ArtifactRequest, _ *fakeResolver, _ *TrustPolicy) {
			entry := candidate[ArtifactExecutionImage]
			entry.Platform = "linux/arm64"
			candidate[ArtifactExecutionImage] = entry
		},
		"missing-endorsement": func(_ map[ArtifactKind]ArtifactRequest, candidateResolver *fakeResolver, _ *TrustPolicy) {
			entry := candidateResolver.artifacts[ArtifactExecutionImage]
			entry.Evidence.Bundle = nil
			candidateResolver.artifacts[ArtifactExecutionImage] = entry
		},
		"corrupted-subject": func(_ map[ArtifactKind]ArtifactRequest, candidateResolver *fakeResolver, _ *TrustPolicy) {
			entry := candidateResolver.artifacts[ArtifactExecutionImage]
			entry.Evidence.Attestation.Statement = []byte(`{"predicateType":"https://slsa.dev/provenance/v1","subject":[{"digest":{"sha256":"bad"}}]}`)
			candidateResolver.artifacts[ArtifactExecutionImage] = entry
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate, candidateResolver := cloneArtifactFixture(requests, resolver)
			candidatePolicy := policy
			mutate(candidate, candidateResolver, &candidatePolicy)
			if launches, err := provision(name, candidate, candidateResolver, candidatePolicy); err == nil {
				t.Fatal("verification error = nil, want rejection before launch")
			} else if launches != 0 {
				t.Fatalf("VM launches = %d, want 0 after rejected admission", launches)
			}
		})
	}

	if err := verify("stale", requests, resolver, policy); err != nil {
		t.Fatal(err)
	}
	stale := policy
	stale.Revision = "brood-policy-v2"
	if launches, err := provision("stale", requests, resolver, stale); !errors.Is(err, ErrStalePolicy) {
		t.Fatalf("stale policy error = %v, want ErrStalePolicy", err)
	} else if launches != 0 {
		t.Fatalf("VM launches = %d, want 0 under stale policy", launches)
	}
}

func TestMicroVMRedesign_Scenario4_SigstoreVerificationIsInProcess(t *testing.T) {
	t.Parallel()
	statement, err := os.ReadFile("testdata/sigstore/statement.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile("testdata/sigstore/bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := os.ReadFile("testdata/sigstore/public.pem")
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewSigstoreKeyVerifier(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	identity := PublicKeyIdentity(publicKey)
	if err := verifier.Verify(context.Background(), statement, bundle, identity, ""); err != nil {
		t.Fatalf("verify real cosign-format bundle in process: %v", err)
	}
	if err := verifier.Verify(context.Background(), append(statement, ' '), bundle, identity, ""); err == nil {
		t.Fatal("altered statement verification error = nil")
	}

	source, err := os.ReadFile("artifact_sigstore.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"os/exec"`, "exec.Command", "MkdirTemp", "cosign path", "workDir"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("runtime verifier retains subprocess/temp-file protocol %q", forbidden)
		}
	}
	daemon, err := os.ReadFile(filepath.Join("cmd", "mecatl-microvmd", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(daemon)), "cosign_path") {
		t.Fatal("production daemon configuration still exposes cosign_path")
	}
	for _, path := range []string{
		filepath.Join("..", "..", "internal", "adapter", "microvmmanager", "manager.go"),
		filepath.Join("..", "..", "internal", "adapter", "microvmmanager", "bootstrap.go"),
	} {
		production, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		lower := strings.ToLower(string(production))
		if strings.Contains(lower, "cosignpath") || strings.Contains(lower, "cosign_path") || strings.Contains(lower, "lookpath(\"cosign\")") {
			t.Fatalf("production configuration %s retains an ambient cosign dependency", path)
		}
	}
}

func broodEndorsement(request ArtifactRequest) []byte {
	return []byte(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"digest":{"sha256":"` + strings.TrimPrefix(request.Digest, "sha256:") + `"}}],"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"resolvedDependencies":[{"uri":"` + request.DiscoveryReference + `","digest":{"sha256":"` + strings.TrimPrefix(request.ManifestDigest, "sha256:") + `","resolutionEvidence":"` + strings.TrimPrefix(request.ResolutionEvidence, "sha256:") + `"},"platform":"` + request.Platform + `"}]}}}`)
}

func cloneArtifactFixture(requests map[ArtifactKind]ArtifactRequest, resolver *fakeResolver) (map[ArtifactKind]ArtifactRequest, *fakeResolver) {
	clonedRequests := make(map[ArtifactKind]ArtifactRequest, len(requests))
	for kind, request := range requests {
		clonedRequests[kind] = request
	}
	clonedArtifacts := make(map[ArtifactKind]ResolvedArtifact, len(resolver.artifacts))
	for kind, artifact := range resolver.artifacts {
		clonedArtifacts[kind] = artifact
	}
	return clonedRequests, &fakeResolver{artifacts: clonedArtifacts}
}
