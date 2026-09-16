package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/microvmcmd"
)

func TestResolveMicroVMIsOfflineHandledCommand(t *testing.T) {
	res := resolveCommand([]string{"mecated", "microvm", "status", "--output", "json"})
	if res.err != nil || !res.handled || res.run == nil || res.mode != "" {
		t.Fatalf("resolution = %+v", res)
	}
}

func TestMecatedMicroVMUsesSharedCommandAndDoesNotEnterServerPath(t *testing.T) {
	original := newLocalMicroVMManager
	t.Cleanup(func() { newLocalMicroVMManager = original })
	manager := &mecatedMicroVMFake{status: microvmmanager.Status{Configured: true, Running: true, Socket: "/run/microvmd.sock", Continuation: "token"}}
	newLocalMicroVMManager = func() (microvmcmd.Manager, error) { return manager, nil }

	res := resolveCommand([]string{"mecated", "microvm", "status"})
	var out strings.Builder
	if err := res.run(strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `mecated microvm status --continuation "token"`) || strings.Contains(out.String(), "mecatui microvm status --continuation") || manager.statusCalls != 1 {
		t.Fatalf("output=%q status calls=%d", out.String(), manager.statusCalls)
	}
}

func TestMecatedMicroVMHelpAllMatchesAdvertisedTopLevelGuidance(t *testing.T) {
	manager := &mecatedMicroVMFake{}
	original := newLocalMicroVMManager
	t.Cleanup(func() { newLocalMicroVMManager = original })
	newLocalMicroVMManager = func() (microvmcmd.Manager, error) { return manager, nil }
	res := resolveCommand([]string{"mecated", "microvm", "--help-all"})
	var out strings.Builder
	if res.err != nil || !res.handled || res.run == nil {
		t.Fatalf("resolution = %+v", res)
	}
	if err := res.run(strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Usage: mecated microvm", "doctor", "status", "delete", "--output text|json", "mecated serve --headless"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help-all omitted %q: %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "mecatui microvm") {
		t.Fatalf("help advertised noncanonical frontend: %q", out.String())
	}
	for _, command := range []string{"init", "recover"} {
		if strings.Contains(out.String(), command) {
			t.Fatalf("help advertised obsolete command %q: %q", command, out.String())
		}
		obsolete := resolveCommand([]string{"mecated", "microvm", command})
		if obsolete.err != nil || !obsolete.handled || obsolete.run == nil {
			t.Fatalf("obsolete command %q resolution = %+v", command, obsolete)
		}
		if err := obsolete.run(strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Fatalf("obsolete command %q was accepted", command)
		}
	}
	if manager.doctorCalls != 0 || manager.statusCalls != 0 || manager.deleteCalls != 0 {
		t.Fatalf("help called manager: doctor=%d status=%d delete=%d", manager.doctorCalls, manager.statusCalls, manager.deleteCalls)
	}
}

