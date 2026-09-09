//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const (
	recordDomain   = "mecatl/credentialstore/record/v1"
	lockRetryDelay = 5 * time.Millisecond
)

type fileOps struct {
	random       io.Reader
	beforeCommit func(context.Context, string) error
	rename       func(*os.Root, string, string) error
	remove       func(*os.Root, string) error
	syncDir      func(*os.File) error
}

func defaultFileOps() fileOps {
	return fileOps{
		random: rand.Reader,
		beforeCommit: func(ctx context.Context, _ string) error {
			return ctx.Err()
		},
		rename:  func(root *os.Root, oldName, newName string) error { return root.Rename(oldName, newName) },
		remove:  func(root *os.Root, name string) error { return root.Remove(name) },
		syncDir: syncDirectory,
	}
}

// localFileStore supplies the shared owner-only, locked, atomic CAS substrate.
type localFileStore struct {
	mu        sync.RWMutex
	closed    bool
	key       []byte
	plain     bool
	namespace []byte
	nsPath    string
	nsRoot    *os.Root
	ops       fileOps
}

// EncryptedFileStore is an encrypted local-file credential store.
type EncryptedFileStore struct{ *localFileStore }

// PlainFileStore is an owner-only plaintext local-file credential store.
type PlainFileStore struct{ *localFileStore }

var (
	_ Reader            = (*EncryptedFileStore)(nil)
	_ ConditionalWriter = (*EncryptedFileStore)(nil)
	_ Store             = (*EncryptedFileStore)(nil)
	_ Store             = (*PlainFileStore)(nil)
)

// NewEncryptedFile constructs a local encrypted-file store. root must be an
// explicit absolute owner-only path and key must contain exactly 32 bytes of
// already-derived random key material. No key or path discovery is performed.
func NewEncryptedFile(root, namespace string, key []byte) (*EncryptedFileStore, error) {
	if err := validateNamespace(namespace); err != nil {
		return nil, fmt.Errorf("open encrypted credential store: %w", err)
	}
	if err := validateEncryptionKey(key); err != nil {
		return nil, fmt.Errorf("open encrypted credential store: %w", err)
	}
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("open encrypted credential store: %w", ErrUnavailable)
	}
	ownedKey := bytes.Clone(key)
	ok := false
	defer func() {
		if !ok {
			clear(ownedKey)
			runtime.KeepAlive(ownedKey)
		}
	}()

	root = filepath.Clean(root)
	canonicalRoot, err := canonicalPrivateRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open encrypted credential store: %w", err)
	}
	root = canonicalRoot
	if err := ensurePrivateRoot(root, syncDirectory); err != nil {
		return nil, fmt.Errorf("open encrypted credential store: %w", err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, unavailable("open credential root", err)
	}
	defer func() { _ = rootHandle.Close() }()
	nsName := namespacePhysicalName([]byte(namespace))
	if err := ensurePrivateDir(rootHandle, nsName, syncDirectory); err != nil {
		return nil, err
	}
	nsPath := filepath.Join(root, nsName)
	nsRoot, err := rootHandle.OpenRoot(nsName)
	if err != nil {
		return nil, unavailable("open credential namespace", err)
	}
	store := &EncryptedFileStore{localFileStore: &localFileStore{
		key:       ownedKey,
		namespace: []byte(namespace),
		nsPath:    nsPath,
		nsRoot:    nsRoot,
		ops:       defaultFileOps(),
	}}
	ok = true
	return store, nil
}

