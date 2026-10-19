package app

import (
	"reflect"
	"sort"
	"testing"
	"time"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func sortedTools(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

func buildRootFromDef(t *testing.T, cfg Config, def agents.AgentDef, profile server.SessionProfile) server.SessionEngineResult {
	t.Helper()
	oa := mockllm.New(mockllm.TextTurn("x"))
	provReg := regForTest(oa, providerMock, "model-x")
	cfg = isolateConfig(t, cfg)
	factory := agentDefSessionEngineFactory(cfg, provReg, oa, memstore.New(), testAgentDefRootPolicy(cfg),
		hookexec.New(nil), nil, nil, catalogAssets{agentReg: agents.NewRegistry([]agents.AgentDef{def})}, nil)
	res, err := factory(t.Context(), server.ProviderSelector{}, profile, session.ModeDefault, session.Limits{}, def.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Close() })
	return res
}

// The advertised catalog, the granted authority and the ceiling must agree, and
// an explicit exclusion must hold for tools added after name scoping (MCP tools
// and the automatic ShellStatus companion). The expected sets are specified
// independently of the factory so an extra tool in the ceiling alone fails.
func TestAgentDefRootExplicitExclusionsApplyToFinalCatalogAuthorityAndCeiling(t *testing.T) {
	url := newMCPTestServer(t)
	cases := []struct {
		name     string
		excluded []string
		want     []string
	}{
		{"no exclusions keeps MCP and ShellStatus", nil, []string{"Read", "Shell", "ShellStatus", "mcp__inline__echo"}},
		{"excluded MCP tool", []string{"mcp__inline__echo"}, []string{"Read", "Shell", "ShellStatus"}},
		{"excluded ShellStatus", []string{"ShellStatus"}, []string{"Read", "Shell", "mcp__inline__echo"}},
		{"both excluded", []string{"mcp__inline__echo", "ShellStatus"}, []string{"Read", "Shell"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := agents.AgentDef{
				Name: "root", Tools: []string{"Read", "Shell"}, DisallowedTools: tc.excluded,
				MCPServers: []agents.AgentMCPServer{{Name: "inline", URL: url}},
			}
			res := buildRootFromDef(t, Config{Shell: "/bin/sh", TrustProject: true}, def, server.ProfileDefault)
			want := sortedTools(tc.want)
			if got := sortedTools(res.Authority.CapabilitySet.Tools); !reflect.DeepEqual(got, want) {
				t.Fatalf("granted tools = %v, want %v", got, want)
			}
			if res.Authority.Ceiling == nil {
				t.Fatal("agent-bound authority has no ceiling")
			}
			if got := sortedTools(res.Authority.Ceiling.Tools); !reflect.DeepEqual(got, want) {
				t.Fatalf("ceiling tools = %v, want %v", got, want)
			}
			for _, name := range []string{"mcp__inline__echo", "ShellStatus"} {
				excluded := false
				for _, e := range tc.excluded {
					excluded = excluded || e == name
				}
				if has := res.Engine.HasTool(name); has == excluded {
					t.Fatalf("advertised %s = %v, excluded = %v", name, has, excluded)
				}
			}
			// A normal grant attempt must not override an explicit exclusion, and a
			// rejected grant must leave the bound authority untouched.
			sess := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
			sess.AgentDefinitionName = def.Name
			if err := sess.BindAuthority(res.Authority); err != nil {
				t.Fatal(err)
			}
			before, _ := sess.BoundAuthority()
			for _, e := range tc.excluded {
				if err := sess.GrantToolAuthority([]string{e}); err == nil {
					t.Fatalf("grant of excluded %s succeeded", e)
				}
			}
			if err := sess.GrantToolAuthority([]string{"Write"}); err == nil {
				t.Fatal("grant outside the definition succeeded")
			}
			if after, _ := sess.BoundAuthority(); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected grants mutated authority: %+v -> %+v", before, after)
			}
		})
	}
}

// A root uses the main runner, so Shell availability must not inherit the child
// worktree trust gate. no-shell and no-FS deployment restrictions still apply.
func TestAgentDefRootShellFollowsMainRunnerNotChildTrustGate(t *testing.T) {
	def := agents.AgentDef{Name: "root", Tools: []string{"Read", "Shell"}}
	cases := []struct {
		name    string
		cfg     Config
		profile server.SessionProfile
		want    []string
	}{
		{"untrusted checkout keeps Shell", Config{Shell: "/bin/sh", TrustProject: false}, server.ProfileDefault, []string{"Read", "Shell", "ShellStatus"}},
		{"trusted checkout keeps Shell", Config{Shell: "/bin/sh", TrustProject: true}, server.ProfileDefault, []string{"Read", "Shell", "ShellStatus"}},
		{"no-shell deployment drops Shell", Config{Shell: "/bin/sh", NoShell: true}, server.ProfileDefault, []string{"Read"}},
		{"no-FS profile drops Shell", Config{Shell: "/bin/sh", TrustProject: false}, server.ProfileNoFS, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := buildRootFromDef(t, tc.cfg, def, tc.profile)
			got := sortedTools(res.Authority.CapabilitySet.Tools)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, sortedTools(tc.want)) {
				t.Fatalf("granted tools = %v, want %v", got, tc.want)
			}
		})
	}
}
