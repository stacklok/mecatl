package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type progressivePageResult struct {
	page client.SessionInventoryPage
	err  error
}

type progressiveSessionPager struct {
	mu      sync.Mutex
	results map[string][]progressivePageResult
	calls   []string
	block   map[string]bool
	called  chan struct{}
}

func (f *progressiveSessionPager) ListSessionPage(ctx context.Context, cursor string) (client.SessionInventoryPage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cursor)
	blocked := f.block[cursor]
	var result progressivePageResult
	if queue := f.results[cursor]; len(queue) > 0 {
		result = queue[0]
		f.results[cursor] = queue[1:]
	}
	f.mu.Unlock()
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	if blocked {
		<-ctx.Done()
		return client.SessionInventoryPage{}, ctx.Err()
	}
	return result.page, result.err
}

func (f *progressiveSessionPager) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func progressiveSessionsModel(pager client.SessionPager) Model {
	m := newTestModelFromDeps(Deps{
		Sessions: pager, Transcript: &fakeSessionTranscriptLoader{}, BrowseSessions: true,
		Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), NoAltScreen: true,
	})
	m.width, m.height = 80, 24
	return m
}

func TestSessionStorageContinuity_Scenario3_FirstPageRendersImmediately(t *testing.T) {
	pager := &progressiveSessionPager{results: map[string][]progressivePageResult{
		"":       {{page: client.SessionInventoryPage{Sessions: []client.SessionListItem{{ID: "first", Title: "first usable chat", Kind: client.SessionKindMain}}, NextCursor: "page-2"}}},
		"page-2": {{page: client.SessionInventoryPage{Sessions: []client.SessionListItem{{ID: "second", Title: "second chat", Kind: client.SessionKindMain}}}}},
	}}
	m := progressiveSessionsModel(pager)
	if ensureActiveSessions(&m).loadState != sessionsInitialLoading {
		t.Fatalf("initial state = %v, want initial loading", ensureActiveSessions(&m).loadState)
	}
	firstCmd := ensureActiveSessions(&m).pageCmd()
	first := firstCmd()
	if pager.callCount() != 1 {
		t.Fatalf("requests before first render = %d, want 1", pager.callCount())
	}
	mm, nextCmd := m.Update(first)
	m = mm.(Model)
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "first usable chat") || !strings.Contains(got, "loading more") {
		t.Fatalf("first page was not immediately usable:\n%s", got)
	}
	if pager.callCount() != 1 {
		t.Fatalf("next page requested before first render: calls=%d", pager.callCount())
	}
	if nextCmd == nil {
		t.Fatal("first page did not schedule its continuation")
	}
	second := nextCmd()
	if pager.callCount() != 2 {
		t.Fatalf("continuation requests = %d, want 2", pager.callCount())
	}
	mm, _ = m.Update(second)
	m = mm.(Model)
	if ensureActiveSessions(&m).loadState != sessionsComplete || len(ensureActiveSessions(&m).sessions) != 2 {
		t.Fatalf("completed state=%v rows=%d", ensureActiveSessions(&m).loadState, len(ensureActiveSessions(&m).sessions))
	}
}

func TestSessionStorageContinuity_Scenario3_IncrementalStateStable(t *testing.T) {
	m := progressiveSessionsModel(&progressiveSessionPager{})
	ensureActiveSessions(&m).tab = tabChildRuns
	ensureActiveSessions(&m).filter.SetValue("needle")
	ensureActiveSessions(&m).sessions = make([]client.SessionListItem, 14)
	for i := range ensureActiveSessions(&m).sessions {
		ensureActiveSessions(&m).sessions[i] = client.SessionListItem{
			ID: "child-" + string(rune('a'+i)), Title: "needle original", Kind: client.SessionKindSubagent,
			ModifiedAt: int64(100 - i),
		}
	}
	ensureActiveSessions(&m).syncFilter()
	ensureActiveSessions(&m).syncList("").SetCursor(12)
	selectedID := ensureActiveSessions(&m).list.CursorID()

	msg := client.SessionInventoryPageMsg{Cursor: "page-2", Page: client.SessionInventoryPage{Sessions: []client.SessionListItem{
		{ID: selectedID, Title: "duplicate must not win", Kind: client.SessionKindSubagent, ModifiedAt: 88},
		{ID: "child-z", Title: "needle C", Kind: client.SessionKindSubagent, ModifiedAt: 1},
	}}}
	mm, _ := m.Update(msg)
	m = mm.(Model)
	if ensureActiveSessions(&m).tab != tabChildRuns || ensureActiveSessions(&m).filter.Value() != "needle" {
		t.Fatalf("tab/query drifted: tab=%v query=%q", ensureActiveSessions(&m).tab, ensureActiveSessions(&m).filter.Value())
	}
	if len(ensureActiveSessions(&m).sessions) != 15 {
		t.Fatalf("deduplicated rows = %d, want 15: %+v", len(ensureActiveSessions(&m).sessions), ensureActiveSessions(&m).sessions)
	}
	if ensureActiveSessions(&m).list.CursorID() != selectedID {
		t.Fatalf("selection drifted to %q", ensureActiveSessions(&m).list.CursorID())
	}
	for _, row := range ensureActiveSessions(&m).sessions {
		if row.ID == selectedID && row.Title != "needle original" {
			t.Fatalf("duplicate changed deterministic first row: %+v", row)
		}
	}
}

