//go:build kind_execution_e2e

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestQualificationTemplateGrant(t *testing.T) {
	dir := t.TempDir()
	args := os.Args
	os.Args = []string{"pki", dir}
	t.Cleanup(func() { os.Args = args })
	main()
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Clients []struct {
			URI                string   `json:"uri"`
			ExecutionTemplates []string `json:"executionTemplates"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, client := range manifest.Clients {
		if client.URI == "spiffe://mecatl.test/client/qualification" {
			found = true
			if !slices.Equal(client.ExecutionTemplates, []string{"go"}) {
				t.Fatalf("qualification must have exactly the go template grant: %v", client.ExecutionTemplates)
			}
		}
	}
	if !found {
		t.Fatal("qualification client missing")
	}
}
