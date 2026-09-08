package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// TestAgentSnapshotLiteralPin pins agentSnapshot's projection against
// LITERALS — the byte-parity proof through the C2 type move (AgentDef now
// lives engine-side; the proto AgentInfo field set and every projected value
// must be unchanged). The expectations are the PRE-CHANGE outputs: resolved
// model (alias → inherit → cfg.Model; id-looking strings literal), the
// EFFECTIVE read-only Subagent tool scope (allowlist ∩ base, mutating
// dropped, sorted), raw permissionMode/color strings.
func TestAgentSnapshotLiteralPin(t *testing.T) {
	cfg := Config{Model: "gpt-test"} // no shell => no Bash in the base set
	reg := agents.NewRegistry([]agents.AgentDef{
		{
			Name:           "explorer",
			Description:    "read-only code explorer",
			Tools:          []string{"Read", "Grep", "Write"}, // Write must be dropped (mutating)
			Model:          "gpt-x",                           // id-looking => literal
			PermissionMode: "plan",
			Color:          "blue",
		},
		{
			Name:        "summarizer",
			Description: "summarizes findings",
			Model:       "sonnet", // builtin alias => inherit => cfg.Model
		},
	})
	got := agentSnapshot(cfg, reg)
	if len(got) != 2 {
		t.Fatalf("agentSnapshot = %d entries, want 2", len(got))
	}

	first := got[0]
	if first.GetName() != "explorer" || first.GetDescription() != "read-only code explorer" {
		t.Errorf("agent[0] metadata = %+v", first)
	}
	if first.GetModel() != "gpt-x" || first.GetPermissionMode() != "plan" || first.GetColor() != "blue" {
		t.Errorf("agent[0] resolved fields = %+v", first)
	}
	if want := []string{"Grep", "Read"}; !equalStrings(first.GetTools(), want) {
		t.Errorf("agent[0] tools = %v, want %v (allowlist minus mutating, sorted)", first.GetTools(), want)
	}

	second := got[1]
	if second.GetName() != "summarizer" || second.GetModel() != "gpt-test" {
		t.Errorf("agent[1] = %+v (sonnet must resolve to inherit => cfg.Model)", second)
	}
	if second.GetPermissionMode() != "" || second.GetColor() != "" {
		t.Errorf("agent[1] optional fields must stay empty: %+v", second)
	}
	if want := []string{"FetchMcpResource", "Glob", "Grep", "ListDir", "Read", "WebFetch"}; !equalStrings(second.GetTools(), want) {
		t.Errorf("agent[1] tools = %v, want the full read-only base %v", second.GetTools(), want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestSubagentRosterByteIdentical pins the MODEL-FACING roster: the Subagent
// tool's Spec().Description tail rendered from a fixture registry must be
// byte-for-byte the PRE-CHANGE rendering (the AgentMeta name/description
// enumeration in name-sorted order) — the type move may not perturb a single
// byte of what the model reads.
func TestSubagentRosterByteIdentical(t *testing.T) {
	cfg := Config{Model: "m"}
	provider := mockllm.New(mockllm.TextTurn("x"))
	reg := agents.NewRegistry([]agents.AgentDef{
		{Name: "b-researcher", Description: "Researches a topic deeply."},
		{Name: "a-reviewer", Description: "Reviews a diff for correctness bugs."},
	})
	tt, closeFn := taskToolForTest(context.Background(), cfg, provider, hookexec.New(nil), reg, nil)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}
	const wantTail = "\n\nAvailable specialist agents (pass the name as `agent`):" +
		"\n- a-reviewer: Reviews a diff for correctness bugs." +
		"\n- b-researcher: Researches a topic deeply."
	desc := tt.Spec().Description
	if !strings.HasSuffix(desc, wantTail) {
		t.Errorf("Subagent Spec().Description tail diverged from the pre-change rendering.\nwant suffix %q\ngot tail    %q",
			wantTail, desc[max(0, len(desc)-len(wantTail)-40):])
	}
}

// countingAgentSourceServer serves one fixed def and counts ListAgentDefs
// RPCs, so the single-resolution invariant is observable on the wire.
type countingAgentSourceServer struct {
	driverv1.UnimplementedAgentSourceServiceServer
	calls atomic.Int32
}

func (s *countingAgentSourceServer) ListAgentDefs(context.Context, *driverv1.ListAgentDefsRequest) (*driverv1.ListAgentDefsResponse, error) {
	s.calls.Add(1)
	return &driverv1.ListAgentDefsResponse{AgentDefs: []*driverv1.AgentDef{
		{Name: "counter-spec", Description: "counts resolutions", Body: "be countable"},
	}}, nil
}

// TestBuildResolvesAgentRegistryExactlyOnce is the drift-class guard (§0.3):
// the registry used to be resolved THREE times per Build (engines/catalog,
// ListAgents snapshot, team wiring) — C2 consolidates to ONE resolveAgentSeam.
// A counting wire server makes the count un-fakeable: a regression that
// reintroduces a second resolution (the third firing of the per-session-drift
// class) fails here with calls > 1. EnableTeams + Parallel are ON so every
// historical re-resolution site is exercised in this one Build.
func TestBuildResolvesAgentRegistryExactlyOnce(t *testing.T) {
	srv := &countingAgentSourceServer{}
	addr := startSourceDriver(t, func(gs *grpc.Server) {
		driverv1.RegisterAgentSourceServiceServer(gs, srv)
	})

	built, err := Build(context.Background(), Config{
		Workspace:      t.TempDir(),
		Model:          "mock",
		UseMock:        true,
		AgentSourceURL: addr,
		EnableTeams:    true,
		EnableParallel: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if got := srv.calls.Load(); got != 1 {
		t.Fatalf("Build issued %d ListAgentDefs RPCs, want exactly 1 (the registry must be resolved ONCE and shared — catalog, snapshot, team wiring)", got)
	}
}
