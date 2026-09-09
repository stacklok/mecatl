package microvmmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	microvmclient "github.com/stacklok/mecatl/internal/adapter/microvm"
)

func TestMicroVMUserBootstrap_Scenario1_SafeXDGDefaults(t *testing.T) {
	home := t.TempDir()
	paths, err := DefaultPaths(HostPaths{Home: home, UID: 1234, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if paths.StateDir != filepath.Join(home, ".local", "state", "mecatl", "microvm") || paths.DataDir != filepath.Join(home, ".local", "share", "mecatl", "microvm") {
		t.Fatalf("unexpected manager paths: %+v", paths)
	}
	if !filepath.IsAbs(paths.Socket) || len(paths.Socket) >= DarwinSocketPathLimit {
		t.Fatalf("socket is not absolute and Darwin-safe: %q", paths.Socket)
	}
}

func TestDefaultPathsBoundsDerivedRepositoryNetworkSocket(t *testing.T) {
	paths, err := DefaultPaths(HostPaths{
		Home: t.TempDir(), XDGRuntimeDir: "/dev/shm/daily-1234567", UID: 1234, GOOS: "linux",
	})
	if err != nil {
		t.Fatal(err)
	}
	derived := filepath.Join(paths.RuntimeDir, longestRuntimeSocketSuffix)
	if len(derived) >= DarwinSocketPathLimit {
		t.Fatalf("derived repository network socket is too long: %q (%d bytes)", derived, len(derived))
	}
	if paths.RuntimeDir == filepath.Join("/dev/shm/daily-1234567", "mecatl-microvm") {
		t.Fatalf("overlong XDG runtime directory was accepted: %q", paths.RuntimeDir)
	}
}

func TestEnsureReadyPreservesOmittedGuestEgressAndExplicitPermissiveResets(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	manager := New(paths, &fakeOps{})
	request := ReadyRequest{
		Release: Release{URL: "https://example.invalid/release.tar.gz", SHA256: strings.Repeat("a", 64)},
		Policy:  testPolicy(root),
	}
	request.Policy.GuestEgressMode = GuestEgressDenyAll
	if _, err := manager.EnsureReady(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	omitted := request
	omitted.Policy.GuestEgressMode = GuestEgressPermissive
	omitted.PreserveExistingGuestEgress = true
	if _, err := manager.EnsureReady(t.Context(), omitted); err != nil {
		t.Fatal(err)
	}
	selection, err := readGuestEgressPolicy(paths.ConfigFile)
	if err != nil || selection.Mode != GuestEgressDenyAll {
		t.Fatalf("preserved policy = %#v, err=%v", selection, err)
	}
	status, err := manager.Status(t.Context())
	if err != nil || status.GuestEgress != GuestEgressDenyAll {
		t.Fatalf("status policy = %q, err=%v", status.GuestEgress, err)
	}

	explicit := omitted
	explicit.PreserveExistingGuestEgress = false
	if _, err := manager.EnsureReady(t.Context(), explicit); err != nil {
		t.Fatal(err)
	}
	selection, err = readGuestEgressPolicy(paths.ConfigFile)
	if err != nil || selection.Mode != GuestEgressPermissive {
		t.Fatalf("reset policy = %#v, err=%v", selection, err)
	}
}

func TestEnsureReadyRejectsUnsafeExistingPolicyWhenFlagsOmitted(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte(`{"guest_egress":{"Mode":"allowlist","Allow":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	request := ReadyRequest{
		Release: Release{URL: "https://example.invalid/release.tar.gz", SHA256: strings.Repeat("a", 64)},
		Policy:  testPolicy(root), PreserveExistingGuestEgress: true,
	}
	ops := &fakeOps{}
	_, err := New(paths, ops).EnsureReady(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "refusing to replace existing microvmd guest egress policy") {
		t.Fatalf("error = %v", err)
	}
	if len(ops.calls) != 0 {
		t.Fatalf("unsafe policy reached operations: %v", ops.calls)
	}
}

func TestEnsureReadyReportsBoundedSecretFreeStagesInOrder(t *testing.T) {
	root := t.TempDir()
	secret := "super-secret-api-key"
	request := ReadyRequest{
		Release: Release{URL: "https://" + secret + "@example.invalid/release.tar.gz", SHA256: strings.Repeat("a", 64)},
		Policy:  testPolicy(root),
	}
	var stages []ReadinessStage
	var messages []string
	ctx := WithReadinessObserver(t.Context(), func(stage ReadinessStage, message string) {
		stages = append(stages, stage)
		messages = append(messages, message)
	})
	if _, err := New(testPaths(root), &fakeOps{}, &fakeLifecycleClient{}).EnsureReady(ctx, request); err != nil {
		t.Fatal(err)
	}
	want := []ReadinessStage{StagePrepare, StagePreflight, StageDownload, StageVerify, StageInstall, StageDaemon, StageSocket, StageReconcile, StageHealth, StageReady}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
	joined := strings.Join(messages, "\n")
	for _, forbidden := range []string{request.Release.URL, secret, request.Policy.PublicKey, testPaths(root).DataDir} {
		if forbidden != "" && strings.Contains(joined, forbidden) {
			t.Fatalf("stage text leaked %q:\n%s", forbidden, joined)
		}
	}
}

func TestEnsureReadyFailureStopsAtExactReportedStage(t *testing.T) {
	tests := []struct {
		name      string
		failAt    string
		nilOps    bool
		lifecycle *fakeLifecycleClient
		wantStage ReadinessStage
	}{
		{name: "prepare", nilOps: true, wantStage: StagePrepare},
		{name: "preflight", failAt: "preflight", wantStage: StagePreflight},
		{name: "download", failAt: "download", wantStage: StageDownload},
		{name: "verify", failAt: "verify", wantStage: StageVerify},
		{name: "install", failAt: "install", wantStage: StageInstall},
		{name: "daemon", failAt: "start", wantStage: StageDaemon},
		{name: "socket", failAt: "wait", wantStage: StageSocket},
		{name: "reconcile", lifecycle: &fakeLifecycleClient{reconcileErr: errors.New("reconcile failed")}, wantStage: StageReconcile},
		{name: "health", failAt: "doctor", wantStage: StageHealth},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			request := ReadyRequest{Release: Release{URL: "https://example.invalid/release.tar.gz", SHA256: strings.Repeat("a", 64)}, Policy: testPolicy(root)}
			var stages []ReadinessStage
			ctx := WithReadinessObserver(t.Context(), func(stage ReadinessStage, _ string) { stages = append(stages, stage) })
			var ops Operations = &fakeOps{failAt: tc.failAt}
			if tc.nilOps {
				ops = nil
			}
			manager := New(testPaths(root), ops)
			if tc.lifecycle != nil {
				manager = New(testPaths(root), ops, tc.lifecycle)
			}
			_, err := manager.EnsureReady(ctx, request)
			if err == nil || !strings.HasPrefix(err.Error(), "microvm-local readiness failed during "+string(tc.wantStage)+":") || !strings.HasSuffix(err.Error(), "correct the reported problem and retry ordinary use") {
				t.Fatalf("error = %v, want exact stage prefix %s", err, tc.wantStage)
			}
			if len(stages) == 0 || stages[len(stages)-1] != tc.wantStage {
				t.Fatalf("stages = %v, want failure stage %s last", stages, tc.wantStage)
			}
			for _, stage := range stages {
				if stage == StageReady {
					t.Fatalf("ready stage followed failure: %v", stages)
				}
			}
		})
	}
}

func TestReadinessStageProjectionIsClosedAndUnknownIsSilent(t *testing.T) {
	want := map[ReadinessStage]string{
		StagePrepare: "Preparing local microVM readiness", StagePreflight: "Checking host prerequisites",
		StageDownload: "Downloading microVM components (up to about 2 GiB)", StageVerify: "Verifying downloaded components",
		StageInstall: "Installing and configuring the local microVM", StageDaemon: "Starting or reusing the microVM daemon",
		StageSocket: "Waiting for the microVM daemon socket", StageReconcile: "Reconciling existing microVM state",
		StageHealth: "Running microVM health checks", StageReady: "Local microVM is ready",
	}
	for stage, text := range want {
		if got, ok := readinessText(stage); !ok || got != text {
			t.Fatalf("readinessText(%q) = %q, %t; want %q, true", stage, got, ok, text)
		}
	}
	calls := 0
	ctx := WithReadinessObserver(t.Context(), func(ReadinessStage, string) { calls++ })
	ReportReadinessStage(ctx, ReadinessStage("future-stage"))
	if calls != 0 {
		t.Fatalf("unknown readiness stage emitted %d observer calls", calls)
	}
}

func TestMicroVMRedesign_EnsureReadyRestartsOnlyIncompatibleDaemon(t *testing.T) {
	for _, mismatch := range []string{"", "binary", "policy"} {
		t.Run(mismatch, func(t *testing.T) {
			root := t.TempDir()
			ops := &fakeOps{running: true, identityMismatch: mismatch}
			request := ReadyRequest{Release: Release{URL: "https://example.invalid/release.tar.gz", SHA256: strings.Repeat("a", 64)}, Policy: testPolicy(root)}
			if _, err := New(testPaths(root), ops).EnsureReady(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			wantRestart := mismatch != ""
			if got := contains(ops.calls, "stop") && contains(ops.calls, "start"); got != wantRestart {
				t.Fatalf("calls = %v, restart=%t want %t", ops.calls, got, wantRestart)
			}
		})
	}
}

func TestDoctorReportsFreshAndFailureStatesWithoutMutation(t *testing.T) {
	tests := []struct {
		name       string
		configured bool
		ops        *fakeOps
		want       []string
	}{
		{name: "fresh home", ops: &fakeOps{}, want: []string{"host preflight: passed", "backend: not configured", "host prerequisites passed; select microvm-local", "mecatui --default-placement microvm-local", "doctor is read-only"}},
		{name: "daemon stopped", configured: true, ops: &fakeOps{}, want: []string{"host preflight: passed", "backend: configured; daemon not running", "host prerequisites passed; select microvm-local"}},
		{name: "healthy", configured: true, ops: &fakeOps{running: true}, want: []string{"host preflight: passed", "backend: healthy", "PASS hypervisor ready"}},
		{name: "daemon unhealthy", configured: true, ops: &fakeOps{running: true, doctorErr: errors.New("guest transport unavailable")}, want: []string{"backend: unhealthy", "guest transport unavailable"}},
		{name: "host preflight failed", ops: &fakeOps{failAt: "preflight"}, want: []string{"host preflight: failed", "backend: not configured"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			paths := testPaths(root)
			if tc.configured {
				if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.ConfigFile, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := New(paths, tc.ops).Doctor(t.Context())
			if tc.name == "healthy" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("Doctor succeeded for an unready state")
			}
			for _, want := range tc.want {
				if !strings.Contains(report, want) {
					t.Fatalf("report omitted %q:\n%s", want, report)
				}
			}
			if tc.ops.failAt != "preflight" && strings.Contains(report, "failed host prerequisite") {
				t.Fatalf("passed preflight received failed-prerequisite guidance:\n%s", report)
			}
			for _, forbidden := range []string{"download", "verify", "install", "start", "stop", "wait"} {
				if contains(tc.ops.calls, forbidden) {
					t.Fatalf("read-only Doctor called %q: %v", forbidden, tc.ops.calls)
				}
			}
		})
	}
}

func TestMicroVMLifecycleUX_ManagerStatusAndDeleteRemainExact(t *testing.T) {
	root := t.TempDir()
	paths := testPaths(root)
	ops := &fakeOps{running: true}
	daemon := &fakeLifecycleClient{entries: []microvmclient.InventoryEntry{{
		Owner: "local", SessionID: "s1", EnvironmentID: "env-1", Ref: "env-1@7", Generation: 7,
		WorktreePath: "/worktrees/s1", Health: microvmclient.GenerationStale,
	}}}
	manager := New(paths, ops, daemon)
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Generations) != 1 || status.Generations[0].Ref != "env-1@7" {
		t.Fatalf("status = %+v", status)
	}
	result, err := manager.Delete(context.Background(), DeleteRequest{SessionID: "s1", Ref: "env-1@7", Generation: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !result.WorktreeRetained || daemon.deleted.Ref != "env-1@7" {
		t.Fatalf("delete result=%+v binding=%+v", result, daemon.deleted)
	}
}

func TestRepositoryManagerInventoryPagesAndExactLogicalDelete(t *testing.T) {
	root := t.TempDir()
	daemon := &fakeLifecycleClient{entries: []microvmclient.InventoryEntry{{
		Owner: "local", SessionID: "session-a", EnvironmentID: "logical-a", Ref: "logical-a@9", Generation: 9,
		WorktreePath: "/worktrees/a", State: "ready", Health: microvmclient.GenerationHealthy,
	}}, continuation: "next"}
	manager := New(testPaths(root), &fakeOps{running: true}, daemon)
	first, err := manager.Status(t.Context(), StatusRequest{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	daemon.entries = []microvmclient.InventoryEntry{{
		Owner: "local", SessionID: "session-b", EnvironmentID: "logical-b", Ref: "logical-b@9", Generation: 9,
		WorktreePath: "/worktrees/b", State: "ready", Health: microvmclient.GenerationHealthy,
	}}
	daemon.continuation = ""
	second, err := manager.Status(t.Context(), StatusRequest{PageSize: 1, Continuation: first.Continuation})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Generations) != 1 || len(second.Generations) != 1 || first.Generations[0].Generation != second.Generations[0].Generation ||
		first.Generations[0].WorktreePath == second.Generations[0].WorktreePath || daemon.inventoryRequest.Continuation != "next" {
		t.Fatalf("repository manager pages = %+v / %+v, request=%+v", first, second, daemon.inventoryRequest)
	}
	result, err := manager.Delete(t.Context(), DeleteRequest{SessionID: "session-b", Ref: "logical-b@9", Generation: 9})
	if err != nil {
		t.Fatal(err)
	}
	if !result.WorktreeRetained || daemon.deleted.EnvironmentID != "logical-b" || daemon.reconcileCalls != 0 {
		t.Fatalf("logical delete=%+v binding=%+v legacy reconcile calls=%d", result, daemon.deleted, daemon.reconcileCalls)
	}
}

type fakeLifecycleClient struct {
	entries          []microvmclient.InventoryEntry
	continuation     string
	inventoryRequest microvmclient.InventoryRequest
	deleted          microvmclient.GenerationBinding
	reconcileCalls   int
	reconcileErr     error
}

func (f *fakeLifecycleClient) Inventory(_ context.Context, _ string, requests ...microvmclient.InventoryRequest) (microvmclient.InventoryPage, error) {
	if len(requests) > 0 {
		f.inventoryRequest = requests[0]
	}
	return microvmclient.InventoryPage{Entries: f.entries, Continuation: f.continuation}, nil
}
func (f *fakeLifecycleClient) Reconcile(context.Context, string) error {
	f.reconcileCalls++
	return f.reconcileErr
}
func (f *fakeLifecycleClient) DeleteGeneration(_ context.Context, binding microvmclient.GenerationBinding) (microvmclient.DeleteResult, error) {
	f.deleted = binding
	return microvmclient.DeleteResult{WorktreePath: "/worktrees/s1", WorktreeRetained: true}, nil
}

type fakeOps struct {
	calls            []string
	running          bool
	doctorErr        error
	failAt           string
	identityMismatch string
}

func (f *fakeOps) stageError(stage string) error {
	if f.failAt == stage {
		return errors.New(stage + " failed")
	}
	return nil
}
func (f *fakeOps) Preflight(context.Context, Paths) error {
	f.calls = append(f.calls, "preflight")
	return f.stageError("preflight")
}
func (f *fakeOps) Download(context.Context, Release, string) (string, error) {
	f.calls = append(f.calls, "download")
	return "/bundle/manifest.json", f.stageError("download")
}
func (f *fakeOps) Verify(context.Context, Release, string) error {
	f.calls = append(f.calls, "verify")
	return f.stageError("verify")
}
func (f *fakeOps) Install(_ context.Context, _ string, installRoot string) (InstalledArtifacts, error) {
	f.calls = append(f.calls, "install")
	if err := f.stageError("install"); err != nil {
		return InstalledArtifacts{}, err
	}
	binary := filepath.Join(filepath.Dir(installRoot), "bin", "mecatl-microvmd")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		return InstalledArtifacts{}, err
	}
	if err := os.WriteFile(binary, []byte("test-microvmd"), 0o700); err != nil {
		return InstalledArtifacts{}, err
	}
	digest := "sha256:" + strings.Repeat("1", 64)
	artifacts := make([]Artifact, 0, 4)
	for _, kind := range []string{"runtime", "firmware", "execution-image", "guest-agent"} {
		artifact := Artifact{Kind: kind, Reference: "oci.example/" + kind + "@" + digest, Digest: digest, Path: filepath.Join(installRoot, "artifacts", kind), Provenance: filepath.Join(filepath.Dir(installRoot), "download", kind+".provenance.json"), SigstoreBundle: filepath.Join(filepath.Dir(installRoot), "download", kind+".sigstore.json")}
		if kind == "execution-image" {
			artifact.Reference = "ghcr.io/stacklok/brood-box/base@" + digest
			artifact.ManifestDigest = digest
			artifact.DiscoveryReference = "ghcr.io/stacklok/brood-box/base:latest"
			artifact.ResolutionEvidence = digest
			artifact.Platform = "linux/amd64"
			artifact.Path = ""
		}
		artifacts = append(artifacts, artifact)
	}
	return InstalledArtifacts{Artifacts: artifacts}, nil
}
func (f *fakeOps) Running(context.Context, Paths) (bool, error) { return f.running, nil }
func (f *fakeOps) DaemonInfo(_ context.Context, paths Paths) (DaemonInfo, error) {
	f.calls = append(f.calls, "info")
	info, err := expectedDaemonInfo(paths)
	if err != nil {
		return DaemonInfo{}, err
	}
	if f.identityMismatch == "binary" {
		info.BinaryIdentity = "sha256:" + strings.Repeat("0", 64)
	}
	if f.identityMismatch == "policy" {
		info.PolicyRevision = "stale-policy"
	}
	return info, nil
}
func (f *fakeOps) Start(context.Context, Paths) error {
	f.calls = append(f.calls, "start")
	if err := f.stageError("start"); err != nil {
		return err
	}
	f.running = true
	return nil
}
func (f *fakeOps) WaitSocket(context.Context, string) error {
	f.calls = append(f.calls, "wait")
	return f.stageError("wait")
}
func (f *fakeOps) Doctor(context.Context, Paths) (string, error) {
	f.calls = append(f.calls, "doctor")
	if f.doctorErr != nil {
		return "", f.doctorErr
	}
	return "PASS hypervisor ready; remediation: none\n", f.stageError("doctor")
}
func (f *fakeOps) Stop(context.Context, Paths) error {
	f.calls = append(f.calls, "stop")
	if err := f.stageError("stop"); err != nil {
		return fmt.Errorf("safe managed restart failed: %w", err)
	}
	f.running = false
	return nil
}

func testPaths(root string) Paths {
	return Paths{StateDir: filepath.Join(root, "state"), RuntimeDir: filepath.Join(root, "run"), Socket: filepath.Join(root, "run", "d.sock"), DataDir: filepath.Join(root, "data"), ConfigFile: filepath.Join(root, "config", "microvmd.json"), UserSettings: filepath.Join(root, "config", "settings.yaml"), DaemonBinary: filepath.Join(root, "data", "bin", "mecatl-microvmd")}
}

func testPolicy(_ string) Policy {
	return Policy{PolicyRevision: "release-v1", CertificateIdentity: "https://github.com/stacklok/mecatl/.github/workflows/release.yml@refs/tags/v1", OIDCIssuer: "https://token.actions.githubusercontent.com", RequiredAttestations: map[string]string{"runtime": "https://slsa.dev/provenance/v1", "firmware": "https://slsa.dev/provenance/v1", "execution-image": "https://slsa.dev/provenance/v1", "guest-agent": "https://slsa.dev/provenance/v1"}, GuestEgressMode: "deny-all"}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
