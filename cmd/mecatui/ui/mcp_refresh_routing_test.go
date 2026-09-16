package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type refreshRoutingMCP struct {
	fakeMCP
	refreshes int
}

func (f *refreshRoutingMCP) RefreshMCP(context.Context, string) (client.MCPRefreshResult, error) {
	f.refreshes++
	return client.MCPRefreshResult{Revision: 9, Changed: true}, nil
}

type refreshRoutingBroker struct{ connects int }

func (f *refreshRoutingBroker) ConnectWorkspaceServices(context.Context, string) (client.WorkspaceEnrollment, error) {
	f.connects++
	return client.WorkspaceEnrollment{ID: "enroll-1", Status: client.WorkspaceEnrollmentPending}, nil
}
func (*refreshRoutingBroker) RetryWorkspaceEnrollment(context.Context, string, string) (client.WorkspaceEnrollment, error) {
	return client.WorkspaceEnrollment{}, nil
}
func (*refreshRoutingBroker) CancelWorkspaceEnrollment(context.Context, string, string) (client.WorkspaceEnrollment, error) {
	return client.WorkspaceEnrollment{}, nil
}

func TestMCPSourceReconciliation_Scenario4_UnifiedCommandRoutingMatrix(t *testing.T) {
	for _, tc := range []struct {
		name               string
		caps               client.Capabilities
		withDirect         bool
		withBroker         bool
		wantRefreshCommand bool
		wantAlias          bool
		wantDirectCalls    int
		wantBrokerCalls    int
	}{
		{name: "direct-only", caps: client.Capabilities{MCPRefresh: true}, withDirect: true, wantRefreshCommand: true, wantDirectCalls: 1},
		{name: "broker-only", caps: client.Capabilities{WorkspaceEnrollment: true}, withBroker: true, wantRefreshCommand: true, wantAlias: true, wantBrokerCalls: 1},
		{name: "both-fail-closed", caps: client.Capabilities{MCPRefresh: true, WorkspaceEnrollment: true}, withDirect: true, withBroker: true, wantAlias: true},
		{name: "neither-fail-closed"},
		{name: "direct-missing-collaborator", caps: client.Capabilities{MCPRefresh: true}},
		{name: "broker-missing-collaborator", caps: client.Capabilities{WorkspaceEnrollment: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			direct := &refreshRoutingMCP{}
			broker := &refreshRoutingBroker{}
			m, _ := builtinDispatchModel(t, tc.caps, false)
			if tc.withDirect {
				m.deps.MCP = direct
			}
			if tc.withBroker {
				m.deps.WorkspaceEnrollment = broker
			}
			m.caps = tc.caps

			refresh, refreshOK := builtinByName(m.caps, m.wiredCollaborators(), "mcp-refresh")
			if refreshOK != tc.wantRefreshCommand {
				t.Fatalf("mcp-refresh registered=%v want %v", refreshOK, tc.wantRefreshCommand)
			}
			_, aliasOK := builtinByName(m.caps, m.wiredCollaborators(), "tools-connect")
			if aliasOK != tc.wantAlias {
				t.Fatalf("tools-connect registered=%v want %v", aliasOK, tc.wantAlias)
			}
			if refreshOK {
				mm, cmd := refresh.run(m)
				m = mm.(Model)
				if cmd == nil {
					t.Fatal("mcp-refresh returned nil command")
				}
				msg := cmd()
				if batch, ok := msg.(tea.BatchMsg); ok {
					if len(batch) == 0 {
						t.Fatal("empty command batch")
					}
					msg = batch[0]()
				}
				mm, _ = m.Update(msg)
				m = mm.(Model)
			}
			if direct.refreshes != tc.wantDirectCalls || broker.connects != tc.wantBrokerCalls {
				t.Fatalf("direct calls=%d broker calls=%d, want %d/%d", direct.refreshes, broker.connects, tc.wantDirectCalls, tc.wantBrokerCalls)
			}
		})
	}
}
