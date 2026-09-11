//go:build microvm_e2e

package microvm_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
)

const microVME2EPolicy = "microvm-production-e2e-v1"

func TestMicroVMDefaultPlacementDailyHarnessJourney(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatalf("Linux amd64 KVM is the only live microVM target, got %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	assertKVMAccess(t)

	rootParent := requiredAbsoluteEnv(t, "MECATL_MICROVM_E2E_ROOT")
	root, err := os.MkdirTemp(rootParent, "daily-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("retained failed production E2E state at %s", root)
			return
		}
		_ = os.RemoveAll(root)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	t.Cleanup(cancel)

	source := filepath.Join(root, "source")
	initE2ERepository(t, source)
	ready, paths := prepareManagedRelease(t, ctx, root)
	manager, endpoint, err := microvmmanager.DefaultLocal()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopManagedDaemon(t, paths) })
	if _, err := manager.EnsureReady(ctx, ready); err != nil {
		t.Fatalf("prepare microVM deployment default: %v", err)
	}

	const scope server.PlacementScope = "deployment"
	placement, err := microvmadapter.NewPlacementProvider(endpoint, source, microvmmanager.Alias, scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("daily-bash", "Shell", json.RawMessage(`{"command":"cat tracked.txt && printf harness-change > journey.txt"}`))),
		mockllm.TextTurn("done"),
	)
	built, err := app.Build(ctx, app.Config{
		Workspace: source, StoreDir: filepath.Join(root, "store"), MockProvider: provider,
		Shell: "/bin/sh", AllowAllTools: true,
		PlacementProvider: placement, PlacementScope: scope,
		EnvironmentForkers: map[session.EnvironmentKind]tool.EnvironmentForker{"microvm": placement},
		EnvironmentMergers: map[session.EnvironmentKind]tool.EnvironmentMerger{"microvm": placement},
	})
	if err != nil {
		t.Fatalf("build harness with microVM deployment default: %v", err)
	}
	defer built.Close()

	// This is deliberately the ordinary no-selector API. The deployment, not the
	// client, chooses microvm-local as the default placement.
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("normal CreateSession: %v", err)
	}
	defer func() {
		if err := placement.Delete(context.WithoutCancel(ctx), sess); err != nil {
			t.Errorf("delete exact microVM generation: %v", err)
		}
	}()
	if sess.EnvironmentRef.Kind != "microvm" || sess.EnvironmentRef.ID == "" || sess.EnvironmentRef.Revision == "" {
		t.Fatalf("CreateSession did not persist an exact microVM placement: %+v", sess.EnvironmentRef)
	}

	run, err := built.Service.StartRunContent(ctx, sess.ID, "perform the daily filesystem operation", nil)
	if err != nil {
		t.Fatalf("start harness run: %v", err)
	}
	var result *session.ResultPayload
	var toolSucceeded bool
	for event := range run.Events() {
		if event.ToolResult != nil && event.ToolResult.CallID == "daily-bash" && !event.ToolResult.IsError {
			toolSucceeded = strings.Contains(event.ToolResult.Content, "base")
		}
		if event.Result != nil {
			result = event.Result
		}
	}
	if result == nil || result.Stop == session.StopError || !toolSucceeded {
		t.Fatalf("guest-backed harness operation did not complete: result=%+v tool_succeeded=%v", result, toolSucceeded)
	}

	binding, err := placement.Reattach(ctx, server.PlacementReattachRequest{Ref: sess.EnvironmentRef, Principal: sess.Owner, Scope: scope})
	if err != nil {
		t.Fatalf("reattach exact guest generation: %v", err)
	}
	data, err := binding.Environment.Workspace().Read(ctx, "journey.txt")
	if err != nil || string(data) != "harness-change" {
		t.Fatalf("guest workspace did not retain harness change: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(source, "journey.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("guest change escaped its isolated worktree into the source checkout: %v", err)
	}
	pwd := mustGuestRun(t, ctx, binding.Environment.CommandRunner(), "pwd").Stdout
	if !strings.HasPrefix(strings.TrimSpace(pwd), "/run/mecatl/repositories/") {
		t.Fatalf("guest command ran outside its isolated repository worktree: %q", pwd)
	}
}

