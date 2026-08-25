package ui

// HitID is an opaque, frame-scoped handle for a clickable surface region. It has
// no model-wide meaning: only the surface that minted it during the current View
// frame may interpret it.
type HitID uint64

// ClickableRegion is a surface body-relative rectangle tagged with an opaque,
// frame-scoped handle.
type ClickableRegion struct {
	rect cellRect
	hit  HitID
}

// surfaceHitMsg is delivered through surface.HandleMsg after the parent has
// hit-tested a rendered frame. X and Y are local to the matched region.
type surfaceHitMsg struct {
	ID   HitID
	X, Y int
}

// hitIDAllocator is the only hit-state capability surfaces receive.
type hitIDAllocator interface {
	allocate() HitID
}

type renderedHitRegion struct {
	id   HitID
	rect cellRect
}

// hitRegions is Model-owned reference state. It retains only the current
// surface body's local regions and opaque IDs; placement belongs to
// renderedSurfaceMetrics.
type hitRegions struct {
	next  HitID
	frame []renderedHitRegion
}

func (h *hitRegions) allocate() HitID {
	h.next++
	return h.next
}

func (h *hitRegions) replace(regions []ClickableRegion) {
	h.frame = h.frame[:0]
	for _, region := range regions {
		h.frame = append(h.frame, renderedHitRegion{id: region.hit, rect: region.rect})
	}
}

func (h *hitRegions) clear() { h.frame = h.frame[:0] }

func (h *hitRegions) at(x, y int) (renderedHitRegion, bool) {
	for _, hit := range h.frame {
		if hit.rect.contains(x, y) {
			return hit, true
		}
	}
	return renderedHitRegion{}, false
}

func (h *hitRegions) contains(id HitID) bool {
	for _, hit := range h.frame {
		if hit.id == id {
			return true
		}
	}
	return false
}
