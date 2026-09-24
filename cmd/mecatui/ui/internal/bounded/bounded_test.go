package bounded

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestBoundedScrollCursorRespectsWidthAndHeight(t *testing.T) {
	for _, policy := range []Policy{Wrap, Clip} {
		viewport := new(Viewport)
		viewport.SetGeometry(8, 2, 2, policy)
		viewportView := viewport.View([]string{"\x1b[31malpha界 beta-gamma\x1b[0m"})
		rows := viewportView.Rows
		if len(rows) == 0 || len(rows) > 2 {
			t.Fatalf("rendered %d rows", len(rows))
		}
		for _, row := range rows {
			if 2+ansi.StringWidth(ansi.Strip(row)) > 8 {
				t.Fatalf("wide row %q", row)
			}
		}
	}
}

// TestWrapDropsWideGraphemesThatCannotFit verifies ANSI state does not cause an
// over-wide grapheme to escape a narrow viewport, while widths that can hold it
// retain the original content.
func TestWrapDropsWideGraphemesThatCannotFit(t *testing.T) {
	for _, tc := range []struct {
		width int
		want  string
	}{
		{width: 1, want: "AB"},
		{width: 2, want: "A界B"},
	} {
		t.Run("width="+strconv.Itoa(tc.width), func(t *testing.T) {
			viewport := new(Viewport)
			viewport.SetGeometry(tc.width, 8, 0, Wrap)
			view := viewport.View([]string{"\x1b[31mA界B\x1b[0m"})
			for _, row := range view.Rows {
				if width := ansi.StringWidth(row); width > tc.width {
					t.Fatalf("wide grapheme escaped %d-cell row: width=%d row=%q", tc.width, width, row)
				}
			}
			if got := ansi.Strip(strings.Join(view.Rows, "")); got != tc.want {
				t.Fatalf("wrapped content = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListSetItemsOwnsStorageAndPreservesAnchors(t *testing.T) {
	items := []ListItem{{ID: "a", Text: "a0\na1"}, {ID: "b", Text: "b0\nb1\nb2"}, {ID: "c", Text: "c0"}}
	list := new(List)
	list.SetGeometry(20, 2, 1, Clip)
	list.SetItems(items)
	list.SetCursor(1)
	items[1] = ListItem{ID: "changed", Text: "changed"}
	if got := list.CursorID(); got != "b" {
		t.Fatalf("SetItems retained caller storage: cursor ID %q", got)
	}
	viewAfterMutation := list.View()
	if len(viewAfterMutation.Rows) < 2 || viewAfterMutation.Rows[1].ID != "b" || ansi.Strip(viewAfterMutation.Rows[1].Text) != "b0" {
		t.Fatalf("SetItems retained caller storage in rendered rows: %#v", viewAfterMutation.Rows)
	}
	list.SetItems([]ListItem{{ID: "x", Text: "x"}, {ID: "b", Text: "b0\nb1\nb2\nb3"}, {ID: "a", Text: "a0\na1\na2"}, {ID: "c", Text: "c"}})
	if got := list.CursorID(); got != "b" {
		t.Fatalf("selected ID = %q, want b", got)
	}
	view := list.View()
	if len(view.Rows) == 0 || view.Rows[0].ID != "a" || view.Rows[0].ItemLine != 1 {
		t.Fatalf("top anchor = %#v", view.Rows)
	}
}

func TestListStatusCellsAreBoundedAndCopied(t *testing.T) {
	list := new(List)
	list.SetGeometry(20, 4, 3, Clip)
	items := []ListItem{
		{ID: "a", Text: "row", StatusCells: [2]string{"★", "too wide"}},
		{ID: "b", Text: "bidi", StatusCells: [2]string{"\u2066", "\u202e"}},
		{ID: "c", Text: "multiline", StatusCells: [2]string{"★\n", "★\r\n"}},
		{ID: "d", Text: "ansi", StatusCells: [2]string{"\x1b[31m★\x1b[0m", "\x9b31m★\x9b0m"}},
	}
	list.SetItems(items)
	items[0].StatusCells[0] = "!"
	row := list.View().Rows[0]
	if row.GutterCells != 3 || row.StatusCells != [2]string{"★", ""} {
		t.Fatalf("status metadata = %#v, want one valid marker in a three-cell gutter", row)
	}
	for _, id := range []string{"b", "c", "d"} {
		for _, row = range list.View().Rows {
			if row.ID == id {
				break
			}
		}
		if row.StatusCells != [2]string{} {
			t.Fatalf("unsafe status markers for %s were accepted: %#v", id, row.StatusCells)
		}
	}
}

func TestListIndicatorAdjustedPagingUsesVisibleHeight(t *testing.T) {
	list := new(List)
	list.SetGeometry(20, 4, 1, Clip)
	list.SetItems([]ListItem{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}, {ID: "c", Text: "c"}, {ID: "d", Text: "d"}, {ID: "e", Text: "e"}})
	list.Scroll(LineDown)
	view := list.ViewWithIndicators(4, false)
	// A one-row forward tail does not consume a second chrome row. The retained
	// above indicator still leaves the list on its three-row visible page.
	if view.Above == 0 || view.Below != 0 {
		t.Fatalf("indicator-adjusted view = %+v, want only the above indicator for a short forward tail", view)
	}
	list.Move(PageDown)
	if got := list.CursorID(); got != "e" {
		t.Fatalf("page down cursor = %q, want e with the short-tail visible height", got)
	}
	list.Move(PageUp)
	if got := list.CursorID(); got != "a" {
		t.Fatalf("page up cursor = %q, want a at the start of the immediately preceding short-tail page", got)
	}
	list.SetCursor(4)
	view = list.ViewWithIndicators(4, true)
	if len(view.Rows) == 0 || view.Rows[len(view.Rows)-1].ID != "e" {
		t.Fatalf("reveal after indicator reservation hid cursor: %+v", view.Rows)
	}
	if strings.TrimSpace(list.View().Rows[0].Text) == "" {
		t.Fatal("empty list view")
	}
}

func TestListPageDownThenPageUpReturnsToImmediatelyPrecedingPage(t *testing.T) {
	list := new(List)
	list.SetGeometry(20, 3, 1, Clip)
	items := make([]ListItem, 10)
	for i := range items {
		items[i] = ListItem{ID: string(rune('a' + i)), Text: string(rune('a' + i))}
	}
	list.SetItems(items)

	list.Move(PageDown)
	list.Move(PageDown)
	if got := list.CursorID(); got != "g" {
		t.Fatalf("second page down selected %q, want g", got)
	}
	list.Move(PageUp)
	if got := list.CursorID(); got != "d" {
		t.Fatalf("page up selected %q, want immediately preceding page at d", got)
	}
	list.Move(PageUp)
	if got := list.CursorID(); got != "a" {
		t.Fatalf("second page up selected %q, want first page at a", got)
	}
}

func TestViewportViewProjectsCallerProvidedLinesWithoutStoringContent(t *testing.T) {
	var viewport Viewport
	viewport.SetGeometry(20, 1, 0, Clip)
	first := viewport.View([]string{"first", "second"})
	if got, want := first.Rows, []string{"first\x1b[0m"}; !slices.Equal(got, want) || first.Above != 0 || first.Below != 1 {
		t.Fatalf("first projection = %#v, want first caller line with below count", first)
	}

	viewport.Move(End, 2)
	second := viewport.View([]string{"replacement", "current"})
	if got, want := second.Rows, []string{"current\x1b[0m"}; !slices.Equal(got, want) || second.Above != 1 || second.Below != 0 {
		t.Fatalf("stateful projection = %#v, want current caller line at retained offset", second)
	}
}
