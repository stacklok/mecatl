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

Manage provider configuration for the embedded mecated that bare mecatui starts. These commands do not configure a remote mecated; use its operator configuration instead.

Commands:
  status [PROVIDER]              inspect local provider state without changing it
  setup [PROVIDER]               choose a provider, log in, or start custom setup
  add PROVIDER [--no-login]      create a custom provider definition
  login PROVIDER [--no-browser]  save a locally managed API key or enroll OIDC
  logout PROVIDER                remove locally managed credentials
  set-default PROVIDER [MODEL]   set the embedded deployment default
  remove PROVIDER                remove a custom definition and its managed credentials

Classes, authentication, and custody:
  Built-in providers use locally managed API keys; openai-codex is manually managed.
  Custom providers declare api_key, oidc, or no authentication in operator settings.
  ToolHive is external: its lifecycle and credentials remain with ` + "`thv llm`" + ` tooling.
  API keys and OIDC enrollments stay in local operator-managed credential storage. Environment credentials can take precedence; status reports that fact without revealing a secret.

No-provider recovery:
  Run ` + "`mecatui providers status`" + ` to inspect what is available. Run ` + "`mecatui providers setup`" + ` to choose a built-in provider or create a custom one, then use ` + "`mecatui providers set-default PROVIDER [MODEL]`" + ` before starting the embedded server.
`
	case providerActionStatus:
		text = `Usage: mecatui providers status [PROVIDER]

Inspect providers available to the embedded mecated without changing configuration, credentials, or enrollment. Output identifies each provider's class, authentication state, default model, and next recovery action; it never prints credential values.

Built-in and custom providers are locally configured for embedded mecatui. ToolHive is external and remains managed through ` + "`thv llm`" + `. If no provider is ready, run ` + "`mecatui providers setup`" + ` or ` + "`mecatui providers add PROVIDER`" + `, then select it with ` + "`mecatui providers set-default PROVIDER [MODEL]`" + `.
`
	case providerActionSetup:
		text = `Usage: mecatui providers setup [PROVIDER]

Interactively choose a built-in, external, or custom provider. Naming a configured provider starts its local login flow; an unknown name starts custom-provider setup. Setup can write operator configuration and, when authentication is needed, locally managed credentials or OIDC enrollment.

ToolHive remains externally managed and delegates its lifecycle to ` + "`thv llm`" + `. Use ` + "`mecatui providers status`" + ` first when recovering from a no-provider installation.
`
	case providerActionAdd:
		text = `Usage: mecatui providers add PROVIDER [--no-login]

Create a custom provider definition in local operator settings. You will supply its HTTPS base URL, API flavor, default model, and authentication method (api_key, oidc, or none); OIDC also collects issuer and trust settings. Without --no-login, add then starts the selected local credential or OIDC enrollment flow.

--no-login saves only the definition and prints the follow-up login command. This command does not configure remote mecated instances. If no provider is usable afterward, run ` + "`mecatui providers login PROVIDER`" + ` and ` + "`mecatui providers set-default PROVIDER [MODEL]`" + `.
`
	case providerActionLogin:
		text = `Usage: mecatui providers login PROVIDER [--no-browser]

For built-in and api_key custom providers, securely enter and save a locally managed API key. For OIDC custom providers, enroll locally; --no-browser prints the authorization URL instead of opening a browser. ToolHive login is delegated to its externally managed ` + "`thv llm`" + ` lifecycle.

Credentials remain in local operator-managed storage and are never printed. Login does not start or configure a remote mecated. Use ` + "`mecatui providers status PROVIDER`" + ` to verify readiness, then set an embedded default if needed.
`
	case providerActionLogout:
		text = `Usage: mecatui providers logout PROVIDER

Remove only locally managed API-key or OIDC credentials for PROVIDER. The provider definition and embedded deployment default are not changed. ToolHive credentials are externally managed and must be handled with ` + "`thv llm`" + ` tooling.

This is a side-effecting local operation. To recover a provider afterward, run ` + "`mecatui providers login PROVIDER`" + `; use status to distinguish local credentials from environment-provided credentials.
`
	case providerActionSetDefault:
		text = `Usage: mecatui providers set-default PROVIDER [MODEL]

Set the operator deployment default used by the embedded mecated started by bare mecatui. PROVIDER must be usable under the current local provider configuration; MODEL is optional and otherwise resolves to that provider's default. This writes local operator settings and does not alter a remote mecated.

When no provider is usable, first run ` + "`mecatui providers setup`" + ` or ` + "`mecatui providers add PROVIDER`" + `, authenticate it if required, and confirm with ` + "`mecatui providers status`" + `.
`
	case providerActionRemove:
		text = `Usage: mecatui providers remove PROVIDER

Remove a custom provider definition after an explicit confirmation. For locally managed api_key or oidc providers, this also removes the corresponding local credential or enrollment. Built-in providers and externally managed ToolHive cannot be removed.

This is a side-effecting local operation and does not change a remote mecated. If it leaves no usable provider, recover with ` + "`mecatui providers setup`" + ` or ` + "`mecatui providers add PROVIDER`" + `, then set an embedded default.
`
	}
	_, err := fmt.Fprint(out, text)
	return err
}

func providerHelpResult(out io.Writer, action string) error {
	return errors.Join(flag.ErrHelp, writeProviderHelp(out, action))
}
