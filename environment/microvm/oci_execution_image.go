package microvm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/stacklok/go-microvm/extract"
	gomicrovmimage "github.com/stacklok/go-microvm/image"
)

// OCIExecutionImageResolver pulls a platform-specific digest-pinned OCI image
// through go-microvm and exposes only its extracted tree to artifact admission.
type OCIExecutionImageResolver struct {
	cache    *gomicrovmimage.Cache
	fetcher  gomicrovmimage.ImageFetcher
	evidence map[string]VerificationEvidence
	mu       sync.Mutex
}

const ociExtractionCacheVersion = "deterministic-modes-v1"

// NewOCIExecutionImageResolver constructs an OCI execution-image resolver.
// cacheRoot is go-microvm's pull/extract cache; mecatl's verified cache remains
// a separate admission boundary. The versioned child leaves mode-dependent
// entries produced by older daemons unreachable rather than trusting them.
func NewOCIExecutionImageResolver(cacheRoot string, fetcher gomicrovmimage.ImageFetcher, evidence map[string]VerificationEvidence) *OCIExecutionImageResolver {
	cloned := make(map[string]VerificationEvidence, len(evidence))
	for ref, item := range evidence {
		cloned[ref] = item
	}
	return &OCIExecutionImageResolver{cache: gomicrovmimage.NewCache(filepath.Join(cacheRoot, ociExtractionCacheVersion)), fetcher: fetcher, evidence: cloned}
}

// Resolve implements ArtifactResolver. Digest is the expected extracted-tree
// identity; ManifestDigest is the independent OCI manifest identity.
func (r *OCIExecutionImageResolver) Resolve(ctx context.Context, request ArtifactRequest) (ResolvedArtifact, error) {
	if r == nil || r.cache == nil || request.Kind != ArtifactExecutionImage {
		return ResolvedArtifact{}, errors.New("OCI resolver accepts execution images only")
	}
	manifest, err := pinnedOCIManifest(request.Reference)
	if err != nil {
		return ResolvedArtifact{}, err
	}
	if request.ManifestDigest != "" && request.ManifestDigest != manifest {
		return ResolvedArtifact{}, ErrDigestMismatch
	}
	evidence, ok := r.evidence[request.Reference]
	if !ok {
		return ResolvedArtifact{}, fmt.Errorf("%w: OCI execution image has no verification evidence", ErrUnverifiedArtifact)
	}

	// go-microvm's cache ref index is process-safe on disk but Pull's cold path
	// is not documented as concurrently callable. Serialize it here so one
	// daemon performs one complete extraction for a cold manifest.
	r.mu.Lock()
	rootfs, err := gomicrovmimage.PullWithFetcher(ctx, request.Reference, r.cache, manifestPlatformFetcher{next: r.fetcher, manifest: manifest})
	r.mu.Unlock()
	if err != nil {
		return ResolvedArtifact{}, fmt.Errorf("pull OCI execution image: %w", err)
	}
	treeDigest, err := digestTree(rootfs.Path)
	if err != nil {
		return ResolvedArtifact{}, fmt.Errorf("digest extracted OCI execution image: %w", err)
	}
	if request.Digest != "" && request.Digest != treeDigest {
		return ResolvedArtifact{}, ErrDigestMismatch
	}
	return ResolvedArtifact{
		Kind: ArtifactExecutionImage, Digest: treeDigest, ManifestDigest: manifest,
		Source: extract.Dir(filepath.Clean(rootfs.Path)), Evidence: evidence,
	}, nil
}

func pinnedOCIManifest(ref string) (string, error) {
	parsed, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil || parsed.Name() != ref || !validDigest(parsed.DigestStr()) {
		return "", fmt.Errorf("%w: OCI execution image must be canonical repo@sha256", ErrMutableArtifact)
	}
	return parsed.DigestStr(), nil
}

type manifestPlatformFetcher struct {
	next     gomicrovmimage.ImageFetcher
	manifest string
}

func (f manifestPlatformFetcher) Pull(ctx context.Context, ref string) (v1.Image, error) {
	if f.next == nil {
		f.next = gomicrovmimage.RemoteFetcher{}
	}
	img, err := f.next.Pull(ctx, ref)
	if err != nil {
		return nil, err
	}
	digest, err := img.Digest()
	if err != nil || digest.String() != f.manifest {
		return nil, ErrDigestMismatch
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("read OCI execution-image platform: %w", err)
	}
	if cfg.OS != "linux" || cfg.Architecture != runtime.GOARCH {
		return nil, fmt.Errorf("OCI execution-image platform is %s/%s, want linux/%s", cfg.OS, cfg.Architecture, runtime.GOARCH)
	}
	return img, nil
}
