package server

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/identitybroker/conformance"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// gatewayTestAttachment is a broker attachment whose catalogue holds a
// gateway route and a route that acts as the session, plus the query tool.
type gatewayTestAttachment struct {
	helperTestAttachment
	binding            session.ExternalBinding
	committed, aborted *atomic.Int32
}

func (a gatewayTestAttachment) Commit(context.Context) error {
	a.committed.Add(1)
	return nil
}
func (a gatewayTestAttachment) Abort(context.Context) error {
	a.aborted.Add(1)
	return nil
}
func (a gatewayTestAttachment) Binding() session.ExternalBinding { return a.binding }
func (gatewayTestAttachment) CallMcpWithQueryTool() tool.Tool {
	return helperTestTool{name: "CallMcpWithQuery"}
}

// gatewayTestBroker hands each session its own gatewayTestAttachment, with
// extra in its catalogue too.
type gatewayTestBroker struct {
	attached, committed, aborted atomic.Int32
	extra                        []tool.Tool
}

func (b *gatewayTestBroker) AttachSession(_ context.Context, id session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	b.attached.Add(1)
	return gatewayTestAttachment{
		helperTestAttachment: helperTestAttachment{tools: append([]tool.Tool{
			helperTestTool{name: "mcp__calendar__echo", gateway: true},
			helperTestTool{name: "mcp__notes__ping"},
		}, b.extra...)},
		binding:   session.ExternalBinding("binding-" + string(id)),
		committed: &b.committed, aborted: &b.aborted,
	}, brokercontract.AttachCreated, nil
}

func (*gatewayTestBroker) DeleteSession(context.Context, session.SessionID) (brokercontract.DeleteOutcome, error) {
	return brokercontract.DeleteDeleted, nil
}

// gatewayToolsSeen records the gateway tools each agent-bound engine build
// received, failing the build when fail is set.
type gatewayToolsSeen struct {
	mu    sync.Mutex
	names [][]string
	fail  bool
}

func (g *gatewayToolsSeen) factory() AgentDefSessionEngineFactory {
	return func(_ context.Context, _ ProviderSelector, _ SessionProfile, _ session.PermissionMode, _ session.Limits, name string, gatewayTools []tool.Tool) (SessionEngineResult, error) {
		gateway := toolNames(gatewayTools)
		g.mu.Lock()
		g.names = append(g.names, gateway)
		fail := g.fail
		g.mu.Unlock()
		if fail {
			return SessionEngineResult{}, fmt.Errorf("%w: unknown agent definition %q", ErrInvalidArgument, name)
		}
		// Like the real factory, mint the authority from the catalog, which
		// holds the gateway tools.
		res := agentDefinitionEngineResult(name)
		res.Authority.CapabilitySet.Tools = append([]string{"Read"}, gateway...)
		ceiling := res.Authority.CapabilitySet
		res.Authority.Ceiling = &ceiling
		return res, nil
	}
}

func (g *gatewayToolsSeen) builds() [][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.names)
}

// newAgentBoundGatewayService serves agent-bound sessions with an MCP broker
// whose catalogue holds a gateway route, with the identity broker on or off.
func newAgentBoundGatewayService(t *testing.T, identityBroker bool) (*Service, *gatewayTestBroker, *gatewayToolsSeen) {
	t.Helper()
	broker := &gatewayTestBroker{}
	seen := &gatewayToolsSeen{}
	cfg := Config{
		Engine:                brokerEngineResult().Engine,
		Store:                 memstore.New(),
		PlacementProvider:     brokerPlacementProvider{},
		PlacementScope:        "test",
		MCPBroker:             broker,
		AgentDefSessionEngine: seen.factory(),
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool, []string) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	}
	if identityBroker {
		cfg.IdentityBroker = convertingBroker(t, &switchableAS1{})
		cfg.IdentityDefinitions = conformance.DefinitionRefs()
	}
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, broker, seen
}

