// Command resolution for the mecatui binary: a PURE, argv-accepting seam that
// classifies the leading CLI word into a complete invocation: bare/flags (local),
// named commands, top-level help, or a usage error. It does not touch os.Args or
// any package-level state. main() calls it once, threads the result into the
// existing parse/run path explicitly, and owns the side-effecting run preparation.
//
// Final grammar (ADR 0089): bare `mecatui [flags]` is the canonical default — it
// ALWAYS hosts an embedded server and NEVER probes loopback. `mecatui connect
// ADDRESS` ALWAYS dials ADDRESS and NEVER probes/embeds. `sessions` opens the
// embedded session browser. ToolHive LLM login is `mecatui llm login`; the
// top-level `login ADDRESS` is reserved for remote login. There is no
// compatibility path: `mecatui local` is an unknown command (fail-closed, the
// error names `connect`) and `--server` is an unknown flag.
//
// The pure shape (resolveInvocation + writeTopLevelHelp) is what the tests
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
	modeLocal          transportMode = "local"
	modeConnect        transportMode = "connect"
	providerActionLogin      = "login"
	providerActionStatus     = "status"
	providerActionLogout     = "logout"
	providerActionSetup      = "setup"
	providerActionAdd        = "add"
	providerActionSetDefault = "set-default"
	providerActionRemove     = "remove"
	// Legacy implementation names are private compatibility shims for the OIDC runtime;
	// command parsing accepts only the providers vocabulary.
	llmActionLogin = providerActionLogin
	llmActionStatus = providerActionStatus
	llmActionLogout = providerActionLogout
	toolHiveEndpointID               = "toolhive"
	// modeLogin is the CLI-only `mecatui llm` lifecycle subcommand.
	modeLogin transportMode = "llm-login"
	// modeLLMConfig writes native endpoint configuration without starting lifecycle operations.
	modeLLMConfig transportMode = "llm-config"
	// modeRemoteLogout removes one saved remote enrolment without starting a transport.
	modeRemoteLogout transportMode = "remote-logout"
	// modeRemoteLogin is the reserved remote-login route. It must remain
	// distinct from modeLogin so an address can never accidentally invoke the
	// ToolHive browser flow.
	modeRemoteLogin transportMode = "remote-login"
)

// topLevelCommand is the single catalog for named entry points. Resolution,
// command discovery, and unknown-command output all derive from this list so a
// newly registered command cannot be accepted but omitted from help.
type topLevelCommand struct {
	name     string
	synopsis string
	purpose  string
	resolve  func([]string) invocationResolution // arguments after the command word
}

var topLevelCommands = []topLevelCommand{
	{
		name:     "sessions",
		synopsis: "sessions",
		purpose:  "browse stored sessions before creating or continuing a chat",
		resolve: func(args []string) invocationResolution {
			return invocationResolution{mode: modeLocal, browseSessions: true, remaining: args}
		},
	},
	{
		name:     "debug",
		synopsis: "debug TARGET [flags]",
		purpose:  "diagnose by an exact session ID or displayed 12-column short handle; exact identity wins, a unique handle resolves automatically, and ambiguity asks for the full exact ID",
		resolve: func(args []string) invocationResolution {
			return resolveDebugCommand(modeLocal, "", args)
		},
	},
	{
		name:     "connect",
		synopsis: "connect ADDRESS [sessions | debug TARGET] [flags]",
		purpose:  "dial a running mecated at ADDRESS (host:port), optionally browsing or debugging a stored session",
		resolve:  resolveConnectCommand,
	},
	{
		name:     "login",
		synopsis: "login ADDRESS",
		purpose:  "log in to a remote mecated at ADDRESS using OIDC",
		resolve:  resolveRemoteLoginCommand,
	},
	{
		name:     "logout",
		synopsis: "logout ADDRESS",
		purpose:  "remove a saved remote OIDC login and best-effort revoke its tokens",
		resolve:  resolveRemoteLogoutCommand,
	},
	{
		name:     "providers",
		synopsis: "providers [command]",
		purpose:  "inspect and manage embedded provider configuration and locally managed credentials",
		resolve:  resolveProvidersCommand,
	},
}

// invocationResolution is the pure classification of a complete CLI invocation:
// bare/default mode, a named command, top-level help, or a leading-word usage
// error. mode + remaining drive run()'s transport path when applicable; address
// is the connect or remote-login target ("" for bare/local or login help). run
// preparation handles the help output and error wrapping after this resolver returns.
type invocationResolution struct {
	mode               transportMode
	address            string // connect or remote-login target; empty for local/login help
	browseSessions     bool   // launch directly into the shared stored-session inventory
	debugTarget        string // immutable target for a dedicated no-filesystem debug session
	debugHelp          bool   // render dedicated debug help instead of transport flag help
	helpIndex          bool   // render the top-level command index
	remaining          []string
	llmAction          string
	llmEndpoint        string
	llmDeprecatedAlias bool
	err                error
}

