//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestEncryptedFileReopenWrongKeyAndIndependentHandles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	key := bytes.Repeat([]byte{1}, 32)
	first := openFileStore(t, root, "namespace", key)
	record, err := first.Put(context.Background(), []byte("key"), []byte("secret"), nil)
	if err != nil {
		t.Fatal(err)
	}
	second := openFileStore(t, root, "namespace", key)
	got, err := second.Get(context.Background(), []byte("key"))
	if err != nil || !bytes.Equal(got.Value, []byte("secret")) || !got.Version.Equal(record.Version) {
		t.Fatalf("reopen = %q, %v", got.Value, err)
	}
	if _, err := first.Put(context.Background(), []byte("key"), []byte("stale"), &record.Version); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if _, err := second.Put(context.Background(), []byte("key"), []byte("lost"), &record.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale second replace = %v", err)
	}
	wrong := openFileStore(t, root, "namespace", bytes.Repeat([]byte{2}, 32))
	if _, err := wrong.Get(context.Background(), []byte("key")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong-key Get = %v, want ErrCorrupt", err)
	}
}

func TestEncryptedFileCorruptionIsNeverOverwrittenOrDeleted(t *testing.T) {
	store := openFileStore(t, filepath.Join(t.TempDir(), "credentials"), "namespace", bytes.Repeat([]byte{3}, 32))
	ctx := context.Background()
	key := []byte("key")
	record, err := store.Put(ctx, key, []byte("secret"), nil)
	if err != nil {
		t.Fatal(err)
	}
	name := store.recordNames(key).data
	path := filepath.Join(store.nsPath, name)
	corrupt := []byte("not an envelope")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	for operation, err := range map[string]error{
		"Get":    func() error { _, err := store.Get(ctx, key); return err }(),
		"Put":    func() error { _, err := store.Put(ctx, key, []byte("replacement"), &record.Version); return err }(),
		"Delete": store.Delete(ctx, key, record.Version),
	} {
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s = %v, want ErrCorrupt", operation, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, corrupt) {
		t.Fatalf("corruption changed: %x, %v", got, err)
	}
}

func TestEncryptedFileStableSentinelAndPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	store := openFileStore(t, root, "namespace", bytes.Repeat([]byte{4}, 32))
	ctx := context.Background()
	key := []byte("binary/\x00/key")
	first, err := store.Put(ctx, key, []byte("one"), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := store.recordNames(key)
	lockPath := filepath.Join(store.nsPath, names.lock)
	lockBefore, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(ctx, key, []byte("two"), &first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, key, second.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, key, []byte("three"), nil); err != nil {
		t.Fatal(err)
	}
	lockAfter, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatalf("stable lock changed: %v", err)
	}
	for _, path := range []string{root, store.nsPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("directory %s mode=%v err=%v", filepath.Base(path), info.Mode().Perm(), err)
		}
	}
	for _, path := range []string{lockPath, filepath.Join(store.nsPath, names.data)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("file %s mode=%v err=%v", filepath.Base(path), info.Mode().Perm(), err)
		}
	}
}

func TestEncryptedFileContextAwareLock(t *testing.T) {
	store := openFileStore(t, filepath.Join(t.TempDir(), "credentials"), "namespace", bytes.Repeat([]byte{5}, 32))
	key := []byte("key")
	if _, err := store.Put(context.Background(), key, []byte("value"), nil); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(store.nsPath, store.recordNames(key).lock), flock.SetPermissions(0o600))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := store.Get(ctx, key); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("contended Get = %v", err)
	}
}

func TestEncryptedFilePrecommitCancellationAndFaultsPreserveOldRecord(t *testing.T) {
	store := openFileStore(t, filepath.Join(t.TempDir(), "credentials"), "namespace", bytes.Repeat([]byte{6}, 32))
	ctx := context.Background()
	key := []byte("key")
	base, err := store.Put(ctx, key, []byte("old"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store.ops.beforeCommit = func(_ context.Context, _ string) error { return context.Canceled }
	if _, err := store.Put(ctx, key, []byte("new"), &base.Version); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v", err)
	}
	got, err := store.Get(ctx, key)
	if err != nil || string(got.Value) != "old" || !got.Version.Equal(base.Version) {
		t.Fatalf("after failed Put = %q, %v", got.Value, err)
	}
	if err := store.Delete(ctx, key, base.Version); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete = %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.nsPath, store.recordNames(key).data)); err != nil {
		t.Fatalf("Delete committed: %v", err)
	}
}

