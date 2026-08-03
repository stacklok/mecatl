package main

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

// resolveCommand is a PURE seam (no os.Args, no os.Exit, no I/O), so these tests
// exercise the REAL production command-resolution logic directly — they do NOT
// mutate global state, do NOT bind a listener, and do NOT call os.Exit.

// --- Requirement 1: pure resolution seam (mode + remaining) ----------------

func TestResolveServeStripsCommandWord(t *testing.T) {
	res := resolveCommand([]string{"mecated", "serve", "--workspace", "/tmp/w"})
	if res.handled {
		t.Fatal("serve should not be handled; it falls through to run()")
	}
	if res.err != nil {
		t.Fatalf("serve resolution error: %v", res.err)
	}
	if res.mode != modeServe {
		t.Errorf("mode = %q, want serve", res.mode)
	}
	if len(res.remaining) != 2 || res.remaining[0] != "--workspace" || res.remaining[1] != "/tmp/w" {
		t.Errorf("remaining = %v, want [--workspace /tmp/w]", res.remaining)
	}
	if res.run != nil {
		t.Error("serve must not carry a subcommand runner")
	}
}

func TestResolveAcpStripsCommandWord(t *testing.T) {
	res := resolveCommand([]string{"mecated", "acp"})
	if res.handled || res.err != nil {
		t.Fatalf("acp handled=%v err=%v, want false/nil", res.handled, res.err)
	}
	if res.mode != modeACP {
		t.Errorf("mode = %q, want acp", res.mode)
	}
	if len(res.remaining) != 0 {
		t.Errorf("remaining = %v, want empty", res.remaining)
	}
}

func TestResolveBareNoArgsIsLegacy(t *testing.T) {
	res := resolveCommand([]string{"mecated"})
	if res.handled || res.err != nil {
		t.Fatalf("bare handled=%v err=%v, want false/nil", res.handled, res.err)
	}
	if res.mode != modeLegacy {
		t.Errorf("mode = %q, want legacy", res.mode)
	}
	if len(res.remaining) != 0 {
		t.Errorf("remaining = %v, want empty", res.remaining)
	}
}

func TestResolveLeadingFlagIsLegacy(t *testing.T) {
	res := resolveCommand([]string{"mecated", "--workspace", "/tmp/test"})
	if res.handled || res.err != nil {
		t.Fatalf("leading-flag handled=%v err=%v, want false/nil", res.handled, res.err)
	}
	if res.mode != modeLegacy {
		t.Errorf("mode = %q, want legacy", res.mode)
	}
	if len(res.remaining) != 2 || res.remaining[0] != "--workspace" {
		t.Errorf("remaining = %v, want [--workspace /tmp/test]", res.remaining)
	}
}

func TestResolveBareAcpFlagIsLegacy(t *testing.T) {
	res := resolveCommand([]string{"mecated", "--acp"})
	if res.handled || res.err != nil {
		t.Fatalf("bare --acp handled=%v err=%v, want false/nil", res.handled, res.err)
	}
	if res.mode != modeLegacy {
		t.Errorf("mode = %q, want legacy (the --acp flag is a legacy form)", res.mode)
	}
}

// --- Requirement 2: fail closed for ALL command-group typos/missing subs ----

func TestResolveUnknownCommandFailsClosed(t *testing.T) {
	// `mecated srve` (typo) must NOT fall through to run() — it errors before the
	// daemon boots. main() prints res.err and exits non-zero.
	res := resolveCommand([]string{"mecated", "srve"})
	if res.err == nil {
		t.Fatal("unknown command 'srve' resolved with nil error; want a fail-closed error before daemon startup")
	}
	if !strings.Contains(res.err.Error(), "srve") {
		t.Errorf("error %q does not name the unknown command 'srve'", res.err)
	}
	if !strings.Contains(res.err.Error(), "Available commands") {
		t.Errorf("error %q does not list available commands", res.err)
	}
}

