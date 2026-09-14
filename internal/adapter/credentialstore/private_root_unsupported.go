//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package credentialstore

import "fmt"

// EnsurePrivateRoot fails without filesystem side effects on unsupported platforms.
func EnsurePrivateRoot(string) error {
	return fmt.Errorf("prepare credential root: unsupported platform: %w", ErrUnavailable)
}
