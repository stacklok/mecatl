package server

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// helperTestTool is a broker session tool; gateway marks a gateway route.
type helperTestTool struct {
	name    string
	gateway bool
}

func (t helperTestTool) Spec() tool.ToolSpec { return tool.ToolSpec{Name: t.name} }
func (helperTestTool) ReadOnly() bool        { return false }
func (helperTestTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}
func (t helperTestTool) HelperGatewayTool() (tool.Tool, bool) { return t, t.gateway }

// helperTestAttachment is a broker attachment holding tools.
type helperTestAttachment struct {
	brokercontract.Attachment
	tools []tool.Tool
}

func (a helperTestAttachment) Tools() []tool.Tool { return a.tools }

func (helperTestAttachment) Close(context.Context) (brokercontract.CloseOutcome, error) {
	return brokercontract.CloseAlreadyClosed, nil
}

func TestHelperGatewayToolsKeepsOnlyGatewayRoutes(t *testing.T) {
	got := HelperGatewayTools([]tool.Tool{
		helperTestTool{name: "mcp__calendar__echo", gateway: true},
		helperTestTool{name: "mcp__notes__ping"},
		fakeNonBrokerTool{},
	})
	if len(got) != 1 || got[0].Spec().Name != "mcp__calendar__echo" {
		t.Fatalf("HelperGatewayTools = %v, want the gateway route only", got)
	}
}

type fakeNonBrokerTool struct{ helperTestTool }

func (fakeNonBrokerTool) HelperGatewayTool() {}

