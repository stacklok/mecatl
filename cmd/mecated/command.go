// Command resolution for the mecated binary: a PURE, argv-accepting seam that
// classifies the leading CLI word into one of {legacy bare/flags, serve, acp, an
// offline subcommand, an error} WITHOUT touching os.Args or any package-level
// state. main() calls it once, threads the result into the existing parse/run
// path explicitly, and owns the one app.Build path.
//
// The pure shape (resolveCommand + applyCommandMode + warnLegacyMode +
// writeTopLevelHelp/writeServeHelp) is what the tests exercise; main wires the
// io/os.Exit side effects around it.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// commandMode is the canonical command mode resolved from the leading CLI word.
// "" is the legacy bare/flag invocation; "serve" is the network daemon;
// "acp" is the ACP stdio mode.
type commandMode string

const (
	modeLegacy commandMode = ""
	modeServe  commandMode = "serve"
	modeACP    commandMode = "acp"
)

// commandResolution is the result of classifying argv[1:]. mode + remaining drive
// the daemon run() path. When handled is true, the invocation was a one-shot
// offline subcommand that fully ran (main exits with run's error, if any) — run
// is the closure executing that subcommand against the supplied streams; it is
// nil for the non-handled daemon path. When err is non-nil the leading word was
// a usage error (unknown command, unknown/missing subcommand); main prints it
// and exits non-zero WITHOUT constructing any listener.
type commandResolution struct {
	mode      commandMode
	remaining []string
	handled   bool
	run       func(stdin io.Reader, stdout, stderr io.Writer) error
	err       error
}

// subcommandAction is the closure type a handled offline subcommand runs as.
type subcommandAction func(stdin io.Reader, stdout, stderr io.Writer) error

// resolveCommand classifies argv (the FULL arg vector, argv[0] included as the
// program name) into a commandResolution. It is PURE: it does not read or mutate
// os.Args, does not call os.Exit, and does not perform I/O. The offline
// subcommand runners are captured as a closure (run) so callers control streams
// and the exit code; a bare word that is neither a known command, a known
// subcommand group, nor a flag yields err (fail closed before daemon startup).
func resolveCommand(argv []string) commandResolution {
	args := argv
	if len(args) < 2 {
		// Bare `mecated` with no args: legacy bare daemon invocation.
		return commandResolution{mode: modeLegacy, remaining: args[1:]}
	}
	first := args[1]

	// Canonical `mecated serve [flags]` — strip the command word and fall through
	// to run()'s normal daemon path.
	if first == "serve" {
		return commandResolution{mode: modeServe, remaining: stripCommandWord(args)}
	}
	// Canonical `mecated acp [flags]` — strip the command word and fall through
	// to run()'s ACP stdio path.
	if first == "acp" {
		return commandResolution{mode: modeACP, remaining: stripCommandWord(args)}
	}

	// `mecated skills promote ...` is the OPERATOR gate. A bare `skills` or an
	// unknown `skills <x>` is a usage error (fail closed), NOT a fall-through to
	// the daemon.
	if first == "skills" {
		if len(args) >= 3 && args[2] == "promote" {
			return commandResolution{
				handled: true,
				run: subcommandAction(func(stdin io.Reader, _, stderr io.Writer) error {
					return runSkillsPromote(args[3:], stdin, stderr)
				}),
			}
		}
		return commandResolution{err: skillsUsageError(args)}
	}

	// `mecated perf-mcp print-config` prints a paste-ready client .mcp.json
	// snippet. A bare `perf-mcp` or an unknown `perf-mcp <x>` is a usage error.
	if first == "perf-mcp" {
		if len(args) >= 3 && args[2] == "print-config" {
			return commandResolution{
				handled: true,
				run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error {
					return runPerfMCPPrintConfig(args[3:], stdout)
				}),
			}
		}
		return commandResolution{err: perfMCPUsageError(args)}
	}

	// `mecated config ...` is the config-management subcommand group. The ONLY
	// subcommand is `config init`. A bare `config` or an unknown `config <x>` is
	// a usage error (a typo starting an unauthenticated server is a nasty
	// surprise): fail closed before the daemon boots.
	if first == "config" {
		if len(args) >= 3 && args[2] == "init" {
			return commandResolution{
				handled: true,
				run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error {
					return runConfigInit(args[3:], stdout)
				}),
			}
		}
		return commandResolution{err: configUsageError(args)}
	}

	// A leading flag (starts with '-') is a valid legacy invocation: `mecated
	// --workspace …` / `mecated --acp`. Fall through to the daemon path.
	if strings.HasPrefix(first, "-") {
		return commandResolution{mode: modeLegacy, remaining: args[1:]}
	}

	// Anything else is an unknown command — fail closed.
	return commandResolution{err: unknownCommandError(first)}
}

