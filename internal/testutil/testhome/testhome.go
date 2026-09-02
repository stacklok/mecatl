// Package testhome isolates process-wide config discovery in command tests.
package testhome

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
)

// Run creates isolated HOME and XDG_CONFIG_HOME directories for run.
func Run(prefix string, run func() int) int {
	if marker := os.Getenv("MECATL_TEST_TEMP_LEASE"); managedtemp.ValidTestHomeMarker(marker) {
		root := filepath.Join(marker, "test-home")
		// #nosec G703 -- ValidTestHomeMarker rejects unowned, malformed, and linked leases.
		if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
			_, _ = fmt.Fprintf(os.Stderr, "create managed test home: %v\n", err)
			return 1
		}
		return runWithRoot(root, run)
	}

	root, err := os.MkdirTemp("", prefix+"-test-home-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create isolated test home: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()
	return runWithRoot(root, run)
}

func runWithRoot(root string, run func() int) int {
	for name, dir := range map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"HOME":            filepath.Join(root, "home"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "create isolated %s: %v\n", name, err)
			return 1
		}
		if err := os.Setenv(name, dir); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "set isolated %s: %v\n", name, err)
			return 1
		}
	}
	return run()
}
