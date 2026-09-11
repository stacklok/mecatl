package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func sampleTools() []tool.ToolSpec {
	return []tool.ToolSpec{
		{Name: "Read", Description: "Read a file's contents.\nMore detail here."},
		{Name: "Edit", Description: "Edit a file in place."},
		{Name: "Shell", Description: "Run a shell command."},
	}
}

// TestStablePrefixByteStableAcrossEnv is the gauntlet #6 cache invariant: the
// StablePrefix must be byte-identical across two builds that differ only in env
// (date, cwd, model, mode).
func TestStablePrefixByteStableAcrossEnv(t *testing.T) {
	base := prompt.Config{Tools: sampleTools()}

	cfgA := base
	cfgA.Env = prompt.Env{
		Cwd: "/home/a/proj", OS: "linux", Model: "model-x",
		Date: "2026-05-29", Mode: "default",
		Shell: "/bin/bash", GitStatus: "branch: main\nstatus:\n(clean)",
	}
	cfgB := base
	cfgB.Env = prompt.Env{
		Cwd: "/tmp/other", OS: "darwin", Model: "model-y",
		Date: "1999-12-31", Mode: "plan",
		Shell: "/bin/zsh", GitStatus: "branch: feature\nstatus:\n M file.go",
	}

	a := prompt.Build(cfgA)
	b := prompt.Build(cfgB)

	if a.StablePrefix != b.StablePrefix {
		t.Fatalf("StablePrefix changed when only env changed:\nA=%q\nB=%q",
			a.StablePrefix, b.StablePrefix)
	}

	// Determinism: same config builds the same prefix.
	if prompt.Build(cfgA).StablePrefix != a.StablePrefix {
		t.Fatal("StablePrefix not deterministic for identical config")
	}

	// The volatile suffix MUST differ since the env differs.
	if a.VolatileSuffix == b.VolatileSuffix {
		t.Fatal("VolatileSuffix identical despite differing env")
	}
}

// TestStablePrefixContainsToolsAndNoVolatile asserts the prefix lists tool names
// and contains no date/cwd/model leakage.
func TestStablePrefixContainsToolsAndNoVolatile(t *testing.T) {
	cfg := prompt.Config{
		Tools: sampleTools(),
		Env: prompt.Env{
			Cwd: "/secret/cwd/path", OS: "linux", Model: "leaky-model-id",
			Date: "2026-05-29", Mode: "acceptEdits",
			Shell: "/secret/shell-path", GitStatus: "branch: secret-branch-marker",
		},
	}
	got := prompt.Build(cfg).StablePrefix

	for _, name := range []string{"Read", "Edit", "Shell"} {
		if !strings.Contains(got, name) {
			t.Errorf("StablePrefix missing tool name %q\nprefix=%q", name, got)
		}
	}
	// One-line purpose should come from the first line only.
	if strings.Contains(got, "More detail here.") {
		t.Errorf("StablePrefix included non-first-line description text")
	}

	for _, vol := range []string{
		"/secret/cwd/path", "leaky-model-id", "2026-05-29", "acceptEdits", "<env>",
		"/secret/shell-path", "secret-branch-marker",
	} {
		if strings.Contains(got, vol) {
			t.Errorf("StablePrefix leaked volatile value %q\nprefix=%q", vol, got)
		}
	}
}

func TestEnvBlockDeterministicAndComplete(t *testing.T) {
	env := prompt.Env{
		Cwd: "/w", OS: "linux", Model: "m1", Date: "2026-05-29", Mode: "default",
		Shell: "/bin/bash", GitStatus: "branch: main\nstatus:\n(clean)",
	}
	first := prompt.EnvBlock(env)
	second := prompt.EnvBlock(env)
	if first != second {
		t.Fatalf("EnvBlock not deterministic:\n1=%q\n2=%q", first, second)
	}

	for _, want := range []string{
		"<env>", "</env>",
		"cwd: /w", "os: linux", "model: m1",
		"date: 2026-05-29", "permission-mode: default", "shell: /bin/bash",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("EnvBlock missing %q\ngot=%q", want, first)
		}
	}

	// Stable key order: keys appear sorted alphabetically; shell sorts last.
	wantOrder := []string{"cwd:", "date:", "model:", "os:", "permission-mode:", "shell:"}
	idx := -1
	for _, k := range wantOrder {
		at := strings.Index(first, k)
		if at <= idx {
			t.Fatalf("EnvBlock key %q out of order in %q", k, first)
		}
		idx = at
	}

	// The multi-line git-status sub-block appears AFTER the sorted scalar pairs.
	gsAt := strings.Index(first, "<git-status>")
	if gsAt < idx {
		t.Fatalf("EnvBlock <git-status> sub-block must come after the scalar pairs\ngot=%q", first)
	}
}

