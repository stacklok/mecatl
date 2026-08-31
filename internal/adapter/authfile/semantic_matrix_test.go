package authfile

import (
	"os"
	"testing"

	"github.com/goccy/go-yaml"
)

type matrixFixture struct {
	Cases []struct {
		Name     string            `yaml:"name"`
		Category string            `yaml:"category"`
		Document string            `yaml:"document"`
		Readers  map[string]string `yaml:"readers"`
	} `yaml:"cases"`
}

// TestSemanticMatrix executes authfile's claimed compatibility cases through Load,
// rather than treating the shared fixture as metadata.
func TestSemanticMatrix(t *testing.T) {
	data, err := os.ReadFile("../../../engine/testdata/semantic-matrix.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var matrix matrixFixture
	if err := yaml.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	seen := false
	seenCategories := map[string]bool{}
	for _, fixture := range matrix.Cases {
		want, ok := fixture.Readers["authfile"]
		if !ok {
			continue
		}
		seen = true
		seenCategories[fixture.Category] = true
		if want != "accept" && want != "reject" {
			t.Fatalf("fixture %q declares invalid authfile outcome %q", fixture.Name, want)
		}
		t.Run(fixture.Name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeFile(t, dir, fixture.Document, 0o600)
			file, warning := Load(path, false, fakeEnv(dir), testKnownProviders)
			accepted := file != nil && warning == ""
			if accepted != (want == "accept") {
				t.Fatalf("Load accepted=%v warning=%q, want %s", accepted, warning, want)
			}
		})
	}
	if !seen {
		t.Fatal("semantic matrix declares no authfile outcomes")
	}
	for _, category := range []string{"implicit-bool", "quoted-bool", "null", "timestamp", "tag", "anchor", "alias"} {
		if !seenCategories[category] {
			t.Fatalf("semantic matrix does not execute authfile %s coverage", category)
		}
	}
}
