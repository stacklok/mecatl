package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func setupPanelModel(t *testing.T) (Model, *workspaceEnrollmentControlFake) {
	t.Helper()
	control := &workspaceEnrollmentControlFake{connect: client.WorkspaceEnrollment{ID: "bundle", Status: client.WorkspaceEnrollmentPending}}
	m := newMCPModel(t, aztec(), &countingBrokerMCP{inventory: client.MCPConnectorInventory{Availability: "available", EnrollmentState: "not_started"}})
	m.caps = client.Capabilities{MCPConnectorStatus: true, WorkspaceEnrollment: true}
	m.deps.WorkspaceEnrollment = control
	m.deps.OpenURL = func(context.Context, string) error { return nil }
	return m, control
}

func setupPanelView(m Model) string {
	if st := mcpActive(m); st != nil {
		body, _ := st.Render(100, 30)
		return stripANSIstr(body)
	}
	return ""
}

func TestBrokerMCPStatus_Scenario3_SetupEligibility(t *testing.T) {
	for _, name := range []string{"fresh", "unknown", "used", "active", "snapshot-running", "resumed", "completed", "busy", "unwired"} {
		t.Run(name, func(t *testing.T) {
			m, control := setupPanelModel(t)
			switch name {
			case "unknown":
				m.sessionState = ""
			case "used":
				m.conv.addUser("already sent")
			case "active":
				m.phase = phaseRunning
			case "snapshot-running":
				m.sessionState = "running"
			case "resumed":
				mm, _, _ := m.adoptAuthoritativeTranscript(client.SessionListItem{ID: m.sessionID, State: "idle"}, conversation{})
				m = mm.(Model)
			case "completed":
				m.enrollment.ID = "bundle"
				m = applyAll(m, workspaceEnrollmentMsg{sessionID: m.sessionID, targetEnrollmentID: "bundle", action: "check", result: client.WorkspaceEnrollment{ID: "bundle", Status: client.WorkspaceEnrollmentConnected}})
			case "busy":
				m.enrollment.busy = true
			case "unwired":
				m.deps.WorkspaceEnrollment = nil
			}
			m = openOverlay(t, m, ctrlKey('o'))
			if got := strings.Contains(setupPanelView(m), "c connect tools"); got != (name == "fresh") {
				t.Fatalf("connect offered=%t: %s", got, setupPanelView(m))
			}
			mm, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
			m = mm.(Model)
			if cmd != nil && name != "fresh" && mcpActive(m) != nil {
				t.Fatal("ineligible key queued an action")
			}
			if cmd != nil && name == "fresh" {
				mm, cmd = m.Update(cmd())
				m = mm.(Model)
				if cmd != nil {
					_ = cmd()
				}
			}
			if control.connectCalls != 0 && name != "fresh" {
				t.Fatal("ineligible connect called controller")
			}
			if name == "fresh" && control.connectCalls != 1 {
				t.Fatal("fresh connect did not reach controller")
			}
		})
	}
}

func TestBrokerMCPStatus_Scenario3_ClearSuccessorCanConnect(t *testing.T) {
	m, _ := setupPanelModel(t)
	m.deps.Session.(*fakeConv).caps = m.caps
	mm, clear := m.runClear()
	if clear == nil {
		t.Fatal("clear did not start successor creation")
	}
	m = feedCmd(t, mm.(Model), clear)
	m = openOverlay(t, m, ctrlKey('o'))
	if !strings.Contains(setupPanelView(m), "c connect tools") {
		t.Fatalf("fresh clear successor was not eligible: fresh=%t state=%q caps=%+v view=%s", m.freshSessionBinding, m.sessionState, m.caps, setupPanelView(m))
	}
}

func TestBrokerMCPStatus_Scenario3_AliasCancelReopen(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprint(alias), func(t *testing.T) {
			m, control := setupPanelModel(t)
			m = typeText(t, m, "/mcp")
			mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = feedCmd(t, mm.(Model), cmd)
			if mcpActive(m) == nil || control.connectCalls != 0 {
				t.Fatal("slash opening must only inspect")
			}
			if alias {
				mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				m = typeText(t, mm.(Model), "/tools-connect")
				mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			} else {
				mm, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
				mm, cmd = mm.(Model).Update(cmd())
			}
			m = applyAll(mm.(Model), cmd())
			if control.connectCalls != 1 || m.enrollment.ID != "bundle" {
				t.Fatal("connect alias divergence")
			}
			if mcpActive(m) != nil {
				mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				m = mm.(Model)
			}
			m = openOverlay(t, m, ctrlKey('o'))
			if !strings.Contains(setupPanelView(m), "x cancel setup") {
				t.Fatal("pending reopen lost cancel")
			}
			mm, refresh := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
			m = feedCmd(t, mm.(Model), refresh)
			if control.connectCalls != 1 || control.cancelCalls != 0 {
				t.Fatal("pending refresh invoked control")
			}
			if alias {
				mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				m = typeText(t, mm.(Model), "/tools-cancel")
				mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			} else {
				mm, cmd = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
				mm, cmd = mm.(Model).Update(cmd())
			}
			m = applyAll(mm.(Model), cmd())
			if control.cancelCalls != 1 || m.enrollment.ID != "" {
				t.Fatal("cancel alias divergence")
			}
			if mcpActive(m) != nil {
				mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				m = mm.(Model)
			}
			m = openOverlay(t, m, ctrlKey('o'))
			if view := setupPanelView(m); strings.Contains(view, "cancel setup") || strings.Contains(view, "Setup in progress") || !strings.Contains(view, "c connect tools") {
				t.Fatalf("cancelled reopen: %s", view)
			}
		})
	}
}

