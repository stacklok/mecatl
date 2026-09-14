//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore

// EnsurePrivateRoot securely creates or validates an owner-only credential root.
func EnsurePrivateRoot(root string) error {
	return ensurePrivateRoot(root, syncDirectory)
}
