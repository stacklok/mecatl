package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/stacklok/mecatl/internal/timezonegen"
)

func TestRunRecordsActiveToolchain(t *testing.T) {
	args, flags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = args, flags })
	output := filepath.Join(t.TempDir(), "zones.json")
	os.Args = []string{"timezonegen", "--output", output}
	flag.CommandLine = flag.NewFlagSet("timezonegen", flag.ContinueOnError)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var catalog timezonegen.Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	//nolint:staticcheck // SA1019: verify provenance against the running toolchain, not a different go on PATH.
	archive, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "lib", "time", "zoneinfo.zip"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	if catalog.GoVersion != runtime.Version() || catalog.ArchiveSHA256 != hex.EncodeToString(sum[:]) || len(catalog.Names) < 2 || !slices.Contains(catalog.Names, "UTC") {
		t.Fatalf("generator did not record the active toolchain's catalog: %+v", catalog)
	}
}
