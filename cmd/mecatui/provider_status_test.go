package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestProvidersStatusIsPassiveAndDeterministic(t *testing.T) {
	old := loadProviderStatuses
	t.Cleanup(func() { loadProviderStatuses = old })
	calls := 0
	loadProviderStatuses = func() ([]providerStatus, error) {
		calls++
		return []providerStatus{
			{Name: "openai", Class: "built-in", Auth: "configured", DefaultModel: "gpt-5", Next: "ready to use"},
			{Name: "toolhive", Class: "external", Auth: "managed externally", DefaultModel: "ToolHive managed", Next: "use `thv llm` tooling"},
		}, nil
	}

	res := resolveInvocation([]string{"mecatui", "providers"})
	if res.err != nil || res.mode != modeProviderStatus {
		t.Fatalf("bare providers resolution = %+v", res)
	}
	var first, second bytes.Buffer
	if err := runProviderStatusCommand(res, &first, &bytes.Buffer{}); err != nil {
		t.Fatalf("first status: %v", err)
	}
	if err := runProviderStatusCommand(res, &second, &bytes.Buffer{}); err != nil {
		t.Fatalf("second status: %v", err)
	}
	if calls != 2 || first.String() != second.String() {
		t.Fatalf("status output must be deterministic and passive: calls=%d first=%q second=%q", calls, first.String(), second.String())
	}
	if strings.Contains(first.String(), "credential") || strings.Contains(first.String(), "fingerprint") {
		t.Fatalf("status exposed credential detail: %q", first.String())
	}
}

func TestProvidersStatusToolHiveAndUnknownProvider(t *testing.T) {
	old := loadProviderStatuses
	t.Cleanup(func() { loadProviderStatuses = old })
	loadProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{{Name: "toolhive", Class: "external", Auth: "managed externally", DefaultModel: "ToolHive managed", Next: "use `thv llm` tooling"}}, nil
	}

	res := resolveInvocation([]string{"mecatui", "providers", "status", "toolhive"})
	var output bytes.Buffer
	if err := runProviderStatusCommand(res, &output, &bytes.Buffer{}); err != nil {
		t.Fatalf("ToolHive status: %v", err)
	}
	if !strings.Contains(output.String(), "next=use `thv llm` tooling") {
		t.Fatalf("ToolHive handoff missing: %q", output.String())
	}

	res = resolveInvocation([]string{"mecatui", "providers", "status", "missing"})
	if err := runProviderStatusCommand(res, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `unknown provider "missing"`) {
		t.Fatalf("unknown provider error = %v", err)
	}
}

func TestProvidersOnlyStatusIsExecutable(t *testing.T) {
	for _, args := range [][]string{
		{"mecatui", "providers", "setup", "openai"},
		{"mecatui", "providers", "add", "custom"},
		{"mecatui", "providers", "login", "openai"},
		{"mecatui", "providers", "logout", "openai"},
		{"mecatui", "providers", "remove", "openai"},
		{"mecatui", "providers", "set-default", "openai"},
	} {
		if got := resolveInvocation(args); got.err == nil {
			t.Errorf("%v resolved to executable mode %+v", args, got)
		}
	}
}
