package redisstore

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	miniredisserver "github.com/alicebob/miniredis/v2/server"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type metadataWorkCounts struct {
	loads int
	rows  int
}

type redisCommand struct {
	name string
	args []string
}

type redisCommandSpy struct {
	mu       sync.Mutex
	commands []redisCommand
}

func (s *redisCommandSpy) hook(_ *miniredisserver.Peer, command string, args ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, redisCommand{name: strings.ToUpper(command), args: slices.Clone(args)})
	return false
}

func (s *redisCommandSpy) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = nil
}

func (s *redisCommandSpy) snapshot() []redisCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commands)
}

func TestPageSessionMetadataSeparatesOwnerScopesAndRejectsCursorReuse(t *testing.T) {
	st, _ := newMetadataTestStore(t)
	ctx := context.Background()
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	bob := &session.Principal{Issuer: "https://issuer.example", Subject: "bob"}
	for _, fixture := range []struct {
		id    session.SessionID
		owner *session.Principal
	}{
		{id: "alice-a", owner: alice},
		{id: "bob-a", owner: bob},
		{id: "alice-b", owner: alice},
		{id: "bob-b", owner: bob},
		{id: "alice-c", owner: alice},
	} {
		s := session.New(fixture.id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())
		if err := s.RestoreLabels(fixture.owner, session.Authority{}); err != nil {
			t.Fatalf("RestoreLabels(%q): %v", fixture.id, err)
		}
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save(%q): %v", fixture.id, err)
		}
	}

	aliceRequest := port.SessionMetadataPageRequest{Limit: 2, OwnershipEnforced: true, Owner: alice}
	alicePage, err := st.PageSessionMetadata(ctx, aliceRequest)
	if err != nil {
		t.Fatalf("PageSessionMetadata(alice): %v", err)
	}
	assertOwnerPage(t, alicePage, alice, 3, 2)
	if alicePage.NextCursor == nil {
		t.Fatal("alice page cursor is nil, want continuation")
	}
	aliceRequest.Cursor = alicePage.NextCursor
	aliceSecond, err := st.PageSessionMetadata(ctx, aliceRequest)
	if err != nil {
		t.Fatalf("PageSessionMetadata(alice second): %v", err)
	}
	assertOwnerPage(t, aliceSecond, alice, 3, 1)

	bobRequest := port.SessionMetadataPageRequest{Limit: 10, OwnershipEnforced: true, Owner: bob}
	bobPage, err := st.PageSessionMetadata(ctx, bobRequest)
	if err != nil {
		t.Fatalf("PageSessionMetadata(bob): %v", err)
	}
	assertOwnerPage(t, bobPage, bob, 2, 2)

	assertCursorRestart(t, st, port.SessionMetadataPageRequest{
		Limit: 2, OwnershipEnforced: true, Owner: bob, Cursor: alicePage.NextCursor,
	})
	assertCursorRestart(t, st, port.SessionMetadataPageRequest{
		Limit: 2, Cursor: alicePage.NextCursor,
	})
}

func TestPageSessionMetadataUsesOneBoundedMetadataRangePerPage(t *testing.T) {
	st, mr := newMetadataTestStore(t)
	ctx := context.Background()
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	for _, id := range []session.SessionID{"alice-a", "alice-b", "alice-c", "alice-d"} {
		s := session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())
		if err := s.RestoreLabels(alice, session.Authority{}); err != nil {
			t.Fatalf("RestoreLabels(%q): %v", id, err)
		}
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save(%q): %v", id, err)
		}
	}

	spy := &redisCommandSpy{}
	mr.Server().SetPreHook(spy.hook)
	if err := pageMetadataScript.Load(ctx, st.testClient()).Err(); err != nil {
		t.Fatalf("load page script: %v", err)
	}
	commands := spy.snapshot()
	if len(commands) != 1 || commands[0].name != "SCRIPT" || len(commands[0].args) != 2 || strings.ToUpper(commands[0].args[0]) != "LOAD" {
		t.Fatalf("script-load commands = %#v, want one SCRIPT LOAD", commands)
	}
	assertBoundedMetadataScript(t, commands[0].args[1])

	request := port.SessionMetadataPageRequest{Limit: 2, OwnershipEnforced: true, Owner: alice}
	spy.reset()
	first, err := st.PageSessionMetadata(ctx, request)
	if err != nil {
		t.Fatalf("PageSessionMetadata(first): %v", err)
	}
	assertPageRedisWork(t, spy.snapshot(), request.Limit)
	if first.NextCursor == nil {
		t.Fatal("first page cursor is nil, want continuation")
	}

	request.Cursor = first.NextCursor
	spy.reset()
	second, err := st.PageSessionMetadata(ctx, request)
	if err != nil {
		t.Fatalf("PageSessionMetadata(second): %v", err)
	}
	assertPageRedisWork(t, spy.snapshot(), request.Limit)
	if len(second.Sessions) != 2 {
		t.Fatalf("second page rows = %d, want 2", len(second.Sessions))
	}
}

