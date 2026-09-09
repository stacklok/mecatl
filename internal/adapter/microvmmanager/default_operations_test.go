package microvmmanager

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultOperationsPreflightInvokesAndPropagatesUserNamespaceProbe(t *testing.T) {
	probeErr := errors.New("injected production userns probe failure")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	o := &DefaultOperations{GOOS: "linux", GOARCH: "amd64", usernsProbe: func(got context.Context) error {
		called = true
		if got != ctx {
			t.Fatal("Preflight did not propagate its context to the userns probe")
		}
		return probeErr
	}}
	if err := o.Preflight(ctx, Paths{}); !errors.Is(err, probeErr) {
		t.Fatalf("Preflight error = %v, want userns probe error", err)
	}
	if !called {
		t.Fatal("Preflight skipped the production userns probe")
	}
}

func TestMicroVMLivePlatformBoundary(t *testing.T) {
	if !supportedPlatform("linux", "amd64") {
		t.Fatal("Linux amd64 live platform was rejected")
	}
	for _, platform := range [][2]string{{"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		if supportedPlatform(platform[0], platform[1]) {
			t.Fatalf("unsupported live platform accepted: %s/%s", platform[0], platform[1])
		}
	}
}

func TestMicroVMUserBootstrap_Scenario14_PIDReuseWithSameBinaryIsRejected(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process-start identity is Unix-only")
	}
	paths := testPaths(t.TempDir())
	record := managedProcessRecord{
		Schema: managedProcessSchema, PID: os.Getpid(), ProcessIdentity: "stale-start-token", BinaryIdentity: "same-binary",
		Args: []string{paths.DaemonBinary, "--state-dir", paths.StateDir, "--socket", paths.Socket, "--config", paths.ConfigFile}, Socket: paths.Socket,
	}
	if err := validateManagedProcess(record, paths); err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("same-binary PID reuse error = %v, want process-start rejection", err)
	}
}

func TestDefaultOperationsWaitSocketRejectsStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "microvmd.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (&DefaultOperations{}).WaitSocket(ctx, path) }()
	select {
	case err := <-done:
		t.Fatalf("WaitSocket accepted stale socket: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	live, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitSocket rejected live socket: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("WaitSocket did not observe live daemon socket")
	}
}

func TestMicroVMUserBootstrap_Scenario7_DownloadVerifiesBeforeExtraction(t *testing.T) {
	bundle := releaseBundle(t, "microvm-release-linux-amd64.json", []byte(`{"schema":"mecatl-microvm-release/v2"}`))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bundle) }))
	defer srv.Close()
	digest := fmt.Sprintf("%x", sha256.Sum256(bundle))
	ops := &DefaultOperations{HTTPClient: srv.Client(), GOOS: "linux", GOARCH: "amd64"}
	dest := filepath.Join(t.TempDir(), "download")
	manifest, err := ops.Download(context.Background(), Release{URL: srv.URL, SHA256: digest}, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := ops.Verify(context.Background(), Release{URL: srv.URL, SHA256: digest}, manifest); err != nil {
		t.Fatal(err)
	}
	if !regularFile(manifest) {
		t.Fatalf("manifest was not safely extracted: %s", manifest)
	}

	badDest := filepath.Join(t.TempDir(), "bad")
	if _, err := ops.Download(context.Background(), Release{URL: srv.URL, SHA256: strings.Repeat("0", 64)}, badDest); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if _, err := os.Stat(filepath.Join(badDest, "unpacked")); !os.IsNotExist(err) {
		t.Fatalf("unverified bundle was extracted: %v", err)
	}
}

func TestMicroVMFirstRunRepair_InstallerComesFromVerifiedBundle(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "unpacked")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(bundle, "microvm-release-linux-amd64.json")
	if err := os.WriteFile(manifest, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	installer := filepath.Join(bundle, "install-microvm-release.sh")
	script := "#!/bin/sh\nset -eu\nprintf '%s' '{\"schema\":\"mecatl-microvmd-artifacts/v1\",\"artifacts\":[]}' > \"$2/microvmd-artifacts.json\"\n"
	if err := os.WriteFile(installer, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	writeInstallerBundle(t, filepath.Join(root, "release.tar.gz"), installer)
	if err := os.WriteFile(filepath.Join(bundle, "mecatl-microvmd-"+runtime.GOOS+"-"+runtime.GOARCH), []byte("daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(root, "installed")
	if err := os.MkdirAll(installRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	installed, err := (&DefaultOperations{}).Install(context.Background(), manifest, installRoot)
	if err != nil {
		t.Fatalf("Install with verified bundled installer: %v", err)
	}
	if installed.Artifacts == nil {
		t.Fatal("bundled installer result was not decoded")
	}
}

func TestMicroVMUserBootstrap_Scenario8_BundleSymlinkIsRejected(t *testing.T) {
	var raw bytes.Buffer
	zw := gzip.NewWriter(&raw)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "microvm-release-linux-amd64.json", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	bundle := raw.Bytes()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bundle) }))
	defer srv.Close()
	ops := &DefaultOperations{HTTPClient: srv.Client(), GOOS: "linux", GOARCH: "amd64"}
	_, err := ops.Download(context.Background(), Release{URL: srv.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(bundle))}, filepath.Join(t.TempDir(), "download"))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want symlink refusal", err)
	}
}

func TestMicroVMUsabilityRepair_Scenario2_InstallerComesFromVerifiedBundle(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "unpacked")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(assets, "microvm-release-linux-amd64.json")
	if err := os.WriteFile(manifest, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "mecatl-microvmd-linux-amd64"), []byte("daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	packagedMarker := filepath.Join(root, "packaged-ran")
	packagedInstaller := filepath.Join(assets, "install-microvm-release.sh")
	writeInstallerFixture(t, packagedInstaller, packagedMarker)
	writeInstallerBundle(t, filepath.Join(root, "release.tar.gz"), packagedInstaller)

	installRoot := filepath.Join(root, "install", "artifacts")
	ops := &DefaultOperations{GOOS: "linux", GOARCH: "amd64"}
	if _, err := ops.Install(context.Background(), manifest, installRoot); err != nil {
		t.Fatalf("install with packaged bundle member: %v", err)
	}
	if _, err := os.Stat(packagedMarker); err != nil {
		t.Fatalf("packaged installer did not execute: %v", err)
	}
}

func writeInstallerFixture(t *testing.T, path, marker string) {
	t.Helper()
	script := "#!/bin/sh\nset -eu\ntouch '" + marker + "'\nmkdir -p \"$2\"\nprintf '%s' '{\"schema\":\"mecatl-microvmd-artifacts/v1\",\"artifacts\":[]}' > \"$2/microvmd-artifacts.json\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeInstallerBundle(t *testing.T, path, installer string) {
	t.Helper()
	data, err := os.ReadFile(installer)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(file)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "./install-microvm-release.sh", Typeflag: tar.TypeReg, Size: int64(len(data)), Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func releaseBundle(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	zw := gzip.NewWriter(&raw)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(data)), Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}