func TestResolveSkillsBareFailsClosed(t *testing.T) {
	res := resolveCommand([]string{"mecated", "skills"})
	if res.err == nil {
		t.Fatal("bare 'skills' resolved with nil error; want missing-subcommand fail-closed error")
	}
	if !strings.Contains(res.err.Error(), "skills") {
		t.Errorf("error %q does not mention 'skills'", res.err)
	}
	if res.handled {
		t.Error("bare 'skills' must not be handled (no runner); it is a usage error")
	}
}

func TestResolveSkillsWrongSubFailsClosed(t *testing.T) {
	res := resolveCommand([]string{"mecated", "skills", "wrong"})
	if res.err == nil {
		t.Fatal("'skills wrong' resolved with nil error; want unknown-subcommand error")
	}
	if !strings.Contains(res.err.Error(), "wrong") {
		t.Errorf("error %q does not name the unknown subcommand 'wrong'", res.err)
	}
	if res.handled {
		t.Error("'skills wrong' must not be handled; it is a usage error")
	}
}

func TestResolvePerfMcpBareFailsClosed(t *testing.T) {
	res := resolveCommand([]string{"mecated", "perf-mcp"})
	if res.err == nil {
		t.Fatal("bare 'perf-mcp' resolved with nil error; want missing-subcommand error")
	}
	if !strings.Contains(res.err.Error(), "perf-mcp") {
		t.Errorf("error %q does not mention 'perf-mcp'", res.err)
	}
	if res.handled {
		t.Error("bare 'perf-mcp' must not be handled; it is a usage error")
	}
}

func TestResolvePerfMcpWrongSubFailsClosed(t *testing.T) {
	res := resolveCommand([]string{"mecated", "perf-mcp", "wrong"})
	if res.err == nil {
		t.Fatal("'perf-mcp wrong' resolved with nil error; want unknown-subcommand error")
	}
	if !strings.Contains(res.err.Error(), "wrong") {
		t.Errorf("error %q does not name the unknown subcommand 'wrong'", res.err)
	}
	if res.handled {
		t.Error("'perf-mcp wrong' must not be handled; it is a usage error")
	}
}

func TestResolveConfigBareFailsClosed(t *testing.T) {
	res := resolveCommand([]string{"mecated", "config"})
	if res.err == nil {
		t.Fatal("bare 'config' resolved with nil error; want missing-subcommand error")
	}
	if res.handled {
		t.Error("bare 'config' must not be handled; it is a usage error")
	}
}

func TestResolveConfigWrongSubFailsClosed(t *testing.T) {
	res := resolveCommand([]string{"mecated", "config", "wrong"})
	if res.err == nil {
		t.Fatal("'config wrong' resolved with nil error; want unknown-subcommand error")
	}
	if !strings.Contains(res.err.Error(), "wrong") {
		t.Errorf("error %q does not name the unknown subcommand 'wrong'", res.err)
	}
}

// --- Requirement 3: production help renderers used by tests ----------------

// helpRenderOut drives the REAL parseFlagsModeOut (the injected-writer seam)
// with the full production FlagSet — every flag parseFlagsMode registers — and
// returns the rendered help output. It is the seam item 3 asks for: tests
// capture the actual parseFlagsMode/Usage output rather than a synthetic subset,
// so common-help / help-all assertions hold against the full real FlagSet.
func helpRenderOut(t *testing.T, mode commandMode, argv []string) string {
	t.Helper()
	var buf strings.Builder
	_, _, err := parseFlagsModeOut(mode, argv, &buf)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlagsModeOut(%v, %v): %v", mode, argv, err)
	}
	return buf.String()
}

// hasFlagHeader reports whether the rendered help output contains the flag's own
// header line ("  -<name>" at the start of a line, followed by a space, tab, or
// newline) — distinguishing the flag's own entry from a bare mention of "-<name>"
// inside ANOTHER flag's description (e.g. "--headless" appears in the
// --plan-mode-auto-approve description, and "--session-store-url" appears in the
// --child-retention description).
func hasFlagHeader(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		// flag.PrintDefaults emits the header as "  -<name>" possibly followed by
		// " <type>" then a tab or newline.
		rest := strings.TrimPrefix(line, "  -"+name)
		if rest == line {
			continue // not this flag's header line
		}
		// The header line ends after the name: the next char is a space (before a
		// type name), a tab (one-byte bool), or end-of-line. A longer flag whose
		// name starts with this prefix (e.g. "headless" vs "headless-foo") would
		// leave a non-empty rest that is not a separator.
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return true
		}
	}
	return false
}

