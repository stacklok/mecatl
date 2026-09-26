// Command resolution for the mecated binary: a PURE, argv-accepting seam that
// classifies the leading CLI word into one of {serve, acp, an offline
// subcommand, an error} WITHOUT touching os.Args or any package-level state.
// main() calls it once, threads the result into the existing parse/run path
// explicitly, and owns the one app.Build path.
//
// The pure shape (resolveCommand + writeTopLevelHelp) is what the tests
// exercise; main wires the io/os.Exit side effects around it.

package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// commandMode is the canonical command mode resolved from the leading CLI word.
// "serve" is the network daemon; "acp" is the ACP stdio mode. The zero value ""
// is the no-mode sentinel carried by error resolutions (never a daemon mode).
type commandMode string

const (
	modeServe     commandMode = "serve"
	modeACP       commandMode = "acp"
	mcpAddCommand             = "add"
	mcpFileFlag               = "--file"
)

// errBareInvocation is the usage error a bare `mecated` (no command word) or a
// leading-flag invocation resolves to: a command word is REQUIRED. main()
// recognizes it via errors.Is and prints the top-level help alongside the error.
var errBareInvocation = errors.New("mecated requires a command: 'serve' for the network daemon or 'acp' for ACP stdio")

// commandResolution is the result of classifying argv[1:]. mode + remaining drive
// the daemon run() path. When handled is true, the invocation was a one-shot
// offline subcommand or a leading help-flag request that fully ran (main exits
// with run's error, if any) — run is the closure executing it against the
// supplied streams; it is nil for the non-handled daemon path. When err is
// non-nil the leading word was a usage error (unknown command, unknown/missing
// subcommand); main prints it and exits non-zero WITHOUT constructing any
// listener.
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
		// Bare `mecated` with no args: a usage error — a command word is required.
		return commandResolution{err: errBareInvocation}
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
	// `mecated mcp login` is the sole interactive OAuth presenter. Command
	// classification remains pure; settings, credentials, listener, and browser
	// work happen only inside the captured action after argument validation.
	if first == "mcp" {
		return resolveMCPSubcommand(args)
	}

	// Local microVM administration is a handled one-shot. It runs before daemon
	// configuration, provider construction, and listener setup.
	if first == "microvm" {
		return commandResolution{
			handled: true,
			run: subcommandAction(func(stdin io.Reader, stdout, _ io.Writer) error {
				return runLocalMicroVMCommand(args[2:], stdin, stdout)
			}),
		}
	}

	// `mecated import` is an offline migration command. It never starts a
	// listener or constructs an LLM provider.
	if first == "import" {
		return commandResolution{
			handled: true,
			run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error {
				return runImport(args[2:], stdout)
			}),
		}
	}

	// A leading HELP flag is a help intent, not a usage error: `mecated --help`
	// renders the top-level command page and exits 0 (the universal --help
	// contract — mecatui's bare mode already treats it this way). `--help-all`
	// renders the exhaustive top-level reference. Both are handled one-shots.
	if first == "-h" || first == "--help" || first == "--help-all" {
		return commandResolution{handled: true, run: topLevelHelpAction(first == "--help-all")}
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

	// `mecated config ...` is the config-management subcommand group — see
	// resolveConfigSubcommand.
	if first == "config" {
		return resolveConfigSubcommand(args)
	}

	// Any OTHER leading flag (starts with '-') with no command word is a usage
	// error: `mecated --workspace …` must be spelled `mecated serve --workspace …`.
	if strings.HasPrefix(first, "-") {
		return commandResolution{err: errBareInvocation}
	}

	// Anything else is an unknown command — fail closed.
	return commandResolution{err: unknownCommandError(first)}
}

// topLevelHelpAction is the handled runner for a leading help meta-flag:
// `--help-all` renders the exhaustive top-level reference over the FULL
// production serve FlagSet (a registration error is a build bug, so fail loudly
// rather than render a partial page); `--help`/`-h` render the concise page.
func topLevelHelpAction(all bool) subcommandAction {
	return func(_ io.Reader, stdout, _ io.Writer) error {
		if !all {
			writeTopLevelHelp(stdout)
			return nil
		}
		fs, _, err := parseFlagsModeOut(modeServe, nil, io.Discard)
		if err != nil {
			return fmt.Errorf("build flag set for --help-all: %w", err)
		}
		writeTopLevelHelpAll(stdout, fs)
		return nil
	}
}

