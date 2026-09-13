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

const providerClassBuiltin = "built-in"

type providerStatus struct {
	Name         string
	Class        string
	Auth         string
	DefaultModel string
	Next         string
}

// providerInspection is the local, value-free view used to inspect providers
// and validate a proposed embedded deployment default.
type providerInspection struct {
	definitions      permconfig.ProviderDefinitions
	credentials      cliconfig.ResolvedCredentials
	aliases          map[string]string
	shadowed         map[string]bool
	selectedProvider string
	selectedModel    string
}

var (
	loadProviderStatuses    = currentProviderStatuses
	loadAllProviderStatuses = currentAllProviderStatuses
	toolHiveAvailable       = currentToolHiveAvailable
)

func runProviderStatusCommand(res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, res.llmAction)
	}
	if res.llmEndpoint != "" {
		allStatuses, err := loadAllProviderStatuses()
		if err != nil {
			return err
		}
		for _, status := range allStatuses {
			if status.Name == res.llmEndpoint {
				return writeProviderStatus(stdout, status)
			}
		}
		return fmt.Errorf("unknown provider %q; run `mecatui providers` to inspect configured providers", res.llmEndpoint)
	}
	statuses, err := loadProviderStatuses()
	if err != nil {
		return err
	}
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

func currentProviderStatuses() ([]providerStatus, error) {
	inspection, err := inspectLocalProviders()
	if err != nil {
		return nil, err
	}
	return providerStatuses(inspection, false), nil
}

func currentAllProviderStatuses() ([]providerStatus, error) {
	inspection, err := inspectLocalProviders()
	if err != nil {
		return nil, err
	}
	return providerStatuses(inspection, true), nil
}

func providerStatuses(inspection providerInspection, includeUnconfiguredBuiltin bool) []providerStatus {
	statuses := builtinProviderStatuses(inspection.credentials, inspection.shadowed)
	if !includeUnconfiguredBuiltin {
		statuses = slices.DeleteFunc(statuses, func(status providerStatus) bool { return status.Auth == "not configured" })
	}
	for id, definition := range inspection.definitions {
		status := providerStatus{
			Name:         id,
			Class:        "custom",
			Auth:         customProviderAuth(inspection.credentials, id, definition.Auth.Method),
			DefaultModel: definition.DefaultModel,
			Next:         "configure this provider in operator settings",
		}
		if definition.Auth.Method == providerAuthOIDC {
			status.Auth, status.Next = providerOIDCStatus(definition)
		} else if status.Auth == "configured" || status.Auth == "not required" {
			status.Next = "ready to use"
		}
		statuses = append(statuses, status)
	}
	if toolHiveAvailable() {
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
	return providerStatus{Name: toolHiveEndpointID, Class: "external", Auth: "managed externally", DefaultModel: "ToolHive managed", Next: "use `thv llm` tooling"}
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
	if store := resolver.OperatorCredentialStore(); store != nil && store.APIKey != nil {
		flags.SetAPIKeyFile(store.APIKey.File)
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
	known := []string{"anthropic", "openai", "openrouter", "opencode", "openai-codex"}
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
		selectedProvider: selectedProvider, selectedModel: selectedModel,
	}, nil
}

func providerOIDCStatus(definition permconfig.ProviderDefinition) (string, string) {
	runtime, err := openProviderOIDCRuntime(context.Background(), definition, false, io.Discard)
	if err != nil {
		return "OIDC storage unavailable", "check locally managed OIDC enrollment"
	}
	defer func() { _ = runtime.Close() }()
	switch runtime.Status(context.Background()) {
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
		{Name: "anthropic", Class: providerClassBuiltin, Auth: configuredSource(keys.Anthropic != "", shadowed["anthropic"]), DefaultModel: "claude-sonnet-4-6", Next: nextForAPIKey(keys.Anthropic != "")},
		{Name: "openai", Class: providerClassBuiltin, Auth: configuredSource(keys.OpenAI != "", shadowed["openai"]), DefaultModel: "gpt-5", Next: nextForAPIKey(keys.OpenAI != "")},
		{Name: "openai-codex", Class: providerClassBuiltin, Auth: configured(keys.HasOpenAICodex()), DefaultModel: "provider default", Next: nextForAPIKey(keys.HasOpenAICodex())},
		{Name: "opencode", Class: providerClassBuiltin, Auth: configuredSource(keys.OpenCode != "", shadowed["opencode"]), DefaultModel: "glm-5.2", Next: nextForAPIKey(keys.OpenCode != "")},
		{Name: "openrouter", Class: providerClassBuiltin, Auth: configuredSource(keys.OpenRouter != "", shadowed["openrouter"]), DefaultModel: "openai/gpt-5", Next: nextForAPIKey(keys.OpenRouter != "")},
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
