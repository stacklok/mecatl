package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestFreezeAuthenticatedCataloguePublishesCompleteLiveRoutes(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	attachment := testAttachment(t, runtime)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"first":  {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__one", Description: "one", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}},
		"second": {Backend: "second", Tools: []ToolDefinition{{Backend: "second", Name: "mcp__second__two", Description: "two", Schema: json.RawMessage(`{"type":"object"}`)}}},
	}}
	process := testCatalogueProcess(runtime, queries, "first", "second")
	process.construction.staticByBackend = map[string][]StaticTool{
		"first": {{Name: "declared", Description: "reviewed declaration", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}},
	}

	frozen, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), []string{"Read"})
	if err != nil {
		t.Fatalf("FreezeAuthenticatedCatalogue: %v", err)
	}
	if got, want := queries.order, []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query order = %v, want %v", got, want)
	}
	if got, want := toolNames(attachment.Tools()), []string{"mcp__anonymous__status", "mcp__first__declared", "mcp__first__one", "mcp__second__two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attachment tools = %v, want %v", got, want)
	}
	if got, ok := attachment.lookupRoute("mcp__second__two"); !ok || got.backend != "second" {
		t.Fatalf("authoritative route lookup = %#v, %v", got, ok)
	}
	if _, ok := attachment.lookupRoute("mcp__unknown__tool"); ok {
		t.Fatal("unknown route resolved")
	}
	first := frozen.Tools()
	first[0] = nil
	if got := frozen.Tools(); got[0] == nil {
		t.Fatal("frozen tools alias their slice")
	}
}

func TestFreezeAuthenticatedCatalogueFailureDoesNotPublishPartialRoutes(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	attachment := testAttachment(t, runtime)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"first": {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__one", Description: "one", Schema: json.RawMessage(`{"type":"object"}`)}}},
	}, fail: "second"}
	process := testCatalogueProcess(runtime, queries, "first", "second")

	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); !errors.Is(err, ErrAuthenticatedDiscovery) {
		t.Fatalf("FreezeAuthenticatedCatalogue error = %v, want discovery failure", err)
	}
	if got, want := toolNames(attachment.Tools()), []string{"mcp__anonymous__status"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("partial catalogue published: %v", got)
	}
	if _, ok := attachment.lookupRoute("mcp__first__one"); ok {
		t.Fatal("partial route published")
	}
}

func TestFreezeAuthenticatedCatalogueRejectsInvalidLiveMetadataWithoutPublish(t *testing.T) {
	for _, definition := range []ToolDefinition{
		{Backend: "first", Name: "mcp__first__bad name", Description: "safe", Schema: json.RawMessage(`{"type":"object"}`)},
		{Backend: "first", Name: "mcp__first__tool", Description: string([]byte{0xff}), Schema: json.RawMessage(`{"type":"object"}`)},
		{Backend: "first", Name: "mcp__first__tool", Description: "safe", Schema: json.RawMessage(`[]`)},
		{Backend: "first", Name: "mcp__first__tool", Description: "safe", Schema: json.RawMessage(`{`)},
	} {
		t.Run(definition.Name, func(t *testing.T) {
			runtime := testAnonymousRuntime(t)
			attachment := testAttachment(t, runtime)
			process := testCatalogueProcess(runtime, &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"first": {Backend: "first", Tools: []ToolDefinition{definition}}}}, "first")
			if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); !errors.Is(err, ErrInvalidCatalogue) {
				t.Fatalf("FreezeAuthenticatedCatalogue error = %v, want invalid catalogue", err)
			}
			if got := toolNames(attachment.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status"}) {
				t.Fatalf("invalid metadata published %v", got)
			}
		})
	}
}

func TestADR_0298_FreezeAuthenticatedCatalogueStagesStaticAndLiveDefinitions(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	attachment := testAttachment(t, runtime)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"first": {
		Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__live", Description: "live", Schema: json.RawMessage(`{"type":"object"}`)}},
	}}}
	process := testCatalogueProcess(runtime, queries, "first")
	process.construction.staticByBackend = map[string][]StaticTool{"first": {{Name: "static", Description: "must not replace live", Schema: json.RawMessage(`{"type":"object"}`)}}}
	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); err != nil {
		t.Fatalf("FreezeAuthenticatedCatalogue: %v", err)
	}
	if got := toolNames(attachment.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status", "mcp__first__live", "mcp__first__static"}) {
		t.Fatalf("static and live definitions were not admitted together: %v", got)
	}

	other := testAttachment(t, runtime)
	if _, err := other.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), []string{"mcp__first__live"}); !errors.Is(err, ErrInvalidCatalogue) {
		t.Fatalf("reserved name collision error = %v, want invalid catalogue", err)
	}
	if got := toolNames(other.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status"}) {
		t.Fatalf("collision published protected tools: %v", got)
	}
}