// OpenExistingEncryptedFile opens an already-created encrypted-file namespace
// without creating or changing the root or namespace permissions.
func OpenExistingEncryptedFile(root, namespace string, key []byte) (*EncryptedFileStore, error) {
	if err := validateNamespace(namespace); err != nil {
		return nil, fmt.Errorf("open existing encrypted credential store: %w", err)
	}
	if err := validateEncryptionKey(key); err != nil {
		return nil, fmt.Errorf("open existing encrypted credential store: %w", err)
	}
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("open existing encrypted credential store: %w", ErrUnavailable)
	}
	ownedKey := bytes.Clone(key)
	ok := false
	defer func() {
		if !ok {
			clear(ownedKey)
			runtime.KeepAlive(ownedKey)
		}
	}()

	root = filepath.Clean(root)
	canonicalRoot, err := canonicalPrivateRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open existing encrypted credential store: %w", err)
	}
	root = canonicalRoot
	if err := validateExistingPrivateRoot(root); err != nil {
		return nil, fmt.Errorf("open existing encrypted credential store: %w", err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, unavailable("open credential root", err)
	}
	defer func() { _ = rootHandle.Close() }()
	nsName := namespacePhysicalName([]byte(namespace))
	if err := validateExistingPrivateDir(rootHandle, nsName); err != nil {
		return nil, err
	}
	nsRoot, err := rootHandle.OpenRoot(nsName)
	if err != nil {
		return nil, unavailable("open credential namespace", err)
	}
	store := &EncryptedFileStore{localFileStore: &localFileStore{
		key:       ownedKey,
		namespace: []byte(namespace),
		nsPath:    filepath.Join(root, nsName),
		nsRoot:    nsRoot,
		ops:       defaultFileOps(),
	}}
	ok = true
	return store, nil
}

// NewPlainFile constructs an owner-only plaintext store below the dedicated
// clientauth-plaintext directory. It shares the encrypted store's local CAS and
// filesystem-safety substrate but performs no encryption.
func NewPlainFile(root, namespace string) (*PlainFileStore, error) {
	return openPlainFile(root, namespace, false)
}

// OpenExistingPlainFile opens an existing plaintext store without creating or
// changing the root or namespace.
func OpenExistingPlainFile(root, namespace string) (*PlainFileStore, error) {
	return openPlainFile(root, namespace, true)
}

func openPlainFile(root, namespace string, existing bool) (*PlainFileStore, error) {
	if err := validateNamespace(namespace); err != nil {
		return nil, fmt.Errorf("open plaintext credential store: %w", err)
	}
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("open plaintext credential store: %w", ErrUnavailable)
	}
	root = filepath.Clean(root)
	canonicalRoot, err := canonicalPrivateRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open plaintext credential store: %w", err)
	}
	root = canonicalRoot
	if existing {
		err = validateExistingPrivateRoot(root)
	} else {
		err = ensurePrivateRoot(root, syncDirectory)
	}
	if err != nil {
		return nil, fmt.Errorf("open plaintext credential store: %w", err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, unavailable("open credential root", err)
	}
	defer func() { _ = rootHandle.Close() }()
	if existing {
		err = validateExistingPrivateDir(rootHandle, plainDirectory)
	} else {
		err = ensurePrivateDir(rootHandle, plainDirectory, syncDirectory)
	}
	if err != nil {
		return nil, err
	}
	nsRoot, err := rootHandle.OpenRoot(plainDirectory)
	if err != nil {
		return nil, unavailable("open plaintext credential namespace", err)
	}
	return &PlainFileStore{localFileStore: &localFileStore{
		plain: true, namespace: []byte(namespace), nsPath: filepath.Join(root, plainDirectory), nsRoot: nsRoot, ops: defaultFileOps(),
	}}, nil
}

// Get returns the authenticated current record.
func (s *localFileStore) Get(ctx context.Context, key []byte) (Record, error) {
	if err := validateRecordKey(key); err != nil {
		return Record{}, err
	}
	var out Record
	err := s.withRecordLock(ctx, key, func(names recordNames) error {
		value, version, exists, err := s.readCurrent(names.data, key)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		out = Record{Value: value, Version: version}
		return nil
	})
	return out, err
}