// resolveInvocation classifies argv (the FULL arg vector, argv[0] included as
// the program name) into an invocationResolution. It is PURE: it does not read
// or mutate os.Args, does not call os.Exit, and does not perform I/O or any
// network probe.
//
//   - bare `mecatui [flags]` / leading flag → modeLocal, embed, never probe (the
//     canonical default).
//   - named commands dispatch to their command-specific resolvers.
//   - `help` and help meta-flags select top-level or command-specific help.
//   - invalid forms return usage errors before run preparation has side effects.
//
// connect and top-level login REQUIRE an ADDRESS immediately after the command
// word: a missing ADDRESS or a flag-first token (--x) is a usage error. Unknown
// leading commands fail closed.
func resolveInvocation(argv []string) invocationResolution {
	args := argv
	if len(args) == 0 {
		// A nil/empty argv is still the bare embedded invocation. Keep this
		// seam total for callers that construct argv rather than using os.Args.
		return invocationResolution{mode: modeLocal, remaining: args}
	}
	if len(args) < 2 {
		// Bare `mecatui` with no args: the canonical embedded invocation.
		return invocationResolution{mode: modeLocal, remaining: args[1:]}
	}
	first := args[1]

	if strings.HasPrefix(first, "-") {
		if first == "--help" || first == "-h" {
			if len(args) != 2 {
				return invocationResolution{err: helpUsageError("help does not accept additional operands")}
			}
			return invocationResolution{helpIndex: true}
		}
		if (first == "--help-all" || first == "--help-flags") && len(args) != 2 {
			return invocationResolution{err: helpUsageError("help does not accept additional operands")}
		}
		for _, arg := range args[2:] {
			if arg == "--help" || arg == "-h" {
				return invocationResolution{helpIndex: true}
			}
		}
		// A leading flag is the bare embedded invocation: `mecatui --workspace …`.
		return invocationResolution{mode: modeLocal, remaining: args[1:]}
	}

	commandArgs := []string(nil)
	if len(args) > 2 {
		commandArgs = args[2:]
	}
	if first == "help" {
		return resolveHelpCommand(commandArgs)
	}
	for _, command := range topLevelCommands {
		if first == command.name {
			if hasUnexpectedHelpOperands(command.name, commandArgs) {
				return invocationResolution{err: helpUsageError("help does not accept additional operands")}
			}
			return command.resolve(commandArgs)
		}
	}

	// Anything else is an unknown command — fail closed.
	return invocationResolution{err: unknownCommandError(first)}
}

// resolveHelpCommand maps the help meta-command to the catalogued command's own
// resolver with the equivalent --help tail. Help itself is not executable, so it
// has the one distinct top-level-index outcome.
func resolveHelpCommand(args []string) invocationResolution {
	if len(args) == 0 {
		return invocationResolution{helpIndex: true}
	}
	if len(args) != 1 {
		return invocationResolution{err: helpUsageError("help accepts at most one command")}
	}
	for _, command := range topLevelCommands {
		if args[0] == command.name {
			return command.resolve([]string{"--help"})
		}
	}
	return invocationResolution{err: helpUsageError(fmt.Sprintf("unknown help target %q", args[0]))}
}

func helpUsageError(problem string) error {
	return fmt.Errorf("%s; run 'mecatui help' for the command index", problem)
}

func hasUnexpectedHelpOperands(command string, args []string) bool {
	if command == "connect" && len(args) > 2 && isHelpMetaFlag(args[1]) {
		return true
	}
	if command == "providers" && len(args) == 2 && args[0] == providerActionLogin && isHelpMetaFlag(args[1]) {
		return false
	}
	return len(args) > 1 && isHelpMetaFlag(args[0])
}

func resolveRemoteLoginCommand(args []string) invocationResolution {
	if len(args) == 1 && isHelpMetaFlag(args[0]) {
		return invocationResolution{mode: modeRemoteLogin, remaining: args}
	}
	if len(args) == 0 {
		return invocationResolution{err: errors.New("login: missing ADDRESS; usage: mecatui login ADDRESS")}
	}
	if strings.HasPrefix(args[0], "-") {
		return invocationResolution{err: fmt.Errorf("login: ADDRESS must immediately follow 'login' (got flag %q); usage: mecatui login ADDRESS", args[0])}
	}
	return invocationResolution{mode: modeRemoteLogin, address: args[0], remaining: args[1:]}
}

