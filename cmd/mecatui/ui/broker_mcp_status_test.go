package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// countingBrokerMCP proves broker-only paths never fall through to the direct
// MCP interface, even when a collaborator happens to implement both interfaces.
type countingBrokerMCP struct {
	directCalls int
	inventory   client.MCPConnectorInventory
	err         error
}

func (f *countingBrokerMCP) ListMCPConnectors(context.Context, string) (client.MCPConnectorInventory, error) {
	return f.inventory, f.err
}

func (f *countingBrokerMCP) ListMCPResources(context.Context, string) ([]client.MCPResource, error) {
	f.directCalls++
	return nil, errors.New("direct MCP must not be called")
}
func (f *countingBrokerMCP) ReadMCPResource(context.Context, string, string) ([]client.MCPResourceContents, error) {
	f.directCalls++
	return nil, errors.New("direct MCP must not be called")
}
func (f *countingBrokerMCP) ListMCPPrompts(context.Context, string) ([]client.MCPPrompt, error) {
	f.directCalls++
	return nil, errors.New("direct MCP must not be called")
}
func (f *countingBrokerMCP) GetMCPPrompt(context.Context, string, string, map[string]string) (string, []client.MCPPromptMessage, error) {
	f.directCalls++
	return "", nil, errors.New("direct MCP must not be called")
}
func (f *countingBrokerMCP) ListMCPSources(context.Context) ([]client.MCPSource, error) {
	f.directCalls++
	return nil, errors.New("direct MCP must not be called")
}
func (f *countingBrokerMCP) ListToolHiveGroups(context.Context) ([]string, error) {
	f.directCalls++
	return nil, errors.New("direct MCP must not be called")
}

func TestMCPBrokerPanelGolden(t *testing.T) {
	st := mcpState{brokerMode: true, inventory: client.MCPConnectorInventory{
		Availability: "available", EnrollmentState: "not_started", Truncated: true,
		Connectors: []client.MCPConnectorStatus{
			{Name: "hidden", CatalogueState: "hidden"},
			{Name: "github", CatalogueState: "discovered", ToolCount: 2},
		},
	}}
	compareGolden(t, "mcp_broker_panel.golden", stripANSI([]byte(renderBrokerMCPPanel(aztec(), st, helpKeys{}, 100))))
}

func TestBrokerMCPStatus_Scenario3_UnifiedSetupActions(t *testing.T) {
	st := mcpState{view: mcpPanel, brokerMode: true, inventory: client.MCPConnectorInventory{Availability: "available", EnrollmentState: "not_started"}, setup: brokerMCPSetupState{eligible: true}}
	view := renderBrokerMCPPanel(aztec(), st, helpKeys{}, 100)
	if !strings.Contains(view, "c connect tools") {
		t.Fatalf("eligible inventory omitted setup action:\n%s", view)
	}
	cmd, handled, closed := st.HandleKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if !handled || closed || cmd == nil {
		t.Fatalf("connect action = handled:%t closed:%t cmd:%t", handled, closed, cmd != nil)
	}
	if msg := cmd(); msg != (mcpWorkspaceEnrollmentActionMsg{action: connectAction, source: &st}) {
		t.Fatalf("connect action message = %#v", msg)
	}

	st.setup.pending = true
	view = renderBrokerMCPPanel(aztec(), st, helpKeys{}, 100)
	for _, want := range []string{"Setup in progress", "x cancel setup"} {
		if !strings.Contains(view, want) {
			t.Errorf("pending inventory omitted %q:\n%s", want, view)
		}
	}
	cmd, handled, closed = st.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !handled || closed || cmd == nil {
		t.Fatalf("cancel action = handled:%t closed:%t cmd:%t", handled, closed, cmd != nil)
	}
	if msg := cmd(); msg != (mcpWorkspaceEnrollmentActionMsg{action: "cancel", source: &st}) {
		t.Fatalf("cancel action message = %#v", msg)
	}

	st.setup = brokerMCPSetupState{}
	st.inventory.EnrollmentState = "not_started"
	view = renderBrokerMCPPanel(aztec(), st, helpKeys{}, 100)
	for _, unwanted := range []string{"connect tools", "Continue in browser", "cancel setup"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("ineligible inventory offered %q:\n%s", unwanted, view)
		}
	}

	m, control := setupPanelModel(t)
	m = openOverlay(t, m, ctrlKey('o'))
	mm, action := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = mm.(Model)
	mm, controlCmd := m.Update(action())
	if controlCmd == nil {
		t.Fatal("missing controller command")
	}
	m = applyAll(mm.(Model), controlCmd())
	if control.connectCalls != 1 || m.enrollment.ID != "bundle" {
		t.Fatal("controller not reused")
	}
}

