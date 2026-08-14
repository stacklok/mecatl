//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package credentialstore

import (
	"errors"
	"testing"
)

func TestEncryptedFileUnsupported(t *testing.T) {
	if store, err := NewEncryptedFile("ignored", "namespace", make([]byte, 32)); store != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewEncryptedFile = %v, %v", store, err)
	}
}
