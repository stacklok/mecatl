//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore/conformance"
)

func TestEncryptedFileConformance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	key := bytes.Repeat([]byte{0x42}, 32)
	conformance.Run(t, func(namespace string) (credentialstore.Store, error) {
		store, err := credentialstore.NewEncryptedFile(root, namespace, key)
		if err != nil {
			return nil, err
		}
		return store, nil
	}, credentialstore.Capabilities{Persistent: true, CrossProcessCAS: true})
}
