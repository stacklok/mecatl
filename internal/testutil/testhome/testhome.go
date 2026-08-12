// Package testhome isolates process-wide config discovery in command tests.
package testhome

import (
	"fmt"
	"os"
	"path/filepath"
)

// Run creates isolated HOME and XDG_CONFIG_HOME directories for run.
func Run(prefix string, run func() int) int {
	root, err := os.MkdirTemp("", prefix+"-test-home-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create isolated test home: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()
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
