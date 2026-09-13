package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
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

var loadProviderStatuses = currentProviderStatuses

func runProviderStatusCommand(res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		_, err := fmt.Fprintln(stderr, "Usage: mecatui providers [status [PROVIDER]]")
		return errors.Join(flag.ErrHelp, err)
	}
	statuses, err := loadProviderStatuses()
	if err != nil {
		return err
	}
	if res.llmEndpoint != "" {
		for _, status := range statuses {
			if status.Name == res.llmEndpoint {
				return writeProviderStatus(stdout, status)
			}
		}
		return fmt.Errorf("unknown provider %q; run `mecatui providers` to inspect configured providers", res.llmEndpoint)
	}
	for _, status := range statuses {
		if err := writeProviderStatus(stdout, status); err != nil {
			return err
		}
	}
	return nil
}

func writeProviderStatus(out io.Writer, status providerStatus) error {
	_, err := fmt.Fprintf(out, "%s\tclass=%s\tauth=%s\tdefault-model=%s\tnext=%s\n", status.Name, status.Class, status.Auth, status.DefaultModel, status.Next)
	return err
}

func currentProviderStatuses() ([]providerStatus, error) {
	resolver := permconfig.NewWithEnv(permconfig.Options{Conventional: true}, xdgconfig.OSEnv)
	definitions, _, err := resolver.OperatorProviders()
	if err != nil {
		return nil, err
	}
	flags := &cliconfig.ProviderFlags{}
	credentials, err := cliconfig.ResolveProviderCredentials(flags, definitions, xdgconfig.OSEnv)
	if err != nil {
		return nil, err
	}

	selectedProvider, selectedModel := "", ""
	if models := resolver.OperatorModelPolicy(); models != nil {
		selectedProvider, selectedModel = models.DefaultProvider, models.Default
	}
	statuses := builtinProviderStatuses(credentials)
	for id, definition := range definitions {
		status := providerStatus{
			Name:         id,
			Class:        "custom",
			Auth:         customProviderAuth(credentials, id, definition.Auth.Method),
			DefaultModel: definition.DefaultModel,
			Next:         "configure this provider in operator settings",
		}
		if status.Auth == "configured" || status.Auth == "not required" {
			status.Next = "ready to use"
		}
		statuses = append(statuses, status)
	}
	for i := range statuses {
		if statuses[i].Name == selectedProvider && selectedModel != "" {
			statuses[i].DefaultModel = selectedModel
		}
	}
	slices.SortFunc(statuses, func(a, b providerStatus) int { return cmp.Compare(a.Name, b.Name) })
	return statuses, nil
}

func builtinProviderStatuses(keys cliconfig.ResolvedCredentials) []providerStatus {
	return []providerStatus{
		{Name: "anthropic", Class: providerClassBuiltin, Auth: configured(keys.Anthropic != ""), DefaultModel: "claude-sonnet-4-6", Next: nextForAPIKey(keys.Anthropic != "")},
		{Name: "openai", Class: providerClassBuiltin, Auth: configured(keys.OpenAI != ""), DefaultModel: "gpt-5", Next: nextForAPIKey(keys.OpenAI != "")},
		{Name: "openai-codex", Class: providerClassBuiltin, Auth: configured(keys.HasOpenAICodex()), DefaultModel: "provider default", Next: nextForAPIKey(keys.HasOpenAICodex())},
		{Name: "opencode", Class: providerClassBuiltin, Auth: configured(keys.OpenCode != ""), DefaultModel: "glm-5.2", Next: nextForAPIKey(keys.OpenCode != "")},
		{Name: "openrouter", Class: providerClassBuiltin, Auth: configured(keys.OpenRouter != ""), DefaultModel: "openai/gpt-5", Next: nextForAPIKey(keys.OpenRouter != "")},
		{Name: "toolhive", Class: "external", Auth: "managed externally", DefaultModel: "ToolHive managed", Next: "use `thv llm` tooling"},
	}
}

func customProviderAuth(keys cliconfig.ResolvedCredentials, id, method string) string {
	if method == "none" {
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

func nextForAPIKey(configured bool) string {
	if configured {
		return "ready to use"
	}
	return "configure an API key"
}