func TestEncryptedFileCancellationAfterPrecommitGateDoesNotMisreportCommit(t *testing.T) {
	putCtx, cancelPut := context.WithCancel(context.Background())
	defer cancelPut()
	store := openFileStore(t, filepath.Join(t.TempDir(), "credentials"), "namespace", bytes.Repeat([]byte{7}, 32))
	key := []byte("key")
	base, err := store.Put(context.Background(), key, []byte("old"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store.ops.beforeCommit = func(context.Context, string) error {
		cancelPut()
		return nil
	}

	replaced, err := store.Put(putCtx, key, []byte("new"), &base.Version)
	if err != nil {
		t.Fatalf("Put after authorized precommit gate = %v", err)
	}
	got, err := store.Get(context.Background(), key)
	if err != nil || string(got.Value) != "new" || !got.Version.Equal(replaced.Version) {
		t.Fatalf("committed Put state = %q, %v", got.Value, err)
	}

	deleteCtx, cancelDelete := context.WithCancel(context.Background())
	defer cancelDelete()
	store.ops.beforeCommit = func(context.Context, string) error {
		cancelDelete()
		return nil
	}
	if err := store.Delete(deleteCtx, key, replaced.Version); err != nil {
		t.Fatalf("Delete after authorized precommit gate = %v", err)
	}
	if _, err := store.Get(context.Background(), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("committed Delete state = %v, want ErrNotFound", err)
	}
}

func TestEncryptedFileFaultBoundaries(t *testing.T) {
	ctx := context.Background()
	key := []byte("key")
	t.Run("entropy and rename failure are precommit", func(t *testing.T) {
		store := openFileStore(t, filepath.Join(t.TempDir(), "credentials"), "namespace", bytes.Repeat([]byte{8}, 32))
		base, err := store.Put(ctx, key, []byte("old"), nil)
		if err != nil {
			t.Fatal(err)
		}
		store.ops.random = failingReader{}
		if _, err := store.Put(ctx, key, []byte("new"), &base.Version); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("entropy failure = %v", err)
		}
		store.ops.random = bytes.NewReader(bytes.Repeat([]byte{1}, 128))
		store.ops.rename = func(*os.Root, string, string) error { return errors.New("injected rename failure") }
		if _, err := store.Put(ctx, key, []byte("new"), &base.Version); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("rename failure = %v", err)
		}
		got, err := store.Get(ctx, key)
		if err != nil || string(got.Value) != "old" || !got.Version.Equal(base.Version) {
			t.Fatalf("precommit failure changed record: %q, %v", got.Value, err)
		}
	})
	t.Run("directory sync failure reports uncertain committed state", func(t *testing.T) {
		store := openFileStore(t, filepath.Join(t.TempDir(), "credentials"), "namespace", bytes.Repeat([]byte{9}, 32))
		base, err := store.Put(ctx, key, []byte("old"), nil)
		if err != nil {
			t.Fatal(err)
		}
		store.ops.syncDir = func(*os.File) error { return errors.New("injected sync failure") }
		if _, err := store.Put(ctx, key, []byte("new"), &base.Version); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("postcommit sync failure = %v", err)
		}
		store.ops.syncDir = syncDirectory
		got, err := store.Get(ctx, key)
		if err != nil || string(got.Value) != "new" {
			t.Fatalf("postcommit record invalid: %q, %v", got.Value, err)
		}
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("injected entropy failure") }

func TestEncryptedFileCloseClearsOwnedKeyAndCopiesInput(t *testing.T) {
	input := bytes.Repeat([]byte{7}, 32)
	store := openFileStore(t, filepath.Join(t.TempDir(), "credentials"), "namespace", input)
	input[0] = 8
	if _, err := store.Put(context.Background(), []byte("key"), []byte("value"), nil); err != nil {
		t.Fatalf("caller key mutation affected store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(store.key, make([]byte, 32)) {
		t.Fatalf("owned key not cleared: %x", store.key)
	}
}

func openFileStore(t *testing.T, root, namespace string, key []byte) *EncryptedFileStore {
	t.Helper()
	store, err := NewEncryptedFile(root, namespace, key)
	if err != nil {
		t.Fatalf("NewEncryptedFile: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
