package server

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// continuityFixture composes the real continuity path end to end, offline:
// two independently constructed broker processes over one shared encrypted
// Redis (miniredis) and one KEK, each served over the real broker gRPC
// adapter to a real host Service, real ToolHive incoming middleware and
// upstream injection, and fake local OAuth issuers and MCP upstreams.
//
// Deviation from the full plan fixture: the broker gRPC peer is authenticated
// by an interceptor that installs a fixed workload principal rather than a
// verified workload JWT; the peer-derived workload-partition checks still run.
type continuityFixture struct {
	t        *testing.T
	issuerA  *multiUpstreamOIDC
	issuerB  *multiUpstreamOIDC
	mcpA     *multiUpstreamMCP
	mcpB     *multiUpstreamMCP
	redis    *miniredis.Miniredis
	keyFile  string
	roots    *x509.CertPool
	gateway  *httptest.Server
	profiles []mcpbroker.ToolHiveProfile
	switcher *rebindSwitchConn
	calls    *failingConn
	store    *flakyStore
	workload *session.Principal
	owner    context.Context
	service  *Service
	session  *session.Session

	mu              sync.Mutex
	route           http.Handler
	process         *mcpbroker.Process
	stopRPC         func()
	catalogueBuilds [][]tool.Tool
	factoryCalls    int
}

func newContinuityFixture(t *testing.T) *continuityFixture {
	t.Helper()
	f := &continuityFixture{
		t: t, issuerA: newMultiUpstreamOIDC(t, "backend-a"), issuerB: newMultiUpstreamOIDC(t, "backend-b"),
		mcpA: newMultiUpstreamMCP(t, "backend-a"), mcpB: newMultiUpstreamMCP(t, "backend-b"),
		redis:    miniredis.RunT(t),
		workload: &session.Principal{Issuer: "https://kubernetes.default.svc", Subject: "system:serviceaccount:agents:mecak8s"},
		owner:    session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://idp.example", Subject: "owner", GrantType: session.GrantTypeUser}),
	}
	f.keyFile = filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(f.keyFile, []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatal(err)
	}
	// One stable gateway: the ToolHive issuer is derived from the callback URL,
	// so both broker processes share it and the profile digest stays equal.
	f.gateway = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		route := f.route
		f.mu.Unlock()
		route.ServeHTTP(w, r)
	}))
	f.gateway.StartTLS()
	t.Cleanup(f.gateway.Close)
	f.roots = x509.NewCertPool()
	f.roots.AddCert(f.gateway.Certificate())
	f.profiles = []mcpbroker.ToolHiveProfile{
		{Name: "backend-a", URL: f.mcpA.server.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{Issuer: f.issuerA.server.URL, ClientID: f.issuerA.clientID, Scopes: []string{"openid"}, RequestRefreshToken: true}},
		{Name: "backend-b", URL: f.mcpB.server.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{Issuer: f.issuerB.server.URL, ClientID: f.issuerB.clientID, Scopes: []string{"openid"}, RequestRefreshToken: true}},
	}
	f.switcher = &rebindSwitchConn{}
	f.calls = &failingConn{next: f.switcher}
	f.startBroker()
	pinned, err := mcpbrokergrpc.NewClientWithConfig(f.calls, mcpbrokergrpc.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	f.store = &flakyStore{SessionStore: memstore.New()}
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: f.store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test", NewID: func() session.SessionID { return "continuity-e2e" },
		MCPBroker: pinned, MCPBrokerClose: func() error { return nil }, WorkspaceEnrollment: true,
		MCPBrokerFactory: func(context.Context) (brokercontract.Service, func() error, error) {
			f.mu.Lock()
			f.factoryCalls++
			f.mu.Unlock()
			fresh, freshErr := mcpbrokergrpc.NewClientWithConfig(f.calls, mcpbrokergrpc.DefaultConfig())
			return fresh, func() error { return nil }, freshErr
		},
		BrokerWorkloadIdentity: f.workload,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__backend-a__whoami", "mcp__backend-b__whoami"}}, Provenance: "continuity-e2e"}
		},
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.catalogueBuilds = append(f.catalogueBuilds, append([]tool.Tool(nil), tools...))
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	f.service = svc
	t.Cleanup(svc.Close)
	if f.session, err = svc.CreateSession(f.owner, session.ModeDefault, session.Limits{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return f
}

// startBroker constructs a fresh broker process over the shared encrypted
// Redis and routes both the gateway and the host's gRPC connection to it.
func (f *continuityFixture) startBroker() *mcpbroker.Process {
	f.t.Helper()
	protected := &mcpbroker.ProtectedStorageConfig{
		Redis: mcpbroker.ProtectedRedisConfig{
			Client: func(mcpbroker.ProtectedRedisClientConfig) (redis.UniversalClient, error) {
				return redis.NewClient(&redis.Options{Addr: f.redis.Addr()}), nil
			},
			ClientConfig:  mcpbroker.ProtectedRedisClientConfig{TLS: true},
			HealthTimeout: time.Second,
		},
		Encryption: mcpbroker.ProtectedEncryptionConfig{ActiveID: "current", Keys: []mcpbroker.ProtectedEncryptionKey{{ID: "current", File: f.keyFile}}},
	}
	process, err := mcpbroker.NewToolHiveProcess(f.t.Context(), mcpbroker.ToolHiveConfig{CallbackURL: f.gateway.URL + "/callback", Profiles: f.profiles, ProtectedStorage: protected},
		mcpbroker.WithOAuthLoopbackForTest(f.t, f.roots), mcpbroker.WithBrokerHTTPClientForTest(f.t, f.gateway.Client()))
	if err != nil {
		f.t.Fatalf("NewToolHiveProcess: %v", err)
	}
	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		f.t.Fatalf("mount ToolHive handlers: %v", err)
	}
	conn, stop := serveContinuityBroker(f.t, process, f.workload)
	f.mu.Lock()
	f.route, f.process, f.stopRPC = mux, process, stop
	f.mu.Unlock()
	f.switcher.set(conn)
	f.t.Cleanup(func() { stop(); _ = process.Close() })
	return process
}

