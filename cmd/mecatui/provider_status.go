package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

const (
	providerClassBuiltin  = "built-in"
	openAICodexEndpointID = "openai-codex"
)

type providerStatus struct {
	Name         string
	Class        string
	AuthMethod   string
	Configured   bool
	Auth         string
	DefaultModel string
	Next         string
}

// providerInspection is the local, value-free view used to inspect providers
// and validate a proposed embedded deployment default.
type providerInspection struct {
	definitions         permconfig.ProviderDefinitions
	credentials         cliconfig.ResolvedCredentials
	aliases             map[string]string
	shadowed            map[string]bool
	oidcStoreConfigured bool
	selectedProvider    string
	selectedModel       string
}

func (c providerCommands) runStatus(ctx context.Context, res invocationResolution, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return providerCredentialCancellation(stderr)
	}
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, res.providerAction)
	}
	inspection, err := c.backend.inspect()
	if err != nil {
		return err
	}
	if res.providerName != "" {
		for _, status := range c.statuses(ctx, inspection, true) {
			if status.Name == res.providerName {
				return writeProviderStatus(stdout, status)
			}
		}
		return fmt.Errorf("unknown provider %q; run `mecatui providers` to inspect configured providers", res.providerName)
	}
	statuses := c.statuses(ctx, inspection, false)
	if len(statuses) == 0 {
		_, err := fmt.Fprintln(stdout, "No local providers are configured.\n\nRun `mecatui providers setup` to configure a built-in provider, or `mecatui providers add PROVIDER` to add a custom provider.")
		return err
	}
	for i, status := range statuses {
		if i > 0 {
			if _, err := fmt.Fprintln(stdout); err != nil {
				return err
			}
		}
		if err := writeProviderStatus(stdout, status); err != nil {
			return err
		}
	}
	return nil
}

func writeProviderStatus(out io.Writer, status providerStatus) error {
	_, err := fmt.Fprintf(out, "%s (%s)\n  Authentication: %s\n  Default model: %s\n  Next step: %s\n", status.Name, status.Class, status.Auth, status.DefaultModel, status.Next)
	return err
}

func (c providerCommands) statuses(ctx context.Context, inspection providerInspection, includeUnconfiguredBuiltin bool) []providerStatus {
	statuses := builtinProviderStatuses(inspection.credentials, inspection.shadowed)
	if !includeUnconfiguredBuiltin {
		statuses = slices.DeleteFunc(statuses, func(status providerStatus) bool {
			return status.Class == providerClassBuiltin && !status.Configured
		})
	}
	for id, definition := range inspection.definitions {
		status := providerStatus{
			Name:         id,
			Class:        "custom",
			AuthMethod:   definition.Auth.Method,
			Configured:   definition.Auth.Method == providerAuthNone || inspection.credentials.CustomAvailable(id),
			Auth:         customProviderAuth(inspection.credentials, id, definition.Auth.Method),
			DefaultModel: definition.DefaultModel,
			Next:         "configure this provider in operator settings",
		}
		if definition.Auth.Method == providerAuthOIDC {
			status.Auth, status.Next = c.oidcStatus(ctx, definition)
			status.Configured = status.Auth == "OIDC enrolled"
		} else if status.Configured {
			status.Next = "ready to use"
		} else if definition.Auth.Method == providerAuthAPIKey {
			status.Next = "run `mecatui providers login " + id + "`"
		}
		statuses = append(statuses, status)
	}
	if c.backend.toolHiveAvailable() {
		statuses = append(statuses, toolHiveProviderStatus())
	}
	for i := range statuses {
		if statuses[i].Name == inspection.selectedProvider && inspection.selectedModel != "" {
			statuses[i].DefaultModel = inspection.selectedModel
		}
	}
	slices.SortFunc(statuses, func(a, b providerStatus) int { return cmp.Compare(a.Name, b.Name) })
	return statuses
}

func toolHiveProviderStatus() providerStatus {
	return providerStatus{Name: toolHiveEndpointID, Class: "external", AuthMethod: "external", Configured: true, Auth: "managed externally", DefaultModel: "ToolHive managed", Next: "use `thv llm` tooling"}
}

func currentToolHiveAvailable() bool {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return false
	}
	_, found := toolhivellm.DetectConfig(filepath.Join(dir, toolhivellm.DefaultConfigRelPath))
	return found
}

