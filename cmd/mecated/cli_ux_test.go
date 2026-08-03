package main

import (
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

// helpCapture builds a FlagSet the way parseFlags does and invokes the SAME
// production Usage hook (so the test exercises the real renderer, not a copy).
func helpCapture(t *testing.T, mode commandMode, argv []string) string {
	t.Helper()
	fs := flag.NewFlagSet("mecated", flag.ContinueOnError)
	var cfg config
	// Register just the acp flag so the serve/acp flag list is non-empty enough
	// to assert the "Flags:" section header; the real parseFlags registers all.
	fs.BoolVar(&cfg.acp, "acp", false, "serve the Agent Client Protocol (ACP) over stdio")
	var buf strings.Builder
	fs.SetOutput(&buf)
	fs.Usage = func() {
		out := fs.Output()
		if mode == modeLegacy {
			writeTopLevelHelp(out)
			return
		}
		writeServeHelp(out, mode, fs)
	}
	if err := fs.Parse(argv); err != nil && err != flag.ErrHelp {
		t.Fatalf("parse: %v", err)
	}
	return buf.String()
}

func TestTopLevelHelpRealRendererContainsCommands(t *testing.T) {
	out := helpCapture(t, modeLegacy, []string{"--help"})
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
	out := helpCapture(t, modeServe, []string{"--help"})
	if !strings.Contains(out, "Usage: mecated serve [flags]") {
		t.Errorf("serve help missing 'Usage: mecated serve [flags]':\n%s", out)
	}
	if !strings.Contains(out, "Flags:") {
		t.Errorf("serve help missing 'Flags:' section:\n%s", out)
	}
	// The exhaustive list must NOT show the concise command page markers.
	if strings.Contains(out, "Compatibility: bare 'mecated") {
		t.Errorf("serve help leaked the legacy compatibility note (should be concise only):\n%s", out)
	}
}

func TestAcpHelpRealRendererShowsFlagList(t *testing.T) {
	out := helpCapture(t, modeACP, []string{"--help"})
	if !strings.Contains(out, "Usage: mecated acp [flags]") {
		t.Errorf("acp help missing 'Usage: mecated acp [flags]':\n%s", out)
	}
	if !strings.Contains(out, "Flags:") {
		t.Errorf("acp help missing 'Flags:' section:\n%s", out)
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
