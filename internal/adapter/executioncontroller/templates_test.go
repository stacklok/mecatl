package executioncontroller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func templateFixture(t *testing.T, entries map[string]TemplateRevision, defaultRevision string) (*Profiles, string) {
	t.Helper()
	file := TemplatesFile{Templates: map[string]TemplateDefinition{"go": {Default: defaultRevision, Revisions: entries}}}
	body, err := yaml.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "templates.yaml")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadTemplates(path)
	if err != nil {
		t.Fatal(err)
	}
	return p, path
}

func fixtureRevision(t *testing.T, spec ProfileSpec) string {
	t.Helper()
	canonical, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append([]byte("mecatl/execution-template/v1\x00"), canonical...))
	return "v1-" + hex.EncodeToString(sum[:])
}

func TestExecutionTemplateHistoryAcrossRestart(t *testing.T) {
	oldSpec := testProfiles().byName["go"].Spec
	newSpec := oldSpec
	newSpec.Image = strings.Replace(oldSpec.Image, "aaaa", "bbbb", 1)
	newSpec.MaxEnvironments = oldSpec.MaxEnvironments - 1
	oldRev, newRev := fixtureRevision(t, oldSpec), fixtureRevision(t, newSpec)
	old := TemplateRevision{Execution: oldSpec, Display: TemplateDisplay{Name: "Old", Extensions: map[string]map[string]string{"example.com/catalog": {"schema_version": "1", "category": "docs"}}}}
	newer := TemplateRevision{Execution: newSpec}
	initial, _ := templateFixture(t, map[string]TemplateRevision{oldRev: old}, oldRev)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	store := NewStore(dyn, "ns", initial, nil)
	a, err := store.EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "binding", "go", oldRev, "fp", "operation")
	if err != nil {
		t.Fatal(err)
	}
	env, err := dyn.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), a.Environment.ID, metav1.GetOptions{})
	if err != nil || textNested(env.Object, "spec", "templateRevision") != oldRev {
		t.Fatalf("allocation lacks exact revision: %v: %v", env, err)
	}
	updated, _ := templateFixture(t, map[string]TemplateRevision{oldRev: {Execution: oldSpec, Deprecated: true, Display: TemplateDisplay{Name: "Renamed"}}, newRev: newer}, newRev)
	if updated.capacity("go") != newSpec.MaxEnvironments {
		t.Fatal("template revisions do not share the strictest ID-wide capacity")
	}
	oldLoaded, ok := updated.forEnvironment(env)
	if !ok || oldLoaded.Spec.Image != oldSpec.Image || oldLoaded.Digest != initial.revisions["go"][oldRev].Digest {
		t.Fatal("historical recipe changed or was lost after restart")
	}
	if _, ok := updated.selectRevision("go", oldRev); ok {
		t.Fatal("deprecated revision remains selectable")
	}
	store = NewStore(dyn, "ns", updated, nil)
	if _, err := store.EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "other", "go", oldRev, "fp", "operation"); err == nil {
		t.Fatal("deprecated revision allocated a new environment")
	}
	if _, err := store.EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "binding", "go", newRev, "fp", "operation"); err == nil {
		t.Fatal("same binding adopted a different revision")
	}
	revoked, _ := templateFixture(t, map[string]TemplateRevision{oldRev: {Execution: oldSpec, Revoked: true}, newRev: newer}, newRev)
	if _, ok := revoked.forEnvironment(env); ok || !revoked.isRevoked(env) {
		t.Fatal("revocation did not fence retained definition")
	}
	if _, err := NewStore(dyn, "ns", revoked, nil).Attach(t.Context(), a.Environment, "client", ownerHash(owner), "binding"); err == nil {
		t.Fatal("revoked environment reattached")
	}
	if _, err := NewStore(dyn, "ns", revoked, nil).AcquireRun(t.Context(), a.Environment, "client", ownerHash(owner), "binding", "run", "acquire", executionenv.MinRunTTL); err == nil {
		t.Fatal("revoked environment admitted a new run")
	}
	removed, _ := templateFixture(t, map[string]TemplateRevision{newRev: newer}, newRev)
	if _, ok := removed.forEnvironment(env); ok {
		t.Fatal("missing historical definition was silently replaced")
	}
}