func TestPageSessionMetadataWorkIsBoundedAndDoesNotLoadSnapshots(t *testing.T) {
	st, _ := newMetadataTestStore(t)
	ctx := context.Background()
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	for i := range 8 {
		s := session.New(session.SessionID("session-"+string(rune('a'+i))), session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())
		if err := s.RestoreLabels(owner, session.Authority{}); err != nil {
			t.Fatalf("RestoreLabels: %v", err)
		}
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	work := &metadataWorkCounts{}
	st.metadataWorkObserver = func(kind metadataWorkKind) {
		switch kind {
		case metadataWorkLoad:
			work.loads++
		case metadataWorkRow:
			work.rows++
		}
	}
	request := port.SessionMetadataPageRequest{Limit: 2, OwnershipEnforced: true, Owner: owner}
	first, err := st.PageSessionMetadata(ctx, request)
	if err != nil {
		t.Fatalf("PageSessionMetadata(first): %v", err)
	}
	if work.loads != 0 || work.rows > request.Limit+1 {
		t.Fatalf("first-page work = loads:%d rows:%d, want zero loads and <= %d rows", work.loads, work.rows, request.Limit+1)
	}
	if len(first.Sessions) != request.Limit || first.NextCursor == nil || first.TotalCount != 8 {
		t.Fatalf("first page = %+v, want 2/8 rows plus cursor", first)
	}

	*work = metadataWorkCounts{}
	request.Cursor = first.NextCursor
	second, err := st.PageSessionMetadata(ctx, request)
	if err != nil {
		t.Fatalf("PageSessionMetadata(second): %v", err)
	}
	if work.loads != 0 || work.rows > request.Limit+1 {
		t.Fatalf("second-page work = loads:%d rows:%d, want zero loads and <= %d rows", work.loads, work.rows, request.Limit+1)
	}
	if len(second.Sessions) != request.Limit || second.Sessions[0].ID == first.Sessions[0].ID || second.Sessions[0].ID == first.Sessions[1].ID {
		t.Fatalf("second page traversed or repeated prior rows: first=%v second=%v", first.Sessions, second.Sessions)
	}
}

func TestMetadataMemberOrderingUsesModifiedDescThenIDAsc(t *testing.T) {
	modifiedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	members := make([]string, 0, 4)
	for _, id := range []session.SessionID{"aa", "b", "a"} {
		member, err := encodeMetadataMember(port.SessionDiscoveryMeta{ID: id, ModifiedAt: modifiedAt})
		if err != nil {
			t.Fatalf("encodeMetadataMember(%q): %v", id, err)
		}
		members = append(members, member)
	}
	newest, err := encodeMetadataMember(port.SessionDiscoveryMeta{ID: "z", ModifiedAt: modifiedAt.Add(time.Second)})
	if err != nil {
		t.Fatalf("encodeMetadataMember(newest): %v", err)
	}
	members = append(members, newest)
	slices.Sort(members)
	for i, want := range []session.SessionID{"z", "a", "aa", "b"} {
		row, err := decodeMetadataMember(members[i])
		if err != nil {
			t.Fatalf("decodeMetadataMember: %v", err)
		}
		if row.ID != want {
			t.Fatalf("ordered row %d = %q, want %q", i, row.ID, want)
		}
	}
}

func TestSaveAndDeleteAdvanceGenerationAndInvalidateCursors(t *testing.T) {
	st, mr := newMetadataTestStore(t)
	ctx := context.Background()
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	sessions := make(map[session.SessionID]*session.Session)
	for _, id := range []session.SessionID{"alice-a", "alice-b"} {
		s := session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())
		if err := s.RestoreLabels(alice, session.Authority{}); err != nil {
			t.Fatalf("RestoreLabels(%q): %v", id, err)
		}
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save(%q): %v", id, err)
		}
		sessions[id] = s
	}
	request := port.SessionMetadataPageRequest{Limit: 1, OwnershipEnforced: true, Owner: alice}
	beforeSave, err := st.PageSessionMetadata(ctx, request)
	if err != nil || beforeSave.NextCursor == nil {
		t.Fatalf("owner page before save = %+v, %v; want cursor", beforeSave, err)
	}
	globalRequest := port.SessionMetadataPageRequest{Limit: 1}
	globalBeforeSave, err := st.PageSessionMetadata(ctx, globalRequest)
	if err != nil || globalBeforeSave.NextCursor == nil {
		t.Fatalf("global page before save = %+v, %v; want cursor", globalBeforeSave, err)
	}
	globalGeneration := metadataGeneration(t, mr, metadataGlobalScope)
	ownerScope := metadataOwnerScope(alice)
	ownerGeneration := metadataGeneration(t, mr, ownerScope)

	if err := st.Save(ctx, sessions["alice-a"]); err != nil {
		t.Fatalf("Save(existing): %v", err)
	}
	assertGeneration(t, mr, metadataGlobalScope, globalGeneration+1)
	assertGeneration(t, mr, ownerScope, ownerGeneration+1)
	assertCursorRestart(t, st, port.SessionMetadataPageRequest{
		Limit: 1, OwnershipEnforced: true, Owner: alice, Cursor: beforeSave.NextCursor,
	})
	globalRequest.Cursor = globalBeforeSave.NextCursor
	assertCursorRestart(t, st, globalRequest)

	afterSave, err := st.PageSessionMetadata(ctx, request)
	if err != nil || afterSave.NextCursor == nil {
		t.Fatalf("owner page before delete = %+v, %v; want cursor", afterSave, err)
	}
	globalRequest.Cursor = nil
	globalAfterSave, err := st.PageSessionMetadata(ctx, globalRequest)
	if err != nil || globalAfterSave.NextCursor == nil {
		t.Fatalf("global page before delete = %+v, %v; want cursor", globalAfterSave, err)
	}
	globalGeneration++
	ownerGeneration++
	if err := st.Delete(ctx, "alice-b"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	assertGeneration(t, mr, metadataGlobalScope, globalGeneration+1)
	assertGeneration(t, mr, ownerScope, ownerGeneration+1)
	assertCursorRestart(t, st, port.SessionMetadataPageRequest{
		Limit: 1, OwnershipEnforced: true, Owner: alice, Cursor: afterSave.NextCursor,
	})
	globalRequest.Cursor = globalAfterSave.NextCursor
	assertCursorRestart(t, st, globalRequest)
}

