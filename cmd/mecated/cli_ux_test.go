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

func TestResolveBareNoArgsIsUsageError(t *testing.T) {
	res := resolveCommand([]string{"mecated"})
	if !errors.Is(res.err, errBareInvocation) {
		t.Fatalf("bare err = %v, want errBareInvocation", res.err)
	}
	if res.handled {
		t.Error("bare invocation must not be handled (no runner); it is a usage error")
	}
	if res.mode != "" {
		t.Errorf("bare mode = %q, want the no-mode sentinel", res.mode)
	}
}

func TestResolveLeadingFlagIsUsageError(t *testing.T) {
	res := resolveCommand([]string{"mecated", "--workspace", "/tmp/test"})
	if !errors.Is(res.err, errBareInvocation) {
		t.Fatalf("leading-flag err = %v, want errBareInvocation", res.err)
	}
	if res.handled {
		t.Error("leading-flag invocation must not be handled; it is a usage error")
	}
	if res.mode != "" {
		t.Errorf("leading-flag mode = %q, want the no-mode sentinel", res.mode)
	}
}

func TestResolveAcpFlagIsUnknownFlag(t *testing.T) {
	// The --acp boolean flag is DELETED: `mecated serve --acp` now fails at
	// flag-parse time with the standard unknown-flag error, and the ACP stdio
	// mode is selected ONLY by the `acp` command word.
	_, err := parseFlags([]string{"--acp"})
	if err == nil {
		t.Fatal("parseFlags(--acp) = nil error; want 'flag provided but not defined'")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("parseFlags(--acp) err = %q, want 'flag provided but not defined'", err)
	}
}

func TestOutputEconomyFlagIsUnknownFlag(t *testing.T) {
	// The --output-economy compatibility flag is DELETED (ADR 0089, the clean
	// break superseding ADR 0086's parse-compat shim): it now fails at flag-parse
	// time with the standard unknown-flag error instead of parsing as a no-op.
	_, err := parseFlags([]string{"--output-economy", "terse"})
	if err == nil {
		t.Fatal("parseFlags(--output-economy terse) = nil error; want 'flag provided but not defined'")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("parseFlags(--output-economy terse) err = %q, want 'flag provided but not defined'", err)
	}
}

// --- Requirement 1b: a leading help meta-flag is a HELP intent (exit 0) ------

// TestResolveLeadingHelpRendersTopLevelHelp pins the universal --help contract at
// the top level: `mecated -h` / `mecated --help` resolve to a HANDLED help
// action (not errBareInvocation), render the top-level command page to stdout,
// and return no error — so main() exits 0 and prints no "mecated:" error line.
func TestResolveLeadingHelpRendersTopLevelHelp(t *testing.T) {
	for _, help := range []string{"-h", "--help"} {
		res := resolveCommand([]string{"mecated", help})
		if res.err != nil {
			t.Errorf("%s: err = %v, want nil (help is not a bare-invocation error)", help, res.err)
		}
		if !res.handled || res.run == nil {
			t.Errorf("%s: handled=%v run-nil=%v, want a handled help runner", help, res.handled, res.run == nil)
			continue
		}
		var out strings.Builder
		if err := res.run(strings.NewReader(""), &out, io.Discard); err != nil {
			t.Errorf("%s: help runner returned error: %v", help, err)
		}
		if !strings.Contains(out.String(), "Usage: mecated <command> [flags]") {
			t.Errorf("%s: rendered help missing the top-level usage header:\n%s", help, out.String())
		}
		if !strings.Contains(out.String(), "serve                   start the network daemon") {
			t.Errorf("%s: rendered help missing the serve command entry:\n%s", help, out.String())
		}
		if strings.Contains(out.String(), "Exhaustive") {
			t.Errorf("%s: concise help leaked the exhaustive header:\n%s", help, out.String())
		}
	}
}

// TestResolveLeadingHelpAllRendersExhaustiveReference pins `mecated --help-all` as
// a HANDLED help action rendering the exhaustive top-level reference (reviving
// writeTopLevelHelpAll as production-referenced) over the FULL real FlagSet.
func TestResolveLeadingHelpAllRendersExhaustiveReference(t *testing.T) {
	res := resolveCommand([]string{"mecated", "--help-all"})
	if res.err != nil {
		t.Fatalf("--help-all: err = %v, want nil", res.err)
	}
	if !res.handled || res.run == nil {
		t.Fatal("--help-all: want a handled help runner")
	}
	var out strings.Builder
	if err := res.run(strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("--help-all runner: %v", err)
	}
	s := out.String()
	for _, want := range []string{
		"Usage: mecated <command> [flags]",
		"Exhaustive serve-compatible flag reference",
		"-workspace",
		"mecated acp --help-all",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("--help-all rendered reference missing %q", want)
		}
	}
}

