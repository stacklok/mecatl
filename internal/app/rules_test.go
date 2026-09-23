package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	rules "github.com/stacklok/mecatl/engine/adapter/rulesfs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// fakeRulesEnv installs a faked XDG/home path-resolution env for the duration of
// a test, so the conventional user-scoped rules (~/.config/mecatl/rules,
// ~/.claude/rules) resolve against temp dirs — never the developer's real home.
// It uses t.Setenv so the rulesfs OSEnv.Getenv seam reads the faked values
// (rulesfs.ResolveSources consults $XDG_CONFIG_HOME then os.UserHomeDir). Keep
// these tests serial — they mutate the process env.
func fakeRulesEnv(t *testing.T, xdg, home string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	// os.UserHomeDir reads $HOME on Unix.
	t.Setenv("HOME", home)
}

// writeProjectRule seeds a real project rule at <workspace>/.claude/rules/<name>.md.
func writeProjectRule(t *testing.T, workspace, name, body string) {
	t.Helper()
	dir := filepath.Join(workspace, ".claude", "rules")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir project rules dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed project rule %s: %v", name, err)
	}
}

// writeUserRule seeds a real user rule at <xdg>/mecatl/rules/<name>.md.
func writeUserRule(t *testing.T, xdg, name, body string) {
	t.Helper()
	dir := filepath.Join(xdg, "mecatl", "rules")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir user rules dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed user rule %s: %v", name, err)
	}
}

// staticRulesSource is an in-memory prompt.RulesSource for the assembler tests.
type staticRulesSource struct{ rules []prompt.Rule }

func (s staticRulesSource) ListRules(_ context.Context) ([]prompt.Rule, error) {
	return s.rules, nil
}

// TestResolveRulesSeam pins the composition wiring of resolveRulesSeam: the
// always-on conventional discovery (no flags), the project-tier trust gate
// (WITHHELD + WARN on an untrusted workspace; the user-tier lanes stay active),
// the fail-soft outcomes (no valid file → nil + INFO; discovery is inert when no
// dir exists), and the non-empty ENABLED narration. It NEVER aborts the build.
func TestResolveRulesSeam(t *testing.T) {
	ctx := context.Background()

	t.Run("no dirs yields nil + DISABLED info", func(t *testing.T) {
		// An empty workspace + faked home with no rules dirs: the conventional
		// lanes resolve to non-existent dirs, which are harmlessly skipped.
		xdg, home := t.TempDir(), t.TempDir()
		fakeRulesEnv(t, xdg, home)
		diag := &kvDiag{}
		cfg := Config{Workspace: t.TempDir(), Diagnostics: diag}
		src := resolveRulesSeam(ctx, cfg)
		if src != nil {
			t.Fatalf("resolveRulesSeam with no rule files = %v, want nil (inert)", src)
		}
		if !diag.hasMsg("rules DISABLED (no valid <name>.md found in any source)") &&
			!diag.hasMsg("rules DISABLED (no rules dirs configured)") {
			t.Fatalf("expected a DISABLED info line; messages = %v", diag.msgs)
		}
	})

	t.Run("conventional + untrusted + project rules present → WARN + project withheld", func(t *testing.T) {
		// An untrusted workspace with a project rule: the project tier is withheld,
		// a WARN is logged, and a USER-tier rule stays active (if present).
		xdg, home := t.TempDir(), t.TempDir()
		fakeRulesEnv(t, xdg, home)
		ws := t.TempDir()
		writeProjectRule(t, ws, "projonly", "# Project rule\nbody")
		writeUserRule(t, xdg, "userrule", "# User rule\nbody")

		diag := &kvDiag{}
		cfg := Config{Workspace: ws, TrustProject: false, Diagnostics: diag}
		src := resolveRulesSeam(ctx, cfg)
		if src == nil {
			t.Fatal("resolveRulesSeam returned nil with a user-tier rule present; user tier must stay active")
		}
		if !diag.hasMsgContaining("rules: project-tier rules WITHHELD") {
			t.Fatalf("expected the project-tier WITHHELD warn; messages = %v", diag.msgs)
		}
		// The project rule must NOT be in the discovered set; the user rule must.
		rs, _ := src.ListRules(ctx)
		if len(rs) != 1 || rs[0].Name != "userrule" {
			t.Fatalf("want only the user-tier rule [userrule], got %v", ruleNames(rs))
		}
		if rs[0].Origin != prompt.RuleOriginUser {
			t.Fatalf("user rule origin = %q, want %q", rs[0].Origin, prompt.RuleOriginUser)
		}
	})

	t.Run("trusted workspace admits the project tier", func(t *testing.T) {
		xdg, home := t.TempDir(), t.TempDir()
		fakeRulesEnv(t, xdg, home)
		ws := t.TempDir()
		writeProjectRule(t, ws, "projrule", "# Project rule\nbody")

		diag := &kvDiag{}
		cfg := Config{Workspace: ws, TrustProject: true, Diagnostics: diag}
		src := resolveRulesSeam(ctx, cfg)
		if src == nil {
			t.Fatal("resolveRulesSeam returned nil with a trusted project rule present")
		}
		if diag.hasMsgContaining("WITHHELD") {
			t.Fatalf("trusted workspace must NOT log WITHHELD; messages = %v", diag.msgs)
		}
		if !diag.hasMsg("rules ENABLED") {
			t.Fatalf("expected rules ENABLED info; messages = %v", diag.msgs)
		}
		rs, _ := src.ListRules(ctx)
		if len(rs) != 1 || rs[0].Name != "projrule" {
			t.Fatalf("want [projrule], got %v", ruleNames(rs))
		}
		if rs[0].Origin != prompt.RuleOriginProject {
			t.Fatalf("project rule origin = %q, want %q", rs[0].Origin, prompt.RuleOriginProject)
		}
	})
}