// resolveConfigSubcommand classifies the `config` subcommand group. The
// subcommands are `config init` (settings.yaml skeleton), `config validate`
// (settings.yaml validation), and `config daemon <init|validate>` (daemon topology
// YAML). A bare `config`, an unknown
// `config <x>`, or an unknown/missing `config daemon <x>` is a usage error (a
// typo starting an unauthenticated server is a nasty surprise): fail closed
// before the daemon boots — never fall through to run().
func resolveConfigSubcommand(args []string) commandResolution {
	if len(args) >= 3 && args[2] == "init" {
		return commandResolution{
			handled: true,
			run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error {
				return runConfigInit(args[3:], stdout)
			}),
		}
	}
	if len(args) >= 3 && args[2] == "validate" {
		return commandResolution{
			handled: true,
			run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error {
				return runConfigValidate(args[3:], stdout)
			}),
		}
	}
	// `config daemon <init|validate>` — the daemon topology config group
	// (issue #338, ADR 0088). A bare `config daemon` or an unknown
	// `config daemon <x>` is a usage error (fail closed).
	if len(args) >= 3 && args[2] == "daemon" {
		if len(args) >= 4 && args[3] == "init" {
			return commandResolution{
				handled: true,
				run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error {
					return runConfigDaemonInit(args[4:], stdout)
				}),
			}
		}
		if len(args) >= 4 && args[3] == "validate" {
			return commandResolution{
				handled: true,
				run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error {
					return runConfigDaemonValidate(args[4:], stdout)
				}),
			}
		}
		return commandResolution{err: configDaemonUsageError(args)}
	}
	return commandResolution{err: configUsageError(args)}
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

// writeTopLevelHelp renders the concise command-oriented entry page shown by a
// leading `mecated --help` (and printed beneath the error line for a bare or
// other leading-flag invocation). It is the production renderer the resolveCommand
// help resolution AND main's errBareInvocation arm share with the tests; do not
// duplicate it in a test helper.
func writeTopLevelHelp(out io.Writer) {
	_, _ = fmt.Fprintf(out, "Usage: mecated <command> [flags]\n\n")
	writeTopLevelCommands(out)
	_, _ = fmt.Fprintf(out, "\nGlobal: mecated --version prints the build version and exits.\n")
	_, _ = fmt.Fprintf(out, "\nRun 'mecated <command> --help' for common flags and 'mecated <command> --help-all' for the exhaustive reference.\n")
}

func writeTopLevelCommands(out io.Writer) {
	_, _ = fmt.Fprintf(out, "Commands:\n")
	_, _ = fmt.Fprintf(out, "  serve                   run the Mecatl network daemon over gRPC and HTTP/SSE\n")
	_, _ = fmt.Fprintf(out, "  acp                     run the Agent Client Protocol over standard input/output\n")
	_, _ = fmt.Fprintf(out, "  mcp add NAME URL         discover and authorize a direct OAuth MCP server\n")
	_, _ = fmt.Fprintf(out, "  mcp list                 list configured MCP profiles without network or prompts\n")
	_, _ = fmt.Fprintf(out, "  mcp login SERVER [flags] authorize a configured OAuth MCP server\n")
	_, _ = fmt.Fprintf(out, "  mcp remove NAME          remove a configured direct OAuth MCP server\n")
	_, _ = fmt.Fprintf(out, "  microvm doctor|status|delete inspect and administer local microVM attachments\n")
	_, _ = fmt.Fprintf(out, "  import                  import a Codex or Claude Code session, skills, and workspace files\n")
	_, _ = fmt.Fprintf(out, "  config init             write or print an operator settings.yaml template\n")
	_, _ = fmt.Fprintf(out, "  config validate         validate operator settings.yaml without writing\n")
	_, _ = fmt.Fprintf(out, "  config daemon init      write or print a daemon.yaml template\n")
	_, _ = fmt.Fprintf(out, "  config daemon validate  validate daemon.yaml without writing\n")
	_, _ = fmt.Fprintf(out, "  skills promote          promote a candidate skill from quarantine\n")
	_, _ = fmt.Fprintf(out, "  perf-mcp print-config   print perf MCP client configuration\n")
}

// unknownCommandError builds the error message for an unknown leading bare word.
func unknownCommandError(arg string) error {
	var commands strings.Builder
	writeTopLevelCommands(&commands)
	return fmt.Errorf("unknown command %q\n\nAvailable commands:\n%s\nRun 'mecated <command> --help' for command-specific flags", arg, strings.TrimPrefix(commands.String(), "Commands:\n"))
}

