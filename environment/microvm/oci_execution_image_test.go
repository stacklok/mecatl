package microvm

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestMicroVMEnvironments_Scenario2_OCIExecutionImageResolver(t *testing.T) {
	t.Parallel()
	img := testOCIImage(t, runtime.GOARCH)
	manifest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	ref := "ghcr.io/stacklok/brood-box/base@" + manifest.String()
	fetcher := &countingImageFetcher{image: img}
	resolver := NewOCIExecutionImageResolver(filepath.Join(t.TempDir(), "oci"), fetcher, map[string]VerificationEvidence{ref: {Bundle: []byte("bundle")}})
	request := ArtifactRequest{Kind: ArtifactExecutionImage, Reference: ref, ManifestDigest: manifest.String()}

	first, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("cold Resolve() error = %v", err)
	}
	if first.ManifestDigest != manifest.String() || first.Digest == "" || first.Source == nil {
		t.Fatalf("cold identity/source = %+v", first)
	}
	request.Digest = first.Digest
	warm, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("warm Resolve() error = %v", err)
	}
	if warm.Digest != first.Digest || fetcher.calls.Load() != 1 {
		t.Fatalf("warm cache identity/calls = %q/%d, want %q/1", warm.Digest, fetcher.calls.Load(), first.Digest)
	}

	const workers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resolved, resolveErr := resolver.Resolve(context.Background(), request)
			if resolveErr == nil && resolved.Digest != first.Digest {
				resolveErr = errors.New("concurrent resolver returned another tree identity")
			}
			errCh <- resolveErr
		}()
	}
	wg.Wait()
	close(errCh)
	for resolveErr := range errCh {
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
	}
	if fetcher.calls.Load() != 1 {
		t.Fatalf("warm/concurrent pulls = %d, want one cold pull", fetcher.calls.Load())
	}

	coldFetcher := &countingImageFetcher{image: img}
	coldResolver := NewOCIExecutionImageResolver(filepath.Join(t.TempDir(), "oci-cold-concurrent"), coldFetcher, map[string]VerificationEvidence{ref: {Bundle: []byte("bundle")}})
	start := make(chan struct{})
	errCh = make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, resolveErr := coldResolver.Resolve(context.Background(), ArtifactRequest{Kind: ArtifactExecutionImage, Reference: ref, ManifestDigest: manifest.String()})
			errCh <- resolveErr
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for resolveErr := range errCh {
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
	}
	if coldFetcher.calls.Load() != 1 {
		t.Fatalf("concurrent cold pulls = %d, want one", coldFetcher.calls.Load())
	}
}

func TestOCIExecutionImageResolverInvalidatesLegacyModeDependentCacheNamespace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "refs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "refs", "legacy-mode-corruption"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	img := testOCIImage(t, runtime.GOARCH)
	manifest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	ref := "ghcr.io/stacklok/brood-box/base@" + manifest.String()
	fetcher := &countingImageFetcher{image: img}
	resolver := NewOCIExecutionImageResolver(root, fetcher, map[string]VerificationEvidence{ref: {Bundle: []byte("bundle")}})
	if resolver.cache.BaseDir() != filepath.Join(root, ociExtractionCacheVersion) {
		t.Fatalf("cache root = %q, want versioned deterministic-mode namespace", resolver.cache.BaseDir())
	}
	if _, err := resolver.Resolve(t.Context(), ArtifactRequest{Kind: ArtifactExecutionImage, Reference: ref, ManifestDigest: manifest.String()}); err != nil {
		t.Fatalf("Resolve() consulted legacy cache state: %v", err)
	}
	if fetcher.calls.Load() != 1 {
		t.Fatalf("cold pulls = %d, want 1", fetcher.calls.Load())
	}
}

