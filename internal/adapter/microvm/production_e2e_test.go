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
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
)

const microVME2EPolicy = "microvm-production-e2e-v1"

func dailyHarnessProbe(hostCanary, managerConfig string) string {
	return fmt.Sprintf(`fail() { echo "probe failed: $1" >&2; exit 1; }
test "$(id -u)" = 65532 || fail workload-uid
test ! -e %q || fail host-canary-visible
test ! -e %q || fail manager-config-visible
if cat /etc/mecatl/guest-agent.json >/dev/null 2>&1; then fail guest-agent-config-readable; fi
shared_objects=%q
test -d "$shared_objects" || fail shared-object-store-missing
test -r "$shared_objects" || fail shared-object-store-unreadable
if touch "$shared_objects/mecatl-write-forbidden" 2>/dev/null; then
  rm -f "$shared_objects/mecatl-write-forbidden"
  fail shared-object-store-writable
fi
private_objects=$(git rev-parse --git-path objects) || fail private-object-path
case "$private_objects" in "$shared_objects") fail private-object-store-aliases-shared-export ;; esac
touch "$private_objects/mecatl-private-write" || fail private-object-store-unwritable
rm "$private_objects/mecatl-private-write" || fail private-object-cleanup
mkdir -p "$HOME/private" "${XDG_CACHE_HOME:-$HOME/.cache}/mecatl" || fail private-directory-create
printf private > "$HOME/private/proof" || fail home-write
printf cache > "${XDG_CACHE_HOME:-$HOME/.cache}/mecatl/proof" || fail cache-write
cat tracked.txt || fail source-read
printf harness-change > journey.txt || fail workspace-write`, hostCanary, managerConfig, worktree.GuestObjectStore)
}

func TestDailyHarnessProbeSeparatesSharedAndPrivateObjectStores(t *testing.T) {
	probe := dailyHarnessProbe("/host/canary", "/host/manager-config")
	for _, required := range []string{
		`shared_objects="` + worktree.GuestObjectStore + `"`,
		`touch "$shared_objects/mecatl-write-forbidden"`,
		`private_objects=$(git rev-parse --git-path objects)`,
		`touch "$private_objects/mecatl-private-write"`,
	} {
		if !strings.Contains(probe, required) {
			t.Fatalf("daily harness probe omits %q", required)
		}
	}
	if strings.Contains(probe, `touch "$(git rev-parse --git-path objects)/mecatl-write-forbidden"`) {
		t.Fatal("daily harness probe tests the writable private object store as the read-only export")
	}
}

