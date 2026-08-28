package jsonlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestSidecarKindsIsFamilyOrderWithoutSnapshot pins the relationship both vars'
// doc comments assert but nothing else enforces: sidecarKinds must be exactly
// familyOrder minus the trailing snapshot. A fourth file kind added to one and
// not the other compiles, passes every other test, and then silently orphans a
// file (Delete skips it, so List can never see it again) or strands one
// (migrateLegacyFamily leaves it behind in the legacy namespace). It also pins
// snapshot-last, which is otherwise asserted only in prose.
func TestSidecarKindsIsFamilyOrderWithoutSnapshot(t *testing.T) {
	want := append(slices.Clone(sidecarKinds), kindSnapshot)
	if !slices.Equal(familyOrder, want) {
		t.Fatalf("familyOrder = %v, want sidecarKinds + kindSnapshot = %v", familyOrder, want)
	}
}

// TestSessionTokenIsInjectiveAndAlphabetSafe pins what the token must actually
// guarantee: filename-safe, and distinct ids never share a stem. It deliberately
// does NOT assert reversibility — the stem is not decodable, and nothing needs it
// to be (scanSnapshotDir re-encodes forward instead).
func TestSessionTokenIsInjectiveAndAlphabetSafe(t *testing.T) {
	ids := []session.SessionID{
		"a/b",
		"a_b",
		"percent%id",
		`back\slash`,
		"雪だるま☃",
		"",
		// Same first 40 sanitized runes, differing only in the tail: the prefix
		// truncates identically, so ONLY the hash suffix keeps these apart.
		session.SessionID(strings.Repeat("x", 60) + "-alpha"),
		session.SessionID(strings.Repeat("x", 60) + "-beta"),
	}
	valid := regexp.MustCompile(`^sid-v1-[-_A-Za-z0-9]+$`)
	seen := make(map[string]session.SessionID, len(ids))
	for _, id := range ids {
		token := encodeSessionToken(id)
		if !valid.MatchString(token) {
			t.Errorf("encodeSessionToken(%q) = %q, contains a filename-unsafe byte", id, token)
		}
		if prior, ok := seen[token]; ok {
			t.Fatalf("token collision: %q and %q both encoded as %q", prior, id, token)
		}
		seen[token] = id
	}
	if encodeSessionToken("a/b") == encodeSessionToken("a_b") {
		t.Fatal("formerly-colliding ids a/b and a_b still collide")
	}
}

// TestSessionTokenLengthIsBoundedForAnyID is the regression pin for the ceiling
// this codec replaced. The old Raw-base64 token inflated 4/3 with no cap, so an
// id past 175 bytes produced a filename over NAME_MAX and every filesystem call
// returned ENAMETOOLONG. The property to hold is a CONSTANT bound, so assert the
// bound rather than the arithmetic.
func TestSessionTokenLengthIsBoundedForAnyID(t *testing.T) {
	const nameMax = 255
	for _, n := range []int{0, 1, 40, 175, 176, 241, 1024, 100_000} {
		id := session.SessionID(strings.Repeat("界", n)) // 3 bytes per rune
		longest := len(encodeSessionToken(id)) + len(sessionFileSuffix)
		if longest > nameMax {
			t.Fatalf("id of %d runes -> filename of %d bytes, over NAME_MAX %d", n, longest, nameMax)
		}
		// 7 (prefix) + 40 (capped readable half) + 1 + 32 (hash) + 14 (suffix).
		// Short ids come out shorter; nothing may ever come out longer.
		if longest > 94 {
			t.Errorf("id of %d runes -> filename of %d bytes, want at most 94", n, longest)
		}
	}
}

func TestScheduleNamingRemainsLegacy(t *testing.T) {
	if got := safeFilePart("a/b"); got != "a_b" {
		t.Fatalf("safeFilePart(a/b) = %q, want legacy a_b", got)
	}
	if got := safeFilePart("a/b"); got == encodeSessionToken("a/b") {
		t.Fatal("schedule filename unexpectedly switched to the session codec")
	}
}

func TestCanonicalAndLegacyNamespacesAreDisjoint(t *testing.T) {
	st := newInternalStore(t)
	canonicalID := session.SessionID("x")
	// A legacy id whose LOSSY stem is byte-identical to canonicalID's canonical
	// token — the physical-name alias this test guards against. DERIVED from the
	// canonical name rather than hardcoded, so the fixture keeps aliasing (and
	// keeps testing something) if the token encoding ever changes again.
	legacyID := session.SessionID(strings.TrimSuffix(
		filepath.Base(st.resolver.canonicalPath(canonicalID, kindSnapshot)), sessionFileSuffix))
	if filepath.Base(st.resolver.canonicalPath(canonicalID, kindSnapshot)) != filepath.Base(st.resolver.legacyPath(legacyID, kindSnapshot)) {
		t.Fatal("fixture no longer exercises the historical physical-name alias")
	}
	if filepath.Dir(st.resolver.canonicalPath(canonicalID, kindSnapshot)) == filepath.Dir(st.resolver.legacyPath(legacyID, kindSnapshot)) {
		t.Fatal("canonical and legacy families share a directory")
	}
	if err := st.Save(context.Background(), session.New(canonicalID, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))); err != nil {
		t.Fatalf("Save canonical: %v", err)
	}
	writeBytes(t, st.resolver.legacyPath(legacyID, kindSnapshot), append(snapshotLine(t, legacyID, "legacy"), '\n'))
	for _, id := range []session.SessionID{canonicalID, legacyID} {
		got, err := st.Load(context.Background(), id)
		if err != nil || got.ID != id {
			t.Fatalf("Load(%q) = %#v, %v", id, got, err)
		}
	}
}

