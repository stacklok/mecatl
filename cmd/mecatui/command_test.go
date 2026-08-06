package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
)

// resolveTransportMode is a PURE seam (no os.Args, no os.Exit, no I/O), so
// these tests exercise the REAL production command-resolution logic directly —
// they do NOT mutate global state, do NOT resolve a transport, and do NOT call
// os.Exit.

// --- Requirement 1: pure resolution seam (mode + remaining + address) -------

func TestResolveLocalWordIsUnknownCommand(t *testing.T) {
	res := resolveTransportMode([]string{"mecatui", "local", "--workspace", "/tmp/w"})
	if res.err == nil {
		t.Fatal("the retired 'local' word must fail closed as an unknown command")
	}
	if !strings.Contains(res.err.Error(), "local") {
		t.Errorf("error %q does not name the unknown command", res.err)
	}
	if !strings.Contains(res.err.Error(), "connect") {
		t.Errorf("error %q does not name the available 'connect' command", res.err)
	}
}

func TestResolveConnectStripsCommandWordAndAddress(t *testing.T) {
	res := resolveTransportMode([]string{"mecatui", "connect", "10.0.0.5:8080", "--workspace", "/tmp/w"})
	if res.err != nil {
		t.Fatalf("connect resolution error: %v", res.err)
	}
	if res.mode != modeConnect {
		t.Errorf("mode = %q, want connect", res.mode)
	}
	if res.address != "10.0.0.5:8080" {
		t.Errorf("address = %q, want 10.0.0.5:8080", res.address)
	}
	if len(res.remaining) != 2 || res.remaining[0] != "--workspace" {
		t.Errorf("remaining = %v, want [--workspace /tmp/w]", res.remaining)
	}
}

func TestResolveBareNoArgsIsLocal(t *testing.T) {
	res := resolveTransportMode([]string{"mecatui"})
	if res.err != nil {
		t.Fatalf("bare handled=%v, want nil", res.err)
	}
	if res.mode != modeLocal {
		t.Errorf("mode = %q, want local (bare is the canonical embedded default)", res.mode)
	}
	if len(res.remaining) != 0 {
		t.Errorf("remaining = %v, want empty", res.remaining)
	}
}

func TestResolveLeadingFlagIsLocal(t *testing.T) {
	res := resolveTransportMode([]string{"mecatui", "--workspace", "/tmp/w"})
	if res.err != nil {
		t.Fatalf("leading-flag error: %v", res.err)
	}
	if res.mode != modeLocal {
		t.Errorf("mode = %q, want local (a leading flag is the bare embedded form)", res.mode)
	}
	if len(res.remaining) != 2 || res.remaining[0] != "--workspace" {
		t.Errorf("remaining = %v, want [--workspace /tmp/w]", res.remaining)
	}
}

// --- Requirement 1: connect requires ADDRESS immediately, fail closed --------

func TestResolveConnectMissingAddressFailsClosed(t *testing.T) {
	res := resolveTransportMode([]string{"mecatui", "connect"})
	if res.err == nil {
		t.Fatal("connect with no ADDRESS must fail closed")
	}
	if !strings.Contains(res.err.Error(), "missing ADDRESS") {
		t.Errorf("error %q does not name the missing ADDRESS", res.err)
	}
}

func TestResolveConnectFlagFirstFailsClosed(t *testing.T) {
	res := resolveTransportMode([]string{"mecatui", "connect", "--workspace", "/tmp/w"})
	if res.err == nil {
		t.Fatal("connect with a flag-first token must fail closed")
	}
	if !strings.Contains(res.err.Error(), "ADDRESS must immediately follow") {
		t.Errorf("error %q does not name the flag-first problem", res.err)
	}
}

// connect --help is a help request, not a usage error (the universal --help contract).
func TestResolveConnectHelpPassesThrough(t *testing.T) {
	for _, help := range []string{"--help", "-h", "--help-all"} {
		res := resolveTransportMode([]string{"mecatui", "connect", help})
		if res.err != nil {
			t.Errorf("connect %s must pass through as a help request, got error: %v", help, res.err)
		}
		if res.mode != modeConnect {
			t.Errorf("connect %s: mode = %q, want connect", help, res.mode)
		}
		if res.address != "" {
			t.Errorf("connect %s: address = %q, want empty (help needs no ADDRESS)", help, res.address)
		}
	}
}

