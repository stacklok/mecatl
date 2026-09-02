package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

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

	frozen, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, "token", []string{"Read"})
	if err != nil {
		t.Fatalf("FreezeAuthenticatedCatalogue: %v", err)
	}
	if got, want := queries.order, []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query order = %v, want %v", got, want)
	}
	if got, want := toolNames(attachment.Tools()), []string{"mcp__anonymous__status", "mcp__first__one", "mcp__second__two"}; !reflect.DeepEqual(got, want) {
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

	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, "token", nil); !errors.Is(err, ErrAuthenticatedDiscovery) {
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
			if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, "token", nil); !errors.Is(err, ErrInvalidCatalogue) {
				t.Fatalf("FreezeAuthenticatedCatalogue error = %v, want invalid catalogue", err)
			}
			if got := toolNames(attachment.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status"}) {
				t.Fatalf("invalid metadata published %v", got)
			}
		})
	}
}

func TestFreezeAuthenticatedCatalogueUsesLiveDefinitionsAndRejectsCollisions(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	attachment := testAttachment(t, runtime)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"first": {
		Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__live", Description: "live", Schema: json.RawMessage(`{"type":"object"}`)}},
	}}}
	process := testCatalogueProcess(runtime, queries, "first")
	process.construction.staticByBackend = map[string][]StaticTool{"first": {{Name: "static", Description: "must not replace live", Schema: json.RawMessage(`{"type":"object"}`)}}}
	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, "token", nil); err != nil {
		t.Fatalf("FreezeAuthenticatedCatalogue: %v", err)
	}
	if got := toolNames(attachment.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status", "mcp__first__live"}) {
		t.Fatalf("static declaration replaced live definition: %v", got)
	}

	other := testAttachment(t, runtime)
	if _, err := other.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, "token", []string{"mcp__first__live"}); !errors.Is(err, ErrInvalidCatalogue) {
		t.Fatalf("occupied collision error = %v, want invalid catalogue", err)
	}
	if got := toolNames(other.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status"}) {
		t.Fatalf("collision published protected tools: %v", got)
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
			_, err := attachment.FreezeAuthenticatedCatalogue(context.Background(), ref, process, "token", nil)
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

func (q *orderedCapabilityQueries) query(_ context.Context, _ ToolHiveAuthSessionID, backend string) (AuthenticatedCapabilities, error) {
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
	return &Process{Runtime: runtime, construction: toolHiveConstruction{protectedBackends: backends}, queryAuthenticated: queries.query, ctx: context.Background(), cancel: func() {}}
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