// TestToolDisciplineHints exercises the generated tool-discipline guidance: a
// full catalog emits every dedicated-tool and delegation clause plus the
// reserve-Shell and generic parallel lines; a Shell-absent catalog omits the reserve
// line; an empty catalog emits only the generic parallel line with no dangling
// heading; and output is byte-stable on repeat.
func TestToolDisciplineHints(t *testing.T) {
	full := []tool.ToolSpec{
		{Name: "Read"}, {Name: "Edit"}, {Name: "Write"}, {Name: "Glob"},
		{Name: "Grep"}, {Name: "Shell"}, {Name: "Subagent"}, {Name: "Parallel"},
		{Name: "Team"}, {Name: "Remember"},
	}
	got := prompt.Build(prompt.Config{Tools: full}).StablePrefix
	for _, want := range []string{
		"Use the dedicated tool when one fits:",
		"Read (not cat/head/tail/sed) to read files",
		"Edit (not sed/awk) to modify files",
		"Write (not heredoc/echo) to create files",
		"Glob (not find/ls) to locate files",
		"Grep (not grep/rg) to search contents",
		"Reserve Shell for real system/terminal commands.",
		"Use Subagent for focused delegation.",
		"issue one Subagent call per task in the same assistant turn so eligible calls run concurrently",
		"wait between calls only when a later task depends on an earlier result.",
		"Use Parallel only for isolated writable or competing branches that need built-in join or winner selection.",
		"Do not use Parallel merely for independent read-only investigation; use same-turn Subagent calls instead.",
		"Use Team only for workers that must coordinate through shared tasks or messages over multiple rounds.",
		"Use same-turn read-only Subagent calls instead for independent result-only fan-out.",
		"Use the memory tools to persist or recall durable facts across sessions.",
		"Make independent tool calls in parallel; never pass placeholder or guessed arguments.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("full-catalog hints missing %q\nprefix=%q", want, got)
		}
	}

	// Shell absent → no reserve-Shell line.
	noShell := prompt.Build(prompt.Config{Tools: []tool.ToolSpec{{Name: "Read"}}}).StablePrefix
	if strings.Contains(noShell, "Reserve Shell") {
		t.Errorf("Shell-absent catalog should omit the reserve-Shell line\nprefix=%q", noShell)
	}
	if !strings.Contains(noShell, "Make independent tool calls in parallel") {
		t.Errorf("parallel line must always be present\nprefix=%q", noShell)
	}

	// Empty catalog → only the parallel line, NO dangling dedicated-tool heading.
	empty := prompt.Build(prompt.Config{Tools: nil}).StablePrefix
	if strings.Contains(empty, "Use the dedicated tool when one fits:") {
		t.Errorf("empty catalog must not emit a dangling dedicated-tool heading\nprefix=%q", empty)
	}
	if !strings.Contains(empty, "Make independent tool calls in parallel") {
		t.Errorf("empty catalog must still emit the parallel line\nprefix=%q", empty)
	}

	// Byte-stable on repeat.
	if prompt.Build(prompt.Config{Tools: full}).StablePrefix != got {
		t.Error("tool-discipline hints not byte-stable across identical builds")
	}
}

