package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestSessionBrokerConnectorProtectedPassive(t *testing.T) {
	f := newContinuityFixture(t)
	client := redis.NewClient(&redis.Options{Addr: f.redis.Addr()})
	defer client.Close()
	api, err := mcpbroker.NewSessionAPI(f.process, client, func(context.Context) *session.Principal { return f.workload.Clone() })
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	opened, err := api.OpenSession(f.owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	started, err := api.BeginEnrollment(f.owner, opened.Ref)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := api.InspectConnectors(f.owner, opened.Ref, opened.Catalogue.Ref())
	if err != nil || pending.EnrollmentState != c.EnrollmentPending {
		t.Fatalf("pending: %#v %v", pending, err)
	}
	browser := f.gateway.Client()
	browser.Timeout = 8 * time.Second
	response, err := browser.Get(started.Started.Prompt.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("browser: %d", response.StatusCode)
	}
	// A granted native flow stays pending until the explicit observation settles it.
	pending, err = api.InspectConnectors(f.owner, opened.Ref, opened.Catalogue.Ref())
	if err != nil || pending.EnrollmentState != c.EnrollmentPending {
		t.Fatalf("status settled enrollment: %#v %v", pending, err)
	}
	completed, err := api.ObserveEnrollment(f.owner, opened.Ref, started.Started.Ref)
	if err != nil || completed.Kind != c.FlowCompleted {
		t.Fatalf("complete: %#v %v", completed, err)
	}
	inventory, err := api.InspectConnectors(f.owner, opened.Ref, completed.Catalogue.Ref())
	if err != nil || inventory.Availability != c.AvailabilityAvailable || inventory.EnrollmentState != c.EnrollmentCompleted || inventory.TotalConnectors != 2 {
		t.Fatalf("completed: %#v %v", inventory, err)
	}
	for _, row := range inventory.Connectors {
		if row.CatalogueState != c.CatalogueDiscovered || row.ToolCount != 1 {
			t.Fatalf("row: %#v", row)
		}
	}
}
