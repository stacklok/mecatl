package mcpbroker

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

// ConnectorInspector is optional, read-only inspection of an existing exact
// broker incarnation. The caller must authorize the session owner first. It
// never attaches, enrolls, refreshes credentials, or performs discovery.
type ConnectorInspector interface {
	InspectConnectors(context.Context, session.SessionID, session.ExternalBinding) (ConnectorInventory, error)
}

// Availability is the closed vocabulary for ConnectorInventory.Availability.
type Availability string

const (
	AvailabilityAvailable   Availability = "available"
	AvailabilityUnavailable Availability = "unavailable"
)

// EnrollmentState is the closed vocabulary for ConnectorInventory.EnrollmentState.
type EnrollmentState string

const (
	EnrollmentNotRequired EnrollmentState = "not_required"
	EnrollmentNotStarted  EnrollmentState = "not_started"
	EnrollmentPending     EnrollmentState = "pending"
	EnrollmentCompleted   EnrollmentState = "completed"
	EnrollmentUnknown     EnrollmentState = "unknown"
)

// CatalogueState is the closed vocabulary for ConnectorStatus.CatalogueState. A
// zero ToolCount with CatalogueStateUnknown is missing information, not proof
// of an empty catalogue.
type CatalogueState string

const (
	CatalogueHidden     CatalogueState = "hidden"
	CatalogueDeclared   CatalogueState = "declared"
	CatalogueDiscovered CatalogueState = "discovered"
	CatalogueUnknown    CatalogueState = "unknown"
)

// ConnectorInventory describes broker-local catalogue publication, not session
// installation, persistence, prompt readiness, authorization validity, or health.
type ConnectorInventory struct {
	Availability    Availability
	EnrollmentState EnrollmentState
	Connectors      []ConnectorStatus
	TotalConnectors uint32
	Truncated       bool
}

// ConnectorStatus contains only a bounded display name and published tool count.
type ConnectorStatus struct {
	Name           string
	CatalogueState CatalogueState
	ToolCount      uint32
}
