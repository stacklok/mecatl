package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func connectorFixture(t *testing.T) (*Runtime, *Attachment, *orderedCapabilityQueries, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"opaque","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"hidden":   {Backend: "hidden", Tools: []ToolDefinition{{Backend: "hidden", Name: "mcp__hidden__found", Schema: json.RawMessage(`{"type":"object"}`)}}},
		"declared": {Backend: "declared", Tools: []ToolDefinition{{Backend: "declared", Name: "mcp__declared__standin", Schema: json.RawMessage(`{"type":"object"}`)}}},
		"empty":    {Backend: "empty"},
	}}
	r := newWorkspaceEnrollmentRuntime(t, tokenServer, queries, "hidden", "declared", "empty")
	t.Cleanup(func() { _ = r.Close() })
	declared := protectedToolHiveProfile("declared")
	declared.Static = []StaticTool{{Name: "standin", Schema: json.RawMessage(`{"type":"object"}`)}}
	construction, err := compileToolHiveConstruction([]ToolHiveProfile{
		protectedToolHiveProfile("hidden"), {Name: "anonymous", URL: "https://anonymous.example/mcp", Auth: authNone}, declared, protectedToolHiveProfile("empty"),
	}, "https://broker.example")
	if err != nil {
		t.Fatal(err)
	}
	r.process.construction = construction
	routes, err := compileStaticProtectedRoutes(construction, r.process.protectedTarget, r.catalogue.routes, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.catalogue.routes = append(r.catalogue.routes, routes...)
	a := testAttachment(t, r)
	if err := a.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r, a, queries, requests
}

