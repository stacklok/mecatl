package ui

import "charm.land/bubbles/v2/viewport"

// conversationView owns the transient projection of the logical conversation
// position into the Bubble Tea viewport. The Model owns routing and layout; this
// owner alone decides whether a replacement follows the tail or restores reading.
type conversationView struct {
	mode   followMode
	anchor readingAnchor
	frame  renderedFrame
}

func (v *conversationView) observe(vp viewport.Model) {
	if vp.AtBottom() {
		v.mode = followTail
		return
	}
	if anchor, ok := v.frame.observedAnchorForRow(vp.YOffset(), towardStart); ok {
		anchor.bias = towardStart
		v.anchor = anchor
	}
	v.mode = anchored
}

func (v *conversationView) replace(vp *viewport.Model, frame renderedFrame) {
	v.capture(*vp)
	vp.SetContentLines(frame.lines)
	v.restoreViewport(vp, frame)
}

func (v *conversationView) replaceContent(vp *viewport.Model, content string, frame renderedFrame) {
	v.capture(*vp)
	vp.SetContent(content)
	v.restoreViewport(vp, frame)
}

// replaceProjection updates styling over the same rendered rows. It retains the
// physical offset because no document position changed.
func (v *conversationView) replaceProjection(vp *viewport.Model, content string) {
	offset := vp.YOffset()
	vp.SetContent(content)
	vp.SetYOffset(offset)
	v.observe(*vp)
}

func (v *conversationView) capture(vp viewport.Model) {
	if v.mode == followTail {
		return
	}
	v.observe(vp)
}

func (v *conversationView) restoreViewport(vp *viewport.Model, frame renderedFrame) {
	// The renderer reuses provenance scratch memory on the next render, while this
	// frame remains the viewport's identity source until its next replacement.
	// Keep row metadata independent without copying rendered strings.
	v.frame = frame
	v.frame.provenance = append([]renderedRow(nil), frame.provenance...)
	if v.mode == followTail {
		vp.GotoBottom()
		return
	}
	vp.SetYOffset(v.restore(frame, v.anchor))
	// A replacement can clamp the restored reading position to the tail. That is
	// tail-follow, not a stale independently-inferred flag.
	if vp.AtBottom() {
		v.mode = followTail
	}
}

// restore resolves an anchor using ADR 0301's exact/same-region/same-block/
// adjacent-block order. It returns a valid row whenever the frame has rows.
func (conversationView) restore(frame renderedFrame, anchor readingAnchor) int {
	if len(frame.provenance) == 0 {
		return 0
	}
	if row := exactAnchorRow(frame, anchor); row >= 0 {
		return row
	}
	if row := sameRegionRow(frame, anchor); row >= 0 {
		return row
	}
	if anchor.text {
		if row := sameDerivedFallbackRow(frame, anchor); row >= 0 {
			return row
		}
	}
	if row := sameBlockRow(frame, anchor); row >= 0 {
		return row
	}
	if row := adjacentBlockRow(frame, anchor); row >= 0 {
		return row
	}
	if anchor.bias == towardEnd {
		return len(frame.provenance) - 1
	}
	return 0
}

func exactAnchorRow(frame renderedFrame, anchor readingAnchor) int {
	for i, row := range frame.provenance {
		if row.blockID != anchor.blockID || row.region != anchor.region {
			continue
		}
		if anchor.text && row.text && row.sourceOffset == anchor.sourceOffset {
			return i
		}
		if !anchor.text && row.row == anchor.row {
			return i
		}
	}
	return -1
}

func sameRegionRow(frame renderedFrame, anchor readingAnchor) int {
	if !anchor.text {
		return sameDerivedRegionRow(frame, anchor)
	}
	candidate := -1
	for i, row := range frame.provenance {
		if row.blockID != anchor.blockID || row.region != anchor.region || !row.text {
			continue
		}
		if anchor.bias == towardStart && row.sourceOffset <= anchor.sourceOffset {
			if candidate < 0 || frame.provenance[candidate].sourceOffset < row.sourceOffset {
				candidate = i
			}
		}
		if anchor.bias == towardEnd && row.sourceOffset >= anchor.sourceOffset && candidate < 0 {
			candidate = i
		}
	}
	return candidate
}

func sameDerivedFallbackRow(frame renderedFrame, anchor readingAnchor) int {
	return nearestRegionRow(frame, anchor, true)
}

