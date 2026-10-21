package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/standin"
)

// closableCore is a stand-in core that records the workloads it sees and
// whether it was closed.
type closableCore struct {
	*standin.Broker
	mu        sync.Mutex
	workloads []identitybroker.Workload
	closed    int
}

func (c *closableCore) FindSession(ctx context.Context, workload identitybroker.Workload, req identitybroker.FindSessionRequest) (identitybroker.Session, bool, error) {
	c.mu.Lock()
	c.workloads = append(c.workloads, workload)
	c.mu.Unlock()
	return c.Broker.FindSession(ctx, workload, req)
}

func (c *closableCore) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

func TestIdentityBrokerIsOffByDefault(t *testing.T) {
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.IdentityBroker != nil {
		t.Fatal("an identity broker was constructed with the flag off")
	}
}

func TestIdentityBrokerCallsCarryTheUnverifiedWorkload(t *testing.T) {
	core := &closableCore{Broker: standin.New()}
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true, IdentityBroker: true, identityBrokerCore: core})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.IdentityBroker == nil {
		t.Fatal("no identity broker was constructed with the flag on")
	}
	// Service and Core cannot share an implementation, so Mecatl cannot hold
	// the core; calls go through the boundary, which supplies the workload.
	if _, _, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: "hostS42"}); err != nil {
		t.Fatalf("FindSession: %v", err)
	}
	want := identitybroker.Workload{Name: identityBrokerWorkload}
	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.workloads) != 1 || core.workloads[0] != want || core.workloads[0].Verified {
		t.Fatalf("core workloads = %+v, want one unverified %+v", core.workloads, want)
	}
}

func TestIdentityBrokerIsClosedOnShutdown(t *testing.T) {
	core := &closableCore{Broker: standin.New()}
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true, IdentityBroker: true, identityBrokerCore: core})
	if err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	closedBeforeShutdown := core.closed
	core.mu.Unlock()
	if closedBeforeShutdown != 0 {
		t.Fatal("the identity broker was closed before shutdown")
	}
	built.Close()
	built.Close()
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.closed != 1 {
		t.Fatalf("identity broker closed %d times, want once", core.closed)
	}
}

func TestIdentityBrokerIsClosedWhenLaterStartupFails(t *testing.T) {
	core := &closableCore{Broker: standin.New()}
	// A protected MCP route without a callback URL makes the bundled MCP broker,
	// built after the identity broker, fail to construct.
	cfg := Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true, IdentityBroker: true, identityBrokerCore: core,
		MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{protectedToolHiveRoute("calendar")}})}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err == nil {
		built.Close()
		t.Fatal("Build succeeded; the test needs a startup failure after the identity broker is built")
	}
	if !strings.Contains(err.Error(), "MCP broker") {
		t.Fatalf("Build failed before the MCP broker, so the identity broker may not have been built: %v", err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.closed != 1 {
		t.Fatalf("identity broker closed %d times after a failed startup, want once", core.closed)
	}
}

func TestIdentityBrokerDefaultWiringOpensSessions(t *testing.T) {
	// No core override: this builds the real broker and the production
	// authenticator.
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true, IdentityBroker: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.IdentityBroker == nil {
		t.Fatal("no identity broker was constructed with the flag on")
	}
	owner := identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	opened, err := built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{HostSessionID: "hostS42", Incarnation: "inc_hostS42", Owner: owner})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	found, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: "hostS42"})
	if err != nil || !ok || found != opened {
		t.Fatalf("FindSession = %+v, %v, %v; want %+v, true, nil", found, ok, err, opened)
	}
	// The real broker's session IDs start with bs_; the stand-in's do not.
	if !strings.HasPrefix(opened.ID, "bs_") {
		t.Fatalf("broker session ID %q is not the real broker's", opened.ID)
	}
}

func TestIdentityBrokerConstructionFailureStopsStartup(t *testing.T) {
	// The profile lifecycle is built before the identity broker, so its close
	// shows that a failed startup releases what was already built.
	closer := new(countingMCPProfileCloser)
	invalid := identitybroker.Config{}
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true,
		IdentityBroker: true, identityBrokerConfig: &invalid, MCPProfileLifecycle: closer})
	if err == nil {
		built.Close()
		t.Fatal("Build succeeded with an invalid identity broker configuration")
	}
	if !errors.Is(err, identitybroker.ErrInvalidConfig) {
		t.Fatalf("Build error = %v, want the identity broker's invalid configuration", err)
	}
	if got := closer.calls.Load(); got != 1 {
		t.Fatalf("profile lifecycle Close calls = %d, want 1", got)
	}
}