func TestBrokerMCPStatus_Scenario3_StaleSetupActions(t *testing.T) {
	for _, change := range []string{"session", "generation", "busy", "used"} {
		t.Run(change, func(t *testing.T) {
			m, _ := setupPanelModel(t)
			m = openOverlay(t, m, ctrlKey('o'))
			mm, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
			m = mm.(Model)
			msg := cmd()
			switch change {
			case "session":
				m = m.bindSessionID("replacement")
			case "generation":
				m.enrollment.controlGen++
			case "busy":
				m.enrollment.busy = true
			case "used":
				m.conv.addUser("sent")
			}
			if _, cmd = m.Update(msg); cmd != nil {
				t.Fatal("stale action reached controller")
			}
		})
	}
}

func TestBrokerMCPStatus_Scenario3_ClosedPanelActionIsInert(t *testing.T) {
	m, control := setupPanelModel(t)
	m = openOverlay(t, m, ctrlKey('o'))
	mm, action := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if action == nil {
		t.Fatal("eligible panel did not queue connect")
	}
	m = mm.(Model)
	queued := action()
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = openOverlay(t, mm.(Model), ctrlKey('o'))
	if _, cmd := m.Update(queued); cmd != nil || control.connectCalls != 0 {
		t.Fatal("queued action from closed panel reached controller")
	}
}

func TestBrokerMCPStatus_Scenario3_SetupLiveState(t *testing.T) {
	m, control := setupPanelModel(t)
	m = openOverlay(t, m, ctrlKey('o'))
	mm, refresh := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = feedCmd(t, mm.(Model), refresh)
	if control.connectCalls != 0 {
		t.Fatal("open/refresh enrolled")
	}
	mm, action := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = mm.(Model)
	if action == nil {
		t.Fatal("missing action")
	}
	actionMsg := action()
	mm, connect := m.Update(actionMsg)
	m = mm.(Model)
	if connect == nil {
		t.Fatal("missing controller command")
	}
	if _, duplicate := m.Update(actionMsg); duplicate != nil {
		t.Fatal("duplicate action admitted")
	}
	m = applyAll(m, connect())
	if view := setupPanelView(m); !strings.Contains(view, "Setup in progress") || !strings.Contains(view, "x cancel setup") || strings.Contains(view, "Continue in browser") || strings.Contains(view, "c connect tools") {
		t.Fatalf("pending: %s", view)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"}); cmd != nil {
		t.Fatal("pending connect admitted")
	}
	gen := m.enrollment.controlGen
	stale := workspaceEnrollmentMsg{sessionID: m.sessionID, targetEnrollmentID: "bundle", gen: gen + 1, action: "check", result: client.WorkspaceEnrollment{ID: "bundle", Status: client.WorkspaceEnrollmentConnected}}
	m = applyAll(m, stale)
	if !strings.Contains(setupPanelView(m), "x cancel setup") {
		t.Fatal("stale completion altered panel")
	}
	m.enrollment.presentationDelivered = true
	mm, poll := m.Update(workspaceEnrollmentPollTickMsg{sessionID: m.sessionID, enrollmentID: "bundle", gen: gen})
	m = mm.(Model)
	if poll == nil {
		t.Fatal("poll not armed")
	}
	if strings.Contains(setupPanelView(m), "x cancel setup") {
		t.Fatal("busy poll left cancel enabled")
	}
	control.connect.Status = client.WorkspaceEnrollmentConnected
	m = applyAll(m, poll())
	if view := setupPanelView(m); strings.Contains(view, "c connect tools") || strings.Contains(view, "x cancel setup") || strings.Contains(view, "Setup in progress") {
		t.Fatalf("completed: %s", view)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = openOverlay(t, mm.(Model), ctrlKey('o'))
	if strings.Contains(setupPanelView(m), "c connect tools") {
		t.Fatal("reopen offered completed connect")
	}
	if _, cmd := m.Update(actionMsg); cmd != nil {
		t.Fatal("old panel action admitted")
	}
}