func TestMicroVMDefaultPlacementDailyHarnessJourney(t *testing.T) {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		assertKVMAccess(t)
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		if err := (&microvmmanager.DefaultOperations{}).Preflight(t.Context(), microvmmanager.Paths{}); err != nil {
			t.Fatalf("Darwin arm64 macOS 15/HVF preflight: %v", err)
		}
	default:
		t.Fatalf("live microVM journey requires Linux amd64 KVM or Darwin arm64 macOS 15+ HVF, got %s/%s", runtime.GOOS, runtime.GOARCH)
	}

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
	manager := microvmmanager.New(paths, &microvmmanager.DefaultOperations{})
	endpoint := "unix://" + paths.Socket
	t.Cleanup(func() { stopManagedDaemon(t, paths) })
	if _, err := manager.EnsureReady(ctx, ready); err != nil {
		t.Fatalf("prepare microVM deployment default: %v", err)
	}

	const scope server.PlacementScope = "deployment"
	placement, err := microvmadapter.NewPlacementProvider(endpoint, source, microvmmanager.Alias, scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	hostCanary := filepath.Join(root, "host-config-canary")
	if err := os.WriteFile(hostCanary, []byte("must-never-enter-guest"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("daily-read", "Read", json.RawMessage(`{"path":"tracked.txt"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("daily-write", "Write", json.RawMessage(`{"path":"written-by-tool.txt","content":"filesystem-write"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("forbidden-read", "Read", json.RawMessage(fmt.Sprintf(`{"path":%q}`, hostCanary)))),
		mockllm.ToolCallTurn(session.NewToolCall("daily-bash", "Shell", json.RawMessage(fmt.Sprintf(`{"command":%q}`, dailyHarnessProbe(hostCanary, paths.ConfigFile))))),
		mockllm.TextTurn("done"),
	)
	built, err := app.Build(ctx, app.Config{
		Workspace: source, StoreDir: filepath.Join(root, "store"), MockProvider: provider,
		Shell: "/bin/sh", AllowAllTools: true, NoSoul: true, LearningMode: learning.Off,
		MemoryDir: filepath.Join(root, "memory"), UserModelDir: filepath.Join(root, "user-model"),
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
	toolResults := make(map[session.ToolCallID]session.ToolResult)
	for event := range run.Events() {
		if event.ToolResult != nil {
			toolResults[event.ToolResult.CallID] = *event.ToolResult
		}
		if event.Result != nil {
			result = event.Result
		}
	}
	if result == nil || result.Stop == session.StopError {
		t.Fatalf("guest-backed harness operation did not complete: result=%+v", result)
	}
	for _, callID := range []session.ToolCallID{"daily-read", "daily-write", "daily-bash"} {
		if got, ok := toolResults[callID]; !ok || got.IsError {
			t.Fatalf("guest-backed %s operation failed: %+v", callID, got)
		}
	}
	if !strings.Contains(toolResults["daily-read"].Content, "base") || !strings.Contains(toolResults["daily-bash"].Content, "base") {
		t.Fatalf("filesystem Read/Bash did not observe guest source: read=%+v bash=%+v", toolResults["daily-read"], toolResults["daily-bash"])
	}
	if got := toolResults["forbidden-read"]; !got.IsError || strings.Contains(got.Content, "must-never-enter-guest") {
		t.Fatalf("forbidden host-path attempt did not fail closed: %+v", got)
	}

	binding, err := placement.Reattach(ctx, server.PlacementReattachRequest{Ref: sess.EnvironmentRef, Principal: sess.Owner, Scope: scope})
	if err != nil {
		t.Fatalf("reattach exact guest generation: %v", err)
	}
	data, err := binding.Environment.Workspace().Read(ctx, "journey.txt")
	if err != nil || string(data) != "harness-change" {
		t.Fatalf("guest workspace did not retain harness change: %q, %v", data, err)
	}
	data, err = binding.Environment.Workspace().Read(ctx, "written-by-tool.txt")
	if err != nil || string(data) != "filesystem-write" {
		t.Fatalf("filesystem Write did not persist in guest workspace: %q, %v", data, err)
	}
	for _, escaped := range []string{"journey.txt", "written-by-tool.txt"} {
		if _, err := os.Stat(filepath.Join(source, escaped)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("guest change %s escaped its isolated worktree into the source checkout: %v", escaped, err)
		}
	}
	child, cleanupChild, _, err := placement.Fork(ctx, binding.Environment, "daily-nonconflict")
	if err != nil {
		t.Fatalf("fork post-boot logical child: %v", err)
	}
	defer func() {
		if err := cleanupChild(); err != nil {
			t.Errorf("cleanup merged logical child: %v", err)
		}
	}()
	childIdentity := mustGuestRun(t, ctx, child.CommandRunner(), "test \"$(id -u)\" = 65532 && printf child-change > child.txt && chmod 755 child.txt && stat -c '%u:%g:%a' child.txt 2>/dev/null || stat -f '%u:%g:%Lp' child.txt").Stdout
	if !strings.Contains(childIdentity, "65532:65532:755") {
		t.Fatalf("post-boot child ownership/mode = %q, want 65532:65532:755", childIdentity)
	}
	if err := placement.Merge(ctx, child, binding.Environment); err != nil {
		t.Fatalf("merge non-conflicting isolated child: %v", err)
	}
	merged := mustGuestRun(t, ctx, binding.Environment.CommandRunner(), "cat child.txt && (stat -c '%a' child.txt 2>/dev/null || stat -f '%Lp' child.txt)").Stdout
	if !strings.Contains(merged, "child-change") || !strings.Contains(merged, "755") {
		t.Fatalf("parent did not read merged child bytes/mode: %q", merged)
	}

	conflict, cleanupConflict, _, err := placement.Fork(ctx, binding.Environment, "daily-conflict")
	if err != nil {
		t.Fatalf("fork conflicting logical child: %v", err)
	}
	defer func() { _ = cleanupConflict() }()
	mustGuestRun(t, ctx, conflict.CommandRunner(), "printf child-conflict > tracked.txt")
	mustGuestRun(t, ctx, binding.Environment.CommandRunner(), "printf parent-conflict > tracked.txt")
	if err := placement.Merge(ctx, conflict, binding.Environment); err == nil {
		t.Fatal("conflicting isolated-child merge unexpectedly succeeded")
	}
	if got := mustGuestRun(t, ctx, conflict.CommandRunner(), "cat tracked.txt").Stdout; !strings.Contains(got, "child-conflict") {
		t.Fatalf("conflict did not preserve exact child: %q", got)
	}
	if got := mustGuestRun(t, ctx, binding.Environment.CommandRunner(), "cat tracked.txt").Stdout; !strings.Contains(got, "parent-conflict") {
		t.Fatalf("conflicting merge changed parent: %q", got)
	}

	mustGuestRun(t, ctx, binding.Environment.CommandRunner(), "printf '\\ndirty-after-restart' >> tracked.txt && printf staged-after-restart > staged.txt && git add staged.txt && printf untracked-after-restart > untracked.txt && mkdir -p /home/guest && printf rootfs-after-restart > /home/guest/restart-marker")
	beforeRestart := readOnlyRepositoryBootRecord(t, paths.StateDir)
	if err := (&microvmmanager.DefaultOperations{}).Stop(ctx, paths); err != nil {
		t.Fatalf("stop managed microvmd for restart qualification: %v", err)
	}
	if _, err := manager.EnsureReady(ctx, ready); err != nil {
		t.Fatalf("restart managed microvmd over retained state: %v", err)
	}
	restarted, err := placement.Reattach(ctx, server.PlacementReattachRequest{Ref: sess.EnvironmentRef, Principal: sess.Owner, Scope: scope})
	if err != nil {
		t.Fatalf("reattach exact placement after actual microvmd restart: %v", err)
	}
	if restarted.Environment.Ref() != sess.EnvironmentRef {
		t.Fatalf("restart changed exact environment ref: got %+v want %+v", restarted.Environment.Ref(), sess.EnvironmentRef)
	}
	status := mustGuestRun(t, ctx, restarted.Environment.CommandRunner(), "git status --porcelain=v1 && printf '\\n--tracked--\\n' && cat tracked.txt && printf '\\n--staged--\\n' && cat staged.txt && printf '\\n--untracked--\\n' && cat untracked.txt && printf '\\n--rootfs--\\n' && cat /home/guest/restart-marker && printf '\\n--private-home--\\n' && cat \"$HOME/private/proof\" && printf '\\n--private-cache--\\n' && cat \"${XDG_CACHE_HOME:-$HOME/.cache}/mecatl/proof\" && printf '\\n--merged--\\n' && cat child.txt").Stdout
	for _, preserved := range []string{" M tracked.txt", "A  staged.txt", "?? untracked.txt", "dirty-after-restart", "staged-after-restart", "untracked-after-restart", "rootfs-after-restart", "private", "cache", "child-change"} {
		if !strings.Contains(status, preserved) {
			t.Fatalf("microvmd restart did not preserve %q in guest state:\n%s", preserved, status)
		}
	}
	afterRestart := readOnlyRepositoryBootRecord(t, paths.StateDir)
	if afterRestart.Generation <= beforeRestart.Generation || afterRestart.AuthorityDigest == beforeRestart.AuthorityDigest || afterRestart.AuthorityDigest == "" {
		t.Fatalf("restart did not establish new boot authority: before=%+v after=%+v", beforeRestart, afterRestart)
	}
	binding = restarted
	pwd := mustGuestRun(t, ctx, binding.Environment.CommandRunner(), "pwd").Stdout
	if !strings.HasPrefix(strings.TrimSpace(pwd), "/run/mecatl/repositories/") {
		t.Fatalf("guest command ran outside its isolated repository worktree: %q", pwd)
	}
	if runtime.GOOS == "linux" {
		mustGuestRun(t, ctx, binding.Environment.CommandRunner(), "grep -q '^Groups:[[:space:]]*$' /proc/self/status")
	} else {
		mustGuestRun(t, ctx, binding.Environment.CommandRunner(), "test \"$(id -u)\" = 65532")
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
	runtimeID := sha256.Sum256([]byte(root))
	for key, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME": filepath.Join(home, ".local", "share"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
		"XDG_RUNTIME_DIR": filepath.Join(requiredAbsoluteEnv(t, "MECATL_MICROVM_E2E_SOCKET_ROOT"), fmt.Sprintf("mve-%x", runtimeID[:4])),
		"SSL_CERT_FILE":   certificate,
	} {
		t.Setenv(key, value)
	}
	paths, err := microvmmanager.DefaultPaths(microvmmanager.HostPaths{Home: home, UID: os.Getuid(), GOOS: runtime.GOOS})
	if err != nil {
		t.Fatal(err)
	}
	paths.RuntimeDir = filepath.Join(requiredAbsoluteEnv(t, "MECATL_MICROVM_E2E_SOCKET_ROOT"), fmt.Sprintf("m%x", runtimeID[:4]))
	paths.Socket = filepath.Join(paths.RuntimeDir, "microvmd.sock")
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

type e2eRepositoryBootRecord struct {
	Generation      uint32 `json:"generation"`
	AuthorityDigest string `json:"authority_digest"`
}

func readOnlyRepositoryBootRecord(t *testing.T, stateDir string) e2eRepositoryBootRecord {
	t.Helper()
	var records []string
	err := filepath.WalkDir(filepath.Join(stateDir, "repositories"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == "registry.json" {
			records = append(records, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inspect repository boot record: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("repository boot record count = %d, want 1: %v", len(records), records)
	}
	data, err := os.ReadFile(records[0])
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Record struct {
			Boot e2eRepositoryBootRecord `json:"boot"`
		} `json:"record"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode repository boot record: %v", err)
	}
	if document.Record.Boot.Generation == 0 || document.Record.Boot.AuthorityDigest == "" {
		t.Fatalf("incomplete repository boot record: %+v", document.Record.Boot)
	}
	return document.Record.Boot
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
