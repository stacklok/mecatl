package server_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// agentsService builds a Service carrying the given agent snapshot, mirroring
// mcpService but for the ListAgents RPC's snapshot field.
func agentsService(t *testing.T, snapshot []*mecatlv1.AgentInfo) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:     engine,
		Store:      memstore.New(),
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		Agents:     snapshot,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func cannedAgents() []*mecatlv1.AgentInfo {
	return []*mecatlv1.AgentInfo{
		{
			Name:           "explorer",
			Description:    "read-only code explorer",
			Model:          "gpt-test",
			Tools:          []string{"Grep", "Read"},
			PermissionMode: "plan",
			Color:          "blue",
		},
		{
			Name:        "summarizer",
			Description: "summarizes findings",
			// model empty => inherit; no tools, no mode, no color.
		},
	}
}

func TestGRPCListAgents(t *testing.T) {
	svc := agentsService(t, cannedAgents())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListAgents(context.Background(), &mecatlv1.ListAgentsRequest{})
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(resp.GetAgents()) != 2 {
		t.Fatalf("agents = %d, want 2", len(resp.GetAgents()))
	}
	first := resp.GetAgents()[0]
	if first.GetName() != "explorer" || first.GetDescription() != "read-only code explorer" {
		t.Fatalf("agent[0] metadata = %+v", first)
	}
	if first.GetModel() != "gpt-test" || first.GetPermissionMode() != "plan" || first.GetColor() != "blue" {
		t.Fatalf("agent[0] resolved fields = %+v", first)
	}
	if len(first.GetTools()) != 2 || first.GetTools()[0] != "Grep" || first.GetTools()[1] != "Read" {
		t.Fatalf("agent[0] tools = %v", first.GetTools())
	}
	second := resp.GetAgents()[1]
	if second.GetName() != "summarizer" || second.GetModel() != "" || len(second.GetTools()) != 0 {
		t.Fatalf("agent[1] = %+v", second)
	}
}

func TestGRPCListAgentsEmpty(t *testing.T) {
	// No snapshot configured (agent definitions disabled) => empty list, no error.
	svc := agentsService(t, nil)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListAgents(context.Background(), &mecatlv1.ListAgentsRequest{})
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(resp.GetAgents()) != 0 {
		t.Fatalf("empty-registry agents = %d, want 0", len(resp.GetAgents()))
	}
}

func TestHTTPListAgents(t *testing.T) {
	svc := agentsService(t, cannedAgents())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	var resp mecatlv1.ListAgentsResponse
	if code := httpGet(t, srv, "/v1/agents", &resp); code != 200 {
		t.Fatalf("GET /v1/agents status = %d", code)
	}
	if len(resp.GetAgents()) != 2 {
		t.Fatalf("http agents = %d, want 2", len(resp.GetAgents()))
	}
	if resp.GetAgents()[0].GetName() != "explorer" || resp.GetAgents()[1].GetName() != "summarizer" {
		t.Fatalf("http agents = %+v", resp.GetAgents())
	}

	// Empty snapshot still returns 200 with an empty list.
	emptySrv := httptest.NewServer(server.NewHTTPHandler(agentsService(t, nil)))
	defer emptySrv.Close()
	var empty mecatlv1.ListAgentsResponse
	if code := httpGet(t, emptySrv, "/v1/agents", &empty); code != 200 {
		t.Fatalf("empty GET /v1/agents status = %d", code)
	}
	if len(empty.GetAgents()) != 0 {
		t.Fatalf("empty http agents = %d, want 0", len(empty.GetAgents()))
	}
}
