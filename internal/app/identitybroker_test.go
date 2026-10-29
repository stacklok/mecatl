package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"google.golang.org/grpc/connectivity"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/syscaller"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// closableCore wraps the real broker, recording the workloads it sees and
// whether it was closed.
type closableCore struct {
	*identitybroker.Broker
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

func newClosableCore(t *testing.T) *closableCore {
	t.Helper()
	broker, err := newIdentityBroker(Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &closableCore{Broker: broker.core}
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
	core := newClosableCore(t)
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
	core := newClosableCore(t)
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
	core := newClosableCore(t)
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
	opened, err := built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{HostSessionID: "hostS42", Incarnation: string(session.NewIncarnationID()), Owner: owner})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	found, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: "hostS42"})
	if err != nil || !ok || found != opened {
		t.Fatalf("FindSession = %+v, %v, %v; want %+v, true, nil", found, ok, err, opened)
	}
	// The real broker's session IDs start with bs_.
	if !strings.HasPrefix(opened.ID, "bs_") {
		t.Fatalf("broker session ID %q is not the real broker's", opened.ID)
	}
}

func TestIdentityBrokerRefusesSessionsWithAShell(t *testing.T) {
	for name, cfg := range map[string]Config{
		"a shell":                   {Shell: "/bin/sh"},
		"a shell with Parallel on":  {Shell: "/bin/sh", EnableParallel: true},
		"a shell with a remote env": {Shell: "/bin/sh", RemoteExecution: true},
	} {
		t.Run(name, func(t *testing.T) {
			core := newClosableCore(t)
			cfg.Workspace, cfg.UseMock, cfg.NoSoul, cfg.IdentityBroker, cfg.identityBrokerCore = t.TempDir(), true, true, true, core
			built, err := buildIsolated(t, t.Context(), cfg)
			if err == nil {
				built.Close()
				t.Fatal("Build started the identity broker while sessions have a shell")
			}
			if !errors.Is(err, errIdentityBrokerWithShell) {
				t.Fatalf("Build error = %v, want %v", err, errIdentityBrokerWithShell)
			}
		})
	}
	for name, cfg := range map[string]Config{
		"no shell":         {Shell: ""},
		"the shell is off": {Shell: "/bin/sh", NoShell: true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg.Workspace, cfg.UseMock, cfg.NoSoul, cfg.IdentityBroker = t.TempDir(), true, true, true
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			t.Cleanup(built.Close)
			if built.IdentityBroker == nil {
				t.Fatal("no identity broker was constructed")
			}
		})
	}
	// The Redis filesystem turns the shell off before the broker's check runs.
	t.Run("the Redis filesystem", func(t *testing.T) {
		mr := miniredis.RunT(t)
		built, err := buildIsolated(t, t.Context(), Config{
			RedisURL: mr.Addr(), RedisAllowPlaintext: true, RedisFilesystem: true,
			Shell: "/bin/sh", UseMock: true, NoSoul: true, IdentityBroker: true,
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		t.Cleanup(built.Close)
		if built.IdentityBroker == nil {
			t.Fatal("no identity broker was constructed")
		}
	})
}

func TestIdentityBrokerDefaultWiringRegistersAgents(t *testing.T) {
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true, IdentityBroker: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	owner := identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	incarnation := string(session.NewIncarnationID())
	opened, err := built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{HostSessionID: "hostS42", Incarnation: incarnation, Owner: owner})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	reg, err := built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: opened.ID, HostSessionID: "hostS42", Owner: owner, Incarnation: incarnation,
		Definition: identitybroker.DefinitionRef{Source: "system", Name: "main"},
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if reg.Credential.IsZero() {
		t.Fatal("RegisterAgent returned no credential")
	}
	// The interim configuration issues hour-long credentials.
	if lifetime := time.Until(reg.ExpiresAt); lifetime < 59*time.Minute || lifetime > time.Hour {
		t.Fatalf("credential lifetime = %v, want an hour", lifetime)
	}
}