func TestToolDisciplineHintsDoNotAdvertiseAbsentDelegationTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tools  []tool.ToolSpec
		wants  []string
		avoids []string
	}{
		{
			name:   "subagent only",
			tools:  []tool.ToolSpec{{Name: "Subagent"}},
			wants:  []string{"Use Subagent for focused delegation."},
			avoids: []string{"Use Parallel", "Use Team"},
		},
		{
			name:   "parallel only",
			tools:  []tool.ToolSpec{{Name: "Parallel"}},
			wants:  []string{"Use Parallel only for isolated writable or competing branches"},
			avoids: []string{"Subagent", "Use Team"},
		},
		{
			name:   "team only",
			tools:  []tool.ToolSpec{{Name: "Team"}},
			wants:  []string{"Use Team only for workers that must coordinate"},
			avoids: []string{"Subagent", "Use Parallel"},
		},
		{
			name:  "subagent and parallel",
			tools: []tool.ToolSpec{{Name: "Parallel"}, {Name: "Subagent"}},
			wants: []string{
				"Use Subagent for focused delegation.",
				"Use Parallel only for isolated writable or competing branches",
				"use same-turn Subagent calls instead",
			},
			avoids: []string{"Use Team"},
		},
		{
			name:  "subagent and team",
			tools: []tool.ToolSpec{{Name: "Team"}, {Name: "Subagent"}},
			wants: []string{
				"Use Subagent for focused delegation.",
				"Use Team only for workers that must coordinate",
				"Use same-turn read-only Subagent calls instead",
			},
			avoids: []string{"Use Parallel"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := prompt.Build(prompt.Config{Tools: tt.tools}).StablePrefix
			for _, want := range tt.wants {
				if !strings.Contains(got, want) {
					t.Errorf("missing guidance %q\nprefix=%q", want, got)
				}
			}
			for _, avoid := range tt.avoids {
				if strings.Contains(got, avoid) {
					t.Errorf("advertises absent delegation tool via %q\nprefix=%q", avoid, got)
				}
			}
		})
	}
}

// TestToolDisciplineHintsFixedEmitOrder asserts the dedicated-tool clauses and
// delegation guidance are emitted in their documented order regardless of catalog
// order, and that the generic parallel-calls line is always last.
func TestToolDisciplineHintsFixedEmitOrder(t *testing.T) {
	// Register the tools OUT of documented order to prove the emit order is fixed by
	// the table, not by the caller's catalog order.
	tools := []tool.ToolSpec{
		{Name: "Team"}, {Name: "Grep"}, {Name: "Parallel"}, {Name: "Write"},
		{Name: "Subagent"}, {Name: "Read"}, {Name: "Glob"}, {Name: "Edit"},
	}
	got := prompt.Build(prompt.Config{Tools: tools}).StablePrefix

	ordered := []string{
		"Read (not cat/head/tail/sed) to read files",
		"Edit (not sed/awk) to modify files",
		"Write (not heredoc/echo) to create files",
		"Glob (not find/ls) to locate files",
		"Grep (not grep/rg) to search contents",
		"Use Subagent for focused delegation.",
		"Use Parallel only for isolated writable or competing branches",
		"Do not use Parallel merely for independent read-only investigation",
		"Use Team only for workers that must coordinate",
		"Use same-turn read-only Subagent calls instead for independent result-only fan-out.",
	}
	prev := -1
	for _, clause := range ordered {
		at := strings.Index(got, clause)
		if at < 0 {
			t.Fatalf("clause %q absent\nprefix=%q", clause, got)
		}
		if at <= prev {
			t.Errorf("clause %q out of documented order (index %d, previous %d)\nprefix=%q",
				clause, at, prev, got)
		}
		prev = at
	}

	// The generic parallel-calls line must come AFTER every catalog-aware clause (i.e. last).
	parallel := strings.Index(got, "Make independent tool calls in parallel")
	if parallel < 0 {
		t.Fatalf("parallel-calls line absent\nprefix=%q", got)
	}
	if parallel <= prev {
		t.Errorf("parallel-calls line (index %d) is not LAST; a dedicated clause follows it (last clause at %d)\nprefix=%q",
			parallel, prev, got)
	}
}

