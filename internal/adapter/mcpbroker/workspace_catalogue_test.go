package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestADR_0310_AuthenticatedCatalogueReplacesDeclaredMembership(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	attachment := testAttachment(t, runtime)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"first":  {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__one", Description: "one", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}},
		"second": {Backend: "second", Tools: []ToolDefinition{{Backend: "second", Name: "mcp__second__two", Description: "two", Schema: json.RawMessage(`{"type":"object"}`)}}},
	}}
	process := testCatalogueProcess(runtime, queries, "first", "second")

	frozen, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), []string{"Read"})
	if err != nil {
		t.Fatalf("FreezeAuthenticatedCatalogue: %v", err)
	}
	if got, want := queries.order, []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query order = %v, want %v", got, want)
	}
	if got, want := toolNames(attachment.Tools()), []string{"mcp__anonymous__status", "mcp__first__one", "mcp__second__two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attachment tools = %v, want complete authenticated membership %v", got, want)
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

func TestADR_0310_AuthenticatedReplacementFailsAtomically(t *testing.T) {
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

	collisionRaw, _, err := runtime.AttachSession(t.Context(), "collision-session")
	if err != nil {
		t.Fatal(err)
	}
	collision := collisionRaw.(*Attachment)
	collisionProcess := testCatalogueProcess(runtime, &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"first": {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__one", Description: "one", Schema: json.RawMessage(`{"type":"object"}`)}}},
	}}, "first")
	if _, err := collision.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), collisionProcess, staticTokenSource("opaque-broker-token"), []string{"mcp__first__one"}); !errors.Is(err, ErrInvalidCatalogue) {
		t.Fatalf("collision error = %v, want invalid catalogue", err)
	}
	if got := toolNames(collision.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status"}) {
		t.Fatalf("collision published partial catalogue: %v", got)
	}

	raceRaw, _, err := runtime.AttachSession(t.Context(), "close-race-session")
	if err != nil {
		t.Fatal(err)
	}
	raceAttachment := raceRaw.(*Attachment)
	started := make(chan struct{})
	release := make(chan struct{})
	raceProcess := testCatalogueProcess(runtime, &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"first": {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__one", Description: "one", Schema: json.RawMessage(`{"type":"object"}`)}}},
	}, started: started, release: release}, "first")
	freezeDone := make(chan error, 1)
	go func() {
		_, freezeErr := raceAttachment.FreezeAuthenticatedCatalogue(context.Background(), testEnrollmentRef(), raceProcess, staticTokenSource("opaque-broker-token"), nil)
		freezeDone <- freezeErr
	}()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- raceProcess.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		raceProcess.lifecycleMu.Lock()
		closed := raceProcess.closed
		raceProcess.lifecycleMu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Process.Close did not win the in-flight discovery race")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if freezeErr := <-freezeDone; !errors.Is(freezeErr, ErrAuthenticatedDiscovery) {
		t.Fatalf("close-raced freeze error = %v, want authenticated discovery failure", freezeErr)
	}
	if closeErr := <-closeDone; closeErr != nil {
		t.Fatalf("Process.Close: %v", closeErr)
	}
	if got := toolNames(raceAttachment.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status"}) {
		t.Fatalf("close race published partial catalogue: %v", got)
	}
}

func TestADR_0310_AuthenticatedReplacementUsesAdmissionBoundary(t *testing.T) {
	privateRuntime := testAnonymousRuntime(t)
	privateAttachment := testAttachment(t, privateRuntime)
	privateProcess := discoveryProcess(&discoveryQueries{response: &aggregator.BackendCapabilities{BackendID: "private", Tools: []vmcp.Tool{{
		BackendID: "private", Name: "tool", Description: "contains broker-secret", InputSchema: map[string]any{"type": "object"},
	}}}}, identityMiddleware("broker-secret", "provider-private", "upstream-private"), "provider-private")
	privateProcess.Runtime = privateRuntime
	privateProcess.construction.protectedBackends = []string{"private"}
	if _, err := privateAttachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), privateProcess, staticTokenSource("broker-secret"), nil); !errors.Is(err, ErrAuthenticatedDiscovery) {
		t.Fatalf("private material error = %v, want authenticated discovery failure", err)
	}
	if got := toolNames(privateAttachment.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status"}) {
		t.Fatalf("private metadata published %v", got)
	}

	oversizedSchema := json.RawMessage(`{"value":"` + strings.Repeat("x", 1<<20) + `"}`)
	for _, definition := range []ToolDefinition{
		{Backend: "first", Name: "mcp__first__bad name", Description: "safe", Schema: json.RawMessage(`{"type":"object"}`)},
		{Backend: "first", Name: "mcp__first__tool", Description: string([]byte{0xff}), Schema: json.RawMessage(`{"type":"object"}`)},
		{Backend: "first", Name: "mcp__first__tool", Description: strings.Repeat("x", 64<<10+1), Schema: json.RawMessage(`{"type":"object"}`)},
		{Backend: "first", Name: "mcp__first__tool", Description: "safe", Schema: json.RawMessage(`[]`)},
		{Backend: "first", Name: "mcp__first__tool", Description: "safe", Schema: json.RawMessage(`{`)},
		{Backend: "first", Name: "mcp__first__tool", Description: "safe", Schema: oversizedSchema},
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

func TestADR_0310_AuthenticatedCatalogueReplacesDeclaredToolMetadata(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	staticSchema := json.RawMessage(`{"type":"object","properties":{"static":{"type":"string"}}}`)
	liveSchema := json.RawMessage(`{"type":"object","properties":{"live":{"type":"boolean"}}}`)
	static := route{backend: "first", spec: tool.ToolSpec{Name: "mcp__first__same", Description: "static description", Schema: staticSchema}, readOnly: false, oauth: &oauthRoute{}, broker: true}
	runtime.catalogue = &Catalogue{routes: append(runtime.catalogue.routes, static)}
	attachment := testAttachment(t, runtime)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"first": {
		Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__same", Description: "live description", Schema: liveSchema, ReadOnly: true}},
	}}}
	process := testCatalogueProcess(runtime, queries, "first")

	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); err != nil {
		t.Fatalf("FreezeAuthenticatedCatalogue: %v", err)
	}
	if got := toolNames(attachment.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status", "mcp__first__same"}) {
		t.Fatalf("authenticated tool was not published exactly once: %v", got)
	}
	got := toolByName(t, attachment, "mcp__first__same")
	if spec := got.Spec(); spec.Description != "live description" || !reflect.DeepEqual(spec.Schema, liveSchema) || !got.ReadOnly() {
		t.Fatalf("authenticated metadata = (%q, %s, readOnly=%v), want live values", spec.Description, spec.Schema, got.ReadOnly())
	}
}