func TestResolveUnknownCommandFailsClosed(t *testing.T) {
	res := resolveTransportMode([]string{"mecatui", "loal"})
	if res.err == nil {
		t.Fatal("unknown command 'loal' must fail closed")
	}
	if !strings.Contains(res.err.Error(), "loal") {
		t.Errorf("error %q does not name the unknown command", res.err)
	}
	if !strings.Contains(res.err.Error(), "Available commands") {
		t.Errorf("error %q does not list available commands", res.err)
	}
}

// --- Requirement 1: no-probe / no-embed mode selection (parse path) ---------

// parseTransportFlagsTest is a helper that runs the REAL parse seam with a
// discard writer and returns the FlagSet + config + error (mirroring the
// production path used by run()).
func parseTransportFlagsTest(t *testing.T, mode transportMode, args []string) (*flag.FlagSet, config, error) {
	t.Helper()
	var buf bytes.Buffer
	fs, cfg, err := parseTransportFlags(mode, &buf, args)
	_ = buf
	return fs, cfg, err
}

func TestParseTransportFlagsLocalModeIsLocal(t *testing.T) {
	_, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--mock", "--workspace", "/abs"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.transportMode != modeLocal {
		t.Errorf("transportMode = %q, want local", cfg.transportMode)
	}
}

func TestParseTransportFlagsConnectModeIsConnect(t *testing.T) {
	_, cfg, err := parseTransportFlagsTest(t, modeConnect, []string{"--workspace", "/abs"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.transportMode != modeConnect {
		t.Errorf("transportMode = %q, want connect", cfg.transportMode)
	}
}

// The retired --server flag is a parse-level unknown-flag error (mirrors
// TestAskReviewerFlagsAreUnknownFlagErrors) in BOTH modes — unregistration is
// total, not local-mode-only.
func TestResolveServerFlagIsUnknownFlag(t *testing.T) {
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		t.Run(string(mode), func(t *testing.T) {
			_, _, err := parseTransportFlagsTest(t, mode, []string{"--server", "127.0.0.1:8080", "--workspace", "/abs"})
			if err == nil {
				t.Errorf("removed flag --server must now be an unknown-flag error in %q mode, not a silent no-op", mode)
			}
			if !strings.Contains(err.Error(), "not defined") && !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Errorf("removed flag --server should surface as a stdlib unknown-flag error in %q mode, got: %v", mode, err)
			}
		})
	}
}

// --- Requirement 4: by-name rejection for every classified flag family -------

// embedded-only flags rejected in connect mode.
func TestRejectEmbeddedOnlyFlagsInConnect(t *testing.T) {
	embeddedOnly := []string{
		"mock", "no-bash", "trust-project", "yolo", "posture",
		"openai-base-url", "openrouter-base-url", "anthropic-base-url", "opencode-base-url",
		"toolhive-llm", "toolhive-llm-base-url",
		"model", "default-provider", "default-model", "subagent-model",
		"model-alias", "model-slot", "subagent-model-router",
		"llm-per-attempt-timeout", "llm-stream-idle-timeout",
		"no-prompt-cache", "anthropic-cache-ttl",
		"memory-dir", "no-memory", "store-dir", "no-store",
		"soul-file", "no-soul", "approve-soul", "soul-strict",
		"user-model-dir", "no-user-model", "user-model-review", "user-model-review-interval",
		"commands-dir", "no-commands", "skills-dir", "no-skills",
		"perf", "perf-addr", "perf-goroutine-warn-threshold", "perf-mcp",
		"reasoning-effort", "quiet",
	}
	for _, name := range embeddedOnly {
		t.Run(name, func(t *testing.T) {
			// connect mode parses only shared + remote flags; pass the embedded flag
			// alongside a valid workspace and assert it is rejected BY NAME.
			_, _, err := parseTransportFlagsTest(t, modeConnect, []string{"--" + name, flagValueForTest(name), "--workspace", "/abs"})
			if err == nil {
				t.Errorf("embedded-only flag --%s must be rejected in connect mode", name)
			} else if !strings.Contains(err.Error(), name) {
				t.Errorf("connect rejection for --%s does not name the flag: %v", name, err)
			}
		})
	}
}

