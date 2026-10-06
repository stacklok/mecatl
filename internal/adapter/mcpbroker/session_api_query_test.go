package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestSessionAPIQueryNativeProjectionAndNoReplay(t *testing.T) {
	var calls atomic.Int32
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "session-query", Version: "test"}, nil)
	upstream.AddTool(&mcpsdk.Tool{Name: "list", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls.Add(1)
		return &mcpsdk.CallToolResult{StructuredContent: map[string]any{"keep": "projected", "raw_canary": strings.Repeat("private-canary", 6000)}}, nil
	})
	server := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, nil))
	t.Cleanup(server.Close)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []ToolHiveProfile{{Name: "search", URL: server.URL, Auth: authNone}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	db := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: db.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	api, err := NewSessionAPI(process, client, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "client"}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
	opened, err := api.OpenSession(ctx, nil)
	if err != nil || len(opened.Catalogue.Tools()) != 0 {
		t.Fatalf("open: %#v %v", opened, err)
	}
	enrolled, err := api.BeginEnrollment(ctx, opened.Ref)
	if err != nil || enrolled.Catalogue == nil {
		t.Fatalf("enroll: %#v %v", enrolled, err)
	}
	query := find(enrolled.Catalogue, "CallMcpWithQuery")
	if query == nil || !query.ReadOnly() || !isAuth(query) || isSerial(query) {
		t.Fatalf("native query markers not preserved: %T", query)
	}
	for _, tc := range []struct {
		name, target, filter string
		dispatch, failure    bool
	}{
		{"projection", "list", ".keep", true, false},
		{"projection-failure", "list", "error(.raw_canary)", true, true},
		{"projection-limit", "list", ".", true, true},
		{"invalid-filter", "list", "[", false, true},
		{"absent-target", "absent", ".", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]any{"server": "search", "tool": tc.target, "args": map[string]any{}, "jq_filter": tc.filter})
			call := c.Call{ID: session.ToolCallID(tc.name), Name: "CallMcpWithQuery", Arguments: args}
			before := calls.Load()
			out, err := api.InvokeTool(ctx, opened.Ref, enrolled.Catalogue.Ref(), call)
			if err != nil {
				t.Fatal(err)
			}
			if tc.dispatch {
				if out.Kind != c.InvocationCompleted || out.Result.IsError != tc.failure || strings.Contains(out.Result.Content, "private-canary") {
					t.Fatalf("projection result: %#v", out)
				}
				if !tc.failure && out.Result.Content != `"projected"` {
					t.Fatalf("unfiltered or wrong result: %q", out.Result.Content)
				}
				if tc.failure && !strings.Contains(out.Result.Content, "automatic replay refused") {
					t.Fatalf("uncertain query lost refusal: %q", out.Result.Content)
				}
				if calls.Load() != before+1 {
					t.Fatal("query did not dispatch exactly once")
				}
			} else if out.Kind != c.InvocationNotDispatched || calls.Load() != before {
				t.Fatalf("invalid target/filter dispatched: %#v", out)
			}
			after := calls.Load()
			if _, err := api.InvokeTool(ctx, opened.Ref, enrolled.Catalogue.Ref(), call); err != nil || calls.Load() != after {
				t.Fatalf("repeat redispatched: %v", err)
			}
		})
	}
	// Even if the attachment retains a route, the frozen catalogue is the ceiling.
	st := api.states[opened.Ref]
	limited, err := api.catalogue(st, c.CatalogueRef(apiRef()), []tool.Tool{}, st.record.Account)
	if err != nil || len(limited.Tools()) != 0 {
		t.Fatalf("empty publication exposed query: %v", err)
	}
	if _, err := api.DisconnectTools(ctx, opened.Ref, enrolled.Catalogue.Ref()); err != nil {
		t.Fatal(err)
	}
	reopened, err := api.OpenSession(ctx, &opened.Ref)
	if err != nil || len(reopened.Catalogue.Tools()) != 0 {
		t.Fatalf("withdrawn query republished: %#v %v", reopened, err)
	}
}