func TestTopLevelHelpRealRendererContainsCommands(t *testing.T) {
	out := helpRenderOut(t, modeLegacy, []string{"--help"})
	for _, want := range []string{
		"Usage: mecated <command> [flags]",
		"serve                   start the network daemon",
		"acp                     serve the Agent Client Protocol over stdio",
		"config init             write/print the operator settings.yaml skeleton",
		"skills promote          promote a model-authored candidate skill",
		"perf-mcp print-config   print a paste-ready client",
		"deprecated",
		"mecated <command> --help",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("top-level help (real renderer) missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestServeHelpRealRendererShowsFlagList(t *testing.T) {
	out := helpRenderOut(t, modeServe, []string{"--help"})
	if !strings.Contains(out, "Usage: mecated serve [flags]") {
		t.Errorf("serve help missing 'Usage: mecated serve [flags]':\n%s", out)
	}
	if !strings.Contains(out, "Common flags grouped by task") {
		t.Errorf("serve help missing common-help header:\n%s", out)
	}
	if !strings.Contains(out, "--help-all") {
		t.Errorf("serve common help missing pointer to --help-all:\n%s", out)
	}
	// The common help must NOT show the exhaustive flag list.
	if strings.Contains(out, "Compatibility: bare 'mecated") {
		t.Errorf("serve help leaked the legacy compatibility note (should be concise only):\n%s", out)
	}
}

func TestAcpHelpRealRendererShowsFlagList(t *testing.T) {
	out := helpRenderOut(t, modeACP, []string{"--help"})
	if !strings.Contains(out, "Usage: mecated acp [flags]") {
		t.Errorf("acp help missing 'Usage: mecated acp [flags]':\n%s", out)
	}
	if !strings.Contains(out, "Common flags for ACP") {
		t.Errorf("acp help missing ACP common-help header:\n%s", out)
	}
	if !strings.Contains(out, "--help-all") {
		t.Errorf("acp common help missing pointer to --help-all:\n%s", out)
	}
	// ACP common help must NOT show server-boundary flags.
	if strings.Contains(out, "grpc-addr") {
		t.Errorf("acp common help leaked server-boundary flag 'grpc-addr':\n%s", out)
	}
	if strings.Contains(out, "http-addr") {
		t.Errorf("acp common help leaked server-boundary flag 'http-addr':\n%s", out)
	}
}

// --- Requirement 4: real conflict errors -----------------------------------

func TestApplyCommandModeServeWithExplicitAcpConflicts(t *testing.T) {
	cfg := config{acp: true, acpFlagSet: true}
	_, err := applyCommandMode(cfg, modeServe)
	if err == nil {
		t.Fatal("applyCommandMode(serve, --acp) = nil error; want actionable conflict error")
	}
	if !strings.Contains(err.Error(), "serve") || !strings.Contains(err.Error(), "--acp") {
		t.Errorf("conflict error %q is not actionable (must name both 'serve' and '--acp')", err)
	}
}

func TestApplyCommandModeAcpWithAcpFalseConflicts(t *testing.T) {
	cfg := config{acp: false, acpFlagSet: true}
	_, err := applyCommandMode(cfg, modeACP)
	if err == nil {
		t.Fatal("applyCommandMode(acp, --acp=false) = nil error; want actionable conflict error")
	}
	if !strings.Contains(err.Error(), "acp") || !strings.Contains(err.Error(), "--acp=false") {
		t.Errorf("conflict error %q is not actionable (must name both 'acp' and '--acp=false')", err)
	}
}

func TestApplyCommandModeServeWithoutExplicitAcpNoConflict(t *testing.T) {
	cfg := config{acp: false, acpFlagSet: false}
	acp, err := applyCommandMode(cfg, modeServe)
	if err != nil {
		t.Fatalf("serve without --acp: unexpected error %v", err)
	}
	if acp {
		t.Error("serve mode should yield acp=false")
	}
}

func TestApplyCommandModeAcpWithoutExplicitAcpFlagSetsAcp(t *testing.T) {
	cfg := config{acp: false, acpFlagSet: false}
	acp, err := applyCommandMode(cfg, modeACP)
	if err != nil {
		t.Fatalf("acp without --acp flag: unexpected error %v", err)
	}
	if !acp {
		t.Error("canonical acp mode should set acp=true")
	}
}

func TestApplyCommandModeServeWithExplicitAcpFalseNoConflict(t *testing.T) {
	// `mecated serve --acp=false` is NOT a conflict — explicit false agrees with serve.
	cfg := config{acp: false, acpFlagSet: true}
	acp, err := applyCommandMode(cfg, modeServe)
	if err != nil {
		t.Fatalf("serve --acp=false: unexpected conflict %v", err)
	}
	if acp {
		t.Error("serve mode should yield acp=false even with --acp=false")
	}
}

func TestApplyCommandModeLegacyPassesAcpThrough(t *testing.T) {
	cfg := config{acp: true, acpFlagSet: true}
	acp, err := applyCommandMode(cfg, modeLegacy)
	if err != nil {
		t.Fatalf("legacy --acp: unexpected error %v", err)
	}
	if !acp {
		t.Error("legacy --acp=true should pass acp=true through")
	}
	cfg2 := config{acp: false, acpFlagSet: false}
	acp2, err := applyCommandMode(cfg2, modeLegacy)
	if err != nil || acp2 {
		t.Errorf("legacy bare: acp=%v err=%v, want false/nil", acp2, err)
	}
}

// --- Requirement 5: legacy warning projection through an injected seam ------

func TestLegacyWarningBareWarns(t *testing.T) {
	if msg := legacyWarning(modeLegacy, false); msg == "" {
		t.Error("legacyWarning(bare) = empty; want a deprecation warning")
	} else if !strings.Contains(msg, "bare 'mecated'") {
		t.Errorf("legacyWarning(bare) = %q; want it to name bare 'mecated'", msg)
	}
}

func TestLegacyWarningAcpFlagWarns(t *testing.T) {
	if msg := legacyWarning(modeLegacy, true); msg == "" {
		t.Error("legacyWarning(--acp) = empty; want a deprecation warning")
	} else if !strings.Contains(msg, "--acp") {
		t.Errorf("legacyWarning(--acp) = %q; want it to name --acp", msg)
	}
}

func TestLegacyWarningServeDoesNotWarn(t *testing.T) {
	if msg := legacyWarning(modeServe, false); msg != "" {
		t.Errorf("legacyWarning(serve) = %q; want empty (canonical serve must not warn)", msg)
	}
}

func TestLegacyWarningAcpDoesNotWarn(t *testing.T) {
	if msg := legacyWarning(modeACP, true); msg != "" {
		t.Errorf("legacyWarning(acp) = %q; want empty (canonical acp must not warn)", msg)
	}
}

func TestEmitLegacyWarningWritesOnlyForLegacy(t *testing.T) {
	cases := []struct {
		mode    commandMode
		acp     bool
		wantOut bool
	}{
		{modeLegacy, false, true},
		{modeLegacy, true, true},
		{modeServe, false, false},
		{modeACP, true, false},
	}
	for _, c := range cases {
		var sb strings.Builder
		emitLegacyWarning(&sb, c.mode, c.acp)
		got := sb.String()
		if c.wantOut && got == "" {
			t.Errorf("emitLegacyWarning(%v,%v) wrote nothing; want a warning", c.mode, c.acp)
		}
		if !c.wantOut && got != "" {
			t.Errorf("emitLegacyWarning(%v,%v) wrote %q; want nothing (canonical)", c.mode, c.acp, got)
		}
	}
}

// --- Requirement 6: real dispatch/runner tests (no os.Exit) -----------------

// resolveCommand returns a `run` closure for handled subcommands that takes
// injected streams, so dispatch tests exercise the REAL runner without os.Exit
// and without writing outside .scratch.

func TestResolveSkillsPromoteDispatchesRunner(t *testing.T) {
	res := resolveCommand([]string{"mecated", "skills", "promote"})
	if res.err != nil {
		t.Fatalf("skills promote resolved error: %v", res.err)
	}
	if !res.handled || res.run == nil {
		t.Fatal("skills promote should be handled with a runner closure")
	}
	// The runner requires flags; calling it with no args is a usage error (not a
	// panic, not an os.Exit). This proves the dispatch wires the REAL runner.
	err := res.run(strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("skills promote with no flags should return a usage error from the real runner")
	}
}

func TestResolveConfigInitPrintRunsSideEffectFree(t *testing.T) {
	res := resolveCommand([]string{"mecated", "config", "init", "--print"})
	if res.err != nil {
		t.Fatalf("config init --print resolved error: %v", res.err)
	}
	if !res.handled || res.run == nil {
		t.Fatal("config init --print should be handled with a runner closure")
	}
	var out strings.Builder
	// config init --print writes NOTHING to disk; route HOME/XDG at a temp dir so
	// any accidental write would be harmless and detectable.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := res.run(strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("config init --print runner: %v", err)
	}
	if !strings.Contains(out.String(), "GENERATED commented skeleton") {
		t.Errorf("config init --print output does not look like the skeleton:\n%s", out.String())
	}
}

func TestResolvePerfMCPrintConfigRunsPureRunner(t *testing.T) {
	res := resolveCommand([]string{"mecated", "perf-mcp", "print-config"})
	if res.err != nil {
		t.Fatalf("perf-mcp print-config resolved error: %v", res.err)
	}
	if !res.handled || res.run == nil {
		t.Fatal("perf-mcp print-config should be handled with a runner closure")
	}
	var out strings.Builder
	if err := res.run(strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("perf-mcp print-config runner: %v", err)
	}
	if !strings.Contains(out.String(), "mcpServers") {
		t.Errorf("perf-mcp print-config output does not look like the .mcp.json skeleton:\n%s", out.String())
	}
}

// --- Requirement 7: pure-seam proof unknown command errors before run ------

// TestResolveUnknownCommandDoesNotConstructRunner proves an unknown command
// resolves to an error (not a handled runner, not a legacy fall-through), so
// main() exits before run()/listener construction. This is the process-level
// proof: res.err != nil && !res.handled && res.run == nil.
func TestResolveUnknownCommandDoesNotConstructRunner(t *testing.T) {
	for _, argv := range [][]string{
		{"mecated", "srve"},
		{"mecated", "srever"},
		{"mecated", "acpp"},
	} {
		res := resolveCommand(argv)
		if res.err == nil {
			t.Errorf("%v: expected a fail-closed error, got none", argv)
		}
		if res.handled {
			t.Errorf("%v: unknown command must NOT be handled (no runner)", argv)
		}
		if res.run != nil {
			t.Errorf("%v: unknown command must NOT carry a runner closure", argv)
		}
		if res.mode != modeLegacy {
			t.Errorf("%v: unknown command mode = %q, want legacy/empty (so run() is never reached)", argv, res.mode)
		}
	}
}

// ── Progressive help tests (over the FULL real FlagSet) ──────────────────

// ── Requirement: serve --help is common, grouped, bounded, deterministic ────

func TestServeCommonHelpIsGroupedAndBounded(t *testing.T) {
	out := helpRenderOut(t, modeServe, []string{"--help"})

	// Must contain the renamed group headings (Workspace & Session, not
	// Invocation; no single-entry "Deployment Mode" group).
	for _, grp := range []string{"Workspace & Session:", "Provider:", "Permissions:"} {
		if !strings.Contains(out, grp) {
			t.Errorf("serve common help missing group %q", grp)
		}
	}
	if strings.Contains(out, "Invocation:") {
		t.Errorf("serve common help still uses implementation-vocabulary group 'Invocation':\n%s", out)
	}
	if strings.Contains(out, "Deployment Mode:") {
		t.Errorf("serve common help still has the folded single-entry 'Deployment Mode' group:\n%s", out)
	}

	// Must point to --help-all.
	if !strings.Contains(out, "--help-all") {
		t.Errorf("serve common help missing discovery text pointing to --help-all")
	}

	// Must NOT contain representative expert/advanced flags.
	for _, expert := range []string{
		"llm-breaker-cooldown",
		"otlp-endpoint",
		"driver-tls",
		"scheduler-tick-interval",
	} {
		if strings.Contains(out, expert) {
			t.Errorf("serve common help leaked expert flag %q", expert)
		}
	}
}

// TestServeCommonHelpGroupOrderingIsDeterministic proves flags WITHIN a group
// are sorted by name (deterministic, independent of registration order). It
// renders the full serve common help and checks the Workspace & Session group
// lists its flags alphabetically.
func TestServeCommonHelpGroupOrderingIsDeterministic(t *testing.T) {
	out := helpRenderOut(t, modeServe, []string{"--help"})
	// The Workspace & Session common flags are headless + workspace; sorted
	// alphabetically that is "headless" before "workspace".
	idxGroup := strings.Index(out, "Workspace & Session:")
	if idxGroup < 0 {
		t.Fatal("missing Workspace & Session group")
	}
	rest := out[idxGroup:]
	idxHeadless := strings.Index(rest, "headless")
	idxWorkspace := strings.Index(rest, "workspace")
	if idxHeadless < 0 || idxWorkspace < 0 {
		t.Fatalf("Workspace & Session group missing headless/workspace:\n%s", rest)
	}
	if idxHeadless > idxWorkspace {
		t.Errorf("Workspace & Session flags not sorted alphabetically: headless (idx %d) after workspace (idx %d)", idxHeadless, idxWorkspace)
	}
}

// ── Requirement: ACP common help excludes server-boundary groups ────────────

func TestAcpCommonHelpExcludesServerBoundary(t *testing.T) {
	out := helpRenderOut(t, modeACP, []string{"--help"})

	// Server-boundary flags MUST be absent.
	for _, sb := range []string{
		"grpc-addr",
		"http-addr",
		"tls-cert",
		"tls-key",
		"auth-token",
		"rate-limit",
		"metrics-addr",
		"otlp-endpoint",
		"driver-tls",
		"session-store-url",
		"scheduler-tick-interval",
		"flight-recorder",
		"headless",
	} {
		if strings.Contains(out, sb) {
			t.Errorf("acp common help leaked server-boundary flag %q", sb)
		}
	}

	// ACP-applicable common flags MUST be present.
	for _, acpFlag := range []string{
		"workspace",
		"model",
	} {
		if !strings.Contains(out, acpFlag) {
			t.Errorf("acp common help missing acp-applicable flag %q", acpFlag)
		}
	}
}

// ── Requirement: help-all over the FULL real FlagSet ────────────────────────

// TestServeHelpAllRendersFullRealFlagSet verifies serve --help-all against the
// FULL real parseFlagsMode FlagSet: every public metadata-covered flag MUST
// appear, and the hidden --output-economy MUST NOT appear.
func TestServeHelpAllRendersFullRealFlagSet(t *testing.T) {
	out := helpRenderOut(t, modeServe, []string{"--help-all"})

	if !strings.Contains(out, "Flags:") {
		t.Errorf("serve --help-all missing 'Flags:' header")
	}

	// Every flag with metadata (i.e. every public flag) MUST appear in the
	// exhaustive reference. This is the full-set assertion, not a subset.
	for name := range flagMetaByFlag {
		if !hasFlagHeader(out, name) {
			t.Errorf("serve --help-all missing public flag %q", name)
		}
	}

	// The hidden legacy flag MUST be absent from ALL help.
	if strings.Contains(out, "output-economy") {
		t.Errorf("serve --help-all leaked hidden flag 'output-economy'")
	}
}

// TestAcpHelpAllExcludesServerBoundaryOverFullFlagSet verifies acp --help-all
// against the FULL real FlagSet: server-boundary flags are absent, ACP-
// applicable flags are present, the hidden flag is absent, and --acp itself is
// excluded (the command already selects the mode; --acp=false conflicts).
func TestAcpHelpAllExcludesServerBoundaryOverFullFlagSet(t *testing.T) {
	out := helpRenderOut(t, modeACP, []string{"--help-all"})

	// Server-boundary (acpExclude) flags MUST be absent — checked by header
	// line so a mention inside another flag's description does not false-pass.
	for name, m := range flagMetaByFlag {
		if m.acp != acpExclude {
			continue
		}
		if hasFlagHeader(out, name) {
			t.Errorf("acp --help-all leaked server-boundary flag %q", name)
		}
	}

	// ACP-applicable flags MUST be present (every non-excluded, non-hidden flag).
	for name, m := range flagMetaByFlag {
		if m.acp == acpExclude || name == "acp" {
			continue
		}
		if !hasFlagHeader(out, name) {
			t.Errorf("acp --help-all missing ACP-applicable flag %q", name)
		}
	}

	// --acp MUST be excluded from the canonical acp --help-all reference.
	if hasFlagHeader(out, "acp") {
		t.Errorf("acp --help-all leaked --acp (the command already selects mode; --acp=false conflicts):\n%s", out)
	}

	// The hidden legacy flag MUST be absent.
	if strings.Contains(out, "output-economy") {
		t.Errorf("acp --help-all leaked hidden flag 'output-economy'")
	}
}

// ── Requirement: help paths return success/pre-run (exit 0, no listener) ────

func TestHelpAllReturnsErrHelp(t *testing.T) {
	for _, mode := range []commandMode{modeLegacy, modeServe, modeACP} {
		_, err := parseFlagsMode(mode, []string{"--help-all"})
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%v --help-all: got err=%v, want flag.ErrHelp", mode, err)
		}
	}
}

