package daemonconfig

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

// TestSemanticMatrix executes daemonconfig's claimed compatibility cases through
// its strict parse boundary, rather than treating the shared fixture as metadata.
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
		want, ok := fixture.Readers["daemonconfig"]
		if !ok {
			continue
		}
		seen = true
		seenCategories[fixture.Category] = true
		if want != "accept" && want != "reject" {
			t.Fatalf("fixture %q declares invalid daemonconfig outcome %q", fixture.Name, want)
		}
		t.Run(fixture.Name, func(t *testing.T) {
			_, err := parse([]byte(fixture.Document), "matrix.yaml")
			accepted := err == nil
			if accepted != (want == "accept") {
				t.Fatalf("parse accepted=%v err=%v, want %s", accepted, err, want)
			}
		})
	}
	if !seen {
		t.Fatal("semantic matrix declares no daemonconfig outcomes")
	}
	for _, category := range []string{"empty", "numeric", "anchor", "alias"} {
		if !seenCategories[category] {
			t.Fatalf("semantic matrix does not execute daemonconfig %s coverage", category)
		}
	}
}