func TestADR_0310_AuthenticatedReadOnlyHintReplacesStaticHint(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	static := route{backend: "first", spec: tool.ToolSpec{Name: "mcp__first__declared", Description: "declared", Schema: json.RawMessage(`{"type":"object"}`)}, readOnly: true, oauth: &oauthRoute{}, broker: true}
	runtime.catalogue = &Catalogue{routes: append(runtime.catalogue.routes, static)}
	attachment := testAttachment(t, runtime)
	process := testCatalogueProcess(runtime, &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"first": {Backend: "first", Tools: []ToolDefinition{
			{Backend: "first", Name: "mcp__first__declared", Description: "live", Schema: json.RawMessage(`{"type":"object"}`)},
			{Backend: "first", Name: "mcp__first__read", Description: "read", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
		}},
	}}, "first")

	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); err != nil {
		t.Fatalf("FreezeAuthenticatedCatalogue: %v", err)
	}
	catalogue := tool.NewCatalog()
	for _, candidate := range attachment.Tools() {
		catalogue.MustRegister(candidate)
	}
	if got := toolNames(catalogue.Available(session.ModePlan)); !reflect.DeepEqual(got, []string{"mcp__anonymous__status", "mcp__first__read"}) {
		t.Fatalf("plan-mode tools = %v, want live read-only hints only", got)
	}
	if toolByName(t, attachment, "mcp__first__declared").ReadOnly() {
		t.Fatal("live false/absent read-only hint retained the static true hint")
	}
}

func TestADR_0310_AuthenticatedDeclaredMetadataIsSessionSpecific(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	static := route{backend: "first", spec: tool.ToolSpec{Name: "mcp__first__same", Description: "static", Schema: json.RawMessage(`{"type":"object"}`)}, oauth: &oauthRoute{}, broker: true}
	runtime.catalogue = &Catalogue{routes: append(runtime.catalogue.routes, static)}
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"first": {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__same", Description: "user one", Schema: json.RawMessage(`{"type":"object","title":"one"}`), ReadOnly: true}}}}}
	process := testCatalogueProcess(runtime, queries, "first")

	firstRaw, _, err := runtime.AttachSession(t.Context(), "first-session")
	if err != nil {
		t.Fatal(err)
	}
	first := firstRaw.(*Attachment)
	if _, err := first.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); err != nil {
		t.Fatal(err)
	}
	queries.responses["first"] = AuthenticatedCapabilities{Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__same", Description: "user two", Schema: json.RawMessage(`{"type":"object","title":"two"}`)}}}
	secondRaw, _, err := runtime.AttachSession(t.Context(), "second-session")
	if err != nil {
		t.Fatal(err)
	}
	second := secondRaw.(*Attachment)
	if _, err := second.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); err != nil {
		t.Fatal(err)
	}

	firstTool := toolByName(t, first, "mcp__first__same")
	secondTool := toolByName(t, second, "mcp__first__same")
	if firstTool.Spec().Description != "user one" || !firstTool.ReadOnly() || secondTool.Spec().Description != "user two" || secondTool.ReadOnly() || reflect.DeepEqual(firstTool.Spec().Schema, secondTool.Spec().Schema) {
		t.Fatalf("session metadata leaked or retained static values: first=%+v/%v second=%+v/%v", firstTool.Spec(), firstTool.ReadOnly(), secondTool.Spec(), secondTool.ReadOnly())
	}
}

