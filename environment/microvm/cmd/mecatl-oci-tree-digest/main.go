// Command mecatl-oci-tree-digest pulls one digest-pinned platform image through
// go-microvm's extractor and prints its materialized tree identity.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	gomicrovmimage "github.com/stacklok/go-microvm/image"

	microvm "github.com/stacklok/mecatl/environment/microvm"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: mecatl-oci-tree-digest REPO@SHA256 CACHE_DIR OS/ARCH")
		os.Exit(2)
	}
	digest, err := treeDigest(context.Background(), os.Args[1], os.Args[2], os.Args[3], nil)
	if err != nil {
		if errors.Is(err, microvm.ErrMutableArtifact) {
			fmt.Fprintln(os.Stderr, "OCI reference must be digest-pinned")
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
	fmt.Println(digest)
}

func treeDigest(ctx context.Context, ref, cacheDir, platform string, fetcher gomicrovmimage.ImageFetcher) (string, error) {
	manifest, err := pinnedManifest(ref)
	if err != nil {
		return "", err
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return "", fmt.Errorf("target platform must be linux/amd64 or linux/arm64")
	}
	osName, architecture, _ := strings.Cut(platform, "/")
	// Cache hits bypass the fetcher, so only reuse trees validated for this target.
	rootfs, err := gomicrovmimage.PullWithFetcher(ctx, ref, gomicrovmimage.NewCache(filepath.Join(cacheDir, platform)), platformFetcher{
		next: fetcher, manifest: manifest, os: osName, architecture: architecture,
	})
	if err != nil {
		return "", fmt.Errorf("pull OCI image: %w", err)
	}
	digest, err := microvm.ArtifactTreeDigest(rootfs.Path)
	if err != nil {
		return "", fmt.Errorf("digest extracted OCI image: %w", err)
	}
	return digest, nil
}

func pinnedManifest(ref string) (string, error) {
	parsed, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil || parsed.Name() != ref || !strings.HasPrefix(parsed.DigestStr(), "sha256:") {
		return "", microvm.ErrMutableArtifact
	}
	return parsed.DigestStr(), nil
}

type platformFetcher struct {
	next                       gomicrovmimage.ImageFetcher
	manifest, os, architecture string
}

func (f platformFetcher) Pull(ctx context.Context, ref string) (v1.Image, error) {
	if f.next == nil {
		f.next = gomicrovmimage.RemoteFetcher{}
	}
	image, err := f.next.Pull(ctx, ref)
	if err != nil {
		return nil, err
	}
	digest, err := image.Digest()
	if err != nil || digest.String() != f.manifest {
		return nil, microvm.ErrDigestMismatch
	}
	config, err := image.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("read OCI image platform: %w", err)
	}
	if config.OS != f.os || config.Architecture != f.architecture {
		return nil, fmt.Errorf("OCI image platform is %s/%s, want %s/%s", config.OS, config.Architecture, f.os, f.architecture)
	}
	return image, nil
}