func TestSaveLoadFormerlyCollidingIDsAreIndependent(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, id := range []session.SessionID{"a/b", "a_b"} {
		s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0).UTC())
		s.SetTitle("title:" + string(id))
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save(%q): %v", id, err)
		}
		if err := st.Append(ctx, id, session.Event{Type: session.EvResult, Seq: int64(i + 1)}); err != nil {
			t.Fatalf("Append(%q): %v", id, err)
		}
		st.ToolCall(id, session.NewToolCall("call", "Read", nil), session.NewToolResult("call", string(id)), 0, 0)
	}
	for i, id := range []session.SessionID{"a/b", "a_b"} {
		got, err := st.Load(ctx, id)
		if err != nil {
			t.Fatalf("Load(%q): %v", id, err)
		}
		if got.ID != id || got.Title != "title:"+string(id) {
			t.Errorf("Load(%q) = id %q title %q", id, got.ID, got.Title)
		}
		events := collectEvents(t, st, id)
		if len(events) != 1 || events[0].Seq != int64(i+1) {
			t.Errorf("Read(%q) = %+v", id, events)
		}
		toolBytes, err := os.ReadFile(st.resolver.canonicalPath(id, kindTools))
		if err != nil {
			t.Fatalf("read tools for %q: %v", id, err)
		}
		var record toolCallRecord
		if err := json.Unmarshal(bytes.TrimSpace(toolBytes), &record); err != nil {
			t.Fatalf("decode tools for %q: %v", id, err)
		}
		if record.SessionID != id {
			t.Errorf("tool session id = %q, want %q", record.SessionID, id)
		}
	}
	listed, err := st.List(ctx)
	if err != nil || len(listed) != 2 || listed[0].ID != "a/b" || listed[1].ID != "a_b" {
		t.Fatalf("List = %+v, %v; want ordered ids [a/b a_b]", listed, err)
	}
	meta, err := st.MetaList(ctx)
	if err != nil || len(meta) != 2 ||
		meta[0].ID != "a/b" || meta[0].Title != "title:a/b" ||
		meta[1].ID != "a_b" || meta[1].Title != "title:a_b" {
		t.Fatalf("MetaList = %+v, %v; want exact colliding ids/titles", meta, err)
	}
}

func TestUnicodeSessionIDAcrossAdapterOperations(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("会話/雪だるま☃")
	s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))
	s.SetTitle("unicode")
	if err := st.Save(context.Background(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 4}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	st.ToolCall(id, session.NewToolCall("call", "Read", nil), session.NewToolResult("call", "ok"), 0, 0)
	loaded, err := st.Load(context.Background(), id)
	if err != nil || loaded.ID != id {
		t.Fatalf("Load = %#v, %v", loaded, err)
	}
	listed, err := st.List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != id {
		t.Fatalf("List = %+v, %v", listed, err)
	}
	meta, err := st.MetaList(context.Background())
	if err != nil || len(meta) != 1 || meta[0].ID != id || meta[0].Title != "unicode" {
		t.Fatalf("MetaList = %+v, %v", meta, err)
	}
	if events := collectEvents(t, st, id); len(events) != 1 || events[0].Seq != 4 {
		t.Fatalf("Read = %+v", events)
	}
	var audit toolCallRecord
	auditBytes, err := os.ReadFile(st.resolver.canonicalPath(id, kindTools))
	if err != nil || json.Unmarshal(bytes.TrimSpace(auditBytes), &audit) != nil || audit.SessionID != id {
		t.Fatalf("audit id = %q, read err=%v", audit.SessionID, err)
	}
	if err := st.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, kind := range []sessionKind{kindSnapshot, kindTools, kindEvents} {
		assertMissing(t, st.resolver.canonicalPath(id, kind))
	}
}

func TestInvalidUTF8SessionIDWritesAreRejected(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID(string([]byte{'x', 0xff}))
	s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))
	if err := st.Save(context.Background(), s); err == nil {
		t.Fatal("Save accepted invalid UTF-8 session id")
	}
	if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult}); err == nil {
		t.Fatal("Append accepted invalid UTF-8 session id")
	}
	st.ToolCall(id, session.NewToolCall("call", "Read", nil), session.NewToolResult("call", "ok"), 0, 0)
	for _, kind := range []sessionKind{kindSnapshot, kindTools, kindEvents} {
		assertMissing(t, st.resolver.canonicalPath(id, kind))
	}
}

