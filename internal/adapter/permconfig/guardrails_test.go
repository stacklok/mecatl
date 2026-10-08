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

const operatorGuardrailsYAML = `
guardrails:
  model: "gpt-5"
  rules:
    - match: "WebFetch"
      phases: ["post"]
      mode: "block"
    - match: "mcp__github__*"
      mode: "advisory"
`

// envWithExplicit returns a fake env whose ReadFile serves the given path → content.
func envWithExplicit(path, content string) xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile: func(p string) ([]byte, error) {
			if p == path {
				return []byte(content), nil
			}
			return nil, errors.New("not found")
		},
	}
}

// (14a) An OPERATOR-TIER (CLI explicit) guardrails block is honoured.
func TestOperatorGuardrailsFromCLIHonoured(t *testing.T) {
	env := envWithExplicit("/etc/mecatl/guardrails.yaml", operatorGuardrailsYAML)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/guardrails.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	g := r.OperatorGuardrails()
	if g == nil {
		t.Fatal("operator-tier guardrails must be honoured from the CLI/explicit tier")
		return
	}
	if g.Model != "gpt-5" || len(g.Rules) != 2 {
		t.Fatalf("guardrails not parsed faithfully: %+v", g)
	}
	if g.Rules[0].Match != "WebFetch" || g.Rules[1].Mode != "advisory" {
		t.Fatalf("guardrail rules not parsed: %+v", g.Rules)
	}
}

// (14b) A PROJECT-TIER guardrails block is IGNORED with a WARN naming why (a project
// repo configuring/weakening a checker is a security downgrade — decision 3).
func TestProjectGuardrailsIgnoredWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, operatorGuardrailsYAML)

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	// Resolving the project triggers the project-file parse (and the ignore-WARN).
	_ = r.Resolve(context.Background(), ws)

	if r.OperatorGuardrails() != nil {
		t.Fatal("a PROJECT-tier guardrails block must NOT become operator guardrails")
	}
	log := buf.String()
	if !strings.Contains(log, "IGNORING a project-tier guardrails") {
		t.Fatalf("expected an ignore-WARN naming the project tier; got:\n%s", log)
	}
}

// (3) Strict parse: a typo'd key inside the guardrails subtree is a parse error (so a
// guardrail can't be silently disabled), surfaced through the per-file skip.
func TestGuardrailsStrictUnknownKeyRejected(t *testing.T) {
	const bad = `
guardrails:
  model: "gpt-5"
  rulez:
    - match: "*"
`
	if _, err := parseYAML([]byte(bad)); err == nil {
		t.Fatal("an unknown key inside guardrails: must be a strict parse error")
	} else if !strings.Contains(err.Error(), "guardrails") {
		t.Fatalf("error should name the guardrails subtree; got %v", err)
	}
}

func TestGuardrailsRemovedMinContentBytesRejected(t *testing.T) {
	const bad = `
guardrails:
  model: "gpt-5"
  minContentBytes: 32
`
	if _, err := parseYAML([]byte(bad)); err == nil {
		t.Fatal("removed guardrails.minContentBytes must be rejected as unknown")
	}
}

func TestGuardrailsTaskWindowStrictInteger(t *testing.T) {
	cfg, err := parseYAML([]byte("guardrails:\n  taskWindow: 3\n"))
	if err != nil || cfg.Guardrails == nil || cfg.Guardrails.TaskWindow != 3 {
		t.Fatalf("taskWindow parse = %#v, %v", cfg.Guardrails, err)
	}
	if _, err := parseYAML([]byte("guardrails:\n  taskWindow: two\n")); err == nil {
		t.Fatal("non-integer guardrails.taskWindow must be rejected")
	}
}

// CLI out-ranks user-global for the guardrails block (first-non-nil keeps CLI, since
// CLI files are parsed before user-global).
func TestGuardrailsCLIOutranksUser(t *testing.T) {
	const cliYAML = `
guardrails:
  model: "cli-model"
  rules:
    - match: "Shell"
`
	const userYAML = `
guardrails:
  model: "user-model"
  rules:
    - match: "WebFetch"
`
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
			case "/cli/guardrails.yaml":
				return []byte(cliYAML), nil
			case "/cfg/mecatl/settings.yaml":
				return []byte(userYAML), nil
			}
			return nil, errors.New("not found")
		},
	}
	r := newWithEnv(Options{Conventional: true, ExplicitFiles: []string{"/cli/guardrails.yaml"}}, env)
	g := r.OperatorGuardrails()
	if g == nil || g.Model != "cli-model" {
		t.Fatalf("CLI guardrails must out-rank user-global; got %+v", g)
	}
}
