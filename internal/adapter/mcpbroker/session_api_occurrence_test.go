package mcpbroker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
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