func TestLoadExactLegacyFallbackIsReadOnly(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("legacy/id")
	line := snapshotLine(t, id, "legacy")
	legacy := st.resolver.legacyPath(id, kindSnapshot)
	legacyEvents := st.resolver.legacyPath(id, kindEvents)
	snapshotBytes := append(line, '\n')
	eventBytes := append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 7}), '\n')
	writeBytes(t, legacy, snapshotBytes)
	writeBytes(t, legacyEvents, eventBytes)

	got, err := st.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != id || got.Title != "legacy" {
		t.Fatalf("Load = id %q title %q", got.ID, got.Title)
	}
	assertBytes(t, legacy, snapshotBytes)
	assertBytes(t, legacyEvents, eventBytes)
	events := collectEvents(t, st, id)
	if len(events) != 1 || events[0].Seq != 7 {
		t.Fatalf("legacy events = %+v, want seq 7", events)
	}
	assertBytes(t, legacyEvents, eventBytes)
	assertMissing(t, st.resolver.canonicalPath(id, kindSnapshot))
	assertMissing(t, st.resolver.canonicalPath(id, kindEvents))
}

func TestLoadLegacyMismatchIsNotFoundAndUntouched(t *testing.T) {
	st := newInternalStore(t)
	requested := session.SessionID("a/b")
	owner := session.SessionID("a_b")
	legacySnapshot := st.resolver.legacyPath(requested, kindSnapshot)
	legacyTools := st.resolver.legacyPath(requested, kindTools)
	legacyEvents := st.resolver.legacyPath(requested, kindEvents)
	snapshotBytes := append(snapshotLine(t, owner, "owner"), '\n')
	toolBytes := []byte("first\nsecond\n")
	eventBytes := append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 99}), '\n')
	writeBytes(t, legacySnapshot, snapshotBytes)
	writeBytes(t, legacyTools, toolBytes)
	writeBytes(t, legacyEvents, eventBytes)

	_, err := st.Load(context.Background(), requested)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load mismatch error = %v, want ErrNotFound", err)
	}
	assertBytes(t, legacySnapshot, snapshotBytes)
	assertBytes(t, legacyTools, toolBytes)
	assertBytes(t, legacyEvents, eventBytes)
	if events := collectEvents(t, st, requested); len(events) != 0 {
		t.Fatalf("mismatched legacy events leaked: %+v", events)
	}
	assertMissing(t, st.resolver.canonicalPath(requested, kindSnapshot))
	assertMissing(t, st.resolver.canonicalPath(requested, kindTools))
	assertMissing(t, st.resolver.canonicalPath(requested, kindEvents))

	if err := st.Append(context.Background(), requested, session.Event{Type: session.EvResult, Seq: 1}); err != nil {
		t.Fatalf("Append mismatched id: %v", err)
	}
	st.ToolCall(requested, session.NewToolCall("new", "Read", nil), session.NewToolResult("new", "ok"), 0, 0)
	assertBytes(t, legacySnapshot, snapshotBytes)
	assertBytes(t, legacyTools, toolBytes)
	assertBytes(t, legacyEvents, eventBytes)
	assertMissing(t, st.resolver.canonicalPath(requested, kindSnapshot))
	if events := collectEvents(t, st, requested); len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("canonical events = %+v, want only seq 1", events)
	}

	requestedSession := session.New(requested, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0).UTC())
	if err := st.Save(context.Background(), requestedSession); err != nil {
		t.Fatalf("Save mismatched id: %v", err)
	}
	assertBytes(t, legacySnapshot, snapshotBytes)
	assertBytes(t, legacyTools, toolBytes)
	assertBytes(t, legacyEvents, eventBytes)
	if _, err := os.Stat(st.resolver.currentSnapshotPath(requested)); err != nil {
		t.Fatalf("current snapshot was not created: %v", err)
	}
}

func TestSaveMigratesMatchingLegacyFamilyPreservingBytes(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("family/id")
	first := snapshotLine(t, id, "first")
	latest := snapshotLine(t, id, "latest")
	family := map[sessionKind][]byte{
		kindSnapshot: bytes.Join([][]byte{first, latest, nil}, []byte("\n")),
		kindTools:    []byte("tool-one\ntool-two\n"),
		kindEvents:   []byte("event-one\n\nevent-two\n"),
	}
	for kind, content := range family {
		writeBytes(t, st.resolver.legacyPath(id, kind), content)
	}

	got, err := st.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Title != "latest" {
		t.Fatalf("loaded title = %q, want latest", got.Title)
	}
	for kind, want := range family {
		assertBytes(t, st.resolver.legacyPath(id, kind), want)
		assertMissing(t, st.resolver.canonicalPath(id, kind))
	}
	if err := st.Save(context.Background(), got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for kind, want := range family {
		assertMissing(t, st.resolver.legacyPath(id, kind))
		assertBytes(t, st.resolver.canonicalPath(id, kind), want)
	}
	if _, err := os.Stat(st.resolver.currentSnapshotPath(id)); err != nil {
		t.Fatalf("current snapshot was not created: %v", err)
	}
}

func TestSidecarWritesMigrateThenAppendInOrder(t *testing.T) {
	cases := []struct {
		name  string
		kind  sessionKind
		old   []byte
		write func(*Store, session.SessionID) error
	}{
		{
			name: "events",
			kind: kindEvents,
			old:  append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 1}), '\n'),
			write: func(st *Store, id session.SessionID) error {
				return st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 2})
			},
		},
		{
			name: "tools",
			kind: kindTools,
			old:  []byte("{\"old\":true}\n"),
			write: func(st *Store, id session.SessionID) error {
				st.ToolCall(id, session.NewToolCall("new", "Read", nil), session.NewToolResult("new", "ok"), 0, 0)
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newInternalStore(t)
			id := session.SessionID("legacy/" + tc.name)
			snapshot := append(snapshotLine(t, id, "legacy"), '\n')
			writeBytes(t, st.resolver.legacyPath(id, kindSnapshot), snapshot)
			writeBytes(t, st.resolver.legacyPath(id, tc.kind), tc.old)

			if err := tc.write(st, id); err != nil {
				t.Fatalf("write: %v", err)
			}
			assertMissing(t, st.resolver.legacyPath(id, kindSnapshot))
			assertMissing(t, st.resolver.legacyPath(id, tc.kind))
			assertBytes(t, st.resolver.canonicalPath(id, kindSnapshot), snapshot)
			got, err := os.ReadFile(st.resolver.canonicalPath(id, tc.kind))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if !bytes.HasPrefix(got, tc.old) || bytes.Count(got, []byte("\n")) != 2 {
				t.Fatalf("sidecar order = %q, want old record then one new record", got)
			}
		})
	}
}

