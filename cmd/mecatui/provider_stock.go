package main

import "github.com/stacklok/mecatl/internal/cliconfig"

// stockAPIKeyProviders is the Mecatui-local metadata for the built-in providers
// whose API keys Mecatui can manage. Other provider types intentionally retain
// their separate handling.
type stockAPIKeyProvider struct {
	id             string
	environmentVar string
	defaultModel   string
	guidance       string
	credential     func(cliconfig.ResolvedCredentials) string
}

var stockAPIKeyProviders = []stockAPIKeyProvider{
	{
		id:             "anthropic",
		environmentVar: "ANTHROPIC_API_KEY",
		defaultModel:   "claude-sonnet-4-6",
		guidance:       "Anthropic API: create a developer key at https://console.anthropic.com/settings/keys . Claude consumer subscriptions do not include API usage.",
		credential:     func(keys cliconfig.ResolvedCredentials) string { return keys.Anthropic },
	},
	{
		id:             "openai",
		environmentVar: "OPENAI_API_KEY",
		defaultModel:   "gpt-5",
		guidance:       "OpenAI API key (not the manual OpenAI Codex subscription token): https://platform.openai.com/api-keys . ChatGPT consumer subscriptions do not include developer API usage.",
		credential:     func(keys cliconfig.ResolvedCredentials) string { return keys.OpenAI },
	},
	{
		id:             "opencode",
		environmentVar: "OPENCODE_API_KEY",
		defaultModel:   "glm-5.2",
		guidance:       "OpenCode Go API: obtain a key with an active Go subscription at https://opencode.ai/go . Go is not interchangeable with a Zen key, subscription, or endpoint; unrelated consumer subscriptions do not grant Go API access.",
		credential:     func(keys cliconfig.ResolvedCredentials) string { return keys.OpenCode },
	},
	{
		id:             "openrouter",
		environmentVar: "OPENROUTER_API_KEY",
		defaultModel:   "openai/gpt-5",
		guidance:       "OpenRouter API: create a key at https://openrouter.ai/settings/keys and arrange API credits/billing. Consumer chat subscriptions do not fund this API.",
		credential:     func(keys cliconfig.ResolvedCredentials) string { return keys.OpenRouter },
	},
}

func stockAPIKeyProviderFor(id string) (stockAPIKeyProvider, bool) {
	for _, provider := range stockAPIKeyProviders {
		if provider.id == id {
			return provider, true
		}
	}
	return stockAPIKeyProvider{}, false
}

func stockAPIKeyProviderIDs() []string {
	ids := make([]string, 0, len(stockAPIKeyProviders))
	for _, provider := range stockAPIKeyProviders {
		ids = append(ids, provider.id)
	}
	return ids
}
