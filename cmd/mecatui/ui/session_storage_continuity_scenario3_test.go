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
	return New(Deps{
		Sessions: pager, Transcript: &fakeSessionTranscriptLoader{}, BrowseSessions: true,
		Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), NoAltScreen: true,
	})
}

func TestSessionStorageContinuity_Scenario3_FirstPageRendersImmediately(t *testing.T) {
	pager := &progressiveSessionPager{results: map[string][]progressivePageResult{
		"":       {{page: client.SessionInventoryPage{Sessions: []client.SessionListItem{{ID: "first", Title: "first usable chat", Kind: client.SessionKindMain}}, NextCursor: "page-2"}}},
		"page-2": {{page: client.SessionInventoryPage{Sessions: []client.SessionListItem{{ID: "second", Title: "second chat", Kind: client.SessionKindMain}}}}},
	}}
	m := progressiveSessionsModel(pager)
	if m.sessions.loadState != sessionsInitialLoading {
		t.Fatalf("initial state = %v, want initial loading", m.sessions.loadState)
	}
	firstCmd := m.sessionPageCmd()
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
	if m.sessions.loadState != sessionsComplete || len(m.sessions.sessions) != 2 {
		t.Fatalf("completed state=%v rows=%d", m.sessions.loadState, len(m.sessions.sessions))
	}
}

func TestSessionStorageContinuity_Scenario3_IncrementalStateStable(t *testing.T) {
	m := progressiveSessionsModel(&progressiveSessionPager{})
	m.sessions.tab = tabChildRuns
	m.sessions.filter.SetValue("needle")
	m.sessions.sessions = make([]client.SessionListItem, 14)
	for i := range m.sessions.sessions {
		m.sessions.sessions[i] = client.SessionListItem{
			ID: "child-" + string(rune('a'+i)), Title: "needle original", Kind: client.SessionKindSubagent,
			ModifiedAt: int64(100 - i),
		}
	}
	m = m.syncSessionsFilter()
	m.sessions.cursor = 12
	selectedID := m.sessions.filtered[m.sessions.cursor].ID

	msg := client.SessionInventoryPageMsg{Cursor: "page-2", Page: client.SessionInventoryPage{Sessions: []client.SessionListItem{
		{ID: selectedID, Title: "duplicate must not win", Kind: client.SessionKindSubagent, ModifiedAt: 88},
		{ID: "child-z", Title: "needle C", Kind: client.SessionKindSubagent, ModifiedAt: 1},
	}}}
	mm, _, _ := m.updateSessionsMsg(msg)
	m = mm.(Model)
	if m.sessions.tab != tabChildRuns || m.sessions.filter.Value() != "needle" {
		t.Fatalf("tab/query drifted: tab=%v query=%q", m.sessions.tab, m.sessions.filter.Value())
	}
	if len(m.sessions.sessions) != 15 {
		t.Fatalf("deduplicated rows = %d, want 15: %+v", len(m.sessions.sessions), m.sessions.sessions)
	}
	if m.sessions.filtered[m.sessions.cursor].ID != selectedID {
		t.Fatalf("selection drifted to %q", m.sessions.filtered[m.sessions.cursor].ID)
	}
	for _, row := range m.sessions.sessions {
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
	m.sessions.sessions = []client.SessionListItem{{ID: "one"}}
	m.sessions.nextCursor = "page-2"
	m.sessions.loadState = sessionsLoadingMore

	mm, _, _ := m.updateSessionsMsg(client.SessionInventoryPageMsg{Cursor: "page-2", Err: laterErr})
	m = mm.(Model)
	if m.sessions.loadState != sessionsLaterPageError || len(m.sessions.sessions) != 1 {
		t.Fatalf("later failure state=%v rows=%v", m.sessions.loadState, m.sessions.sessions)
	}
	mm, retry, handled := m.onSessionsKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = mm.(Model)
	if !handled || retry == nil || m.sessions.loadState != sessionsLoadingMore {
		t.Fatalf("retry handled=%v cmd=%v state=%v", handled, retry != nil, m.sessions.loadState)
	}
	mm, _, _ = m.updateSessionsMsg(retry())
	m = mm.(Model)
	if m.sessions.loadState != sessionsComplete || len(m.sessions.sessions) != 2 {
		t.Fatalf("retry state=%v rows=%v", m.sessions.loadState, m.sessions.sessions)
	}

	m.sessions.nextCursor = "stale"
	m.sessions.loadState = sessionsLoadingMore
	mm, restart, _ := m.updateSessionsMsg(client.SessionInventoryPageMsg{Cursor: "stale", Err: client.ErrSessionInventoryRestart})
	m = mm.(Model)
	if m.sessions.loadState != sessionsStaleRestart || len(m.sessions.sessions) != 2 || restart == nil {
		t.Fatalf("stale restart state=%v rows=%v cmd=%v", m.sessions.loadState, m.sessions.sessions, restart != nil)
	}
	mm, _, _ = m.updateSessionsMsg(restart())
	m = mm.(Model)
	if m.sessions.loadState != sessionsComplete || len(m.sessions.sessions) != 1 || m.sessions.sessions[0].ID != "fresh" {
		t.Fatalf("restart result state=%v rows=%v", m.sessions.loadState, m.sessions.sessions)
	}
}

func TestSessionStorageContinuity_Scenario3_CancelStopsPagination(t *testing.T) {
	pager := &progressiveSessionPager{block: map[string]bool{"page-2": true}, called: make(chan struct{}, 1)}
	m := progressiveSessionsModel(pager)
	m.sessions.sessions = []client.SessionListItem{{ID: "visible"}}
	m.sessions.nextCursor = "page-2"
	m.sessions.loadState = sessionsLoadingMore
	m = m.ensureSessionPagination()
	cmd := m.sessionPageCmd()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	select {
	case <-pager.called:
	case <-time.After(time.Second):
		t.Fatal("page request did not start")
	}
	mm, _, handled := m.onSessionsKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = mm.(Model)
	if !handled || m.sessions.loadState != sessionsCancelled {
		t.Fatalf("cancel handled=%v state=%v", handled, m.sessions.loadState)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop the blocked page request")
	}
	if m.sessionID != "" {
		t.Fatalf("cancel rebound session %q", m.sessionID)
	}
	if got := len(m.sessions.sessions); got != 1 {
		t.Fatalf("cancel removed visible rows: %d", got)
	}
	calls := pager.callCount()
	mm, _ = m.closeSessions()
	m = mm.(Model)
	if pager.callCount() != calls || m.sessionID != "" {
		t.Fatalf("close requested/rebound: calls %d->%d id=%q", calls, pager.callCount(), m.sessionID)
	}
}