func TestBrokerMCPStatus_Scenario3_Panel(t *testing.T) {
	st := mcpState{brokerMode: true, inventory: client.MCPConnectorInventory{
		Availability: "available", EnrollmentState: "not_started", Truncated: true,
		Connectors: []client.MCPConnectorStatus{{Name: "hidden", CatalogueState: "hidden"}, {Name: "future", CatalogueState: "future"}},
	}}
	view := renderBrokerMCPPanel(aztec(), st, helpKeys{}, 100)
	for _, want := range []string{"MCP inventory", "Enrollment: No active setup", "hidden  Awaiting discovery", "  — tools", "future  Status unavailable", "Connector list truncated.", "Catalogue status · not a live connection check"} {
		if !strings.Contains(view, want) {
			t.Errorf("panel missing %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"not_started", "hidden  hidden", "future  unknown", "Broker publication only", "Enrollment describes catalogue", "Live health is not monitored"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("panel exposes machine status or verbose caveat %q:\n%s", unwanted, view)
		}
	}
}

func TestBrokerMCPStatus_FriendlyStatusLabels(t *testing.T) {
	for state, want := range map[string]string{
		"not_required": "No setup required",
		"not_started":  "No active setup",
		"pending":      "Setup in progress",
		"completed":    "Catalogue ready",
		"future":       "Status unavailable",
	} {
		if got := brokerEnrollmentLabel(state); got != want {
			t.Errorf("brokerEnrollmentLabel(%q) = %q, want %q", state, got, want)
		}
	}
	for state, want := range map[string]string{
		"hidden":     "Awaiting discovery",
		"declared":   "Tools declared",
		"discovered": "Tools discovered",
		"future":     "Status unavailable",
	} {
		if got := brokerCatalogueLabel(state); got != want {
			t.Errorf("brokerCatalogueLabel(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestBrokerMCPStatus_Scenario3_StaleResponses(t *testing.T) {
	st := mcpState{brokerMode: true, sessionID: "current", requestToken: 9, brokerGeneration: 2, loading: true}
	st.HandleMsg(client.MCPConnectorStatusMsg{RequestToken: 8, SessionID: "current", Generation: 2, Inventory: client.MCPConnectorInventory{Availability: "available"}})
	if st.inventory.Availability != "" || !st.loading {
		t.Fatalf("old panel response changed state: %#v", st)
	}
	st.HandleMsg(client.MCPConnectorStatusMsg{RequestToken: 9, SessionID: "old", Generation: 1, Inventory: client.MCPConnectorInventory{Availability: "available"}})
	if st.inventory.Availability != "" || !st.loading {
		t.Fatalf("stale response changed state: %#v", st)
	}
	st.HandleMsg(client.MCPConnectorStatusMsg{RequestToken: 9, SessionID: "current", Generation: 2, Inventory: client.MCPConnectorInventory{Availability: "unavailable"}})
	if st.loading || st.inventory.Availability != "unavailable" {
		t.Fatalf("current response not applied: %#v", st)
	}
	if !strings.Contains(renderBrokerMCPPanel(aztec(), st, helpKeys{}, 100), "Broker state unavailable") {
		t.Fatal("unavailable state was not rendered")
	}

	st = mcpState{brokerMode: true, sessionID: "current", requestToken: 9, brokerGeneration: 4, loading: true}
	st.HandleMsg(client.MCPConnectorErrMsg{RequestToken: 8, SessionID: "current", Generation: 4, Err: errors.New("stale")})
	if st.errMsg != "" || !st.loading {
		t.Fatalf("old panel error changed state: %#v", st)
	}
	st.HandleMsg(client.MCPConnectorErrMsg{RequestToken: 9, SessionID: "old", Generation: 3, Err: errors.New("stale")})
	if st.errMsg != "" || !st.loading {
		t.Fatalf("stale broker error changed state: %#v", st)
	}
	st.HandleMsg(client.MCPConnectorErrMsg{RequestToken: 9, SessionID: "current", Generation: 4, Err: errors.New("current")})
	if st.loading || !strings.Contains(st.errMsg, "current") {
		t.Fatalf("current broker error not applied: %#v", st)
	}
}

func TestBrokerMCPStatus_Scenario3_ReopenRejectsOldPanelResponses(t *testing.T) {
	m, _ := setupPanelModel(t)
	m = openOverlay(t, m, ctrlKey('o'))
	old := mcpActive(m)
	if old == nil {
		t.Fatal("initial panel did not open")
	}
	oldSuccess := client.MCPConnectorStatusMsg{RequestToken: old.requestToken, SessionID: old.sessionID, Generation: old.brokerGeneration, Inventory: client.MCPConnectorInventory{Availability: "unavailable"}}
	oldErr := client.MCPConnectorErrMsg{RequestToken: old.requestToken, SessionID: old.sessionID, Generation: old.brokerGeneration, Err: errors.New("old panel")}
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = openOverlay(t, mm.(Model), ctrlKey('o'))
	current := mcpActive(m)
	if current == nil || current.requestToken == old.requestToken {
		t.Fatalf("reopened panel did not receive a new identity: old=%#v new=%#v", old, current)
	}
	m = applyAll(m, oldSuccess, oldErr)
	current = mcpActive(m)
	if current.inventory.Availability != "available" || current.errMsg != "" {
		t.Fatalf("old panel result overwrote reopened panel: %#v", current)
	}
}

func TestBrokerMCPStatus_Scenario3_UnknownAvailability(t *testing.T) {
	st := mcpState{brokerMode: true, inventory: client.MCPConnectorInventory{Availability: "future"}}
	view := renderBrokerMCPPanel(aztec(), st, helpKeys{}, 100)
	if !strings.Contains(view, "Status unavailable") {
		t.Fatalf("future availability looked available:\n%s", view)
	}
}
func TestBrokerMCPStatus_Scenario3_BuiltinCapabilityAndCollaborator(t *testing.T) {
	oldServer := newMCPModel(t, aztec(), &fakeMCP{})
	oldServer.caps = client.Capabilities{MCPConnectorStatus: true}
	if _, ok := builtinByName(oldServer.caps, oldServer.wiredCollaborators(), "mcp"); ok {
		t.Fatal("broker capability without connector reader registered /mcp")
	}

	broker := newMCPModel(t, aztec(), &countingBrokerMCP{})
	broker.caps = client.Capabilities{MCPConnectorStatus: true}
	if _, ok := builtinByName(broker.caps, broker.wiredCollaborators(), "mcp"); !ok {
		t.Fatal("broker capability with connector reader did not register /mcp")
	}
}

func TestBrokerMCPStatus_Scenario3_BrokerOnlyInteractions(t *testing.T) {
	broker := &countingBrokerMCP{inventory: client.MCPConnectorInventory{Availability: "available", EnrollmentState: "not_started"}}
	m := newMCPModel(t, aztec(), broker)
	m.caps = client.Capabilities{MCPConnectorStatus: true}

	m = openOverlay(t, m, ctrlKey('o'))
	if st := mcpActive(m); st == nil || !st.brokerMode {
		t.Fatalf("broker /mcp did not open the broker panel: %#v", st)
	}
	if broker.directCalls != 0 {
		t.Fatalf("broker /mcp issued %d direct calls", broker.directCalls)
	}

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	for _, key := range []tea.KeyPressMsg{ctrlKey('r'), ctrlKey('p')} {
		mm, cmd := m.Update(key)
		m = mm.(Model)
		if cmd != nil || mcpActive(m) != nil {
			t.Fatalf("broker-only shortcut %q opened a direct surface", key.String())
		}
	}
	if broker.directCalls != 0 {
		t.Fatalf("broker-only shortcuts issued %d direct calls", broker.directCalls)
	}
}