func TestInterruptedMigrationRetriesAfterSidecarAlreadyMoved(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("interrupted/migration")
	snapshot := append(snapshotLine(t, id, "legacy"), '\n')
	movedTools := []byte("already-moved-tools\n")
	legacyEvents := append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 1}), '\n')
	writeBytes(t, st.resolver.legacyPath(id, kindSnapshot), snapshot)
	writeBytes(t, st.resolver.canonicalPath(id, kindTools), movedTools)
	writeBytes(t, st.resolver.legacyPath(id, kindEvents), legacyEvents)

	if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 2}); err != nil {
		t.Fatalf("Append retry: %v", err)
	}
	assertBytes(t, st.resolver.canonicalPath(id, kindTools), movedTools)
	assertBytes(t, st.resolver.canonicalPath(id, kindSnapshot), snapshot)
	assertMissing(t, st.resolver.legacyPath(id, kindSnapshot))
	assertMissing(t, st.resolver.legacyPath(id, kindEvents))
	events := collectEvents(t, st, id)
	if len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("events after migration retry = %+v", events)
	}
}

func TestResolverRootRejectsTraversalDuringMigration(t *testing.T) {
	st := newInternalStore(t)
	root, err := st.resolver.openRoot()
	if err != nil {
		t.Fatalf("openRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	foreign := filepath.Join(filepath.Dir(st.resolver.dir), "foreign-migration-source")
	want := []byte("foreign\n")
	writeBytes(t, foreign, want)
	dst, err := st.resolver.canonicalRelativeName("traversal", kindSnapshot)
	if err != nil {
		t.Fatalf("canonicalRelativeName: %v", err)
	}
	if err := moveLegacyFile((*os.Root).Rename, root, filepath.Join("..", filepath.Base(foreign)), dst); err == nil {
		t.Fatal("moveLegacyFile accepted a source outside the store root")
	}
	assertBytes(t, foreign, want)
	assertMissing(t, st.resolver.canonicalPath("traversal", kindSnapshot))
}

func TestCanonicalSymlinkToForeignDescendantFailsClosed(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("symlinked-canonical")
	foreign := filepath.Join(st.resolver.dir, "foreign.session.jsonl")
	foreignBytes := append(snapshotLine(t, id, "foreign"), '\n')
	writeBytes(t, foreign, foreignBytes)
	writeBytes(t, st.resolver.legacyPath(id, kindSnapshot), append(snapshotLine(t, id, "legacy"), '\n'))

	canonical := st.resolver.canonicalPath(id, kindSnapshot)
	if err := os.Symlink(filepath.Join("..", filepath.Base(foreign)), canonical); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := st.Load(context.Background(), id); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Load through canonical symlink error = %v, want fail-closed infrastructure error", err)
	}
	assertBytes(t, foreign, foreignBytes)
	if info, err := os.Lstat(canonical); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("canonical symlink changed: info=%v err=%v", info, err)
	}
}