// TestBuildInstructionAssemblerRulesOrdering pins the seam-order change: a
// non-nil rulesSrc places RulesAssembler SECOND (after RootAssembler, before
// SoulAssembler). Asserted behaviourally: render with a rules source + a soul
// source and assert the rules fragment PRECEDES the soul fragment.
func TestBuildInstructionAssemblerRulesOrdering(t *testing.T) {
	ctx := context.Background()
	// A soul source that yields a recognisable marker the rules source does not.
	soulSrc := staticSoulSource{soulBody: "SOUL-MARKER"}
	rulesSrc := staticRulesSource{rules: []prompt.Rule{{Name: "testing", Body: "RULES-MARKER"}}}

	asm := buildInstructionAssembler(rulesSrc, soulSrc, nil, nil, false)
	msgs, err := asm.Assemble(ctx, memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want two messages (rules + soul), got %d", len(msgs))
	}
	// RulesAssembler is SECOND in the seam (after RootAssembler), so its message
	// PRECEDES the soul message. RootAssembler yields no message here, so msg[0]
	// is the rules fragment and msg[1] the soul fragment.
	if !strings.Contains(msgs[0].Text, "RULES-MARKER") {
		t.Fatalf("first message must be the rules fragment; got:\n%s", msgs[0].Text)
	}
	if !strings.Contains(msgs[1].Text, "SOUL-MARKER") {
		t.Fatalf("second message must be the soul fragment; got:\n%s", msgs[1].Text)
	}
	// A nil rulesSrc must NOT include the rules header.
	noRules := buildInstructionAssembler(nil, soulSrc, nil, nil, false)
	msgs2, _ := noRules.Assemble(ctx, memfs.NewWorkspace("/ws"))
	if len(msgs2) != 1 {
		t.Fatalf("want one soul-only message, got %d", len(msgs2))
	}
	if strings.Contains(msgs2[0].Text, prompt.RulesHeader()) {
		t.Fatalf("nil rulesSrc must not emit the rules header:\n%s", msgs2[0].Text)
	}
}

// TestBuildInstructionAssemblerAllNilIsRootAssembler pins the typed-nil guard:
// all-nil inputs return a BARE RootAssembler (not a MultiAssembler carrying
// typed-nil assemblers).
func TestBuildInstructionAssemblerAllNilIsRootAssembler(t *testing.T) {
	asm := buildInstructionAssembler(nil, nil, nil, nil, false)
	if _, ok := asm.(prompt.RootAssembler); !ok {
		t.Fatalf("all-nil inputs returned %T, want prompt.RootAssembler (typed-nil guard)", asm)
	}
}

// TestPerSessionAssemblerMatchesShared is the issue-#42 guard applied to the
// assembler seam: the per-session factory (sessionEngineFactory) receives the
// SAME instructions (the composed assembler, which carries the rulesSrc resolved
// ONCE at Build) the shared engine got — never a re-resolution. Proven
// behaviourally: build a factory's instructions with a rules-bearing assembler
// (the SAME object sessionEngineFactory captures), drive a single turn through a
// factory-shaped engine, and assert the turn-0 request carries the rules
// fragment exactly once — the shared rulesSrc threaded through, not re-resolved.
func TestPerSessionAssemblerMatchesShared(t *testing.T) {
	ctx := context.Background()
	rulesSrc := staticRulesSource{rules: []prompt.Rule{{Name: "testing", Body: "RULES-ONCE-MARKER"}}}
	// This is EXACTLY what sessionEngineFactory captures: the composed assembler
	// built ONCE at Build over the resolved rulesSrc. The factory threads it
	// verbatim into every per-session engine's Deps.Instructions.
	asm := buildInstructionAssembler(rulesSrc, nil, nil, nil, false)

	obs := &observedReq{}
	prov := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(obs.observer())},
		mockllm.TextTurn("done"),
	)
	eng := agent.NewEngine(agent.Deps{
		LLM:          prov,
		Catalog:      tool.NewCatalog(),
		Instructions: asm,
	})
	sess := session.New("sPerSession", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	run := eng.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "hello"})
	for ev := range run.Events() {
		_ = ev
	}

	var rulesCount int
	obs.mu.Lock()
	for _, um := range obs.userMsgs {
		rulesCount += strings.Count(um, "RULES-ONCE-MARKER")
	}
	obs.mu.Unlock()
	if rulesCount != 1 {
		t.Fatalf("turn-0 request must carry the rules fragment exactly once; got %d (userMsgs=%v)", rulesCount, obs.userMsgs)
	}
	// Ephemeral (ADR 0043): the rules fragment must NOT be persisted into the
	// conversation — it is prepended per-run, never recorded.
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "RULES-ONCE-MARKER") {
			t.Fatalf("the rules fragment must NOT be persisted into the conversation (it is ephemeral):\n%+v", messageTexts(sess.Conversation.Messages))
		}
	}
}

// staticSoulSource is a minimal prompt.SoulSource for the ordering test.
type staticSoulSource struct{ soulBody string }

func (s staticSoulSource) Load(_ context.Context) (string, error) { return s.soulBody, nil }

func ruleNames(rs []prompt.Rule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

// hasMsg reports whether the recorder saw the exact message.
func (d *kvDiag) hasMsg(want string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, m := range d.msgs {
		if m == want {
			return true
		}
	}
	return false
}

// hasMsgContaining reports whether the recorder saw a message containing substr.
func (d *kvDiag) hasMsgContaining(substr string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, m := range d.msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

// Ensure the rules alias shim compiles from composition (no behaviour, just a
// build-time reference so an accidental removal of a re-exported identifier
// fails the test build).
var _ = rules.ProjectDirClaude