func resolveRemoteLogoutCommand(args []string) invocationResolution {
	if len(args) == 1 && isHelpMetaFlag(args[0]) {
		return invocationResolution{mode: modeRemoteLogout, remaining: args}
	}
	if len(args) == 0 {
		return invocationResolution{err: errors.New("logout: missing ADDRESS; usage: mecatui logout ADDRESS")}
	}
	if strings.HasPrefix(args[0], "-") {
		return invocationResolution{err: fmt.Errorf("logout: ADDRESS must immediately follow 'logout' (got flag %q); usage: mecatui logout ADDRESS", args[0])}
	}
	return invocationResolution{mode: modeRemoteLogout, address: args[0], remaining: args[1:]}
}

func resolveProvidersCommand(args []string) invocationResolution {
	const usage = "providers: usage: mecatui providers [status [PROVIDER] | setup [PROVIDER] | add PROVIDER [--no-login] | login PROVIDER [--no-browser] | logout PROVIDER | set-default PROVIDER [MODEL] | remove PROVIDER]"
	if len(args) == 1 && isHelpMetaFlag(args[0]) {
		return invocationResolution{mode: modeLogin, remaining: args}
	}
	if len(args) == 0 {
		return invocationResolution{mode: modeLogin, llmAction: providerActionStatus}
	}
	if len(args) == 2 && isHelpMetaFlag(args[1]) {
		switch args[0] {
		case providerActionStatus, providerActionSetup, providerActionAdd, providerActionLogin, providerActionLogout, providerActionSetDefault, providerActionRemove:
			return invocationResolution{mode: modeLogin, llmAction: args[0], remaining: args[1:]}
		}
	}
	action := args[0]
	if action == providerActionStatus && len(args) <= 2 && (len(args) == 1 || !strings.HasPrefix(args[1], "-")) {
		endpoint := ""
		if len(args) == 2 { endpoint = args[1] }
		return invocationResolution{mode: modeLogin, llmAction: action, llmEndpoint: endpoint}
	}
	if (action == providerActionLogin || action == providerActionLogout || action == providerActionSetup || action == providerActionAdd || action == providerActionSetDefault || action == providerActionRemove) && len(args) >= 2 && !strings.HasPrefix(args[1], "-") {
		if action == providerActionLogin && len(args) == 3 && args[2] == "--no-browser" { return invocationResolution{mode: modeLogin, llmAction: action, llmEndpoint: args[1], remaining: args[2:]} }
		if action == providerActionAdd && len(args) == 3 && args[2] == "--no-login" { return invocationResolution{mode: modeLogin, llmAction: action, llmEndpoint: args[1], remaining: args[2:]} }
		if (action == providerActionLogin || action == providerActionLogout || action == providerActionSetup || action == providerActionAdd || action == providerActionRemove) && len(args) == 2 { return invocationResolution{mode: modeLogin, llmAction: action, llmEndpoint: args[1]} }
		if action == providerActionSetDefault && (len(args) == 2 || len(args) == 3) { return invocationResolution{mode: modeLogin, llmAction: action, llmEndpoint: args[1], remaining: args[2:]} }
	}
	return invocationResolution{err: errors.New(usage)}
}

// resolveConnectCommand preserves connect's special grammar: ADDRESS must
// immediately follow the command, except that its help meta-flags may omit it.
func resolveConnectCommand(args []string) invocationResolution {
	if len(args) == 0 {
		return invocationResolution{err: connectUsageError(args)}
	}
	if strings.HasPrefix(args[0], "-") && !isHelpMetaFlag(args[0]) {
		return invocationResolution{err: connectUsageError(args)}
	}
	if isHelpMetaFlag(args[0]) {
		// Help requests need no ADDRESS; parseTransportFlags renders connect help.
		return invocationResolution{mode: modeConnect, remaining: args}
	}
	remaining := args[1:]
	browseSessions := len(remaining) > 0 && remaining[0] == "sessions"
	if browseSessions {
		remaining = remaining[1:]
	}
	if len(remaining) > 0 && remaining[0] == "debug" {
		return resolveDebugCommand(modeConnect, args[0], remaining[1:])
	}
	return invocationResolution{
		mode:           modeConnect,
		address:        args[0],
		browseSessions: browseSessions,
		remaining:      remaining,
	}
}