// TestBuildPlanModeReminder asserts the plan-mode reminder rides the VOLATILE
// suffix (it varies with the session mode) and never leaks into the cache-stable
// prefix; non-plan modes emit no reminder. It ALSO asserts the plan-approval
// workflow contract (issue #206 follow-up): the PresentPlan gate language + the
// inline-is-NOT-approval clause are present in the plan-mode suffix.
func TestBuildPlanModeReminder(t *testing.T) {
	const marker = "Plan mode is active"

	plan := prompt.Build(prompt.Config{Tools: sampleTools(), Env: prompt.Env{Mode: "plan"}})
	if !strings.Contains(plan.VolatileSuffix, marker) {
		t.Errorf("plan mode: reminder missing from VolatileSuffix\ngot=%q", plan.VolatileSuffix)
	}
	if strings.Contains(plan.StablePrefix, marker) {
		t.Errorf("plan mode: reminder leaked into StablePrefix\ngot=%q", plan.StablePrefix)
	}

	// Issue #206 follow-up: the plan-approval workflow contract must be in the
	// plan-mode volatile suffix — it tells the model to call PresentPlan and
	// that inline "acceptable" is NOT approval.
	for _, clause := range []string{
		"call the PresentPlan tool EXACTLY ONCE",
		"and STOP",
		"inline",
		"is NOT approval",
		"PresentPlan gate",
		"Pass the FULL plan text in the PresentPlan `plan` argument",
	} {
		if !strings.Contains(plan.VolatileSuffix, clause) {
			t.Errorf("plan mode: plan-approval contract clause %q missing from VolatileSuffix\ngot=%q",
				clause, plan.VolatileSuffix)
		}
	}
	// The contract must NOT leak into the stable prefix (same discipline as the
	// general reminder — this is per-turn volatile content).
	if strings.Contains(plan.StablePrefix, "PresentPlan") {
		t.Errorf("plan mode: plan-approval contract leaked into StablePrefix\ngot=%q", plan.StablePrefix)
	}

	def := prompt.Build(prompt.Config{Tools: sampleTools(), Env: prompt.Env{Mode: "default"}})
	if strings.Contains(def.VolatileSuffix, marker) {
		t.Errorf("default mode: unexpected plan reminder\ngot=%q", def.VolatileSuffix)
	}
	if strings.Contains(def.VolatileSuffix, "PresentPlan") {
		t.Errorf("default mode: unexpected plan-approval contract\ngot=%q", def.VolatileSuffix)
	}
}

// TestEnvBlockGitStatusSubBlock verifies the multi-line git snapshot renders into
// a dedicated <git-status> sub-block, an empty snapshot emits nothing, and the
// rendering is deterministic.
func TestEnvBlockGitStatusSubBlock(t *testing.T) {
	withGit := prompt.EnvBlock(prompt.Env{GitStatus: "branch: main\nstatus:\n M a.go\ncommits:\nabc123 fix"})
	if !strings.Contains(withGit, "<git-status>\nbranch: main\nstatus:\n M a.go\ncommits:\nabc123 fix\n</git-status>") {
		t.Errorf("git-status sub-block not rendered as expected\ngot=%q", withGit)
	}

	noGit := prompt.EnvBlock(prompt.Env{Cwd: "/w"})
	if strings.Contains(noGit, "<git-status>") {
		t.Errorf("empty GitStatus must emit no sub-block\ngot=%q", noGit)
	}

	det1 := prompt.EnvBlock(prompt.Env{GitStatus: "branch: x"})
	det2 := prompt.EnvBlock(prompt.Env{GitStatus: "branch: x"})
	if det1 != det2 {
		t.Error("EnvBlock with git-status not deterministic")
	}
}

// TestDefaultToneSafetyAndCorrectnessClauses pins the surviving default-tone
// contract (ADR 0041 as rebalanced by ADR 0054, the prose-economy control surface
// removed by the ADR superseding 0041) in the default StablePrefix. The
// output-economy "terse" operator knob was REMOVED; these clauses — the
// investigation-depth / minimum-change / safety / read-before-edit / trust-boundary
// guidance baked into the always-on defaultTone — STAY. It asserts the load-bearing
// clauses are present: (1) brevity scoped to the FINAL message to the client, with
// reasoning named as a PROTECTED channel and an explicit exemption list — the
// interleaved-reasoning regression (Claude Code #32508/#42796) where "be brief"
// suppressed cognition; (2) the explicit thoroughness directives that push the
// model to reason before acting and work through edge cases; (3) the minimum-code
// ladder (the upward "does this need to exist / stdlib / dep / one line / minimum"
// rungs) — the ponytail-measured biggest lever; (4) the safety carveout that is
// measured to be load-bearing against the "one-liner drops a guard" failure
// (ponytail Axis 2); (5) the Edit-over-Write nudge — the mecatl-native economy
// lever. All live in StablePrefix (cache-stable, gauntlet #6). If any clause is
// silently dropped or weakened in a future tone rewrite, this fails.
func TestDefaultToneSafetyAndCorrectnessClauses(t *testing.T) {
	got := prompt.Build(prompt.Config{Tools: sampleTools()}).StablePrefix

	for _, want := range []string{
		// (1) Brevity scoped to the final message; reasoning protected; exemption.
		"your final answer to the client",
		"Brevity applies to what you write for the reader",
		"It does NOT mean: read less",
		// (2) Thoroughness directives.
		"Reason through the problem before you change anything",
		"work through edge cases and failure modes",
		// (3) Minimum-code ladder.
		"stop at the first rung that holds",
		"does the standard library do it",
		"already-imported dependency",
		// (4) Safety carveout — never cut these.
		"Never cut these to hit a smaller line count",
		"input validation at trust boundaries",
		// (5) Edit-over-Write economy nudge.
		"Prefer Edit (emit only the change) over Write",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("defaultTone clause missing %q\nprefix=%q", want, got)
		}
	}
}