// TestTeamHelperToolsAreTheRootsAllowedGatewayTools pins what the generic
// members of a team created over the API get: the root's resident gateway
// tools that the root's authority allows, and nothing without a broker
// attachment or a root.
func TestTeamHelperToolsAreTheRootsAllowedGatewayTools(t *testing.T) {
	svc, _ := newRegisteringService(t, func(cfg *Config) {
		cfg.MemberEngineWithHelperTools = func(context.Context, []tool.Tool) MemberEngineFactory { return nil }
	})
	store := svc.cfg.Store

	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	root := session.New("root", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := root.BindAuthority(session.Authority{Provenance: "test-root", CapabilitySet: governance.CapabilitySet{Tools: []string{"Read", "mcp__calendar__echo"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), root); err != nil {
		t.Fatal(err)
	}

	if tools, names, err := svc.teamHelperTools(t.Context(), root.ID); err != nil || tools != nil || names != nil {
		t.Fatalf("with no resident attachment, teamHelperTools = %v, %v; want none", tools, names)
	}
	svc.mu.Lock()
	svc.brokerAttachments[root.ID] = helperTestAttachment{tools: []tool.Tool{
		helperTestTool{name: "mcp__calendar__echo", gateway: true},
		helperTestTool{name: "mcp__calendar__other", gateway: true}, // not in the root's authority
		helperTestTool{name: "mcp__notes__ping"},                    // not a gateway route
	}}
	svc.mu.Unlock()
	_, names, err := svc.teamHelperTools(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("teamHelperTools: %v", err)
	}
	if !slices.Equal(names, []string{"mcp__calendar__echo"}) {
		t.Fatalf("teamHelperTools names = %v, want the root's allowed gateway tool", names)
	}
	if tools, names, err := svc.teamHelperTools(t.Context(), ""); err != nil || tools != nil || names != nil {
		t.Fatalf("a team with no root got %v, %v; want none", tools, names)
	}
	// A root with no bound authority gives members nothing: fail closed.
	unbound := session.New("unbound-root", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(t.Context(), unbound); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.brokerAttachments[unbound.ID] = svc.brokerAttachments[root.ID]
	svc.mu.Unlock()
	if tools, names, err := svc.teamHelperTools(t.Context(), unbound.ID); err != nil || tools != nil || names != nil {
		t.Fatalf("a root with no bound authority gave %v, %v; want none", tools, names)
	}
	// A root bound to a named definition gives members nothing, even with a
	// resident attachment and an authority allowing the tool (#2938).
	named := session.New("named-root", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	named.AgentDefinitionName = "calendar-reader"
	namedTools := governance.CapabilitySet{Tools: []string{"Read", "mcp__calendar__echo"}}
	if err := named.BindAuthority(session.Authority{Provenance: "test-root", CapabilitySet: namedTools, Ceiling: &namedTools}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), named); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.brokerAttachments[named.ID] = svc.brokerAttachments[root.ID]
	svc.mu.Unlock()
	if tools, names, err := svc.teamHelperTools(t.Context(), named.ID); err != nil || tools != nil || names != nil {
		t.Fatalf("a root bound to a named definition gave %v, %v; want none", tools, names)
	}

	// A member's authority gains the names, once each and sorted.
	got := withHelperToolNames(session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read", "mcp__calendar__echo"}}}, []string{"mcp__calendar__echo", "Bash"})
	if !slices.Equal(got.CapabilitySet.Tools, []string{"Bash", "Read", "mcp__calendar__echo"}) {
		t.Fatalf("withHelperToolNames = %v", got.CapabilitySet.Tools)
	}
}

// TestAPITeamMembersGetTheRootsGatewayTools pins the wiring for a team created
// over the API: its member factory is built with the root's allowed gateway
// tools, and each member's authority allows them.
func TestAPITeamMembersGetTheRootsGatewayTools(t *testing.T) {
	var gotTools []string
	memberFactory := func(_ *team.Team, _ agent.MemberSpec, _ string) agent.MemberBuild {
		return agent.MemberBuild{Engine: brokerEngineResult().Engine}
	}
	svc, _ := newRegisteringService(t, func(cfg *Config) {
		cfg.MemberEngine = memberFactory
		cfg.MemberEngineWithHelperTools = func(_ context.Context, tools []tool.Tool) MemberEngineFactory {
			for _, tl := range tools {
				gotTools = append(gotTools, tl.Spec().Name)
			}
			return memberFactory
		}
		cfg.RootAuthority = func(session.SessionKind) session.Authority {
			return session.Authority{Provenance: "test-root", CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}}
		}
	})
	store := svc.cfg.Store
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	root := session.New("root", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := root.BindAuthority(session.Authority{Provenance: "test-root", CapabilitySet: governance.CapabilitySet{Tools: []string{"Read", "mcp__calendar__echo"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.brokerAttachments[root.ID] = helperTestAttachment{tools: []tool.Tool{
		helperTestTool{name: "mcp__calendar__echo", gateway: true},
		helperTestTool{name: "mcp__notes__ping"},
	}}
	svc.mu.Unlock()

	ts := &teamState{
		env:  tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil),
		name: "reviewers", goal: "goal", rootSessionID: root.ID,
		specs: []agent.MemberSpec{{Name: "lead", Lead: true}},
	}
	_, sup, err := svc.buildTeamForOperation(t.Context(), "team-1", ts)
	if err != nil {
		t.Fatalf("buildTeamForOperation: %v", err)
	}
	t.Cleanup(sup.Close)
	if !slices.Equal(gotTools, []string{"mcp__calendar__echo"}) {
		t.Fatalf("the member factory got %v, want the root's gateway tool", gotTools)
	}
	member, err := store.Load(t.Context(), session.SessionID(agent.TeamSessionPrefix+"team-1-lead"))
	if err != nil {
		t.Fatalf("load the member: %v", err)
	}
	authority, bound := member.BoundAuthority()
	if !bound || !authority.CapabilitySet.AllowsTool("mcp__calendar__echo") || authority.CapabilitySet.AllowsTool("mcp__notes__ping") {
		t.Fatalf("member authority = %+v (bound %t), want the gateway tool and not the other route", authority.CapabilitySet.Tools, bound)
	}
}

// failingLoadStore is a store whose Load fails with err.
type failingLoadStore struct {
	port.SessionStore
	err error
}

func (f failingLoadStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, f.err
}

// TestTeamHelperToolsRootLoadFailureFailsTheStart pins that a team root the
// server cannot load fails the team's start instead of starting its members
// without the gateway tools they should have: a missing root is not found,
// a store failure is internal, and buildTeamForOperation returns the error.
func TestTeamHelperToolsRootLoadFailureFailsTheStart(t *testing.T) {
	built := 0
	memberFactory := func(_ *team.Team, _ agent.MemberSpec, _ string) agent.MemberBuild {
		built++
		return agent.MemberBuild{Engine: brokerEngineResult().Engine}
	}
	svc, _ := newRegisteringService(t, func(cfg *Config) {
		cfg.MemberEngine = memberFactory
		cfg.MemberEngineWithHelperTools = func(context.Context, []tool.Tool) MemberEngineFactory { return memberFactory }
	})
	svc.mu.Lock()
	svc.brokerAttachments["root"] = helperTestAttachment{tools: []tool.Tool{helperTestTool{name: "mcp__calendar__echo", gateway: true}}}
	svc.mu.Unlock()

	// The root is not in the store.
	if _, _, err := svc.teamHelperTools(t.Context(), "root"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("teamHelperTools for a missing root = %v, want ErrNotFound", err)
	}
	// The store fails.
	svc.cfg.Store = failingLoadStore{SessionStore: svc.cfg.Store, err: errors.New("store unavailable")}
	if _, _, err := svc.teamHelperTools(t.Context(), "root"); !errors.Is(err, ErrInternal) {
		t.Fatalf("teamHelperTools with a failing store = %v, want ErrInternal", err)
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	ts := &teamState{
		env:  tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil),
		name: "reviewers", goal: "goal", rootSessionID: "root",
		specs: []agent.MemberSpec{{Name: "lead", Lead: true}},
	}
	if _, _, err := svc.buildTeamForOperation(t.Context(), "team-1", ts); !errors.Is(err, ErrInternal) {
		t.Fatalf("buildTeamForOperation with a failing store = %v, want ErrInternal", err)
	}
	if built != 0 {
		t.Fatalf("a team whose root could not be loaded built %d members", built)
	}
}
