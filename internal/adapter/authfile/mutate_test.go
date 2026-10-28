package authfile

import (
	"strings"
	"testing"
)

func TestMutateAuthRemovesFinalProvidersSection(t *testing.T) {
	out, unchanged, err := mutateAuth([]byte("providers:\n  openai:\n    api_key: old-key\n"), APIKeyUpdate{Provider: "openai"})
	if err != nil || unchanged {
		t.Fatalf("mutateAuth = (%q, %v, %v), want changed success", out, unchanged, err)
	}
	if got := string(out); got != "" {
		t.Fatalf("final credential should leave an empty document, got %q", got)
	}
	if _, unchanged, err := mutateAuth(out, APIKeyUpdate{Provider: "openai"}); err != nil || !unchanged {
		t.Fatalf("removing from the empty document = (_, %v, %v), want unchanged success", unchanged, err)
	}
	if err := validateUpdatedAuth(out); err != nil {
		t.Fatalf("updated auth document is invalid: %v\n%s", err, out)
	}
}

func TestAuthDocumentValidationRetainsProviderChecksWithMCP(t *testing.T) {
	for name, document := range map[string]string{
		"invalid provider ID":    "providers:\n  BadID:\n    api_key: key\nmcp:\n  context7:\n    api_key: token\n",
		"unknown provider field": "providers:\n  openai:\n    unexpected: value\nmcp:\n  context7:\n    api_key: token\n",
		"non-string API key":     "providers:\n  openai:\n    api_key: 123\nmcp:\n  context7:\n    api_key: token\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateAuthDocument([]byte(document)); err == nil {
				t.Fatal("validateAuthDocument succeeded, want provider preflight rejection")
			}
			if _, _, err := mutateAuth([]byte(document), APIKeyUpdate{Provider: "openai", APIKey: stringPtr("new-key")}); err == nil {
				t.Fatal("mutateAuth succeeded, want provider preflight rejection")
			}
		})
	}
}

func TestMutateAuthPreservesStaticAPIKeyEntries(t *testing.T) {
	const apiKey = "static-secret"
	for _, tt := range []struct {
		name, initial  string
		update         APIKeyUpdate
		wantProvider   string
		wantNamespaces []string
	}{
		{
			name:           "add provider to search-only file",
			initial:        "# keep this comment\nsearch:\n  exa:\n    api_key: '" + apiKey + "'\n",
			update:         APIKeyUpdate{Provider: "openai", APIKey: stringPtr("new-key")},
			wantProvider:   "new-key",
			wantNamespaces: []string{"search:"},
		},
		{
			name:           "add provider to MCP-only file",
			initial:        "mcp:\n  context7:\n    api_key: '" + apiKey + "'\n",
			update:         APIKeyUpdate{Provider: "openai", APIKey: stringPtr("new-key")},
			wantProvider:   "new-key",
			wantNamespaces: []string{"mcp:"},
		},
		{
			name:           "update provider in mixed file",
			initial:        "providers:\n  openai:\n    api_key: old-key\nsearch:\n  brave:\n    api_key: '" + apiKey + "'\nmcp:\n  context7:\n    api_key: '" + apiKey + "'\n",
			update:         APIKeyUpdate{Provider: "openai", APIKey: stringPtr("updated-key")},
			wantProvider:   "updated-key",
			wantNamespaces: []string{"search:", "mcp:"},
		},
		{
			name:           "remove final provider from mixed file",
			initial:        "providers:\n  openai:\n    api_key: old-key\nsearch:\n  websearch:\n    api_key: '" + apiKey + "'\nmcp:\n  context7:\n    api_key: '" + apiKey + "'\n",
			update:         APIKeyUpdate{Provider: "openai"},
			wantNamespaces: []string{"search:", "mcp:"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, unchanged, err := mutateAuth([]byte(tt.initial), tt.update)
			if err != nil || unchanged {
				t.Fatalf("mutateAuth = (%q, %v, %v), want changed success", out, unchanged, err)
			}
			if tt.name == "add provider to search-only file" && !strings.Contains(string(out), "# keep this comment") {
				t.Fatalf("mutation dropped comment: %q", out)
			}
			for _, namespace := range tt.wantNamespaces {
				if !strings.Contains(string(out), namespace) || !strings.Contains(string(out), "api_key: '"+apiKey+"'") {
					t.Fatalf("mutation dropped %s API key: %q", namespace, out)
				}
			}
			if tt.wantProvider == "" {
				if strings.Contains(string(out), "providers:") {
					t.Fatalf("removing final provider retained providers mapping: %q", out)
				}
			} else if !strings.Contains(string(out), "api_key: '"+tt.wantProvider+"'") {
				t.Fatalf("provider update missing %q: %q", tt.wantProvider, out)
			}
			if err := validateAuthDocument(out); err != nil {
				t.Fatalf("mutated document is invalid: %v\n%s", err, out)
			}
		})
	}
}

func TestMutateAuthUpdatesCustomExaWithoutChangingSearchExa(t *testing.T) {
	initial := "providers:\n  exa:\n    api_key: old-llm-key\nsearch:\n  exa:\n    api_key: search-key\n"
	newKey := "new-llm-key"
	out, unchanged, err := mutateAuth([]byte(initial), APIKeyUpdate{Provider: "exa", APIKey: &newKey})
	if err != nil || unchanged {
		t.Fatalf("mutateAuth = (%q, %v, %v), want changed success", out, unchanged, err)
	}
	dir := t.TempDir()
	path := writeFile(t, dir, string(out), 0o600)
	file, warning := Load(path, false, fakeEnv(dir), []string{"exa"})
	if warning != "" || file == nil || file.APIKey("exa") != newKey || file.SearchAPIKey("exa") != "search-key" {
		t.Fatal("custom providers.exa update changed or consumed search.exa")
	}
}

func TestMutateAuthRejectsInvalidStaticAPIKeyEntries(t *testing.T) {
	for name, document := range map[string]string{
		"obsolete MCP token": "mcp:\n  context7:\n    token: old-token\n",
		"invalid search ID":  "search:\n  unknown:\n    api_key: search-key\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateAuthDocument([]byte(document)); err == nil {
				t.Fatal("validateAuthDocument succeeded, want static API-key rejection")
			}
			if _, _, err := mutateAuth([]byte(document), APIKeyUpdate{Provider: "openai", APIKey: stringPtr("new-key")}); err == nil {
				t.Fatal("mutateAuth succeeded, want static API-key preflight rejection")
			}
		})
	}
}

func stringPtr(value string) *string { return &value }

func TestMutateAuthKeepsExistingFlowStyleWhenRemovingSibling(t *testing.T) {
	out, unchanged, err := mutateAuth([]byte("providers: {openai: {api_key: old-key}, anthropic: {api_key: retained-key}}\n"), APIKeyUpdate{Provider: "openai"})
	if err != nil || unchanged {
		t.Fatalf("mutateAuth = (%q, %v, %v), want changed success", out, unchanged, err)
	}
	if !strings.Contains(string(out), "providers: {anthropic:") {
		t.Fatalf("existing flow-style providers section was reformatted:\n%s", out)
	}
}
