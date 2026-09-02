package client

import mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"

// Placement is bounded, display-only metadata. It must never be interpreted as
// a filesystem path or sent back as placement authority.
type Placement struct {
	Kind     string
	Label    string
	Branch   string
	Revision string
}

func placementFrom(p *mecatlv1.PlacementMetadata) Placement {
	if p == nil {
		return Placement{}
	}
	return Placement{Kind: p.GetKind(), Label: p.GetLabel(), Branch: p.GetBranch(), Revision: p.GetRevision()}
}
