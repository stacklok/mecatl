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

func TestMutateAuthKeepsExistingFlowStyleWhenRemovingSibling(t *testing.T) {
	out, unchanged, err := mutateAuth([]byte("providers: {openai: {api_key: old-key}, anthropic: {api_key: retained-key}}\n"), APIKeyUpdate{Provider: "openai"})
	if err != nil || unchanged {
		t.Fatalf("mutateAuth = (%q, %v, %v), want changed success", out, unchanged, err)
	}
	if !strings.Contains(string(out), "providers: {anthropic:") {
		t.Fatalf("existing flow-style providers section was reformatted:\n%s", out)
	}
}
