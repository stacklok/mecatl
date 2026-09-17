package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMecatuiBoundedScrollCursor_Scenario1_RespectsWidthAndHeight(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy boundedWidthPolicy
	}{{"wrap", boundedWrap}, {"clip", boundedClip}} {
		t.Run(tc.name, func(t *testing.T) {
			var viewport boundedViewport
			viewport.setGeometry(8, 2, 2, tc.policy)
			view := viewport.view([]string{"\x1b[31malpha界 beta-gamma\x1b[0m", "second physical line"})
			if len(view.rows) == 0 || len(view.rows) > 2 {
				t.Fatalf("rendered %d physical lines, want 1..2", len(view.rows))
			}
			for i, row := range view.rows {
				if got := 2 + ansi.StringWidth(ansi.Strip(row)); got > 8 {
					t.Errorf("row %d width with gutter = %d, want <= 8: %q", i, got, ansi.Strip(row))
				}
			}
		})
	}

	t.Run("wrap preserves styled wide graphemes", func(t *testing.T) {
		const content = "a界🙂e\u0301Z"
		var viewport boundedViewport
		viewport.setGeometry(4, 10, 2, boundedWrap)
		view := viewport.view([]string{"\x1b[31m" + content + "\x1b[0m"})
		var rebuilt strings.Builder
		for i, row := range view.rows {
			if got := 2 + ansi.StringWidth(ansi.Strip(row)); got > 4 {
				t.Fatalf("row %d width with gutter = %d, want <= 4", i, got)
			}
			if !strings.Contains(row, "\x1b[31m") || !strings.HasSuffix(row, "\x1b[0m") {
				t.Errorf("row %d did not preserve and close ANSI style: %q", i, row)
			}
			rebuilt.WriteString(ansi.Strip(row))
		}
		if got := rebuilt.String(); got != content {
			t.Fatalf("wrapped content = %q, want byte-preserved graphemes %q", got, content)
		}
	})

	for _, policy := range []boundedWidthPolicy{boundedWrap, boundedClip} {
		for _, width := range []int{2, 1, 0, -1} {
			var viewport boundedViewport
			viewport.setGeometry(width, 2, 2, policy)
			if got := viewport.view([]string{"content"}); len(got.rows) != 0 {
				t.Errorf("policy %d width %d with gutter 2 rendered %d rows, want empty", policy, width, len(got.rows))
			}
		}
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_MultilinePagingTargets(t *testing.T) {
	items := []boundedListItem{
		{id: "zero", text: "zero-a\nzero-b"},
		{id: "one", text: "one-a\none-b"},
		{id: "two", text: "two"},
		{id: "three", text: "three-a\nthree-b"},
	}
	var list boundedList
	list.setGeometry(20, 3, 2, boundedClip)
	list.setItems(items)

	list.move(boundedPageDown)
	if list.cursor != 2 || list.cursorID != "two" {
		t.Fatalf("Page Down cursor = (%d,%q), want first item after old window (2,two)", list.cursor, list.cursorID)
	}
	list.move(boundedPageUp)
	if list.cursor != 0 || list.cursorID != "zero" {
		t.Fatalf("Page Up cursor = (%d,%q), want first item in preceding window (0,zero)", list.cursor, list.cursorID)
	}
	list.move(boundedLineDown)
	if list.cursor != 1 || list.viewport.offset != 1 {
		t.Fatalf("Down = cursor %d offset %d, want logical item 1 minimally revealed at offset 1", list.cursor, list.viewport.offset)
	}
	list.move(boundedLineUp)
	if list.cursor != 0 || list.viewport.offset != 0 {
		t.Fatalf("Up = cursor %d offset %d, want item 0 minimally revealed", list.cursor, list.viewport.offset)
	}
	list.move(boundedEnd)
	if list.cursor != 3 || list.cursorID != "three" {
		t.Fatalf("End cursor = (%d,%q), want last item", list.cursor, list.cursorID)
	}
	list.move(boundedTop)
	if list.cursor != 0 || list.cursorID != "zero" {
		t.Fatalf("Top cursor = (%d,%q), want first item", list.cursor, list.cursorID)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_OversizedCursorItemReachable(t *testing.T) {
	items := []boundedListItem{
		{id: "large", text: "\x1b[31mabcdefghijklmnopqr\x1b[0m"},
		{id: "next", text: "next"},
	}
	var list boundedList
	list.setGeometry(5, 2, 2, boundedWrap)
	list.setItems(items)

	wantForward := []string{"abcdef", "ghijkl", "mnopqr"}
	for page, want := range wantForward {
		view := list.view()
		if len(view.rows) != 2 {
			t.Fatalf("forward page %d has %d rows, want 2", page, len(view.rows))
		}
		if list.cursorLine != page*2 || list.viewport.offset != page*2 {
			t.Fatalf("forward page %d cursor line/viewport offset = %d/%d, want %d/%d",
				page, list.cursorLine, list.viewport.offset, page*2, page*2)
		}
		var content strings.Builder
		for rowIndex, row := range view.rows {
			if !row.selected || row.id != "large" || row.gutter != "  " {
				t.Errorf("forward page %d row %d metadata = %#v", page, rowIndex, row)
			}
			if row.cursorMarker != (rowIndex == 0) {
				t.Errorf("forward page %d row %d marker = %v, want %v", page, rowIndex, row.cursorMarker, rowIndex == 0)
			}
			if strings.Contains(row.text, "\x1b[35m") {
				t.Errorf("control applied caller-owned selected style: %q", row.text)
			}
			if !strings.Contains(row.text, "\x1b[31m") || !strings.HasSuffix(row.text, "\x1b[0m") {
				t.Errorf("forward page %d row %d leaks or loses ANSI style: %q", page, rowIndex, row.text)
			}
			prefix := row.gutter
			if row.cursorMarker {
				prefix = "▶ "
			}
			rendered := prefix + row.text
			if row.selected {
				rendered = "\x1b[35m" + rendered + "\x1b[0m"
			}
			if !strings.Contains(rendered, "\x1b[35m") {
				t.Error("caller could not independently apply selected styling")
			}
			content.WriteString(ansi.Strip(row.text))
		}
		if content.String() != want {
			t.Fatalf("forward page %d content = %q, want %q", page, content.String(), want)
		}
		if page+1 < len(wantForward) {
			list.move(boundedPageDown)
			if list.cursorID != "large" {
				t.Fatalf("Page Down advanced before oversized item was exhausted: %q", list.cursorID)
			}
		}
	}

	list.move(boundedPageDown)
	if list.cursorID != "next" {
		t.Fatalf("Page Down after final segment selected %q, want next", list.cursorID)
	}
	for page, want := range []string{"mnopqr", "ghijkl", "abcdef"} {
		list.move(boundedPageUp)
		if list.cursorID != "large" {
			t.Fatalf("reverse page %d selected %q, want large", page, list.cursorID)
		}
		view := list.view()
		var content strings.Builder
		for rowIndex, row := range view.rows {
			if row.cursorMarker != (rowIndex == 0) || row.gutter != "  " {
				t.Errorf("reverse page %d row %d marker/gutter = %v/%q", page, rowIndex, row.cursorMarker, row.gutter)
			}
			content.WriteString(ansi.Strip(row.text))
		}
		if content.String() != want {
			t.Fatalf("reverse page %d content = %q, want %q", page, content.String(), want)
		}
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_ClampsContentAndDegenerateBounds(t *testing.T) {
	var viewport boundedViewport
	viewport.setGeometry(12, 2, 0, boundedClip)
	lines := []string{"zero", "one", "two", "three", "four"}
	viewport.move(boundedLineDown, len(lines))
	viewport.move(boundedPageDown, len(lines))
	if viewport.offset != 3 {
		t.Fatalf("browsing offset = %d, want clamped 3", viewport.offset)
	}
	if got := viewport.view([]string{"only"}); viewport.offset != 0 || len(got.rows) != 1 {
		t.Fatalf("content shrink left stale offset/view: offset=%d view=%#v", viewport.offset, got)
	}

	var list boundedList
	list.setGeometry(12, 2, 2, boundedClip)
	list.setItems([]boundedListItem{{id: "a", text: "a0\na1"}, {id: "b", text: "b0\nb1"}, {id: "c", text: "c"}})
	list.setCursor(1)
	cursor, cursorID, cursorLine := list.cursor, list.cursorID, list.cursorLine
	list.scroll(boundedLineDown)
	if list.cursor != cursor || list.cursorID != cursorID || list.cursorLine != cursorLine {
		t.Fatalf("viewport scroll moved cursor state from (%d,%q,%d) to (%d,%q,%d)",
			cursor, cursorID, cursorLine, list.cursor, list.cursorID, list.cursorLine)
	}
	if list.viewport.offset == 0 {
		t.Fatal("independent viewport scroll did not move physical offset")
	}
	list.move(boundedLineDown)
	if list.cursorID != "c" {
		t.Fatalf("cursor movement after independent scroll selected %q, want c", list.cursorID)
	}

	list.setGeometry(12, 2, 0, boundedClip)
	if got := list.view(); len(got.rows) == 0 || got.rows[0].gutter != "" {
		t.Fatalf("zero-gutter list geometry = %#v, want visible rows with no gutter", got)
	}
	list.viewport.offset = 3
	list.setGeometry(12, 4, 0, boundedClip)
	if list.viewport.offset != 1 || len(list.view().rows) != 4 {
		t.Fatalf("valid geometry growth left a blank page: offset=%d rows=%d, want 1/4", list.viewport.offset, len(list.view().rows))
	}
	list.viewport.offset = 1
	list.setGeometry(12, 1, 0, boundedClip)
	if list.viewport.offset != 1 || len(list.view().rows) != 1 {
		t.Fatalf("valid geometry shrink lost/clobbered viewport: offset=%d rows=%d, want 1/1", list.viewport.offset, len(list.view().rows))
	}
	viewportType := reflect.TypeOf(boundedViewport{})
	for _, name := range []string{"x", "xOffset", "horizontalOffset"} {
		if _, ok := viewportType.FieldByName(name); ok {
			t.Fatalf("boundedViewport exposes forbidden horizontal navigation state %q", name)
		}
	}
	list.setGeometry(2, 2, 2, boundedWrap)
	if got := list.view(); len(got.rows) != 0 || got.above != 0 || got.below != 0 {
		t.Fatalf("width smaller than gutter+content rendered %#v", got)
	}
	for _, bounds := range [][2]int{{0, 2}, {12, 0}, {-1, 2}, {12, -1}} {
		list.setGeometry(bounds[0], bounds[1], 2, boundedWrap)
		if got := list.view(); len(got.rows) != 0 {
			t.Errorf("bounds %v rendered %d rows, want empty", bounds, len(got.rows))
		}
	}

	var indicators boundedList
	indicators.setGeometry(12, 2, 2, boundedClip)
	indicators.setItems([]boundedListItem{{id: "a", text: "a"}, {id: "b", text: "b"}, {id: "c", text: "c"}})
	indicators.viewport.offset = 1
	got := boundedListViewWithIndicators(&indicators, 2, false)
	if len(got.rows) < 1 {
		t.Fatalf("two-row viewport was consumed entirely by indicators: %#v", got)
	}
	chrome := 0
	if got.above > 0 {
		chrome++
	}
	if got.below > 0 {
		chrome++
	}
	if len(got.rows)+chrome > 2 {
		t.Fatalf("content plus indicators use %d rows, want <= 2: %#v", len(got.rows)+chrome, got)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_RefreshPreservesSemanticAnchors(t *testing.T) {
	var list boundedList
	list.setGeometry(20, 2, 2, boundedClip)
	list.setItems([]boundedListItem{
		{id: "a", text: "a0\na1"},
		{id: "b", text: "b0\nb1\nb2"},
		{id: "c", text: "c0"},
	})
	list.setCursor(1) // minimally reveals b with top anchor {a,1}
	list.cursorLine = 2
	if list.viewport.offset != 1 {
		t.Fatalf("initial top offset = %d, want 1", list.viewport.offset)
	}

	list.setItems([]boundedListItem{
		{id: "x", text: "x0"},
		{id: "b", text: "b0\nb1\nb2\nb3"},
		{id: "a", text: "a0\na1\na2"},
		{id: "c", text: "c0"},
	})
	if list.cursorID != "b" || list.cursor != 1 || list.cursorLine != 2 {
		t.Fatalf("refresh lost selected stable ID/line: cursor=(%d,%q,%d), want (1,b,2)", list.cursor, list.cursorID, list.cursorLine)
	}
	if top := list.view().rows[0]; top.id != "a" || top.itemLine != 1 {
		t.Fatalf("refresh top anchor = {%q,%d}, want {a,1}", top.id, top.itemLine)
	}

	list.setItems([]boundedListItem{
		{id: "x", text: "x0"},
		{id: "d", text: "d0"},
		{id: "a", text: "a0\na1\na2"},
		{id: "c", text: "c0"},
	})
	if list.cursorID != "d" || list.cursor != 1 {
		t.Fatalf("missing selected ID fallback = (%d,%q), want prior index replacement (1,d)", list.cursor, list.cursorID)
	}
	list.setItems([]boundedListItem{
		{id: "b", text: "b0\nb1\nb2\nb3"},
		{id: "x", text: "x0"},
		{id: "d", text: "d0"},
		{id: "a", text: "a0\na1\na2"},
		{id: "c", text: "c0"},
	})
	if list.cursorID != "d" || list.cursor != 2 {
		t.Fatalf("reappearing old ID snapped selection back: cursor=(%d,%q), want (2,d)", list.cursor, list.cursorID)
	}
	oldOffset := list.viewport.offset
	list.setItems([]boundedListItem{
		{id: "b", text: "b0\nb1\nb2\nb3"},
		{id: "x", text: "x0"},
		{id: "d", text: "d0"},
		{id: "c", text: "c0"},
	})
	wantOffset := clampScroll(oldOffset, 7, 2)
	if list.viewport.offset != wantOffset {
		t.Fatalf("missing top ID offset = %d, want physical fallback %d", list.viewport.offset, wantOffset)
	}
	if list.cursorID != "d" {
		t.Fatalf("missing top ID disturbed selected ID: %q", list.cursorID)
	}

	var lineClamp boundedList
	lineClamp.setGeometry(20, 2, 2, boundedClip)
	lineClamp.setItems([]boundedListItem{{id: "selected", text: "s0\ns1\ns2"}})
	lineClamp.cursorLine = 2
	lineClamp.setItems([]boundedListItem{{id: "selected", text: "short"}})
	if lineClamp.cursorID != "selected" || lineClamp.cursorLine != 0 {
		t.Fatalf("selected line did not clamp after height shrink: id=%q line=%d", lineClamp.cursorID, lineClamp.cursorLine)
	}
}
