package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestSoulSnapshotUserSoul proves the snapshot projects a selected user soul's
// content + meta into the proto SoulInfo (present, content, user provenance,
// trusted, hash/size populated).
func TestSoulSnapshotUserSoul(t *testing.T) {
	xdg := t.TempDir()
	fakeSoulEnv(t, xdg)
	writeUserSoul(t, xdg, "You are terse and direct.")

	got := soulSnapshotWith(Config{}, newFakeIO().io())
	if got == nil {
		t.Fatal("a present user soul must yield a non-nil snapshot")
	}
	if !got.GetPresent() {
		t.Error("present = false, want true")
	}
	if got.GetContent() != "You are terse and direct." {
		t.Errorf("content = %q", got.GetContent())
	}
	if got.GetProvenance() != mecatlv1.SoulProvenance_SOUL_PROVENANCE_USER {
		t.Errorf("provenance = %v, want USER", got.GetProvenance())
	}
	if !got.GetTrusted() {
		t.Error("a user soul must be trusted")
	}
	if got.GetSha256() == "" || got.GetSizeBytes() == 0 {
		t.Errorf("hash/size not populated: sha=%q size=%d", got.GetSha256(), got.GetSizeBytes())
	}
}

// TestSoulSnapshotUntrustedProject proves a discovered-but-untrusted project soul
// still yields a (content-less) snapshot so the panel can explain the dropped state.
func TestSoulSnapshotUntrustedProject(t *testing.T) {
	xdg := t.TempDir() // no user soul
	fakeSoulEnv(t, xdg)
	ws := t.TempDir()
	writeProjectSoul(t, ws, "You are a project persona.")

	got := soulSnapshotWith(Config{Workspace: ws}, newFakeIO().io())
	if got == nil {
		t.Fatal("an untrusted project soul should still yield a snapshot (to explain the dropped state)")
	}
	if got.GetPresent() {
		t.Error("present = true, want false (untrusted project soul dropped)")
	}
	if got.GetProvenance() != mecatlv1.SoulProvenance_SOUL_PROVENANCE_PROJECT {
		t.Errorf("provenance = %v, want PROJECT", got.GetProvenance())
	}
	if got.GetTrusted() {
		t.Error("trusted = true, want false (untrusted)")
	}
	if got.GetContent() != "" {
		t.Error("a dropped soul must carry no content")
	}
}

// TestSoulSnapshotNone proves no candidate soul anywhere yields nil (so the cap is
// false and the /soul built-in is gated off).
func TestSoulSnapshotNone(t *testing.T) {
	xdg := t.TempDir() // empty: no user soul
	fakeSoulEnv(t, xdg)

	if got := soulSnapshotWith(Config{}, newFakeIO().io()); got != nil {
		t.Fatalf("no soul present must yield nil, got %+v", got)
	}
}

// TestSoulSnapshotDisabled proves --no-soul yields nil.
func TestSoulSnapshotDisabled(t *testing.T) {
	xdg := t.TempDir()
	fakeSoulEnv(t, xdg)
	writeUserSoul(t, xdg, "ignored when disabled")

	if got := soulSnapshotWith(Config{NoSoul: true}, newFakeIO().io()); got != nil {
		t.Fatalf("--no-soul must yield nil, got %+v", got)
	}
}

// TestUserModelListerNilSafe proves a nil store yields a nil lister (so the cap is
// false and /usermodel is gated off).
func TestUserModelListerNilSafe(t *testing.T) {
	if l := userModelLister(nil); l != nil {
		t.Fatalf("nil store must yield a nil lister, got %v", l)
	}
}

// secretUserModelValue is a distinctive fact VALUE used to prove it never crosses
// the lister seam (only key + description are exposed). If a future field addition
// leaks the value into server.UserModelEntry, the no-leak scan below fails.
const secretUserModelValue = "SECRET-VALUE-DO-NOT-LEAK-7f3a"

// TestUserModelListerMapsIndex proves the lister maps the store's Index (key +
// description, value omitted) into server.UserModelEntry rows, KEY-SORTED, against a
// real temp-dir-backed store — AND that the fact VALUE never crosses the seam
// (SECURITY: only key + description should reach the wire-bound type).
func TestUserModelListerMapsIndex(t *testing.T) {
	store, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("new user-model store: %v", err)
	}
	// Insert OUT OF ORDER so the key-sort is actually exercised (a pre-sorted fixture
	// would pass even if the lister did no sorting).
	rememberProfile(t, context.Background(), store, tool.MemoryEntry{Key: "zeta", Value: secretUserModelValue, Description: "the last fact"})
	rememberProfile(t, context.Background(), store, tool.MemoryEntry{Key: "alpha", Value: secretUserModelValue, Description: "the operator's name"})

	l := userModelLister(store)
	if l == nil {
		t.Fatal("a non-nil store must yield a lister")
	}
	entries, lerr := l.List(context.Background())
	if lerr != nil {
		t.Fatalf("list: %v", lerr)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	// Key-sorted: alpha before zeta, regardless of insertion order.
	if entries[0].Key != "alpha" || entries[1].Key != "zeta" {
		t.Fatalf("entries not key-sorted: %+v", entries)
	}
	if entries[0].Description != "the operator's name" {
		t.Fatalf("entry[0] description = %q", entries[0].Description)
	}

	// SECURITY: the fact VALUE must NOT cross the seam. server.UserModelEntry has no
	// Value field today; pin that — neither a field named "Value" nor the secret bytes
	// in ANY field. A future leak (e.g. adding a Value field populated from the store)
	// then fails this test loudly.
	et := reflect.TypeOf(server.UserModelEntry{})
	if _, ok := et.FieldByName("Value"); ok {
		t.Error("server.UserModelEntry must NOT expose a Value field (the fact value must not cross the seam)")
	}
	for _, e := range entries {
		v := reflect.ValueOf(e)
		for i := 0; i < v.NumField(); i++ {
			if s, ok := v.Field(i).Interface().(string); ok && strings.Contains(s, secretUserModelValue) {
				t.Fatalf("entry leaked the fact VALUE in field %q: %q", et.Field(i).Name, s)
			}
		}
	}
}

// TestUserModelListerIsLive proves the lister reflects the CURRENT store state on
// each call (the documented live-read contract — vs the soul's startup snapshot):
// writing A then B is observed as 1-then-2 entries by successive List calls.
func TestUserModelListerIsLive(t *testing.T) {
	store, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("new user-model store: %v", err)
	}
	l := userModelLister(store)

	rememberProfile(t, context.Background(), store, tool.MemoryEntry{Key: "a", Value: "1", Description: "fact a"})
	got, lerr := l.List(context.Background())
	if lerr != nil {
		t.Fatalf("list after A: %v", lerr)
	}
	if len(got) != 1 {
		t.Fatalf("after writing A, List = %d entries, want 1", len(got))
	}

	rememberProfile(t, context.Background(), store, tool.MemoryEntry{Key: "b", Value: "2", Description: "fact b"})
	got, lerr = l.List(context.Background())
	if lerr != nil {
		t.Fatalf("list after B: %v", lerr)
	}
	if len(got) != 2 {
		t.Fatalf("after writing B, List = %d entries, want 2 (live read, not a snapshot)", len(got))
	}
}