// replaceBroker loses B1 entirely and starts B2 over the same encrypted storage.
func (f *continuityFixture) replaceBroker() {
	f.t.Helper()
	f.mu.Lock()
	old, stop := f.process, f.stopRPC
	f.mu.Unlock()
	stop()
	_ = old.Close()
	f.startBroker()
}

func serveContinuityBroker(t *testing.T, service brokercontract.Service, workload *session.Principal) (grpc.ClientConnInterface, func()) {
	t.Helper()
	return serveRebindBrokerWithOptions(t, service, grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(session.WithPrincipal(ctx, workload.Clone()), req)
	}))
}

func (f *continuityFixture) enroll() {
	f.t.Helper()
	started, err := f.service.ConnectWorkspaceServices(f.t.Context(), f.session.ID)
	if err != nil || started.Status != brokercontract.WorkspaceEnrollmentPending || started.URL == "" {
		f.t.Fatalf("start enrollment = %#v, %v", started, err)
	}
	client := f.gateway.Client()
	client.Timeout = 8 * time.Second
	response, err := client.Get(started.URL)
	if err != nil {
		f.t.Fatalf("authorization chain: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		f.t.Fatalf("authorization callback status = %d", response.StatusCode)
	}
	connected, err := f.service.ConnectWorkspaceServices(f.t.Context(), f.session.ID)
	if err != nil || connected.Status != brokercontract.WorkspaceEnrollmentConnected {
		f.t.Fatalf("complete enrollment = %#v, %v", connected, err)
	}
}

func (f *continuityFixture) stored() *session.Session {
	f.t.Helper()
	loaded, err := f.service.cfg.Store.Load(f.t.Context(), f.session.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return loaded
}

func (f *continuityFixture) latestTools() []tool.Tool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.catalogueBuilds) == 0 {
		return nil
	}
	return f.catalogueBuilds[len(f.catalogueBuilds)-1]
}

func (f *continuityFixture) rebuildEngineAfterReplacement() {
	f.t.Helper()
	f.service.closeSessionLocal(f.session.ID)
	sess := f.stored()
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	if _, err := f.service.buildAndRegisterSessionEngine(f.t.Context(), sess, sel, profileForSession(sess), sess.Mode, true); err != nil {
		f.t.Fatalf("run-entry engine build after broker replacement: %v", err)
	}
}

