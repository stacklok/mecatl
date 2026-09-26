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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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
	for _, platform := range [][2]string{{"linux", "amd64"}, {"darwin", "arm64"}} {
		if !supportedPlatform(platform[0], platform[1]) {
			t.Fatalf("supported live platform rejected: %s/%s", platform[0], platform[1])
		}
	}
	for _, platform := range [][2]string{{"linux", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}} {
		if supportedPlatform(platform[0], platform[1]) {
			t.Fatalf("unsupported live platform accepted: %s/%s", platform[0], platform[1])
		}
	}
}

func TestDarwinPreflightRequiresMacOS15AndHVF(t *testing.T) {
	tests := []struct {
		name, version, hvf string
		wantErr            string
	}{
		{name: "supported", version: "15.0", hvf: "1"},
		{name: "old macOS", version: "14.7", hvf: "1", wantErr: "macOS 15"},
		{name: "HVF unavailable", version: "15.1", hvf: "0", wantErr: "hypervisor.framework"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &DefaultOperations{GOOS: "darwin", GOARCH: "arm64",
				darwinVersionProbe: func(context.Context) (string, error) { return tc.version, nil },
				darwinHVFProbe:     func(context.Context) (string, error) { return tc.hvf, nil },
			}
			err := o.Preflight(t.Context(), Paths{})
			if tc.wantErr == "" && err != nil {
				t.Fatalf("Preflight() error = %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Preflight() error = %v, want %q", err, tc.wantErr)
			}
		})
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
	t.Chdir(t.TempDir())
	path := "microvmd.sock"
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

func TestMicroVMSubprocessEnvironmentScrubsSecretsAndPreservesRuntimeValues(t *testing.T) {
	secrets := map[string]string{
		"OPENROUTER_API_KEY":    "provider-canary",
		"GH_TOKEN":              "github-canary",
		"AWS_SECRET_ACCESS_KEY": "aws-canary",
		"AZURE_CLIENT_SECRET":   "azure-canary",
		"DEPLOYMENT_TOKEN":      "shaped-canary",
	}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	kept := map[string]string{
		"HOME":          "/home/microvm-canary",
		"XDG_DATA_HOME": "/var/lib/microvm-canary",
		"SSL_CERT_FILE": "/etc/ssl/microvm-canary.pem",
		"GOTOOLCHAIN":   "local",
	}
	for name, value := range kept {
		t.Setenv(name, value)
	}
	output, err := scrubbedCommand(exec.Command("env")).Output()
	if err != nil {
		t.Fatal(err)
	}
	environment := string(output)
	for name, value := range secrets {
		if strings.Contains(environment, name+"=") || strings.Contains(environment, value) {
			t.Fatalf("scrubbed subprocess leaked %s", name)
		}
	}
	for name, value := range kept {
		if !strings.Contains(environment, name+"="+value+"\n") {
			t.Fatalf("scrubbed subprocess dropped %s", name)
		}
	}
	if !strings.Contains(environment, "PATH=") {
		t.Fatal("scrubbed subprocess dropped PATH")
	}
}

func TestDefaultOperationsStartScrubsDaemonEnvironmentAndDetachesFromRequest(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := preparePaths(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.DaemonBinary), 0o700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(root, "daemon.env")
	script := fmt.Sprintf("#!/bin/sh\n/usr/bin/env > %q\nwhile :; do sleep 1; done\n", capture)
	if err := os.WriteFile(paths.DaemonBinary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	secrets := map[string]string{
		"OPENROUTER_API_KEY": "provider-start-canary",
		"GH_TOKEN":           "github-start-canary",
		"DEPLOYMENT_TOKEN":   "shaped-start-canary",
	}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	kept := map[string]string{
		"HOME":            filepath.Join(root, "home"),
		"XDG_DATA_HOME":   filepath.Join(root, "data-home"),
		"XDG_RUNTIME_DIR": filepath.Join(root, "runtime-home"),
		"SSL_CERT_FILE":   filepath.Join(root, "cert.pem"),
	}
	for name, value := range kept {
		t.Setenv(name, value)
	}

	ctx, cancel := context.WithCancel(t.Context())
	if err := (&DefaultOperations{}).Start(ctx, paths); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel() // the detached daemon must outlive the initiating readiness request.
	pidData, err := os.ReadFile(filepath.Join(paths.StateDir, "microvmd.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = os.Remove(paths.RuntimeDir)
	})

	deadline := time.Now().Add(2 * time.Second)
	var environment string
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(capture)
		if readErr == nil && len(data) > 0 {
			environment = string(data)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if environment == "" {
		t.Fatal("detached daemon did not capture its launch environment")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("daemon did not survive initiating-request cancellation: %v", err)
	}
	for name, value := range secrets {
		if strings.Contains(environment, name+"=") || strings.Contains(environment, value) {
			t.Fatalf("daemon launch environment leaked %s", name)
		}
	}
	for name, value := range kept {
		if !strings.Contains(environment, name+"="+value+"\n") {
			t.Fatalf("daemon launch environment dropped %s", name)
		}
	}
	if !strings.Contains(environment, "PATH=") {
		t.Fatal("daemon launch environment dropped PATH")
	}
}

func TestSecureMkdirAllRejectsExistingPublicRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := secureMkdirAll(root, filepath.Join(root, "child")); err == nil || !strings.Contains(err.Error(), "not private") {
		t.Fatalf("secureMkdirAll error = %v, want existing public root rejection", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("root mode = %o, want unchanged 755", got)
	}
}

func TestMicroVMUserBootstrap_Scenario7_DownloadVerifiesBeforeExtraction(t *testing.T) {
	bundle := releaseBundle(t, "microvm-release-linux-amd64.json", []byte(`{"schema":"mecatl-microvm-release/v2"}`))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bundle) }))
	defer srv.Close()
	digest := fmt.Sprintf("%x", sha256.Sum256(bundle))
	ops := &DefaultOperations{HTTPClient: srv.Client(), GOOS: "linux", GOARCH: "amd64"}
	destRoot := privateTempDir(t)
	dest := filepath.Join(destRoot, "download")
	manifest, err := ops.Download(context.Background(), Release{URL: srv.URL, SHA256: digest}, destRoot, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := ops.Verify(context.Background(), Release{URL: srv.URL, SHA256: digest}, manifest); err != nil {
		t.Fatal(err)
	}
	if !regularFile(manifest) {
		t.Fatalf("manifest was not safely extracted: %s", manifest)
	}

	badRoot := privateTempDir(t)
	badDest := filepath.Join(badRoot, "bad")
	if _, err := ops.Download(context.Background(), Release{URL: srv.URL, SHA256: strings.Repeat("0", 64)}, badRoot, badDest); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if _, err := os.Stat(filepath.Join(badDest, "unpacked")); !os.IsNotExist(err) {
		t.Fatalf("unverified bundle was extracted: %v", err)
	}
}

func TestMicroVMFirstRunRepair_InstallerComesFromVerifiedBundle(t *testing.T) {
	root := privateTempDir(t)
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
	installed, err := (&DefaultOperations{}).Install(context.Background(), manifest, root, installRoot)
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
	downloadRoot := privateTempDir(t)
	_, err := ops.Download(context.Background(), Release{URL: srv.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(bundle))}, downloadRoot, filepath.Join(downloadRoot, "download"))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want symlink refusal", err)
	}
}

func TestMicroVMUsabilityRepair_Scenario2_InstallerComesFromVerifiedBundle(t *testing.T) {
	root := privateTempDir(t)
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
	if _, err := ops.Install(context.Background(), manifest, root, installRoot); err != nil {
		t.Fatalf("install with packaged bundle member: %v", err)
	}
	if _, err := os.Stat(packagedMarker); err != nil {
		t.Fatalf("packaged installer did not execute: %v", err)
	}
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
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