func TestMecatedReleaseStampFeedsReadinessDefaults(t *testing.T) {
	originalVersion, originalDefaults := microVMReleaseVersion, microVMReleaseDefaultsB64
	t.Cleanup(func() {
		microVMReleaseVersion, microVMReleaseDefaultsB64 = originalVersion, originalDefaults
	})
	microVMReleaseVersion = "v-test-mecated"
	platform := runtime.GOOS + "-" + runtime.GOARCH
	encoded, err := json.Marshal(map[string]microvmmanager.ReleaseDefaults{
		platform: {
			Version: microVMReleaseVersion, Platform: platform, URL: "https://example.invalid/microvm.tar.gz",
			SHA256: strings.Repeat("a", 64), PolicyRevision: "policy-test",
			CertificateIdentity: "https://example.invalid/release.yml", OIDCIssuer: "https://token.actions.githubusercontent.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	microVMReleaseDefaultsB64 = base64.StdEncoding.EncodeToString(encoded)
	request, err := microVMReadyRequest()
	if err != nil {
		t.Fatal(err)
	}
	if request.Release.URL != "https://example.invalid/microvm.tar.gz" || request.Policy.PolicyRevision != "policy-test" {
		t.Fatalf("readiness request did not consume mecated stamp: %+v", request)
	}
	microVMReleaseVersion = "v-wrong-symbol"
	if _, err := microVMReadyRequest(); err == nil {
		t.Fatal("mecated readiness accepted defaults stamped for another version")
	}
}

func TestConfigureMicroVMLocalProfileWiresHeadlessReadinessDiagnosticsAndEgress(t *testing.T) {
	originalVersion, originalDefaults := microVMReleaseVersion, microVMReleaseDefaultsB64
	t.Cleanup(func() { microVMReleaseVersion, microVMReleaseDefaultsB64 = originalVersion, originalDefaults })
	microVMReleaseVersion = "v-test-composition"
	platform := runtime.GOOS + "-" + runtime.GOARCH
	defaults, err := json.Marshal(map[string]microvmmanager.ReleaseDefaults{platform: {
		Version: microVMReleaseVersion, Platform: platform, URL: "https://example.invalid/microvm.tar.gz", SHA256: strings.Repeat("a", 64),
		PolicyRevision: "policy-test", CertificateIdentity: "identity", OIDCIssuer: "issuer",
	}})
	if err != nil {
		t.Fatal(err)
	}
	microVMReleaseDefaultsB64 = base64.StdEncoding.EncodeToString(defaults)
	manager := &mecatedMicroVMFake{endpoint: "unix:///run/private/microvmd.sock"}
	selection := microvmmanager.GuestEgressSelection{Mode: microvmmanager.GuestEgressDenyAll}
	cfg, err := app.ConfigureExecution(app.Config{
		Workspace: t.TempDir(), DefaultPlacement: app.PlacementMicroVMLocal, DefaultPlacementSet: true,
		MicroVMGuestEgress: selection, MicroVMGuestEgressSet: true,
		MicroVMReadyRequest: func(selection microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
			return microVMReadyRequestWithDevelopment("", false, selection)
		},
		MicroVMManagerFactory: func() (app.MicroVMReadyManager, string, error) { return manager, manager.endpoint, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PlacementProvider == nil || cfg.PlacementScope != "deployment" {
		t.Fatalf("placement provider was not selected: scope=%q", cfg.PlacementScope)
	}
	if cfg.EnvironmentForkers["microvm"] == nil || cfg.EnvironmentMergers["microvm"] == nil {
		t.Fatal("microvm delegation routing was not configured")
	}
}

func TestMecatedCLICompositionValidatesMicroVMDefaultAfterReadiness(t *testing.T) {
	originalVersion, originalDefaults := microVMReleaseVersion, microVMReleaseDefaultsB64
	t.Cleanup(func() { microVMReleaseVersion, microVMReleaseDefaultsB64 = originalVersion, originalDefaults })
	microVMReleaseVersion = "v-test-cli-composition"
	platform := runtime.GOOS + "-" + runtime.GOARCH
	defaults, err := json.Marshal(map[string]microvmmanager.ReleaseDefaults{platform: {
		Version: microVMReleaseVersion, Platform: platform, URL: "https://example.invalid/microvm.tar.gz", SHA256: strings.Repeat("a", 64),
		PolicyRevision: "policy-cli-composition", CertificateIdentity: "identity", OIDCIssuer: "issuer",
	}})
	if err != nil {
		t.Fatal(err)
	}
	microVMReleaseDefaultsB64 = base64.StdEncoding.EncodeToString(defaults)

	socketDir, err := os.MkdirTemp("/tmp", "mecatl-mv-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(socketDir, "microvmd.sock"))
		_ = os.Remove(socketDir)
	})
	listener, err := net.Listen("unix", filepath.Join(socketDir, "microvmd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	created := make(chan struct{}, 1)
	detached := make(chan struct{}, 1)
	serverDone := make(chan error, 1)
	go serveCLICompositionPlacement(listener, created, detached, serverDone)

	workspace, store := t.TempDir(), t.TempDir()
	parsed, err := parseFlagsMode(modeServe, []string{"--mock", "--default-placement=microvm-local", "--workspace=" + workspace, "--store-dir=" + store})
	if err != nil {
		t.Fatalf("parse documented CLI selection: %v", err)
	}
	composition := appConfig(parsed, nil, nil, nil, nil, nil)
	manager := &mecatedMicroVMFake{endpoint: "unix://" + listener.Addr().String()}
	composition.MicroVMManagerFactory = func() (app.MicroVMReadyManager, string, error) {
		return manager, manager.endpoint, nil
	}
	buildCtx, cancelBuild := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelBuild()
	built, err := buildIsolated(t, buildCtx, composition)
	if err != nil {
		t.Fatalf("CLI composition did not become service-ready: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			built.Close()
		}
	})
	createCtx, cancelCreate := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelCreate()
	if _, err := built.Service.CreateSession(createCtx, session.ModeDefault, session.Limits{}); err != nil {
		t.Fatalf("create CLI-selected MicroVM session: %v", err)
	}
	awaitCLICompositionPlacement(t, "create", created, serverDone)
	assertNoCLICompositionDetach(t, detached, serverDone)
	if manager.request.Policy.PolicyRevision != "policy-cli-composition" {
		t.Fatalf("readiness request policy = %q", manager.request.Policy.PolicyRevision)
	}

	built.Close()
	closed = true
	awaitCLICompositionPlacement(t, "detach", detached, serverDone)
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("placement server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("placement server did not terminate after detach")
	}
}

func awaitCLICompositionPlacement(t *testing.T, operation string, observed <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-observed:
		return
	default:
	}
	select {
	case <-observed:
	case err := <-done:
		select {
		case <-observed:
			return
		default:
			t.Fatalf("placement server stopped before %s: %v", operation, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for placement %s", operation)
	}
}

func assertNoCLICompositionDetach(t *testing.T, detached <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-detached:
		t.Fatal("placement detached before Build close")
	default:
	}
	select {
	case <-detached:
		t.Fatal("placement detached before Build close")
	case err := <-done:
		t.Fatalf("placement server stopped before Build close: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func serveCLICompositionPlacement(listener net.Listener, created, detached chan<- struct{}, result chan<- error) {
	for _, expected := range []string{"create", "detach"} {
		conn, err := listener.Accept()
		if err != nil {
			result <- err
			return
		}
		var request map[string]any
		if err := readCLICompositionFrame(conn, &request); err != nil {
			_ = conn.Close()
			result <- err
			return
		}
		operation, _ := request["operation"].(string)
		if operation != expected {
			_ = conn.Close()
			result <- fmt.Errorf("placement operation = %q, want %q", operation, expected)
			return
		}
		binding, _ := request["binding"].(map[string]any)
		response := map[string]any{"binding": binding}
		if operation == "create" {
			sessionID, _ := binding["session_id"].(string)
			response = map[string]any{
				"binding": map[string]any{"owner": "local", "session_id": sessionID, "environment_id": "logical-cli", "ref": "logical-cli@1", "generation": 1},
				"created": map[string]any{
					"ref": map[string]any{"Kind": "microvm", "ID": "logical-cli@1"}, "generation": 1,
					"host_worktree": "/private/worktree", "guest_root": "/workspace", "profile": "microvm-local",
					"guest_egress": "permissive", "host_egress": "not constrained",
				},
			}
		}
		err = writeCLICompositionFrame(conn, response)
		_ = conn.Close()
		if err != nil {
			result <- err
			return
		}
		if operation == "create" {
			created <- struct{}{}
		} else {
			detached <- struct{}{}
		}
	}
	result <- nil
}

func readCLICompositionFrame(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(r, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, value)
}

func writeCLICompositionFrame(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var frame bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	frame.Write(header[:])
	frame.Write(payload)
	_, err = w.Write(frame.Bytes())
	return err
}

func TestMecatedHelpDiscoversMicroVMCommands(t *testing.T) {
	var out strings.Builder
	writeTopLevelHelp(&out)
	for _, want := range []string{"microvm doctor|status|delete", "local microVM attachments"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help omitted %q:\n%s", want, out.String())
		}
	}
}

type mecatedMicroVMFake struct {
	status      microvmmanager.Status
	statusCalls int
	doctorCalls int
	deleteCalls int
	endpoint    string
	request     microvmmanager.ReadyRequest
}

func (f *mecatedMicroVMFake) EnsureReady(ctx context.Context, request microvmmanager.ReadyRequest) (string, error) {
	f.request = request
	microvmmanager.ReportReadinessStage(ctx, microvmmanager.StagePrepare)
	microvmmanager.ReportReadinessStage(ctx, microvmmanager.StageReady)
	return f.endpoint, nil
}

func (f *mecatedMicroVMFake) Doctor(context.Context) (string, error) {
	f.doctorCalls++
	return "", nil
}
func (f *mecatedMicroVMFake) Status(context.Context, ...microvmmanager.StatusRequest) (microvmmanager.Status, error) {
	f.statusCalls++
	return f.status, nil
}
func (f *mecatedMicroVMFake) Delete(context.Context, microvmmanager.DeleteRequest) (microvmmanager.DeleteResult, error) {
	f.deleteCalls++
	return microvmmanager.DeleteResult{}, nil
}
