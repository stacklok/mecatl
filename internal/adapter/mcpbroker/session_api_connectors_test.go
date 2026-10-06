package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestSessionAPIInspectConnectorsPassive(t *testing.T) {
	var requests atomic.Int32
	upstream := toolHiveDiscoveryServer(t, "echo", &requests)
	newProcess := func() *Process {
		p, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []ToolHiveProfile{{Name: "public", URL: upstream.URL, Auth: authNone}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Close() })
		return p
	}
	db := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: db.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	workload := &session.Principal{Issuer: "https://workload.test", Subject: "host"}
	newAPI := func(p *Process) *SessionAPI {
		api, err := NewSessionAPI(p, client, func(context.Context) *session.Principal { return workload })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = api.Close() })
		return api
	}
	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
	api := newAPI(newProcess())
	opened, err := api.OpenSession(owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := api.InspectConnectors(owner, opened.Ref, opened.Catalogue.Ref())
	if err != nil || inventory.Availability != c.AvailabilityUnavailable || requests.Load() != 0 {
		t.Fatalf("open inspection: %#v %v", inventory, err)
	}
	enrolled, err := api.BeginEnrollment(owner, opened.Ref)
	if err != nil {
		t.Fatal(err)
	}
	cat := enrolled.Catalogue.Ref()
	before := requests.Load()
	inventory, err = api.InspectConnectors(owner, opened.Ref, cat)
	if err != nil || inventory.Connectors[0].CatalogueState != c.CatalogueDiscovered || inventory.Connectors[0].ToolCount != 1 {
		t.Fatalf("enrolled: %#v %v", inventory, err)
	}
	key := sessionAPIPrefix + string(opened.Ref)
	stored, err := client.Get(owner, key).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	cold := newAPI(newProcess())
	inventory, err = cold.InspectConnectors(owner, opened.Ref, cat)
	if err != nil || inventory.Availability != c.AvailabilityUnavailable || inventory.EnrollmentState != c.EnrollmentUnknown || inventory.Connectors[0].CatalogueState != c.CatalogueUnknown || inventory.Connectors[0].ToolCount != 0 {
		t.Fatalf("cold: %#v %v", inventory, err)
	}
	if len(cold.states) != 0 || len(cold.process.Runtime.sessions) != 0 || requests.Load() != before {
		t.Fatal("inspection activated state or contacted upstream")
	}
	after, _ := client.Get(owner, key).Bytes()
	if !reflect.DeepEqual(stored, after) {
		t.Fatal("inspection saved metadata")
	}
	var record apiRecord
	if err := json.Unmarshal(stored, &record); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*apiRecord){
		"ref":       func(r *apiRecord) { r.Ref = c.SessionRef(apiRef()) },
		"owner":     func(r *apiRecord) { r.Owner = [32]byte{} },
		"workload":  func(r *apiRecord) { r.Workload = [32]byte{} },
		"profile":   func(r *apiRecord) { r.Profile = [32]byte{} },
		"expired":   func(r *apiRecord) { r.ExpiresAt = time.Now().Add(-time.Second) },
		"catalogue": func(r *apiRecord) { r.Catalogue = c.CatalogueRef(apiRef()) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := record
			mutate(&bad)
			b, _ := json.Marshal(bad)
			if err := client.Set(owner, key, b, time.Hour).Err(); err != nil {
				t.Fatal(err)
			}
			if _, err := cold.InspectConnectors(owner, opened.Ref, cat); !errors.Is(err, c.ErrStateUnavailable) {
				t.Fatalf("got %v", err)
			}
		})
	}
	for _, payload := range []string{"{", "null"} {
		if err := client.Set(owner, key, payload, time.Hour).Err(); err != nil {
			t.Fatal(err)
		}
		if _, err := cold.InspectConnectors(owner, opened.Ref, cat); !errors.Is(err, c.ErrStateUnavailable) {
			t.Fatalf("corrupt metadata: %v", err)
		}
	}
	if err := client.Del(owner, key).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := cold.InspectConnectors(owner, opened.Ref, cat); !errors.Is(err, c.ErrStateUnavailable) {
		t.Fatalf("missing metadata: %v", err)
	}
	if err := client.Set(owner, key, stored, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(owner)
	cancel()
	if _, err := cold.InspectConnectors(cancelled, opened.Ref, cat); !errors.Is(err, context.Canceled) {
		t.Fatalf("metadata I/O failure: %v", err)
	}
	for _, refs := range [][2]string{{"bad", string(cat)}, {apiRef(), string(cat)}, {string(opened.Ref), apiRef()}, {string(opened.Ref), string(opened.Catalogue.Ref())}} {
		if _, err := api.InspectConnectors(owner, c.SessionRef(refs[0]), c.CatalogueRef(refs[1])); err == nil {
			t.Fatal("invalid references accepted")
		}
	}
	if _, err := api.DisconnectTools(owner, opened.Ref, cat); err != nil {
		t.Fatal(err)
	}
	withdrawn := api.states[opened.Ref].record.Catalogue
	inventory, err = api.InspectConnectors(owner, opened.Ref, withdrawn)
	if err != nil || inventory.Availability != c.AvailabilityUnavailable || inventory.Connectors[0].CatalogueState != c.CatalogueUnknown {
		t.Fatalf("withdrawn: %#v %v", inventory, err)
	}
	if requests.Load() != before || len(cold.states) != 0 {
		t.Fatal("passive status side effects")
	}
	if err := client.Set(owner, key, stored, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	reactivated, err := cold.OpenSession(owner, &opened.Ref)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err = cold.InspectConnectors(owner, opened.Ref, reactivated.Catalogue.Ref())
	if err != nil || inventory.Connectors[0].ToolCount != 1 || inventory.Availability != c.AvailabilityAvailable || requests.Load() != before {
		t.Fatalf("explicit activation inventory: %#v %v", inventory, err)
	}
}
