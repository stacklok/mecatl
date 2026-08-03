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

// TestOperatorOutputEconomyDeprecatedWarns: an OPERATOR-TIER (CLI explicit)
// output-economy: scalar is PARSED (legacy compatibility) but has NO EFFECT — the
// resolver emits a DEPRECATION WARN through its diagnostics. The captured value is
// not surfaced (no OperatorOutputEconomy accessor; composition does not read it).
func TestOperatorOutputEconomyDeprecatedWarns(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)
	env := envWithExplicit("/etc/mecatl/economy.yaml", "output-economy: terse\n")
	r := newWithEnv(Options{
		ExplicitFiles: []string{"/etc/mecatl/economy.yaml"},
		Diagnostics:   diag,
	}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	_ = r.Resolve(context.Background(), &countingWS{Workspace: memfs.NewWorkspace("/repo")})
	log := buf.String()
	if !strings.Contains(log, "output-economy: DEPRECATED and has no effect") {
		t.Fatalf("expected a DEPRECATION WARN for the legacy output-economy: scalar; got:\n%s", log)
	}
}

// TestProjectOutputEconomyDeprecatedWarns: a PROJECT-TIER output-economy: scalar
// is likewise parsed and emits the SAME deprecation WARN (output-economy has no
// effect at any tier now).
func TestProjectOutputEconomyDeprecatedWarns(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, "output-economy: terse\n")

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	log := buf.String()
	if !strings.Contains(log, "output-economy: DEPRECATED and has no effect") {
		t.Fatalf("expected a DEPRECATION WARN for the project-tier output-economy: scalar; got:\n%s", log)
	}
}

// TestUserGlobalOutputEconomyDeprecatedWarns: a USER-GLOBAL output-economy: scalar
// emits the deprecation WARN (the operator tier is not special-cased — output-economy
// is deprecated everywhere). CLI out-ranks user-global on first-non-empty, but BOTH
// emit the deprecation WARN at most once (first-seen).
func TestUserGlobalOutputEconomyDeprecatedWarns(t *testing.T) {
	const userYAML = "output-economy: terse\n"
	env := xdgconfig.ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return "/cfg"
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "/home/u", nil },
		ReadFile: func(p string) ([]byte, error) {
			if p == "/cfg/mecatl/settings.yaml" {
				return []byte(userYAML), nil
			}
			return nil, errors.New("not found")
		},
	}
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)
	r := newWithEnv(Options{Conventional: true, Diagnostics: diag}, env)
	_ = r.Resolve(context.Background(), &countingWS{Workspace: memfs.NewWorkspace("/repo")})
	if !strings.Contains(buf.String(), "output-economy: DEPRECATED and has no effect") {
		t.Fatalf("expected a DEPRECATION WARN for the user-global output-economy: scalar; got:\n%s", buf.String())
	}
}
