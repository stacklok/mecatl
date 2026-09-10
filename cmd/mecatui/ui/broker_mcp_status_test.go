package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestBrokerMCPStatus_Scenario3_Panel(t *testing.T) {
	st := mcpState{brokerMode: true, inventory: client.MCPConnectorInventory{
		Availability: "available", EnrollmentState: "not_started", Truncated: true,
		Connectors: []client.MCPConnectorStatus{{Name: "hidden", CatalogueState: "hidden"}, {Name: "future", CatalogueState: "future"}},
	}}
	view := renderBrokerMCPPanel(aztec(), st, helpKeys{}, 100)
	for _, want := range []string{"Broker catalogue", "Enrollment: not_started", "— tools", "Connector list truncated.", "Broker publication only; session installation, persistence and prompt readiness are not verified.", "Live health is not monitored."} {
		if !strings.Contains(view, want) {
			t.Errorf("panel missing %q:\n%s", want, view)
		}
	}
}

func TestBrokerMCPStatus_Scenario3_StaleResponses(t *testing.T) {
	st := mcpState{brokerMode: true, sessionID: "current", brokerGeneration: 2, loading: true}
	st.HandleMsg(client.MCPConnectorStatusMsg{SessionID: "old", Generation: 1, Inventory: client.MCPConnectorInventory{Availability: "available"}})
	if st.inventory.Availability != "" || !st.loading {
		t.Fatalf("stale response changed state: %#v", st)
	}
	st.HandleMsg(client.MCPConnectorStatusMsg{SessionID: "current", Generation: 2, Inventory: client.MCPConnectorInventory{Availability: "unavailable"}})
	if st.loading || st.inventory.Availability != "unavailable" {
		t.Fatalf("current response not applied: %#v", st)
	}
	if !strings.Contains(renderBrokerMCPPanel(aztec(), st, helpKeys{}, 100), "Broker state unavailable") {
		t.Fatal("unavailable state was not rendered")
	}
}