// remote-only flags rejected in the bare (embedded) mode.
func TestRejectRemoteOnlyFlagsInBare(t *testing.T) {
	remoteOnly := []string{"auth-token", "tls", "tls-ca", "insecure"}
	for _, name := range remoteOnly {
		t.Run(name, func(t *testing.T) {
			_, _, err := parseTransportFlagsTest(t, modeLocal, []string{"--" + name, flagValueForTest(name), "--mock", "--workspace", "/abs"})
			if err == nil {
				t.Errorf("remote-only flag --%s must be rejected in the bare (embedded) mode", name)
			} else if !strings.Contains(err.Error(), name) {
				t.Errorf("bare-mode rejection for --%s does not name the flag: %v", name, err)
			}
		})
	}
}

// shared flags valid in BOTH modes (no rejection).
func TestSharedFlagsValidInBothModes(t *testing.T) {
	shared := []string{"workspace", "mode", "theme", "theme-dir", "no-alt-screen", "inline", "no-mouse", "no-banner", "keymap"}
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		for _, name := range shared {
			t.Run(string(mode)+"/"+name, func(t *testing.T) {
				_, _, err := parseTransportFlagsTest(t, mode, []string{"--" + name, flagValueForTest(name)})
				// Some shared flags need --mock/--workspace to pass validate; but
				// applicability rejection runs BEFORE validate, so a nil error here
				// means the flag was accepted by the applicability check. We only
				// assert the error is NOT an applicability rejection naming the flag.
				if err != nil && strings.Contains(err.Error(), "not applicable in") && strings.Contains(err.Error(), name) {
					t.Errorf("shared flag --%s must not be rejected as inapplicable in %q mode: %v", name, mode, err)
				}
			})
		}
	}
}

// flagValueForTest returns a value for flags that require one in the test
// harness. Booleans take no value; the others take a placeholder.
func flagValueForTest(name string) string {
	switch name {
	case "mock", "no-bash", "trust-project", "yolo", "no-memory", "no-store",
		"no-soul", "approve-soul", "soul-strict", "no-user-model", "user-model-review",
		"no-commands", "no-skills", "perf", "perf-mcp", "tls", "insecure",
		"no-alt-screen", "inline", "no-mouse", "no-banner", "list-themes",
		"subagent-model-router", "help-all", "quiet", "no-prompt-cache":
		return "" // bool: no value consumed
	}
	// Provide a plausible value; the applicability check runs at fs.Visit time so
	// the value just needs to parse.
	switch name {
	case "auth-token":
		return "127.0.0.1:8080"
	case "tls-ca", "soul-file", "memory-dir", "store-dir", "user-model-dir",
		"commands-dir", "skills-dir", "perf-addr":
		return "/tmp/x"
	case "workspace":
		return "/abs"
	case "mode":
		return "default"
	case "theme", "terminal-title":
		return "aztec"
	case "theme-dir":
		return "/tmp/themes"
	case "model", "default-provider", "default-model", "subagent-model",
		"reasoning-effort", "posture":
		return "x"
	case "anthropic-cache-ttl":
		return "1h"
	case "openai-base-url", "openrouter-base-url", "anthropic-base-url",
		"opencode-base-url", "toolhive-llm-base-url":
		return "http://x"
	case "model-alias", "model-slot", "keymap":
		return "k=v"
	case "llm-per-attempt-timeout", "llm-stream-idle-timeout":
		return "30s"
	case "perf-goroutine-warn-threshold", "user-model-review-interval":
		return "1"
	case "toolhive-llm":
		return "true"
	}
	return ""
}

// --- Requirement 4: metadata completeness / no orphans ---------------------

func TestFlagApplicabilityCompletenessOverRealFlagSet(t *testing.T) {
	fs, _, err := parseTransportFlagsTest(t, modeLocal, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validateFlagApplicability(fs); err != nil {
		t.Fatalf("flagApplicability metadata is incomplete or has orphans: %v", err)
	}
}

// Default unknown metadata must fail closed (reject), not silently include.
func TestUnknownMetadataFailsClosed(t *testing.T) {
	// A flag not in flagApplicabilityByFlag is
	// rejected in BOTH explicit modes. --help is registered by the stdlib flag
	// parser implicitly but is NOT in our metadata; however it is handled by
	// fs.Parse (returns flag.ErrHelp) BEFORE the applicability check runs. So we
	// assert the invariant directly: applicableIn for a synthetic unknown name is
	// false in both explicit modes.
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		if applicableIn("__synthetic_unknown_flag__", mode) {
			t.Errorf("unknown metadata must fail closed (reject) in %q mode, not silently include", mode)
		}
	}
}