func TestMigrationRejectsSymlinkSourceToForeignDescendant(t *testing.T) {
	st := newInternalStore(t)
	root, err := st.resolver.openRoot()
	if err != nil {
		t.Fatalf("openRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	foreign := filepath.Join(st.resolver.dir, "foreign-tools")
	want := []byte("foreign-tools\n")
	writeBytes(t, foreign, want)
	src, err := st.resolver.legacyName("symlink-source", kindTools)
	if err != nil {
		t.Fatalf("legacyName: %v", err)
	}
	if err := os.Symlink(filepath.Base(foreign), filepath.Join(st.resolver.dir, src)); err != nil {
		t.Fatalf("Symlink source: %v", err)
	}
	dst, err := st.resolver.canonicalRelativeName("symlink-source", kindTools)
	if err != nil {
		t.Fatalf("canonicalRelativeName: %v", err)
	}
	if err := moveLegacyFile((*os.Root).Rename, root, src, dst); err == nil {
		t.Fatal("moveLegacyFile accepted a symlink source")
	}
	assertBytes(t, foreign, want)
	assertMissing(t, st.resolver.canonicalPath("symlink-source", kindTools))
}

func TestLoadPresentCorruptCanonicalDoesNotFallback(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("corrupt-new")
	canonicalBytes := []byte("{not-json\n")
	legacyBytes := append(snapshotLine(t, id, "legacy"), '\n')
	canonical := st.resolver.canonicalPath(id, kindSnapshot)
	legacy := st.resolver.legacyPath(id, kindSnapshot)
	writeBytes(t, canonical, canonicalBytes)
	writeBytes(t, legacy, legacyBytes)

	_, err := st.Load(context.Background(), id)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Load error = %v, want canonical corruption", err)
	}
	assertBytes(t, canonical, canonicalBytes)
	assertBytes(t, legacy, legacyBytes)
}

func TestEventReadPresentCorruptCanonicalDoesNotFallback(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("corrupt-canonical-events")
	canonical := st.resolver.canonicalPath(id, kindEvents)
	legacySnapshot := append(snapshotLine(t, id, "legacy"), '\n')
	legacyEvents := append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 9}), '\n')
	writeBytes(t, canonical, []byte("{not-json\n"))
	writeBytes(t, st.resolver.legacyPath(id, kindSnapshot), legacySnapshot)
	writeBytes(t, st.resolver.legacyPath(id, kindEvents), legacyEvents)

	sawErr := false
	for _, err := range st.Read(context.Background(), id) {
		if err != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("corrupt canonical event file did not fail")
	}
	assertBytes(t, st.resolver.legacyPath(id, kindEvents), legacyEvents)
}

func TestCanonicalSnapshotOwnershipMismatchFailsClosed(t *testing.T) {
	st := newInternalStore(t)
	requested := session.SessionID("canonical-a")
	stored := session.SessionID("canonical-b")
	paths := map[sessionKind][]byte{
		kindSnapshot: append(snapshotLine(t, stored, "wrong-owner"), '\n'),
		kindTools:    []byte("existing-tools\n"),
		kindEvents:   append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 8}), '\n'),
	}
	for kind, content := range paths {
		writeBytes(t, st.resolver.canonicalPath(requested, kind), content)
	}
	if _, err := st.Load(context.Background(), requested); err == nil {
		t.Fatal("Load accepted mismatched canonical snapshot")
	}
	if err := st.Save(context.Background(), session.New(requested, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))); err == nil {
		t.Fatal("Save appended to mismatched canonical snapshot")
	}
	if err := st.Append(context.Background(), requested, session.Event{Type: session.EvResult}); err == nil {
		t.Fatal("Append accepted mismatched canonical snapshot")
	}
	st.ToolCall(requested, session.NewToolCall("new", "Read", nil), session.NewToolResult("new", "ok"), 0, 0)
	sawReadErr := false
	for _, err := range st.Read(context.Background(), requested) {
		if err != nil {
			sawReadErr = true
		}
	}
	if !sawReadErr {
		t.Fatal("Read accepted sidecar under mismatched canonical snapshot")
	}
	listed, err := st.List(context.Background())
	if err != nil || len(listed) != 0 {
		t.Fatalf("List = %+v, %v; want mismatch skipped", listed, err)
	}
	meta, err := st.MetaList(context.Background())
	if err != nil || len(meta) != 0 {
		t.Fatalf("MetaList = %+v, %v; want mismatch skipped", meta, err)
	}
	if err := st.Delete(context.Background(), requested); err == nil {
		t.Fatal("Delete removed mismatched canonical family")
	}
	for kind, content := range paths {
		assertBytes(t, st.resolver.canonicalPath(requested, kind), content)
	}
}

func TestLoadCorruptLegacyIsRealErrorAndUntouched(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("corrupt-legacy")
	legacy := st.resolver.legacyPath(id, kindSnapshot)
	want := []byte("{not-json\n")
	writeBytes(t, legacy, want)

	_, err := st.Load(context.Background(), id)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Load error = %v, want legacy corruption", err)
	}
	assertBytes(t, legacy, want)
	assertMissing(t, st.resolver.canonicalPath(id, kindSnapshot))
}

func TestMetaListRetainsIDWhenMetadataDecodeFails(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("meta-corrupt")
	writeBytes(t, st.resolver.legacyPath(id, kindSnapshot), []byte(`{"id":"meta-corrupt","state":"idle","counters":"bad"}`+"\n"))
	listed, err := st.List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != id {
		t.Fatalf("List = %+v, %v", listed, err)
	}
	meta, err := st.MetaList(context.Background())
	if err != nil || len(meta) != 1 || meta[0].ID != id {
		t.Fatalf("MetaList = %+v, %v", meta, err)
	}
	if meta[0].State != "" || meta[0].Turns != 0 || meta[0].Title != "" || !meta[0].CreatedAt.IsZero() {
		t.Fatalf("corrupt metadata was not zeroed: %+v", meta[0])
	}
}

