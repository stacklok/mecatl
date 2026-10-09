// Package testhome isolates process-wide config discovery in command tests.
package testhome

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
)

// Run creates isolated HOME and XDG directories for run.
func Run(prefix string, run func() int) int {
	if marker := os.Getenv("MECATL_TEST_TEMP_LEASE"); managedtemp.ValidTestHomeMarker(marker) {
		root := filepath.Join(marker, "test-home")
		// #nosec G703 -- ValidTestHomeMarker rejects unowned, malformed, and linked leases.
		if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
			_, _ = fmt.Fprintf(os.Stderr, "create managed test home: %v\n", err)
			return 1
		}
		return runWithRoot(root, filepath.Join(marker, "tmp"), run)
	}

	root, err := os.MkdirTemp("", prefix+"-test-home-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create isolated test home: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()
	return runWithRoot(root, filepath.Join(root, "runtime"), run)
}

func runWithRoot(root, runtime string, run func() int) int {
	paths := []struct {
		name string
		dir  string
	}{
		{name: "HOME", dir: filepath.Join(root, "home")},
		{name: "XDG_CONFIG_HOME", dir: filepath.Join(root, "config")},
		{name: "XDG_DATA_HOME", dir: filepath.Join(root, "data")},
		{name: "XDG_STATE_HOME", dir: filepath.Join(root, "state")},
		{name: "XDG_CACHE_HOME", dir: filepath.Join(root, "cache")},
		{name: "XDG_RUNTIME_DIR", dir: runtime},
	}
	for _, path := range paths {
		if err := os.MkdirAll(path.dir, 0o700); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "create isolated %s: %v\n", path.name, err)
			return 1
		}
		// #nosec G302 -- XDG directory roots must be owner-only and searchable.
		if err := os.Chmod(path.dir, 0o700); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "make isolated %s private: %v\n", path.name, err)
			return 1
		}
	}

	type environment struct {
		name  string
		value string
		set   bool
	}
	previous := make([]environment, 0, len(paths))
	for _, path := range paths {
		value, set := os.LookupEnv(path.name)
		previous = append(previous, environment{name: path.name, value: value, set: set})
	}
	restore := func() {
		for _, value := range previous {
			if value.set {
				_ = os.Setenv(value.name, value.value)
			} else {
				_ = os.Unsetenv(value.name)
			}
		}
	}
	for _, path := range paths {
		if err := os.Setenv(path.name, path.dir); err != nil {
			restore()
			xdg.Reload()
			_, _ = fmt.Fprintf(os.Stderr, "set isolated %s: %v\n", path.name, err)
			return 1
		}
	}
	xdg.Reload()
	defer func() {
		restore()
		xdg.Reload()
	}()
	return run()
}