func connectorInventory(t *testing.T, r *Runtime, id session.SessionID, binding session.ExternalBinding) contract.ConnectorInventory {
	t.Helper()
	var inspector contract.ConnectorInspector = r
	got, err := inspector.InspectConnectors(t.Context(), id, binding)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func initialConnectorRows() []contract.ConnectorStatus {
	return []contract.ConnectorStatus{
		{Name: "hidden", CatalogueState: "hidden"},
		{Name: "anonymous", CatalogueState: "discovered", ToolCount: 1},
		{Name: "declared", CatalogueState: "declared", ToolCount: 1},
		{Name: "empty", CatalogueState: "hidden"},
	}
}

func TestBrokerMCPStatus_Scenario1_CatalogueStates(t *testing.T) {
	r, a, queries, requests := connectorFixture(t)
	got := connectorInventory(t, r, "session", a.Binding())
	if got.Availability != "available" || got.EnrollmentState != "not_started" || got.TotalConnectors != 4 || got.Truncated || !reflect.DeepEqual(got.Connectors, initialConnectorRows()) {
		t.Fatalf("initial inventory = %+v", got)
	}
	// A lazy grant alone neither discovers tools nor completes enrollment.
	a.logical.mu.Lock()
	a.logical.brokerCredential = &oauthGrant{token: &oauth2.Token{AccessToken: "lazy-grant"}}
	a.logical.mu.Unlock()
	if next := connectorInventory(t, r, "session", a.Binding()); !reflect.DeepEqual(got, next) {
		t.Fatalf("lazy credential changed inventory: %+v", next)
	}
	if requests.Load() != 0 || queries.calls != 0 {
		t.Fatal("inspection contacted an upstream")
	}
	got.Connectors[0].Name = "changed by caller"
	if next := connectorInventory(t, r, "session", a.Binding()); next.Connectors[0].Name != "hidden" {
		t.Fatal("returned rows alias broker state")
	}
	presentation := beginAndGrantWorkspaceEnrollment(t, r, a)
	// Granted consent is pending discovery. Inspection must not perform that discovery.
	beforeRequests := requests.Load()
	got = connectorInventory(t, r, "session", a.Binding())
	if got.EnrollmentState != "pending" || !reflect.DeepEqual(got.Connectors, initialConnectorRows()) || queries.calls != 0 || requests.Load() != beforeRequests {
		t.Fatalf("granted inspection = %+v, queries=%d", got, queries.calls)
	}
	result, err := a.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || result.Status != contract.WorkspaceEnrollmentConnected {
		t.Fatalf("enrollment = %+v, %v", result, err)
	}
	got = connectorInventory(t, r, "session", a.Binding())
	want := []contract.ConnectorStatus{
		{Name: "hidden", CatalogueState: "discovered", ToolCount: 1},
		{Name: "anonymous", CatalogueState: "discovered", ToolCount: 1},
		{Name: "declared", CatalogueState: "discovered", ToolCount: 1},
		{Name: "empty", CatalogueState: "discovered", ToolCount: 0},
	}
	if got.EnrollmentState != "completed" || !reflect.DeepEqual(got.Connectors, want) {
		t.Fatalf("published inventory = %+v", got)
	}
	// Counts use structured route provenance, not model-visible name parsing.
	a.logical.mu.Lock()
	a.logical.completedEnrollment.routes[0].spec.Name = "not_a_connector_prefix"
	a.logical.mu.Unlock()
	if next := connectorInventory(t, r, "session", a.Binding()); !reflect.DeepEqual(got, next) {
		t.Fatalf("name changed provenance: %+v", next)
	}
}

func TestBrokerMCPStatus_Scenario1_EnrollmentStates(t *testing.T) {
	for _, status := range []session.AuthorizationStatus{session.AuthorizationPending, session.AuthorizationGranted, session.AuthorizationCancelled, session.AuthorizationDenied, session.AuthorizationExpired, session.AuthorizationFailed, session.AuthorizationClosed} {
		t.Run(string(status), func(t *testing.T) {
			r, a, queries, requests := connectorFixture(t)
			now := time.Unix(1000, 0)
			r.oauth.now = func() time.Time { return now }
			presentation, err := a.BeginWorkspaceEnrollment(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			a.logical.mu.Lock()
			tx := lookupWorkspaceTransactionLocked(a.logical, presentation.Ref.ID)
			tx.status = status
			a.logical.mu.Unlock()
			want := contract.EnrollmentNotStarted
			if status == session.AuthorizationPending || status == session.AuthorizationGranted {
				want = contract.EnrollmentPending
			}
			got := connectorInventory(t, r, "session", a.Binding())
			if got.EnrollmentState != want || !reflect.DeepEqual(got.Connectors, initialConnectorRows()) {
				t.Fatalf("status %s: %+v", status, got)
			}
			now = presentation.Ref.ExpiresAt
			got = connectorInventory(t, r, "session", a.Binding())
			if got.EnrollmentState != "not_started" {
				t.Fatalf("expired status %s: %+v", status, got)
			}
			a.logical.mu.RLock()
			unchanged := tx.status == status && tx.state != "" && len(a.logical.authorizations) == 1
			a.logical.mu.RUnlock()
			if !unchanged || requests.Load() != 0 || queries.calls != 0 {
				t.Fatal("inspection settled transaction or performed I/O")
			}
		})
	}
	t.Run("anonymous needs no enrollment", func(t *testing.T) {
		r := testAnonymousRuntime(t)
		t.Cleanup(func() { _ = r.Close() })
		construction, err := compileToolHiveConstruction([]ToolHiveProfile{{Name: "anonymous", URL: "https://anonymous.example/mcp", Auth: authNone}}, "")
		if err != nil {
			t.Fatal(err)
		}
		r.process = &Process{Runtime: r, construction: construction}
		a := testAttachment(t, r)
		if err := a.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := connectorInventory(t, r, "session", a.Binding())
		if got.EnrollmentState != "not_required" || got.Connectors[0].CatalogueState != "discovered" {
			t.Fatalf("anonymous = %+v", got)
		}
	})
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("atomic freeze fail=%v", fail), func(t *testing.T) {
			r, a, queries, _ := connectorFixture(t)
			presentation := beginAndGrantWorkspaceEnrollment(t, r, a)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			r.process.queryAuthenticated = func(ctx context.Context, source oauth2.TokenSource, backend string) (AuthenticatedCapabilities, error) {
				if backend == "declared" {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return AuthenticatedCapabilities{}, ctx.Err()
					}
					if fail {
						return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
					}
				}
				return queries.query(ctx, source, backend)
			}
			done := make(chan error, 1)
			go func() {
				result, err := a.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
				want := contract.WorkspaceEnrollmentConnected
				if fail {
					want = contract.WorkspaceEnrollmentFailed
				}
				if err == nil && result.Status != want {
					err = fmt.Errorf("freeze status = %s, want %s", result.Status, want)
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("freeze did not reach second backend")
			}
			for range 20 {
				got := connectorInventory(t, r, "session", a.Binding())
				if got.EnrollmentState != "pending" || !reflect.DeepEqual(got.Connectors, initialConnectorRows()) {
					t.Fatalf("partial publication = %+v", got)
				}
			}
			before := connectorInventory(t, r, "session", a.Binding())
			after := before
			after.Connectors = initialConnectorRows()
			after.EnrollmentState = "not_started"
			if !fail {
				after.EnrollmentState = "completed"
				for i := range after.Connectors {
					after.Connectors[i].CatalogueState = "discovered"
				}
				after.Connectors[0].ToolCount = 1
			}
			unblock()
			for range 100 {
				got := connectorInventory(t, r, "session", a.Binding())
				if !reflect.DeepEqual(got, before) && !reflect.DeepEqual(got, after) {
					t.Fatalf("mixed publication snapshot: %+v", got)
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("freeze did not finish")
			}
			got := connectorInventory(t, r, "session", a.Binding())
			if fail {
				if got.EnrollmentState != "not_started" || !reflect.DeepEqual(got.Connectors, initialConnectorRows()) {
					t.Fatalf("failure erased static routes or published partial: %+v", got)
				}
			} else if got.EnrollmentState != "completed" || got.Connectors[0].ToolCount != 1 || got.Connectors[3].CatalogueState != "discovered" {
				t.Fatalf("incomplete freeze = %+v", got)
			}
		})
	}
}

func TestBrokerMCPStatus_Scenario2_Lifecycle(t *testing.T) {
	r, a, _, _ := connectorFixture(t)
	binding := a.Binding()
	assertUnavailable := func(runtime *Runtime, id session.SessionID, binding session.ExternalBinding) {
		t.Helper()
		got := connectorInventory(t, runtime, id, binding)
		if got.Availability != "unavailable" || got.EnrollmentState != "unknown" || got.TotalConnectors != 4 {
			t.Fatalf("unavailable = %+v", got)
		}
		for _, row := range got.Connectors {
			if row.CatalogueState != "unknown" || row.ToolCount != 0 {
				t.Fatalf("stale row = %+v", row)
			}
		}
	}
	assertUnavailable(r, "missing", binding)
	assertUnavailable(r, "session", "")
	assertUnavailable(r, "session", "wrong-binding")
	if len(r.sessions) != 1 || r.nextGeneration != 1 {
		t.Fatal("inspection attached or created state")
	}
	provisional, _ := attach(t, r, "provisional")
	assertUnavailable(r, "provisional", provisional.Binding())
	if err := provisional.Abort(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := connectorInventory(t, r, "session", binding); got.Availability != "available" {
		t.Fatalf("local close lost logical state: %+v", got)
	}
	if _, err := r.DeleteSession(t.Context(), "session"); err != nil {
		t.Fatal(err)
	}
	assertUnavailable(r, "session", binding)
	replacement := testAttachment(t, r)
	if err := replacement.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertUnavailable(r, "session", binding)
	if got := connectorInventory(t, r, "session", replacement.Binding()); got.Availability != "available" {
		t.Fatalf("replacement unavailable: %+v", got)
	}
	restarted, _, _, _ := connectorFixture(t)
	assertUnavailable(restarted, "session", binding)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	assertUnavailable(r, "session", replacement.Binding())

	for _, action := range []string{"local close", "delete", "runtime close", "process close"} {
		t.Run("concurrent "+action, func(t *testing.T) {
			r, a, _, _ := connectorFixture(t)
			presentation := beginAndGrantWorkspaceEnrollment(t, r, a)
			if result, err := a.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref); err != nil || result.Status != contract.WorkspaceEnrollmentConnected {
				t.Fatalf("publish before shutdown = %+v, %v", result, err)
			}
			before := connectorInventory(t, r, "session", a.Binding())
			after := connectorInventory(t, r, "missing", a.Binding())
			if action == "local close" {
				after = before
			}
			start := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				<-start
				var err error
				switch action {
				case "local close":
					_, err = a.Close(t.Context())
				case "delete":
					_, err = r.DeleteSession(t.Context(), "session")
				case "runtime close":
					err = r.Close()
				case "process close":
					err = r.process.Close()
				}
				done <- err
			}()
			close(start)
			for range 100 {
				got := connectorInventory(t, r, "session", a.Binding())
				if !reflect.DeepEqual(got, before) && !reflect.DeepEqual(got, after) {
					t.Fatalf("mixed shutdown snapshot: %+v", got)
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("inspection blocked shutdown")
			}
			if got := connectorInventory(t, r, "session", a.Binding()); !reflect.DeepEqual(got, after) {
				t.Fatalf("after shutdown: %+v", got)
			}
		})
	}
}

func TestConnectorInventoryBundledConstruction(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	declared := protectedToolHiveProfile("declared")
	declared.Static = []StaticTool{{Name: "standin", Schema: json.RawMessage(`{"type":"object"}`)}}
	p, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{protectedToolHiveProfile("hidden"), declared}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	a := testAttachment(t, p.Runtime)
	if err := a.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := connectorInventory(t, p.Runtime, "session", a.Binding())
	want := []contract.ConnectorStatus{{Name: "hidden", CatalogueState: "hidden"}, {Name: "declared", CatalogueState: "declared", ToolCount: 1}}
	if got.Availability != "available" || got.EnrollmentState != "not_started" || !reflect.DeepEqual(got.Connectors, want) {
		t.Fatalf("real bundled construction = %+v", got)
	}
}

func TestConnectorInventoryBoundsAndCancellation(t *testing.T) {
	r, a, _, _ := connectorFixture(t)
	profiles := make([]ToolHiveProfile, 257)
	for i := range profiles {
		profiles[i] = ToolHiveProfile{Name: fmt.Sprintf("anonymous-%d", i), URL: "https://anonymous.example/mcp", Auth: authNone}
	}
	profiles[0].Name = "bad\xff\n\u202e" + strings.Repeat("界", 140)
	construction, err := compileToolHiveConstruction(profiles, "")
	if err != nil {
		t.Fatal(err)
	}
	r.process.construction = construction
	got := connectorInventory(t, r, "session", a.Binding())
	wantName := "bad�" + strings.Repeat("界", 124)
	if got.TotalConnectors != 257 || !got.Truncated || len(got.Connectors) != 256 || got.Connectors[0].Name != wantName || got.Connectors[255].Name != "anonymous-255" {
		t.Fatalf("bounded inventory = %+v", got)
	}
	// An unprotected connector with zero discovered tools, before any enrollment
	// completes, must still read "discovered" (the !protected branch) rather
	// than being mistaken for "hidden" (a protected connector's pre-enrollment
	// state) just because its tool count happens to be zero.
	if got.Connectors[1].Name != "anonymous-1" || got.Connectors[1].CatalogueState != "discovered" || got.Connectors[1].ToolCount != 0 {
		t.Fatalf("unprotected zero-tool connector = %+v", got.Connectors[1])
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.InspectConnectors(ctx, "session", a.Binding()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled inspection = %v", err)
	}
}