func TestMicroVMEnvironments_Scenario2_OCIExecutionImageRejectsMutableMalformedAndWrongPlatform(t *testing.T) {
	t.Parallel()
	img := testOCIImage(t, runtime.GOARCH)
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	good := "ghcr.io/stacklok/brood-box/base@" + digest.String()
	wrongArch := "amd64"
	if runtime.GOARCH == wrongArch {
		wrongArch = "arm64"
	}
	wrongPlatformImage := testOCIImage(t, wrongArch)
	wrongPlatformDigest, err := wrongPlatformImage.Digest()
	if err != nil {
		t.Fatal(err)
	}
	wrongPlatformRef := "ghcr.io/stacklok/brood-box/base@" + wrongPlatformDigest.String()

	tests := []struct {
		name    string
		request ArtifactRequest
		image   v1.Image
	}{
		{name: "tag", request: ArtifactRequest{Kind: ArtifactExecutionImage, Reference: "ghcr.io/stacklok/brood-box/base:v1", ManifestDigest: digest.String()}, image: img},
		{name: "latest", request: ArtifactRequest{Kind: ArtifactExecutionImage, Reference: "ghcr.io/stacklok/brood-box/base:latest", ManifestDigest: digest.String()}, image: img},
		{name: "malformed digest", request: ArtifactRequest{Kind: ArtifactExecutionImage, Reference: "ghcr.io/stacklok/brood-box/base@sha256:nope", ManifestDigest: "sha256:nope"}, image: img},
		{name: "manifest mismatch", request: ArtifactRequest{Kind: ArtifactExecutionImage, Reference: good, ManifestDigest: digestFor("another-manifest")}, image: img},
		{name: "platform mismatch", request: ArtifactRequest{Kind: ArtifactExecutionImage, Reference: wrongPlatformRef, ManifestDigest: wrongPlatformDigest.String()}, image: wrongPlatformImage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := NewOCIExecutionImageResolver(filepath.Join(t.TempDir(), "oci"), &countingImageFetcher{image: tc.image}, map[string]VerificationEvidence{tc.request.Reference: {Bundle: []byte("bundle")}})
			if _, err := resolver.Resolve(context.Background(), tc.request); err == nil {
				t.Fatal("Resolve() error = nil, want fail-closed rejection")
			}
		})
	}
}

func TestInvariant_oci_execution_image_retains_manifest_and_admitted_tree_identities(t *testing.T) {
	t.Parallel()
	img := testOCIImage(t, runtime.GOARCH)
	manifest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	ref := "ghcr.io/stacklok/brood-box/base@" + manifest.String()
	resolver := NewOCIExecutionImageResolver(filepath.Join(t.TempDir(), "oci"), &countingImageFetcher{image: img}, map[string]VerificationEvidence{ref: {Bundle: []byte("bundle")}})
	resolved, err := resolver.Resolve(context.Background(), ArtifactRequest{Kind: ArtifactExecutionImage, Reference: ref, ManifestDigest: manifest.String()})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ManifestDigest == resolved.Digest {
		t.Fatalf("manifest identity %q unexpectedly aliases materialized-tree identity", resolved.ManifestDigest)
	}
	request := ArtifactRequest{Kind: ArtifactExecutionImage, Reference: ref, ManifestDigest: manifest.String(), Digest: digestFor("wrong-tree")}
	if _, err := resolver.Resolve(context.Background(), request); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("tree mismatch error = %v, want ErrDigestMismatch", err)
	}
}

func TestMicroVMEnvironments_Scenario2_LiveOCIPlatformResolver(t *testing.T) {
	ref := os.Getenv("MECATL_MICROVM_LIVE_OCI_REF")
	if ref == "" {
		t.Skip("release/live platform OCI reference is not configured")
	}
	at := strings.LastIndexByte(ref, '@')
	if at < 0 {
		t.Fatalf("live OCI reference %q is not digest-pinned", ref)
	}
	manifest := ref[at+1:]
	resolver := NewOCIExecutionImageResolver(filepath.Join(t.TempDir(), "oci"), nil, map[string]VerificationEvidence{ref: {Bundle: []byte("release-live")}})
	first, err := resolver.Resolve(context.Background(), ArtifactRequest{Kind: ArtifactExecutionImage, Reference: ref, ManifestDigest: manifest})
	if err != nil {
		t.Fatalf("live platform Resolve() error = %v", err)
	}
	second, err := resolver.Resolve(context.Background(), ArtifactRequest{Kind: ArtifactExecutionImage, Reference: ref, ManifestDigest: manifest, Digest: first.Digest})
	if err != nil {
		t.Fatalf("live warm Resolve() error = %v", err)
	}
	if second.ManifestDigest != manifest || second.Digest != first.Digest || second.Source == nil {
		t.Fatalf("live OCI identities/source = %+v", second)
	}
}

func testOCIImage(t *testing.T, arch string) v1.Image {
	t.Helper()
	var layer bytes.Buffer
	writer := tar.NewWriter(&layer)
	if err := writer.WriteHeader(&tar.Header{Name: "usr/bin/mecatl-tool", Mode: 0o755, Size: int64(len("tool")), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("tool")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, static.NewLayer(layer.Bytes(), types.OCILayer))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg.OS = "linux"
	cfg.Architecture = arch
	cfg.Config.User = "65532:65532"
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

type countingImageFetcher struct {
	image v1.Image
	calls atomic.Int32
}

func (f *countingImageFetcher) Pull(context.Context, string) (v1.Image, error) {
	f.calls.Add(1)
	return f.image, nil
}
