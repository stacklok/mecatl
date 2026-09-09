//go:build microvm_e2e

package microvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/go-microvm/extract"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

const e2eTimeout = 4 * time.Minute

func TestMicroVMEnvironments_Scenario8_HostSecretAndSiblingIsolation(t *testing.T) {
	assertSupportedE2EPlatform(t)
	artifacts := loadE2EArtifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	root := t.TempDir()
	source := initE2ERepository(t, root)
	for _, dir := range []string{filepath.Join(root, "worktrees"), filepath.Join(root, "metadata")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	secretRoot := filepath.Join(root, "host-only")
	if err := os.Mkdir(secretRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	secretFiles := map[string]string{
		"provider-credential": "provider-secret-e2e", "mcp-credential": "mcp-secret-e2e",
		"identity-credential": "identity-secret-e2e", "registry-credential": "registry-secret-e2e",
	}
	for name, value := range secretFiles {
		if err := os.WriteFile(filepath.Join(secretRoot, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	controlSocket := filepath.Join(secretRoot, "microvmd.sock")
	controlListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: controlSocket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controlListener.Close() })
	t.Setenv("OPENROUTER_API_KEY", "provider-env-secret-e2e")
	t.Setenv("MCP_TOKEN", "mcp-env-secret-e2e")
	t.Setenv("GH_TOKEN", "identity-env-secret-e2e")

	registry, err := OpenFileRegistry(filepath.Join(root, "state", "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	placements, err := NewFileSessionPersister(filepath.Join(root, "state", "placements.json"))
	if err != nil {
		t.Fatal(err)
	}
	worktrees := NewGitWorktrees()
	runtimeAdapter, err := NewGoMicroVMRuntime(GoMicroVMRuntimeConfig{
		Backend: NewLibkrunBackend(), Network: NewHostedNetworkController(e2eGuestNetwork{}),
		GuestEgress:       GuestEgressPolicy{Mode: EgressAllowlist, Allow: []EgressDestination{{Hostname: "example.com", Port: 80, Protocol: ProtocolTCP}}},
		UnixGuestEndpoint: true, CapabilityKey: e2eCapabilityKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpointDir := shortE2EEndpointDir(t)
	lifecycle := NewLifecycle(LifecycleDeps{
		Identities: IdentitySequence{EndpointDir: endpointDir},
		Worktrees:  worktrees, Artifacts: e2eArtifactVerifier{artifacts: artifacts}, VMs: runtimeAdapter,
		Protocol: runtimeAdapter, Registry: registry, Sessions: placements,
	})

	type createdResult struct {
		created CreatedEnvironment
		err     error
	}
	results := make(chan createdResult, 2)
	for index := range 2 {
		go func() {
			request := CreateRequest{
				Owner: "e2e-owner", SessionID: fmt.Sprintf("e2e-session-%d", index), Profile: "e2e",
				Worktree:         worktree.Request{Source: source, WorktreePath: filepath.Join(root, "worktrees", strconv.Itoa(index)), MetadataPath: filepath.Join(root, "metadata", strconv.Itoa(index)), Branch: fmt.Sprintf("mecatl/e2e-%d", index)},
				ArtifactRequests: e2eArtifactRequests(artifacts), Resources: ResourceUsage{CPU: 1, RAMBytes: 256 << 20},
			}
			created, createErr := lifecycle.Create(ctx, request)
			results <- createdResult{created: created, err: createErr}
		}()
	}
	created := make([]CreatedEnvironment, 2)
	for i := range created {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent environment %d creation: %v", i, result.err)
		}
		created[i] = result.created
		if created[i].Ref.ID == "" {
			t.Fatalf("concurrent environment %d was not created", i)
		}
	}
	if created[0].Ref == created[1].Ref || created[0].HostWorktree == created[1].HostWorktree {
		t.Fatalf("concurrent sessions collided: %+v %+v", created[0], created[1])
	}

	records := make([]EnvironmentRecord, 2)
	services := make([]*e2eServices, 2)
	for i, item := range created {
		environmentID, _, parseErr := parseEnvironmentRef(item.Ref)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		records[i], err = registry.Lookup(ctx, environmentID)
		if err != nil {
			t.Fatal(err)
		}
		guestServices, serviceErr := runtimeAdapter.Services(item.Ref)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		services[i] = &e2eServices{Workspace: guestServices.Workspace, Runner: guestServices.Runner}
	}

	primary, sibling := services[0], records[1]
	data, version, err := primary.Workspace.ReadVersion(ctx, "tracked.txt")
	if err != nil || string(data) != "base\n" {
		t.Fatalf("positive workspace read = %q, %v", data, err)
	}
	primary.Workspace.RecordRead("tracked.txt", version)
	var versionMismatch *tool.VersionMismatchError
	if _, err := primary.Workspace.ReplaceFile(ctx, "tracked.txt", tool.NewFileVersion("stale"), []byte("bad\n")); !errors.As(err, &versionMismatch) {
		t.Fatalf("stale Workspace CAS = %v, want version mismatch", err)
	}
	if _, err := primary.Workspace.ReplaceFile(ctx, "tracked.txt", version, []byte("guest-edit\n")); err != nil {
		t.Fatalf("Workspace CAS replace: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(records[0].WorktreePath, "tracked.txt"))); got != "guest-edit\n" {
		t.Fatalf("virtiofs host visibility = %q", got)
	}

	gitProbe, err := primary.Runner.Run(ctx, "git -C /workspace status --porcelain=v1 >/dev/null && test \"$(git -C /workspace rev-parse --git-dir)\" = /run/mecatl/git-metadata && cat /run/mecatl/git-metadata/HEAD")
	if err != nil || gitProbe.ExitCode != 0 || !strings.Contains(gitProbe.Stdout, "refs/heads/mecatl/e2e-") {
		t.Fatalf("guest Git did not consume /workspace-local metadata: %+v err=%v", gitProbe, err)
	}
	workloadProbe, err := primary.Runner.Run(ctx, "test \"$(id -u)\" -ne 0 && grep -q '^CapEff:[[:space:]]*0000000000000000$' /proc/self/status && printf workload-ok > workload-probe && cat workload-probe")
	if err != nil || workloadProbe.ExitCode != 0 || workloadProbe.Stdout != "workload-ok" {
		t.Fatalf("unprivileged workload positive control failed: %+v err=%v", workloadProbe, err)
	}
	assertGuestCommandDenied(t, ctx, primary.Runner, "guest capability key", "cat /etc/mecatl/guest-agent.json >/dev/null 2>&1")
	assertGuestCommandDenied(t, ctx, primary.Runner, "guest IPv6 sysctl", "value=$(cat /proc/sys/net/ipv6/conf/all/disable_ipv6) && printf %s \"$value\" > /proc/sys/net/ipv6/conf/all/disable_ipv6")
	assertGuestCommandDenied(t, ctx, primary.Runner, "guest agent signal", "kill -0 1")
	assertGuestCommandDenied(t, ctx, primary.Runner, "guest mount policy", "mkdir -p /workspace/.mount-probe && mount -t tmpfs none /workspace/.mount-probe && umount /workspace/.mount-probe")
	streamer, ok := primary.Runner.(tool.CommandStreamer)
	if !ok {
		t.Fatal("microVM runner does not implement exec streaming")
	}
	var stream bytes.Buffer
	exit, err := streamer.RunStreaming(ctx, "printf 'stdout-one\\n'; printf 'stderr-two\\n' >&2", &stream)
	if err != nil || exit != 0 || !strings.Contains(stream.String(), "stdout-one") || !strings.Contains(stream.String(), "stderr-two") {
		t.Fatalf("exec streaming = exit %d output %q err %v", exit, stream.String(), err)
	}
	cancelCtx, cancelExec := context.WithCancel(ctx)
	cancelDone := make(chan error, 1)
	go func() {
		_, runErr := primary.Runner.Run(cancelCtx, "trap '' TERM; sleep 60 & wait")
		cancelDone <- runErr
	}()
	time.AfterFunc(250*time.Millisecond, cancelExec)
	select {
	case cancelErr := <-cancelDone:
		if !errors.Is(cancelErr, context.Canceled) {
			t.Fatalf("cancelled exec = %v", cancelErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled exec did not terminate its guest process group")
	}

	allowed, err := primary.Runner.Run(ctx, "wget -qO- http://example.com/")
	if err != nil || allowed.ExitCode != 0 || !strings.Contains(allowed.Stdout, "Example Domain") {
		t.Fatalf("allowed guest egress failed: %+v err=%v", allowed, err)
	}
	deniedCtx, deniedCancel := context.WithTimeout(ctx, 3*time.Second)
	defer deniedCancel()
	denied, _ := primary.Runner.Run(deniedCtx, "wget -T 2 -qO- http://example.org/")
	if denied.ExitCode == 0 {
		t.Fatalf("non-allowlisted guest egress succeeded: %+v", denied)
	}

	siblingRead, err := primary.Runner.Run(ctx, "cat "+shellQuote(sibling.Binding.AssignedRoot+"/tracked.txt"))
	if err != nil || siblingRead.ExitCode != 0 || siblingRead.Stdout != "base\n" {
		t.Fatalf("same-repository Bash could not address sibling guest worktree: %+v err=%v", siblingRead, err)
	}
	for label, hostPath := range map[string]string{
		"provider credential": filepath.Join(secretRoot, "provider-credential"), "MCP credential": filepath.Join(secretRoot, "mcp-credential"),
		"identity credential": filepath.Join(secretRoot, "identity-credential"), "registry credential": filepath.Join(secretRoot, "registry-credential"),
		"unrelated host root": filepath.Join(source, "tracked.txt"),
		"sibling worktree":    filepath.Join(sibling.WorktreePath, "tracked.txt"),
	} {
		assertGuestCannotRead(t, ctx, primary.Runner, label, hostPath)
	}
	for label, hostPath := range map[string]string{"daemon control socket": controlSocket, "sibling guest endpoint": sibling.Endpoint} {
		assertGuestCannotSee(t, ctx, primary.Runner, label, hostPath)
	}
	environment, err := primary.Runner.Run(ctx, "env")
	if err != nil || environment.ExitCode != 0 {
		t.Fatalf("guest env: %+v err=%v", environment, err)
	}
	for _, forbidden := range []string{"provider-env-secret-e2e", "mcp-env-secret-e2e", "identity-env-secret-e2e"} {
		if strings.Contains(environment.Stdout, forbidden) {
			t.Fatalf("host credential entered guest environment: %q", forbidden)
		}
	}

	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, Registry: registry, Runtime: runtimeAdapter, Worktrees: RetentionAdapter{Worktrees: worktrees}})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	binding := bindingForRecord(records[0])
	if response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: binding}); response.Err != nil {
		t.Fatalf("detach: %v", response.Err)
	}
	if response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: binding}); response.Err != nil {
		t.Fatalf("reattach resolve: %v", response.Err)
	}
	if _, err := runtimeAdapter.Negotiate(ctx, records[0].Endpoint, binding); err != nil {
		t.Fatalf("reattach negotiate: %v", err)
	}
	reattached, err := runtimeAdapter.Services(records[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if result, runErr := reattached.Runner.Run(ctx, "cat tracked.txt"); runErr != nil || result.Stdout != "guest-edit\n" {
		t.Fatalf("reattached exec = %+v err=%v", result, runErr)
	}

	for _, record := range records {
		response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: bindingForRecord(record)})
		if response.Err != nil || response.Record == nil || response.Record.State != EnvironmentDestroyed || !response.Record.Tombstone {
			t.Fatalf("delete exact generation: %+v", response)
		}
		if _, statErr := os.Stat(record.Endpoint); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("deleted guest endpoint remains: %v", statErr)
		}
	}
}

type e2eServices struct {
	Workspace tool.Workspace
	Runner    tool.CommandRunner
}

type e2eGuestNetwork struct{}

func (e2eGuestNetwork) DisableIPv6(context.Context) error { return nil }

func e2eCapabilityKey() ([]byte, error) { return bytes.Repeat([]byte{0x6d}, 32), nil }

type e2eArtifactVerifier struct{ artifacts VerifiedArtifacts }

func (v e2eArtifactVerifier) Verify(_ context.Context, _ map[ArtifactKind]ArtifactRequest) (VerifiedArtifacts, string, error) {
	return v.artifacts, "go-microvm-v0.0.40", nil
}

func assertGuestCommandDenied(t *testing.T, ctx context.Context, runner tool.CommandRunner, label, command string) {
	t.Helper()
	result, err := runner.Run(ctx, command)
	if err != nil {
		t.Fatalf("probe %s: %v", label, err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("model workload controls forbidden %s", label)
	}
}

func assertGuestCannotRead(t *testing.T, ctx context.Context, runner tool.CommandRunner, label, path string) {
	t.Helper()
	marker := "MECATL_FORBIDDEN_" + strings.ToUpper(strings.ReplaceAll(label, " ", "_"))
	result, err := runner.Run(ctx, "if cat "+shellQuote(path)+" >/dev/null 2>&1; then printf "+shellQuote(marker)+"; exit 0; else exit 23; fi")
	if err != nil {
		t.Fatalf("probe %s: %v", label, err)
	}
	if result.ExitCode == 0 || strings.Contains(result.Stdout, marker) {
		t.Fatalf("guest read forbidden %s at host path %q", label, path)
	}
}

func assertGuestCannotSee(t *testing.T, ctx context.Context, runner tool.CommandRunner, label, path string) {
	t.Helper()
	result, err := runner.Run(ctx, "test ! -e "+shellQuote(path))
	if err != nil {
		t.Fatalf("probe %s: %v", label, err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("guest can resolve forbidden %s at host path %q", label, path)
	}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

type e2eArtifactPaths struct {
	runtime, firmware, rootfs, guestAgent string
}

func loadE2EArtifacts(t *testing.T) VerifiedArtifacts {
	t.Helper()
	paths := e2eArtifactPaths{
		runtime: os.Getenv("MECATL_MICROVM_RUNTIME_DIR"), firmware: os.Getenv("MECATL_MICROVM_FIRMWARE_DIR"),
		rootfs: os.Getenv("MECATL_MICROVM_ROOTFS_DIR"), guestAgent: os.Getenv("MECATL_MICROVM_GUEST_AGENT_DIR"),
	}
	for name, path := range map[string]string{"runtime": paths.runtime, "firmware": paths.firmware, "execution image": paths.rootfs, "guest agent": paths.guestAgent} {
		if path == "" || !filepath.IsAbs(path) {
			t.Fatalf("%s artifact is not configured by task e2e:microvm", name)
		}
	}
	return VerifiedArtifacts{
		Runtime:        VerifiedArtifact{Kind: ArtifactRuntime, Digest: "sha256:pinned-go-microvm-v0.0.40", Path: paths.runtime, Source: extract.Dir(paths.runtime)},
		Firmware:       VerifiedArtifact{Kind: ArtifactFirmware, Digest: "sha256:pinned-go-microvm-v0.0.40", Path: paths.firmware, Source: extract.Dir(paths.firmware)},
		ExecutionImage: VerifiedArtifact{Kind: ArtifactExecutionImage, Digest: "sha256:pinned-alpine-3.22.1", Path: paths.rootfs, Source: extract.Dir(paths.rootfs)},
		GuestAgent:     VerifiedArtifact{Kind: ArtifactGuestAgent, Digest: "sha256:independently-built-guest-agent", Path: paths.guestAgent, Source: extract.Dir(paths.guestAgent)},
	}
}

func e2eArtifactRequests(artifacts VerifiedArtifacts) map[ArtifactKind]ArtifactRequest {
	requests := make(map[ArtifactKind]ArtifactRequest, 4)
	for _, artifact := range artifacts.All() {
		requests[artifact.Kind] = ArtifactRequest{Kind: artifact.Kind, Reference: artifact.Path, Digest: artifact.Digest}
	}
	return requests
}

func TestMicroVMEnvironments_PlatformSupportIsExplicit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		goos, arch string
		macMajor   int
		want       bool
	}{
		{goos: "linux", arch: "amd64", want: true},
		{goos: "linux", arch: "arm64", want: true},
		{goos: "darwin", arch: "arm64", macMajor: 15, want: true},
		{goos: "darwin", arch: "arm64", macMajor: 14, want: false},
		{goos: "darwin", arch: "amd64", macMajor: 15, want: false},
		{goos: "linux", arch: "386", want: false},
	} {
		if got := supportedE2EPlatform(tc.goos, tc.arch, tc.macMajor); got != tc.want {
			t.Errorf("supportedE2EPlatform(%s, %s, %d) = %v, want %v", tc.goos, tc.arch, tc.macMajor, got, tc.want)
		}
	}
}

func supportedE2EPlatform(goos, arch string, macMajor int) bool {
	return (goos == "linux" && (arch == "amd64" || arch == "arm64")) || (goos == "darwin" && arch == "arm64" && macMajor >= 15)
}

func assertSupportedE2EPlatform(t *testing.T) {
	t.Helper()
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64", "linux/arm64":
		info, err := os.Stat("/dev/kvm")
		if err != nil {
			t.Fatalf("supported Linux E2E cell requires /dev/kvm: %v", err)
		}
		file, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("supported Linux E2E cell cannot access /dev/kvm: %v", err)
		}
		_ = file.Close()
		_ = info
	case "darwin/arm64":
		output, err := exec.Command("sw_vers", "-productVersion").Output()
		if err != nil {
			t.Fatalf("read macOS version: %v", err)
		}
		major, err := strconv.Atoi(strings.Split(strings.TrimSpace(string(output)), ".")[0])
		if err != nil || !supportedE2EPlatform(runtime.GOOS, runtime.GOARCH, major) {
			t.Fatalf("Apple Silicon microVM E2E requires macOS 15 or newer, got %q", output)
		}
	default:
		t.Fatalf("unsupported microVM E2E platform %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func shortE2EEndpointDir(t *testing.T) string {
	t.Helper()
	parent := "/dev/shm"
	if runtime.GOOS == "darwin" {
		parent = "/private/tmp"
	}
	dir, err := os.MkdirTemp(parent, "me-")
	if err != nil {
		t.Fatalf("create short microVM endpoint directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func initE2ERepository(t *testing.T, root string) string {
	t.Helper()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"config", "user.name", "MicroVM E2E"}, {"config", "user.email", "microvm-e2e@example.invalid"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-m", "base"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return source
}
