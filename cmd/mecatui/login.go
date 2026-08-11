// login.go implements the `mecatui login` subcommand (issue #265): a CLI-only
// interactive ToolHive LLM OIDC browser flow run IN-PROCESS (the SAME
// toolhivellm.RunInteractiveLogin pipeline `thv llm token` and the ToolHive
// proxy use), so an operator signs in to a ToolHive LLM gateway without
// leaving the terminal or running a separate `thv` binary. It is NOT a
// session and NOT a transport: it writes a refresh-token REFERENCE (never the
// token value) into ToolHive's own config so a subsequent non-interactive
// direct-mode provider (mecated `--toolhive-llm-mode direct`, or the embedded
// server under mecatui) finds the credential without re-login.
//
// It runs in the normal buffer (no alt screen) and uses the default
// ToolHive config path (os.UserConfigDir() + toolhive/config.yaml, the SAME
// path resolveToolhiveIntent reads) — there is no --toolhive-config flag here
// because login is an operator action against the operator's real config,
// not a test fixture.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
)

// runLogin parses the `login` flag tail and drives the interactive OIDC flow.
// Flags:
//
//	--skip-browser   print the authorization URL instead of opening a browser
//	                 (headless/SSH/CI); the operator pastes it into a browser
//	                 that can reach the IdP, then the callback completes.
//	--help / -h      render the login help.
//
// It returns an error (surfaced by main) on any failure; a successful login
// prints the fresh access token to stdout (mirrors `thv llm token`) and exits 0.
// SIGINT/SIGTERM while parked on the OIDC callback cancels the flow and exits 0
// with a "login cancelled" note on stderr — a deliberate ctrl-C is not a failure.
func runLogin(args []string) error {
	fs := flag.NewFlagSet("mecatui login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var skipBrowser bool
	fs.BoolVar(&skipBrowser, "skip-browser", false,
		"print the OIDC authorization URL instead of opening a browser, then wait for the callback (headless/SSH/CI use)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mecatui login [flags]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Run the interactive ToolHive LLM gateway OIDC browser flow in-process and")
		fmt.Fprintln(os.Stderr, "print a fresh access token to stdout. The refresh-token reference is persisted to")
		fmt.Fprintln(os.Stderr, "ToolHive's config so a subsequent direct-mode session reuses the credential.")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// The flow PARKS waiting for the OIDC callback — it opens a browser (or, with
	// --skip-browser, prints a URL the operator carries to another machine) and
	// blocks on a local listener, which can be minutes over SSH. Bind SIGINT/SIGTERM
	// so ctrl-C unwinds that wait (the callback listener + any in-flight IdP
	// exchange close on ctx) instead of the process dying under them.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := toolhivellm.RunInteractiveLogin(ctx, "" /* default config path */, skipBrowser, nil /* diag: stderr fallback */)
	// A cancelled login is an operator action, not a failure: report it plainly
	// rather than as an error trailer. RunInteractiveLogin routes its error through
	// the same sanitiser as the direct path, which passes context sentinels through
	// UNWRAPPED, so errors.Is sees the cancel here.
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "login cancelled")
		return nil
	}
	return err
}