func TestFreezeAuthenticatedCatalogueSupersedesVisibleStaticRoutes(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	static := route{backend: "first", spec: tool.ToolSpec{Name: "mcp__first__declared", Description: "declared", Schema: json.RawMessage(`{"type":"object"}`)}, oauth: &oauthRoute{}, broker: true}
	runtime.catalogue = &Catalogue{routes: append(runtime.catalogue.routes, static)}
	attachment := testAttachment(t, runtime)
	process := testCatalogueProcess(runtime, &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"first": {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__live", Description: "live", Schema: json.RawMessage(`{"type":"object"}`)}}},
	}}, "first")
	process.construction.staticByBackend = map[string][]StaticTool{"first": {{Name: "declared", Description: "declared", Schema: json.RawMessage(`{"type":"object"}`)}}}

	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); err != nil {
		t.Fatalf("FreezeAuthenticatedCatalogue: %v", err)
	}
	if got, want := toolNames(attachment.Tools()), []string{"mcp__anonymous__status", "mcp__first__declared", "mcp__first__live"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("frozen tools = %v, want %v", got, want)
	}
	declared, ok := attachment.lookupRoute("mcp__first__declared")
	if !ok || !declared.broker || declared.oauth != nil {
		t.Fatalf("declared frozen route = %#v, want broker route", declared)
	}
}

func TestFreezeAuthenticatedCatalogueConcurrentFreezeHasOneCatalogue(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	attachment := testAttachment(t, runtime)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"first": {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__tool", Description: "safe", Schema: json.RawMessage(`{"type":"object"}`)}}}}}
	process := testCatalogueProcess(runtime, queries, "first")
	ref := testEnrollmentRef()

	var group sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := attachment.FreezeAuthenticatedCatalogue(context.Background(), ref, process, staticTokenSource("opaque-broker-token"), nil)
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent freeze: %v", err)
		}
	}
	if queries.calls != 1 {
		t.Fatalf("discovery calls = %d, want one winning enrollment", queries.calls)
	}
}

type orderedCapabilityQueries struct {
	mu        sync.Mutex
	responses map[string]AuthenticatedCapabilities
	order     []string
	calls     int
	fail      string
}

func (q *orderedCapabilityQueries) query(_ context.Context, _ oauth2.TokenSource, backend string) (AuthenticatedCapabilities, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.order = append(q.order, backend)
	q.calls++
	if backend == q.fail {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	return q.responses[backend], nil
}

func testCatalogueProcess(runtime *Runtime, queries *orderedCapabilityQueries, backends ...string) *Process {
	return &Process{
		Runtime: runtime, construction: toolHiveConstruction{protectedBackends: backends},
		protectedTarget:    &oauthRoute{authorizationEndpoint: "https://issuer.example/authorize", tokenEndpoint: "https://issuer.example/token", callbackURL: "https://issuer.example/callback", clientID: "broker"},
		queryAuthenticated: queries.query, ctx: context.Background(), cancel: func() {},
	}
}

func testAnonymousRuntime(t *testing.T) *Runtime {
	t.Helper()
	catalogue := &Catalogue{routes: []route{{backend: "anonymous", spec: tool.ToolSpec{Name: "mcp__anonymous__status", Schema: json.RawMessage(`{"type":"object"}`)}, readOnly: true}}}
	runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func testAttachment(t *testing.T, runtime *Runtime) *SessionHandle {
	t.Helper()
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	return attached.(*SessionHandle)
}

func testEnrollmentRef() contract.WorkspaceEnrollmentRef {
	return contract.WorkspaceEnrollmentRef{ID: "enrollment", RequiredServices: 1, ExpiresAt: time.Unix(1, 0)}
}

func toolNames(tools []tool.Tool) []string {
	names := make([]string, len(tools))
	for i, candidate := range tools {
		names[i] = candidate.Spec().Name
	}
	return names
}

// Authenticated discovery runs without the attachment lock: other operations on
// the handle proceed while a provider is slow, and a catalogue that changed
// during discovery is never overwritten by the stale result.
func TestFreezeAuthenticatedCatalogueDoesNotHoldAttachmentLockAcrossDiscovery(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"broker","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, &orderedCapabilityQueries{}, "github")
	entered, release := make(chan struct{}), make(chan struct{})
	runtime.process.queryAuthenticated = func(context.Context, oauth2.TokenSource, string) (AuthenticatedCapabilities, error) {
		close(entered)
		<-release
		return AuthenticatedCapabilities{Backend: "github", Tools: []ToolDefinition{
			{Backend: "github", Name: "mcp__github__list", Description: "list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
		}}, nil
	}
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	handle := attached.(*SessionHandle)
	presentation := beginAndGrantWorkspaceEnrollment(t, runtime, handle)

	observed := make(chan contract.WorkspaceEnrollmentStatus, 1)
	go func() {
		result, _ := handle.ObserveWorkspaceEnrollment(context.Background(), presentation.Ref)
		observed <- result.Status
	}()
	<-entered

	tools := make(chan int, 1)
	go func() { tools <- len(handle.Tools()) }()
	select {
	case <-tools:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("handle operation blocked behind upstream discovery")
	}

	handle.mu.Lock()
	replacement := *handle.catalogue
	handle.catalogue = &replacement
	handle.mu.Unlock()
	close(release)
	if status := <-observed; status == contract.WorkspaceEnrollmentConnected {
		t.Fatal("freeze reported connected over a catalogue that changed during discovery")
	}
	handle.mu.RLock()
	published := handle.catalogue.frozen
	handle.mu.RUnlock()
	if published != nil {
		t.Fatal("stale discovery result replaced the changed catalogue")
	}
}
