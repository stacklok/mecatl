package mcpbroker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"

	"golang.org/x/oauth2"
)

type authenticatedDiscoveryFunc func(context.Context, oauth2.TokenSource, string) (AuthenticatedCapabilities, error)

func (f authenticatedDiscoveryFunc) QueryAuthenticatedCapabilities(ctx context.Context, credential oauth2.TokenSource, backend string) (AuthenticatedCapabilities, error) {
	return f(ctx, credential, backend)
}

type testPublicationGate struct {
	mu     sync.Mutex
	closed bool
}

func (g *testPublicationGate) WhileOpen(commit func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrAuthenticatedDiscovery
	}
	return commit()
}

func (q *orderedCapabilityQueries) QueryAuthenticatedCapabilities(ctx context.Context, credential oauth2.TokenSource, backend string) (AuthenticatedCapabilities, error) {
	return q.query(ctx, credential, backend)
}

func TestRuntimeEnrollmentConfigurationIsCopied(t *testing.T) {
	r := testAnonymousRuntime(t)
	target := &oauthRoute{clientID: "original", scopes: []string{"openid"}}
	r.catalogue.routes = append(r.catalogue.routes, route{oauth: target, broker: true})
	config := enrollmentConfig{backends: []string{"first", "second"}, occupied: []string{"Read"}, connectors: []configuredConnector{{id: "first", name: "First", protected: true}}, target: target}
	r.configureEnrollment(config, &orderedCapabilityQueries{}, &testPublicationGate{})
	config.backends[0], config.occupied[0], config.connectors[0].name = "changed", "changed", "changed"
	target.clientID, target.scopes[0] = "changed", "changed"
	if r.enrollment.backends[0] != "first" || r.enrollment.backends[1] != "second" || r.enrollment.occupied[0] != "Read" || r.enrollment.connectors[0].name != "First" || r.enrollment.target.clientID != "original" || r.enrollment.target.scopes[0] != "openid" {
		t.Fatal("runtime enrollment configuration aliases construction inputs")
	}
	if r.catalogue.routes[1].oauth != r.enrollment.target {
		t.Fatal("bundle routes lost their canonical target identity")
	}
}

type publicationGateFunc func(func() error) error

func (f publicationGateFunc) WhileOpen(commit func() error) error { return f(commit) }

func TestProcessPublicationGateSerializesShutdown(t *testing.T) {
	r := testAnonymousRuntime(t)
	p := &Process{Runtime: r}
	gate := processPublicationGate{process: p, runtime: r}
	var published atomic.Bool
	p.resources = []ownedResource{{close: func() error {
		if !published.Load() {
			t.Error("shutdown retired resources before publication completed")
		}
		return nil
	}}}
	entered, release := make(chan struct{}), make(chan struct{})
	committed := make(chan error, 1)
	go func() {
		committed <- gate.WhileOpen(func() error {
			close(entered)
			<-release
			published.Store(true)
			return nil
		})
	}()
	<-entered
	// Probe the actual mutex while the callback is executing, rather than
	// inferring lock ownership from a goroutine's start signal.
	if p.lifecycleMu.TryLock() {
		p.lifecycleMu.Unlock()
		close(release)
		<-committed
		t.Fatal("publication callback did not hold lifecycle admission")
	}
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	close(release)
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if !published.Load() {
		t.Fatal("shutdown completed without publication")
	}
	called := false
	if err := gate.WhileOpen(func() error { called = true; return nil }); !errors.Is(err, ErrAuthenticatedDiscovery) || called {
		t.Fatalf("shutdown admitted publication: called=%v err=%v", called, err)
	}
}

func TestProcessPublicationGateRejectsDifferentOwner(t *testing.T) {
	r := testAnonymousRuntime(t)
	p := &Process{Runtime: r}
	gate := processPublicationGate{process: p, runtime: r}
	p.Runtime = testAnonymousRuntime(t)
	if err := gate.WhileOpen(func() error { t.Error("wrong runtime admitted"); return nil }); !errors.Is(err, ErrAuthenticatedDiscovery) {
		t.Fatal(err)
	}
}

func TestProcessRollbackRevokesAdmissionBeforeResourceCleanup(t *testing.T) {
	r := testAnonymousRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	p := &Process{Runtime: r, ctx: ctx, cancel: cancel}
	gate := processPublicationGate{process: p, runtime: r}
	closed := false
	p.resources = []ownedResource{{close: func() error {
		closed = true
		if ctx.Err() == nil {
			t.Error("resource closed before process cancellation")
		}
		if err := gate.WhileOpen(func() error { t.Error("rollback admitted publication"); return nil }); err == nil {
			t.Error("rollback did not close admission")
		}
		return nil
	}}}
	p.rollback()
	if !closed {
		t.Fatal("rollback skipped resource cleanup")
	}
}

func TestLazyPublicationRejectsStaleAuthority(t *testing.T) {
	for _, change := range []string{"deleted", "runtime closed", "missing session", "transaction", "status", "grant", "attachment"} {
		t.Run(change, func(t *testing.T) {
			r := testAnonymousRuntime(t)
			r.publication = &testPublicationGate{}
			a := testAttachment(t, r)
			logical := a.logical
			tx := &authorizationTransaction{status: session.AuthorizationGranted}
			grant := &oauthGrant{}
			logical.authorizations[tx.identity] = tx
			logical.brokerCredential = grant
			bundle := grantedBundle{transaction: tx, grant: grant}
			switch change {
			case "deleted":
				logical.deleted = true
			case "runtime closed":
				r.closed = true
			case "missing session":
				delete(r.sessions, logical.ref.id)
			case "transaction":
				logical.authorizations[tx.identity] = &authorizationTransaction{status: session.AuthorizationGranted}
			case "status":
				tx.status = session.AuthorizationCancelled
			case "grant":
				logical.brokerCredential = &oauthGrant{}
			case "attachment":
				a.closed = true
			}
			before := a.catalogue
			a.mu.Lock()
			published := a.publishRefreshedCatalogue(logical, bundle, &attachmentCatalogue{})
			a.mu.Unlock()
			if published || a.catalogue != before {
				t.Fatal("stale authority published a catalogue")
			}
		})
	}
}