func resolveDebugCommand(mode transportMode, address string, args []string) invocationResolution {
	if len(args) == 1 && isHelpMetaFlag(args[0]) {
		return invocationResolution{mode: mode, address: address, debugHelp: true}
	}
	if len(args) == 0 || args[0] == "" {
		return invocationResolution{err: helpUsageError("debug requires TARGET")}
	}
	return invocationResolution{mode: mode, address: address, debugTarget: args[0], remaining: args[1:]}
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

// writeCommandSummary renders every named command from the catalog and its
// concrete command-specific help route. Each synopsis is followed by a softly
// wrapped, indented description to keep the command index readable at the usual
// help width.
func writeCommandSummary(out io.Writer) {
	const descriptionIndent = "    "
	const helpWidth = 80

	_, _ = fmt.Fprintln(out, "Commands:")
	for _, command := range topLevelCommands {
		_, _ = fmt.Fprintf(out, "  %s\n", command.synopsis)
		writeSoftWrapped(out, descriptionIndent, command.purpose, helpWidth)
	}
	_, _ = fmt.Fprintln(out, "\nCommand-specific help:")
	for _, command := range topLevelCommands {
		_, _ = fmt.Fprintf(out, "  mecatui %s --help\n", command.name)
	}
}

func writeSoftWrapped(out io.Writer, indent, text string, width int) {
	line := indent
	for _, word := range strings.Fields(text) {
		if len(line) > len(indent) && len(line)+1+len(word) > width {
			_, _ = fmt.Fprintln(out, line)
			line = indent
		}
		if len(line) > len(indent) {
			line += " "
		}
		line += word
	}
	_, _ = fmt.Fprintln(out, line)
}

// writeTopLevelHelp renders the concise command index used by the top-level help
// spellings and after a leading-word usage error.
func writeTopLevelHelp(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Usage: mecatui [flags]")
	_, _ = fmt.Fprintln(out, "       mecatui debug TARGET [flags]")
	_, _ = fmt.Fprintln(out, "       mecatui connect ADDRESS debug TARGET [flags]")
	_, _ = fmt.Fprintln(out, "       mecatui <command> [flags]")
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "Bare 'mecatui [flags]' hosts an embedded mecated server in-process (no loopback probe).")
	_, _ = fmt.Fprintln(out)
	writeCommandSummary(out)
	_, _ = fmt.Fprintln(out, "Provider configuration and lifecycle: mecatui providers [status [PROVIDER] | setup [PROVIDER] | add PROVIDER [--no-login] | login PROVIDER [--no-browser] | logout PROVIDER | set-default PROVIDER [MODEL] | remove PROVIDER]")
	_, _ = fmt.Fprintln(out, "Remote mecatui uses `mecatui login ADDRESS`; ToolHive MCP discovery and manual openai-codex authentication are separate.")
	_, _ = fmt.Fprintln(out, "\nHelp: mecatui --help, mecatui -h, or mecatui help")
	_, _ = fmt.Fprintln(out, "      mecatui help <command> aliases mecatui <command> --help")
	_, _ = fmt.Fprintln(out, "      mecatui --version prints the build version and exits")
	_, _ = fmt.Fprintln(out, "\nRun 'mecatui --help-flags' for common embedded-mode flags or '--help-all' for the exhaustive bare reference.")
}

// writeDebugHelp renders the debug command contract without falling through to
// the generic transport flag reference.
func writeDebugHelp(out io.Writer, connect bool) {
	usage := "mecatui debug TARGET [flags]"
	if connect {
		usage = "mecatui connect ADDRESS debug TARGET [flags]"
	}
	_, _ = fmt.Fprintf(out, "Usage: %s\n\n", usage)
	_, _ = fmt.Fprintln(out, "TARGET is either the exact session ID (including the ID printed on exit) or the displayed 12-column short handle.")
	_, _ = fmt.Fprintln(out, "Exact identity wins automatically. A unique short handle resolves from the caller-visible session inventory.")
	_, _ = fmt.Fprintln(out, "If a handle is ambiguous, open /session, copy the full exact ID, and pass it as TARGET to the same command.")
	_, _ = fmt.Fprintln(out, "If inventory is unavailable or no handle matches, TARGET is sent unchanged for the server to authorize or reject as an exact ID.")
}

// unknownCommandError builds the error message for an unknown leading bare word.
func unknownCommandError(arg string) error {
	var commands strings.Builder
	writeCommandSummary(&commands)
	return fmt.Errorf("unknown command %q\n\nAvailable commands:\n%s\nBare 'mecatui [flags]' hosts an embedded mecated server in-process (no loopback probe).\nRun 'mecatui --help-flags' for bare-mode common flags", arg, strings.TrimPrefix(commands.String(), "Commands:\n"))
}

// connectUsageError builds the error message for a bare/flag-first `connect`
// invocation. connect REQUIRES an ADDRESS immediately after the command word.
func connectUsageError(args []string) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("connect: ADDRESS must immediately follow 'connect' (got flag %q); usage: mecatui connect ADDRESS [flags]", args[0])
	}
	return errors.New("connect: missing ADDRESS; usage: mecatui connect ADDRESS [flags]")
}