// TestIdentityBrokerKeysAreSeparatePerRole: the wiring generates a broker
// client key that is not the agent issuer key, nor another build's.
func TestIdentityBrokerKeysAreSeparatePerRole(t *testing.T) {
	broker, err := newIdentityBroker(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if broker.core == nil || broker.client == nil {
		t.Fatalf("newIdentityBroker = %+v, want a core and a client assertion signer", broker)
	}
	workload := identitybroker.Workload{Name: identityBrokerWorkload}
	owner := identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	incarnation := string(session.NewIncarnationID())
	opened, err := broker.core.OpenSession(t.Context(), workload, identitybroker.OpenSessionRequest{HostSessionID: "hostS42", Incarnation: incarnation, Owner: owner})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	reg, err := broker.core.RegisterAgent(t.Context(), workload, identitybroker.RegisterAgentRequest{
		SessionID: opened.ID, HostSessionID: "hostS42", Owner: owner, Incarnation: incarnation,
		Definition: identitybroker.DefinitionRef{Source: "system", Name: "main"},
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	header, err := base64.RawURLEncoding.DecodeString(strings.Split(reg.Credential.Value(), ".")[0])
	if err != nil {
		t.Fatal(err)
	}
	var agentKey struct {
		KID string `json:"kid"`
	}
	if err := json.Unmarshal(header, &agentKey); err != nil || agentKey.KID == "" {
		t.Fatalf("credential header %s: %v", header, err)
	}
	keys := broker.client.JWKS().Keys
	if len(keys) != 1 || keys[0].KeyID == "" || keys[0].KeyID == agentKey.KID {
		t.Fatalf("broker client JWKS %+v; want one key distinct from the agent issuer key %q", keys, agentKey.KID)
	}
	other, err := newIdentityBroker(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if other.client.JWKS().Keys[0].KeyID == keys[0].KeyID {
		t.Fatal("two builds generated the same broker client key")
	}
}

// TestIdentityBrokerWiringConvertsAtAS1: with an AS-1 configured, the real
// broker's session open converts the access token there, authenticating with
// the broker client key.
func TestIdentityBrokerWiringConvertsAtAS1(t *testing.T) {
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(clientKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := identityharness.New(t, identityharness.Options{
		BrokerKeys: []identityharness.BrokerKey{{KeyID: signer.JWKS().Keys[0].KeyID, Public: &clientKey.PublicKey}},
	})
	broker, err := newIdentityBroker(Config{IdentityBrokerAS1: &IdentityBrokerAS1{
		Issuer: h.AS1.Issuer(), TokenEndpoint: h.AS1.TokenEndpoint(), HTTPClient: h.HTTPClient(), ClientKey: clientKey,
	}})
	if err != nil {
		t.Fatal(err)
	}
	workload := identitybroker.Workload{Name: identityBrokerWorkload}
	alice := h.Owner(identityharness.Alice)
	req := identitybroker.OpenSessionRequest{
		HostSessionID: "hostS42", Incarnation: string(session.NewIncarnationID()),
		Owner:       identitybroker.Owner{Issuer: alice.Issuer, Subject: alice.Subject},
		AccessToken: identitybroker.NewToken(h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Alice})),
	}
	opened, err := broker.core.OpenSession(t.Context(), workload, req)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if !opened.HoldsRefreshToken || h.AS1.GrantsIssued() != 1 {
		t.Fatalf("session %+v after %d grants; want it holding the one grant AS-1 issued", opened, h.AS1.GrantsIssued())
	}
	// A reopen without a token, as mecak8s sends to a session holding a
	// refresh token, spends nothing.
	req.AccessToken = identitybroker.Token{}
	again, err := broker.core.OpenSession(t.Context(), workload, req)
	if err != nil || again != opened || h.AS1.GrantsIssued() != 1 {
		t.Fatalf("reopen = %+v, %v after %d grants; want %+v and no new grant", again, err, h.AS1.GrantsIssued(), opened)
	}
}

// TestIdentityBrokerSessionsRegisterTheirAgent is the slice test: with the
// real broker and no shell, a signed-in user's session registers its main agent
// when created, and runs.
func TestIdentityBrokerSessionsRegisterTheirAgent(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), MockProvider: llm, NoSoul: true, NoShell: true, IdentityBroker: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser})
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	open, found, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: string(sess.ID)})
	if err != nil || !found {
		t.Fatalf("FindSession = %v, %v; want the session's broker session", found, err)
	}
	// The main agent is registered: a child naming it as parent passes the
	// parent check (an unregistered parent is PERMISSION_DENIED), then is
	// refused as a takeover of the main agent's incarnation.
	mainAgent := identitybroker.AgentRef{Subject: "spiffe://" + interimIdentityBrokerConfig.TrustDomain + "/mecatl/agent/shared/inst/" + string(sess.Incarnation())}
	_, err = built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: open.ID, HostSessionID: string(sess.ID) + "-child", Owner: open.Owner, Incarnation: string(sess.Incarnation()),
		Definition: identitybroker.DefinitionRef{Source: "system", Name: "main"}, Parent: mainAgent,
	})
	if !errors.Is(err, identitybroker.NewError(identitybroker.CodeAlreadyExists, "", "")) {
		t.Fatalf("a child taking the main agent's incarnation = %v, want ALREADY_EXISTS", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	if llm.Calls() == 0 {
		t.Fatal("the agent did not run")
	}
}

func TestIdentityBrokerConstructionFailureStopsStartup(t *testing.T) {
	// The session store's driver connection is closed on this failure only by
	// the identity broker's own error branch: no deferred cleanup covers the
	// store at this point in Build. The profile lifecycle is closed by Build's
	// earlier deferred cleanup.
	closer := new(countingMCPProfileCloser)
	addr := startSessionStoreDriver(t, memstore.New())
	conns := newDriverConns()
	invalid := identitybroker.Config{}
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true,
		SessionStoreURL: addr, driverConns: conns,
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
	conns.mu.Lock()
	dc := conns.conns[addr]
	conns.mu.Unlock()
	if dc == nil {
		t.Fatal("Build did not dial the session store driver")
	}
	if state := dc.conn.GetState(); state != connectivity.Shutdown {
		t.Fatalf("session store driver connection = %v after the failed startup, want %v", state, connectivity.Shutdown)
	}
}

// countingIdentityBroker counts the broker sessions opened and closed through
// the boundary it wraps.
type countingIdentityBroker struct {
	identitybroker.Service
	mu            sync.Mutex
	opens, closes int
}

func (b *countingIdentityBroker) OpenSession(ctx context.Context, req identitybroker.OpenSessionRequest) (identitybroker.Session, error) {
	b.mu.Lock()
	b.opens++
	b.mu.Unlock()
	return b.Service.OpenSession(ctx, req)
}

func (b *countingIdentityBroker) CloseSession(ctx context.Context, req identitybroker.CloseSessionRequest) error {
	b.mu.Lock()
	b.closes++
	b.mu.Unlock()
	return b.Service.CloseSession(ctx, req)
}

func TestScheduledFireClosesItsBrokerSessionWhenTheRunEnds(t *testing.T) {
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ib, err := newIdentityBroker(Config{})
	if err != nil {
		t.Fatal(err)
	}
	broker := &countingIdentityBroker{Service: identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: identityBrokerWorkload}, ib.core)}
	llm := mockllm.New(mockllm.TextTurn("fire done"))
	engine := agent.NewEngine(agent.Deps{
		LLM: llm, Catalog: tool.NewCatalog(), Model: "test-model", Store: store,
		Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
	})
	svc, err := newTestServerService(server.Config{
		Engine: engine, Store: store, Now: time.Now, DefaultCapabilities: llm.Capabilities(),
		EventLog: store, Diagnostics: port.NopDiagnostics{}, IdentityBroker: broker,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := alicePrincipal
	sched := port.Schedule{Spec: port.ScheduleSpec{
		Name: "owned-fire", Prompt: "say hi", Mode: session.ModePlan, Owner: &owner,
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: t.TempDir(), Revision: "in-tree-v1"},
		PlacementScope: "legacy-local",
	}}
	fire := makeFireFunc(svc, store.ScheduleStore(), defaultFireTimeout, nil)
	rec, err := fire(syscaller.Context(context.Background(), syscaller.RootScheduler), sched, time.Now())
	if err != nil {
		t.Fatalf("fire: %v", err)
	}
	if rec.Stop != session.StopEndTurn {
		t.Fatalf("fire stop = %q, want %q", rec.Stop, session.StopEndTurn)
	}
	broker.mu.Lock()
	opens, closes := broker.opens, broker.closes
	broker.mu.Unlock()
	if opens != 1 || closes != 1 {
		t.Fatalf("broker sessions opened %d and closed %d, want one of each", opens, closes)
	}
	if _, open, err := broker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: string(rec.SessionID)}); err != nil || open {
		t.Fatalf("after the fire, FindSession = %v, %v; want none open", open, err)
	}
}
