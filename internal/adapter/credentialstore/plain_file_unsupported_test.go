//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package credentialstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPlainFileUnsupportedFailsBeforeMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	for _, open := range []func(string, string) (*PlainFileStore, error){NewPlainFile, OpenExistingPlainFile} {
		store, err := open(root, "plain")
		if store != nil || !errors.Is(err, ErrUnavailable) {
			t.Fatal("unsupported platform admitted store")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("unsupported platform mutated filesystem")
		}
	}
}