func TestHelpReturnsErrHelp(t *testing.T) {
	for _, mode := range []commandMode{modeLegacy, modeServe, modeACP} {
		_, err := parseFlagsMode(mode, []string{"--help"})
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%v --help: got err=%v, want flag.ErrHelp", mode, err)
		}
	}
}

// ── Requirement: bare --help-all provides the exhaustive reference ───────────

func TestLegacyHelpAllProvidesExhaustiveReference(t *testing.T) {
	out := helpRenderOut(t, modeLegacy, []string{"--help-all"})

	// The --help-all flag description promises an EXHAUSTIVE reference, so the
	// bare form must provide the serve-compatible flag reference, not only
	// pointers.
	if !strings.Contains(out, "Exhaustive serve-compatible flag reference") {
		t.Errorf("legacy --help-all missing the exhaustive flag-reference header:\n%s", out)
	}
	if !strings.Contains(out, "Flags:") {
		t.Errorf("legacy --help-all missing the 'Flags:' exhaustive listing:\n%s", out)
	}
	// A representative public flag must appear in the exhaustive listing.
	if !strings.Contains(out, "-workspace") {
		t.Errorf("legacy --help-all exhaustive listing missing -workspace:\n%s", out)
	}
	// The brief compatibility note must still point to the ACP-scoped subset.
	if !strings.Contains(out, "mecated acp --help-all") {
		t.Errorf("legacy --help-all missing the ACP compatibility note:\n%s", out)
	}
	// The hidden flag must be absent.
	if strings.Contains(out, "output-economy") {
		t.Errorf("legacy --help-all leaked hidden flag 'output-economy':\n%s", out)
	}
}

// ── Requirement: metadata completeness over the FULL real FlagSet ────────────

// TestFlagMetaCompletenessOverRealFlagSet is the REAL completeness invariant:
// every registered public flag except the explicitly hidden legacy flags has
// metadata, and every metadata key names a real registered flag. It drives the
// validateFlagMeta seam over the FULL real parseFlagsModeOut FlagSet (not a
// synthetic subset), so a flag added/removed from parseFlagsMode without
// updating the metadata fails here. parseFlagsModeOut returns the built FlagSet
// so the test runs validateFlagMeta against the single production registration
// path — no copied registration block.
func TestFlagMetaCompletenessOverRealFlagSet(t *testing.T) {
	var buf strings.Builder
	fs, _, err := parseFlagsModeOut(modeServe, []string{"--help"}, &buf)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlagsModeOut: %v", err)
	}
	if err := validateFlagMeta(fs); err != nil {
		t.Fatalf("validateFlagMeta over the full real FlagSet: %v", err)
	}
}
