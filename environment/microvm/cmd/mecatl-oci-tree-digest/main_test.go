package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	gomicrovmimage "github.com/stacklok/go-microvm/image"

	"github.com/stacklok/mecatl/environment/microvm"
)

func TestTreeDigest_ExtractsArm64ImageAsData(t *testing.T) {
	t.Parallel()
	image := testImage(t, "linux", "arm64")
	manifest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	ref := "ghcr.io/stacklok/brood-box/base@" + manifest.String()
	fetcher := &testFetcher{image: image}

	cacheDir := t.TempDir()
	digest, err := treeDigest(t.Context(), ref, cacheDir, "linux/arm64", fetcher)
	if err != nil {
		t.Fatalf("treeDigest() error = %v", err)
	}
	nativeImage := testImage(t, "linux", runtime.GOARCH)
	nativeManifest, err := nativeImage.Digest()
	if err != nil {
		t.Fatal(err)
	}
	nativeRef := "ghcr.io/stacklok/brood-box/base@" + nativeManifest.String()
	resolver := microvm.NewOCIExecutionImageResolver(t.TempDir(), &testFetcher{image: nativeImage}, map[string]microvm.VerificationEvidence{
		nativeRef: {Bundle: []byte("test")},
	})
	resolved, err := resolver.Resolve(t.Context(), microvm.ArtifactRequest{
		Kind: microvm.ArtifactExecutionImage, Reference: nativeRef, ManifestDigest: nativeManifest.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if digest != resolved.Digest {
		t.Fatalf("arm64 data digest = %q, want native production digest %q", digest, resolved.Digest)
	}
	warm, err := treeDigest(t.Context(), ref, cacheDir, "linux/arm64", fetcher)
	if err != nil || warm != digest || fetcher.calls.Load() != 1 {
		t.Fatalf("warm digest/error/calls = %q/%v/%d, want %q/nil/1", warm, err, fetcher.calls.Load(), digest)
	}
	if _, err := treeDigest(t.Context(), ref, cacheDir, "linux/amd64", fetcher); err == nil || !strings.Contains(err.Error(), "want linux/amd64") {
		t.Fatalf("wrong target reused arm64 cache: %v", err)
	}
}

func TestTreeDigest_RejectsWrongManifestOrTargetPlatform(t *testing.T) {
	t.Parallel()
	image := testImage(t, "linux", "arm64")
	manifest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	ref := "ghcr.io/stacklok/brood-box/base@" + manifest.String()
	wrongManifest := testImage(t, "linux", "amd64")

	for _, tc := range []struct {
		name      string
		image     v1.Image
		platform  string
		wantError string
	}{
		{name: "wrong manifest", image: wrongManifest, platform: "linux/arm64", wantError: "digest mismatch"},
		{name: "wrong os", image: testImage(t, "windows", "arm64"), platform: "linux/arm64", wantError: "platform is windows/arm64, want linux/arm64"},
		{name: "wrong architecture", image: image, platform: "linux/amd64", wantError: "platform is linux/arm64, want linux/amd64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caseRef := ref
			if tc.name != "wrong manifest" {
				manifest, err := tc.image.Digest()
				if err != nil {
					t.Fatal(err)
				}
				caseRef = "ghcr.io/stacklok/brood-box/base@" + manifest.String()
			}
			if _, err := treeDigest(t.Context(), caseRef, t.TempDir(), tc.platform, &testFetcher{image: tc.image}); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("treeDigest() error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestTreeDigest_RejectsInvalidInputsBeforePull(t *testing.T) {
	t.Parallel()
	ref := "ghcr.io/stacklok/brood-box/base@sha256:" + strings.Repeat("a", 64)
	for _, platform := range []string{"", "linux", "linux/", "linux/arm64/v8", "windows/arm64", "linux/386", "linux/../amd64"} {
		t.Run(platform, func(t *testing.T) {
			fetcher := &testFetcher{}
			if _, err := treeDigest(t.Context(), ref, t.TempDir(), platform, fetcher); err == nil {
				t.Fatal("accepted invalid target platform")
			}
			if fetcher.calls.Load() != 0 {
				t.Fatal("invalid platform reached fetcher")
			}
		})
	}
	for _, ref := range []string{"", "ghcr.io/stacklok/brood-box/base:latest", "ghcr.io/stacklok/brood-box/base@sha256:nope", "ghcr.io/stacklok/brood-box/base@sha256:" + strings.Repeat("g", 64), "ghcr.io/stacklok/brood-box/base@sha512:" + strings.Repeat("a", 128)} {
		t.Run(ref, func(t *testing.T) {
			fetcher := &testFetcher{}
			if _, err := treeDigest(t.Context(), ref, t.TempDir(), "linux/amd64", fetcher); err == nil {
				t.Fatal("accepted invalid digest reference")
			}
			if fetcher.calls.Load() != 0 {
				t.Fatal("invalid reference reached fetcher")
			}
		})
	}
}

func testImage(t *testing.T, osName, architecture string) v1.Image {
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
	image, err := mutate.AppendLayers(empty.Image, static.NewLayer(layer.Bytes(), types.OCILayer))
	if err != nil {
		t.Fatal(err)
	}
	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	config.OS = osName
	config.Architecture = architecture
	image, err = mutate.ConfigFile(image, config)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

type testFetcher struct {
	image v1.Image
	calls atomic.Int32
}

func (f *testFetcher) Pull(context.Context, string) (v1.Image, error) {
	f.calls.Add(1)
	if f.image == nil {
		return nil, errors.New("unexpected pull")
	}
	return f.image, nil
}

var _ gomicrovmimage.ImageFetcher = (*testFetcher)(nil)
