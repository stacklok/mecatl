package bounded

import (
	"slices"
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

func TestListSetItemsOwnsStorageAndPreservesAnchors(t *testing.T) {
	items := []ListItem{{ID: "a", Text: "a0\na1"}, {ID: "b", Text: "b0\nb1\nb2"}, {ID: "c", Text: "c0"}}
	list := new(List)
	list.SetGeometry(20, 2, 2, Clip)
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

func TestListIndicatorAdjustedPagingUsesVisibleHeight(t *testing.T) {
	list := new(List)
	list.SetGeometry(20, 4, 2, Clip)
	list.SetItems([]ListItem{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}, {ID: "c", Text: "c"}, {ID: "d", Text: "d"}, {ID: "e", Text: "e"}})
	list.Scroll(LineDown)
	list.ViewWithIndicators(4, false)
	list.Move(PageDown)
	if got := list.CursorID(); got != "d" {
		t.Fatalf("page down cursor = %q, want d with indicator-adjusted height", got)
	}
	if strings.TrimSpace(list.View().Rows[0].Text) == "" {
		t.Fatal("empty list view")
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
