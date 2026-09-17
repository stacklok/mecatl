package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

const (
	providerOpenRouterID  = "openrouter"
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
	Source       string
	Shadowed     bool
	Selected     bool
}

// providerInspection is the local, value-free view used to inspect providers
// and validate a proposed embedded deployment default.
type providerInspection struct {
	definitions         permconfig.ProviderDefinitions
	credentials         cliconfig.ResolvedCredentials
	aliases             map[string]string
	shadowed            map[string]bool
	sources             map[string]string
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
	marker, shadow := "", ""
	if status.Selected {
		marker = " [selected default]"
	}
	if status.Shadowed {
		shadow = "; file present (shadowed)"
	}
	_, err := fmt.Fprintf(out, "%s (%s)%s\n  Authentication: %s\n  Auth method: %s\n  Source: %s%s\n  Default model: %s\n  verification: not checked\n  Next step: %s\n", providerDisplay(status.Name), providerDisplay(status.Class), marker, providerDisplay(status.Auth), providerDisplay(status.AuthMethod), providerDisplay(status.Source), shadow, providerDisplay(status.DefaultModel), providerDisplay(status.Next))
	return err
}

func providerDisplay(value string) string {
	value = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) || isTrustControl(r) {
			return -1
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > 240 {
		return string(runes[:240]) + "…"
	}
	return value
}

func (c providerCommands) statuses(ctx context.Context, inspection providerInspection, includeUnconfiguredBuiltin bool) []providerStatus {
	// OpenRouter's last-resort credential is environment-only, just as in composition.
	keys := inspection.credentials
	if keys.OpenRouter == "" && inspection.sources[providerOpenRouterID] == "OPENAI_API_KEY (environment fallback)" {
		keys.OpenRouter = keys.OpenAI
	}
	statuses := builtinProviderStatuses(keys, inspection.shadowed)
	if !includeUnconfiguredBuiltin {
		statuses = slices.DeleteFunc(statuses, func(status providerStatus) bool {
			return status.Class == providerClassBuiltin && !status.Configured && status.Name != inspection.selectedProvider && (status.Name != openAICodexEndpointID || keys.AuthFileWarning == "")
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
			status.Next = "select a default with `mecatui providers set-default " + id + "`"
		} else if definition.Auth.Method == providerAuthAPIKey {
			status.Next = "run `mecatui providers login " + id + "`"
		}
		statuses = append(statuses, status)
	}
	if c.backend.toolHiveAvailable() {
		statuses = append(statuses, toolHiveProviderStatus())
	}
	if inspection.selectedProvider != "" && !slices.ContainsFunc(statuses, func(s providerStatus) bool { return s.Name == inspection.selectedProvider }) {
		statuses = append(statuses, providerStatus{Name: inspection.selectedProvider, Class: "unavailable", Auth: "not configured", Next: "restore this provider or select another with `mecatui providers set-default PROVIDER MODEL`"})
	}
	for i := range statuses {
		status := &statuses[i]
		status.Source = providerSource(inspection.sources[status.Name], status.AuthMethod)
		status.Shadowed = inspection.shadowed[status.Name]
		status.Selected = status.Name == inspection.selectedProvider
		if status.Selected && inspection.selectedModel != "" {
			status.DefaultModel = inspection.selectedModel
		}
	}
	slices.SortFunc(statuses, func(a, b providerStatus) int { return cmp.Compare(a.Name, b.Name) })
	return statuses
}

func providerSource(source, method string) string {
	if source != "" {
		return source
	}
	switch method {
	case providerAuthNone:
		return "not required"
	case providerAuthOIDC:
		return "encrypted local OIDC store"
	case "external":
		return "ToolHive configuration; authentication managed externally"
	default:
		return "none"
	}
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

//nolint:gocyclo // Resolve credentials and their provenance from ordinary local configuration inputs.
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
		return providerInspection{}, providerCredentialInputError(flags)
	}
	var aliases map[string]string
	selectedProvider, selectedModel := "", ""
	if policy := resolver.OperatorModelPolicy(); policy != nil {
		aliases = policy.Aliases
		selectedProvider, selectedModel = policy.DefaultProvider, policy.Default
	}
	path, explicit := flags.AuthFilePath()
	known := append(stockAPIKeyProviderIDs(), openAICodexEndpointID)
	for id := range definitions {
		known = append(known, id)
	}
	file, err := authfile.LoadStrict(path, explicit, xdgconfig.OSEnv, known)
	if err != nil {
		return providerInspection{}, providerCredentialInputError(flags)
	}
	envKeys := cliconfig.ReadProviderKeys()
	shadowed := make(map[string]bool, len(stockAPIKeyProviders))
	for _, provider := range stockAPIKeyProviders {
		shadowed[provider.id] = provider.credential(envKeys) != "" && file.APIKey(provider.id) != ""
	}
	sources := make(map[string]string)
	for _, id := range known {
		if file.APIKey(id) != "" {
			sources[id] = "credential_store.api_key.file"
		}
	}
	for _, provider := range stockAPIKeyProviders {
		if provider.credential(envKeys) != "" {
			sources[provider.id] = provider.environmentVar + " (environment)"
		}
	}
	if credentials.OpenRouter == "" && envKeys.OpenAI != "" {
		sources[providerOpenRouterID] = "OPENAI_API_KEY (environment fallback)"
	}
	if credentials.HasOpenAICodex() || credentials.AuthFileWarning != "" {
		sources[openAICodexEndpointID] = "credential_store.api_key.file (manual token)"
	}
	return providerInspection{
		definitions: definitions, credentials: credentials, aliases: aliases, shadowed: shadowed, sources: sources,
		oidcStoreConfigured: credentialStore != nil && credentialStore.OIDC != nil,
		selectedProvider:    selectedProvider, selectedModel: selectedModel,
	}, nil
}

