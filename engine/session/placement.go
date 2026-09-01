package session

// PlacementSelectorKind is the closed public placement-selection vocabulary.
type PlacementSelectorKind string

const (
	// PlacementSelectorDefault asks trusted composition for its configured
	// deployment default.
	PlacementSelectorDefault PlacementSelectorKind = "default"
	// PlacementSelectorNoFS explicitly attenuates placement to no filesystem.
	PlacementSelectorNoFS PlacementSelectorKind = "no-fs"
	// PlacementSelectorID selects a provider-owned opaque placement ID.
	PlacementSelectorID PlacementSelectorKind = "id"
)

// PlacementSelector is one of the three placement selector variants. ID is
// populated only for PlacementSelectorID.
type PlacementSelector struct {
	Kind PlacementSelectorKind
	ID   string
}

// DefaultPlacement returns the omitted/default selector.
func DefaultPlacement() PlacementSelector {
	return PlacementSelector{Kind: PlacementSelectorDefault}
}

// NoFSPlacement returns the explicit no-filesystem attenuation selector.
func NoFSPlacement() PlacementSelector {
	return PlacementSelector{Kind: PlacementSelectorNoFS}
}

// SelectPlacementID returns an opaque-ID selector. An empty ID remains invalid
// and is rejected by Valid at the binding boundary.
func SelectPlacementID(id string) PlacementSelector {
	return PlacementSelector{Kind: PlacementSelectorID, ID: id}
}

// Valid reports whether the selector is exactly one closed protocol variant.
func (s PlacementSelector) Valid() bool {
	switch s.Kind {
	case PlacementSelectorDefault, PlacementSelectorNoFS:
		return s.ID == ""
	case PlacementSelectorID:
		return s.ID != ""
	default:
		return false
	}
}
