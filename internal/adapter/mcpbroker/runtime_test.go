package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func anonymousConfig() mcpauthority.BrokerConfig {
	return mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{
		{Name: "calendar", URL: "https://calendar.invalid/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}},
		{Name: "search", URL: "https://search.invalid/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}},
	}}
}

func discoveredTools() []ToolDefinition {
	return []ToolDefinition{
		{Backend: "search", Name: "mcp__search__query", Description: "Search.", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
		{Backend: "calendar", Name: "mcp__calendar__create", Description: "Create an event.", Schema: json.RawMessage(`{"type":"object"}`)},
	}
}

func TestCompileProducesStableNeutralCatalogue(t *testing.T) {
	discovered := discoveredTools()
	catalogue, err := Compile(anonymousConfig(), discovered, []string{"Read"})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got := catalogue.Specs()
	if names := []string{got[0].Name, got[1].Name}; !reflect.DeepEqual(names, []string{"mcp__calendar__create", "mcp__search__query"}) {
		t.Fatalf("sorted stable names = %v", names)
	}
	if got[0].Description != "Create an event." || string(got[0].Schema) != `{"type":"object"}` {
		t.Fatalf("neutral spec = %+v", got[0])
	}

	// The compiled catalogue owns its schemas and every projection is defensive.
	discovered[0].Schema[0] = '['
	got[0].Schema[0] = '['
	if fresh := catalogue.Specs(); string(fresh[0].Schema) != `{"type":"object"}` || string(fresh[1].Schema) != `{"type":"object"}` {
		t.Fatalf("catalogue schemas aliased caller data: %#v", fresh)
	}
}

func TestCompileRejectsCollisionsAndNonAnonymousDeclarations(t *testing.T) {
	tests := []struct {
		name       string
		config     mcpauthority.BrokerConfig
		discovered []ToolDefinition
		occupied   []string
		want       error
	}{
		{name: "core collision", config: anonymousConfig(), discovered: []ToolDefinition{{Backend: "calendar", Name: "Read"}}, occupied: []string{"Read"}, want: ErrInvalidCatalogue},
		{name: "route collision", config: anonymousConfig(), discovered: []ToolDefinition{{Backend: "calendar", Name: "same"}, {Backend: "search", Name: "same"}}, want: ErrInvalidCatalogue},
		{name: "unknown backend", config: anonymousConfig(), discovered: []ToolDefinition{{Backend: "private", Name: "mcp__private__get"}}, want: ErrInvalidCatalogue},
		{name: "protected deferred", config: mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{{Name: "github", Auth: permconfig.MCPAuthProfile{Mode: "oauth"}}}}, want: ErrProtectedRouteUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.config, test.discovered, test.occupied)
			if !errors.Is(err, test.want) {
				t.Fatalf("Compile error = %v, want %v", err, test.want)
			}
		})
	}
}

type recordedCall struct {
	session session.SessionID
	backend string
	call    session.ToolCall
}

type callRecorder struct {
	mu    sync.Mutex
	calls []recordedCall
}

func (r *callRecorder) call(_ context.Context, ref SessionRef, backend string, call session.ToolCall) (session.ToolResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, recordedCall{session: ref.SessionID(), backend: backend, call: call})
	r.mu.Unlock()
	return session.NewToolResult(call.ID, string(call.Args)), nil
}

