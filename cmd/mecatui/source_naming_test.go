package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSourceNamingRegression keeps the source boundary and client
// settings ownership from drifting back into the old generator/keymap names.
func TestSourceNamingRegression(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"statusline/generator.go",
		"statusline/generator_test.go",
		"ui/statusline_generator.go",
		"ui/statusline_generator_test.go",
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("obsolete status source file %q exists", path)
		}
	}

	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		lowerPath := strings.ToLower(path)
		if strings.Contains(filepath.Base(lowerPath), "generator") && strings.Contains(lowerPath, "status") {
			t.Errorf("obsolete status source file %q exists", path)
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, obsolete := range []string{"StatusLineGenerator", "StatusGenerator"} {
			if strings.Contains(string(body), obsolete) {
				t.Errorf("%s retains obsolete status boundary %q", path, obsolete)
			}
		}
		if strings.Contains(strings.ToLower(filepath.Base(path)), "keymap") && strings.Contains(string(body), "statusCustomization") {
			t.Errorf("%s mixes status settings with keymap wiring", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
