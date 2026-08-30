package credentialstore

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxNamespaceBytes bounds the UTF-8 encoding of a namespace.
	MaxNamespaceBytes = 128
	// MaxRecordKeyBytes bounds an opaque record key.
	MaxRecordKeyBytes = 1024
	// MaxValueBytes bounds an opaque record value to one MiB.
	MaxValueBytes = 1 << 20

	namespaceDomain = "mecatl/credentialstore/namespace/v1"
)

func namespacePhysicalName(namespace []byte) string {
	digest := sha256.Sum256(frameFields(namespaceDomain, namespace))
	return "ns-v1-" + hex.EncodeToString(digest[:])
}

// NamespacePhysicalName returns the on-disk directory name NewEncryptedFile
// uses for namespace. Callers that must locate an existing namespace directory
// without opening the store (e.g. an existing-only precondition check) use this
// instead of re-deriving the naming scheme themselves.
func NamespacePhysicalName(namespace string) string {
	return namespacePhysicalName([]byte(namespace))
}

var (
	// ErrNotFound reports that the selected namespace has no record for the key.
	ErrNotFound = errors.New("credentialstore: record not found")
	// ErrConflict reports that a create target exists or an expected version is stale.
	ErrConflict = errors.New("credentialstore: version conflict")
	// ErrClosed reports an operation attempted through a closed handle.
	ErrClosed = errors.New("credentialstore: closed")
	// ErrTooLarge reports a record key or value above its public size bound.
	ErrTooLarge = errors.New("credentialstore: input too large")
	// ErrInvalidKey reports an empty record key or invalid encryption key.
	ErrInvalidKey = errors.New("credentialstore: invalid key")
	// ErrInvalidNamespace reports a namespace outside the documented grammar.
	ErrInvalidNamespace = errors.New("credentialstore: invalid namespace")
	// ErrInvalidEnvironment reports an invalid environment variable name or lookup.
	ErrInvalidEnvironment = errors.New("credentialstore: invalid environment configuration")
	// ErrCorrupt reports persisted data that cannot be safely authenticated or decoded.
	ErrCorrupt = errors.New("credentialstore: corrupt store")
	// ErrUnavailable reports a backend resource or operation that is unavailable.
	ErrUnavailable = errors.New("credentialstore: unavailable")
)

// Version is an opaque record revision. Its zero value is never accepted as a
// mutation wildcard. Only store implementations in this package can mint valid
// versions.
type Version struct {
	token [32]byte
	valid bool
}

// Equal reports whether two version values are identical. Two zero Versions
// compare equal, but remain invalid mutation expectations.
func (v Version) Equal(other Version) bool {
	return v == other
}

// Record is an owned value copy and the version that identified it when read or
// written. Mutating Value never mutates the store.
type Record struct {
	Value   []byte
	Version Version
}

// Capabilities describes backend durability and compare-and-swap scope.
// Mutability is represented by implementing ConditionalWriter, not by a flag
// that could contradict the implemented interfaces.
type Capabilities struct {
	Persistent      bool
	CrossProcessCAS bool
}

// Reader is the host-internal read-only port for a namespace-bound opaque
// credential source. Backends such as environment or Kubernetes Secret sources
// may implement Reader without supporting mutation.
type Reader interface {
	// Get returns an owned value copy and its current opaque version.
	Get(ctx context.Context, key []byte) (Record, error)

	Capabilities() Capabilities

	// Close is safe for concurrent use and idempotent for this handle.
	Close() error
}

// ConditionalWriter provides compare-and-swap mutation of opaque credential
// records. It is intentionally separate from Reader so read-only sources do not
// have to expose mutations they cannot honor.
type ConditionalWriter interface {
	// Put is always conditional. A nil expected version means create-only. A
	// non-nil expected version replaces only a record carrying that version.
	// There is no unconditional overwrite mode, and a zero Version is not a
	// wildcard.
	Put(ctx context.Context, key, value []byte, expected *Version) (Record, error)

	// Delete removes only the record carrying expected. A missing record returns
	// ErrNotFound; a stale, zero, or foreign version returns ErrConflict.
	Delete(ctx context.Context, key []byte, expected Version) error
}

// Store is a mutable namespace-bound opaque record store. Durable credential
// refresh rotation requires a Store so updates remain conditional.
type Store interface {
	Reader
	ConditionalWriter
}

func validateNamespace(namespace string) error {
	if namespace == "" || len(namespace) > MaxNamespaceBytes || !utf8.ValidString(namespace) {
		return ErrInvalidNamespace
	}
	first, _ := utf8.DecodeRuneInString(namespace)
	last, _ := utf8.DecodeLastRuneInString(namespace)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return ErrInvalidNamespace
	}
	for _, r := range namespace {
		if unicode.IsControl(r) {
			return ErrInvalidNamespace
		}
	}
	return nil
}

func validateRecordKey(key []byte) error {
	switch {
	case len(key) == 0:
		return ErrInvalidKey
	case len(key) > MaxRecordKeyBytes:
		return ErrTooLarge
	default:
		return nil
	}
}

func validateValue(value []byte) error {
	if len(value) > MaxValueBytes {
		return ErrTooLarge
	}
	return nil
}

func validateEncryptionKey(key []byte) error {
	if len(key) != 32 {
		return ErrInvalidKey
	}
	return nil
}

// frameFields creates an unambiguous domain-separated encoding. Domain tags are
// fixed protocol constants; every caller-controlled field carries a big-endian
// uint32 length prefix.
func frameFields(domain string, fields ...[]byte) []byte {
	size := len(domain)
	for _, field := range fields {
		if len(field) > math.MaxUint32 {
			panic("credentialstore: framed field exceeds uint32")
		}
		size += 4 + len(field)
	}
	framed := make([]byte, 0, size)
	framed = append(framed, domain...)
	var length [4]byte
	for _, field := range fields {
		// The pre-allocation bounds check proves this conversion cannot truncate.
		binary.BigEndian.PutUint32(length[:], uint32(len(field))) // #nosec G115
		framed = append(framed, length[:]...)
		framed = append(framed, field...)
	}
	return framed
}