// Put creates or conditionally replaces an encrypted record.
func (s *localFileStore) Put(ctx context.Context, key, value []byte, expected *Version) (Record, error) {
	if err := validateRecordKey(key); err != nil {
		return Record{}, err
	}
	if err := validateValue(value); err != nil {
		return Record{}, err
	}
	var out Record
	err := s.withRecordLock(ctx, key, func(names recordNames) error {
		_, current, exists, err := s.readCurrent(names.data, key)
		if err != nil {
			return err
		}
		switch {
		case expected == nil && exists:
			return ErrConflict
		case expected != nil && !exists:
			return ErrNotFound
		case expected != nil && (!expected.valid || !current.Equal(*expected)):
			return ErrConflict
		}
		envelope, version, err := s.sealRecord(key, value)
		if err != nil {
			return err
		}
		if err := s.commitEnvelope(ctx, names, envelope); err != nil {
			return err
		}
		out = Record{Value: bytes.Clone(value), Version: version}
		return nil
	})
	return out, err
}

// ReplaceCorrupt atomically replaces a record only while it remains unreadable
// as an authenticated envelope. Valid, missing, and operationally unreadable
// records are never overwritten.
func (s *localFileStore) ReplaceCorrupt(ctx context.Context, key, value []byte) (Record, error) {
	if err := validateRecordKey(key); err != nil {
		return Record{}, err
	}
	if err := validateValue(value); err != nil {
		return Record{}, err
	}
	var out Record
	err := s.withRecordLock(ctx, key, func(names recordNames) error {
		_, _, exists, err := s.readCurrent(names.data, key)
		if err == nil || !errors.Is(err, ErrCorrupt) {
			if err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
			return ErrConflict
		}
		envelope, version, err := s.sealRecord(key, value)
		if err != nil {
			return err
		}
		if err := s.commitEnvelope(ctx, names, envelope); err != nil {
			return err
		}
		out = Record{Value: bytes.Clone(value), Version: version}
		return nil
	})
	return out, err
}

// Delete conditionally removes an authenticated record.
func (s *localFileStore) Delete(ctx context.Context, key []byte, expected Version) error {
	if err := validateRecordKey(key); err != nil {
		return err
	}
	return s.withRecordLock(ctx, key, func(names recordNames) error {
		_, current, exists, err := s.readCurrent(names.data, key)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if !expected.valid || !current.Equal(expected) {
			return ErrConflict
		}
		if err := s.ops.beforeCommit(ctx, "delete"); err != nil {
			return err
		}
		if err := s.ops.remove(s.nsRoot, names.data); err != nil {
			return unavailable("delete credential record", err)
		}
		return s.syncNamespace()
	})
}

// Capabilities reports durable, cooperating-process local CAS.
func (*localFileStore) Capabilities() Capabilities {
	return Capabilities{Persistent: true, CrossProcessCAS: true}
}

// Close waits for operations, closes the rooted handle, and clears the
// store-owned long-lived key copy. It is safe for concurrent use and idempotent.
func (s *localFileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	clear(s.key)
	runtime.KeepAlive(s.key)
	return s.nsRoot.Close()
}

type recordNames struct {
	stem string
	lock string
	data string
}

func (s *localFileStore) withRecordLock(ctx context.Context, key []byte, fn func(recordNames) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	names := s.recordNames(key)
	if err := ensurePrivateFile(s.nsRoot, names.lock, true); err != nil {
		return err
	}
	fl := flock.New(filepath.Join(s.nsPath, names.lock), flock.SetPermissions(0o600))
	locked, err := fl.TryLockContext(ctx, lockRetryDelay)
	if err != nil {
		_ = fl.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("acquire credential record lock: %w: %w", ErrUnavailable, ctxErr)
		}
		return unavailable("acquire credential record lock", err)
	}
	if !locked {
		_ = fl.Close()
		return unavailable("acquire credential record lock", errors.New("lock not acquired"))
	}
	defer func() { _ = fl.Close() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkLockedSentinel(fl, names.lock); err != nil {
		return err
	}
	return fn(names)
}

