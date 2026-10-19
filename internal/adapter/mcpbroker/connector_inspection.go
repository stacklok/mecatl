package mcpbroker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const maxConnectorRows = 256

var _ contract.ConnectorInspector = (*Runtime)(nil)

// InspectConnectors reads an existing committed incarnation without touching its
// authorization or attachment lifecycle. Composition exposes this only for the
// bundled Process, whose immutable construction retains configured order.
func (r *Runtime) InspectConnectors(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (contract.ConnectorInventory, error) {
	if err := ctx.Err(); err != nil {
		return contract.ConnectorInventory{}, err
	}
	if !contract.ValidLogicalSessionID(id) {
		return contract.ConnectorInventory{}, fmt.Errorf("%w: invalid session ID", ErrInvalidCatalogue)
	}
	config := r.enrollment
	if config == nil || r.publication == nil {
		return contract.ConnectorInventory{}, contract.ErrStateUnavailable
	}
	count := len(config.connectors)
	if count > math.MaxUint32 {
		return contract.ConnectorInventory{}, ErrInvalidCatalogue
	}
	out := contract.ConnectorInventory{
		Availability: contract.AvailabilityUnavailable, EnrollmentState: contract.EnrollmentUnknown,
		Connectors:      make([]contract.ConnectorStatus, min(count, maxConnectorRows)),
		TotalConnectors: uint32(count), Truncated: count > maxConnectorRows,
	}
	for i := range out.Connectors {
		out.Connectors[i] = contract.ConnectorStatus{Name: config.connectors[i].name, CatalogueState: contract.CatalogueUnknown}
	}
	err := r.publication.WhileOpen(func() error {
		r.mu.RLock()
		defer r.mu.RUnlock()
		logical := r.sessions[id]
		if r.closed || logical == nil {
			return nil
		}
		logical.mu.RLock()
		defer logical.mu.RUnlock()
		expected := session.ExternalBinding(r.bindingPrefix + "." + fmt.Sprint(logical.ref.generation))
		if logical.deleted || logical.provisional || binding != expected {
			return nil
		}
		out.Availability = contract.AvailabilityAvailable
		out.EnrollmentState = r.connectorEnrollmentState(logical)
		routes := r.catalogue.routes
		if logical.completedEnrollment != nil {
			routes = logical.completedEnrollment.routes
		}
		counts := make(map[string]uint32)
		for _, route := range routes {
			counts[route.backend]++
		}
		for i := range out.Connectors {
			connector := config.connectors[i]
			row := &out.Connectors[i]
			row.ToolCount = counts[connector.id]
			switch {
			case !connector.protected || logical.completedEnrollment != nil:
				row.CatalogueState = contract.CatalogueDiscovered
			case row.ToolCount > 0:
				row.CatalogueState = contract.CatalogueDeclared
			default:
				row.CatalogueState = contract.CatalogueHidden
			}
		}
		return nil
	})
	if errors.Is(err, ErrAuthenticatedDiscovery) {
		return out, nil
	}
	return out, err
}

// The caller holds the logical read lock. In particular this is not
// expireLocked or ObserveWorkspaceEnrollment: even expired/granted transactions
// remain untouched until their existing control path settles or discovers them.
func (r *Runtime) connectorEnrollmentState(logical *logicalSession) contract.EnrollmentState {
	if len(r.enrollment.backends) == 0 {
		return contract.EnrollmentNotRequired
	}
	if logical.completedEnrollment != nil {
		return contract.EnrollmentCompleted
	}
	now := r.oauth.now()
	for _, transaction := range logical.authorizations {
		if transaction.bundleBackends != nil && now.Before(transaction.expiresAt) &&
			(transaction.status == session.AuthorizationPending || transaction.status == session.AuthorizationGranted) {
			return contract.EnrollmentPending
		}
	}
	return contract.EnrollmentNotStarted
}

func connectorDisplayName(name string) string {
	var out strings.Builder
	count := 0
	for _, r := range session.ToValidUTF8(name) {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		out.WriteRune(r)
		count++
		if count == 128 {
			break
		}
	}
	return out.String()
}
