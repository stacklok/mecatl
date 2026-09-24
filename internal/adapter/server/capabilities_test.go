package server_test

import (
	"context"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// noopRunner is a do-nothing tool.CommandRunner used only to construct a real
// Shell tool (NewShellTool panics on a nil runner). The test never executes it; it
// only needs the tool registered under its real catalog name so capabilities()
// reports bash=true. Driving caps from the REAL tool constructors (rather than a
// stub named "Shell") is what makes TestCapabilities a rename-drift backstop.
// (The runner is bound to the Environment at Execute time, so NewShellTool takes
// no runner now — issue #462.)

// noopMemStore is a do-nothing tool.MemoryStore used only to construct the real
// Remember tool, so it registers under its real catalog name ("Remember").
type noopMemStore struct{}

func (noopMemStore) Remember(context.Context, tool.MemoryEntry, tool.MemoryCurrent) (tool.MemoryRecord, error) {
	return tool.MemoryRecord{}, nil
}
func (noopMemStore) Inspect(context.Context, string) (tool.MemoryRecord, bool, error) {
	return tool.MemoryRecord{}, false, nil
}
func (noopMemStore) Recall(context.Context, string) (tool.MemoryEntry, bool, error) {
	return tool.MemoryEntry{}, false, nil
}
func (noopMemStore) List(context.Context, string) ([]tool.MemoryEntry, error) { return nil, nil }
func (noopMemStore) Forget(context.Context, string, tool.MemoryVersion) (tool.MemoryRecord, error) {
	return tool.MemoryRecord{}, nil
}
func (noopMemStore) Undo(context.Context, string, tool.MemoryVersion) (tool.MemoryRecord, error) {
	return tool.MemoryRecord{}, nil
}
func (noopMemStore) Index(context.Context) ([]tool.MemoryEntry, error) { return nil, nil }
func (noopMemStore) Search(context.Context, string, int) ([]tool.MemoryEntry, error) {
	return nil, nil
}

type stubCommandLister struct{}

func (*stubCommandLister) List(context.Context) ([]prompt.Command, error) {
	return nil, nil
}
func (*stubCommandLister) Expand(_ context.Context, input string) (string, bool, error) {
	return input, false, nil
}
func (s *stubCommandLister) Borrow(context.Context, session.SessionID, *session.Principal, string) (server.CommandSourceBinding, func(), error) {
	return s, func() {}, nil
}
func (*stubCommandLister) Activate(context.Context, session.SessionID, *session.Principal, string) error {
	return nil
}
func (*stubCommandLister) Retire(session.SessionID) {}

// stubMemberEngine satisfies Config.MemberEngine (MemberEngineFactory) just
// enough to be non-nil; the Service only nil-checks it for the teams cap. It is
// never invoked.
func stubMemberEngine(*team.Team, agent.MemberSpec, string) agent.MemberBuild {
	return agent.MemberBuild{}
}

// buildCapsService constructs a Service whose engine catalog holds exactly the
// named tools, with the given optional seams wired, then returns it. The caps
// table test drives capabilities() through the gRPC CreateSession response (the
// shared create path) so it ALSO verifies population happens at that path, not
// per-surface.
func buildCapsService(
	t *testing.T,
	catalogTools []tool.Tool,
	mcpProvider bool,
	commands bool,
	teams bool,
) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	for _, tl := range catalogTools {
		cat.MustRegister(tl)
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("x")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	cfg := server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now: func() time.Time { return time.Unix(0, 0) },
	}
	if mcpProvider {
		cfg.MCPProvider = &fakeProvider{}
	}
	if commands {
		cfg.Commands = &stubCommandLister{}
	}
	if teams {
		cfg.MemberEngine = stubMemberEngine
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// capsFromCreate reads the canonical deployment-wide compatibility projection.
func capsFromCreate(t *testing.T, svc *server.Service) *mecatlv1.ServerCapabilities {
	t.Helper()
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		t.Fatalf("GetCompatibilityInfo: %v", err)
	}
	return resp.GetCapabilities()
}

// TestCapabilitiesMediaFromProvider asserts the image/audio caps are driven by the
// composition-supplied DefaultCapabilities (the catalog ∩ adapter intersection for
// the default provider+model, multi-provider Phase 0 S5) — NOT the bare engine seam
// (which is adapter-only and would re-introduce the catalog gap): a default capability
// of image-only surfaces over the gRPC CreateSession response as image=true,
// audio=false. This is the gate the client's @-mention file-attach UX reads.
func TestCapabilitiesMediaFromProvider(t *testing.T) {
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})}),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now: func() time.Time { return time.Unix(0, 0) },
		// The server reads DefaultCapabilities (composition-computed), not the engine.
		DefaultCapabilities: port.ProviderCapabilities{Image: true},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	caps := capsFromCreate(t, svc)
	if caps == nil {
		t.Fatal("capabilities not populated on CreateSession response")
	}
	if !caps.GetImage() {
		t.Errorf("image cap = false, want true (provider advertises images)")
	}
	if caps.GetAudio() {
		t.Errorf("audio cap = true, want false (provider does not advertise audio)")
	}
}

