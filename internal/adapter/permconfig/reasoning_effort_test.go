package permconfig

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// TestOperatorReasoningEffortFromCLIHonoured: an OPERATOR-TIER (CLI explicit)
// reasoning-effort: scalar is read and returned by OperatorReasoningEffort()
// (ADR 0055), mirroring the posture operator-tier test.
func TestOperatorReasoningEffortFromCLIHonoured(t *testing.T) {
	const yaml = "reasoning-effort: high\n"
	env := envWithExplicit("/etc/mecatl/effort.yaml", yaml)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/effort.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	if got := r.OperatorReasoningEffort(); got != "high" {
		t.Fatalf("operator-tier reasoning-effort must be honoured from CLI/explicit; got %q", got)
	}
}

// TestProjectReasoningEffortIgnoredWithWarn: a PROJECT-TIER reasoning-effort:
// scalar must NEVER become the operator value, and the resolver WARNs naming why
// (operator-tier only — ADR 0055, a project cannot raise the model's reasoning spend).
func TestProjectReasoningEffortIgnoredWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, "reasoning-effort: high\n")

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	if got := r.OperatorReasoningEffort(); got != "" {
		t.Fatalf("a PROJECT-tier reasoning-effort must NOT become the operator value; got %q", got)
	}
	log := buf.String()
	if !strings.Contains(log, "IGNORING a project-tier reasoning-effort") {
		t.Fatalf("expected an ignore-WARN naming the project tier; got:\n%s", log)
	}
}

// TestReasoningEffortCLIOutranksUser: CLI out-ranks user-global (first-non-empty
// keeps CLI).
func TestReasoningEffortCLIOutranksUser(t *testing.T) {
	const cliYAML = "reasoning-effort: low\n"
	const userYAML = "reasoning-effort: high\n"
	env := xdgconfig.ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return "/cfg"
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "/home/u", nil },
		ReadFile: func(p string) ([]byte, error) {
			switch p {
			case "/cli/effort.yaml":
				return []byte(cliYAML), nil
			case "/cfg/mecatl/settings.yaml":
				return []byte(userYAML), nil
			}
			return nil, errors.New("not found")
		},
	}
	r := newWithEnv(Options{Conventional: true, ExplicitFiles: []string{"/cli/effort.yaml"}}, env)
	if got := r.OperatorReasoningEffort(); got != "low" {
		t.Fatalf("CLI reasoning-effort must out-rank user-global; got %q", got)
	}
}