// TestAgentBoundSessionGetsOnlyItsAttachmentsGatewayRoutes pins #2938 at the
// service: with the identity broker on, an agent-bound session attaches to
// the MCP broker, persists and commits that attachment, and its engine
// factory receives the attachment's gateway routes and no other broker
// route. Fork and Clear successors, and a rebuild of the live registration,
// get the same.
func TestAgentBoundSessionGetsOnlyItsAttachmentsGatewayRoutes(t *testing.T) {
	svc, broker, seen := newAgentBoundGatewayService(t, true)
	ctx := session.WithPrincipal(t.Context(), mcpAuthorizationOwner)
	want := []string{"mcp__calendar__echo"}
	wantBuild := func(step string) {
		t.Helper()
		builds := seen.builds()
		if len(builds) == 0 || !slices.Equal(builds[len(builds)-1], want) {
			t.Fatalf("%s: the engine factory received gateway tools %v, want %v", step, builds, want)
		}
	}

	created, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("release-reviewer"))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	wantBuild("create")
	if created.ExternalBinding != session.ExternalBinding("binding-"+string(created.ID)) {
		t.Fatalf("ExternalBinding = %q, want the attachment's binding", created.ExternalBinding)
	}
	if broker.attached.Load() != 1 || broker.committed.Load() != 1 {
		t.Fatalf("attached %d, committed %d; want one committed attachment", broker.attached.Load(), broker.committed.Load())
	}

	// A rebuild of the live registration, as runtime publication does,
	// reopens the session's own committed attachment. A gateway tool
	// published since the session's authority was minted is not offered.
	svc.mu.Lock()
	resident := svc.brokerAttachments[created.ID].(gatewayTestAttachment)
	resident.tools = append(slices.Clone(resident.tools), helperTestTool{name: "mcp__calendar__new", gateway: true})
	svc.brokerAttachments[created.ID] = resident
	svc.mu.Unlock()
	if _, err := svc.buildAndRegisterSessionEngine(ctx, created, storedProviderSelector(created), ProfileDefault, created.Mode, true); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	wantBuild("rebuild")
	if broker.attached.Load() != 1 {
		t.Fatalf("the rebuild attached %d more time(s), want it to reuse the session's attachment", broker.attached.Load()-1)
	}

	// A successor's fresh attachment holds a gateway tool the inherited
	// authority does not allow; it is not offered either.
	broker.extra = []tool.Tool{helperTestTool{name: "mcp__calendar__new", gateway: true}}
	if _, err := svc.ForkSessionSuccessor(ctx, ForkSuccessorRequest{Source: created.ID}); err != nil {
		t.Fatalf("fork: %v", err)
	}
	wantBuild("fork")
	if _, err := svc.ClearSessionSuccessor(ctx, created.ID, SuccessorPlacement{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	wantBuild("clear")
	if broker.attached.Load() != 3 || broker.committed.Load() != 3 {
		t.Fatalf("attached %d, committed %d; want one committed attachment per session", broker.attached.Load(), broker.committed.Load())
	}
}

// TestAgentBoundSessionWithoutIdentityBrokerAttachesNothing pins that,
// without the identity broker, which alone installs gateway routes and checks
// their calls, an agent-bound session still attaches to nothing.
func TestAgentBoundSessionWithoutIdentityBrokerAttachesNothing(t *testing.T) {
	svc, broker, seen := newAgentBoundGatewayService(t, false)
	created, err := svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{},
		ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("release-reviewer"))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if broker.attached.Load() != 0 || created.ExternalBinding != "" {
		t.Fatalf("attached %d, binding %q; want no attachment", broker.attached.Load(), created.ExternalBinding)
	}
	if builds := seen.builds(); len(builds) != 1 || len(builds[0]) != 0 {
		t.Fatalf("the engine factory received gateway tools %v, want none", builds)
	}
}

// TestAgentBoundSessionListsNoBrokerConnectors pins that an agent-bound
// session's broker attachment, which holds only its gateway tools, is not
// reported as a connector inventory.
func TestAgentBoundSessionListsNoBrokerConnectors(t *testing.T) {
	svc, _, _ := newAgentBoundGatewayService(t, true)
	svc.cfg.OwnershipEnforced = true
	spy := &connectorInspectorSpy{}
	svc.cfg.MCPConnectorInspector = spy
	ctx := session.WithPrincipal(t.Context(), mcpAuthorizationOwner)
	created, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("release-reviewer"))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := svc.ListSessionMcpConnectors(ctx, created.ID); err != errConnectorUnavailable || spy.calls != 0 {
		t.Fatalf("ListSessionMcpConnectors = %v after %d inspection(s), want connector inspection unavailable", err, spy.calls)
	}
}

// TestAgentBoundSessionAbortsItsAttachmentWhenTheBuildFails pins the
// attachment's rollback: when the engine build of an agent-bound session
// fails, its broker attachment is aborted, never committed or persisted.
func TestAgentBoundSessionAbortsItsAttachmentWhenTheBuildFails(t *testing.T) {
	svc, broker, seen := newAgentBoundGatewayService(t, true)
	seen.fail = true
	_, err := svc.CreateSessionWithProfile(session.WithPrincipal(t.Context(), mcpAuthorizationOwner), session.ModeDefault, session.Limits{},
		ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("release-reviewer"))
	if err == nil {
		t.Fatal("CreateSession succeeded with a failing engine build")
	}
	if broker.attached.Load() != 1 || broker.aborted.Load() != 1 || broker.committed.Load() != 0 {
		t.Fatalf("attached %d, aborted %d, committed %d; want one aborted attachment", broker.attached.Load(), broker.aborted.Load(), broker.committed.Load())
	}
}
