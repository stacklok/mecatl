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

const operatorSchedulesYAML = `
schedules:
  - name: nightly-review
    cron: "0 9 * * *"
    timezone: "UTC"
    prompt: "Review open PRs and post a summary"
    provider: openrouter
    model: anthropic/claude-sonnet-4
    mutating: false
    maxTurns: 20
    singleton: true
  - name: one-shot-flag-removal
    oneShot: "2026-08-01T09:00:00Z"
    prompt: "Remove the feature-flag-xyz code"
    mutating: true
`

// (1) An OPERATOR-TIER (CLI explicit) schedules block is honoured: the resolver
// captures it and OperatorSchedules returns the parsed declarations.
func TestOperatorSchedulesFromCLIHonoured(t *testing.T) {
	env := envWithExplicit("/etc/mecatl/schedules.yaml", operatorSchedulesYAML)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/schedules.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	s := r.OperatorSchedules()
	if s == nil {
		t.Fatal("operator-tier schedules must be honoured from the CLI/explicit tier")
	}
	if len(s.Items) != 2 {
		t.Fatalf("expected 2 schedule declarations; got %d", len(s.Items))
	}
	cron := s.Items[0]
	if cron.Name != "nightly-review" || cron.Cron != "0 9 * * *" || cron.Timezone != "UTC" {
		t.Fatalf("cron schedule not parsed faithfully: %+v", cron)
	}
	if cron.Prompt != "Review open PRs and post a summary" || cron.Provider != "openrouter" || cron.Model != "anthropic/claude-sonnet-4" {
		t.Fatalf("cron schedule selector/prompt not parsed: %+v", cron)
	}
	if !cron.Singleton || cron.MaxTurns != 20 || cron.Mutating {
		t.Fatalf("cron schedule knobs not parsed: %+v", cron)
	}
	one := s.Items[1]
	if one.Name != "one-shot-flag-removal" || one.OneShot != "2026-08-01T09:00:00Z" || !one.Mutating {
		t.Fatalf("one-shot schedule not parsed faithfully: %+v", one)
	}
}

// (2) A PROJECT-TIER schedules block is IGNORED with a WARN naming why (a project
// repo registering schedules is an operator decision — the operator-tier-only
// discipline, mirroring guardrails).
func TestProjectSchedulesIgnoredWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, operatorSchedulesYAML)

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	// Resolving the project triggers the project-file parse (and the ignore-WARN).
	_ = r.Resolve(context.Background(), ws)

	if r.OperatorSchedules() != nil {
		t.Fatal("a PROJECT-tier schedules block must NOT become operator schedules")
	}
	log := buf.String()
	if !strings.Contains(log, "IGNORING a project-tier schedules") {
		t.Fatalf("expected an ignore-WARN naming the project tier; got:\n%s", log)
	}
}

// (3) Strict parse: a typo'd key inside a schedule declaration is a parse error
// (so a schedule can't be silently misconfigured), surfaced through the per-file
// skip.
func TestSchedulesStrictUnknownKeyRejected(t *testing.T) {
	const bad = `
schedules:
  - name: nightly-review
    cronn: "0 9 * * *"
    prompt: "..."
`
	if _, err := parseYAML([]byte(bad)); err == nil {
		t.Fatal("an unknown key inside a schedules[] element must be a strict parse error")
	} else if !strings.Contains(err.Error(), "schedules") {
		t.Fatalf("error should name the schedules subtree; got %v", err)
	}
}

// (4) A non-sequence schedules: block (a stray mapping) is a parse error — the
// subtree is a sequence, not a mapping.
func TestSchedulesNonSequenceRejected(t *testing.T) {
	const bad = `
schedules:
  name: not-a-list
`
	if _, err := parseYAML([]byte(bad)); err == nil {
		t.Fatal("a non-sequence schedules: block must be a parse error")
	}
}

// CLI out-ranks user-global for the schedules block (first-non-nil keeps CLI, since
// CLI files are parsed before user-global).
func TestSchedulesCLIOutranksUser(t *testing.T) {
	const cliYAML = `
schedules:
  - name: cli-schedule
    cron: "0 0 * * *"
    prompt: "cli"
`
	const userYAML = `
schedules:
  - name: user-schedule
    cron: "0 12 * * *"
    prompt: "user"
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
			case "/cli/schedules.yaml":
				return []byte(cliYAML), nil
			case "/cfg/mecatl/settings.yaml":
				return []byte(userYAML), nil
			}
			return nil, errors.New("not found")
		},
	}
	r := newWithEnv(Options{Conventional: true, ExplicitFiles: []string{"/cli/schedules.yaml"}}, env)
	s := r.OperatorSchedules()
	if s == nil || len(s.Items) != 1 || s.Items[0].Name != "cli-schedule" {
		t.Fatalf("CLI schedules must out-rank user-global; got %+v", s)
	}
}