// TestCapabilitiesAgentsFromSnapshot asserts the agents cap flips with a
// non-empty Config.Agents snapshot (the resolved agent-definition registry that
// backs ListAgents) and is false when the snapshot is empty. It is independent of
// the teams cap (the run-path member-engine): defs are browsable without teams.
func TestCapabilitiesAgentsFromSnapshot(t *testing.T) {
	newSvc := func(agents []*mecatlv1.AgentInfo) *server.Service {
		t.Helper()
		engine := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("x")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		svc, err := newPlacementTestService(server.Config{
			Engine: engine,
			Store:  memstore.New(),

			Now:    func() time.Time { return time.Unix(0, 0) },
			Agents: agents,
		})
		if err != nil {
			t.Fatalf("new service: %v", err)
		}
		return svc
	}

	// Non-empty snapshot → agents=true, even with teams off.
	on := capsFromCreate(t, newSvc([]*mecatlv1.AgentInfo{{Name: "scout", Description: "explore"}}))
	if !on.GetAgents() {
		t.Errorf("agents cap = false, want true (non-empty Agents snapshot)")
	}
	if on.GetTeams() {
		t.Errorf("teams cap = true, want false (no member engine) — agents must be independent of teams")
	}

	// Empty snapshot → agents=false.
	off := capsFromCreate(t, newSvc(nil))
	if off.GetAgents() {
		t.Errorf("agents cap = true, want false (empty Agents snapshot)")
	}
}

// TestCapabilities asserts that the create response's capabilities reflect the
// BUILT catalog and wired seams, NOT a static list. It is the correctness guard:
// the tool caps are driven by the REAL tool constructors, so it fails if a tool
// is ever renamed (the spelling the Service probes would drift from the
// registration), and the seam caps flip with the nil-checks the feature RPCs use.
func TestCanonicalShellTool_Scenario1_CapabilityCompatibility(t *testing.T) {
	remember := memory.NewRememberTool(noopMemStore{})
	skill := skills.NewTool(nil, nil)
	bash := fstools.NewShellTool()

	tests := []struct {
		name  string
		tools []tool.Tool
		mcp   bool
		cmds  bool
		teams bool
		want  *mecatlv1.ServerCapabilities
	}{
		{
			name: "all off (empty catalog, no seams)",
			want: &mecatlv1.ServerCapabilities{},
		},
		{
			name:  "all on",
			tools: []tool.Tool{remember, skill, bash},
			mcp:   true,
			cmds:  true,
			teams: true,
			want: &mecatlv1.ServerCapabilities{
				Mcp:           true,
				SlashCommands: true,
				Memory:        true,
				Skills:        true,
				Teams:         true,
				Shell:         true,
			},
		},
		{
			name:  "embedded default (memory on; mcp/commands/skills off; teams on; bash on)",
			tools: []tool.Tool{remember, bash},
			teams: true,
			want: &mecatlv1.ServerCapabilities{
				Memory: true,
				Shell:  true,
				Teams:  true,
			},
		},
		{
			name:  "no-bash (bash tool absent)",
			tools: []tool.Tool{remember, skill},
			want: &mecatlv1.ServerCapabilities{
				Memory: true,
				Skills: true,
			},
		},
		{
			name:  "mcp wired only",
			tools: nil,
			mcp:   true,
			want:  &mecatlv1.ServerCapabilities{Mcp: true},
		},
		{
			name:  "skills only",
			tools: []tool.Tool{skill},
			want:  &mecatlv1.ServerCapabilities{Skills: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := buildCapsService(t, tc.tools, tc.mcp, tc.cmds, tc.teams)
			got := capsFromCreate(t, svc)
			if got == nil {
				t.Fatalf("capabilities not populated on CreateSession response")
			}
			if !got.GetManualCompaction() {
				t.Fatal("manual_compaction = false with a configured engine")
			}
			if got.GetMcp() != tc.want.GetMcp() ||
				got.GetSlashCommands() != tc.want.GetSlashCommands() ||
				got.GetMemory() != tc.want.GetMemory() ||
				got.GetSkills() != tc.want.GetSkills() ||
				got.GetTeams() != tc.want.GetTeams() ||
				got.GetShell() != tc.want.GetShell() {
				t.Fatalf("capabilities mismatch\n got: mcp=%v cmds=%v mem=%v skills=%v teams=%v bash=%v\nwant: mcp=%v cmds=%v mem=%v skills=%v teams=%v bash=%v",
					got.GetMcp(), got.GetSlashCommands(), got.GetMemory(), got.GetSkills(), got.GetTeams(), got.GetShell(),
					tc.want.GetMcp(), tc.want.GetSlashCommands(), tc.want.GetMemory(), tc.want.GetSkills(), tc.want.GetTeams(), tc.want.GetShell())
			}
		})
	}
}