func TestCanonicalFamilyIsAuthoritativeOverLegacy(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("coexist")
	canonical := append(snapshotLine(t, id, "canonical"), '\n')
	legacy := append(snapshotLine(t, id, "legacy"), '\n')
	canonicalPath := st.resolver.canonicalPath(id, kindSnapshot)
	legacyPath := st.resolver.legacyPath(id, kindSnapshot)
	writeBytes(t, canonicalPath, canonical)
	writeBytes(t, legacyPath, legacy)
	canonicalEvents := append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 1}), '\n')
	legacyEvents := append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 2}), '\n')
	writeBytes(t, st.resolver.canonicalPath(id, kindEvents), canonicalEvents)
	writeBytes(t, st.resolver.legacyPath(id, kindEvents), legacyEvents)
	old := time.Unix(10, 0)
	newer := time.Unix(20, 0)
	if err := os.Chtimes(canonicalPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(legacyPath, newer, newer); err != nil {
		t.Fatal(err)
	}

	got, err := st.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Title != "canonical" {
		t.Fatalf("loaded title = %q, want canonical", got.Title)
	}
	assertBytes(t, canonicalPath, canonical)
	assertBytes(t, legacyPath, legacy)
	if events := collectEvents(t, st, id); len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("authoritative events = %+v, want canonical seq 1", events)
	}
	listed, err := st.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != id || !listed[0].ModifiedAt.Equal(old) {
		t.Fatalf("List = %+v, want one canonical row at %v", listed, old)
	}
	meta, err := st.MetaList(context.Background())
	if err != nil {
		t.Fatalf("MetaList: %v", err)
	}
	if len(meta) != 1 || meta[0].ID != id || meta[0].Title != "canonical" || !meta[0].ModifiedAt.Equal(old) {
		t.Fatalf("MetaList = %+v, want canonical row", meta)
	}
}

func TestCanonicalSnapshotSuppressesLegacyEventFallback(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("canonical-without-events")
	writeBytes(t, st.resolver.canonicalPath(id, kindSnapshot), append(snapshotLine(t, id, "canonical"), '\n'))
	legacyEvents := append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 9}), '\n')
	writeBytes(t, st.resolver.legacyPath(id, kindSnapshot), append(snapshotLine(t, id, "legacy"), '\n'))
	writeBytes(t, st.resolver.legacyPath(id, kindEvents), legacyEvents)

	if events := collectEvents(t, st, id); len(events) != 0 {
		t.Fatalf("legacy events resurfaced beside an authoritative canonical snapshot: %+v", events)
	}
	assertBytes(t, st.resolver.legacyPath(id, kindEvents), legacyEvents)
}

func TestDeletePreflightsLegacyBeforeRemovingCanonical(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("delete-preflight")
	canonical := map[sessionKind][]byte{
		kindSnapshot: append(snapshotLine(t, id, "canonical"), '\n'),
		kindTools:    []byte("canonical-tools\n"),
		kindEvents:   []byte("canonical-events\n"),
	}
	for kind, content := range canonical {
		writeBytes(t, st.resolver.canonicalPath(id, kind), content)
	}
	legacyCorrupt := []byte("{not-json\n")
	writeBytes(t, st.resolver.legacyPath(id, kindSnapshot), legacyCorrupt)

	if err := st.Delete(context.Background(), id); err == nil {
		t.Fatal("Delete succeeded despite corrupt legacy ownership record")
	}
	for kind, content := range canonical {
		assertBytes(t, st.resolver.canonicalPath(id, kind), content)
	}
	assertBytes(t, st.resolver.legacyPath(id, kindSnapshot), legacyCorrupt)
}