func newTestRuntime(t *testing.T) (*Runtime, *callRecorder) {
	t.Helper()
	catalogue, err := Compile(anonymousConfig(), discoveredTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &callRecorder{}
	runtime, err := New(catalogue, recorder.call)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, recorder
}

func attach(t *testing.T, runtime *Runtime, id session.SessionID) (*Attachment, contract.AttachOutcome) {
	t.Helper()
	neutral, outcome, err := runtime.AttachSession(t.Context(), id)
	if err != nil {
		t.Fatalf("AttachSession(%q): %v", id, err)
	}
	attachment, ok := neutral.(*Attachment)
	if !ok {
		t.Fatalf("attachment type = %T", neutral)
	}
	return attachment, outcome
}

func toolByName(t *testing.T, attachment *Attachment, name string) tool.Tool {
	t.Helper()
	for _, candidate := range attachment.Tools() {
		if candidate.Spec().Name == name {
			return candidate
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func TestWrappersBindCanonicalSessionAndPrivateRoute(t *testing.T) {
	runtime, recorder := newTestRuntime(t)
	first, firstOutcome := attach(t, runtime, "session-one")
	second, secondOutcome := attach(t, runtime, "session-two")
	if firstOutcome != contract.AttachCreated || secondOutcome != contract.AttachCreated {
		t.Fatalf("attach outcomes = %q, %q", firstOutcome, secondOutcome)
	}

	firstTool := toolByName(t, first, "mcp__calendar__create")
	secondTool := toolByName(t, second, "mcp__search__query")
	if firstTool.ReadOnly() || !secondTool.ReadOnly() {
		t.Fatalf("read-only projection = %v, %v", firstTool.ReadOnly(), secondTool.ReadOnly())
	}
	if _, err := firstTool.Execute(t.Context(), session.NewToolCall("call-1", firstTool.Spec().Name, json.RawMessage(`{"event":"one"}`)), tool.Environment{}); err != nil {
		t.Fatal(err)
	}
	if _, err := secondTool.Execute(t.Context(), session.NewToolCall("call-2", secondTool.Spec().Name, json.RawMessage(`{"query":"two"}`)), tool.Environment{}); err != nil {
		t.Fatal(err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.calls) != 2 || recorder.calls[0].session != "session-one" || recorder.calls[0].backend != "calendar" || recorder.calls[1].session != "session-two" || recorder.calls[1].backend != "search" {
		t.Fatalf("private routed calls = %+v", recorder.calls)
	}
}

func TestAttachReattachCloseDeleteLifecycle(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	first, outcome := attach(t, runtime, "session-life")
	if outcome != contract.AttachCreated {
		t.Fatalf("first outcome = %q", outcome)
	}
	second, outcome := attach(t, runtime, "session-life")
	if outcome != contract.AttachReattached {
		t.Fatalf("second outcome = %q", outcome)
	}

	if got, err := first.Close(t.Context()); err != nil || got != contract.CloseClosed {
		t.Fatalf("Close = (%q, %v)", got, err)
	}
	if got, err := first.Close(t.Context()); err != nil || got != contract.CloseAlreadyClosed {
		t.Fatalf("second Close = (%q, %v)", got, err)
	}
	if _, err := toolByName(t, first, "mcp__search__query").Execute(t.Context(), session.NewToolCall("closed", "mcp__search__query", json.RawMessage(`{}`)), tool.Environment{}); !errors.Is(err, contract.ErrAttachmentClosed) {
		t.Fatalf("closed wrapper error = %v", err)
	}
	if _, err := toolByName(t, second, "mcp__search__query").Execute(t.Context(), session.NewToolCall("live", "mcp__search__query", json.RawMessage(`{}`)), tool.Environment{}); err != nil {
		t.Fatalf("sibling attachment after local close: %v", err)
	}

	if got, err := runtime.DeleteSession(t.Context(), "session-life"); err != nil || got != contract.DeleteDeleted {
		t.Fatalf("DeleteSession = (%q, %v)", got, err)
	}
	if got, err := runtime.DeleteSession(t.Context(), "session-life"); err != nil || got != contract.DeleteNotFound {
		t.Fatalf("second DeleteSession = (%q, %v)", got, err)
	}
	fresh, outcome := attach(t, runtime, "session-life")
	if outcome != contract.AttachCreated {
		t.Fatalf("recreated outcome = %q", outcome)
	}
	if _, err := second.AuthorizationStatus(t.Context(), session.ExternalAuthorization{ID: "none", Binding: "none"}); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("stale handle status error = %v", err)
	}
	if _, err := toolByName(t, second, "mcp__search__query").Execute(t.Context(), session.NewToolCall("stale", "mcp__search__query", json.RawMessage(`{}`)), tool.Environment{}); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("stale wrapper error = %v", err)
	}
	if _, err := fresh.AuthorizationStatus(t.Context(), session.ExternalAuthorization{ID: "none", Binding: "none"}); !errors.Is(err, contract.ErrAuthorizationNotFound) {
		t.Fatalf("anonymous authorization status error = %v", err)
	}
}
