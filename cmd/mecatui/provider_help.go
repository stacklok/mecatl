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
		text = `Usage: mecatui providers [status [PROVIDER] | setup [PROVIDER] | add PROVIDER [--no-login] | login PROVIDER [--no-browser|--subscription|--device] | logout PROVIDER [--subscription] | set-default PROVIDER [MODEL] | remove PROVIDER]

Manage providers and credentials used by the embedded server. These commands do not change a remote mecated server.

Commands:
  status [PROVIDER]              show provider readiness
  setup [PROVIDER]               configure a provider interactively
  add PROVIDER [--no-login]      add a custom provider
  login PROVIDER [flags]         save an API key, enroll OIDC, or sign in to a subscription
  logout PROVIDER [flags]        remove saved credentials
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

Configure a provider interactively. For a configured API-key provider, reuse the effective credential or replace it with separate save consent; an unknown name starts custom-provider setup. Successful setup offers a separate optional deployment-default action. Setup can write operator configuration and, when authentication is needed, locally managed credentials or OIDC enrollment.

Custom provider IDs contain 1–63 lowercase letters, digits, or hyphens; they must start with a letter and end with a letter or digit. Manage ToolHive providers with ` + "`thv llm`" + `.
`
	case providerActionAdd:
		text = `Usage: mecatui providers add PROVIDER [--no-login]

Add a custom provider to local settings, then collect its credentials. Use --no-login to save the provider without collecting credentials.

This command does not change a remote mecated server.
`
	case providerActionLogin:
		text = `Usage: mecatui providers login PROVIDER [--no-browser] [--subscription] [--device]

Save an API key or sign in with OIDC for PROVIDER. Use --no-browser to print the OIDC URL instead of opening it.

Subscription sign-in uses a plan entitlement instead of an API key. openai-codex has no API key and always signs in; anthropic keeps its API-key path and requires --subscription. --device completes openai-codex sign-in without a local browser or callback port. The grant is stored in the host credential store and refreshed automatically; ` + "`--subscription`" + ` on logout removes it. An Anthropic sign-in expires about a month after it is granted and cannot be extended by refresh.

Credentials are stored locally and are never printed. Login does not start or configure a remote mecated. Use ` + "`mecatui providers status PROVIDER`" + ` to verify readiness, then set an embedded default if needed. Manage ToolHive credentials with ` + "`thv llm`" + `.
`
	case providerActionLogout:
		text = `Usage: mecatui providers logout PROVIDER

Remove locally stored credentials for PROVIDER. The provider configuration and default selection are unchanged. Environment credentials are not removed.
`
	case providerActionSetDefault:
		text = `Usage: mecatui providers set-default PROVIDER [MODEL]

Set the operator deployment default used by the embedded server that bare mecatui starts. PROVIDER must be ready to use under the current local provider configuration; when MODEL is omitted, the current selector is preserved for the same provider, otherwise its declared default is resolved. Providers without a default require an explicit selector. This writes local operator settings and does not change a remote mecated.

When no provider is usable, first run ` + "`mecatui providers setup`" + ` or ` + "`mecatui providers add PROVIDER`" + `, authenticate it if required, and confirm with ` + "`mecatui providers status`" + `.
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
