//go:build kind_execution_e2e

// This fixture renders digest-pinned template values using the same Go spec
// encoding as the provider. It is not part of the production binary.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
)

func main() {
	if len(os.Args) != 6 {
		fail(fmt.Errorf("usage: templates RECIPE_FILE WORKLOAD_IMAGE DERIVATIVE_IMAGE INCOMPATIBLE_IMAGE OUTPUT_FILE"))
	}
	input, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail(err)
	}
	var recipes struct {
		Recipes map[string]map[string]any `yaml:"recipes"`
	}
	if err := yaml.UnmarshalWithOptions(input, &recipes, yaml.DisallowUnknownField()); err != nil {
		fail(err)
	}
	if len(recipes.Recipes) == 0 {
		fail(fmt.Errorf("empty template recipes"))
	}
	definitions := make(map[string]any, len(recipes.Recipes))
	for id, recipe := range recipes.Recipes {
		recipe["image"] = os.Args[2]
		if id == "operator-utility" {
			recipe["image"] = os.Args[3]
		}
		if id == "incompatible-derivative" {
			recipe["image"] = os.Args[4]
		}
		encoded, err := yaml.Marshal(recipe)
		if err != nil {
			fail(err)
		}
		var spec executioncontroller.ProfileSpec
		if err := yaml.UnmarshalWithOptions(encoded, &spec, yaml.DisallowUnknownField()); err != nil {
			fail(err)
		}
		canonical, err := json.Marshal(spec)
		if err != nil {
			fail(err)
		}
		sum := sha256.Sum256(append([]byte("mecatl/execution-template/v1\x00"), canonical...))
		revision := "v1-" + hex.EncodeToString(sum[:])
		definitions[id] = map[string]any{"default": revision, "revisions": map[string]any{revision: map[string]any{"execution": recipe}}}
		if id == "go" {
			defer fmt.Println(revision)
		}
	}
	output, err := yaml.Marshal(map[string]any{"templates": definitions})
	if err != nil {
		fail(err)
	}
	f, err := os.OpenFile(os.Args[5], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fail(err)
	}
	if _, err = f.Write(output); err != nil {
		f.Close()
		fail(err)
	}
	if err = f.Close(); err != nil {
		fail(err)
	}
	if _, err = executioncontroller.LoadTemplates(os.Args[5]); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