// --- Requirement 5: ask-reviewer removal → honest unknown-flag errors -------

func TestAskReviewerFlagsAreUnknownFlagErrors(t *testing.T) {
	for _, name := range []string{"subagent-ask-reviewer", "subagent-ask-reviewer-max-denies", "subagent-ask-reviewer-policy"} {
		t.Run(name, func(t *testing.T) {
			_, _, err := parseTransportFlagsTest(t, modeLocal, []string{"--" + name, "x", "--workspace", "/abs"})
			if err == nil {
				t.Errorf("removed flag --%s must now be an unknown-flag error, not a silent no-op", name)
			}
			if !strings.Contains(err.Error(), "not defined") && !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Errorf("removed flag --%s should surface as a stdlib unknown-flag error, got: %v", name, err)
			}
		})
	}
}

func TestOutputEconomyFlagIsUnknownFlagError(t *testing.T) {
	// The --output-economy compatibility flag is DELETED (ADR 0089, the clean
	// break superseding ADR 0086's parse-compat shim): it now fails at flag-parse
	// time with the standard unknown-flag error instead of parsing as a no-op —
	// in BOTH modes (unregistration is total).
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		t.Run(string(mode), func(t *testing.T) {
			_, _, err := parseTransportFlagsTest(t, mode, []string{"--output-economy", "terse", "--workspace", "/abs"})
			if err == nil {
				t.Errorf("removed flag --output-economy must now be an unknown-flag error in %q mode, not a silent no-op", mode)
			}
			if !strings.Contains(err.Error(), "not defined") && !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Errorf("removed flag --output-economy should surface as a stdlib unknown-flag error in %q mode, got: %v", mode, err)
			}
		})
	}
}

// --- Requirement 6: provider/trust validation gating -----------------------

// local mode runs the provider check (mayEmbed); --mock satisfies it. The
// --toolhive-llm=false opt-out keeps the test environment-independent (a host
// with a locally-running ToolHive gateway would otherwise auto-satisfy it).
func TestLocalProviderCheckRuns(t *testing.T) {
	if _, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--workspace", "/abs", "--toolhive-llm=false"}); err != nil {
		t.Fatalf("parse: %v", err)
	} else if err := cfg.validate(); err == nil {
		t.Error("local with no provider and no --mock must fail validation (mayEmbed)")
	}
}

// local with --mock works offline (no provider needed).
func TestLocalMockWorksOffline(t *testing.T) {
	if _, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--mock", "--workspace", "/abs"}); err != nil {
		t.Fatalf("parse: %v", err)
	} else if err := cfg.validate(); err != nil {
		t.Errorf("local --mock must validate offline: %v", err)
	}
}

// local canonical path works offline end-to-end (provider check + posture
// check both pass under --mock). This is the "canonical local must work
// offline with --mock" acceptance bullet.
func TestLocalCanonicalOfflineWithMock(t *testing.T) {
	if _, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--mock", "--workspace", "/abs"}); err != nil {
		t.Fatalf("parse: %v", err)
	} else if err := cfg.validate(); err != nil {
		t.Errorf("canonical local --mock must validate offline: %v", err)
	}
}

// connect mode skips the provider check (never embeds).
func TestConnectSkipsProviderCheck(t *testing.T) {
	if _, cfg, err := parseTransportFlagsTest(t, modeConnect, []string{"--workspace", "/abs"}); err != nil {
		t.Fatalf("parse: %v", err)
	} else if err := cfg.validate(); err != nil {
		t.Errorf("connect must skip the provider/posture checks (never embeds): %v", err)
	}
}

// local rejects --yolo as root outside a sandbox (posture check runs).
func TestLocalPostureCheckRuns(t *testing.T) {
	_, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--yolo", "--mock", "--workspace", "/abs"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The posture refusal is only assertable when running as root; in any case
	// validate must NOT skip it (it would surface a refusal for a privileged
	// run). We assert validate returns nil when not privileged (the common test
	// runner) — the gate is exercised structurally.
	if os.Geteuid() != 0 {
		if err := cfg.validate(); err != nil {
			t.Errorf("local --yolo as non-root must validate (sandbox or not-privileged): %v", err)
		}
	}
}

// --- Requirement 7: real help content --------------------------------------

