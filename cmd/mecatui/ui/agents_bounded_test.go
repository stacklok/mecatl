package ui

import "github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"

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