func TestBrokerCredentialContinuity_Scenario2_ReplacementRebuildsFreshAuthority(t *testing.T) {
	f := newContinuityFixture(t)
	f.enroll()
	enrolled := f.stored()
	custody, ok := enrolled.BrokerCredentialCustody()
	if !ok {
		t.Fatal("completed enrollment holds no credential custody")
	}
	oldBinding := enrolled.ExternalBinding
	initialA, _ := f.issuerA.counts()
	initialB, _ := f.issuerB.counts()

	f.replaceBroker()
	f.rebuildEngineAfterReplacement()

	recovered := f.stored()
	if recovered.ExternalBinding == "" || recovered.ExternalBinding == oldBinding {
		t.Fatalf("binding = %q, want a fresh B2 binding replacing %q", recovered.ExternalBinding, oldBinding)
	}
	if after, ok := recovered.BrokerCredentialCustody(); !ok || after.RecoveryReference() != custody.RecoveryReference() {
		t.Fatal("recovery changed or dropped the retained custody")
	}
	if a, _ := f.issuerA.counts(); a != initialA {
		t.Fatalf("recovery repeated browser authorization for backend-a (%d -> %d)", initialA, a)
	}
	if b, _ := f.issuerB.counts(); b != initialB {
		t.Fatalf("recovery repeated browser authorization for backend-b (%d -> %d)", initialB, b)
	}
	if f.factoryCalls != 1 {
		t.Fatalf("broker client replacements = %d, want 1", f.factoryCalls)
	}

	// The recovered B2 catalogue executes through the real middleware and
	// injects the retained upstream credential.
	whoami := toolFromFixture(t, f.latestTools(), "mcp__backend-a__whoami")
	result, err := whoami.Execute(t.Context(), session.NewToolCall("after-recovery", whoami.Spec().Name, json.RawMessage(`{}`)), tool.Environment{})
	if err != nil || result.IsError || !strings.Contains(result.Content, "backend-a") {
		t.Fatalf("recovered tool result = %+v, %v", result, err)
	}
	headers, _ := f.mcpA.snapshot()
	assertOnlyBearer(t, headers, "Bearer "+f.issuerA.accessToken)
}

func TestBrokerCredentialContinuity_Scenario2_AllOuterStateIsFresh(t *testing.T) {
	f := newContinuityFixture(t)
	f.enroll()
	oldBinding := string(f.stored().ExternalBinding)
	oldPrefix, _, _ := strings.Cut(oldBinding, ".")
	f.replaceBroker()
	f.rebuildEngineAfterReplacement()
	newPrefix, _, _ := strings.Cut(string(f.stored().ExternalBinding), ".")
	if newPrefix == "" || newPrefix == oldPrefix {
		t.Fatalf("binding prefix %q -> %q; want a fresh broker instance identity", oldPrefix, newPrefix)
	}
	// B2 holds no B1 logical state: the old binding classifies as instance loss.
	attacher := f.service.brokerService().(brokercontract.ExpectedBindingAttacher)
	if _, _, err := attacher.AttachSessionExpectedBinding(t.Context(), f.session.ID, session.ExternalBinding(oldBinding)); !errorsIsInstanceLost(err) {
		t.Fatalf("attach with B1 binding on B2 = %v, want structured instance loss", err)
	}
}

func TestBrokerCredentialContinuity_Scenario2_FixedCustodyRetention(t *testing.T) {
	f := newContinuityFixture(t)
	f.enroll()
	before, _ := f.stored().BrokerCredentialCustody()
	f.replaceBroker()
	f.rebuildEngineAfterReplacement()
	after, _ := f.stored().BrokerCredentialCustody()
	if !after.ExpiresAt().Equal(before.ExpiresAt()) {
		t.Fatalf("custody expiry %v -> %v; recovery must never extend native retention", before.ExpiresAt(), after.ExpiresAt())
	}
}

func TestBrokerCredentialContinuity_Scenario3_PendingBrowserFlowIsInterrupted(t *testing.T) {
	f := newContinuityFixture(t)
	started, err := f.service.ConnectWorkspaceServices(t.Context(), f.session.ID)
	if err != nil || started.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("start = %#v, %v", started, err)
	}
	f.replaceBroker()
	f.service.closeSessionLocal(f.session.ID)
	fresh, err := f.service.ConnectWorkspaceServices(t.Context(), f.session.ID)
	if err != nil || fresh.Status != brokercontract.WorkspaceEnrollmentPending || fresh.Ref.ID == started.Ref.ID || fresh.URL == started.URL {
		t.Fatalf("after replacement = %#v, %v; want a fresh browser enrollment, not the B1 flow", fresh, err)
	}
	if _, custody := f.stored().BrokerCredentialCustody(); custody {
		t.Fatal("an unfinished browser flow produced custody")
	}
	if a, _ := f.issuerA.counts(); a != 0 {
		t.Fatalf("B1's browser flow was resumed on B2 (%d token exchanges)", a)
	}
}

func errorsIsInstanceLost(err error) bool {
	return err != nil && errors.Is(err, brokercontract.ErrBrokerIncarnationLost)
}

