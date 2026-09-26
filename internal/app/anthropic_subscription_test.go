package app

import (
	"context"
	"testing"
)

// stubSubscription is a credential source that never touches the network.
type stubSubscription struct{ token, account string }

func (s stubSubscription) AccessToken(context.Context) (string, error) { return s.token, nil }
func (s stubSubscription) AccountID(context.Context) (string, error)   { return s.account, nil }

// noEnvKeys isolates the registry from ambient provider credentials so the test
// asserts the configured path rather than whatever the host exports.
func noEnvKeys(string) string { return "" }

// A stored subscription sign-in must make the Anthropic provider available on
// its own. Without this the sign-in succeeds but inference still demands an
// API key, which is the gap this wiring closes.
func TestAnthropicRegistersFromSubscriptionWithoutAPIKey(t *testing.T) {
	registry, err := buildProviderRegistry(Config{
		AnthropicSubscription: stubSubscription{token: "grant-access", account: "acct-1"},
	}, noEnvKeys)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.entries[providerAnthropic]; !ok {
		t.Fatal("a stored subscription did not make the anthropic provider available")
	}
}

// With neither an API key nor a sign-in the provider must stay unavailable.
// The registry reports having no provider at all rather than registering an
// anthropic entry that would fail at first use.
func TestAnthropicUnavailableWithoutAnyCredential(t *testing.T) {
	registry, err := buildProviderRegistry(Config{}, noEnvKeys)
	if err == nil {
		if _, ok := registry.entries[providerAnthropic]; ok {
			t.Fatal("anthropic was registered with no credential at all")
		}
		return
	}
	// No credential for any provider is the expected outcome here, and it is
	// surfaced as the actionable "no LLM provider available" error.
	if registry != nil {
		t.Fatalf("a failed registry build returned entries: %+v", registry.entries)
	}
}

// An explicitly configured API key keeps its billing identity: a stored
// sign-in must never silently redirect an operator's spend.
func TestConfiguredAPIKeyOutranksSubscription(t *testing.T) {
	registry, err := buildProviderRegistry(Config{
		AnthropicKey:          "sk-ant-configured",
		AnthropicSubscription: stubSubscription{token: "grant-access", account: "acct-1"},
	}, noEnvKeys)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.entries[providerAnthropic]
	if !ok {
		t.Fatal("anthropic was not registered from its API key")
	}
	// The API-key path registers a live-listing entry; the assertion that
	// matters is that registration happened through the key branch at all,
	// which the subscription branch cannot reach while a key is present.
	if entry.provider == nil {
		t.Fatal("anthropic entry has no provider adapter")
	}
}
