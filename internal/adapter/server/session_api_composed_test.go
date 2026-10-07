package server

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

// These tests reuse the donor's real Process, encrypted ToolHive Redis, OAuth
// chains and MCP upstreams, not a replacement credential workflow.
func TestSessionAPINativeProtectedReplacement(t *testing.T) {
	f := newContinuityFixture(t)
	client := redis.NewClient(&redis.Options{Addr: f.redis.Addr()})
	defer client.Close()
	workload := func(context.Context) *session.Principal { return f.workload.Clone() }
	api, err := mcpbroker.NewSessionAPI(f.process, client, workload)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := api.OpenSession(f.owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Catalogue.Tools()) != 0 {
		t.Fatal("Open exposed protected tools")
	}
	started, err := api.BeginEnrollment(f.owner, opened.Ref)
	if err != nil || !started.Valid() {
		t.Fatalf("begin: %#v %v", started, err)
	}
	pending, err := api.BeginEnrollment(f.owner, opened.Ref)
	if err != nil || pending.Started.Ref != started.Started.Ref {
		t.Fatalf("pending replacement: %#v %v", pending, err)
	}
	status, err := api.ObserveEnrollment(f.owner, opened.Ref, started.Started.Ref)
	if err != nil || status.Kind != c.FlowPending {
		t.Fatalf("pending: %#v %v", status, err)
	}
	browser := f.gateway.Client()
	browser.Timeout = 8 * time.Second
	response, err := browser.Get(started.Started.Prompt.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback: %d", response.StatusCode)
	}
	status, err = api.ObserveEnrollment(f.owner, opened.Ref, started.Started.Ref)
	if err != nil || status.Kind != c.FlowCompleted {
		t.Fatalf("completed: %#v %v", status, err)
	}
	assertNativeSessionCatalogue(t, status.Catalogue)
	connected, err := api.BeginEnrollment(f.owner, opened.Ref)
	if err != nil || connected.Kind != c.EnrollmentAlreadyConnected {
		t.Fatalf("connected replacement: %#v %v", connected, err)
	}
	call := c.Call{ID: "native-one", Name: "mcp__backend-a__whoami", Arguments: []byte(`{}`)}
	out, err := api.InvokeTool(f.owner, opened.Ref, status.Catalogue.Ref(), call, session.NewBrokerAttempt())
	if err != nil || out.Kind != c.InvocationCompleted {
		t.Fatalf("native invoke: %#v %v", out, err)
	}
	f.replaceBroker()
	api2, err := mcpbroker.NewSessionAPI(f.process, client, workload)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := api2.OpenSession(f.owner, &opened.Ref)
	if err != nil || recovered.Ref != opened.Ref {
		t.Fatalf("custody reopen: %#v %v", recovered, err)
	}
	assertNativeSessionCatalogue(t, recovered.Catalogue)
	out, err = api2.InvokeTool(f.owner, opened.Ref, status.Catalogue.Ref(), call, session.NewBrokerAttempt())
	if err != nil || out.Kind != c.InvocationNotDispatched || out.Reason != c.FailureCatalogueChanged {
		t.Fatalf("stale execution: %#v %v", out, err)
	}
	call.ID = "native-two"
	out, err = api2.InvokeTool(f.owner, opened.Ref, recovered.Catalogue.Ref(), call, session.NewBrokerAttempt())
	if err != nil || out.Kind != c.InvocationCompleted {
		t.Fatalf("recovered execution: %#v %v", out, err)
	}
	result, err := api2.DisconnectTools(f.owner, opened.Ref, recovered.Catalogue.Connection())
	if err != nil || result != c.Disconnected {
		t.Fatalf("disconnect: %v %v", result, err)
	}
	reopened, err := api2.OpenSession(f.owner, &opened.Ref)
	if err != nil || len(reopened.Catalogue.Tools()) != 0 {
		t.Fatalf("withdrawn: %#v %v", reopened, err)
	}
	started, err = api2.BeginEnrollment(f.owner, opened.Ref)
	if err != nil || started.Kind != c.EnrollmentStartedKind {
		t.Fatalf("fresh reenrollment: %#v %v", started, err)
	}
	cancelled, err := api2.CancelEnrollment(f.owner, opened.Ref, started.Started.Ref)
	if err != nil || cancelled != c.Cancelled {
		t.Fatalf("cancel enrollment: %v %v", cancelled, err)
	}
	status, err = api2.ObserveEnrollment(f.owner, opened.Ref, started.Started.Ref)
	if err != nil || status.Kind != c.FlowCancelled {
		t.Fatalf("cancel observed: %#v %v", status, err)
	}
	cancelled, err = api2.CancelEnrollment(f.owner, opened.Ref, started.Started.Ref)
	if err != nil || cancelled != c.AlreadyResolved {
		t.Fatalf("cancel retry: %v %v", cancelled, err)
	}
}

func assertNativeSessionCatalogue(t *testing.T, catalogue c.Catalogue) {
	t.Helper()
	names := make([]string, 0, len(catalogue.Tools()))
	for _, candidate := range catalogue.Tools() {
		names = append(names, candidate.Spec().Name)
	}
	slices.Sort(names)
	want := []string{"CallMcpWithQuery", "mcp__backend-a__whoami", "mcp__backend-b__whoami"}
	if !slices.Equal(names, want) {
		t.Fatalf("native catalogue names = %v, want %v", names, want)
	}
}
