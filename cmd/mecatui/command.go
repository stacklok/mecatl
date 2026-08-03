// Command resolution for the mecatui binary: a PURE, argv-accepting seam that
// classifies the leading CLI word into one of {legacy bare/flags, local, connect,
// an error} WITHOUT touching os.Args or any package-level state. main() calls it
// once, threads the result into the existing parse/run path explicitly, and owns
// the one transport-resolution path.
//
// Phase 1 of the staged transport migration (ADR 0083): the canonical forms
// `mecatui local` and `mecatui connect ADDRESS` are PURE — local ALWAYS embeds and
// NEVER probes loopback; connect ALWAYS dials ADDRESS and NEVER probes/embeds.
// The legacy bare/leading-flag invocation is preserved BYTE-FOR-BYTE (auto-probe
// then embed fallback; `--server ADDRESS` still dials remote) and emits a
// pre-TUI deprecation warning pointing at the canonical form. No behaviour flip
// yet — the legacy path is removed in a later phase.
//
// The pure shape (resolveTransportMode + legacyTransportWarning +
// writeTopLevelHelp) is what the tests exercise; main wires the io/os.Exit side
// effects around it. This mirrors the repaired cmd/mecated/command.go seam.

package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// transportMode is the canonical transport mode resolved from the leading CLI
// word. "" is the legacy bare/flag invocation (auto-probe then embed; --server
// dials remote); "local" always embeds; "connect" always dials an explicit
// address.
type transportMode string

const (
	modeLegacy  transportMode = ""
	modeLocal   transportMode = "local"
	modeConnect transportMode = "connect"
)

// transportResolution is the result of classifying argv[1:]. mode + remaining
// (the flag tail) drive run()'s transport path; address is the connect target
// ("" for legacy/local). When err is non-nil the leading word was a usage error
// (unknown command, connect missing/flag-first ADDRESS); main prints it and
// exits non-zero WITHOUT resolving a transport.
type transportResolution struct {
	mode      transportMode
	address   string // connect target; "" for legacy/local
	remaining []string
	err       error
}

// resolveTransportMode classifies argv (the FULL arg vector, argv[0] included as
// the program name) into a transportResolution. It is PURE: it does not read or
// mutate os.Args, does not call os.Exit, and does not perform I/O or any network
// probe.
//
//   - `mecatui local [flags]`           → modeLocal, embed, never probe.
//   - `mecatui connect ADDRESS [flags]` → modeConnect, dial ADDRESS, never probe/embed.
//   - bare `mecatui [flags]` / leading flag → modeLegacy (byte-for-byte today).
//   - anything else → err (fail closed before transport resolution).
//
// connect REQUIRES an ADDRESS immediately after the command word: a missing
// ADDRESS or a flag-first token (--x) is a usage error. Unknown leading commands
// fail closed.
func resolveTransportMode(argv []string) transportResolution {
	args := argv
	if len(args) < 2 {
		// Bare `mecatui` with no args: legacy invocation.
		return transportResolution{mode: modeLegacy, remaining: args[1:]}
	}
	first := args[1]

	if first == "local" {
		return transportResolution{mode: modeLocal, remaining: stripLeading(args, 2)}
	}
	if first == "connect" {
		// ADDRESS must immediately follow the command word. A missing ADDRESS or a
		// flag-first token (--x) is a usage error — connect NEVER probes/embeds, so
		// there is no fallback target. The ONE exception is the help meta-flags
		// (--help/-h/--help-all): `mecatui connect --help` renders the connect help
		// rather than failing on the missing ADDRESS, matching the universal
		// --help contract.
		if len(args) < 3 {
			return transportResolution{err: connectUsageError(args)}
		}
		if strings.HasPrefix(args[2], "-") && !isHelpMetaFlag(args[2]) {
			return transportResolution{err: connectUsageError(args)}
		}
		if isHelpMetaFlag(args[2]) {
			// Help request: pass the flag tail through to parseTransportFlags so the
			// Usage hook renders the connect help (no ADDRESS required for --help).
			return transportResolution{mode: modeConnect, remaining: args[2:]}
		}
		return transportResolution{
			mode:      modeConnect,
			address:   args[2],
			remaining: stripLeading(args, 3),
		}
	}

	// A leading flag (starts with '-') is a valid legacy invocation: `mecatui
	// --workspace …` / `mecatui --server ADDRESS`. Fall through to the legacy
	// transport path.
	if strings.HasPrefix(first, "-") {
		return transportResolution{mode: modeLegacy, remaining: args[1:]}
	}

	// Anything else is an unknown command — fail closed.
	return transportResolution{err: unknownCommandError(first)}
}