// TestResolveNonHelpLeadingFlagStaysBareInvocation guards the carve-out: any
// OTHER leading flag (e.g. the deleted --acp, or a serve flag at the top level)
// stays the errBareInvocation usage error (main prints error + help, exit 2).
func TestResolveNonHelpLeadingFlagStaysBareInvocation(t *testing.T) {
	for _, tok := range []string{"--acp", "--workspace", "--model"} {
		res := resolveCommand([]string{"mecated", tok, "x"})
		if !errors.Is(res.err, errBareInvocation) {
			t.Errorf("%s: err = %v, want errBareInvocation", tok, res.err)
		}
		if res.handled {
			t.Errorf("%s: must not resolve to a handled help runner", tok)
		}
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
	var buf strings.Builder
	writeTopLevelHelp(&buf)
	out := buf.String()
	for _, want := range []string{
		"Usage: mecated <command> [flags]",
		"serve                   start the network daemon",
		"acp                     serve the Agent Client Protocol over stdio",
		"import                  import a Codex or Claude Code session",
		"config init             write/print the operator settings.yaml skeleton",
		"config validate         validate operator settings.yaml without writing",
		"skills promote          promote a model-authored candidate skill",
		"perf-mcp print-config   print a paste-ready client",
		"mecated <command> --help",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("top-level help (real renderer) missing %q\n--- output ---\n%s", want, out)
		}
	}
	// The Compatibility paragraph (bare/deprecated spellings) is gone: a command
	// word is REQUIRED.
	if strings.Contains(out, "Compatibility") {
		t.Errorf("top-level help still carries the Compatibility paragraph:\n%s", out)
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

func TestResolveImportDispatchesRunner(t *testing.T) {
	res := resolveCommand([]string{"mecated", "import"})
	if res.err != nil {
		t.Fatalf("import resolved error: %v", res.err)
	}
	if !res.handled || res.run == nil {
		t.Fatal("import should be handled with a runner closure")
	}
	if err := res.run(strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("import with no flags should return a usage error from the real runner")
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
		if res.mode != "" {
			t.Errorf("%v: unknown command mode = %q, want the no-mode sentinel (so run() is never reached)", argv, res.mode)
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
		"schedule-store-url",
		"scheduler-tick-interval",
		"flight-recorder",
		"headless",
		"oidc-allow-private-https-issuer",
		"oidc-ca-cert-file",
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
// appear.
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

}

// TestAcpHelpAllExcludesServerBoundaryOverFullFlagSet verifies acp --help-all
// against the FULL real FlagSet: server-boundary flags are absent, ACP-
// applicable flags are present.
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

	// ACP-applicable flags MUST be present (every non-excluded flag).
	for name, m := range flagMetaByFlag {
		if m.acp == acpExclude {
			continue
		}
		if !hasFlagHeader(out, name) {
			t.Errorf("acp --help-all missing ACP-applicable flag %q", name)
		}
	}
}

// ── Requirement: help paths return success/pre-run (exit 0, no listener) ────

func TestHelpAllReturnsErrHelp(t *testing.T) {
	for _, mode := range []commandMode{modeServe, modeACP} {
		_, err := parseFlagsMode(mode, []string{"--help-all"})
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%v --help-all: got err=%v, want flag.ErrHelp", mode, err)
		}
	}
}

func TestHelpReturnsErrHelp(t *testing.T) {
	for _, mode := range []commandMode{modeServe, modeACP} {
		_, err := parseFlagsMode(mode, []string{"--help"})
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%v --help: got err=%v, want flag.ErrHelp", mode, err)
		}
	}
}

// ── Requirement: the top-level --help-all provides the exhaustive reference ──

func TestTopLevelHelpAllProvidesExhaustiveReference(t *testing.T) {
	var buf strings.Builder
	fs, _, err := parseFlagsModeOut(modeServe, []string{"--help"}, &buf)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlagsModeOut: %v", err)
	}
	buf.Reset()
	writeTopLevelHelpAll(&buf, fs)
	out := buf.String()

	// The --help-all flag description promises an EXHAUSTIVE reference, so the
	// top-level form must provide the serve-compatible flag reference, not only
	// pointers.
	if !strings.Contains(out, "Exhaustive serve-compatible flag reference") {
		t.Errorf("top-level --help-all missing the exhaustive flag-reference header:\n%s", out)
	}
	if !strings.Contains(out, "Flags:") {
		t.Errorf("top-level --help-all missing the 'Flags:' exhaustive listing:\n%s", out)
	}
	// A representative public flag must appear in the exhaustive listing.
	if !strings.Contains(out, "-workspace") {
		t.Errorf("top-level --help-all exhaustive listing missing -workspace:\n%s", out)
	}
	// The note must still point to the ACP-scoped subset.
	if !strings.Contains(out, "mecated acp --help-all") {
		t.Errorf("top-level --help-all missing the ACP note:\n%s", out)
	}
	// The compatibility paragraph (deprecated bare spellings) is gone.
	if strings.Contains(out, "deprecated") {
		t.Errorf("top-level --help-all still carries the deprecated-spellings note:\n%s", out)
	}
}

// ── Requirement: metadata completeness over the FULL real FlagSet ────────────

// TestFlagMetaCompletenessOverRealFlagSet is the REAL completeness invariant:
// every registered public flag has metadata, and every metadata key names a
// real registered flag. It drives the
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
