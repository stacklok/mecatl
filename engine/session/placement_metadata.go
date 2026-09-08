package session

// PlacementMetadata is bounded display-only information persisted with a
// session. It is never accepted as placement authority and contains no exact
// environment identity or physical locator.
type PlacementMetadata struct {
	Kind     string `json:"kind,omitempty"`
	Label    string `json:"label,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Revision string `json:"revision,omitempty"`
}