// stripCommandWord returns argv with the leading command word (args[1]) removed,
// preserving argv[0]. Used for the serve/acp canonical forms so run() receives
// the flag tail exactly as if the command word had not been typed.
func stripCommandWord(argv []string) []string {
	if len(argv) <= 2 {
		return nil
	}
	return argv[2:]
}

// applyCommandMode resolves the canonical subcommand vs the --acp flag into a
// final acp bool + error. It is PURE: given the parsed config's acp/acpFlagSet
// and the resolved mode, it returns the effective acp value and a non-nil error
// on a conflicting combination (`serve --acp`, `acp --acp=false`). The legacy
// path returns acp unchanged (the --acp flag value stands) so run()'s caller can
// decide whether to warn.
func applyCommandMode(cfg config, mode commandMode) (acp bool, err error) {
	switch mode {
	case modeServe:
		if cfg.acpFlagSet && cfg.acp {
			return false, errors.New(
				"conflicting options: 'mecated serve' and --acp; use 'mecated acp' for the ACP stdio mode")
		}
		return false, nil
	case modeACP:
		if cfg.acpFlagSet && !cfg.acp {
			return false, errors.New(
				"conflicting options: 'mecated acp' and --acp=false; use 'mecated serve' for the network daemon mode")
		}
		return true, nil
	default:
		// Legacy bare invocation: the --acp flag value (if any) stands.
		return cfg.acp, nil
	}
}

// isLegacyMode reports whether mode is the legacy bare/flag invocation (the path
// that emits a deprecation warning).
func isLegacyMode(mode commandMode) bool {
	return mode == modeLegacy
}

// legacyWarning returns the once-per-startup deprecation warning a legacy
// invocation emits, or "" when mode is canonical (serve/acp). The warning text
// distinguishes bare `mecated` from `mecated --acp`. It is PURE so tests prove
// bare and bare-`--acp` warn while canonical serve/acp do not; main() projects
// it through the injected diagnostics/log seam.
func legacyWarning(mode commandMode, acp bool) string {
	if !isLegacyMode(mode) {
		return ""
	}
	if acp {
		return "DEPRECATED: 'mecated --acp' is deprecated; use 'mecated acp' instead (the flag will be removed in a future release)"
	}
	return "DEPRECATED: bare 'mecated' is deprecated; use 'mecated serve' instead (the bare invocation may be removed in a future release)"
}

// emitLegacyWarning writes the legacy deprecation warning (if any) to w. main()
// calls it with its slog-backed writer so the warning flows through the same
// operator-visible log path as the rest of the daemon's startup output, never
// through a package-level slog call in an internal package.
func emitLegacyWarning(w io.Writer, mode commandMode, acp bool) {
	if msg := legacyWarning(mode, acp); msg != "" {
		_, _ = fmt.Fprintln(w, msg)
	}
}

