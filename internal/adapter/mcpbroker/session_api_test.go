package mcpbroker

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// Supplemental native Runtime proof for parked calls; the composed tests below
// separately prove the real Process/encrypted-custody and MCP execution path.
func TestSessionAPINativeAuthorizationRetainsExactBytes(t *testing.T) {
	t.Run("tool", func(t *testing.T) { sessionAPINativeAuthorizationRetainsExactBytes(t, false) })
	t.Run("query", func(t *testing.T) { sessionAPINativeAuthorizationRetainsExactBytes(t, true) })
}

func sessionAPINativeAuthorizationRetainsExactBytes(t *testing.T, query bool) {
	t.Helper()
	var requests atomic.Int32
	anonymous := toolHiveDiscoveryServer(t, "status", &requests)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []ToolHiveProfile{{Name: "status", URL: anonymous.URL, Auth: authNone}}})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	db := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: db.Addr()})
	defer client.Close()
	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
	api, err := NewSessionAPI(process, client, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "client"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	opened, err := api.OpenSession(owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"test-native-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(tokenServer.Certificate())
	target := &oauthRoute{authorizationEndpoint: "https://accounts.example/authorize", tokenEndpoint: tokenServer.URL, callbackURL: "https://client.example/callback", clientID: "test-client", secretFile: "test-only-secret", scopes: []string{"openid"}}
	routes := []route{{backend: "private", oauth: target, spec: tool.ToolSpec{Name: "mcp__private__create", Description: "create", Schema: json.RawMessage(`{"type":"object"}`)}}}
	var calls atomic.Int32
	var executed []byte
	native, err := New(&Catalogue{routes: routes}, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, errors.New("anonymous dispatch forbidden")
	}, WithAuthorizedCaller(func(_ context.Context, _ SessionRef, _ string, call session.ToolCall, source oauth2.TokenSource) (session.ToolResult, error) {
		if _, err := source.Token(); err != nil {
			return session.ToolResult{}, err
		}
		calls.Add(1)
		executed = append([]byte(nil), call.Args...)
		return session.NewToolResult(call.ID, "authorized"), nil
	}), WithQueryCaller(func(_ context.Context, _ SessionRef, _ string, call session.ToolCall, source oauth2.TokenSource, filter string) (session.ToolResult, error) {
		if !query || filter != ".keep" || call.Name != "mcp__private__create" {
			t.Error("query lost original target/filter")
		}
		if _, err := source.Token(); err != nil {
			return session.ToolResult{}, err
		}
		calls.Add(1)
		executed = append([]byte(nil), call.Args...)
		return session.NewToolResult(call.ID, "authorized"), nil
	}), WithOAuthLoopbackForTest(t, roots), WithOAuthSecretFileReader(func(context.Context, string) (string, error) { return "test-client-secret", nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	a, _ := attach(t, native, session.SessionID(opened.Ref))
	st := api.states[opened.Ref]
	st.attachment = a
	st.record.Connected = true
	st.record.Connection = apiRef()
	st.record.Catalogue = c.CatalogueRef(apiRef())
	st.catalogue, err = api.catalogue(st, st.record.Catalogue, a.Tools(), st.record.Account)
	if err != nil {
		t.Fatal(err)
	}
	if err = api.save(owner, st); err != nil {
		t.Fatal(err)
	}
	rpc, err := mcpbrokergrpc.NewSessionRPC(api)
	if err != nil {
		t.Fatal(err)
	}
	var resumedChecks atomic.Int32
	var exactResumeRef atomic.Value
	exactResumeRef.Store("")
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, r any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if check, ok := r.(*p.CheckAuthorizationRequest); ok && check.GetAuthorizationRef() != "" {
			if check.GetCall() != nil || check.GetAuthorizationRef() != exactResumeRef.Load().(string) {
				t.Error("resumed check resent args or changed auth ref")
			}
			resumedChecks.Add(1)
		}
		if resume, ok := r.(*p.ResumeToolRequest); ok && resume.AuthorizationRef != exactResumeRef.Load().(string) {
			t.Error("resume changed auth ref")
		}
		return h(session.WithPrincipal(ctx, session.PrincipalFromContext(owner)), r)
	}))
	p.RegisterSessionServiceServer(server, rpc)
	listener := bufconn.Listen(1 << 20)
	defer listener.Close()
	defer server.Stop()
	go func() { _ = server.Serve(listener) }()
	remote, err := mcpbrokergrpc.NewSessionClient("passthrough:///protected-session", time.Second, time.Second, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	snapshot, err := remote.OpenSession(owner, &opened.Ref)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{ "title" : "original" }`)
	call := c.Call{ID: "parked", Name: "mcp__private__create", Arguments: append([]byte(nil), original...)}
	if query {
		call.Name = "CallMcpWithQuery"
		call.Arguments = []byte(`{ "server" : "private", "tool" : "create", "args" : { "title" : "original" }, "jq_filter" : ".keep" }`)
	}
	requester := find(snapshot.Catalogue, call.Name).(tool.AuthorizationRequester)
	authRequest, required, err := requester.RequestAuthorization(owner, session.ToolCall{ID: call.ID, Name: call.Name, Args: call.Arguments})
	out := c.InvocationOutcome{Kind: c.InvocationAuthorizationRequired, Authorization: c.AuthorizationRef(authRequest.ID)}
	if err != nil || !required || calls.Load() != 0 {
		t.Fatalf("park: %#v %v", out, err)
	}
	foreign := authRequest
	foreign.Binding = session.AuthorizationBinding(apiRef())
	if err := requester.AbortAuthorization(owner, foreign); err == nil {
		t.Fatal("abort accepted foreign binding")
	}
	cancelled, err := remote.CancelAuthorization(owner, opened.Ref, out.Authorization)
	if err != nil || cancelled != c.Cancelled {
		t.Fatalf("cancel: %v %v", cancelled, err)
	}
	if err := requester.AbortAuthorization(owner, authRequest); err != nil {
		t.Fatalf("abort resolved exact flow: %v", err)
	}
	flow, err := remote.ObserveAuthorization(owner, opened.Ref, out.Authorization)
	if err != nil || flow.Kind != c.FlowCancelled {
		t.Fatalf("cancel observed: %#v %v", flow, err)
	}
	for _, mode := range []string{"cancel", "expire"} {
		t.Run("capacity-"+mode, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				check, err := remote.CheckAuthorization(owner, opened.Ref, st.catalogue.Ref(), &call, "")
				if err != nil || check.Authorization == "" {
					t.Fatalf("cycle %d: %#v %v", i, check, err)
				}
				if mode == "cancel" {
					if _, err := remote.CancelAuthorization(owner, opened.Ref, check.Authorization); err != nil {
						t.Fatal(err)
					}
				} else {
					api.mu.Lock()
					expired := check.ExpiresAt.Add(time.Second)
					api.now = func() time.Time { return expired }
					api.mu.Unlock()
					if _, err := remote.ObserveAuthorization(owner, opened.Ref, check.Authorization); err != nil {
						t.Fatal(err)
					}
					api.mu.Lock()
					api.now = time.Now
					api.mu.Unlock()
				}
				exactResumeRef.Store(string(check.Authorization))
				out, err := remote.ResumeTool(owner, opened.Ref, check.Authorization, st.catalogue.Ref())
				if err != nil || out.Kind != c.InvocationNotDispatched || calls.Load() != 0 {
					t.Fatalf("old resume: %#v %v", out, err)
				}
			}
		})
	}
	api.mu.Lock()
	parkedCount := len(st.parked)
	api.mu.Unlock()
	if parkedCount > 32 {
		t.Fatalf("unbounded terminal records: %d", parkedCount)
	}
	exactResumeRef.Store(string(out.Authorization))
	old, err := remote.ResumeTool(owner, opened.Ref, out.Authorization, st.catalogue.Ref())
	if err != nil || old.Kind != c.InvocationNotDispatched || calls.Load() != 0 {
		t.Fatalf("evicted/cancelled ref resumed: %#v %v", old, err)
	}
	check, err := remote.CheckAuthorization(owner, opened.Ref, st.catalogue.Ref(), &call, "")
	if err != nil || check.Authorization == "" || calls.Load() != 0 {
		t.Fatalf("fresh preflight: %#v %v", check, err)
	}
	auth := check.Authorization
	exactResumeRef.Store(string(auth))
	call.Arguments[2] = 'X' // Caller mutation cannot alter the broker's parked copy.
	prompt, err := remote.BeginAuthorization(owner, opened.Ref, auth)
	if err != nil || !prompt.Valid() {
		t.Fatalf("prompt: %#v %v", prompt, err)
	}
	u, _ := url.Parse(prompt.URL)
	if code := callback(t, native, "test-code", u.Query().Get("state")).Code; code != http.StatusOK {
		t.Fatalf("callback: %d", code)
	}
	flow, err = remote.ObserveAuthorization(owner, opened.Ref, auth)
	if err != nil || flow.Kind != c.FlowCompleted {
		t.Fatalf("observe: %#v %v", flow, err)
	}
	again, err := remote.ObserveAuthorization(owner, opened.Ref, auth)
	if err != nil || again.Catalogue.Ref() != flow.Catalogue.Ref() {
		t.Fatal("observation replaced completed catalogue")
	}
	out, err = remote.ResumeTool(owner, opened.Ref, auth, opened.Catalogue.Ref())
	if err != nil || out.Reason != c.FailureCatalogueChanged || calls.Load() != 0 {
		t.Fatalf("adoption fence: %#v %v", out, err)
	}
	if query {
		api.mu.Lock()
		saved := st.catalogue
		changed := saved.Tools()
		for i, candidate := range changed {
			if candidate.Spec().Name == "mcp__private__create" {
				changed[i] = queryDescriptorDrift{candidate}
			}
		}
		st.catalogue, err = c.NewCatalogue(saved.Ref(), changed)
		api.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		check, err := api.CheckAuthorization(owner, opened.Ref, saved.Ref(), nil, auth)
		if err != nil || check.Reason != c.FailureCatalogueChanged || calls.Load() != 0 {
			t.Fatalf("query target drift preflight: %#v %v", check, err)
		}
		out, err := remote.ResumeTool(owner, opened.Ref, auth, saved.Ref())
		if err != nil || out.Reason != c.FailureCatalogueChanged || calls.Load() != 0 {
			t.Fatalf("query target drift resume: %#v %v", out, err)
		}
		api.mu.Lock()
		st.catalogue = saved
		api.mu.Unlock()
	}
	resumedTool, err := remote.ResumeToolWrapper(opened.Ref, flow.Catalogue, call.Name, call.ID, auth)
	if err != nil {
		t.Fatal(err)
	}
	resumedCall := session.ToolCall{ID: call.ID, Name: call.Name, Args: []byte(`{"title":"must not be sent"}`)}
	_, required, err = resumedTool.(tool.AuthorizationRequester).RequestAuthorization(owner, resumedCall)
	if err != nil || required || calls.Load() != 0 || resumedChecks.Load() != 1 {
		t.Fatalf("resumed preflight: %v %v calls=%d checks=%d", required, err, calls.Load(), resumedChecks.Load())
	}
	result, err := resumedTool.Execute(owner, resumedCall, tool.Environment{})
	if err != nil || result.Content != "authorized" || string(executed) != string(original) || calls.Load() != 1 {
		t.Fatalf("resume exact bytes: err=%v calls=%d", err, calls.Load())
	}
	out, err = remote.ResumeTool(owner, opened.Ref, auth, flow.Catalogue.Ref())
	if err != nil || out.Kind != c.InvocationCompleted || calls.Load() != 1 {
		t.Fatalf("resume receipt: %#v %v", out, err)
	}
}

func TestSessionAPIStableLifetimeOutlivesNativeCustody(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
	client := redis.NewClient(&redis.Options{Addr: f.mini.Addr()})
	defer client.Close()
	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
	api, err := NewSessionAPI(f.process, client, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "client"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	opened, err := api.OpenSession(owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := api.states[opened.Ref]
	guard := c.ContinuityGuard{SessionID: session.SessionID(opened.Ref), SessionIncarnation: st.record.Incarnation, OwnerPartition: st.record.Owner, WorkloadPartition: st.record.Workload, ProfileDigest: f.process.profileDigest, Providers: f.process.providers}
	staged, err := f.process.custody.Stage(owner, custodyRequest{Guard: custodyGuardFromContract(guard), AttemptDeadline: time.Now().Add(time.Minute)}, "verified-tsid")
	if err != nil {
		t.Fatal(err)
	}
	st.record.Custody = &c.StagedCredentialCustody{RecoveryReference: string(staged.Recovery), ExpiresAt: staged.ExpiresAt, ProfileDigest: guard.ProfileDigest, Providers: guard.Providers}
	st.record.Connected = true
	st.record.Connection = apiRef()
	if err = api.save(owner, st); err != nil {
		t.Fatal(err)
	}
	delete(api.states, opened.Ref)
	recovered, err := api.OpenSession(owner, &opened.Ref)
	if err != nil || len(recovered.Catalogue.Tools()) != 2 || find(recovered.Catalogue, "CallMcpWithQuery") == nil {
		t.Fatalf("native custody recovery: %#v %v", recovered, err)
	}
	if !api.states[opened.Ref].record.Custody.ExpiresAt.Equal(staged.ExpiresAt) || !recovered.ExpiresAt.After(staged.ExpiresAt) {
		t.Fatal("recovery renewed custody or shortened session to custody")
	}
	// Advance both owners' clocks, not the persisted expiry. Credentials are
	// fixed-lifetime; the independent session reference remains reopenable.
	later := staged.ExpiresAt.Add(time.Second)
	api.now = func() time.Time { return later }
	f.clock.Set(later)
	f.process.custody.clock = f.clock
	disconnected, err := api.OpenSession(owner, &opened.Ref)
	if err != nil || disconnected.Ref != opened.Ref || len(disconnected.Catalogue.Tools()) != 0 {
		t.Fatalf("expired custody: %#v %v", disconnected, err)
	}
	started, err := api.BeginEnrollment(owner, opened.Ref)
	if err != nil || started.Kind != c.EnrollmentStartedKind || !started.Started.Prompt.Valid() {
		t.Fatalf("explicit reenrollment: %#v %v", started, err)
	}
}

func TestSessionAPIAnonymousNativeLifecycle(t *testing.T) {
	var calls, requests atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "session-poc", Version: "test"}, nil)
	mcpsdk.AddTool(upstream, &mcpsdk.Tool{Name: "echo"}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in struct {
		Value string `json:"value"`
	}) (*mcpsdk.CallToolResult, any, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: in.Value}}}, nil, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); handler.ServeHTTP(w, r) }))
	defer server.Close()
	db := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: db.Addr()})
	defer client.Close()
	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
	workload := func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "broker-client"}
	}
	cfg := ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []ToolHiveProfile{{Name: "echo", URL: server.URL, Auth: authNone}}}
	process, err := NewToolHiveProcess(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	api, err := NewSessionAPI(process, client, workload)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := api.OpenSession(owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Catalogue.Tools()) != 0 || requests.Load() != 0 {
		t.Fatal("Open discovered or exposed tools")
	}
	if _, err = api.OpenSession(owner, new(c.SessionRef)); err == nil {
		t.Fatal("empty saved ref created session")
	}
	foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "other"})
	if _, err = api.OpenSession(foreign, &opened.Ref); err == nil {
		t.Fatal("foreign reopen succeeded")
	}
	enrolled, err := api.BeginEnrollment(owner, opened.Ref)
	if err != nil || !enrolled.Valid() || enrolled.Kind != c.EnrollmentCompletedKind {
		t.Fatalf("enroll: %#v %v", enrolled, err)
	}
	if len(enrolled.Catalogue.Tools()) != 2 || find(enrolled.Catalogue, "CallMcpWithQuery") == nil {
		t.Fatalf("tools: %v", enrolled.Catalogue.ToolNames())
	}
	again, err := api.BeginEnrollment(owner, opened.Ref)
	if err != nil || again.Kind != c.EnrollmentAlreadyConnected {
		t.Fatalf("nonreplacement: %#v %v", again, err)
	}
	call := c.Call{ID: "one", Name: enrolled.Catalogue.ToolNames()[0], Arguments: []byte(`{ "value": "exact" }`)}
	stale, err := api.InvokeTool(owner, opened.Ref, opened.Catalogue.Ref(), call)
	if err != nil || stale.Reason != c.FailureCatalogueChanged {
		t.Fatalf("stale: %#v %v", stale, err)
	}
	ctx, cancel := context.WithCancel(owner)
	done := make(chan c.InvocationOutcome, 1)
	go func() {
		o, e := api.InvokeTool(ctx, opened.Ref, enrolled.Catalogue.Ref(), call)
		if e != nil {
			done <- c.InvocationOutcome{}
			return
		}
		done <- o
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("native execution did not start")
	}
	cancel()
	if out := <-done; out.Kind != c.InvocationOutcomeUnknown {
		t.Fatalf("cancel: %#v", out)
	}
	close(release)
	out, err := api.InvokeTool(owner, opened.Ref, enrolled.Catalogue.Ref(), call)
	if err != nil || out.Kind != c.InvocationCompleted || out.Result.Content != "exact" || calls.Load() != 1 {
		t.Fatalf("receipt: %#v %v calls=%d", out, err, calls.Load())
	}
	changed := call
	changed.Arguments = []byte(`{"value":"changed"}`)
	out, err = api.InvokeTool(owner, opened.Ref, enrolled.Catalogue.Ref(), changed)
	if err != nil || out.Reason != c.FailureCallChanged || calls.Load() != 1 {
		t.Fatalf("changed call: %#v %v", out, err)
	}
	// Lose the entire native Process and facade, retain only Redis metadata.
	if err = process.Close(); err != nil {
		t.Fatal(err)
	}
	process2, err := NewToolHiveProcess(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer process2.Close()
	api2, err := NewSessionAPI(process2, client, workload)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := api2.OpenSession(owner, &opened.Ref)
	if err != nil || recovered.Ref != opened.Ref || len(recovered.Catalogue.Tools()) != 2 || find(recovered.Catalogue, "CallMcpWithQuery") == nil || recovered.Catalogue.Ref() == enrolled.Catalogue.Ref() {
		t.Fatalf("reopen: %#v %v", recovered, err)
	}
	out, err = api2.InvokeTool(owner, opened.Ref, enrolled.Catalogue.Ref(), call)
	if err != nil || out.Reason != c.FailureCatalogueChanged || calls.Load() != 1 {
		t.Fatalf("restart replay: %#v %v", out, err)
	}
	disconnected, err := api2.DisconnectTools(owner, opened.Ref, enrolled.Catalogue.Ref())
	if err != nil || disconnected != c.CatalogueChanged {
		t.Fatalf("wrong disconnect: %v %v", disconnected, err)
	}
	disconnected, err = api2.DisconnectTools(owner, opened.Ref, recovered.Catalogue.Ref())
	if err != nil || disconnected != c.Disconnected {
		t.Fatalf("disconnect: %v %v", disconnected, err)
	}
	out, err = api2.InvokeTool(owner, opened.Ref, recovered.Catalogue.Ref(), call)
	if err != nil || out.Kind != c.InvocationNotDispatched || calls.Load() != 1 {
		t.Fatalf("withdrawn: %#v %v", out, err)
	}
	api3, err := NewSessionAPI(process2, client, workload)
	if err != nil {
		t.Fatal(err)
	}
	disconnectedOpen, err := api3.OpenSession(owner, &opened.Ref)
	if err != nil || len(disconnectedOpen.Catalogue.Tools()) != 0 {
		t.Fatalf("durable disconnected: %#v %v", disconnectedOpen, err)
	}
	deleted, err := api3.DeleteSession(owner, opened.Ref)
	if err != nil || deleted != c.Deleted {
		t.Fatalf("delete: %v %v", deleted, err)
	}
	deleted, err = api3.DeleteSession(owner, opened.Ref)
	if err != nil || deleted != c.AlreadyAbsent {
		t.Fatalf("repeat delete: %v %v", deleted, err)
	}
	if _, err = api3.OpenSession(owner, &opened.Ref); !errors.Is(err, c.ErrStateUnavailable) {
		t.Fatalf("deleted reopened: %v", err)
	}
}

func TestSessionAPIAnonymousAllRequiredFailure(t *testing.T) {
	var requests atomic.Int32
	good := toolHiveDiscoveryServer(t, "ok", &requests)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "offline", http.StatusServiceUnavailable) }))
	defer bad.Close()
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []ToolHiveProfile{{Name: "first", URL: good.URL, Auth: authNone}, {Name: "second", URL: bad.URL, Auth: authNone}}})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	db := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: db.Addr()})
	defer client.Close()
	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
	api, err := NewSessionAPI(process, client, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "client"}
	})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := api.OpenSession(owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.BeginEnrollment(owner, opened.Ref); err == nil {
		t.Fatal("partial enrollment succeeded")
	}
	reopened, err := api.OpenSession(owner, &opened.Ref)
	if err != nil || len(reopened.Catalogue.Tools()) != 0 {
		t.Fatalf("partial publication: %#v %v", reopened, err)
	}
}
