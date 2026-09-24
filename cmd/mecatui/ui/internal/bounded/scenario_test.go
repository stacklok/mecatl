package bounded

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMecatuiBoundedScrollCursor_Scenario1_RespectsWidthAndHeight(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy Policy
	}{{"wrap", Wrap}, {"clip", Clip}} {
		t.Run(tc.name, func(t *testing.T) {
			var viewport Viewport
			viewport.SetGeometry(8, 2, 2, tc.policy)
			viewportView := viewport.View([]string{"\x1b[31malpha界 beta-gamma\x1b[0m", "second physical line"})
			rows := viewportView.Rows
			if len(rows) == 0 || len(rows) > 2 {
				t.Fatalf("rendered %d physical lines, want 1..2", len(rows))
			}
			for i, row := range rows {
				if got := 2 + ansi.StringWidth(ansi.Strip(row)); got > 8 {
					t.Errorf("row %d width with gutter = %d, want <= 8: %q", i, got, ansi.Strip(row))
				}
			}
		})
	}

	t.Run("wrap preserves styled wide graphemes", func(t *testing.T) {
		const content = "a界🙂e\u0301Z"
		var viewport Viewport
		viewport.SetGeometry(4, 10, 2, Wrap)
		viewportView := viewport.View([]string{"\x1b[31m" + content + "\x1b[0m"})
		rows := viewportView.Rows
		var rebuilt strings.Builder
		for i, row := range rows {
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

	for _, policy := range []Policy{Wrap, Clip} {
		for _, width := range []int{2, 1, 0, -1} {
			var viewport Viewport
			viewport.SetGeometry(width, 2, 2, policy)
			if view := viewport.View([]string{"content"}); len(view.Rows) != 0 {
				t.Errorf("policy %d width %d with gutter 2 rendered %d rows, want empty", policy, width, len(view.Rows))
			}
		}
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_MultilinePagingTargets(t *testing.T) {
	list := new(List)
	list.SetGeometry(20, 3, 1, Clip)
	list.SetItems([]ListItem{{ID: "zero", Text: "zero-a\nzero-b"}, {ID: "one", Text: "one-a\none-b"}, {ID: "two", Text: "two"}, {ID: "three", Text: "three-a\nthree-b"}})

	list.Move(PageDown)
	if list.Cursor() != 2 || list.CursorID() != "two" {
		t.Fatalf("Page Down cursor = (%d,%q), want first item after old window (2,two)", list.Cursor(), list.CursorID())
	}
	list.Move(PageUp)
	if list.Cursor() != 0 || list.CursorID() != "zero" {
		t.Fatalf("Page Up cursor = (%d,%q), want first item in preceding window (0,zero)", list.Cursor(), list.CursorID())
	}
	list.Move(LineDown)
	if list.Cursor() != 1 || list.Offset() != 1 {
		t.Fatalf("Down = cursor %d offset %d, want logical item 1 minimally revealed at offset 1", list.Cursor(), list.Offset())
	}
	list.Move(LineUp)
	if list.Cursor() != 0 || list.Offset() != 0 {
		t.Fatalf("Up = cursor %d offset %d, want item 0 minimally revealed", list.Cursor(), list.Offset())
	}
	list.Move(End)
	if list.Cursor() != 3 || list.CursorID() != "three" {
		t.Fatalf("End cursor = (%d,%q), want last item", list.Cursor(), list.CursorID())
	}
	list.Move(Top)
	if list.Cursor() != 0 || list.CursorID() != "zero" {
		t.Fatalf("Top cursor = (%d,%q), want first item", list.Cursor(), list.CursorID())
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_OversizedCursorItemReachable(t *testing.T) {
	list := new(List)
	list.SetGeometry(5, 2, 1, Wrap)
	list.SetItems([]ListItem{{ID: "large", Text: "\x1b[31mabcdefghijklmnopqr\x1b[0m"}, {ID: "next", Text: "next"}})

	for page, want := range []string{"abcdef", "ghijkl", "mnopqr"} {
		view := list.View()
		if len(view.Rows) != 2 || list.Offset() != page*2 {
			t.Fatalf("forward page %d view/offset = %#v/%d", page, view.Rows, list.Offset())
		}
		var content strings.Builder
		for rowIndex, row := range view.Rows {
			if !row.Selected || row.ID != "large" || row.GutterCells != 1 || row.StatusCells != [2]string{} || row.CursorMarker != (rowIndex == 0) {
				t.Errorf("forward page %d row %d metadata = %#v", page, rowIndex, row)
			}
			if !strings.Contains(row.Text, "\x1b[31m") || !strings.HasSuffix(row.Text, "\x1b[0m") {
				t.Errorf("forward page %d row %d loses ANSI style: %q", page, rowIndex, row.Text)
			}
			content.WriteString(ansi.Strip(row.Text))
		}
		if got := content.String(); got != want {
			t.Fatalf("forward page %d content = %q, want %q", page, got, want)
		}
		if page < 2 {
			list.Move(PageDown)
		}
	}
	list.Move(PageDown)
	if list.CursorID() != "next" {
		t.Fatalf("Page Down after final segment selected %q, want next", list.CursorID())
	}
	for page, want := range []string{"mnopqr", "ghijkl", "abcdef"} {
		list.Move(PageUp)
		if list.CursorID() != "large" {
			t.Fatalf("reverse page %d selected %q, want large", page, list.CursorID())
		}
		var content strings.Builder
		for _, row := range list.View().Rows {
			content.WriteString(ansi.Strip(row.Text))
		}
		if got := content.String(); got != want {
			t.Fatalf("reverse page %d content = %q, want %q", page, got, want)
		}
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_ClampsContentAndDegenerateBounds(t *testing.T) {
	var viewport Viewport
	viewport.SetGeometry(12, 2, 0, Clip)
	viewport.Move(LineDown, 5)
	viewport.Move(PageDown, 5)
	if viewport.Offset() != 3 {
		t.Fatalf("browsing offset = %d, want clamped 3", viewport.Offset())
	}
	if view := viewport.View([]string{"only"}); viewport.Offset() != 0 || len(view.Rows) != 1 {
		t.Fatalf("content shrink left stale offset/view: offset=%d rows=%#v", viewport.Offset(), view.Rows)
	}

	list := new(List)
	list.SetGeometry(12, 2, 1, Clip)
	list.SetItems([]ListItem{{ID: "a", Text: "a0\na1"}, {ID: "b", Text: "b0\nb1"}, {ID: "c", Text: "c"}})
	list.SetCursor(1)
	cursor, id := list.Cursor(), list.CursorID()
	list.Scroll(LineDown)
	if list.Cursor() != cursor || list.CursorID() != id || list.Offset() == 0 {
		t.Fatalf("independent viewport scroll changed selection or did not scroll: cursor=%d id=%q offset=%d", list.Cursor(), list.CursorID(), list.Offset())
	}
	list.Move(LineDown)
	if list.CursorID() != "c" {
		t.Fatalf("cursor movement after independent scroll selected %q, want c", list.CursorID())
	}

	for _, bounds := range [][2]int{{2, 2}, {0, 2}, {-1, 2}, {12, 0}, {12, -1}} {
		list.SetGeometry(bounds[0], bounds[1], 1, Wrap)
		if view := list.View(); len(view.Rows) != 0 || view.Above != 0 || view.Below != 0 {
			t.Errorf("bounds %v rendered %#v, want empty", bounds, view)
		}
	}

	indicators := new(List)
	indicators.SetGeometry(12, 2, 1, Clip)
	indicators.SetItems([]ListItem{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}, {ID: "c", Text: "c"}})
	indicators.Scroll(LineDown)
	view := indicators.ViewWithIndicators(2, false)
	if len(view.Rows) < 1 {
		t.Fatalf("two-row viewport was consumed entirely by indicators: %#v", view)
	}
	chrome := 0
	if view.Above > 0 {
		chrome++
	}
	if view.Below > 0 {
		chrome++
	}
	if len(view.Rows)+chrome > 2 {
		t.Fatalf("content plus indicators use %d rows, want <= 2: %#v", len(view.Rows)+chrome, view)
	}

	// A short tail is more useful as content than as a second overflow line.
	tail := new(List)
	tail.SetGeometry(12, 4, 1, Clip)
	tail.SetItems([]ListItem{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}, {ID: "c", Text: "c"}, {ID: "d", Text: "d"}, {ID: "e", Text: "e"}, {ID: "f", Text: "f"}})
	tail.Scroll(LineDown)
	view = tail.ViewWithIndicators(4, false)
	if len(view.Rows) != 3 || view.Rows[2].ID != "d" || view.Below != 0 {
		t.Fatalf("small below tail consumed content or retained indicator: %#v", view)
	}

	forward := new(List)
	forward.SetGeometry(12, 2, 1, Clip)
	forwardItems := make([]ListItem, 10)
	for i := range forwardItems {
		forwardItems[i] = ListItem{ID: string(rune('a' + i)), Text: string(rune('a' + i))}
	}
	forward.SetItems(forwardItems)
	forward.Scroll(LineDown)
	view = forward.ViewWithIndicators(2, false)
	if view.Above != 0 || view.Below <= overflowIndicatorThreshold {
		t.Fatalf("single chrome row did not prioritize forward overflow: %#v", view)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_RefreshPreservesSemanticAnchors(t *testing.T) {
	list := new(List)
	list.SetGeometry(20, 2, 1, Clip)
	list.SetItems([]ListItem{{ID: "a", Text: "a0\na1"}, {ID: "b", Text: "b0\nb1\nb2"}, {ID: "c", Text: "c0"}})
	list.SetCursor(1)
	list.Move(PageDown)
	list.SetItems([]ListItem{{ID: "x", Text: "x0"}, {ID: "b", Text: "b0\nb1\nb2\nb3"}, {ID: "a", Text: "a0\na1\na2"}, {ID: "c", Text: "c0"}})
	if list.CursorID() != "b" || list.Cursor() != 1 {
		t.Fatalf("refresh lost selected stable ID: cursor=(%d,%q)", list.Cursor(), list.CursorID())
	}
	if top := list.View().Rows[0]; top.ID != "b" || top.ItemLine != 2 {
		t.Fatalf("refresh top anchor = {%q,%d}, want {b,2}", top.ID, top.ItemLine)
	}
	list.SetItems([]ListItem{{ID: "x", Text: "x0"}, {ID: "d", Text: "d0"}, {ID: "a", Text: "a0\na1\na2"}, {ID: "c", Text: "c0"}})
	if list.CursorID() != "d" || list.Cursor() != 1 {
		t.Fatalf("missing selected ID fallback = (%d,%q), want prior index replacement (1,d)", list.Cursor(), list.CursorID())
	}
	// Selection adopted d when b disappeared. A later b refresh must not resurrect
	// the historical selection and snap the cursor back.
	list.SetItems([]ListItem{{ID: "b", Text: "b0"}, {ID: "d", Text: "d0"}, {ID: "a", Text: "a0"}, {ID: "c", Text: "c0"}})
	if list.CursorID() != "d" || list.Cursor() != 1 {
		t.Fatalf("restored former ID stole adopted selection = (%d,%q), want (1,d)", list.Cursor(), list.CursorID())
	}

	// Losing only the top semantic anchor preserves the selected ID while falling
	// back to the old physical offset, clamped against the refreshed layout.
	list = new(List)
	list.SetGeometry(20, 2, 1, Clip)
	list.SetItems([]ListItem{{ID: "a", Text: "a0\na1"}, {ID: "b", Text: "b0\nb1"}, {ID: "c", Text: "c0\nc1"}})
	list.SetCursor(1)
	list.Scroll(LineDown)
	list.Scroll(LineDown)
	if top := list.View().Rows[0]; top.ID != "c" || list.CursorID() != "b" {
		t.Fatalf("top/selection setup = {%q,%q}, want {c,b}", top.ID, list.CursorID())
	}
	list.SetItems([]ListItem{{ID: "a", Text: "a0\na1"}, {ID: "b", Text: "b0\nb1"}})
	if list.CursorID() != "b" {
		t.Fatalf("missing top anchor lost selected ID: %q", list.CursorID())
	}
	if got, want := list.Offset(), 2; got != want {
		t.Fatalf("missing top anchor physical offset = %d, want clamped fallback %d", got, want)
	}
	if top := list.View().Rows[0]; top.ID != "b" || top.ItemLine != 0 {
		t.Fatalf("missing top anchor first visible row = {%q,%d}, want {b,0}", top.ID, top.ItemLine)
	}
}
