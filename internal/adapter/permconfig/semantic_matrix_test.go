package permconfig

import (
	"os"
	"testing"

	"github.com/goccy/go-yaml"
)

type semanticMatrixFixture struct {
	Cases []struct {
		Name     string            `yaml:"name"`
		Category string            `yaml:"category"`
		Document string            `yaml:"document"`
		Readers  map[string]string `yaml:"readers"`
	} `yaml:"cases"`
}

func TestSemanticMatrix(t *testing.T) {
	data, err := os.ReadFile("../../../engine/testdata/semantic-matrix.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var matrix semanticMatrixFixture
	if err := yaml.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	seen := false
	seenCategories := map[string]bool{}
	for _, fixture := range matrix.Cases {
		want, ok := fixture.Readers["permconfig"]
		if !ok {
			continue
		}
		seen = true
		seenCategories[fixture.Category] = true
		if want != "accept" && want != "reject" {
			t.Fatalf("fixture %q declares invalid permconfig outcome %q", fixture.Name, want)
		}
		t.Run(fixture.Name, func(t *testing.T) {
			err := ValidateYAML([]byte(fixture.Document))
			accepted := err == nil
			if accepted != (want == "accept") {
				t.Fatalf("ValidateYAML accepted=%v err=%v, want %s", accepted, err, want)
			}
		})
	}
	if !seen {
		t.Fatal("semantic matrix declares no permconfig outcomes")
	}
	if !seenCategories["merge"] {
		t.Fatal("semantic matrix does not execute permconfig merge coverage")
	}
}