func TestDeleteRemovesMetadataAndInvalidatesCursor(t *testing.T) {
	st, _ := newMetadataTestStore(t)
	ctx := context.Background()
	for _, id := range []session.SessionID{"delete-a", "delete-b"} {
		if err := st.Save(ctx, session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())); err != nil {
			t.Fatalf("Save(%q): %v", id, err)
		}
	}
	first, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	if err := st.Delete(ctx, first.Sessions[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("stale cursor error = %v, want restart", err)
	}
	page, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
	if err != nil {
		t.Fatalf("PageSessionMetadata(after delete): %v", err)
	}
	if page.TotalCount != 1 || len(page.Sessions) != 1 {
		t.Fatalf("page after delete = %+v, want one row", page)
	}
}

func TestCurrentMetadataMarkerCorruptionFailsConstructor(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.Set(metadataIndexStateKey, "corrupt-current-marker")
	if _, err := New(mr.Addr()); err == nil || !strings.Contains(err.Error(), "unsupported current metadata index state") {
		t.Fatalf("New error = %v, want current marker failure", err)
	}
	if got, err := mr.Get(metadataIndexStateKey); err != nil || got != "corrupt-current-marker" {
		t.Fatalf("current marker was overwritten: %q, %v", got, err)
	}
}

func TestCurrentSessionWithoutMetadataMarkerFailsConstructor(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.HSet(sessionKey("orphan"), fieldBlob, `{}`, fieldMtime, "1")
	if _, err := New(mr.Addr()); err == nil || !strings.Contains(err.Error(), "current session metadata index is missing") {
		t.Fatalf("New error = %v, want missing current index failure", err)
	}
	if mr.Exists(metadataIndexStateKey) {
		t.Fatal("constructor created a marker over corrupt current state")
	}
}