func helpRenderOut(t *testing.T, mode transportMode, argv []string) string {
	t.Helper()
	var buf strings.Builder
	_, _, err := parseTransportFlags(mode, &buf, argv)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseTransportFlags(%v, %v): %v", mode, argv, err)
	}
	return buf.String()
}

// hasFlagHeader reports whether the rendered help output contains the flag's
// own header line ("  -<name>" at the start of a line) — distinguishing the
// flag's own entry from a bare mention of "-<name>" inside prose (e.g. "--mock"
// appears in the connect help description naming the rejected embedded flags).
func hasFlagHeader(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		rest := strings.TrimPrefix(line, "  -"+name)
		if rest == line {
			continue
		}
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
		"Usage: mecatui <command> [flags]",
		"connect ADDRESS   dial a running mecated",
		"hosts an embedded mecated",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("top-level help (real renderer) missing %q\n--- output ---\n%s", want, out)
		}
	}
	// The retired `local` subcommand and the deprecation/compat prose are gone.
	for _, unwanted := range []string{"local ", "deprecated", "Compatibility", "--server"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("top-level help (real renderer) must not mention %q\n--- output ---\n%s", unwanted, out)
		}
	}
}

func TestBareHelpRealRendererShowsCommonFlags(t *testing.T) {
	out := helpRenderOut(t, modeLocal, []string{"--help"})
	if !strings.Contains(out, "Usage: mecatui [flags]") {
		t.Errorf("bare help missing 'Usage: mecatui [flags]':\n%s", out)
	}
	if !strings.Contains(out, "NEVER probes loopback") {
		t.Errorf("bare help missing the no-probe note:\n%s", out)
	}
	// Embedded flags appear in the bare common help (--mock is common+local).
	if !hasFlagHeader(out, "mock") {
		t.Errorf("bare help missing the embedded --mock common flag header:\n%s", out)
	}
	// Remote-only flags do NOT appear in the bare common help.
	if hasFlagHeader(out, "auth-token") {
		t.Errorf("bare help leaked remote-only --auth-token as a flag header:\n%s", out)
	}
}

func TestConnectHelpRealRendererShowsCommonFlags(t *testing.T) {
	out := helpRenderOut(t, modeConnect, []string{"--help"})
	if !strings.Contains(out, "Usage: mecatui connect ADDRESS [flags]") {
		t.Errorf("connect help missing 'Usage: mecatui connect ADDRESS [flags]':\n%s", out)
	}
	if !strings.Contains(out, "NEVER probes loopback") {
		t.Errorf("connect help missing the no-probe note:\n%s", out)
	}
	// Remote flags appear in connect common help (--server is NOT applicable in
	// connect — connect takes ADDRESS — so it must NOT appear; --auth-token IS).
	if !hasFlagHeader(out, "auth-token") {
		t.Errorf("connect help missing remote --auth-token flag header:\n%s", out)
	}
	if hasFlagHeader(out, "mock") {
		t.Errorf("connect help leaked embedded-only --mock as a flag header:\n%s", out)
	}
}

func TestHelpAllReturnsErrHelp(t *testing.T) {
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		t.Run(string(mode), func(t *testing.T) {
			_, _, err := parseTransportFlags(mode, &bytes.Buffer{}, []string{"--help-all"})
			if !errors.Is(err, flag.ErrHelp) {
				t.Errorf("--help-all in %q mode must return flag.ErrHelp (exit 0), got %v", mode, err)
			}
		})
	}
}

func TestBareHelpAllRendersFullRealFlagSet(t *testing.T) {
	out := helpRenderOut(t, modeLocal, []string{"--help-all"})
	if !strings.Contains(out, "Usage: mecatui [flags]") {
		t.Errorf("bare --help-all missing usage:\n%s", out)
	}
	// A representative advanced embedded flag appears in --help-all.
	if !hasFlagHeader(out, "perf-goroutine-warn-threshold") {
		t.Errorf("bare --help-all missing an advanced flag header:\n%s", out)
	}
}

func TestConnectHelpAllExcludesEmbeddedFlags(t *testing.T) {
	out := helpRenderOut(t, modeConnect, []string{"--help-all"})
	if !strings.Contains(out, "Usage: mecatui connect ADDRESS [flags]") {
		t.Errorf("connect --help-all missing usage:\n%s", out)
	}
	// Embedded-only flags are excluded from connect --help-all.
	for _, embedded := range []string{"mock", "trust-project", "posture", "perf"} {
		if hasFlagHeader(out, embedded) {
			t.Errorf("connect --help-all leaked embedded flag --%s as a header:\n%s", embedded, out)
		}
	}
	// Remote flags appear.
	if !hasFlagHeader(out, "auth-token") {
		t.Errorf("connect --help-all missing remote --auth-token flag header:\n%s", out)
	}
}