func inspectLocalProviders() (providerInspection, error) {
	resolver := permconfig.NewWithEnv(permconfig.Options{Conventional: true}, xdgconfig.OSEnv)
	definitions, _, err := resolver.OperatorProviders()
	if err != nil {
		return providerInspection{}, err
	}
	flags := &cliconfig.ProviderFlags{}
	credentialStore := resolver.OperatorCredentialStore()
	if credentialStore != nil && credentialStore.APIKey != nil {
		flags.SetAPIKeyFile(credentialStore.APIKey.File)
	}
	credentials, err := cliconfig.ResolveProviderCredentials(flags, definitions, xdgconfig.OSEnv)
	if err != nil {
		return providerInspection{}, err
	}
	var aliases map[string]string
	selectedProvider, selectedModel := "", ""
	if policy := resolver.OperatorModelPolicy(); policy != nil {
		aliases = policy.Aliases
		selectedProvider, selectedModel = policy.DefaultProvider, policy.Default
	}
	path, explicit := flags.AuthFilePath()
	known := []string{"anthropic", "openai", "openrouter", "opencode", openAICodexEndpointID}
	for id := range definitions {
		known = append(known, id)
	}
	file, err := authfile.LoadStrict(path, explicit, xdgconfig.OSEnv, known)
	if err != nil {
		return providerInspection{}, err
	}
	env := cliconfig.ReadProviderKeys()
	shadowed := map[string]bool{
		"anthropic":  env.Anthropic != "" && file.APIKey("anthropic") != "",
		"openai":     env.OpenAI != "" && file.APIKey("openai") != "",
		"opencode":   env.OpenCode != "" && file.APIKey("opencode") != "",
		"openrouter": env.OpenRouter != "" && file.APIKey("openrouter") != "",
	}
	return providerInspection{
		definitions: definitions, credentials: credentials, aliases: aliases, shadowed: shadowed,
		oidcStoreConfigured: credentialStore != nil && credentialStore.OIDC != nil,
		selectedProvider:    selectedProvider, selectedModel: selectedModel,
	}, nil
}

func (c providerCommands) oidcStatus(ctx context.Context, definition permconfig.ProviderDefinition) (string, string) {
	runtime, err := c.backend.openOIDCRuntime(ctx, definition, false, io.Discard)
	if err != nil {
		return "OIDC storage unavailable", "check locally managed OIDC enrollment"
	}
	defer func() { _ = runtime.Close() }()
	switch runtime.Status(ctx) {
	case llmendpoint.StatusUsable:
		return "OIDC enrolled", "ready to use"
	case llmendpoint.StatusNotEnrolled:
		return "OIDC not enrolled", "run `mecatui providers login " + definition.ID + "`"
	case llmendpoint.StatusExpired:
		return "OIDC enrollment expired", "run `mecatui providers login " + definition.ID + "`"
	default:
		return "OIDC storage unavailable", "check locally managed OIDC enrollment"
	}
}

func builtinProviderStatuses(keys cliconfig.ResolvedCredentials, shadowed map[string]bool) []providerStatus {
	return []providerStatus{
		builtinAPIKeyStatus("anthropic", keys.Anthropic != "", shadowed["anthropic"], "claude-sonnet-4-6"),
		builtinAPIKeyStatus("openai", keys.OpenAI != "", shadowed["openai"], "gpt-5"),
		{Name: openAICodexEndpointID, Class: providerClassBuiltin, AuthMethod: "manual", Configured: keys.HasOpenAICodex(), Auth: configured(keys.HasOpenAICodex()), DefaultModel: "provider default", Next: nextForAPIKey(keys.HasOpenAICodex())},
		builtinAPIKeyStatus("opencode", keys.OpenCode != "", shadowed["opencode"], "glm-5.2"),
		builtinAPIKeyStatus("openrouter", keys.OpenRouter != "", shadowed["openrouter"], "openai/gpt-5"),
	}
}

func builtinAPIKeyStatus(name string, available, shadowed bool, defaultModel string) providerStatus {
	return providerStatus{
		Name: name, Class: providerClassBuiltin, AuthMethod: providerAuthAPIKey,
		Configured: available, Auth: configuredSource(available, shadowed),
		DefaultModel: defaultModel, Next: nextForAPIKey(available),
	}
}

func customProviderAuth(keys cliconfig.ResolvedCredentials, id, method string) string {
	if method == providerAuthNone {
		return "not required"
	}
	return configured(keys.CustomAvailable(id))
}

func configured(ok bool) string {
	if ok {
		return "configured"
	}
	return "not configured"
}

func configuredSource(ok, shadowed bool) string {
	if shadowed {
		return "configured (environment shadows credential_store.api_key.file)"
	}
	return configured(ok)
}

func nextForAPIKey(configured bool) string {
	if configured {
		return "ready to use"
	}
	return "configure an API key"
}
