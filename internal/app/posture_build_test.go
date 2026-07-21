package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

// writeOperatorPostureFile writes an operator-tier settings.yaml carrying `posture:
// <tier>` to a temp file and returns its path, for use as a Config.PermissionConfigs
// entry (the explicit CLI tier — an OPERATOR tier, so the posture: key is honoured).
func writeOperatorPostureFile(t *testing.T, tier string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "operator.yaml")
	if err := os.WriteFile(path, []byte("posture: "+tier+"\n"), 0o600); err != nil {
		t.Fatalf("write operator posture file: %v", err)
	}
	return path
}

// postureEchoFromBuild creates a session against the built Service via the gRPC
// CreateSession handler and returns the server-wide posture echoed on
// ServerCapabilities — the SAME projection a real client reads (cfg.Posture.String()).
func postureEchoFromBuild(t *testing.T, built *Built) string {
	t.Helper()
	resp, err := server.NewHarnessServer(built.Service).CreateSession(context.Background(),
		&mecatlv1.CreateSessionRequest{Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return resp.GetCapabilities().GetPosture()
}

// TestBuildOperatorYAMLPostureSeam is the HEADLINE seam guard: it drives the REAL
// app.Build (offline mock provider) and proves the operator-tier settings.yaml
// `posture:` key flows all the way to the composed posture — the seam most likely to
// silently disconnect, and the input to the #1 security fix.
//
//	(a) operator-tier `posture: auto` → echoed posture "auto" AND the narrated
//	    composition fact reports allow_all=true (applyPosture derived the knob in Build).
//	(b) a PROJECT-tier `posture: yolo` fixture is IGNORED → echoed posture stays "strict".
//	(c) user-global `posture: yolo` + a PRIVILEGED Config → Build REFUSES (the
//	    operator-YAML tier cannot escape the root/no-sandbox refusal).
func TestBuildOperatorYAMLPostureSeam(t *testing.T) {
	t.Run("operator auto flows to composed posture", func(t *testing.T) {
		diag := slogdiagBuffer(t)
		built, err := Build(context.Background(), Config{
			Workspace:         t.TempDir(),
			Model:             "mock",
			UseMock:           true,
			NoSoul:            true,
			PermissionConfigs: []string{writeOperatorPostureFile(t, "auto")},
			Diagnostics:       diag.diag,
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()

		if got := postureEchoFromBuild(t, built); got != "auto" {
			t.Fatalf("operator-tier posture: auto must compose to posture \"auto\"; echoed %q", got)
		}
		// applyPosture ran in Build and derived AllowAllTools: the narrated fact says so.
		fact := diag.lineContaining("operator posture")
		if fact == "" {
			t.Fatalf("expected the narratePosture composition fact; log:\n%s", diag.String())
		}
		if !strings.Contains(fact, "allow_all=true") {
			t.Fatalf("auto must derive allow_all=true in Build; fact: %q", fact)
		}
	})

	t.Run("project tier posture is ignored (stays strict)", func(t *testing.T) {
		// A project-tier .mecatl/settings.yaml carrying posture: yolo in the workspace.
		ws := t.TempDir()
		mkdirProjectSettings(t, ws, "posture: yolo\n")
		built, err := Build(context.Background(), Config{
			Workspace:               ws,
			Model:                   "mock",
			UseMock:                 true,
			NoSoul:                  true,
			PermissionsConventional: true, // discover the project file (and ignore its posture:)
			Diagnostics:             port.NopDiagnostics{},
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		if got := postureEchoFromBuild(t, built); got != "strict" {
			t.Fatalf("a PROJECT-tier posture: yolo must be IGNORED; echoed %q, want \"strict\"", got)
		}
	})

	t.Run("operator yolo on a privileged Config is refused", func(t *testing.T) {
		built, err := Build(context.Background(), Config{
			Workspace:         t.TempDir(),
			Model:             "mock",
			UseMock:           true,
			NoSoul:            true,
			PermissionConfigs: []string{writeOperatorPostureFile(t, "yolo")},
			Privileged:        true, // root && !sandbox, as the cmd would compute
			Diagnostics:       port.NopDiagnostics{},
		})
		if err == nil {
			built.Close()
			t.Fatal("Build with operator-YAML posture: yolo on a privileged Config must REFUSE (the YAML-only tier must not escape the root/no-sandbox refusal)")
		}
		if !strings.Contains(err.Error(), "yolo") || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("refusal must name the posture and that it was refused; got: %v", err)
		}
	})
}

// mkdirProjectSettings writes <ws>/.mecatl/settings.yaml with the given content.
func mkdirProjectSettings(t *testing.T, ws, content string) {
	t.Helper()
	dir := filepath.Join(ws, ".mecatl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .mecatl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write project settings: %v", err)
	}
}

// slogdiagRecorder wraps a slogdiag sink over a concurrency-safe buffer so a
// test can assert on the FULL structured line (message + args), which
// capturingDiagnostics drops.
//
// It is concurrency-safe: the slog handler writes from the live-model-refresh
// background goroutine (and any other engine goroutine) while the test
// goroutine reads via String/lineContaining. The underlying bytes.Buffer is
// guarded by a mutex (slog writes through the lockedWriter; the reads take the
// same lock), because strings.Builder / bytes.Buffer are NOT safe for
// concurrent use.
type slogdiagRecorder struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	diag port.Diagnostics
}

// lockedWriter is the io.Writer the slog handler writes through; it serializes
// writes against the recorder's mutex.
type lockedWriter struct{ r *slogdiagRecorder }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	return w.r.buf.Write(p)
}

func slogdiagBuffer(t *testing.T) *slogdiagRecorder {
	t.Helper()
	r := &slogdiagRecorder{}
	r.diag = slogdiag.New(lockedWriter{r}, false, port.LevelDebug)
	return r
}

func (r *slogdiagRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func (r *slogdiagRecorder) lineContaining(substr string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range strings.Split(r.buf.String(), "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}
