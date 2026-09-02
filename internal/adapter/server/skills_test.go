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

// skillsService builds a Service carrying the given skills snapshot, mirroring
// agentsService but for the ListSkills RPC's snapshot field.
func skillsService(t *testing.T, snapshot []*mecatlv1.SkillInfo) *server.Service {
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
		Skills:     snapshot,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func cannedSkills() []*mecatlv1.SkillInfo {
	return []*mecatlv1.SkillInfo{
		{Name: "code-review", Description: "review a diff for bugs"},
		{Name: "deep-research", Description: "fan-out web research"},
	}
}

func TestGRPCListSkills(t *testing.T) {
	svc := skillsService(t, cannedSkills())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListSkills(context.Background(), &mecatlv1.ListSkillsRequest{})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(resp.GetSkills()) != 2 {
		t.Fatalf("skills = %d, want 2", len(resp.GetSkills()))
	}
	first := resp.GetSkills()[0]
	if first.GetName() != "code-review" || first.GetDescription() != "review a diff for bugs" {
		t.Fatalf("skill[0] = %+v", first)
	}
	if resp.GetSkills()[1].GetName() != "deep-research" {
		t.Fatalf("skill[1] = %+v", resp.GetSkills()[1])
	}
}

func TestGRPCListSkillsEmpty(t *testing.T) {
	// No snapshot configured (skills disabled) => empty list, no error.
	svc := skillsService(t, nil)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListSkills(context.Background(), &mecatlv1.ListSkillsRequest{})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(resp.GetSkills()) != 0 {
		t.Fatalf("empty-snapshot skills = %d, want 0", len(resp.GetSkills()))
	}
}

func TestHTTPListSkills(t *testing.T) {
	svc := skillsService(t, cannedSkills())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	var resp mecatlv1.ListSkillsResponse
	if code := httpGet(t, srv, "/v1/skills", &resp); code != 200 {
		t.Fatalf("GET /v1/skills status = %d", code)
	}
	if len(resp.GetSkills()) != 2 {
		t.Fatalf("http skills = %d, want 2", len(resp.GetSkills()))
	}
	if resp.GetSkills()[0].GetName() != "code-review" || resp.GetSkills()[1].GetName() != "deep-research" {
		t.Fatalf("http skills = %+v", resp.GetSkills())
	}

	// Empty snapshot still returns 200 with an empty list.
	emptySrv := httptest.NewServer(server.NewHTTPHandler(skillsService(t, nil)))
	defer emptySrv.Close()
	var empty mecatlv1.ListSkillsResponse
	if code := httpGet(t, emptySrv, "/v1/skills", &empty); code != 200 {
		t.Fatalf("empty GET /v1/skills status = %d", code)
	}
	if len(empty.GetSkills()) != 0 {
		t.Fatalf("empty http skills = %d, want 0", len(empty.GetSkills()))
	}
}