func TestEnrollmentFinalPublicationRejectsStaleAuthority(t *testing.T) {
	for _, change := range []string{"shutdown", "deleted", "transaction", "status", "grant", "attachment"} {
		t.Run(change, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"outer-credential","token_type":"Bearer","expires_in":3600}`))
			}))
			t.Cleanup(server.Close)
			r := newWorkspaceEnrollmentRuntime(t, server, &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"backend": {Backend: "backend"}}}, "backend")
			var a *Attachment
			var presentation contract.WorkspaceEnrollmentPresentation
			base := r.publication
			calls := 0
			armed := false
			r.publication = publicationGateFunc(func(commit func() error) error {
				if armed {
					calls++
				}
				// Freeze validates its private candidate first; this is the
				// subsequent logical-enrollment publication, with a.mu held.
				if calls == 2 {
					switch change {
					case "shutdown":
						g := base.(*testPublicationGate)
						g.mu.Lock()
						g.closed = true
						g.mu.Unlock()
					case "attachment":
						a.closed = true
					default:
						a.logical.mu.Lock()
						tx := lookupWorkspaceTransactionLocked(a.logical, presentation.Ref.ID)
						switch change {
						case "deleted":
							a.logical.deleted = true
						case "transaction":
							delete(a.logical.authorizations, tx.identity)
						case "status":
							tx.status = session.AuthorizationCancelled
						case "grant":
							a.logical.brokerCredential = &oauthGrant{}
						}
						a.logical.mu.Unlock()
					}
				}
				return base.WhileOpen(commit)
			})
			a = testAttachment(t, r)
			presentation = beginAndGrantWorkspaceEnrollment(t, r, a)
			armed = true
			before := a.catalogue
			if _, err := a.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref); err == nil {
				t.Fatal("stale enrollment authority was accepted")
			}
			if calls != 2 || a.catalogue != before || a.logical.completedEnrollment != nil {
				t.Fatal("final publication was bypassed or installed a stale catalogue")
			}
		})
	}
}

func TestCompletedEnrollmentPublicationUsesGate(t *testing.T) {
	r := testAnonymousRuntime(t)
	gate := &testPublicationGate{}
	r.configureEnrollment(enrollmentConfig{backends: []string{"backend"}}, &orderedCapabilityQueries{}, gate)
	a := testAttachment(t, r)
	completed := &completedWorkspaceEnrollment{ref: testEnrollmentRef(), routes: cloneRoutes(r.catalogue.routes)}
	a.logical.completedEnrollment = completed
	gate.closed = true
	before := a.catalogue
	if _, err := a.installCompletedEnrollment(completed); !errors.Is(err, ErrAuthenticatedDiscovery) || a.catalogue != before {
		t.Fatalf("closed infrastructure admitted completed enrollment: %v", err)
	}
}

func TestCatalogueUsesCapabilitiesWithoutProcess(t *testing.T) {
	r := testAnonymousRuntime(t)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"backend": {Backend: "backend"}}}
	r.configureEnrollment(enrollmentConfig{backends: []string{"backend"}}, queries, &testPublicationGate{})
	a := testAttachment(t, r)
	if _, err := a.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), staticTokenSource("outer-credential")); err != nil {
		t.Fatal(err)
	}
	if queries.calls != 1 {
		t.Fatal("catalogue did not use injected discovery")
	}
}

func TestCatalogueShutdownAfterDiscoveryPreventsPublication(t *testing.T) {
	r := testAnonymousRuntime(t)
	gate := &testPublicationGate{}
	discovery := authenticatedDiscoveryFunc(func(_ context.Context, _ oauth2.TokenSource, backend string) (AuthenticatedCapabilities, error) {
		gate.mu.Lock()
		gate.closed = true
		gate.mu.Unlock()
		return AuthenticatedCapabilities{Backend: backend}, nil
	})
	r.configureEnrollment(enrollmentConfig{backends: []string{"backend"}}, discovery, gate)
	a := testAttachment(t, r)
	before := a.catalogue
	if _, err := a.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), staticTokenSource("outer-credential")); !errors.Is(err, ErrAuthenticatedDiscovery) || a.catalogue != before {
		t.Fatalf("shutdown after discovery published: %v", err)
	}
}

func TestProcessShutdownDoesNotDeadlockInspectionAndPublication(t *testing.T) {
	r := testAnonymousRuntime(t)
	p := &Process{Runtime: r}
	r.publication = processPublicationGate{process: p, runtime: r}
	r.enrollment = &enrollmentConfig{}
	a := testAttachment(t, r)
	if err := a.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			_, _ = r.InspectConnectors(context.Background(), "session", a.Binding())
			a.mu.Lock()
			_ = r.publication.WhileOpen(func() error { a.logical.mu.Lock(); a.logical.mu.Unlock(); return nil })
			a.mu.Unlock()
		}
	}()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publication/inspection blocked shutdown")
	}
	inventory, err := r.InspectConnectors(t.Context(), "session", a.Binding())
	if err != nil || inventory.Availability != contract.AvailabilityUnavailable {
		t.Fatalf("closed inspection changed contract: %+v %v", inventory, err)
	}
}