func TestOldStoreWithoutMetadataIndexIsIgnoredUntouched(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	const key = "mecatl:session:legacy"
	mr.HSet(key, fieldBlob, `{}`, fieldMtime, "1")
	before := mr.HGet(key, fieldBlob)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatalf("New with old namespace: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	page, err := st.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 10})
	if err != nil || len(page.Sessions) != 0 {
		t.Fatalf("current metadata page = (%+v, %v), want empty", page, err)
	}
	current := session.New("legacy", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/current", Revision: "current"}, session.Limits{}, time.Now().UTC())
	if err := st.Create(t.Context(), current); err != nil {
		t.Fatalf("Create same id in current namespace: %v", err)
	}
	if after := mr.HGet(key, fieldBlob); after != before {
		t.Fatalf("old metadata changed: before=%q after=%q", before, after)
	}
	if !mr.Exists(metadataIndexStateKey) || !mr.Exists(sessionKey(current.ID)) {
		t.Fatal("current namespace was not initialized")
	}
}

func assertOwnerPage(t *testing.T, page port.SessionMetadataPage, owner *session.Principal, wantTotal, wantRows int) {
	t.Helper()
	if page.TotalCount != wantTotal || len(page.Sessions) != wantRows {
		t.Fatalf("page count = %d/%d, want %d/%d: %+v", len(page.Sessions), page.TotalCount, wantRows, wantTotal, page)
	}
	for _, row := range page.Sessions {
		if !owner.SameIdentity(row.Owner) {
			t.Fatalf("page contains foreign owner on session %q: got %+v, want %+v", row.ID, row.Owner, owner)
		}
	}
}

func assertCursorRestart(t *testing.T, st *Store, request port.SessionMetadataPageRequest) {
	t.Helper()
	page, err := st.PageSessionMetadata(context.Background(), request)
	if err != port.ErrSessionMetadataCursorRestart {
		t.Fatalf("cursor reuse error = %v, want exact restart sentinel", err)
	}
	if len(page.Sessions) != 0 || page.TotalCount != 0 || page.NextCursor != nil {
		t.Fatalf("stale cursor leaked page data: %+v", page)
	}
}

func assertBoundedMetadataScript(t *testing.T, script string) {
	t.Helper()
	upper := strings.ToUpper(script)
	if strings.Count(upper, "ZRANGEBYLEX") != 1 {
		t.Fatalf("metadata page script has %d ordered range operations, want 1", strings.Count(upper, "ZRANGEBYLEX"))
	}
	if !strings.Contains(upper, "'ZRANGEBYLEX', KEYS[1], ARGV[3], '+', 'LIMIT', 0, ARGV[4]") {
		t.Fatal("metadata page range is not bounded by LIMIT 0, requested limit")
	}
	if strings.Contains(upper, "SCAN") || strings.Contains(script, "'blob'") || strings.Contains(script, `"blob"`) {
		t.Fatal("metadata page script scans keys or reads snapshot blobs")
	}
}

func assertPageRedisWork(t *testing.T, commands []redisCommand, limit int) {
	t.Helper()
	var scriptCalls, pageRanges int
	for _, command := range commands {
		switch command.name {
		case "GET":
			if len(command.args) != 1 || command.args[0] != metadataIndexStateKey {
				t.Fatalf("unexpected metadata page GET: %#v", command)
			}
		case "EVALSHA":
			if len(command.args) != 8 || command.args[0] != pageMetadataScript.Hash() {
				t.Fatalf("unexpected metadata page EVALSHA: %#v", command)
			}
			if command.args[len(command.args)-1] != strconv.Itoa(limit+1) {
				t.Fatalf("metadata range count = %q, want limit+1 = %d", command.args[len(command.args)-1], limit+1)
			}
			scriptCalls++
		case "HGET":
			if len(command.args) != 2 || command.args[0] != metadataGenerationKey {
				t.Fatalf("metadata page performed snapshot hash read: %#v", command)
			}
		case "ZCARD":
			if len(command.args) != 1 || !strings.HasPrefix(command.args[0], storeKeyPrefix+"session-metadata:index:") {
				t.Fatalf("unexpected metadata count operation: %#v", command)
			}
		case "ZRANGEBYLEX":
			if len(command.args) != 6 || !strings.HasPrefix(command.args[0], storeKeyPrefix+"session-metadata:index:") ||
				command.args[2] != "+" || strings.ToUpper(command.args[3]) != "LIMIT" ||
				command.args[4] != "0" || command.args[5] != strconv.Itoa(limit+1) {
				t.Fatalf("unbounded or unexpected metadata range: %#v", command)
			}
			pageRanges++
		case "HGETALL", "HMGET", "SCAN", "SSCAN", "HSCAN", "ZSCAN":
			t.Fatalf("metadata page performed forbidden snapshot read/scan: %#v", command)
		default:
			t.Fatalf("unexpected Redis command during metadata page: %#v", command)
		}
	}
	if scriptCalls != 1 || pageRanges != 1 {
		t.Fatalf("metadata page operations = %#v, want one script call and one bounded ordered range", commands)
	}
}

func metadataGeneration(t *testing.T, mr *miniredis.Miniredis, scope string) int {
	t.Helper()
	raw := mr.HGet(metadataGenerationKey, scope)
	generation, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("generation %q = %q: %v", scope, raw, err)
	}
	return generation
}

func assertGeneration(t *testing.T, mr *miniredis.Miniredis, scope string, want int) {
	t.Helper()
	if got := metadataGeneration(t, mr, scope); got != want {
		t.Fatalf("generation %q = %d, want %d", scope, got, want)
	}
}

func newMetadataTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, mr
}
