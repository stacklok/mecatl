//go:build microvm_dev

package microvmmanager

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

type rejectHTTPTransport struct{ called bool }

func (r *rejectHTTPTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.called = true
	return nil, errors.New("HTTP must not be used")
}

func TestDevelopmentReleaseDescriptorIsStrictAndLocal(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "release.tar.gz")
	writeDevelopmentBundle(t, bundle)
	key := filepath.Join(root, "publisher.pub")
	writeOwnerOnly(t, key, []byte("development public key"))
	descriptor := filepath.Join(root, "release.json")
	body := validDevelopmentDescriptor(t, bundle, key)
	writeOwnerOnly(t, descriptor, body)

	request, err := ReadyRequestFromDevelopmentDescriptor(descriptor, "source-test")
	if err != nil {
		t.Fatal(err)
	}
	if request.Release.URL != "" || request.Release.bundlePath != bundle || request.Release.bundleFile == nil || request.Policy.PublicKey != "" || string(request.Policy.publicKey) != "development public key" || request.Policy.PolicyRevision != "development-test" {
		t.Fatalf("development request = %#v", request)
	}

	if _, err := ReadyRequestFromDevelopmentDescriptor(descriptor, "changed-source"); err == nil ||
		!strings.Contains(err.Error(), "task microvm:dev:prepare") || !strings.Contains(err.Error(), "task microvm:dev:build") {
		t.Fatalf("source-build mismatch error = %v, want actionable rebuild commands", err)
	}

	bundleLink := filepath.Join(root, "release-link.tar.gz")
	if err := os.Symlink(bundle, bundleLink); err != nil {
		t.Fatal(err)
	}
	keyLink := filepath.Join(root, "publisher-link.pub")
	if err := os.Symlink(key, keyLink); err != nil {
		t.Fatal(err)
	}
	unsafe := []struct {
		name string
		edit func(map[string]any)
	}{
		{"unknown field", func(value map[string]any) { value["unexpected"] = true }},
		{"wrong schema", func(value map[string]any) { value["schema"] = "other" }},
		{"wrong platform", func(value map[string]any) { value["platform"] = "linux-arm64" }},
		{"wrong build", func(value map[string]any) { value["source_build_identity"] = "other" }},
		{"wrong bundle digest", func(value map[string]any) { value["bundle_sha256"] = strings.Repeat("0", 64) }},
		{"wrong key identity", func(value map[string]any) { value["public_key_identity"] = "sha256:" + strings.Repeat("0", 64) }},
		{"symlink bundle", func(value map[string]any) { value["bundle_path"] = bundleLink }},
		{"symlink key", func(value map[string]any) { value["public_key_path"] = keyLink }},
	}
	for _, tc := range unsafe {
		t.Run(tc.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(body, &value); err != nil {
				t.Fatal(err)
			}
			tc.edit(value)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "descriptor.json")
			writeOwnerOnly(t, path, data)
			if _, err := ReadyRequestFromDevelopmentDescriptor(path, "source-test"); err == nil {
				t.Fatal("unsafe descriptor was accepted")
			}
		})
	}

	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadyRequestFromDevelopmentDescriptor(descriptor, "source-test"); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("world-readable key error = %v", err)
	}
}

func TestDevelopmentReleaseInputsRemainBoundAfterPathReplacement(t *testing.T) {
	root := privateTempDir(t)
	bundle := filepath.Join(root, "release.tar.gz")
	writeDevelopmentBundle(t, bundle)
	key := filepath.Join(root, "publisher.pub")
	originalKey := []byte("development public key")
	writeOwnerOnly(t, key, originalKey)
	descriptor := filepath.Join(root, "release.json")
	writeOwnerOnly(t, descriptor, validDevelopmentDescriptor(t, bundle, key))

	request, err := ReadyRequestFromDevelopmentDescriptor(descriptor, "source-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bundle, bundle+".original"); err != nil {
		t.Fatal(err)
	}
	writeOwnerOnly(t, bundle, []byte("replacement bundle"))
	writeOwnerOnly(t, key, []byte("replacement key"))

	ops := &DefaultOperations{GOOS: "linux", GOARCH: "amd64"}
	manifest, err := ops.Download(context.Background(), request.Release, root, filepath.Join(root, "download"))
	if err != nil {
		t.Fatalf("download bound bundle: %v", err)
	}
	if filepath.Base(manifest) != "microvm-release-linux-amd64.json" {
		t.Fatalf("manifest = %q", manifest)
	}

	paths := testPaths(filepath.Join(root, "manager"))
	if _, err := New(paths, &fakeOps{}).EnsureReady(context.Background(), request); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	configData, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	managedKey := filepath.Join(paths.DataDir, "trust", "development-"+strings.TrimPrefix(request.Policy.PublicKeyIdentity, "sha256:")+".pub")
	if config.PublicKey != managedKey {
		t.Fatalf("configured public key = %q, want %q", config.PublicKey, managedKey)
	}
	got, err := os.ReadFile(managedKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(originalKey) {
		t.Fatalf("managed public key = %q, want %q", got, originalKey)
	}
}

func TestDevelopmentReleaseDescriptorSymlinkIsRejected(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "release.json")
	writeOwnerOnly(t, target, []byte(`{}`))
	link := filepath.Join(root, "release-link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadyRequestFromDevelopmentDescriptor(link, "source-test"); err == nil {
		t.Fatal("symlink descriptor was accepted")
	}
}

