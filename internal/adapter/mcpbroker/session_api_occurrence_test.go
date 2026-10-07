package mcpbroker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func slotAPI(t *testing.T, invoke func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error)) (*SessionAPI, context.Context, c.SessionSnapshot, c.Catalogue) {
	t.Helper()
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "slots", Version: "test"}, nil)
	upstream.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, invoke)
	server := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, nil))
	t.Cleanup(server.Close)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []ToolHiveProfile{{Name: "slots", URL: server.URL, Auth: authNone}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	db := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{MaxRetries: -1, Addr: db.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	api, err := NewSessionAPI(process, client, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "slots"}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "slots"})
	opened, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := api.BeginEnrollment(ctx, opened.Ref)
	if err != nil {
		t.Fatal(err)
	}
	return api, ctx, opened, enrolled.Catalogue
}
func slotResult() *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}
}

func awaitV3Occurrence[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("occurrence did not progress")
	}
	var zero T
	return zero
}

func TestSessionAPIOccurrenceWorkerOwnsBusyAfterCallerCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		close(started)
		<-release
		return slotResult(), nil
	})
	defer close(release)
	call := c.Call{ID: "one", Name: "mcp__slots__echo", Arguments: []byte(`{}`)}
	attempt := session.NewBrokerAttempt()
	if api.states[opened.Ref].running != nil {
		t.Fatal("preflight reserved execution")
	}
	caller, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan c.InvocationOutcome, 1)
	go func() { out, _ := api.InvokeTool(caller, opened.Ref, cat.Ref(), call, attempt); done <- out }()
	awaitV3Occurrence(t, started)
	cancel()
	if out := awaitV3Occurrence(t, done); out.Kind != c.InvocationOutcomeUnknown {
		t.Fatalf("cancel: %+v", out)
	}
	out, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, session.NewBrokerAttempt())
	if err != nil || out.Kind != c.InvocationNotDispatched || out.Reason != c.FailureCapacity {
		t.Fatalf("caller released worker: %+v %v", out, err)
	}
}

