package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

type capturingProviderCredentialLoader struct {
	calls       int
	definitions permconfig.ProviderDefinitions
	profile     ProviderCredentials
	lifecycle   interface{ Close() error }
	err         error
}

func (l *capturingProviderCredentialLoader) Load(definitions permconfig.ProviderDefinitions) (ProviderCredentials, interface{ Close() error }, error) {
	l.calls++
	l.definitions = definitions
	return l.profile, l.lifecycle, l.err
}

type countingProviderCredentialsCloser struct{ calls atomic.Int32 }

func (c *countingProviderCredentialsCloser) Close() error {
	c.calls.Add(1)
	return nil
}

func TestADR_0238_BuildLoadsProviderCredentialLoaderOnce(t *testing.T) {
	loader := &capturingProviderCredentialLoader{profile: ProviderCredentials{
		CustomProviderAPIKeys: map[string]string{"gateway": "key"},
	}}
	definitions := permconfig.ProviderDefinitions{"gateway": {
		ID: "gateway", BaseURL: "https://gateway.example/v1", DefaultModel: "model",
		APIFlavor: "openai-responses", Auth: permconfig.ProviderAuth{Method: "api_key"},
	}}
	built, err := Build(context.Background(), Config{
		Workspace: t.TempDir(), Model: "mock", MockProvider: mockllm.New(),
		ProviderDefinitions: definitions, ProviderCredentialLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if loader.calls != 1 {
		t.Fatalf("provider credential loader calls = %d, want 1", loader.calls)
	}
	if got := loader.definitions["gateway"]; got != definitions["gateway"] {
		t.Fatalf("loader definitions = %#v, want resolved operator definition %#v", got, definitions["gateway"])
	}
}

func TestNativeEndpointBypassesLegacyCredentialLoader(t *testing.T) {
	loader := &capturingProviderCredentialLoader{}
	definitions := permconfig.ProviderDefinitions{
		"native": {
			ID: "native", BaseURL: "https://gateway.example/v1", DefaultModel: "model", APIFlavor: "openai-responses",
			Native: &permconfig.NativeEndpointIdentity{CredentialHome: "/credentials"},
		},
		"legacy": {
			ID: "legacy", BaseURL: "https://legacy.example/v1", DefaultModel: "model", APIFlavor: "openai-responses",
			Auth: permconfig.ProviderAuth{Method: "none"},
		},
	}
	built, err := Build(context.Background(), Config{
		Workspace: t.TempDir(), Model: "mock", MockProvider: mockllm.New(),
		ProviderDefinitions: definitions, ProviderCredentialLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if _, exists := loader.definitions["native"]; exists {
		t.Fatal("native endpoint reached the legacy provider credential loader")
	}
	if _, exists := loader.definitions["legacy"]; !exists {
		t.Fatal("legacy provider definition was not passed to its credential loader")
	}
}

func TestADR_0238_BuildOwnsProviderCredentialLifecycle(t *testing.T) {
	t.Run("loader error aborts build", func(t *testing.T) {
		loader := &capturingProviderCredentialLoader{err: errors.New("profile unavailable")}
		if _, err := Build(context.Background(), Config{Workspace: t.TempDir(), Model: "mock", MockProvider: mockllm.New(), ProviderCredentialLoader: loader}); err == nil {
			t.Fatal("Build succeeded after provider credential loader error")
		}
		if loader.calls != 1 {
			t.Fatalf("provider credential loader calls = %d, want 1", loader.calls)
		}
	})
	t.Run("later failure and close release lifecycle once", func(t *testing.T) {
		closer := new(countingProviderCredentialsCloser)
		loader := &capturingProviderCredentialLoader{lifecycle: closer}
		if _, err := Build(context.Background(), Config{ProviderCredentialLoader: loader}); err == nil {
			t.Fatal("Build without provider succeeded")
		}
		if got := closer.calls.Load(); got != 1 {
			t.Fatalf("failure lifecycle closes = %d, want 1", got)
		}

		closer = new(countingProviderCredentialsCloser)
		loader = &capturingProviderCredentialLoader{lifecycle: closer}
		built, err := Build(context.Background(), Config{Workspace: t.TempDir(), Model: "mock", MockProvider: mockllm.New(), ProviderCredentialLoader: loader})
		if err != nil {
			t.Fatal(err)
		}
		built.Close()
		built.Close()
		if got := closer.calls.Load(); got != 1 {
			t.Fatalf("normal lifecycle closes = %d, want 1", got)
		}
	})
}