// A broker restarted with a changed OAuth profile refuses the stored custody
// with the distinct profile-changed reason over gRPC; the remote host then
// invalidates the custody instead of failing on every attempt until expiry.
func TestBrokerCredentialContinuity_RemoteProfileChangeInvalidatesCustody(t *testing.T) {
	f := newContinuityFixture(t)
	f.enroll()
	if _, custody := f.stored().BrokerCredentialCustody(); !custody {
		t.Fatal("enrollment produced no custody")
	}
	changed := *f.profiles[0].OAuth
	changed.Scopes = []string{"openid", "profile"}
	f.profiles[0].OAuth = &changed
	f.replaceBroker()
	f.service.closeSessionLocal(f.session.ID)
	sess := f.stored()
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	if _, err := f.service.buildAndRegisterSessionEngine(t.Context(), sess, sel, profileForSession(sess), sess.Mode, true); err == nil {
		t.Fatal("recovery succeeded against a changed broker profile")
	}
	if _, custody := f.stored().BrokerCredentialCustody(); custody {
		t.Fatal("custody survived a remote broker profile change")
	}
}

// failingConn fails the next call to one gRPC method, simulating a broker lost
// or unreachable at an exact point in the host's sequence.
type failingConn struct {
	next  grpc.ClientConnInterface
	mu    sync.Mutex
	fail  string
	count map[string]int
}

func (c *failingConn) calls(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count[method]
}

func (c *failingConn) failNext(method string) {
	c.mu.Lock()
	c.fail = method
	c.mu.Unlock()
}

func (c *failingConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	c.mu.Lock()
	if c.count == nil {
		c.count = map[string]int{}
	}
	c.count[method]++
	fail := c.fail == method
	if fail {
		c.fail = ""
	}
	c.mu.Unlock()
	if fail {
		return status.Error(codes.Unavailable, "injected broker loss")
	}
	return c.next.Invoke(ctx, method, args, reply, opts...)
}

func (c *failingConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.next.NewStream(ctx, desc, method, opts...)
}

// B1 is lost after the host durably saved pending+custody but before custody
// Commit. B2 must Commit the still-staged custody, then Recover, and complete
// the original enrollment with a fresh binding and no second browser flow.
func TestBrokerCredentialContinuity_Scenario2_SaveCommitCrashOrdering(t *testing.T) {
	f := newContinuityFixture(t)
	started, err := f.service.ConnectWorkspaceServices(t.Context(), f.session.ID)
	if err != nil || started.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("start = %#v, %v", started, err)
	}
	client := f.gateway.Client()
	client.Timeout = 8 * time.Second
	response, err := client.Get(started.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	f.calls.failNext("/mecatl.broker.v1.BrokerService/CommitCredentialCustody")
	if _, err := f.service.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("completion succeeded although custody Commit was lost")
	}
	pending := f.stored()
	if _, stillPending := pending.PendingWorkspaceEnrollment(); !stillPending {
		t.Fatal("lost Commit completed the enrollment")
	}
	if _, custody := pending.BrokerCredentialCustody(); !custody {
		t.Fatal("staged custody was not durable before Commit")
	}
	oldBinding := pending.ExternalBinding
	initialA, _ := f.issuerA.counts()

	f.replaceBroker()
	f.service.closeSessionLocal(f.session.ID)
	connected, err := f.service.ConnectWorkspaceServices(t.Context(), f.session.ID)
	if err != nil || connected.Status != brokercontract.WorkspaceEnrollmentConnected || connected.Ref.ID != started.Ref.ID {
		t.Fatalf("recovery after lost Commit = %#v, %v; want the original enrollment connected", connected, err)
	}
	recovered := f.stored()
	if _, stillPending := recovered.PendingWorkspaceEnrollment(); stillPending || recovered.ExternalBinding == oldBinding {
		t.Fatalf("pending=%v binding %q -> %q; want completed with a fresh binding", stillPending, oldBinding, recovered.ExternalBinding)
	}
	if a, _ := f.issuerA.counts(); a != initialA {
		t.Fatal("recovery repeated the browser authorization")
	}
}