func TestAuthorizationFollowupNeverLosesZeroDispatch(t *testing.T) {
	api, ctx, opened, catalogue := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		t.Fatal("native tool executed before authorization continuation")
		return slotResult(), nil
	})
	probe := &authorizationProbeTool{
		Tool:          catalogue.Tools()[0],
		authorization: session.ExternalAuthorization{ID: apiRef(), Binding: "private-binding", ExpiresAt: time.Now().Add(time.Hour)},
	}
	adoptedRef := c.CatalogueRef(apiRef())
	adopted, err := c.NewCatalogue(adoptedRef, catalogue.Connection(), []tool.Tool{probe})
	if err != nil {
		t.Fatal(err)
	}
	opCtx, release, err := api.operation(ctx, opened.Ref, apiControl{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := api.metadataState(opCtx, opened.Ref)
	if err != nil {
		release()
		t.Fatal(err)
	}
	next := st.record
	next.Catalogue = adoptedRef
	if err := api.saveRecord(opCtx, st, next); err != nil {
		release()
		t.Fatal(err)
	}
	st.catalogue = adopted
	release()

	call := c.Call{ID: "exact-call", Name: probe.Spec().Name, Arguments: []byte(`{"x":1}`)}
	attempt := session.NewBrokerAttempt()
	check, err := api.CheckAuthorization(ctx, opened.Ref, adoptedRef, &call, "", attempt)
	if err != nil || check.Ready || check.Authorization == "" || !check.ExpiresAt.After(time.Now()) {
		t.Fatalf("authorization check = %+v, %v", check, err)
	}
	repeated, err := api.CheckAuthorization(ctx, opened.Ref, adoptedRef, &call, "", attempt)
	if err != nil || repeated.Authorization != check.Authorization || probe.requests.Load() != 1 {
		t.Fatalf("exact followup changed flow: first=%+v repeated=%+v requests=%d err=%v", check, repeated, probe.requests.Load(), err)
	}
	changed := call
	changed.Arguments = []byte(` {"x":1}`)
	mismatch, err := api.CheckAuthorization(ctx, opened.Ref, adoptedRef, &changed, "", attempt)
	if err != nil || mismatch.Reason != c.FailureCapacity || probe.requests.Load() != 1 {
		t.Fatalf("changed call was not fenced: %+v requests=%d err=%v", mismatch, probe.requests.Load(), err)
	}
	out, err := api.InvokeTool(ctx, opened.Ref, adoptedRef, call, attempt)
	if err != nil || out.Kind != c.InvocationAuthorizationRequired || out.Authorization != check.Authorization || probe.executions.Load() != 0 {
		t.Fatalf("authorization response lost zero-dispatch guarantee: %+v executions=%d err=%v", out, probe.executions.Load(), err)
	}
	if _, err := api.CancelAuthorization(ctx, opened.Ref, check.Authorization, session.NewBrokerAttempt()); !errors.Is(err, c.ErrStateUnavailable) {
		t.Fatalf("wrong attempt cancelled parked authorization: %v", err)
	}
	parked := api.states[opened.Ref].parked[check.Authorization]
	if parked == nil || string(parked.call.Arguments) != string(call.Arguments) {
		t.Fatal("invalid worker altered the parked call")
	}
	withdrawn, err := api.DisconnectTools(ctx, opened.Ref, catalogue.Connection())
	if err != nil || withdrawn != c.Disconnected {
		t.Fatalf("withdrawal = %v, %v", withdrawn, err)
	}
	if parked.call.Arguments != nil || parked.terminal == nil || parked.terminal.Kind != c.FlowFailed || parked.terminal.Reason != c.FailureAuthorityWithdrawn {
		t.Fatalf("withdrawal left continuation usable: %+v", parked)
	}
	resumed, err := api.ResumeTool(ctx, opened.Ref, check.Authorization, adoptedRef, attempt)
	if !errors.Is(err, c.ErrStateUnavailable) || resumed.Valid() || probe.executions.Load() != 0 {
		t.Fatalf("withdrawn continuation was not fenced: %+v executions=%d err=%v", resumed, probe.executions.Load(), err)
	}
}

func TestSessionAPIResumeValidatesFrozenTargetAndAccount(t *testing.T) {
	api, ctx, opened, catalogue := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		t.Fatal("test authorization call reached the provider")
		return slotResult(), nil
	})
	probe := &authorizationProbeTool{Tool: catalogue.Tools()[0], authorization: session.ExternalAuthorization{ID: apiRef(), Binding: "private-binding", ExpiresAt: time.Now().Add(time.Hour)}}
	adoptedRef := c.CatalogueRef(apiRef())
	adopted, err := c.NewCatalogue(adoptedRef, catalogue.Connection(), []tool.Tool{probe})
	if err != nil {
		t.Fatal(err)
	}
	opCtx, release, err := api.operation(ctx, opened.Ref, apiControl{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := api.metadataState(opCtx, opened.Ref)
	if err != nil {
		release()
		t.Fatal(err)
	}
	next := st.record
	next.Catalogue = adoptedRef
	if err := api.saveRecord(opCtx, st, next); err != nil {
		release()
		t.Fatal(err)
	}
	st.catalogue = adopted
	release()

	call := c.Call{ID: "frozen-call", Name: probe.Spec().Name, Arguments: []byte(` {"nested": [1, 2]} `)}
	attempt := session.NewBrokerAttempt()
	check, err := api.CheckAuthorization(ctx, opened.Ref, adoptedRef, &call, "", attempt)
	if err != nil || check.Authorization == "" {
		t.Fatalf("authorization check = %+v, %v", check, err)
	}
	parked := api.states[opened.Ref].parked[check.Authorization]
	completed := c.FlowStatus{Kind: c.FlowCompleted, Catalogue: adopted}
	parked.terminal, parked.completed = &completed, adopted
	parked.native = session.ExternalAuthorization{}

	parked.account = [32]byte{1}
	out, err := api.ResumeTool(ctx, opened.Ref, check.Authorization, adoptedRef, attempt)
	if err != nil || out.Kind != c.InvocationNotDispatched || out.Reason != c.FailureCatalogueChanged || probe.executions.Load() != 0 {
		t.Fatalf("account change was accepted: %+v executions=%d err=%v", out, probe.executions.Load(), err)
	}
	parked.account = st.record.Account
	parked.descriptor = [32]byte{1}
	out, err = api.ResumeTool(ctx, opened.Ref, check.Authorization, adoptedRef, attempt)
	if err != nil || out.Kind != c.InvocationNotDispatched || out.Reason != c.FailureCatalogueChanged || probe.executions.Load() != 0 {
		t.Fatalf("frozen descriptor change was accepted: %+v executions=%d err=%v", out, probe.executions.Load(), err)
	}
	parked.descriptor = apiToolDigest(find(adopted, call.Name))
	out, err = api.ResumeTool(ctx, opened.Ref, check.Authorization, adoptedRef, attempt)
	if err != nil || out.Kind != c.InvocationCompleted || out.Result == nil || out.Result.CallID != call.ID || probe.executions.Load() != 1 || string(probe.executedArgs) != string(call.Arguments) {
		t.Fatalf("valid continuation changed the call: %+v args=%q executions=%d err=%v", out, probe.executedArgs, probe.executions.Load(), err)
	}
}

type authorizationProbeTool struct {
	tool.Tool
	authorization session.ExternalAuthorization
	requests      atomic.Int32
	executions    atomic.Int32
	executedArgs  []byte
}

func (t *authorizationProbeTool) RequestAuthorization(_ context.Context, _ session.ToolCall) (session.ExternalAuthorization, bool, error) {
	t.requests.Add(1)
	return t.authorization, true, nil
}

func (t *authorizationProbeTool) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}

func (t *authorizationProbeTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.executions.Add(1)
	t.executedArgs = append([]byte(nil), call.Args...)
	return session.ToolResult{CallID: call.ID, Content: "unexpected dispatch"}, nil
}

func TestSessionAPIOccurrenceHasNoDurableReplayProtocol(t *testing.T) {
	var calls atomic.Int32
	api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls.Add(1)
		return slotResult(), nil
	})
	before, err := api.redis.Get(ctx, sessionAPIPrefix+string(opened.Ref)).Result()
	if err != nil {
		t.Fatal(err)
	}
	call := c.Call{ID: "reused-provider-id", Name: "mcp__slots__echo", Arguments: []byte(`{}`)}
	attempt := session.NewBrokerAttempt()
	// A deliberate raw-client resend can execute again after completion.
	for range 2 {
		out, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, attempt)
		if err != nil || out.Kind != c.InvocationCompleted {
			t.Fatalf("invocation: %+v %v", out, err)
		}
	}
	after, err := api.redis.Get(ctx, sessionAPIPrefix+string(opened.Ref)).Result()
	if err != nil || after != before || calls.Load() != 2 {
		t.Fatalf("execution changed lifecycle metadata or deduplicated: calls=%d err=%v", calls.Load(), err)
	}
}
