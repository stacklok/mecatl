package main

import (
	"os"
	"testing"

	"github.com/goccy/go-yaml"
)

type settingsMatrixFixture struct {
	Cases []struct {
		Name     string            `yaml:"name"`
		Category string            `yaml:"category"`
		Document string            `yaml:"document"`
		Readers  map[string]string `yaml:"readers"`
	} `yaml:"cases"`
}

// TestSettingsSemanticMatrix executes configvalidate's claimed document cases at
// the settings-document boundary, where anchors and aliases are intentionally
// rejected to protect comment-preserving editor ownership.
func TestSettingsSemanticMatrix(t *testing.T) {
	data, err := os.ReadFile("../../engine/testdata/semantic-matrix.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var matrix settingsMatrixFixture
	if err := yaml.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	seen := false
	seenCategories := map[string]bool{}
	for _, fixture := range matrix.Cases {
		want, ok := fixture.Readers["configvalidate"]
		if !ok {
			continue
		}
		seen = true
		seenCategories[fixture.Category] = true
		if want != "accept" && want != "reject" {
			t.Fatalf("fixture %q declares invalid configvalidate outcome %q", fixture.Name, want)
		}
		t.Run(fixture.Name, func(t *testing.T) {
			_, err := parseSettingsDocument([]byte(fixture.Document), false)
			accepted := err == nil
			if accepted != (want == "accept") {
				t.Fatalf("parseSettingsDocument accepted=%v err=%v, want %s", accepted, err, want)
			}
		})
	}
	if !seen {
		t.Fatal("semantic matrix declares no configvalidate outcomes")
	}
	for _, category := range []string{"anchor", "alias", "alias-through-merge"} {
		if !seenCategories[category] {
			t.Fatalf("semantic matrix does not execute configvalidate %s coverage", category)
		}
	}
}
