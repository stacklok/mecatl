package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	requestSeen := make(chan error, 1)
	go serveCLICompositionPlacement(listener, requestSeen)

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
	built, err := app.Build(t.Context(), composition)
	if err != nil {
		t.Fatalf("CLI composition did not become service-ready: %v", err)
	}
	defer built.Close()
	if _, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{}); err != nil {
		t.Fatalf("create CLI-selected MicroVM session: %v", err)
	}
	if err := <-requestSeen; err != nil {
		t.Fatal(err)
	}
	if manager.request.Policy.PolicyRevision != "policy-cli-composition" {
		t.Fatalf("readiness request policy = %q", manager.request.Policy.PolicyRevision)
	}
}

func serveCLICompositionPlacement(listener net.Listener, result chan<- error) {
	for range 2 {
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
		binding, _ := request["binding"].(map[string]any)
		response := map[string]any{"binding": binding}
		if request["operation"] == "create" {
			sessionID, _ := binding["session_id"].(string)
			response = map[string]any{
				"binding": map[string]any{"owner": "local", "session_id": sessionID, "environment_id": "env-cli", "ref": "env-cli@1", "generation": 1},
				"created": map[string]any{
					"ref": map[string]any{"Kind": "microvm", "ID": "env-cli@1"}, "generation": 1,
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