// Recovery itself never re-executes a protected call: upstream sees no calls
// until the host explicitly runs one on the recovered catalogue.
func TestBrokerCredentialContinuity_Scenario3_NoOuterOrExecutionRecovery(t *testing.T) {
	f := newContinuityFixture(t)
	f.enroll()
	whoami := toolFromFixture(t, f.latestTools(), "mcp__backend-a__whoami")
	if result, err := whoami.Execute(t.Context(), session.NewToolCall("before-replacement", whoami.Spec().Name, json.RawMessage(`{}`)), tool.Environment{}); err != nil || result.IsError {
		t.Fatalf("pre-replacement call = %+v, %v", result, err)
	}
	_, callsBefore := f.mcpA.snapshot()
	f.replaceBroker()
	f.rebuildEngineAfterReplacement()
	if _, callsAfter := f.mcpA.snapshot(); callsAfter != callsBefore {
		t.Fatalf("recovery dispatched %d upstream tool calls on its own", callsAfter-callsBefore)
	}
}

// flakyStore can make the next Save land and still report an error, the
// ambiguous outcome a store gives when its reply is lost.
type flakyStore struct {
	port.SessionStore
	mu               sync.Mutex
	landThenFailNext bool
}

func (s *flakyStore) Save(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	ambiguous := s.landThenFailNext
	s.landThenFailNext = false
	s.mu.Unlock()
	if err := s.SessionStore.Save(ctx, sess); err != nil {
		return err
	}
	if ambiguous {
		return errors.New("store reply lost")
	}
	return nil
}

const recoverMethod = "/mecatl.broker.v1.BrokerService/RecoverCredentialAttachment"

// The recovered-authority Save lands but reports an error. The host must
// reload, see the persisted B2 binding, keep ownership (never Abort), Commit,
// and finish; the session keeps working on B2.
func TestBrokerCredentialContinuity_Scenario2_AmbiguousAdoptionSaveCommits(t *testing.T) {
	f := newContinuityFixture(t)
	f.enroll()
	oldBinding := f.stored().ExternalBinding
	f.replaceBroker()
	f.store.mu.Lock()
	f.store.landThenFailNext = true
	f.store.mu.Unlock()
	f.rebuildEngineAfterReplacement()
	if got := f.stored().ExternalBinding; got == oldBinding {
		t.Fatalf("binding stayed %q after an ambiguous but durable adoption save", got)
	}
	whoami := toolFromFixture(t, f.latestTools(), "mcp__backend-a__whoami")
	if result, err := whoami.Execute(t.Context(), session.NewToolCall("after-ambiguous-save", whoami.Spec().Name, json.RawMessage(`{}`)), tool.Environment{}); err != nil || result.IsError {
		t.Fatalf("recovered tool after ambiguous save = %+v, %v", result, err)
	}
}

// Host assertion boundary: if the host's own workload identity changed since
// custody was staged, the host invalidates custody and never asks B2 to recover.
func TestBrokerCredentialContinuity_Scenario2_HostAssertionBoundary(t *testing.T) {
	f := newContinuityFixture(t)
	f.enroll()
	f.replaceBroker()
	f.service.cfg.BrokerWorkloadIdentity = &session.Principal{Issuer: f.workload.Issuer, Subject: "system:serviceaccount:agents:rotated"}
	f.service.closeSessionLocal(f.session.ID)
	sess := f.stored()
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	if _, err := f.service.buildAndRegisterSessionEngine(t.Context(), sess, sel, profileForSession(sess), sess.Mode, true); err == nil {
		t.Fatal("recovery succeeded after the host workload identity changed")
	}
	if got := f.calls.calls(recoverMethod); got != 0 {
		t.Fatalf("host sent %d Recover RPCs after its workload identity changed", got)
	}
	if _, custody := f.stored().BrokerCredentialCustody(); custody {
		t.Fatal("custody survived a host workload identity change")
	}
}

// A session past its first prompt keeps its protected continuations
// interrupted: after broker replacement the host refuses recovery, never
// contacts B2 for it, and does not destroy the custody.
func TestBrokerCredentialContinuity_Scenario2_ProtectedContinuationNeverRecovers(t *testing.T) {
	f := newContinuityFixture(t)
	f.enroll()
	sess := f.stored()
	if err := sess.RecordUserPrompt("already started", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.service.cfg.Store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	f.replaceBroker()
	f.service.closeSessionLocal(f.session.ID)
	sess = f.stored()
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	if _, err := f.service.buildAndRegisterSessionEngine(t.Context(), sess, sel, profileForSession(sess), sess.Mode, true); err == nil {
		t.Fatal("a session with history recovered broker authority")
	}
	if got := f.calls.calls(recoverMethod); got != 0 {
		t.Fatalf("host sent %d Recover RPCs for a session past its first prompt", got)
	}
	if _, custody := f.stored().BrokerCredentialCustody(); !custody {
		t.Fatal("refusing recovery destroyed custody")
	}
}