func TestDiscoverInstructionsAgentsPresent(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	mustWrite(t, ws, "AGENTS.md", "Use tabs, not spaces.")

	msgs, err := prompt.DiscoverInstructions(context.Background(), ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.Role != session.RoleUser {
		t.Errorf("want RoleUser, got %q", m.Role)
	}
	if !strings.Contains(m.Text, "Project instructions (AGENTS.md):") {
		t.Errorf("missing provenance marker: %q", m.Text)
	}
	if !strings.Contains(m.Text, "Use tabs, not spaces.") {
		t.Errorf("missing file content: %q", m.Text)
	}
}

func TestDiscoverInstructionsNeitherPresent(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	msgs, err := prompt.DiscoverInstructions(context.Background(), ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("want 0 messages, got %d", len(msgs))
	}
}

// TestDiscoverInstructionsAgentsWinsOverClaude documents and verifies the chosen
// precedence: when both files exist, AGENTS.md wins and CLAUDE.md is ignored.
func TestDiscoverInstructionsAgentsWinsOverClaude(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	mustWrite(t, ws, "AGENTS.md", "AGENTS content")
	mustWrite(t, ws, "CLAUDE.md", "CLAUDE content")

	msgs, err := prompt.DiscoverInstructions(context.Background(), ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want exactly 1 message (AGENTS wins), got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Text, "AGENTS content") {
		t.Errorf("expected AGENTS.md content, got %q", msgs[0].Text)
	}
	if strings.Contains(msgs[0].Text, "CLAUDE content") {
		t.Errorf("CLAUDE.md should be ignored when AGENTS.md present: %q", msgs[0].Text)
	}
}

// TestDiscoverInstructionsClaudeFallback verifies CLAUDE.md is used when only it
// exists, and that an empty AGENTS.md falls through to it.
func TestDiscoverInstructionsClaudeFallback(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	mustWrite(t, ws, "AGENTS.md", "   \n\t  ") // whitespace-only -> treated as absent
	mustWrite(t, ws, "CLAUDE.md", "CLAUDE fallback content")

	msgs, err := prompt.DiscoverInstructions(context.Background(), ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Text, "Project instructions (CLAUDE.md):") {
		t.Errorf("missing CLAUDE.md provenance marker: %q", msgs[0].Text)
	}
	if !strings.Contains(msgs[0].Text, "CLAUDE fallback content") {
		t.Errorf("missing CLAUDE.md content: %q", msgs[0].Text)
	}
}

func mustWrite(t *testing.T, ws tool.Workspace, path, content string) {
	t.Helper()
	if _, err := ws.CreateFile(context.Background(), path, []byte(content)); err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
}

// TestBuildSatisfiesBuilder pins that the default builder is directly
// assignable to the Builder seam a host plugs into agent.Deps.PromptBuilder
// (issue #127). If Build's signature ever drifts from func(Config) Layered, the
// whole seam's premise breaks and this fails at compile time.
func TestBuildSatisfiesBuilder(t *testing.T) {
	var _ prompt.Builder = prompt.Build
	// Also exercise it through the seam so the assertion is not purely
	// compile-time: a Builder value holding Build must produce the same Layered
	// as a direct call.
	cfg := prompt.Config{Tools: sampleTools()}
	var b prompt.Builder = prompt.Build
	if got := b(cfg); got != prompt.Build(cfg) {
		t.Fatalf("Builder holding Build diverged from direct prompt.Build call")
	}
}