func TestDevelopmentReleaseDownloadNeverUsesHTTP(t *testing.T) {
	root := privateTempDir(t)
	bundle := filepath.Join(root, "release.tar.gz")
	writeDevelopmentBundle(t, bundle)
	transport := &rejectHTTPTransport{}
	ops := &DefaultOperations{HTTPClient: &http.Client{Transport: transport}, GOOS: "linux", GOARCH: "amd64"}
	release := Release{bundlePath: bundle, SHA256: fileDigest(t, bundle)}
	manifest, err := ops.Download(context.Background(), release, root, filepath.Join(root, "download"))
	if err != nil {
		t.Fatal(err)
	}
	if transport.called || filepath.Base(manifest) != "microvm-release-linux-amd64.json" {
		t.Fatalf("local import used HTTP=%v manifest=%q", transport.called, manifest)
	}
}

func TestPreparedDevelopmentReleaseBundleIsImportable(t *testing.T) {
	descriptorPath := os.Getenv("MECATL_MICROVM_DEV_RELEASE_DESCRIPTOR")
	if descriptorPath == "" {
		t.Skip("set MECATL_MICROVM_DEV_RELEASE_DESCRIPTOR after task microvm:dev:prepare")
	}
	data, err := os.ReadFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor DevelopmentReleaseDescriptor
	if err := json.Unmarshal(data, &descriptor); err != nil {
		t.Fatal(err)
	}
	request, err := ReadyRequestFromDevelopmentDescriptor(descriptorPath, descriptor.SourceBuildIdentity)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = request.Release.bundleFile.Close() }()

	bundle, err := os.Open(descriptor.BundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bundle.Close() }()
	zr, err := gzip.NewReader(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	tr := tar.NewReader(zr)
	entries := map[string]bool{}
	packageRoot := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(descriptor.BundlePath))), "microvm-e2e", descriptor.Platform, "package")
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." {
			t.Fatalf("development bundle contains extractor-rejected root entry %q", header.Name)
		}
		entries[name] = true
		source := filepath.Join(packageRoot, name)
		info, err := os.Lstat(source)
		if err != nil {
			t.Fatalf("stat production package member %q: %v", name, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("production package member %q has no Unix metadata", name)
		}
		if header.FileInfo().Mode() != info.Mode() || header.Uid != int(stat.Uid) || header.Gid != int(stat.Gid) || header.ModTime.Unix() != info.ModTime().Unix() {
			t.Fatalf("development bundle changed production package metadata for %q", name)
		}
		if info.Mode().IsRegular() {
			got, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read development bundle member %q: %v", name, err)
			}
			want, err := os.ReadFile(source)
			if err != nil {
				t.Fatalf("read production package member %q: %v", name, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("development bundle changed production package bytes for %q", name)
			}
		}
	}
	for _, name := range []string{"microvm-release-linux-amd64.json", "install-microvm-release.sh"} {
		if !entries[name] {
			t.Fatalf("development bundle omitted %q", name)
		}
	}

	ops := &DefaultOperations{GOOS: "linux", GOARCH: "amd64"}
	root := privateTempDir(t)
	manifest, err := ops.Download(context.Background(), request.Release, root, filepath.Join(root, "download"))
	if err != nil {
		t.Fatalf("import prepared development bundle: %v", err)
	}
	if !regularFile(manifest) || !regularFile(filepath.Join(filepath.Dir(manifest), "install-microvm-release.sh")) {
		t.Fatal("prepared development bundle did not extract expected regular files")
	}
	if err := ops.Verify(context.Background(), request.Release, manifest); err != nil {
		t.Fatalf("verify prepared development bundle: %v", err)
	}
	installed, err := ops.Install(context.Background(), manifest, root, filepath.Join(root, "installed", "verified"))
	if err != nil {
		t.Fatalf("install prepared development bundle: %v", err)
	}
	if len(installed.Artifacts) != 4 {
		t.Fatalf("installed admission artifacts = %d, want 4", len(installed.Artifacts))
	}
	for _, artifact := range installed.Artifacts {
		if artifact.Kind == "execution-image" {
			continue
		}
		if !regularFile(filepath.Join(artifact.Path, "mecatl-guest-agent")) && artifact.Kind == "guest-agent" {
			t.Fatalf("installed guest-agent artifact missing payload: %#v", artifact)
		}
		if artifact.Path == "" {
			t.Fatalf("installed %s artifact has no path", artifact.Kind)
		}
	}
}

func validDevelopmentDescriptor(t *testing.T, bundle, key string) []byte {
	t.Helper()
	value := DevelopmentReleaseDescriptor{
		Schema: DevelopmentReleaseSchema, Platform: runtime.GOOS + "-" + runtime.GOARCH, SourceBuildIdentity: "source-test",
		BundlePath: bundle, BundleSHA256: fileDigest(t, bundle), PublicKeyPath: key,
		PublicKeyIdentity: "sha256:" + fileDigest(t, key), PolicyRevision: "development-test",
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeDevelopmentBundle(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gz)
	body := []byte(`{"schema":"mecatl-microvm-release/v2"}`)
	manifestName := "microvm-release-" + runtime.GOOS + "-" + runtime.GOARCH + ".json"
	if err := tarWriter.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeOwnerOnly(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