func TestExecutionTemplateDisplayDoesNotChangeRevision(t *testing.T) {
	spec := testProfiles().byName["go"].Spec
	revision := fixtureRevision(t, spec)
	p, path := templateFixture(t, map[string]TemplateRevision{revision: {Execution: spec, Display: TemplateDisplay{Name: "Display"}}}, revision)
	if p.capacity("go") != spec.MaxEnvironments {
		t.Fatal("capacity is not pooled by template ID")
	}
	display := NewStore(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), "ns", p, nil).CatalogTemplates()[0]
	p2, _ := templateFixture(t, map[string]TemplateRevision{revision: {Execution: spec, Display: TemplateDisplay{Name: "Changed"}}}, revision)
	if NewStore(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), "ns", p2, nil).CatalogTemplates()[0].DisplayToken == display.DisplayToken || p2.revisions["go"][revision].Digest != p.revisions["go"][revision].Digest {
		t.Fatal("display token and execution revision are coupled")
	}
	bad := strings.Replace(string(mustReadTemplate(t, path)), "templates:", "profiles:", 1)
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTemplates(path); err == nil {
		t.Fatal("legacy config silently accepted")
	}
	if err := validateTemplateDisplay(TemplateDisplay{Extensions: map[string]map[string]string{"example.com/catalog": {"category": "docs"}}}); err == nil {
		t.Fatal("unversioned metadata accepted")
	}
	if err := validateTemplateDisplay(TemplateDisplay{Extensions: map[string]map[string]string{"namespace": {"schema_version": "1"}}}); err == nil {
		t.Fatal("unqualified metadata accepted")
	}
}

func TestExecutionTemplateRetainedOnlyLastRevisionReload(t *testing.T) {
	spec := testProfiles().byName["go"].Spec
	revision := fixtureRevision(t, spec)
	initial, path := templateFixture(t, map[string]TemplateRevision{revision: {Execution: spec}}, revision)
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	allocation, err := NewStore(dyn, "ns", initial, nil).EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "binding", "go", revision, "fp", "op")
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []TemplateRevision{{Execution: spec, Deprecated: true}, {Execution: spec, Revoked: true}} {
		encoded, err := yaml.Marshal(TemplatesFile{Templates: map[string]TemplateDefinition{"go": {Revisions: map[string]TemplateRevision{revision: policy}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		reloaded, err := LoadTemplates(path)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.DefaultRevision("go") != "" || len(reloaded.revisions["go"]) != 1 || len(NewStore(dyn, "ns", reloaded, nil).CatalogTemplates()) != 0 {
			t.Fatal("retained-only revision became selectable or disappeared")
		}
		env, err := dyn.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), allocation.Environment.ID, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, retained := reloaded.forEnvironment(env)
		if retained == policy.Revoked {
			t.Fatal("deprecated revision should remain reattachable, revoked revision must be fenced")
		}
	}
	if _, err := LoadTemplates(path); err != nil {
		t.Fatal(err)
	}
	_, invalidPath := templateFixture(t, map[string]TemplateRevision{revision: {Execution: spec}}, revision)
	body := mustReadTemplate(t, invalidPath)
	body = []byte(strings.Replace(string(body), "  default: "+revision+"\n", "", 1))
	if err := os.WriteFile(invalidPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTemplates(invalidPath); err == nil {
		t.Fatal("selectable revision with no default accepted")
	}
}

func TestExecutionTemplateMetadataAggregateBound(t *testing.T) {
	spec := testProfiles().byName["go"].Spec
	revision := fixtureRevision(t, spec)
	// 238-byte namespace plus separator and 14-byte schema_version key.
	namespace := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 38) + "/catalog"
	if len(namespace) != 238 {
		t.Fatalf("namespace length = %d", len(namespace))
	}
	display := TemplateDisplay{Extensions: map[string]map[string]string{namespace: {"schema_version": "1", "custom": "opaque"}}}
	p, _ := templateFixture(t, map[string]TemplateRevision{revision: {Execution: spec, Display: display}}, revision)
	items := NewStore(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), "ns", p, nil).CatalogTemplates()
	if len(items) != 1 || items[0].Extensions[namespace+"/custom"] != "opaque" {
		t.Fatal("valid metadata lost in private catalog")
	}
	fields := display.Extensions[namespace]
	fields[strings.Repeat("x", 30)] = "opaque"
	if err := validateTemplateDisplay(display); err == nil {
		t.Fatal("oversized flattened public metadata key accepted")
	}
}

func mustReadTemplate(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
