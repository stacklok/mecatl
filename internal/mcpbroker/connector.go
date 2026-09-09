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

// ConnectorInventory describes broker-local catalogue publication, not session
// installation, persistence, prompt readiness, authorization validity, or health.
// Availability is available/unavailable; EnrollmentState is not_required,
// not_started (no active enrollment), pending, completed, or unknown.
type ConnectorInventory struct {
	Availability    string
	EnrollmentState string
	Connectors      []ConnectorStatus
	TotalConnectors uint32
	Truncated       bool
}

// ConnectorStatus contains only a bounded display name and published tool count.
// CatalogueState is hidden, declared, discovered, or unknown. A zero count with
// unknown state is missing information, not proof of an empty catalogue.
type ConnectorStatus struct {
	Name           string
	CatalogueState string
	ToolCount      uint32
}