func TestDeleteLegacyMismatchIsUntouched(t *testing.T) {
	st := newInternalStore(t)
	requested := session.SessionID("a/b")
	owner := session.SessionID("a_b")
	legacy := map[sessionKind][]byte{
		kindSnapshot: append(snapshotLine(t, owner, "owner"), '\n'),
		kindTools:    []byte("owner-tools\n"),
		kindEvents:   []byte("owner-events\n"),
	}
	for kind, content := range legacy {
		writeBytes(t, st.resolver.legacyPath(requested, kind), content)
	}
	if err := st.Append(context.Background(), requested, session.Event{Type: session.EvResult, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	st.ToolCall(requested, session.NewToolCall("call", "Read", nil), session.NewToolResult("call", "ok"), 0, 0)

	if err := st.Delete(context.Background(), requested); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for kind, content := range legacy {
		assertBytes(t, st.resolver.legacyPath(requested, kind), content)
		assertMissing(t, st.resolver.canonicalPath(requested, kind))
	}
}

func TestDeleteRemovesCanonicalAndMatchingLegacyFamilies(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("coexisting-delete")
	for _, kind := range []sessionKind{kindSnapshot, kindTools, kindEvents} {
		canonical := []byte("canonical\n")
		legacy := []byte("legacy\n")
		if kind == kindSnapshot {
			canonical = append(snapshotLine(t, id, "canonical"), '\n')
			legacy = append(snapshotLine(t, id, "legacy"), '\n')
		}
		writeBytes(t, st.resolver.canonicalPath(id, kind), canonical)
		writeBytes(t, st.resolver.legacyPath(id, kind), legacy)
	}

	if err := st.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, kind := range []sessionKind{kindSnapshot, kindTools, kindEvents} {
		assertMissing(t, st.resolver.canonicalPath(id, kind))
		assertMissing(t, st.resolver.legacyPath(id, kind))
	}
}

// TestLongLegacyIDMigratesForward is the regression pin for the ceiling from the
// pre-existing-session direction. A 241-byte id was the maximum the old 1:1
// lossy scheme could name, so such families exist in deployed stores. Under the
// base64 token their canonical name exceeded NAME_MAX, so prepareWrite's very
// first stat returned ENAMETOOLONG — not os.IsNotExist, hence a hard error —
// and the session became permanently unwritable on upgrade, migration included.
func TestLongLegacyIDMigratesForward(t *testing.T) {
	for _, n := range []int{175, 176, 241} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			st := newInternalStore(t)
			id := session.SessionID(strings.Repeat("L", n))
			writeBytes(t, st.resolver.legacyPath(id, kindSnapshot), append(snapshotLine(t, id, "legacy"), '\n'))
			writeBytes(t, st.resolver.legacyPath(id, kindEvents),
				append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 1}), '\n'))

			sess := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(2, 0).UTC())
			sess.SetTitle("migrated")
			if err := st.Save(context.Background(), sess); err != nil {
				t.Fatalf("Save of a %d-byte legacy id = %v; want the family migrated forward", n, err)
			}
			loaded, err := st.Load(context.Background(), id)
			if err != nil || loaded.Title != "migrated" {
				t.Fatalf("Load = %v, %v; want the migrated-then-appended snapshot", loaded, err)
			}
			if got := collectEvents(t, st, id); len(got) != 1 {
				t.Fatalf("event log = %+v; want the migrated event", got)
			}
			assertMissing(t, st.resolver.legacyPath(id, kindSnapshot))
			if err := st.Delete(context.Background(), id); err != nil {
				t.Fatalf("Delete of a %d-byte id = %v; want nil", n, err)
			}
		})
	}
}

// TestAppendLineRepairsTornTail pins that a clean file is extended and an
// unterminated EOF fragment is discarded before the next committed record.
func TestAppendLineRepairsTornTail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing string
		want     string
	}{
		{"clean tail", "{\"a\":1}\n", "{\"a\":1}\n{\"b\":2}\n"},
		{"torn tail", "{\"a\":1}\n{\"partial", "{\"a\":1}\n{\"b\":2}\n"},
		{"empty file", "", "{\"b\":2}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newInternalStore(t)
			path := st.resolver.canonicalPath("append-tail", kindEvents)
			if tc.existing != "" {
				writeBytes(t, path, []byte(tc.existing))
			}
			if err := st.appendLine(path, []byte("{\"b\":2}"), appendStrict); err != nil {
				t.Fatalf("appendLine: %v", err)
			}
			assertBytes(t, path, []byte(tc.want))
		})
	}
}

// tornCanonicalFamily writes a canonical family whose snapshot ends in a
// truncated JSON line — what a crash mid-appendLine leaves behind. The line
// BEFORE it is a complete, valid snapshot, which is what makes the whole class
// so unpleasant: the data is fine and only the tail is damaged.
func tornCanonicalFamily(t *testing.T, st *Store, id session.SessionID) (events []byte) {
	t.Helper()
	good := append(snapshotLine(t, id, "before-the-crash"), '\n')
	writeBytes(t, st.resolver.canonicalPath(id, kindSnapshot),
		append(good, []byte(`{"id":"`+string(id)+`","stat`)...))
	events = append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 7}), '\n')
	writeBytes(t, st.resolver.canonicalPath(id, kindEvents), events)
	writeBytes(t, st.resolver.canonicalPath(id, kindTools), []byte("{\"tool\":\"Read\"}\n"))
	return events
}

// TestDeleteTornCanonicalSnapshotIsIdempotentSuccess pins port.PrunableStore's
// idempotence against a torn snapshot. Gating removal on the snapshot PARSING
// made such a session permanently unprunable: every retention sweep re-failed
// identically, and List skips undecodable files so nothing ever surfaced it
// again — an invisible, unreclaimable disk leak from one interrupted write.
func TestDeleteTornCanonicalSnapshotIsIdempotentSuccess(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("torn-delete")
	tornCanonicalFamily(t, st, id)

	if err := st.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete on torn canonical snapshot = %v; want nil (idempotent success)", err)
	}
	for _, kind := range familyOrder {
		assertMissing(t, st.resolver.canonicalPath(id, kind))
	}
	if err := st.Delete(context.Background(), id); err != nil {
		t.Fatalf("second Delete = %v; want nil", err)
	}
}

// TestEventLogReadSurvivesTornCanonicalSnapshot pins that a sidecar does not
// depend on the validity of the snapshot beside it. The durable event log exists
// to survive the crash that tears the snapshot, so coupling the two lost the log
// exactly when it was most needed (ADR 0038 rehydration, ADR 0027 3b approval
// replay both read it back).
func TestEventLogReadSurvivesTornCanonicalSnapshot(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("torn-events")
	tornCanonicalFamily(t, st, id)

	got := collectEvents(t, st, id) // fails the test on any yielded error
	if len(got) != 1 || got[0].Seq != 7 {
		t.Fatalf("Read = %+v; want the one recorded event (seq 7)", got)
	}
}

