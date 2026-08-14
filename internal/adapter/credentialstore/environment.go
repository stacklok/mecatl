package credentialstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"slices"
	"sync"
)

const (
	maxEnvironmentNameBytes = 128
	environmentNamePrefix   = "MECATL_"
)

// EnvironmentLookup is the explicit lookup function used by EnvironmentReader.
// os.LookupEnv may be supplied by a host, but the adapter never selects it itself.
type EnvironmentLookup func(string) (string, bool)

// EnvironmentReader exposes one opaque record key from one base64-encoded
// environment variable. It performs no global environment lookup or mutation.
type EnvironmentReader struct {
	mu        sync.RWMutex
	namespace string
	key       []byte
	name      string
	lookup    EnvironmentLookup
	closed    bool
}

// NewEnvironment constructs a namespace-bound read-only source. Construction
// validates configuration but deliberately does not read the environment.
func NewEnvironment(namespace string, key []byte, name string, lookup EnvironmentLookup) (*EnvironmentReader, error) {
	if err := validateNamespace(namespace); err != nil {
		return nil, err
	}
	if err := validateRecordKey(key); err != nil {
		return nil, err
	}
	if !validEnvironmentName(name) || lookup == nil {
		return nil, ErrInvalidEnvironment
	}
	return &EnvironmentReader{namespace: namespace, key: slices.Clone(key), name: name, lookup: lookup}, nil
}

func validEnvironmentName(name string) bool {
	if len(name) < len(environmentNamePrefix) || len(name) > maxEnvironmentNameBytes || name[:len(environmentNamePrefix)] != environmentNamePrefix {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if i == 0 {
			if c != '_' && (c < 'A' || c > 'Z') {
				return false
			}
			continue
		}
		if c != '_' && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// Get returns the configured record only. The environment value must use
// canonical padded RFC 4648 base64 so arbitrary credential bytes remain safe.
func (r *EnvironmentReader) Get(ctx context.Context, key []byte) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := validateRecordKey(key); err != nil {
		return Record{}, err
	}
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return Record{}, ErrClosed
	}
	lookup := r.lookup
	name := r.name
	namespace := r.namespace
	configuredKey := r.key
	if !slices.Equal(key, configuredKey) {
		r.mu.RUnlock()
		return Record{}, ErrNotFound
	}
	r.mu.RUnlock()

	encoded, ok := lookup(name)
	var record Record
	var resultErr error
	if !ok {
		resultErr = ErrNotFound
	} else {
		maxEncoded := base64.StdEncoding.EncodedLen(MaxValueBytes)
		switch {
		case len(encoded) > maxEncoded:
			resultErr = ErrTooLarge
		default:
			value, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil || base64.StdEncoding.EncodeToString(value) != encoded {
				resultErr = errors.Join(ErrCorrupt, errors.New("credentialstore: environment value is not canonical base64"))
			} else if err := validateValue(value); err != nil {
				resultErr = err
			} else {
				digest := sha256.Sum256(frameFields("mecatl-credentialstore-environment-v1\x00", []byte(namespace), configuredKey, value))
				record = Record{Value: slices.Clone(value), Version: Version{token: digest, valid: true}}
			}
		}
	}

	r.mu.RLock()
	closed := r.closed
	r.mu.RUnlock()
	if closed {
		return Record{}, ErrClosed
	}
	return record, resultErr
}

// Capabilities reports that process environment is not a writable durable CAS backend.
func (*EnvironmentReader) Capabilities() Capabilities { return Capabilities{} }

// Close prevents future reads. It is safe for concurrent use and idempotent.
func (r *EnvironmentReader) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

var _ Reader = (*EnvironmentReader)(nil)
