//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package credentialstore

import "fmt"

// EncryptedFileStore is unavailable on platforms without the reviewed POSIX
// ownership, flock, atomic-rename, and directory-sync semantics.
type EncryptedFileStore struct{}

// NewEncryptedFile fails without filesystem side effects on unsupported
// platforms. No weaker fallback is selected implicitly.
func NewEncryptedFile(_, _ string, _ []byte) (*EncryptedFileStore, error) {
	return nil, fmt.Errorf("open encrypted credential store: unsupported platform: %w", ErrUnavailable)
}