// writeTopLevelHelp renders the concise command-oriented entry page shown by
// bare `mecated --help` (and `mecated` with no args when help is requested). It
// is the production renderer used by parseFlags' Usage hook AND by the tests; do
// not duplicate it in a test helper.
func writeTopLevelHelp(out io.Writer) {
	_, _ = fmt.Fprintf(out, "Usage: mecated <command> [flags]\n\n")
	_, _ = fmt.Fprintf(out, "Commands:\n")
	_, _ = fmt.Fprintf(out, "  serve                   start the network daemon (gRPC + HTTP/SSE)\n")
	_, _ = fmt.Fprintf(out, "  acp                     serve the Agent Client Protocol over stdio\n")
	_, _ = fmt.Fprintf(out, "  config init             write/print the operator settings.yaml skeleton (--print, --force)\n")
	_, _ = fmt.Fprintf(out, "  skills promote          promote a model-authored candidate skill out of quarantine\n")
	_, _ = fmt.Fprintf(out, "  perf-mcp print-config   print a paste-ready client .mcp.json for the perf MCP server\n")
	_, _ = fmt.Fprintf(out, "\nCompatibility: bare 'mecated [flags]' and 'mecated --acp [flags]' still work but\n")
	_, _ = fmt.Fprintf(out, "are deprecated; prefer 'mecated serve' / 'mecated acp'.\n")
	_, _ = fmt.Fprintf(out, "\nRun 'mecated <command> --help' for the full flag list.\n")
}

// writeServeHelp renders the exhaustive flag list shown by `mecated serve --help`
// and `mecated acp --help`. mode names the command word printed in the Usage
// line ("serve" or "acp"). It is the production renderer used by parseFlags'
// Usage hook AND by the tests.
func writeServeHelp(out io.Writer, mode commandMode, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecated %s [flags]\n\nFlags:\n", mode)
	fs.PrintDefaults()
}

// unknownCommandError builds the error message for an unknown leading bare word.
func unknownCommandError(arg string) error {
	return fmt.Errorf("unknown command %q\n\nAvailable commands:\n  serve    start the network daemon (gRPC + HTTP/SSE)\n  acp      serve the Agent Client Protocol over stdio\n  config   configuration management\n  skills   skill management\n  perf-mcp perf MCP utilities\n\nRun 'mecated <command> --help' for command-specific flags", arg)
}

// configUsageError builds the error message for a bare/unknown `config` invocation.
func configUsageError(argv []string) error {
	sub := ""
	if len(argv) >= 3 {
		sub = argv[2]
	}
	if sub == "" {
		return errors.New("config: missing subcommand\navailable subcommands:\n  config init    write/print the operator settings.yaml skeleton (--print, --force)")
	}
	return fmt.Errorf("config: unknown subcommand %q\navailable subcommands:\n  config init    write/print the operator settings.yaml skeleton (--print, --force)", sub)
}

// skillsUsageError builds the error message for a bare/unknown `skills` invocation.
func skillsUsageError(argv []string) error {
	sub := ""
	if len(argv) >= 3 {
		sub = argv[2]
	}
	if sub == "" {
		return errors.New("skills: missing subcommand\navailable subcommands:\n  skills promote    promote a model-authored candidate skill out of quarantine")
	}
	return fmt.Errorf("skills: unknown subcommand %q\navailable subcommands:\n  skills promote    promote a model-authored candidate skill out of quarantine", sub)
}

// perfMCPUsageError builds the error message for a bare/unknown `perf-mcp` invocation.
func perfMCPUsageError(argv []string) error {
	sub := ""
	if len(argv) >= 3 {
		sub = argv[2]
	}
	if sub == "" {
		return errors.New("perf-mcp: missing subcommand\navailable subcommands:\n  perf-mcp print-config    print a paste-ready client .mcp.json for the perf MCP server")
	}
	return fmt.Errorf("perf-mcp: unknown subcommand %q\navailable subcommands:\n  perf-mcp print-config    print a paste-ready client .mcp.json for the perf MCP server", sub)
}
