// Command execution-templates renders and validates offline execution template values.
// It shares the provider's canonical recipe identity and validation.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 2 && args[0] == "-validate" {
		_, err := executioncontroller.LoadTemplates(args[1])
		return err
	}
	if len(args) != 2 && len(args) != 5 {
		return fmt.Errorf("usage: execution-templates RECIPE_FILE [WORKLOAD_IMAGE DERIVATIVE_IMAGE INCOMPATIBLE_IMAGE] OUTPUT_FILE; or execution-templates -validate TEMPLATES_FILE")
	}
	input, err := os.ReadFile(args[0]) // #nosec G703 -- local operator explicitly selects the recipe file.
	if err != nil {
		return err
	}
	var recipes struct {
		Recipes map[string]map[string]any `yaml:"recipes"`
	}
	if err := yaml.UnmarshalWithOptions(input, &recipes, yaml.DisallowUnknownField()); err != nil {
		return err
	}
	if len(recipes.Recipes) == 0 {
		return fmt.Errorf("empty template recipes")
	}
	definitions := make(map[string]any, len(recipes.Recipes))
	for id, recipe := range recipes.Recipes {
		quota, ok := recipe["maxEnvironments"]
		if !ok {
			return fmt.Errorf("recipe %s requires maxEnvironments", id)
		}
		delete(recipe, "maxEnvironments")
		display := recipe["display"]
		delete(recipe, "display")
		// The Kind harness supplies freshly built images for its three controls.
		if len(args) == 5 {
			recipe["image"] = args[1]
			if id == "operator-utility" {
				recipe["image"] = args[2] // #nosec G602 -- enclosing branch requires exactly five arguments.
			}
			if id == "incompatible-derivative" {
				recipe["image"] = args[3] // #nosec G602 -- enclosing branch requires exactly five arguments.
			}
		}
		encoded, err := yaml.Marshal(recipe)
		if err != nil {
			return err
		}
		var spec executioncontroller.ProfileSpec
		if err := yaml.UnmarshalWithOptions(encoded, &spec, yaml.DisallowUnknownField()); err != nil {
			return err
		}
		revision, err := executioncontroller.CanonicalTemplateRevision(spec)
		if err != nil {
			return err
		}
		entry := map[string]any{"execution": recipe}
		if display != nil {
			entry["display"] = display
		}
		definitions[id] = map[string]any{"default": revision, "maxEnvironments": quota, "revisions": map[string]any{revision: entry}}
	}
	output, err := yaml.Marshal(map[string]any{"templates": definitions})
	if err != nil {
		return err
	}
	profiles, err := writeTemplates(output, args[len(args)-1])
	if err != nil {
		return err
	}
	if len(args) == 5 {
		_, err = fmt.Fprintln(stdout, profiles.DefaultRevision("go"))
	}
	return err
}

func writeTemplates(output []byte, path string) (*executioncontroller.Profiles, error) {
	// Validate before publishing; invalid input must not leave a usable-looking output.
	tmp, err := os.CreateTemp("", "execution-templates-*.yaml")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(output); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	profiles, err := executioncontroller.LoadTemplates(tmp.Name())
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G703 -- operator-selected output path; existing files are never overwritten.
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(output); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return profiles, nil
}