func prepareManagedRelease(t *testing.T, ctx context.Context, root string) (microvmmanager.ReadyRequest, microvmmanager.Paths) {
	t.Helper()
	packageDir := requiredAbsoluteEnv(t, "MECATL_MICROVM_RELEASE_ASSETS")
	bundleDir := filepath.Join(root, "release")
	if err := copyE2ETree(packageDir, bundleDir); err != nil {
		t.Fatalf("copy packaged release: %v", err)
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	if err := copyE2EFile(requiredAbsoluteEnv(t, "MECATL_MICROVMD_BIN"), filepath.Join(bundleDir, "mecatl-microvmd-"+platform), 0o755); err != nil {
		t.Fatalf("project production daemon into release: %v", err)
	}
	bundle := filepath.Join(root, "microvm-release.tar.gz")
	if err := archiveE2ETree(bundleDir, bundle); err != nil {
		t.Fatalf("archive production release: %v", err)
	}
	bundleData, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(bundleData)
	}))
	t.Cleanup(server.Close)
	certificate := filepath.Join(root, "release-ca.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	defaults, err := json.Marshal(map[string]any{platform: map[string]string{
		"version": "e2e", "platform": platform, "url": server.URL + "/microvm-release.tar.gz",
		"sha256": fmt.Sprintf("%x", sha256.Sum256(bundleData)), "policy_revision": microVME2EPolicy,
		"certificate_identity": "https://github.com/stacklok/mecatl/.github/workflows/release.yml@refs/tags/e2e",
		"oidc_issuer":          "https://token.actions.githubusercontent.com",
	}})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := microvmmanager.ReadyRequestFromDefaults(base64.StdEncoding.EncodeToString(defaults), "e2e")
	if err != nil {
		t.Fatal(err)
	}
	publisher := requiredAbsoluteEnv(t, "MECATL_MICROVM_RELEASE_PUBLIC_KEY")
	publisherData, err := os.ReadFile(publisher)
	if err != nil {
		t.Fatal(err)
	}
	identity := sha256.Sum256(publisherData)
	ready.Policy.CertificateIdentity, ready.Policy.OIDCIssuer = "", ""
	ready.Policy.PublicKey = publisher
	ready.Policy.PublicKeyIdentity = fmt.Sprintf("sha256:%x", identity)

	home := filepath.Join(root, "home")
	for key, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME": filepath.Join(home, ".local", "share"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
		"XDG_RUNTIME_DIR": filepath.Join(requiredAbsoluteEnv(t, "MECATL_MICROVM_E2E_SOCKET_ROOT"), "daily-"+fmt.Sprint(os.Getpid())),
		"SSL_CERT_FILE":   certificate,
	} {
		t.Setenv(key, value)
	}
	paths, err := microvmmanager.DefaultPaths(microvmmanager.HostPaths{Home: home, UID: os.Getuid(), GOOS: runtime.GOOS})
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx // keeps preparation explicitly tied to the caller's bounded lifecycle
	return ready, paths
}

func requiredAbsoluteEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" || !filepath.IsAbs(value) {
		t.Fatalf("%s must be an absolute path", name)
	}
	return value
}

func assertKVMAccess(t *testing.T) {
	t.Helper()
	file, err := os.OpenFile("/dev/kvm", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("KVM-authorized runner cannot open /dev/kvm read-write: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func stopManagedDaemon(t *testing.T, paths microvmmanager.Paths) {
	t.Helper()
	if _, err := os.Lstat(paths.Socket); errors.Is(err, fs.ErrNotExist) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := (&microvmmanager.DefaultOperations{}).Stop(ctx, paths); err != nil {
		t.Errorf("stop managed microvmd: %v", err)
	}
}

func initE2ERepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"config", "user.name", "MicroVM E2E"}, {"config", "user.email", "microvm-e2e@example.invalid"}} {
		if output, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-m", "base"}} {
		if output, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
}

func copyE2ETree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyE2EFile(path, target, info.Mode().Perm())
	})
}

func copyE2EFile(source, destination string, mode fs.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	return os.WriteFile(destination, data, mode)
}

func archiveE2ETree(source, destination string) error {
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(file)
	tw := tar.NewWriter(zw)
	walkErr := filepath.Walk(source, func(path string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil || path == source {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil || !info.Mode().IsRegular() {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	for _, closer := range []io.Closer{tw, zw, file} {
		if err := closer.Close(); walkErr == nil {
			walkErr = err
		}
	}
	return walkErr
}

func mustGuestRun(t *testing.T, ctx context.Context, runner tool.CommandRunner, command string) tool.CommandResult {
	t.Helper()
	result, err := runner.Run(ctx, command)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("guest command %q = %+v, %v", command, result, err)
	}
	return result
}
