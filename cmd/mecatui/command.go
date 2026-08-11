// Command resolution for the mecatui binary: a PURE, argv-accepting seam that
// classifies the leading CLI word into one of {bare/flags (local), connect,
// an error} WITHOUT touching os.Args or any package-level state. main() calls it
// once, threads the result into the existing parse/run path explicitly, and owns
// the one transport-resolution path.
//
// Final grammar (ADR 0089): bare `mecatui [flags]` is the canonical default — it
// ALWAYS hosts an embedded server and NEVER probes loopback. `mecatui connect
// ADDRESS` ALWAYS dials ADDRESS and NEVER probes/embeds; it is the ONLY
// subcommand. There is no compatibility path: `mecatui local` is an unknown
// command (fail-closed, the error names `connect`) and `--server` is an unknown
// flag.
//
// The pure shape (resolveTransportMode + writeTopLevelHelp) is what the tests
// exercise; main wires the io/os.Exit side effects around it. This mirrors the
// repaired cmd/mecated/command.go seam.

package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// transportMode is the canonical transport mode resolved from the leading CLI
// word: "local" is the bare/embedded invocation (the canonical default — always
// embeds, never probes); "connect" always dials an explicit address.
type transportMode string

const (
	modeLocal   transportMode = "local"
	modeConnect transportMode = "connect"
	// modeLogin (issue #265) is the CLI-only `mecatui login` subcommand: it runs
	// the interactive ToolHive LLM OIDC browser flow in-process (no session, no
	// server). It is a peer of connect (a leading command word) but owns no
	// transport — run() branches it BEFORE any TUI/server construction.
	modeLogin transportMode = "login"
)

// transportResolution is the result of classifying argv[1:]. mode + remaining
// (the flag tail) drive run()'s transport path; address is the connect target
// ("" for the bare/local mode). When err is non-nil the leading word was a usage
// error (unknown command, connect missing/flag-first ADDRESS); main prints it
// and exits non-zero WITHOUT resolving a transport.
type transportResolution struct {
	mode      transportMode
	address   string // connect target; "" for the bare/local mode
	remaining []string
	err       error
}

// resolveTransportMode classifies argv (the FULL arg vector, argv[0] included as
// the program name) into a transportResolution. It is PURE: it does not read or
// mutate os.Args, does not call os.Exit, and does not perform I/O or any network
// probe.
//
//   - bare `mecatui [flags]` / leading flag → modeLocal, embed, never probe (the
//     canonical default).
//   - `mecatui connect ADDRESS [flags]` → modeConnect, dial ADDRESS, never probe/embed.
//   - anything else (incl. the retired `local` word) → err (fail closed before
//     transport resolution).
//
// connect REQUIRES an ADDRESS immediately after the command word: a missing
// ADDRESS or a flag-first token (--x) is a usage error. Unknown leading commands
// fail closed.
func resolveTransportMode(argv []string) transportResolution {
	args := argv
	if len(args) < 2 {
		// Bare `mecatui` with no args: the canonical embedded invocation.
		return transportResolution{mode: modeLocal, remaining: args[1:]}
	}
	first := args[1]

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

	// `mecatui login` (issue #265): CLI-only interactive ToolHive LLM OIDC login.
	// No ADDRESS, no transport — run() branches it before any TUI/server. The
	// flag tail (--skip-browser, --help) passes through to parseLoginFlags.
	if first == "login" {
		return transportResolution{mode: modeLogin, remaining: args[2:]}
	}

	// A leading flag (starts with '-') is the bare embedded invocation: `mecatui
	// --workspace …`. Fall through to the embedded transport path.
	if strings.HasPrefix(first, "-") {
		return transportResolution{mode: modeLocal, remaining: args[1:]}
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
// n leading tokens. Used by resolveTransportMode to strip the command word + the
// connect ADDRESS (connect: n=3) so run() receives the flag tail exactly as if
// the leading positional tokens had not been typed.
func stripLeading(argv []string, n int) []string {
	if len(argv) <= n {
		return nil
	}
	return argv[n:]
}

// writeTopLevelHelp renders the concise command-oriented entry page shown by
// `mecatui --help` (and bare `mecatui` when help is requested). It is the
// production renderer used by parseFlags' Usage hook AND by the tests; do not
// duplicate it in a test helper.
func writeTopLevelHelp(out io.Writer) {
	_, _ = fmt.Fprintf(out, "Usage: mecatui <command> [flags]\n\n")
	_, _ = fmt.Fprintf(out, "Bare 'mecatui [flags]' hosts an embedded mecated server in-process (no loopback\n")
	_, _ = fmt.Fprintf(out, "probe) — the canonical default. The subcommands:\n\n")
	_, _ = fmt.Fprintf(out, "Commands:\n")
	_, _ = fmt.Fprintf(out, "  connect ADDRESS   dial a running mecated at ADDRESS (host:port); never probe/embed\n")
	_, _ = fmt.Fprintf(out, "  login             run the interactive ToolHive LLM OIDC browser flow (in-process, no session)\n")
	_, _ = fmt.Fprintf(out, "\nRun 'mecatui --help' for the bare-mode common flags, 'mecatui <command> --help'\n")
	_, _ = fmt.Fprintf(out, "for command-specific flags, and '--help-all' on either for the exhaustive reference.\n")
}

// unknownCommandError builds the error message for an unknown leading bare word.
func unknownCommandError(arg string) error {
	return fmt.Errorf("unknown command %q\n\nAvailable commands:\n  connect ADDRESS   dial a running mecated at ADDRESS\n  login             run the interactive ToolHive LLM OIDC browser flow\n\nBare 'mecatui [flags]' hosts an embedded mecated server in-process (no loopback probe).\nRun 'mecatui --help' or 'mecatui connect --help'", arg)
}

// connectUsageError builds the error message for a bare/flag-first `connect`
// invocation. connect REQUIRES an ADDRESS immediately after the command word.
func connectUsageError(argv []string) error {
	if len(argv) >= 3 && strings.HasPrefix(argv[2], "-") {
		return fmt.Errorf("connect: ADDRESS must immediately follow 'connect' (got flag %q); usage: mecatui connect ADDRESS [flags]", argv[2])
	}
	return errors.New("connect: missing ADDRESS; usage: mecatui connect ADDRESS [flags]")
}
