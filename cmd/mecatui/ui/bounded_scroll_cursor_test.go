package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMecatuiBoundedScrollCursor_Scenario1_RespectsWidthAndHeight(t *testing.T) {
	items := []boundedScrollItem{{
		text:         "\x1b[31malpha界 beta-gamma\x1b[0m\nsecond physical line",
		prefix:       "  ",
		cursorPrefix: "▶ ",
	}}
	for _, tc := range []struct {
		name   string
		policy boundedWidthPolicy
	}{{"wrap", boundedWrap}, {"clip", boundedClip}} {
		t.Run(tc.name, func(t *testing.T) {
			var control boundedScrollCursor
			control.setBounds(8, 2, tc.policy)
			control.setCursorItems(items, 0)
			view := control.view()
			if len(view.rows) > 2 {
				t.Fatalf("rendered %d physical lines, want at most 2", len(view.rows))
			}
			if len(view.rows) == 0 {
				t.Fatal("positive bounds rendered no cursor rows")
			}
			for i, row := range view.rows {
				if got := ansi.StringWidth(ansi.Strip(row.text)); got > 8 {
					t.Errorf("row %d width = %d, want <= 8: %q", i, got, ansi.Strip(row.text))
				}
			}
		})
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_MultilinePagingTargets(t *testing.T) {
	items := []boundedScrollItem{
		{text: "zero-a\nzero-b", prefix: "  ", cursorPrefix: "▶ "},
		{text: "one-a\none-b", prefix: "  ", cursorPrefix: "▶ "},
		{text: "two", prefix: "  ", cursorPrefix: "▶ "},
		{text: "three-a\nthree-b", prefix: "  ", cursorPrefix: "▶ "},
	}
	var control boundedScrollCursor
	control.setBounds(20, 3, boundedClip)
	control.setCursorItems(items, 0)

	control.move(boundedPageDown)
	if got := control.cursor; got != 2 {
		t.Fatalf("Page Down cursor = %d, want first item beginning after the old window: 2", got)
	}
	control.move(boundedPageUp)
	if got := control.cursor; got != 0 {
		t.Fatalf("Page Up cursor = %d, want first item visible in the preceding physical window: 0", got)
	}
	control.move(boundedLineDown)
	if got := control.cursor; got != 1 {
		t.Fatalf("Down cursor = %d, want 1", got)
	}
	control.move(boundedLineUp)
	if got := control.cursor; got != 0 {
		t.Fatalf("Up cursor = %d, want 0", got)
	}
	control.move(boundedEnd)
	if got := control.cursor; got != 3 {
		t.Fatalf("End cursor = %d, want 3", got)
	}
	control.move(boundedTop)
	if got := control.cursor; got != 0 {
		t.Fatalf("Top cursor = %d, want 0", got)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_OversizedCursorItemReachable(t *testing.T) {
	items := []boundedScrollItem{
		{text: "\x1b[31mabcdefghijklmnopqr\x1b[0m", prefix: "  ", cursorPrefix: "▶ "},
		{text: "next", prefix: "  ", cursorPrefix: "▶ "},
	}
	var control boundedScrollCursor
	control.setBounds(5, 2, boundedWrap)
	control.setCursorItems(items, 0)

	var reached strings.Builder
	wantAbove := []int{0, 2, 4}
	wantBelow := []int{6, 4, 2}
	for page := 0; page < 3; page++ {
		view := control.view()
		if len(view.rows) == 0 || len(view.rows) > 2 {
			t.Fatalf("page %d has %d rows, want 1..2", page, len(view.rows))
		}
		if view.above != wantAbove[page] || view.below != wantBelow[page] {
			t.Fatalf("page %d overflow = (%d above, %d below), want (%d, %d)",
				page, view.above, view.below, wantAbove[page], wantBelow[page])
		}
		for _, row := range view.rows {
			plain := ansi.Strip(row.text)
			if !strings.HasPrefix(plain, "▶ ") {
				t.Errorf("oversized selected segment lost cursor marker: %q", plain)
			}
			if !strings.Contains(row.text, "\x1b[31m") {
				t.Errorf("wrapped segment lost the item's ANSI style: %q", row.text)
			}
			reached.WriteString(strings.TrimPrefix(plain, "▶ "))
			if !strings.HasSuffix(row.text, "\x1b[0m") {
				t.Errorf("clipped styled segment can leak ANSI state: %q", row.text)
			}
		}
		if page < 2 {
			if view.below == 0 {
				t.Fatalf("page %d reported no content below before oversized item was exhausted", page)
			}
			control.move(boundedPageDown)
			if control.cursor != 0 {
				t.Fatalf("Page Down advanced cursor before oversized item was exhausted: %d", control.cursor)
			}
		}
	}
	if got := reached.String(); got != "abcdefghijklmnopqr" {
		t.Fatalf("reachable oversized content = %q, want complete item", got)
	}
	control.move(boundedPageDown)
	if control.cursor != 1 {
		t.Fatalf("Page Down after final oversized segment cursor = %d, want 1", control.cursor)
	}
	if plain := ansi.Strip(control.view().rows[0].text); plain != "▶ nex" {
		t.Fatalf("style or content leaked into following item: %q", plain)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario1_ClampsContentAndDegenerateBounds(t *testing.T) {
	var control boundedScrollCursor
	control.setBounds(12, 2, boundedClip)
	control.setBrowsing([]string{"zero", "one", "two", "three", "four"})
	control.move(boundedLineDown)
	if got := control.view().above; got != 1 {
		t.Fatalf("browsing line movement offset = %d, want 1", got)
	}
	control.move(boundedPageDown)
	if got := control.view().above; got != 3 {
		t.Fatalf("browsing page movement offset = %d, want clamped 3", got)
	}
	control.move(boundedEnd)
	if got := control.view(); got.above != 3 || len(got.rows) != 2 {
		t.Fatalf("browsing End view = above %d, rows %d; want final nonblank page", got.above, len(got.rows))
	}

	control.setBrowsing([]string{"only"})
	if got := control.view(); got.above != 0 || len(got.rows) != 1 || ansi.Strip(got.rows[0].text) != "only" {
		t.Fatalf("content shrink left a blank/reachable stale page: %#v", got)
	}
	control.setBounds(12, 5, boundedClip)
	if got := control.view(); got.above != 0 || len(got.rows) != 1 {
		t.Fatalf("geometry growth did not clamp browsing offset: %#v", got)
	}

	control.setCursorItems([]boundedScrollItem{{text: "first"}, {text: "last"}}, 99)
	if control.cursor != 1 || len(control.view().rows) == 0 {
		t.Fatalf("cursor content change did not clamp to a reachable item: cursor=%d view=%#v", control.cursor, control.view())
	}
	for _, bounds := range [][2]int{{0, 2}, {12, 0}, {-1, 2}, {12, -1}} {
		control.setBounds(bounds[0], bounds[1], boundedWrap)
		if got := control.view(); len(got.rows) != 0 || got.above != 0 || got.below != 0 {
			t.Errorf("bounds %v rendered nonempty body/overflow: %#v", bounds, got)
		}
	}
}
