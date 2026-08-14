package credentialstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"
	"sync/atomic"
)

const memoryVersionDomain = "mecatl/credentialstore/memory-version/v1"

type memoryRecord struct {
	value   []byte
	version Version
}

// MemoryBackend owns records shared by every handle opened from it. It is safe
// for concurrent use. Namespace maps and the generation counter intentionally
// survive individual handle closure and reopen.
type MemoryBackend struct {
	mu         sync.Mutex
	namespaces map[string]map[string]memoryRecord
	identity   [16]byte
	generation uint64
}

// NewMemoryBackend constructs an empty in-memory backend.
func NewMemoryBackend() *MemoryBackend {
	backend, err := newMemoryBackend(rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("credentialstore: initialize memory backend identity: %v", err))
	}
	return backend
}

func newMemoryBackend(random io.Reader) (*MemoryBackend, error) {
	backend := &MemoryBackend{namespaces: make(map[string]map[string]memoryRecord)}
	if _, err := io.ReadFull(random, backend.identity[:]); err != nil {
		return nil, err
	}
	return backend, nil
}

// Open validates namespace and returns a new handle bound to it. Closing the
// handle does not close the backend or any sibling handle.
func (b *MemoryBackend) Open(namespace string) (Store, error) {
	if err := validateNamespace(namespace); err != nil {
		return nil, fmt.Errorf("open memory store: %w", err)
	}
	return &memoryStore{backend: b, namespace: namespace}, nil
}

type memoryStore struct {
	backend   *MemoryBackend
	namespace string
	closed    atomic.Bool
}

var (
	_ Reader            = (*memoryStore)(nil)
	_ ConditionalWriter = (*memoryStore)(nil)
	_ Store             = (*memoryStore)(nil)
)

func (s *memoryStore) Get(ctx context.Context, key []byte) (Record, error) {
	if err := s.preflight(ctx, key); err != nil {
		return Record{}, err
	}
	mapKey := string(key)
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	if err := s.lockedCheck(ctx); err != nil {
		return Record{}, err
	}
	record, ok := s.backend.namespaces[s.namespace][mapKey]
	if !ok {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

func (s *memoryStore) Put(ctx context.Context, key, value []byte, expected *Version) (Record, error) {
	if err := s.preflight(ctx, key); err != nil {
		return Record{}, err
	}
	if err := validateValue(value); err != nil {
		return Record{}, err
	}
	mapKey := string(key)
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	if err := s.lockedCheck(ctx); err != nil {
		return Record{}, err
	}

	namespace := s.backend.namespaces[s.namespace]
	current, exists := namespace[mapKey]
	switch {
	case expected == nil && exists:
		return Record{}, ErrConflict
	case expected != nil && !exists:
		return Record{}, ErrNotFound
	case expected != nil && (!expected.valid || !current.version.Equal(*expected)):
		return Record{}, ErrConflict
	}

	version, err := s.backend.nextVersion()
	if err != nil {
		return Record{}, err
	}
	if namespace == nil {
		if s.backend.namespaces == nil {
			s.backend.namespaces = make(map[string]map[string]memoryRecord)
		}
		namespace = make(map[string]memoryRecord)
		s.backend.namespaces[s.namespace] = namespace
	}
	stored := memoryRecord{value: bytes.Clone(value), version: version}
	namespace[mapKey] = stored
	return cloneRecord(stored), nil
}

func (s *memoryStore) Delete(ctx context.Context, key []byte, expected Version) error {
	if err := s.preflight(ctx, key); err != nil {
		return err
	}
	mapKey := string(key)
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	if err := s.lockedCheck(ctx); err != nil {
		return err
	}
	namespace := s.backend.namespaces[s.namespace]
	current, exists := namespace[mapKey]
	if !exists {
		return ErrNotFound
	}
	if !expected.valid || !current.version.Equal(expected) {
		return ErrConflict
	}
	delete(namespace, mapKey)
	return nil
}

func (*memoryStore) Capabilities() Capabilities {
	return Capabilities{}
}

func (s *memoryStore) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *memoryStore) preflight(ctx context.Context, key []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() {
		return ErrClosed
	}
	return validateRecordKey(key)
}

func (s *memoryStore) lockedCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() {
		return ErrClosed
	}
	return nil
}

// nextVersion is called with b.mu held.
func (b *MemoryBackend) nextVersion() (Version, error) {
	if b.generation == math.MaxUint64 {
		return Version{}, fmt.Errorf("mint memory version: %w", ErrUnavailable)
	}
	b.generation++
	var generation [8]byte
	binary.BigEndian.PutUint64(generation[:], b.generation)
	token := sha256.Sum256(frameFields(memoryVersionDomain, b.identity[:], generation[:]))
	return Version{token: token, valid: true}, nil
}

func cloneRecord(record memoryRecord) Record {
	return Record{Value: bytes.Clone(record.value), Version: record.version}
}
