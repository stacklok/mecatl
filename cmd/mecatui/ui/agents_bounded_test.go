package ui

import (
	"fmt"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func agentsTestListCursor(cursor int) *bounded.List {
	list := new(bounded.List)
	list.SetGeometry(80, 20, 1, bounded.Clip)
	items := make([]bounded.ListItem, cursor+1)
	for i := range items {
		items[i] = bounded.ListItem{ID: fmt.Sprintf("test-%d", i), Text: "row"}
	}
	list.SetItems(items)
	list.SetCursor(cursor)
	return list
}

func agentsTestViewport(offset int) *bounded.Viewport {
	v := new(bounded.Viewport)
	v.SetGeometry(1, 1, 0, bounded.Clip)
	v.Move(bounded.End, offset+1)
	return v
}

func agentsTestOffset(v *bounded.Viewport) int {
	if v == nil {
		return 0
	}
	return v.Offset()
}