func TestSessionStorageContinuity_Scenario3_PartialFailureAndRetry(t *testing.T) {
	laterErr := errors.New("later page failed")
	pager := &progressiveSessionPager{results: map[string][]progressivePageResult{
		"page-2": {{page: client.SessionInventoryPage{Sessions: []client.SessionListItem{{ID: "one"}, {ID: "two"}}}}},
		"":       {{page: client.SessionInventoryPage{Sessions: []client.SessionListItem{{ID: "fresh"}}}}},
	}}
	m := progressiveSessionsModel(pager)
	ensureActiveSessions(&m).sessions = []client.SessionListItem{{ID: "one"}}
	ensureActiveSessions(&m).nextCursor = "page-2"
	ensureActiveSessions(&m).loadState = sessionsLoadingMore

	mm, _ := m.Update(client.SessionInventoryPageMsg{Cursor: "page-2", Err: laterErr})
	m = mm.(Model)
	if ensureActiveSessions(&m).loadState != sessionsLaterPageError || len(ensureActiveSessions(&m).sessions) != 1 {
		t.Fatalf("later failure state=%v rows=%v", ensureActiveSessions(&m).loadState, ensureActiveSessions(&m).sessions)
	}
	mm, retry, handled := m.onOverlayKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	if !handled || retry == nil || ensureActiveSessions(&m).loadState != sessionsLoadingMore {
		t.Fatalf("retry handled=%v cmd=%v state=%v", handled, retry != nil, ensureActiveSessions(&m).loadState)
	}
	mm, _ = m.Update(retry())
	m = mm.(Model)
	if ensureActiveSessions(&m).loadState != sessionsComplete || len(ensureActiveSessions(&m).sessions) != 2 {
		t.Fatalf("retry state=%v rows=%v", ensureActiveSessions(&m).loadState, ensureActiveSessions(&m).sessions)
	}

	ensureActiveSessions(&m).nextCursor = "stale"
	ensureActiveSessions(&m).loadState = sessionsLoadingMore
	mm, restart := m.Update(client.SessionInventoryPageMsg{Cursor: "stale", Err: client.ErrSessionInventoryRestart})
	m = mm.(Model)
	if ensureActiveSessions(&m).loadState != sessionsStaleRestart || len(ensureActiveSessions(&m).sessions) != 2 || restart == nil {
		t.Fatalf("stale restart state=%v rows=%v cmd=%v", ensureActiveSessions(&m).loadState, ensureActiveSessions(&m).sessions, restart != nil)
	}
	mm, _ = m.Update(restart())
	m = mm.(Model)
	if ensureActiveSessions(&m).loadState != sessionsComplete || len(ensureActiveSessions(&m).sessions) != 1 || ensureActiveSessions(&m).sessions[0].ID != "fresh" {
		t.Fatalf("restart result state=%v rows=%v", ensureActiveSessions(&m).loadState, ensureActiveSessions(&m).sessions)
	}
}

func TestSessionStorageContinuity_Scenario3_CancelStopsPagination(t *testing.T) {
	pager := &progressiveSessionPager{block: map[string]bool{"page-2": true}, called: make(chan struct{}, 1)}
	m := progressiveSessionsModel(pager)
	ensureActiveSessions(&m).sessions = []client.SessionListItem{{ID: "visible"}}
	ensureActiveSessions(&m).nextCursor = "page-2"
	ensureActiveSessions(&m).loadState = sessionsLoadingMore
	if ensureActiveSessions(&m).pageCtx == nil {
		_ = ensureActiveSessions(&m).beginPage("")
	}
	cmd := ensureActiveSessions(&m).pageCmd()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	select {
	case <-pager.called:
	case <-time.After(time.Second):
		t.Fatal("page request did not start")
	}
	mm, _, handled := m.onOverlayKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = mm.(Model)
	if !handled || ensureActiveSessions(&m).loadState != sessionsCancelled {
		t.Fatalf("cancel handled=%v state=%v", handled, ensureActiveSessions(&m).loadState)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop the blocked page request")
	}
	if m.sessionID != "" {
		t.Fatalf("cancel rebound session %q", m.sessionID)
	}
	if got := len(ensureActiveSessions(&m).sessions); got != 1 {
		t.Fatalf("cancel removed visible rows: %d", got)
	}
	calls := pager.callCount()
	mm, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if pager.callCount() != calls || m.sessionID != "" {
		t.Fatalf("close requested/rebound: calls %d->%d id=%q", calls, pager.callCount(), m.sessionID)
	}
}