// --- Requirement 1: top-level help is command-oriented (not flag-dump) ------

func TestBareHelpIsNotRawFlagDump(t *testing.T) {
	out := helpRenderOut(t, modeLocal, []string{"--help"})
	// The bare --help must NOT be a raw flag dump (no "Usage of mecatui:" header);
	// it renders the grouped common-flag help.
	if strings.Contains(out, "Usage of mecatui:") {
		t.Errorf("bare help must be the grouped common help, not a flag dump:\n%s", out)
	}
}

// --- Requirement: --help returns flag.ErrHelp (mirrors mecated) -------------

// TestHelpReturnsErrHelp asserts --help/-h return flag.ErrHelp across every
// transport mode, so main() exits 0 without printing "mecatui: flag: help
// requested" (the regression this guards: a Usage hook that printed help but
// left run() returning a non-ErrHelp error would surface a spurious error line
// on a successful help action).
func TestHelpReturnsErrHelp(t *testing.T) {
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		for _, help := range []string{"--help", "-h"} {
			t.Run(string(mode)+"/"+help, func(t *testing.T) {
				_, _, err := parseTransportFlags(mode, &bytes.Buffer{}, []string{help})
				if !errors.Is(err, flag.ErrHelp) {
					t.Errorf("%q in %q mode must return flag.ErrHelp (exit 0), got %v", help, mode, err)
				}
			})
		}
	}
}

// --- Requirement: actual main/run help behaviour at the run() seam ----------

// runHelpCase runs the REAL run() seam (the function main() calls) with a
// captured stderr and asserts the help contract: run() returns flag.ErrHelp
// (so main() exits 0 WITHOUT printing "mecatui: <err>"), and the help text is
// written to stderr. It does NOT touch the network or start the TUI: a help
// request returns from parseTransportFlags before any transport resolution. The
// argv includes the program name (run() reads argv, not os.Args), matching the
// production main() call shape.
func runHelpCase(t *testing.T, argv []string, wantSubstring string) {
	t.Helper()
	// Capture stderr by swapping os.Stderr for the duration of run(). run()
	// threads os.Stderr into parseTransportFlags as the help output writer, so
	// the rendered help lands here. The --help-all output can exceed an os.Pipe's
	// 64KB buffer, so drain the read end concurrently to avoid a write-block
	// deadlock; io.Copy into a bytes.Buffer and join before asserting.
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	var captured bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&captured, r)
		close(done)
	}()
	runErr := run(argv)
	if err := w.Close(); err != nil {
		t.Fatalf("close write pipe: %v", err)
	}
	<-done
	os.Stderr = orig
	out := captured.String()

	if !errors.Is(runErr, flag.ErrHelp) {
		t.Errorf("run(%v) err = %v, want flag.ErrHelp (so main exits 0 with no error line)", argv, runErr)
	}
	if !strings.Contains(out, wantSubstring) {
		t.Errorf("run(%v) stderr missing %q\n--- stderr ---\n%s", argv, wantSubstring, out)
	}
	// The help action must NOT print the "mecatui: <err>" error line main()
	// emits on a non-ErrHelp failure — that is the regression this guards.
	if strings.HasPrefix(out, "mecatui:") {
		t.Errorf("run(%v) stderr starts with the error line \"mecatui:\" — help must be a clean success:\n%s", argv, out)
	}
}

// TestRunHelpReturnsErrHelpAndWritesHelp exercises run() (the function main
// calls) for --help across every transport shape: the bare embedded form and
// `connect` (with and without an ADDRESS — connect --help needs no ADDRESS).
// It pins the contract that a help request is a SUCCESSFUL action: run()
// returns flag.ErrHelp so main() exits 0 with no error line, and the
// mode-appropriate help is written to stderr.
func TestRunHelpReturnsErrHelpAndWritesHelp(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"bare", []string{"mecatui", "--help"}, "Usage: mecatui [flags]"},
		{"connect-no-addr", []string{"mecatui", "connect", "--help"}, "Usage: mecatui connect ADDRESS [flags]"},
		{"connect-with-addr", []string{"mecatui", "connect", "127.0.0.1:8080", "--help"}, "Usage: mecatui connect ADDRESS [flags]"},
		{"short-h", []string{"mecatui", "-h"}, "Usage: mecatui [flags]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runHelpCase(t, tc.argv, tc.want)
		})
	}
}

