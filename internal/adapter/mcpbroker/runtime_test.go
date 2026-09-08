package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestADR_0298_CompileAdmitsMultipleOAuthRoutes(t *testing.T) {
	config := protectedConfig("https://accounts.example/token")
	second := config.Routes[0]
	second.Name = "calendar"
	second.URL = "https://calendar.example/mcp"
	config.Routes = append(config.Routes, second)
	discovered := []ToolDefinition{
		{Backend: "github", Name: "mcp__github__create", Schema: json.RawMessage(`{"type":"object"}`)},
		{Backend: "calendar", Name: "mcp__calendar__list", Schema: json.RawMessage(`{"type":"object"}`)},
	}

	catalogue, err := Compile(config, discovered, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := catalogue.Specs(); len(got) != 2 || got[0].Name != "mcp__calendar__list" || got[1].Name != "mcp__github__create" {
		t.Fatalf("compiled specs = %#v", got)
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
	if _, ok := firstTool.(tool.AuthorizationRequester); ok {
		t.Fatal("anonymous wrapper unexpectedly advertises authorization")
	}
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

func TestAttachmentCloseWaitIsContextAwareWithoutEarlyStateCleanup(t *testing.T) {
	catalogue, err := Compile(anonymousConfig(), discoveredTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		close(entered)
		<-release
		return session.NewToolResult("call", "done"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, _ := attach(t, runtime, "context-close")
	callDone := make(chan error, 1)
	go func() {
		_, callErr := toolByName(t, attachment, "mcp__search__query").Execute(context.Background(), session.NewToolCall("call", "mcp__search__query", json.RawMessage(`{}`)), tool.Environment{})
		callDone <- callErr
	}()
	<-entered

	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if outcome, closeErr := attachment.Close(closeCtx); outcome != contract.CloseClosed || !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close = (%q, %v), want closed + deadline", outcome, closeErr)
	}
	if _, execErr := toolByName(t, attachment, "mcp__search__query").Execute(context.Background(), session.NewToolCall("late", "mcp__search__query", json.RawMessage(`{}`)), tool.Environment{}); !errors.Is(execErr, contract.ErrAttachmentClosed) {
		t.Fatalf("new operation after timed close = %v", execErr)
	}
	close(release)
	if callErr := <-callDone; callErr != nil {
		t.Fatalf("registered operation was cleared early: %v", callErr)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCloseIsBoundedAndOperationReleaseOwnsCleanup(t *testing.T) {
	catalogue, err := Compile(anonymousConfig(), discoveredTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		close(entered)
		<-release
		return session.NewToolResult("call", "done"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, _ := attach(t, runtime, "runtime-close")
	if err := attachment.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	wrapped := toolByName(t, attachment, "mcp__search__query")
	callDone := make(chan error, 1)
	go func() {
		_, callErr := wrapped.Execute(context.Background(), session.NewToolCall("call", wrapped.Spec().Name, json.RawMessage(`{}`)), tool.Environment{})
		callDone <- callErr
	}()
	<-entered

	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close() }()
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	case <-time.After(250 * time.Millisecond):
		close(release)
		<-callDone
		t.Fatal("Runtime.Close waited for stuck operation")
	}
	attachment.logical.mu.RLock()
	deleted, cleaned := attachment.logical.deleted, attachment.logical.cleaned
	attachment.logical.mu.RUnlock()
	if !deleted || cleaned {
		t.Fatalf("close state before release = deleted %v cleaned %v", deleted, cleaned)
	}

	close(release)
	if callErr := <-callDone; callErr != nil {
		t.Fatal(callErr)
	}
	attachment.logical.mu.RLock()
	cleaned = attachment.logical.cleaned
	active := attachment.logical.activeOps
	attachment.logical.mu.RUnlock()
	if !cleaned || active != 0 {
		t.Fatalf("release cleanup = cleaned %v active %d", cleaned, active)
	}
}

func TestCreatorAbortPreservesReattachedPeer(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	creator, outcome := attach(t, runtime, "shared-provisional")
	if outcome != contract.AttachCreated {
		t.Fatalf("creator outcome = %q", outcome)
	}
	peer, outcome := attach(t, runtime, "shared-provisional")
	if outcome != contract.AttachReattached {
		t.Fatalf("peer outcome = %q", outcome)
	}
	binding := peer.Binding()
	if err := creator.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	wrapped := toolByName(t, peer, "mcp__search__query")
	if _, err := wrapped.Execute(context.Background(), session.NewToolCall("peer", wrapped.Spec().Name, json.RawMessage(`{}`)), tool.Environment{}); err != nil {
		t.Fatalf("peer invalidated by creator abort: %v", err)
	}
	third, outcome := attach(t, runtime, "shared-provisional")
	if outcome != contract.AttachReattached || third.Binding() != binding {
		t.Fatalf("post-abort attach = %q binding %q, want reattached %q", outcome, third.Binding(), binding)
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

func TestRuntimeCloseAndDrainUsesOneProcessDeadline(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	finishes := make([]func(), 0, 3)
	for i := range 3 {
		attached, _, err := runtime.AttachSession(t.Context(), session.SessionID(fmt.Sprintf("drain-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		_, finish, err := attached.(*Attachment).beginOperation(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		finishes = append(finishes, finish)
	}
	defer func() {
		for _, finish := range finishes {
			finish()
		}
	}()

	const timeout = 60 * time.Millisecond
	started := time.Now()
	if err := runtime.closeAndDrain(timeout); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= 2*timeout {
		t.Fatalf("drain elapsed %v, want one %v process deadline", elapsed, timeout)
	}
}

func TestSingletonBrokerRemediation_Scenario2_BoundedAdmissionAcrossBrokerRegistries(t *testing.T) {
	catalogue, err := Compile(anonymousConfig(), discoveredTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, nil
	}, WithLimits(Limits{MaxLogicalSessions: 1, LogicalRetention: 20 * time.Millisecond, SweepInterval: 5 * time.Millisecond, MaxPendingStates: 1}))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	for _, id := range []session.SessionID{"", "bad\x00id", session.SessionID(strings.Repeat("x", maxLogicalSessionIDBytes+1))} {
		if _, _, err := runtime.AttachSession(context.Background(), id); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("AttachSession(%q) error = %v, want invalid ID", id, err)
		}
	}
	first, _, err := runtime.AttachSession(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.AttachSession(context.Background(), "two"); !errors.Is(err, contract.ErrCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	if _, err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := runtime.AttachSession(context.Background(), "two"); err == nil {
			break
		} else if !errors.Is(err, contract.ErrCapacity) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	second, _, err := runtime.AttachSession(context.Background(), "two")
	if err != nil {
		t.Fatalf("retained logical session was not reclaimed: %v", err)
	}
	_, _ = second.Close(context.Background())
}

func TestSingletonBrokerRemediation_Scenario2_RetentionAndOwnership(t *testing.T) {
	catalogue, err := Compile(anonymousConfig(), discoveredTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, nil
	}, WithLimits(Limits{MaxLogicalSessions: 1, LogicalRetention: 15 * time.Millisecond, SweepInterval: time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	active, _, err := runtime.AttachSession(t.Context(), "active")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * runtime.limits.LogicalRetention)
	if _, _, err := runtime.AttachSession(t.Context(), "new"); !errors.Is(err, contract.ErrCapacity) {
		t.Fatalf("active logical session was evicted by retention: %v", err)
	}
	if _, err := active.Close(t.Context()); err != nil {
		t.Fatalf("close active attachment: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		candidate, _, attachErr := runtime.AttachSession(t.Context(), "new")
		if attachErr == nil {
			_, _ = candidate.Close(t.Context())
			break
		}
		if !errors.Is(attachErr, contract.ErrCapacity) || time.Now().After(deadline) {
			t.Fatalf("unattached logical session was not reclaimed: %v", attachErr)
		}
		time.Sleep(time.Millisecond)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if _, _, err := runtime.AttachSession(t.Context(), "after-close"); err == nil {
		t.Fatal("closed runtime admitted a new attachment")
	}
}
