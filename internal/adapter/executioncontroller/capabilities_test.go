package executioncontroller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
)

var bothCapabilities = []string{CapabilityFilesystem, CapabilityForegroundCommand}

func TestTemplateCapabilitiesCanonicalRevision(t *testing.T) {
	spec := testProfiles().byName["go"].Spec
	canonical, err := json.Marshal(spec)
	const encoded = `"capabilities":["filesystem","foreground-command"]`
	if err != nil || !strings.Contains(string(canonical), encoded) {
		t.Fatalf("canonical encoding %s, %v", canonical, err)
	}
	want := fixtureRevision(t, spec)
	reordered := []string{CapabilityForegroundCommand, CapabilityFilesystem}
	for _, declared := range [][]string{nil, bothCapabilities, reordered} {
		spec.Capabilities = declared
		got, err := CanonicalTemplateRevision(spec)
		if err != nil || got != want {
			t.Fatalf("capabilities %v: revision %s, want %s, %v", declared, got, want, err)
		}
	}
}

func TestLoadTemplatesValidatesCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared any
		reject   string
	}{
		{name: "absent"},
		{name: "null", declared: "null"},
		{name: "reordered", declared: []any{"foreground-command", "filesystem"}},
		{name: "empty", declared: []any{}, reject: "must declare both"},
		{name: "unknown", declared: []any{"filesystem", "shell"}, reject: `unknown capability`},
		{
			name:     "duplicate",
			declared: []any{"filesystem", "foreground-command", "filesystem"},
			reject:   "duplicate",
		},
		{name: "file only", declared: []any{"filesystem"}, reject: "must declare both"},
		{name: "command only", declared: []any{"foreground-command"}, reject: "must declare both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := loadDeclaredCapabilities(t, tc.declared)
			if tc.reject != "" {
				if err == nil || !strings.Contains(err.Error(), tc.reject) {
					t.Fatalf("accepted or wrong error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := p.byName["go"].Spec.Capabilities; !slices.Equal(got, bothCapabilities) {
				t.Fatalf("capabilities %v", got)
			}
		})
	}
}

func loadDeclaredCapabilities(t *testing.T, declared any) (*Profiles, error) {
	t.Helper()
	spec := testProfiles().byName["go"].Spec
	revision := fixtureRevision(t, spec)
	spec.Capabilities = nil
	encoded, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var execution map[string]any
	if err := yaml.Unmarshal(encoded, &execution); err != nil {
		t.Fatal(err)
	}
	if declared == "null" {
		execution["capabilities"] = nil
	} else if declared != nil {
		execution["capabilities"] = declared
	}
	revisions := map[string]any{revision: map[string]any{"execution": execution}}
	file := map[string]any{"go": map[string]any{
		"default": revision, "maxEnvironments": 1, "revisions": revisions,
	}}
	body, err := yaml.Marshal(map[string]any{"templates": file})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "templates.yaml")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadTemplates(path)
}

func TestTemplateEndpointsReportDeclaredCapabilities(t *testing.T) {
	const clientID = "spiffe://cluster/ns/allowed"
	profiles := testProfiles()
	revision := profiles.defaultRevision["go"]
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{
		clientID: {MayAttestOwner: true, ExecutionTemplates: []string{"go"}},
	}}, NewStore(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), "ns", profiles, nil))
	check := func(want []string) {
		t.Helper()
		ctx := authenticatedContext(clientID)
		list, err := h.ListExecutionTemplates(ctx, &executionv1.ListExecutionTemplatesRequest{})
		if err != nil || len(list.Items) != 1 || !slices.Equal(list.Items[0].Capabilities, want) {
			t.Fatalf("catalog: %+v, %v", list, err)
		}
		selector := &executionv1.TemplateSelector{Id: "go", Revision: revision}
		q := &executionv1.ValidateTemplateRequest{Template: selector}
		validated, err := h.ValidateTemplate(ctx, q)
		if err != nil || !slices.Equal(validated.Capabilities, want) {
			t.Fatalf("validate: %+v, %v", validated, err)
		}
	}
	check(bothCapabilities)
	// Bypasses load validation to prove both endpoints read the declared field.
	resolved := profiles.revisions["go"][revision]
	resolved.Spec.Capabilities = []string{CapabilityFilesystem}
	profiles.revisions["go"][revision] = resolved
	check([]string{CapabilityFilesystem})
}