func (s *localFileStore) checkLockedSentinel(fl *flock.Flock, name string) error {
	lockedInfo, err := fl.Stat()
	if err != nil {
		return unavailable("inspect locked credential sentinel", err)
	}
	if err := validatePrivateFileInfo(lockedInfo); err != nil {
		return err
	}
	rootedInfo, err := s.nsRoot.Lstat(name)
	if err != nil {
		return unavailable("inspect rooted credential sentinel", err)
	}
	if err := validatePrivateFileInfo(rootedInfo); err != nil {
		return err
	}
	if !os.SameFile(lockedInfo, rootedInfo) {
		return unavailable("validate credential lock identity", errors.New("lock path does not identify rooted sentinel"))
	}
	return nil
}

func (s *localFileStore) readCurrent(name string, recordKey []byte) ([]byte, Version, bool, error) {
	info, err := s.nsRoot.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, Version{}, false, nil
	}
	if err != nil {
		return nil, Version{}, false, unavailable("inspect credential record", err)
	}
	if err := validatePrivateFileInfo(info); err != nil {
		return nil, Version{}, false, err
	}
	file, err := s.nsRoot.Open(name)
	if err != nil {
		return nil, Version{}, false, unavailable("open credential record", err)
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, Version{}, false, unavailable("inspect opened credential record", err)
	}
	if err := validatePrivateFileInfo(openedInfo); err != nil {
		return nil, Version{}, false, err
	}
	envelope, err := readBounded(file)
	if err != nil {
		return nil, Version{}, false, err
	}
	value, version, err := s.openRecord(recordKey, envelope)
	if err != nil {
		return nil, Version{}, false, ErrCorrupt
	}
	return value, version, true, nil
}

func (s *localFileStore) sealRecord(key, value []byte) ([]byte, Version, error) {
	if s.plain {
		return sealPlainRecord(s.namespace, key, value, s.ops.random)
	}
	return sealEnvelope(s.key, s.namespace, key, value, s.ops.random)
}

func (s *localFileStore) openRecord(key, record []byte) ([]byte, Version, error) {
	if s.plain {
		return openPlainRecord(s.namespace, key, record)
	}
	return openEnvelope(s.key, s.namespace, key, record)
}

func (s *localFileStore) commitEnvelope(ctx context.Context, names recordNames, envelope []byte) error {
	var randomName [16]byte
	if _, err := io.ReadFull(s.ops.random, randomName[:]); err != nil {
		return unavailable("create credential temporary name", err)
	}
	tempName := "." + names.stem + "-" + hex.EncodeToString(randomName[:]) + ".tmp"
	temp, err := s.nsRoot.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return unavailable("create credential temporary file", err)
	}
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = s.nsRoot.Remove(tempName)
		}
	}()
	if err := validatePrivateFileHandle(temp); err != nil {
		return err
	}
	if _, err := temp.Write(envelope); err != nil {
		return unavailable("write credential temporary file", err)
	}
	if err := temp.Sync(); err != nil {
		return unavailable("sync credential temporary file", err)
	}
	if err := temp.Close(); err != nil {
		return unavailable("close credential temporary file", err)
	}
	if err := s.ops.beforeCommit(ctx, "put"); err != nil {
		return err
	}
	if info, err := s.nsRoot.Lstat(names.data); err == nil {
		if err := validatePrivateFileInfo(info); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return unavailable("inspect credential destination", err)
	}
	if err := s.ops.rename(s.nsRoot, tempName, names.data); err != nil {
		return unavailable("commit credential record", err)
	}
	committed = true
	return s.syncNamespace()
}

func (s *localFileStore) syncNamespace() error {
	dir, err := s.nsRoot.Open(".")
	if err != nil {
		return unavailable("open credential namespace for sync", err)
	}
	defer func() { _ = dir.Close() }()
	if err := s.ops.syncDir(dir); err != nil {
		return unavailable("sync credential namespace", err)
	}
	return nil
}

func (s *localFileStore) recordNames(key []byte) recordNames {
	digest := sha256.Sum256(frameFields(recordDomain, s.namespace, key))
	stem := "rec-v1-" + hex.EncodeToString(digest[:])
	return recordNames{stem: stem, lock: stem + ".lock", data: stem + ".cred"}
}

func unavailable(operation string, err error) error {
	return fmt.Errorf("%s: %w: %w", operation, ErrUnavailable, err)
}
