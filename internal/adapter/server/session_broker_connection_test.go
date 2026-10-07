package server

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestSessionBrokerConnectionCleanupReplacement(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect-first", true: "recover-before-withdraw"}[warm], func(t *testing.T) {
			f := newContinuityFixture(t)
			storage := redis.NewClient(&redis.Options{Addr: f.redis.Addr()})
			t.Cleanup(func() { _ = storage.Close() })
			newAPI := func() *mcpbroker.SessionAPI {
				api, err := mcpbroker.NewSessionAPI(f.process, storage, func(context.Context) *session.Principal { return f.workload.Clone() })
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = api.Close() })
				return api
			}
			api := newAPI()
			client := sessionBrokerRPCClient(t, api, session.PrincipalFromContext(f.owner))
			store := memstore.New()
			svc := sessionBrokerTestHost(t, client, store, mockllm.New(mockllm.TextTurn("done")), nil)
			created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			pending, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			browser := f.gateway.Client()
			browser.Timeout = 8 * time.Second
			response, err := browser.Get(pending.URL)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatal(response.StatusCode)
			}
			if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatal(err)
			}
			durable, err := store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			old, _ := durable.BrokerAccess()
			if old.Connection == "" {
				t.Fatal("published connection missing from durable host")
			}
			// Withdrawal/reconnection must not release an uncertain host occurrence.
			call := session.NewToolCall("uncertain", "mcp__backend-a__whoami", []byte(`{}`))
			attempt, err := durable.PrepareBrokerInvocation(old.Session, old.Catalogue, call, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := durable.DispatchBrokerInvocation(attempt); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(t.Context(), durable); err != nil {
				t.Fatal(err)
			}
			old, _ = durable.BrokerAccess()
			if err := api.Close(); err != nil {
				t.Fatal(err)
			}
			f.replaceBroker()
			replacement := newAPI()
			fresh := sessionBrokerRPCClient(t, replacement, session.PrincipalFromContext(f.owner))
			svc.cfg.SessionBroker = fresh
			if warm {
				snapshot, err := fresh.OpenSession(t.Context(), &old.Session)
				if err != nil || snapshot.Catalogue.Ref() == old.Catalogue || snapshot.Catalogue.Connection() != old.Connection {
					t.Fatalf("recovery identity: %+v %v", snapshot, err)
				}
			}
			if err := svc.DisconnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				out, err := fresh.DisconnectTools(t.Context(), old.Session, old.Connection)
				if err != nil || out != c.AlreadyDisconnected {
					t.Fatalf("repeat old cleanup: %v %v", out, err)
				}
			}
			durable, err = store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			withdrawn, _ := durable.BrokerAccess()
			if !withdrawn.Withdrawn || withdrawn.Connection != old.Connection || !reflect.DeepEqual(withdrawn.Current, old.Current) {
				t.Fatalf("withdrawal changed fence: %+v", withdrawn)
			}
			pending, err = svc.ConnectWorkspaceServices(t.Context(), created.ID)
			if err != nil || pending.Status != c.WorkspaceEnrollmentPending {
				t.Fatalf("explicit reconnect: %+v %v", pending, err)
			}
			out, err := fresh.DisconnectTools(t.Context(), old.Session, old.Connection)
			if err != nil || out != c.AlreadyDisconnected {
				t.Fatalf("old cleanup/new pending: %v %v", out, err)
			}
			flow, err := fresh.ObserveEnrollment(t.Context(), old.Session, c.EnrollmentRef(pending.Ref.ID))
			if err != nil || flow.Kind != c.FlowPending {
				t.Fatalf("old cleanup cancelled new enrollment: %+v %v", flow, err)
			}
			response, err = browser.Get(pending.URL)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatal(response.StatusCode)
			}
			if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatal(err)
			}
			durable, err = store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			next, _ := durable.BrokerAccess()
			if next.Connection == old.Connection || next.Connection == "" || next.Withdrawn || !reflect.DeepEqual(next.Current, old.Current) {
				t.Fatalf("reconnect identity/fence: %+v", next)
			}
			out, err = fresh.DisconnectTools(t.Context(), old.Session, old.Connection)
			if err != nil || out != c.ConnectionChanged {
				t.Fatalf("old cleanup/new publication: %v %v", out, err)
			}
			if err := svc.DisconnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
