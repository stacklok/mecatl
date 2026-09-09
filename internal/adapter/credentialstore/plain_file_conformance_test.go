//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore_test

import (
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore/conformance"
)

func TestHeadlessCredentialStorage_Scenario3_PlainFileConformance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	conformance.Run(t, func(namespace string) (credentialstore.Store, error) {
		store, err := credentialstore.NewPlainFile(root, namespace)
		if err != nil {
			return nil, err
		}
		return store, nil
	}, credentialstore.Capabilities{Persistent: true, CrossProcessCAS: true})
}