func TestADR_0310_ReplacedDeclaredToolRemainsBrokerRouted(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	static := route{backend: "first", spec: tool.ToolSpec{Name: "mcp__first__same", Description: "static", Schema: json.RawMessage(`{"type":"object"}`)}, oauth: &oauthRoute{}, broker: true}
	runtime.catalogue = &Catalogue{routes: append(runtime.catalogue.routes, static)}
	attachment := testAttachment(t, runtime)
	process := testCatalogueProcess(runtime, &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"first": {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__same", Description: "live", Schema: json.RawMessage(`{"type":"object"}`)}}}}}, "first")
	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); err != nil {
		t.Fatal(err)
	}

	var called bool
	runtime.authorizedCaller = func(_ context.Context, _ SessionRef, backend string, call session.ToolCall, tokens oauth2.TokenSource) (session.ToolResult, error) {
		token, err := tokens.Token()
		if err != nil || token.AccessToken != "opaque-broker-token" || backend != "first" || call.Name != "mcp__first__same" {
			t.Fatalf("broker call = backend %q call %q token %+v err %v", backend, call.Name, token, err)
		}
		called = true
		return session.NewToolResult(call.ID, "broker result"), nil
	}
	attachment.logical.mu.Lock()
	attachment.logical.brokerCredential = &oauthGrant{token: &oauth2.Token{AccessToken: "opaque-broker-token", TokenType: "Bearer"}, executed: make(map[session.ToolCallID][32]byte)}
	attachment.logical.mu.Unlock()
	wrapped := toolByName(t, attachment, "mcp__first__same")
	if _, asksAgain := wrapped.(tool.AuthorizationRequester); asksAgain {
		t.Fatal("frozen broker route exposed a second authorization path")
	}
	result, err := wrapped.Execute(t.Context(), session.NewToolCall("call", wrapped.Spec().Name, json.RawMessage(`{}`)), tool.Environment{})
	if err != nil || result.Content != "broker result" || !called {
		t.Fatalf("broker execution = (%+v, %v), called=%v", result, err, called)
	}
}

func TestADR_0310_FreshSessionPerformsFreshAuthenticatedDiscovery(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"first": {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__tool", Description: "old", Schema: json.RawMessage(`{"type":"object"}`)}}}}}
	process := testCatalogueProcess(runtime, queries, "first")
	firstRaw, _, err := runtime.AttachSession(t.Context(), "fresh-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstRaw.(*Attachment).FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); err != nil {
		t.Fatal(err)
	}
	queries.responses["first"] = AuthenticatedCapabilities{Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__tool", Description: "new", Schema: json.RawMessage(`{"type":"object"}`)}}}
	secondRaw, _, err := runtime.AttachSession(t.Context(), "fresh-two")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondRaw.(*Attachment).FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("opaque-broker-token"), nil); err != nil {
		t.Fatal(err)
	}
	if queries.calls != 2 || toolByName(t, firstRaw.(*Attachment), "mcp__first__tool").Spec().Description != "old" || toolByName(t, secondRaw.(*Attachment), "mcp__first__tool").Spec().Description != "new" {
		t.Fatalf("fresh-session discovery calls/metadata = %d/%q/%q", queries.calls, toolByName(t, firstRaw.(*Attachment), "mcp__first__tool").Spec().Description, toolByName(t, secondRaw.(*Attachment), "mcp__first__tool").Spec().Description)
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
	conflicting := ref
	conflicting.ID = "other-enrollment"
	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), conflicting, process, staticTokenSource("opaque-broker-token"), nil); !errors.Is(err, ErrAuthenticatedDiscovery) {
		t.Fatalf("conflicting enrollment error = %v, want authenticated discovery failure", err)
	}
	if queries.calls != 1 {
		t.Fatalf("conflicting enrollment triggered discovery; calls = %d", queries.calls)
	}
	if got := toolNames(attachment.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status", "mcp__first__tool"}) {
		t.Fatalf("conflicting enrollment changed catalogue: %v", got)
	}
}

type orderedCapabilityQueries struct {
	mu          sync.Mutex
	responses   map[string]AuthenticatedCapabilities
	order       []string
	calls       int
	fail        string
	started     chan struct{}
	startedOnce sync.Once
	release     chan struct{}
}

func (q *orderedCapabilityQueries) query(ctx context.Context, _ oauth2.TokenSource, backend string) (AuthenticatedCapabilities, error) {
	q.mu.Lock()
	q.order = append(q.order, backend)
	q.calls++
	fail := backend == q.fail
	response := q.responses[backend]
	started := q.started
	release := q.release
	q.mu.Unlock()
	if started != nil {
		q.startedOnce.Do(func() { close(started) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return AuthenticatedCapabilities{}, ctx.Err()
		}
	}
	if fail {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	return response, nil
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

func testAttachment(t *testing.T, runtime *Runtime) *Attachment {
	t.Helper()
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	return attached.(*Attachment)
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