// TestSaveHealsTornCanonicalSnapshot pins that a torn tail does not brick the
// session for writes. Load is last-line-wins, so appending a fresh snapshot IS
// the repair; refusing the write left the session permanently unwritable while
// the fix was one append away. Baseline Save never read the snapshot at all.
func TestSaveHealsTornCanonicalSnapshot(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("torn-save")
	tornCanonicalFamily(t, st, id)

	sess := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(2, 0).UTC())
	sess.SetTitle("after-the-repair")
	if err := st.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save over torn canonical snapshot = %v; want nil", err)
	}
	loaded, err := st.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load after heal = %v; want the appended snapshot", err)
	}
	if loaded.Title != "after-the-repair" {
		t.Fatalf("Load title = %q; want the freshly appended snapshot", loaded.Title)
	}
}

// TestCorruptLegacySnapshotIsErrorNotNotOurs pins the fail-CLOSED half of the
// legacy ownership proof at the two call sites that had no coverage: the write
// path and the sidecar-read path. An undecodable legacy snapshot means "cannot
// tell", never "not ours" — folding the two together would authorise a
// migration, or a sidecar read, on a family whose ownership was never proven.
func TestCorruptLegacySnapshotIsErrorNotNotOurs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
	}{
		{"undecodable", []byte("{not-json\n")},
		{"empty", nil},
		{"no id field", []byte("{\"state\":\"idle\"}\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newInternalStore(t)
			id := session.SessionID("legacy-corrupt")
			legacySnap := st.resolver.legacyPath(id, kindSnapshot)
			writeBytes(t, legacySnap, tc.content)
			legacyEvents := append(eventRecordLine(t, session.Event{Type: session.EvResult, Seq: 3}), '\n')
			writeBytes(t, st.resolver.legacyPath(id, kindEvents), legacyEvents)

			// prepareWrite, via Save: must refuse and migrate nothing.
			err := st.Save(context.Background(), session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0)))
			if err == nil {
				t.Fatal("Save proceeded on an unprovable legacy family; want an error")
			}
			assertMissing(t, st.resolver.canonicalPath(id, kindSnapshot))
			assertBytes(t, legacySnap, tc.content)

			// readablePath, via Read: must surface the error, not an empty stream.
			sawErr := false
			for _, rErr := range st.Read(context.Background(), id) {
				if rErr != nil {
					sawErr = true
				}
			}
			if !sawErr {
				t.Fatal("Read returned an empty stream for an unprovable legacy family; want an error")
			}
			assertBytes(t, st.resolver.legacyPath(id, kindEvents), legacyEvents)
		})
	}
}

func eventRecordLine(t *testing.T, ev session.Event) []byte {
	t.Helper()
	evJSON, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	line, err := json.Marshal(eventLogRecord{V: eventLogFormat, Ev: evJSON})
	if err != nil {
		t.Fatalf("marshal event record: %v", err)
	}
	return line
}

func collectEvents(t *testing.T, st *Store, id session.SessionID) []session.Event {
	t.Helper()
	var events []session.Event
	for ev, err := range st.Read(context.Background(), id) {
		if err != nil {
			t.Fatalf("Read(%q): %v", id, err)
		}
		events = append(events, ev)
	}
	return events
}

func TestCreateCollisionDoesNotMigrateLegacyFamily(t *testing.T) {
	ctx := context.Background()
	first := newInternalStore(t)
	second, err := New(first.resolver.dir)
	if err != nil {
		t.Fatalf("New(second): %v", err)
	}
	id := session.SessionID("legacy-create-collision")
	legacy := map[sessionKind][]byte{
		kindSnapshot: append(snapshotLine(t, id, "legacy winner"), '\n'),
		kindTools:    []byte("legacy tool\n"),
		kindEvents:   []byte("legacy event\n"),
	}
	for kind, content := range legacy {
		writeBytes(t, first.resolver.legacyPath(id, kind), content)
	}

	loser := session.New(id, session.ModeAccept, "/loser", session.Limits{}, time.Unix(2, 0).UTC())
	if err := second.Create(ctx, loser); !errors.Is(err, port.ErrSessionAlreadyExists) {
		t.Fatalf("Create(collision) = %v, want ErrSessionAlreadyExists", err)
	}
	for kind, content := range legacy {
		assertBytes(t, first.resolver.legacyPath(id, kind), content)
		assertMissing(t, first.resolver.canonicalPath(id, kind))
	}
	assertMissing(t, first.resolver.currentSnapshotPath(id))
}

func newInternalStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return st
}

func snapshotLine(t *testing.T, id session.SessionID, title string) []byte {
	t.Helper()
	s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0).UTC())
	s.SetTitle(title)
	line, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatalf("sessnap.Marshal: %v", err)
	}
	return line
}

func writeBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s bytes changed:\n got %q\nwant %q", path, got, want)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists or stat failed unexpectedly: %v", path, err)
	}
}
