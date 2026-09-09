//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package credentialstore

import "context"

// PlainFileStore is unavailable without the reviewed local POSIX guarantees.
type PlainFileStore struct{}

// NewPlainFile fails before mutation on unsupported platforms.
func NewPlainFile(_, _ string) (*PlainFileStore, error) { return nil, ErrUnavailable }

// OpenExistingPlainFile fails on unsupported platforms.
func OpenExistingPlainFile(_, _ string) (*PlainFileStore, error) { return nil, ErrUnavailable }

// Get is unavailable on unsupported platforms.
func (*PlainFileStore) Get(context.Context, []byte) (Record, error) { return Record{}, ErrUnavailable }

// Put is unavailable on unsupported platforms.
func (*PlainFileStore) Put(context.Context, []byte, []byte, *Version) (Record, error) {
	return Record{}, ErrUnavailable
}

// Delete is unavailable on unsupported platforms.
func (*PlainFileStore) Delete(context.Context, []byte, Version) error { return ErrUnavailable }

// Close has no resource to release on unsupported platforms.
func (*PlainFileStore) Close() error { return nil }

// Capabilities reports no persistence or CAS on unsupported platforms.
func (*PlainFileStore) Capabilities() Capabilities { return Capabilities{} }
