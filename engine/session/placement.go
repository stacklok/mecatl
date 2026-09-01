package session

// PlacementKind identifies the provider family that owns a placement. The
// value is opaque to the session domain; providers and trusted composition
// define concrete kinds.
type PlacementKind string

// PlacementRef is the durable, provider-owned identity of one immutable
// placement record version. ID is opaque and never grants authority by
// possession. Revision pins the exact provider inventory generation.
type PlacementRef struct {
	Kind     PlacementKind
	ID       string
	Revision string
}

// Valid reports whether every component required for exact reattachment is
// present. The session domain deliberately does not interpret any component.
func (r PlacementRef) Valid() bool {
	return r.Kind != "" && r.ID != "" && r.Revision != ""
}

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