func resolveMCPSubcommand(args []string) commandResolution {
	if len(args) >= 3 && (args[2] == "-h" || args[2] == "--help") {
		return commandResolution{handled: true, run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error {
			writeMCPHelp(stdout)
			return nil
		})}
	}
	if len(args) >= 3 && args[2] == mcpAddCommand {
		return commandResolution{handled: true, run: subcommandAction(func(_ io.Reader, stdout, stderr io.Writer) error { return runMCPAdd(args[3:], stdout, stderr) })}
	}
	if len(args) >= 3 && args[2] == "list" {
		return commandResolution{handled: true, run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error { return runMCPList(args[3:], stdout) })}
	}
	if len(args) >= 3 && args[2] == "remove" {
		return commandResolution{handled: true, run: subcommandAction(func(_ io.Reader, stdout, _ io.Writer) error { return runMCPRemove(args[3:], stdout) })}
	}
	if len(args) >= 3 && args[2] == "login" {
		return commandResolution{
			handled: true,
			run: subcommandAction(func(_ io.Reader, stdout, stderr io.Writer) error {
				return runMCPLogin(args[3:], stdout, stderr)
			}),
		}
	}
	return commandResolution{err: mcpUsageError(args)}
}

func writeMCPHelp(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Usage: mecated mcp <subcommand>\n\nSubcommands:\n  add NAME URL [--file PATH] [--credential-store auto|keyring|file]  discover, save, and authorize a direct OAuth server\n  list [--file PATH]                                                show configured profiles without network or prompts\n  login SERVER [--no-browser] [--file PATH | --permission-config PATH ...] [--reset-dcr-registration | --retry-dcr-registration]\n  remove NAME [--file PATH]                                         remove a local profile (no upstream revocation)\n\nLifecycle settings are host-local; changes affect newly started daemons. Use mecated mcp login SERVER to authorize a profile.")
}

func mcpUsageError(argv []string) error {
	sub := ""
	if len(argv) >= 3 {
		sub = argv[2]
	}
	const commands = "available subcommands:\n  add NAME URL [--file PATH] [--credential-store auto|keyring|file]\n  list [--file PATH]\n  login SERVER [--no-browser] [--file PATH | --permission-config PATH ...] [--reset-dcr-registration | --retry-dcr-registration]\n  remove NAME [--file PATH]"
	if sub == "" {
		return errors.New("mcp: missing subcommand\n" + commands)
	}
	return fmt.Errorf("mcp: unknown subcommand %q\n%s", sub, commands)
}

// configUsageError builds the error message for a bare/unknown `config` invocation.
// It distinguishes the two config surfaces: `config init` owns the operator
// settings.yaml (permissions/trust POLICY), `config daemon` owns the daemon.yaml
// (listener TOPOLOGY) — see ADR 0088.
func configUsageError(argv []string) error {
	sub := ""
	if len(argv) >= 3 {
		sub = argv[2]
	}
	if sub == "" {
		return errors.New("config: missing subcommand\navailable subcommands:\n  config init                     write or print an operator settings.yaml template\n  config validate                 validate operator settings.yaml without writing\n  config daemon <init|validate>   manage daemon listener configuration")
	}
	return fmt.Errorf("config: unknown subcommand %q\navailable subcommands:\n  config init                     write or print an operator settings.yaml template\n  config validate                 validate operator settings.yaml without writing\n  config daemon <init|validate>   manage daemon listener configuration", sub)
}

// configDaemonUsageError builds the error message for a bare/unknown
// `config daemon` invocation.
func configDaemonUsageError(argv []string) error {
	sub := ""
	if len(argv) >= 4 {
		sub = argv[3]
	}
	if sub == "" {
		return errors.New("config daemon: missing subcommand\navailable subcommands:\n  config daemon init      write or print a daemon.yaml template\n  config daemon validate  validate daemon.yaml without writing")
	}
	return fmt.Errorf("config daemon: unknown subcommand %q\navailable subcommands:\n  config daemon init      write or print a daemon.yaml template\n  config daemon validate  validate daemon.yaml without writing", sub)
}

// skillsUsageError builds the error message for a bare/unknown `skills` invocation.
func skillsUsageError(argv []string) error {
	sub := ""
	if len(argv) >= 3 {
		sub = argv[2]
	}
	if sub == "" {
		return errors.New("skills: missing subcommand\navailable subcommands:\n  skills promote    promote a candidate skill from quarantine")
	}
	return fmt.Errorf("skills: unknown subcommand %q\navailable subcommands:\n  skills promote    promote a candidate skill from quarantine", sub)
}

// perfMCPUsageError builds the error message for a bare/unknown `perf-mcp` invocation.
func perfMCPUsageError(argv []string) error {
	sub := ""
	if len(argv) >= 3 {
		sub = argv[2]
	}
	if sub == "" {
		return errors.New("perf-mcp: missing subcommand\navailable subcommands:\n  perf-mcp print-config    print perf MCP client configuration")
	}
	return fmt.Errorf("perf-mcp: unknown subcommand %q\navailable subcommands:\n  perf-mcp print-config    print perf MCP client configuration", sub)
}