func TestSessionAPIQueryProductionNativeReadinessAndAccount(t *testing.T) {
	api, f, ctx := reviewAPI(t)
	mux := http.NewServeMux()
	if err := f.process.Handlers.Mount(mux, f.process.CallbackPath); err != nil {
		t.Fatal(err)
	}
	broker := httptest.NewServer(mux)
	t.Cleanup(broker.Close)
	f.process.Runtime.queryCaller = toolHiveQueryCaller(nil, broker.URL+toolHiveMCPPath, broker.Client())
	provider := f.process.providers[0]
	row := &storage.UpstreamTokens{
		ProviderID: provider, AccessToken: "upstream-access", UserID: "native-user",
		UpstreamSubject: "native-subject", ClientID: "upstream-client",
		ExpiresAt: time.Now().Add(time.Hour), SessionExpiresAt: time.Now().Add(time.Hour),
	}
	if err := f.process.authStorage.StoreUpstreamTokens(ctx, "verified-tsid", provider, row); err != nil {
		t.Fatal(err)
	}
	opened, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := api.states[opened.Ref]
	account, err := f.process.nativeAccount(ctx, "verified-tsid")
	if err != nil {
		t.Fatal(err)
	}
	guard := c.ContinuityGuard{SessionID: session.SessionID(opened.Ref), SessionIncarnation: st.record.Incarnation, OwnerPartition: st.record.Owner, WorkloadPartition: st.record.Workload, ProfileDigest: f.process.profileDigest, Providers: f.process.providers}
	staged, err := f.process.custody.Stage(ctx, custodyRequest{Guard: custodyGuardFromContract(guard), AttemptDeadline: time.Now().Add(time.Minute)}, "verified-tsid")
	if err != nil {
		t.Fatal(err)
	}
	st.record.Custody = &c.StagedCredentialCustody{RecoveryReference: string(staged.Recovery), ExpiresAt: staged.ExpiresAt, ProfileDigest: guard.ProfileDigest, Providers: guard.Providers}
	st.record.Account, st.record.Connected, st.record.Connection = account, true, apiRef()
	if err := api.save(ctx, st); err != nil {
		t.Fatal(err)
	}
	delete(api.states, opened.Ref)
	recovered, err := api.OpenSession(ctx, &opened.Ref)
	if err != nil || find(recovered.Catalogue, "CallMcpWithQuery") == nil {
		t.Fatalf("native recovery/query publication: %#v %v", recovered, err)
	}
	call := c.Call{ID: "protected-query", Name: "CallMcpWithQuery", Arguments: []byte(`{"server":"private","tool":"status","args":{},"jq_filter":"."}`)}
	before := f.upstreamCalls.Load()
	check, err := api.CheckAuthorization(ctx, opened.Ref, recovered.Catalogue.Ref(), &call, "")
	if err != nil || !check.Ready || f.upstreamCalls.Load() != before {
		t.Fatalf("native readiness must be nonexecuting: %#v %v", check, err)
	}
	out, err := api.InvokeTool(ctx, opened.Ref, recovered.Catalogue.Ref(), call)
	if err != nil || out.Kind != c.InvocationCompleted || out.Result.IsError || f.upstreamCalls.Load() <= before {
		t.Fatalf("protected native query: %#v result=%+v err=%v upstream_delta=%d", out, out.Result, err, f.upstreamCalls.Load()-before)
	}
	// Changing an otherwise valid native identity must not bypass the enrolled account.
	row.UpstreamSubject = "other-subject"
	if err := f.process.authStorage.StoreUpstreamTokens(ctx, "verified-tsid", provider, row); err != nil {
		t.Fatal(err)
	}
	call.ID = "changed-account"
	before = f.upstreamCalls.Load()
	if _, err := api.CheckAuthorization(ctx, opened.Ref, recovered.Catalogue.Ref(), &call, ""); err == nil || f.upstreamCalls.Load() != before {
		t.Fatalf("changed account was accepted or dispatched: %v", err)
	}
	// Expired native tokens without refresh must park authorization, not invoke MCP.
	row.UpstreamSubject = "native-subject"
	row.ExpiresAt = time.Now().Add(-time.Minute)
	if err := f.process.authStorage.StoreUpstreamTokens(ctx, "verified-tsid", provider, row); err != nil {
		t.Fatal(err)
	}
	call.ID = "expired-grant"
	check, err = api.CheckAuthorization(ctx, opened.Ref, recovered.Catalogue.Ref(), &call, "")
	if err != nil || check.Ready || check.Authorization == "" || f.upstreamCalls.Load() != before {
		t.Fatalf("expired native grant must park: %#v %v", check, err)
	}
	st = api.states[opened.Ref]
	parked := st.parked[check.Authorization]
	if parked == nil || callDigest(parked.call) != callDigest(call) || parked.descriptor != descriptor(find(st.catalogue, "mcp__private__status")) {
		t.Fatal("production query did not park exact outer bytes and target descriptor")
	}
}

type queryDescriptorDrift struct{ tool.Tool }

func (t queryDescriptorDrift) Spec() tool.ToolSpec {
	spec := t.Tool.Spec()
	spec.Description += " changed"
	return spec
}

func TestSessionAPIQueryDescriptorBindsFrozenTarget(t *testing.T) {
	catalogue, err := Compile(anonymousConfig(), discoveredTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		t.Fatal("descriptor check dispatched")
		return session.ToolResult{}, errors.New("unexpected dispatch")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	attachment, _ := attach(t, runtime, "descriptor-query")
	st := &apiState{attachment: attachment}
	api := &SessionAPI{process: &Process{}}
	st.catalogue, err = api.catalogue(st, c.CatalogueRef(apiRef()), attachment.Tools(), [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	// A query caller is required for publication, even when native routes exist.
	if find(st.catalogue, "CallMcpWithQuery") != nil {
		t.Fatal("query published without native query caller")
	}
	call := c.Call{ID: "query", Name: "CallMcpWithQuery", Arguments: []byte(`{"server":"search","tool":"query","jq_filter":"."}`)}
	query := &attachmentQueryTool{attachment: attachment}
	original, err := st.invocationDescriptor(query, call)
	if err != nil || original != descriptor(find(st.catalogue, "mcp__search__query")) {
		t.Fatalf("query descriptor did not bind target: %v", err)
	}
	st.catalogue, err = c.NewCatalogue(c.CatalogueRef(apiRef()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.invocationDescriptor(query, call); err == nil {
		t.Fatal("absent target retained resume descriptor")
	}
}
