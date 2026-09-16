package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// writeProviderHelp describes the local provider-management surface without
// reading configuration, opening a credential store, or starting the embedded
// server. Help must be safe even when no provider has been configured yet.
func writeProviderHelp(out io.Writer, action string) error {
	var text string
	switch action {
	case "":
		text = `Usage: mecatui providers [status [PROVIDER] | setup [PROVIDER] | add PROVIDER [--no-login] | login PROVIDER [--no-browser] | logout PROVIDER | set-default PROVIDER [MODEL] | remove PROVIDER]

Manage providers and credentials used by the embedded server. These commands do not change a remote mecated server.

Commands:
  status [PROVIDER]              show provider readiness
  setup [PROVIDER]               configure a provider interactively
  add PROVIDER [--no-login]      add a custom provider
  login PROVIDER [--no-browser]  save an API key or sign in with OIDC
  logout PROVIDER                remove saved credentials
  set-default PROVIDER [MODEL]   set the embedded deployment default
  remove PROVIDER                remove a custom provider and its credentials

API keys and OIDC credentials are stored locally and are never printed. Environment credentials may take precedence. Manage ToolHive providers with ` + "`thv llm`" + `.

Start with ` + "`mecatui providers status`" + ` or ` + "`mecatui providers setup`" + `.
`
	case providerActionStatus:
		text = `Usage: mecatui providers status [PROVIDER]

Show provider readiness, authentication state, default model, and suggested next action. This command changes nothing and never prints credential values.
`
	case providerActionSetup:
		text = `Usage: mecatui providers setup [PROVIDER]

Configure a provider interactively. Select a built-in provider, sign in to a configured provider, or create a custom provider.

Custom provider IDs contain 1–63 lowercase letters, digits, or hyphens; they must start with a letter and end with a letter or digit. Manage ToolHive providers with ` + "`thv llm`" + `.
`
	case providerActionAdd:
		text = `Usage: mecatui providers add PROVIDER [--no-login]

Add a custom provider to local settings, then collect its credentials. Use --no-login to save the provider without collecting credentials.

This command does not change a remote mecated server.
`
	case providerActionLogin:
		text = `Usage: mecatui providers login PROVIDER [--no-browser]

Save an API key or sign in with OIDC for PROVIDER. Use --no-browser to print the OIDC URL instead of opening it.

Credentials are stored locally and never printed. Manage ToolHive credentials with ` + "`thv llm`" + `.
`
	case providerActionLogout:
		text = `Usage: mecatui providers logout PROVIDER

Remove locally stored credentials for PROVIDER. The provider configuration and default selection are unchanged. Environment credentials are not removed.
`
	case providerActionSetDefault:
		text = `Usage: mecatui providers set-default PROVIDER [MODEL]

Set the default provider and optional model for the embedded server. PROVIDER must be ready to use. This command does not change a remote mecated server.
`
	case providerActionRemove:
		text = `Usage: mecatui providers remove PROVIDER

Remove a custom provider and its locally stored credentials after confirmation. Built-in and ToolHive providers cannot be removed. This command does not change a remote mecated server.
`
	}
	_, err := fmt.Fprint(out, text)
	return err
}

func providerHelpResult(out io.Writer, action string) error {
	return errors.Join(flag.ErrHelp, writeProviderHelp(out, action))
}
