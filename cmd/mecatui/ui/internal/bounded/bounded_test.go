package bounded

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestBoundedScrollCursorRespectsWidthAndHeight(t *testing.T) {
	for _, policy := range []Policy{Wrap, Clip} {
		viewport := new(Viewport)
		viewport.SetGeometry(8, 2, 2, policy)
		rows, _, _ := viewport.View([]string{"\x1b[31malpha界 beta-gamma\x1b[0m"})
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
	items := []Item{{ID: "a", Text: "a0\na1"}, {ID: "b", Text: "b0\nb1\nb2"}, {ID: "c", Text: "c0"}}
	list := new(List)
	list.SetGeometry(20, 2, 2, Clip)
	list.SetItems(items)
	list.SetCursor(1)
	items[1] = Item{ID: "changed", Text: "changed"}
	if got := list.CursorID(); got != "b" {
		t.Fatalf("SetItems retained caller storage: cursor ID %q", got)
	}
	viewAfterMutation := list.View()
	if len(viewAfterMutation.Rows) < 2 || viewAfterMutation.Rows[1].ID != "b" || ansi.Strip(viewAfterMutation.Rows[1].Text) != "b0" {
		t.Fatalf("SetItems retained caller storage in rendered rows: %#v", viewAfterMutation.Rows)
	}
	list.SetItems([]Item{{ID: "x", Text: "x"}, {ID: "b", Text: "b0\nb1\nb2\nb3"}, {ID: "a", Text: "a0\na1\na2"}, {ID: "c", Text: "c"}})
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
	list.SetItems([]Item{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}, {ID: "c", Text: "c"}, {ID: "d", Text: "d"}, {ID: "e", Text: "e"}})
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
