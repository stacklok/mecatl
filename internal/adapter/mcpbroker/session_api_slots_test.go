package mcpbroker

import (
	"context"
	"encoding/json"
	"math"
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

func TestSessionAPISlots_ReorderedParallel(t *testing.T) {
	started, release := make(chan struct{}, 2), make(chan struct{})
	var calls atomic.Int32
	api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return slotResult(), nil
	})
	defer close(release)
	call := c.Call{ID: "same-id", Name: "mcp__slots__echo", Arguments: []byte(`{ "x": 1 }`)}
	a, b := c.BrokerAttempt{Slot: 63, Sequence: 1}, c.BrokerAttempt{Slot: 2, Sequence: 1}
	caller, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan c.InvocationOutcome, 3)
	for _, attempt := range []c.BrokerAttempt{a, b} {
		go func() { out, _ := api.InvokeTool(caller, opened.Ref, cat.Ref(), call, attempt); done <- out }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("different slots serialized behind worker")
		}
	}
	check, err := api.CheckAuthorization(ctx, opened.Ref, cat.Ref(), &call, "", a)
	if err != nil || check.Ready || check.Reason != c.FailureInterrupted {
		t.Fatalf("running ready: %#v %v", check, err)
	}
	observer, stopObserver := context.WithTimeout(ctx, 100*time.Millisecond)
	duplicate, err := api.InvokeTool(observer, opened.Ref, cat.Ref(), call, a)
	stopObserver()
	if err != nil || duplicate.Kind != c.InvocationOutcomeUnknown {
		t.Fatalf("duplicate observer: %#v %v", duplicate, err)
	}
	busy, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, c.BrokerAttempt{Slot: 63, Sequence: 2})
	if err != nil || busy.Reason != c.FailureCapacity {
		t.Fatalf("busy: %#v %v", busy, err)
	}
	if _, err := api.AcknowledgeAttempt(ctx, opened.Ref, a); err == nil {
		t.Fatal("ack released running worker")
	}
	changed := call
	changed.Arguments = []byte(`{"x":1}`)
	out, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), changed, a)
	if err != nil || out.Reason != c.FailureCallChanged {
		t.Fatalf("byte change accepted: %#v %v", out, err)
	}
	cancel()
	for i := 0; i < 2; i++ {
		if out := <-done; out.Kind != c.InvocationOutcomeUnknown {
			t.Fatalf("cancel observation: %#v", out)
		}
	}
	status, err := api.InspectAttempt(ctx, opened.Ref, a)
	if err != nil || status.Phase != "dispatched" || calls.Load() != 2 {
		t.Fatalf("cancel released dispatch: %#v %v", status, err)
	}
}

func TestSessionAPISlots_RecoveryAndReuse(t *testing.T) {
	var calls atomic.Int32
	api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls.Add(1)
		return slotResult(), nil
	})
	call := c.Call{ID: "reused", Name: "mcp__slots__echo", Arguments: []byte(`{}`)}
	a := c.BrokerAttempt{Sequence: 1}
	for n := uint64(1); n <= 4100; n++ {
		a.Sequence = n
		out, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, a)
		if err != nil || out.Kind != c.InvocationCompleted {
			t.Fatalf("attempt %d: %#v %v", n, out, err)
		}
	}
	for _, n := range []uint64{1, 4099, 4102, math.MaxUint64} {
		if _, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, c.BrokerAttempt{Sequence: n}); err == nil {
			t.Fatalf("accepted older/gap %d", n)
		}
	}
	pending := c.BrokerAttempt{Slot: 1, Sequence: 1}
	check, err := api.CheckAuthorization(ctx, opened.Ref, cat.Ref(), &call, "", pending)
	if err != nil || !check.Ready {
		t.Fatalf("preflight: %#v %v", check, err)
	}
	// New facade has only metadata; inspection must not attach or discover.
	fresh, err := NewSessionAPI(api.process, api.redis, api.workload)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	status, err := fresh.InspectAttempt(ctx, opened.Ref, pending)
	if err != nil || status.Disposition != session.BrokerAttemptNotDispatched || fresh.states[opened.Ref].attachment != nil {
		t.Fatalf("reserved recovery: %#v %v", status, err)
	}
	status, err = fresh.InspectAttempt(ctx, opened.Ref, a)
	if err != nil || status.Disposition != session.BrokerAttemptUnknown {
		t.Fatalf("lost payload: %#v %v", status, err)
	}
	for _, catalogue := range []c.CatalogueRef{cat.Ref(), c.CatalogueRef(apiRef())} {
		out, err := fresh.InvokeTool(ctx, opened.Ref, catalogue, call, a)
		if err != nil || out.Kind != c.InvocationOutcomeUnknown || calls.Load() != 4100 {
			t.Fatalf("restart duplicate: %#v %v", out, err)
		}
	}
	status, err = fresh.InspectAttempt(ctx, opened.Ref, c.BrokerAttempt{Sequence: 4101})
	if err != nil || status.Phase != "not_admitted" {
		t.Fatalf("next proof: %#v %v", status, err)
	}
	status, err = fresh.AcknowledgeAttempt(ctx, opened.Ref, pending)
	if err != nil || status.Disposition != session.BrokerAttemptNotDispatched {
		t.Fatalf("ack: %#v %v", status, err)
	}
}

func TestSessionAPISlots_LostAdmissionAcknowledgement(t *testing.T) {
	for _, mode := range []string{"before", "landed"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				calls.Add(1)
				return slotResult(), nil
			})
			underlying := api.redis
			api.redis = &reviewSaveRedis{UniversalClient: underlying, mode: mode}
			a := c.BrokerAttempt{Sequence: 1}
			call := c.Call{ID: "one", Name: "mcp__slots__echo", Arguments: []byte(`{}`)}
			out, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, a)
			if mode == "landed" {
				if err != nil || out.Kind != c.InvocationCompleted || calls.Load() != 1 {
					t.Fatalf("verified readback: %#v %v", out, err)
				}
				return
			}
			if err == nil || calls.Load() != 0 {
				t.Fatal("ambiguous admission dispatched")
			}
			if status, err := api.InspectAttempt(ctx, opened.Ref, a); err == nil {
				t.Fatalf("arbitrary old GET became proof: %#v", status)
			}
			// Simulate the outstanding SET arriving after its caller returned.
			candidate := api.states[opened.Ref].pendingWrite
			encoded, _ := json.Marshal(candidate)
			if err := underlying.Set(ctx, sessionAPIPrefix+string(opened.Ref), encoded, time.Hour).Err(); err != nil {
				t.Fatal(err)
			}
			status, err := api.InspectAttempt(ctx, opened.Ref, a)
			if err != nil || status.Disposition != session.BrokerAttemptNotDispatched || calls.Load() != 0 {
				t.Fatalf("late SET reconciliation: %#v %v", status, err)
			}
			out, err = api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, a)
			if err != nil || out.Kind != c.InvocationNotDispatched || calls.Load() != 0 {
				t.Fatalf("late admission replay: %#v %v", out, err)
			}
		})
	}
}
