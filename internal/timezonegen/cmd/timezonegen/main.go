// Command timezonegen writes the client catalog. Invoke via task generate:timezones.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/stacklok/mecatl/internal/timezonegen"
)

func run() error {
	output := flag.String("output", "", "generated catalog path")
	flag.Parse()
	if *output == "" {
		return fmt.Errorf("--output is required; use task generate:timezones")
	}
	declared := timezonegen.SourceVersion
	if err := timezonegen.RequireVersion(declared, runtime.Version()); err != nil {
		return err
	}
	//nolint:staticcheck // SA1019: the generator uses its selected runtime archive, not a relocated binary or ambient PATH toolchain.
	catalog, err := timezonegen.Source(runtime.GOROOT(), declared)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*output, append(data, '\n'), 0o644) // #nosec G703 -- build-time CLI output is explicitly selected by the local maintainer, never remote input.
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