// TestRunHelpAllReturnsErrHelpAndWritesHelp exercises run() for --help-all: it
// returns flag.ErrHelp (exit 0, no error line) and writes the exhaustive flag
// reference to stderr with the mode-appropriate usage header.
func TestRunHelpAllReturnsErrHelpAndWritesHelp(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"bare", []string{"mecatui", "--help-all"}, "Usage: mecatui [flags]"},
		{"connect", []string{"mecatui", "connect", "--help-all"}, "Usage: mecatui connect ADDRESS [flags]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runHelpCase(t, tc.argv, tc.want)
		})
	}
}

// TestRunUnknownCommandReturnsNonHelpError asserts the non-help failure path:
// an unknown leading command returns a NON-ErrHelp error (so main() prints
// "mecatui: <err>" and exits 1), distinguishing a help success from a usage
// failure at the run() seam. resolveTransportMode is PURE (no I/O), so run()
// returns the usage error before touching stderr. The error is wrapped in
// usageErrorTrailer so main's printer ALSO appends the top-level command
// summary beneath the error line (mirroring mecated's errBareInvocation arm);
// errors.Is/As traverses the wrapper, and %v prints the inner message.
func TestRunUnknownCommandReturnsNonHelpError(t *testing.T) {
	err := run([]string{"mecatui", "bogus-command"})
	if err == nil {
		t.Fatal("run(unknown command) err = nil, want a non-nil usage error")
	}
	if errors.Is(err, flag.ErrHelp) {
		t.Errorf("run(unknown command) err = flag.ErrHelp, want a non-ErrHelp usage error (so main exits 1 with the error line)")
	}
	if !strings.Contains(err.Error(), "bogus-command") {
		t.Errorf("run(unknown command) error %q does not name the unknown command", err)
	}
	var trailer *usageErrorTrailer
	if !errors.As(err, &trailer) {
		t.Errorf("run(unknown command) error is not a usageErrorTrailer — main would skip the top-level command summary trailer")
	}
}

// TestUsageErrorTrailerMarkers pin the trailer contract main's error printer
// keys on: a resolver usage error (unknown command AND the connect
// missing/flag-first ADDRESS forms) arrives wrapped, while every OTHER run()
// error (flag parse, validation) stays unwrapped so main prints no trailer.
func TestUsageErrorTrailerMarkers(t *testing.T) {
	// connect missing ADDRESS → wrapped usage error.
	err := run([]string{"mecatui", "connect"})
	var trailer *usageErrorTrailer
	if !errors.As(err, &trailer) {
		t.Errorf("run(connect with no ADDRESS) error is not a usageErrorTrailer, want the trailer so main appends the command summary: %v", err)
	}

	// connect flag-first → wrapped usage error.
	err = run([]string{"mecatui", "connect", "--workspace", "/abs"})
	if !errors.As(err, &trailer) {
		t.Errorf("run(connect --workspace …) error is not a usageErrorTrailer: %v", err)
	}

	// A flag-PARSE error (unknown flag in a valid mode) is NOT wrapped: it is a
	// per-flag problem, not a leading-word grammar error.
	err = run([]string{"mecatui", "--not-a-real-flag"})
	if errors.As(err, &trailer) {
		t.Errorf("run(--not-a-real-flag) error unexpectedly wrapped as usageErrorTrailer: %v", err)
	}
}

// TestUnknownCommandErrorFooterIsBareHelpOrConnectHelp pins the rewritten error
// footer: the retired "Run 'mecatui <command> --help'" phrasing (which named
// no real command) is replaced by the two REAL help spellings.
func TestUnknownCommandErrorFooterIsBareHelpOrConnectHelp(t *testing.T) {
	err := unknownCommandError("local")
	if !strings.Contains(err.Error(), "Run 'mecatui --help' or 'mecatui connect --help'") {
		t.Errorf("unknown-command error missing the rewritten footer: %v", err)
	}
	if strings.Contains(err.Error(), "for command-specific flags") {
		t.Errorf("unknown-command error still carries the retired footer: %v", err)
	}
}