func sameDerivedRegionRow(frame renderedFrame, anchor readingAnchor) int {
	return nearestRegionRow(frame, anchor, false)
}

// nearestRegionRow preserves the reading edge within a region whose exact row
// disappeared. derivedOnly selects the collapsed presentation fallback for an
// anchor that was previously text-bearing.
func nearestRegionRow(frame renderedFrame, anchor readingAnchor, derivedOnly bool) int {
	candidate := -1
	for i, row := range frame.provenance {
		if !matchesRegionRow(row, anchor, derivedOnly) {
			continue
		}
		if preferredRegionRow(row.row, candidateRow(frame, candidate), anchor) {
			candidate = i
		}
	}
	if candidate >= 0 {
		return candidate
	}
	for i, row := range frame.provenance {
		if !matchesRegionRow(row, anchor, derivedOnly) {
			continue
		}
		if oppositeRegionRow(row.row, candidateRow(frame, candidate), anchor.bias) {
			candidate = i
		}
	}
	return candidate
}

func matchesRegionRow(row renderedRow, anchor readingAnchor, derivedOnly bool) bool {
	return row.blockID == anchor.blockID && row.region == anchor.region && (!derivedOnly || !row.text)
}

func candidateRow(frame renderedFrame, candidate int) int {
	if candidate < 0 {
		return -1
	}
	return frame.provenance[candidate].row
}

func preferredRegionRow(row, candidate int, anchor readingAnchor) bool {
	if anchor.bias == towardStart {
		return row <= anchor.row && (candidate < 0 || candidate < row)
	}
	return row >= anchor.row && (candidate < 0 || candidate > row)
}

func oppositeRegionRow(row, candidate int, bias edgeBias) bool {
	if candidate < 0 {
		return true
	}
	if bias == towardStart {
		return row < candidate
	}
	return row > candidate
}

func sameBlockRow(frame renderedFrame, anchor readingAnchor) int {
	candidate := -1
	for i, row := range frame.provenance {
		if row.blockID != anchor.blockID || row.region == conversationRegionChrome {
			continue
		}
		if candidate < 0 || anchor.bias == towardEnd {
			candidate = i
		}
	}
	if candidate >= 0 {
		return candidate
	}
	for i, row := range frame.provenance {
		if row.blockID == anchor.blockID {
			if candidate < 0 || anchor.bias == towardEnd {
				candidate = i
			}
		}
	}
	return candidate
}

func adjacentBlockRow(frame renderedFrame, anchor readingAnchor) int {
	// The changed-files appendix is allocated when its first member is observed,
	// but renders after every conversation block. When collapsed it has no row in
	// this frame, so its only physical neighbour is the final conversation block.
	if anchor.region == conversationRegionAppendix {
		for i := len(frame.provenance) - 1; i >= 0; i-- {
			if frame.provenance[i].blockID != 0 {
				return lastBlockRow(frame, frame.provenance[i].blockID)
			}
		}
		return -1
	}

	before, after := -1, -1
	for i, row := range frame.provenance {
		if row.blockID == 0 || row.blockID == anchor.blockID {
			continue
		}
		if row.blockID < anchor.blockID && (before < 0 || row.blockID >= frame.provenance[before].blockID) {
			before = i
		}
		if row.blockID > anchor.blockID && (after < 0 || row.blockID <= frame.provenance[after].blockID) {
			after = i
		}
	}
	if anchor.bias == towardStart {
		if before >= 0 {
			return lastBlockRow(frame, frame.provenance[before].blockID)
		}
		if after >= 0 {
			return firstBlockRow(frame, frame.provenance[after].blockID)
		}
	} else {
		if after >= 0 {
			return firstBlockRow(frame, frame.provenance[after].blockID)
		}
		if before >= 0 {
			return lastBlockRow(frame, frame.provenance[before].blockID)
		}
	}
	return -1
}

func firstBlockRow(frame renderedFrame, blockID uint64) int {
	for i, row := range frame.provenance {
		if row.blockID == blockID {
			return i
		}
	}
	return -1
}

func lastBlockRow(frame renderedFrame, blockID uint64) int {
	for i := len(frame.provenance) - 1; i >= 0; i-- {
		if frame.provenance[i].blockID == blockID {
			return i
		}
	}
	return -1
}
