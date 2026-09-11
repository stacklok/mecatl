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
	// AvailabilityAvailable means the broker reached the connector inventory.
	AvailabilityAvailable Availability = "available"
	// AvailabilityUnavailable means the broker could not reach the connector inventory.
	AvailabilityUnavailable Availability = "unavailable"
)

// EnrollmentState is the closed vocabulary for ConnectorInventory.EnrollmentState.
type EnrollmentState string

const (
	// EnrollmentNotRequired means the connector needs no enrollment step.
	EnrollmentNotRequired EnrollmentState = "not_required"
	// EnrollmentNotStarted means enrollment is required but has not begun.
	EnrollmentNotStarted EnrollmentState = "not_started"
	// EnrollmentPending means enrollment has started but not completed.
	EnrollmentPending EnrollmentState = "pending"
	// EnrollmentCompleted means enrollment has finished successfully.
	EnrollmentCompleted EnrollmentState = "completed"
	// EnrollmentUnknown means enrollment status could not be determined.
	EnrollmentUnknown EnrollmentState = "unknown"
)

// CatalogueState is the closed vocabulary for ConnectorStatus.CatalogueState. A
// zero ToolCount with CatalogueStateUnknown is missing information, not proof
// of an empty catalogue.
type CatalogueState string

const (
	// CatalogueHidden means the connector's tools are not published.
	CatalogueHidden CatalogueState = "hidden"
	// CatalogueDeclared means the connector is declared but not yet discovered.
	CatalogueDeclared CatalogueState = "declared"
	// CatalogueDiscovered means the connector's tools were discovered and published.
	CatalogueDiscovered CatalogueState = "discovered"
	// CatalogueUnknown means the catalogue state could not be determined.
	CatalogueUnknown CatalogueState = "unknown"
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
