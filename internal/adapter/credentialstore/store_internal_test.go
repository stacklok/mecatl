package credentialstore

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
)

func TestFrameFieldsPreservesOpaqueBoundaries(t *testing.T) {
	left := frameFields("domain", []byte("a"), []byte("bc"))
	right := frameFields("domain", []byte("ab"), []byte("c"))
	if bytes.Equal(left, right) {
		t.Fatal("length-prefixed field boundaries aliased")
	}
	binary := frameFields("domain", []byte{0, '/', 0xff})
	if bytes.Equal(left, binary) {
		t.Fatal("opaque binary field aliased text fields")
	}
}

func TestValidateEncryptionKeyRequiresExactly32Bytes(t *testing.T) {
	for _, size := range []int{0, 31, 33} {
		if err := validateEncryptionKey(make([]byte, size)); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("validateEncryptionKey(%d bytes) = %v, want ErrInvalidKey", size, err)
		}
	}
	if err := validateEncryptionKey(make([]byte, 32)); err != nil {
		t.Fatalf("validateEncryptionKey(32 bytes): %v", err)
	}
}

func TestMemoryVersionSurvivesHandleLifecycleWithoutABA(t *testing.T) {
	backend := NewMemoryBackend()
	firstHandle, err := backend.Open("reopen")
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	first, err := firstHandle.Put(context.Background(), []byte("key"), []byte("same"), nil)
	if err != nil {
		t.Fatalf("Put #1: %v", err)
	}
	if err := firstHandle.Delete(context.Background(), []byte("key"), first.Version); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := firstHandle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	secondHandle, err := backend.Open("reopen")
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	t.Cleanup(func() { _ = secondHandle.Close() })
	second, err := secondHandle.Put(context.Background(), []byte("key"), []byte("same"), nil)
	if err != nil {
		t.Fatalf("Put #2: %v", err)
	}
	if second.Version.Equal(first.Version) {
		t.Fatal("delete, close, reopen, and recreate reused a stale version")
	}
}

func TestMemoryVersionsRejectIndependentBackendTokens(t *testing.T) {
	firstBackend := NewMemoryBackend()
	secondBackend := NewMemoryBackend()
	firstStore, err := firstBackend.Open("shared")
	if err != nil {
		t.Fatalf("Open first: %v", err)
	}
	secondStore, err := secondBackend.Open("shared")
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	first, err := firstStore.Put(context.Background(), []byte("key"), []byte("first"), nil)
	if err != nil {
		t.Fatalf("Put first: %v", err)
	}
	second, err := secondStore.Put(context.Background(), []byte("key"), []byte("second"), nil)
	if err != nil {
		t.Fatalf("Put second: %v", err)
	}
	if first.Version.Equal(second.Version) {
		t.Fatal("independent backends minted equal versions")
	}
	if _, err := secondStore.Put(context.Background(), []byte("key"), []byte("foreign"), &first.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("Put with foreign version = %v, want ErrConflict", err)
	}
}

func TestMemoryConcurrentClose(t *testing.T) {
	backend := NewMemoryBackend()
	store, err := backend.Open("concurrent-close")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const operations = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range operations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, putErr := store.Put(context.Background(), []byte{byte(i + 1)}, nil, nil)
			if putErr != nil && !errors.Is(putErr, ErrClosed) {
				t.Errorf("Put concurrent with Close: %v", putErr)
			}
		}()
	}
	close(start)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
	if _, err := store.Get(context.Background(), []byte("key")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get after concurrent Close = %v, want ErrClosed", err)
	}
}