// isHelpMetaFlag reports whether arg is one of the help meta-flags (--help/-h/
// --help-all) that bypass the connect ADDRESS requirement so `mecatui connect
// --help` renders help instead of failing on the missing ADDRESS.
func isHelpMetaFlag(arg string) bool {
	switch arg {
	case "--help", "-h", "--help-all":
		return true
	}
	return false
}

// stripLeading returns argv[n:] (nil when argv is too short), dropping the first
// n leading tokens. Used by resolveTransportMode to strip the command word
// (local: n=2, dropping the program name + "local") and the command word + the
// connect ADDRESS (connect: n=3) so run() receives the flag tail exactly as if
// the leading positional tokens had not been typed.
func stripLeading(argv []string, n int) []string {
	if len(argv) <= n {
		return nil
	}
	return argv[n:]
}

// isLegacyMode reports whether mode is the legacy bare/flag invocation (the path
// that emits a deprecation warning).
func isLegacyMode(mode transportMode) bool {
	return mode == modeLegacy
}

// legacyTransportWarning returns the once-per-startup deprecation warning a
// legacy invocation emits, or "" when mode is canonical (local/connect). The
// warning text distinguishes bare `mecatui` (auto-probe/embed) from
// `mecatui --server ADDRESS` (dial remote) and names the exact canonical
// replacement. It is PURE so tests prove both warn while canonical modes do not;
// main() projects it through the pre-TUI stderr writer.
func legacyTransportWarning(mode transportMode, serverSet bool) string {
	if !isLegacyMode(mode) {
		return ""
	}
	if serverSet {
		return "DEPRECATED: 'mecatui --server ADDRESS' is deprecated; use 'mecatui connect ADDRESS' instead (the --server flag may be removed in a future release)"
	}
	return "DEPRECATED: bare 'mecatui' is deprecated; use 'mecatui local' to host an embedded server (no loopback probe) or 'mecatui connect ADDRESS' to dial a running mecated (the bare invocation's auto-probe-then-embed may be removed in a future release)"
}

// emitLegacyTransportWarning writes the legacy deprecation warning (if any) to w.
// main() calls it with the pre-TUI stderr writer so the warning lands in
// scrollback before the Bubble Tea alt screen takes over.
func emitLegacyTransportWarning(w io.Writer, mode transportMode, serverSet bool) {
	if msg := legacyTransportWarning(mode, serverSet); msg != "" {
		_, _ = fmt.Fprintln(w, msg)
	}
}

// writeTopLevelHelp renders the concise command-oriented entry page shown by
// `mecatui --help` (and bare `mecatui` when help is requested). It is the
// production renderer used by parseFlags' Usage hook AND by the tests; do not
// duplicate it in a test helper.
func writeTopLevelHelp(out io.Writer) {
	_, _ = fmt.Fprintf(out, "Usage: mecatui <command> [flags]\n\n")
	_, _ = fmt.Fprintf(out, "Commands:\n")
	_, _ = fmt.Fprintf(out, "  local             host an embedded mecated server in-process (no loopback probe)\n")
	_, _ = fmt.Fprintf(out, "  connect ADDRESS   dial a running mecated at ADDRESS (host:port); never probe/embed\n")
	_, _ = fmt.Fprintf(out, "\nCompatibility: bare 'mecatui [flags]' still works but is deprecated; the bare\n")
	_, _ = fmt.Fprintf(out, "form auto-probes 127.0.0.1:8080 and falls back to embedding, and 'mecatui --server\n")
	_, _ = fmt.Fprintf(out, "ADDRESS' dials remote. Prefer 'mecatui local' / 'mecatui connect ADDRESS'.\n")
	_, _ = fmt.Fprintf(out, "\nRun 'mecatui <command> --help' for common flags and 'mecatui <command> --help-all'\n")
	_, _ = fmt.Fprintf(out, "for the exhaustive reference.\n")
}

// unknownCommandError builds the error message for an unknown leading bare word.
func unknownCommandError(arg string) error {
	return fmt.Errorf("unknown command %q\n\nAvailable commands:\n  local             host an embedded mecated server in-process\n  connect ADDRESS   dial a running mecated at ADDRESS\n\nRun 'mecatui <command> --help' for command-specific flags", arg)
}

// connectUsageError builds the error message for a bare/flag-first `connect`
// invocation. connect REQUIRES an ADDRESS immediately after the command word.
func connectUsageError(argv []string) error {
	if len(argv) >= 3 && strings.HasPrefix(argv[2], "-") {
		return fmt.Errorf("connect: ADDRESS must immediately follow 'connect' (got flag %q); usage: mecatui connect ADDRESS [flags]", argv[2])
	}
	return errors.New("connect: missing ADDRESS; usage: mecatui connect ADDRESS [flags]")
}
