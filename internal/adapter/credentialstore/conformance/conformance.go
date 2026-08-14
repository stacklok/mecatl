// Package conformance provides the reusable contract tests for mutable
// credential stores.
//
// Importing testing from this non-test package is intentional: backend tests call
// Run with a factory that opens namespace-bound handles over one shared logical
// backend. The suite observes the full credentialstore.Store contract, including
// conditional mutation, and does not apply to read-only Reader implementations.
package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

// Factory opens a handle bound to namespace. Repeated calls for the same
// namespace must share the same logical backend.
type Factory func(namespace string) (credentialstore.Store, error)

// Run executes the credential-store contract against factory.
func Run(t *testing.T, factory Factory, wantCaps credentialstore.Capabilities) {
	t.Helper()
	ctx := context.Background()

	open := func(t *testing.T, namespace string) credentialstore.Store {
		t.Helper()
		store, err := factory(namespace)
		if err != nil {
			t.Fatalf("open namespace: %v", err)
		}
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
		return store
	}

	t.Run("namespace validation", func(t *testing.T) {
		invalid := []string{"", " leading", "trailing\u2003", "control\x00", "control\n", string([]byte{0xff})}
		for _, namespace := range invalid {
			store, err := factory(namespace)
			if store != nil {
				_ = store.Close()
			}
			if !errors.Is(err, credentialstore.ErrInvalidNamespace) {
				t.Errorf("Open(%q) = %v, want ErrInvalidNamespace", namespace, err)
			}
		}
		tooLong := string(bytes.Repeat([]byte{'n'}, credentialstore.MaxNamespaceBytes+1))
		if _, err := factory(tooLong); !errors.Is(err, credentialstore.ErrInvalidNamespace) {
			t.Fatalf("oversized namespace = %v, want ErrInvalidNamespace", err)
		}
		if store, err := factory(string(bytes.Repeat([]byte{'n'}, credentialstore.MaxNamespaceBytes))); err != nil {
			t.Fatalf("max-size namespace: %v", err)
		} else {
			_ = store.Close()
		}
	})

	t.Run("missing and key validation", func(t *testing.T) {
		store := open(t, "missing-and-keys")
		if _, err := store.Get(ctx, []byte("missing")); !errors.Is(err, credentialstore.ErrNotFound) {
			t.Fatalf("Get missing = %v, want ErrNotFound", err)
		}
		if err := store.Delete(ctx, []byte("missing"), credentialstore.Version{}); !errors.Is(err, credentialstore.ErrNotFound) {
			t.Fatalf("Delete missing = %v, want ErrNotFound", err)
		}
		for _, key := range [][]byte{nil, {}} {
			if _, err := store.Get(ctx, key); !errors.Is(err, credentialstore.ErrInvalidKey) {
				t.Errorf("Get empty key = %v, want ErrInvalidKey", err)
			}
		}
		oversized := bytes.Repeat([]byte{'k'}, credentialstore.MaxRecordKeyBytes+1)
		if _, err := store.Put(ctx, oversized, nil, nil); !errors.Is(err, credentialstore.ErrTooLarge) {
			t.Fatalf("Put oversized key = %v, want ErrTooLarge", err)
		}
		binaryKey := make([]byte, 256)
		for i := range binaryKey {
			binaryKey[i] = byte(i)
		}
		if _, err := store.Put(ctx, binaryKey, []byte("binary key"), nil); err != nil {
			t.Fatalf("Put arbitrary binary key: %v", err)
		}
	})

	t.Run("opaque values round trip", func(t *testing.T) {
		values := [][]byte{nil, {}, {0, 1, 0xff, 0xfe}, allBytes()}
		for i, value := range values {
			store := open(t, fmt.Sprintf("opaque-values-%d", i))
			key := []byte("key")
			created, err := store.Put(ctx, key, value, nil)
			if err != nil {
				t.Fatalf("Put value %d: %v", i, err)
			}
			got, err := store.Get(ctx, key)
			if err != nil {
				t.Fatalf("Get value %d: %v", i, err)
			}
			if !bytes.Equal(got.Value, value) || !got.Version.Equal(created.Version) {
				t.Fatalf("round trip %d differs", i)
			}
		}
	})

	t.Run("defensive copies", func(t *testing.T) {
		store := open(t, "defensive-copies")
		key := []byte("key")
		input := []byte("secret")
		if _, err := store.Put(ctx, key, input, nil); err != nil {
			t.Fatalf("Put: %v", err)
		}
		input[0] = 'X'
		first, err := store.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get #1: %v", err)
		}
		if string(first.Value) != "secret" {
			t.Fatalf("stored value aliased input: %q", first.Value)
		}
		first.Value[0] = 'Y'
		second, err := store.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get #2: %v", err)
		}
		if string(second.Value) != "secret" {
			t.Fatalf("stored value aliased output: %q", second.Value)
		}
	})

	t.Run("value size bound", func(t *testing.T) {
		store := open(t, "value-size")
		key := []byte("key")
		atLimit := bytes.Repeat([]byte{'v'}, credentialstore.MaxValueBytes)
		record, err := store.Put(ctx, key, atLimit, nil)
		if err != nil {
			t.Fatalf("Put exact limit: %v", err)
		}
		over := append(atLimit, 'x')
		if _, err := store.Put(ctx, key, over, &record.Version); !errors.Is(err, credentialstore.ErrTooLarge) {
			t.Fatalf("Put over limit = %v, want ErrTooLarge", err)
		}
		got, err := store.Get(ctx, key)
		if err != nil || !bytes.Equal(got.Value, atLimit) || !got.Version.Equal(record.Version) {
			t.Fatalf("oversized Put mutated record: record=%v err=%v", got.Version.Equal(record.Version), err)
		}
	})

	t.Run("conditional put", func(t *testing.T) {
		store := open(t, "conditional-put")
		key := []byte("key")
		created, err := store.Put(ctx, key, []byte("one"), nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := store.Put(ctx, key, []byte("duplicate"), nil); !errors.Is(err, credentialstore.ErrConflict) {
			t.Fatalf("duplicate create = %v, want ErrConflict", err)
		}
		replaced, err := store.Put(ctx, key, []byte("two"), &created.Version)
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
		if created.Version.Equal(replaced.Version) {
			t.Fatal("replace reused version")
		}
		for name, expected := range map[string]credentialstore.Version{
			"stale": created.Version,
			"zero":  {},
		} {
			if _, err := store.Put(ctx, key, []byte(name), &expected); !errors.Is(err, credentialstore.ErrConflict) {
				t.Errorf("%s replace = %v, want ErrConflict", name, err)
			}
		}
		foreign, err := store.Put(ctx, []byte("foreign"), nil, nil)
		if err != nil {
			t.Fatalf("create foreign: %v", err)
		}
		if _, err := store.Put(ctx, key, []byte("foreign-version"), &foreign.Version); !errors.Is(err, credentialstore.ErrConflict) {
			t.Fatalf("foreign-version replace = %v, want ErrConflict", err)
		}
	})

	t.Run("conditional delete and ABA", func(t *testing.T) {
		store := open(t, "delete-aba")
		key := []byte("key")
		first, err := store.Put(ctx, key, []byte("same"), nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		identical, err := store.Put(ctx, key, []byte("same"), &first.Version)
		if err != nil {
			t.Fatalf("identical replace: %v", err)
		}
		if identical.Version.Equal(first.Version) {
			t.Fatal("identical replace reused version")
		}
		for name, expected := range map[string]credentialstore.Version{"stale": first.Version, "zero": {}} {
			if err := store.Delete(ctx, key, expected); !errors.Is(err, credentialstore.ErrConflict) {
				t.Errorf("%s Delete = %v, want ErrConflict", name, err)
			}
		}
		if err := store.Delete(ctx, key, identical.Version); err != nil {
			t.Fatalf("Delete current: %v", err)
		}
		if err := store.Delete(ctx, key, identical.Version); !errors.Is(err, credentialstore.ErrNotFound) {
			t.Fatalf("second Delete = %v, want ErrNotFound", err)
		}
		recreated, err := store.Put(ctx, key, []byte("same"), nil)
		if err != nil {
			t.Fatalf("recreate: %v", err)
		}
		if recreated.Version.Equal(first.Version) || recreated.Version.Equal(identical.Version) {
			t.Fatal("delete/recreate reused an earlier version")
		}
	})

	t.Run("namespace and handle isolation", func(t *testing.T) {
		a := open(t, "namespace-a")
		a2 := open(t, "namespace-a")
		b := open(t, "namespace-b")
		key := []byte("same-key")
		record, err := a.Put(ctx, key, []byte("a"), nil)
		if err != nil {
			t.Fatalf("Put namespace a: %v", err)
		}
		if got, err := a2.Get(ctx, key); err != nil || string(got.Value) != "a" {
			t.Fatalf("second handle Get = %q, %v", got.Value, err)
		}
		if _, err := b.Get(ctx, key); !errors.Is(err, credentialstore.ErrNotFound) {
			t.Fatalf("namespace b fallback = %v, want ErrNotFound", err)
		}
		if _, err := b.Put(ctx, key, []byte("b"), &record.Version); !errors.Is(err, credentialstore.ErrNotFound) {
			t.Fatalf("replace absent namespace b = %v, want ErrNotFound", err)
		}
		if _, err := b.Put(ctx, key, []byte("b"), nil); err != nil {
			t.Fatalf("create namespace b: %v", err)
		}
	})

	t.Run("one concurrent CAS winner", func(t *testing.T) {
		store := open(t, "concurrent-cas")
		key := []byte("key")
		base, err := store.Put(ctx, key, []byte("base"), nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		const contenders = 16
		start := make(chan struct{})
		errs := make(chan error, contenders)
		var wg sync.WaitGroup
		for i := range contenders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := store.Put(ctx, key, []byte(fmt.Sprintf("winner-%d", i)), &base.Version)
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		success, conflicts := 0, 0
		for err := range errs {
			switch {
			case err == nil:
				success++
			case errors.Is(err, credentialstore.ErrConflict):
				conflicts++
			default:
				t.Errorf("concurrent Put: %v", err)
			}
		}
		if success != 1 || conflicts != contenders-1 {
			t.Fatalf("success=%d conflicts=%d, want 1/%d", success, conflicts, contenders-1)
		}
		got, err := store.Get(ctx, key)
		if err != nil || !bytes.HasPrefix(got.Value, []byte("winner-")) {
			t.Fatalf("final value = %q, %v", got.Value, err)
		}
	})

	t.Run("pre-canceled operations do not mutate", func(t *testing.T) {
		store := open(t, "canceled")
		key := []byte("key")
		base, err := store.Put(ctx, key, []byte("base"), nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		deadline, deadlineCancel := context.WithDeadline(ctx, time.Unix(0, 0))
		defer deadlineCancel()
		for name, testCtx := range map[string]struct {
			ctx  context.Context
			want error
		}{
			"canceled": {ctx: canceled, want: context.Canceled},
			"deadline": {ctx: deadline, want: context.DeadlineExceeded},
		} {
			if _, err := store.Get(testCtx.ctx, key); !errors.Is(err, testCtx.want) {
				t.Errorf("%s Get = %v, want %v", name, err, testCtx.want)
			}
			if _, err := store.Put(testCtx.ctx, key, []byte("changed"), &base.Version); !errors.Is(err, testCtx.want) {
				t.Errorf("%s Put = %v, want %v", name, err, testCtx.want)
			}
			if err := store.Delete(testCtx.ctx, key, base.Version); !errors.Is(err, testCtx.want) {
				t.Errorf("%s Delete = %v, want %v", name, err, testCtx.want)
			}
		}
		got, err := store.Get(ctx, key)
		if err != nil || string(got.Value) != "base" || !got.Version.Equal(base.Version) {
			t.Fatalf("canceled operation mutated record: value=%q err=%v", got.Value, err)
		}
	})

	t.Run("close is per-handle and idempotent", func(t *testing.T) {
		first := open(t, "close")
		sibling := open(t, "close")
		if err := first.Close(); err != nil {
			t.Fatalf("Close #1: %v", err)
		}
		if err := first.Close(); err != nil {
			t.Fatalf("Close #2: %v", err)
		}
		key := []byte("key")
		for name, call := range map[string]func() error{
			"Get":    func() error { _, err := first.Get(ctx, key); return err },
			"Put":    func() error { _, err := first.Put(ctx, key, nil, nil); return err },
			"Delete": func() error { return first.Delete(ctx, key, credentialstore.Version{}) },
		} {
			if err := call(); !errors.Is(err, credentialstore.ErrClosed) {
				t.Errorf("post-close %s = %v, want ErrClosed", name, err)
			}
		}
		if _, err := sibling.Put(ctx, key, []byte("live"), nil); err != nil {
			t.Fatalf("sibling Put after Close: %v", err)
		}
	})

	t.Run("capabilities", func(t *testing.T) {
		store := open(t, "capabilities")
		if got := store.Capabilities(); got != wantCaps {
			t.Fatalf("Capabilities = %+v, want %+v", got, wantCaps)
		}
	})
}

func allBytes() []byte {
	value := make([]byte, 256)
	for i := range value {
		value[i] = byte(i)
	}
	return value
}