var errProviderCredentialMissing = errors.New("configured credential input is missing; check credential_store.api_key.file")

// Enrollment may create an absent configured file, but never ignore a malformed
// or unreadable existing input. Passive inspection still reports explicit absence.
func (c providerCommands) inspectForEnrollment() (providerInspection, error) {
	inspection, err := c.backend.inspect()
	if errors.Is(err, errProviderCredentialMissing) {
		err = nil
	}
	return inspection, err
}

func providerCredentialInputError(flags *cliconfig.ProviderFlags) error {
	path, _ := flags.AuthFilePath()
	_, readErr := os.ReadFile(path)
	switch {
	case errors.Is(readErr, os.ErrNotExist):
		return errProviderCredentialMissing
	case readErr != nil:
		return errors.New("credential input is unreadable; check credential_store.api_key.file and access permissions")
	default:
		return errors.New("credential input is malformed or unsafe; check its schema and private permissions")
	}
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
	statuses := make([]providerStatus, 0, len(stockAPIKeyProviders)+1)
	for _, provider := range stockAPIKeyProviders {
		statuses = append(statuses, builtinAPIKeyStatus(provider, shadowed[provider.id], keys))
	}
	return append(statuses, codexProviderStatus(keys))
}

func codexProviderStatus(keys cliconfig.ResolvedCredentials) providerStatus {
	auth := "manual subscription token missing"
	if keys.HasOpenAICodex() {
		auth = "manual subscription token locally usable"
	} else if keys.AuthFileWarning != "" {
		auth = "manual subscription token invalid or expired"
	}
	return providerStatus{Name: openAICodexEndpointID, Class: providerClassBuiltin, AuthMethod: "manual", Configured: keys.HasOpenAICodex(), Auth: auth, DefaultModel: "explicit model selector required", Next: "see manual OpenAI Codex token guidance at https://mecatl.dev/docs/features/choose-models#reuse-a-manual-openai-codex-token; no login, refresh, import, or removal here"}
}

func builtinAPIKeyStatus(provider stockAPIKeyProvider, shadowed bool, keys cliconfig.ResolvedCredentials) providerStatus {
	available := provider.credential(keys) != ""
	return providerStatus{
		Name: provider.id, Class: providerClassBuiltin, AuthMethod: providerAuthAPIKey,
		Configured: available, Auth: configuredSource(available, shadowed),
		DefaultModel: app.BuiltinDefaultModelFor(provider.id), Next: nextForAPIKey(available),
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
		return "select a default with `mecatui providers set-default PROVIDER MODEL`"
	}
	return "configure an API key"
}
