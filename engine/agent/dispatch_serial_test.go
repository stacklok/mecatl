package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type serialFakeTool struct{ *fakeTool }

func (*serialFakeTool) DispatchSerialTool() {}

func TestDispatchSerialReadOnlyCallsDoNotOverlap(t *testing.T) {
	var tracker overlapTracker
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	serial := &serialFakeTool{&fakeTool{name: "SerialRead", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			tracker.enter()
			defer tracker.leave()
			entered <- struct{}{}
			<-release
			return session.NewToolResult(in.ID, "read"), nil
		}}}

	e := newEngine(agent.Deps{LLM: mockllm.New(
		mockllm.ToolCallTurn(toolCall("one", "SerialRead", `{}`), toolCall("two", "SerialRead", `{}`)),
		mockllm.TextTurn("done"),
	), Catalog: catalogWith(t, serial)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	<-entered
	select {
	case <-entered:
		close(release)
		drain(r)
		t.Fatal("serial read-only calls overlapped")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	drain(r)

	if got := tracker.max(); got != 1 {
		t.Fatalf("serial read-only calls overlapped: max concurrency = %d, want 1", got)
	}
}

func TestDispatchSerialFlushesReadBatchesInOrder(t *testing.T) {
	firstStarted := make(chan struct{}, 2)
	firstRelease := make(chan struct{})
	var mu sync.Mutex
	firstComplete := 0
	serialComplete := false
	violation := ""

	first := &fakeTool{name: "First", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			firstStarted <- struct{}{}
			<-firstRelease
			mu.Lock()
			firstComplete++
			mu.Unlock()
			return session.NewToolResult(in.ID, "first"), nil
		}}
	serial := &serialFakeTool{&fakeTool{name: "Serial", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			mu.Lock()
			if firstComplete != 2 {
				violation = "serial read started before the preceding batch completed"
			}
			serialComplete = true
			mu.Unlock()
			return session.NewToolResult(in.ID, "serial"), nil
		}}}
	following := &fakeTool{name: "Following", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			mu.Lock()
			if !serialComplete && violation == "" {
				violation = "following batch started before the serial read completed"
			}
			mu.Unlock()
			return session.NewToolResult(in.ID, "following"), nil
		}}

	e := newEngine(agent.Deps{LLM: mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("first-1", "First", `{}`),
			toolCall("first-2", "First", `{}`),
			toolCall("serial", "Serial", `{}`),
			toolCall("following-1", "Following", `{}`),
			toolCall("following-2", "Following", `{}`),
		),
		mockllm.TextTurn("done"),
	), Catalog: catalogWith(t, first, serial, following)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	<-firstStarted
	<-firstStarted
	close(firstRelease)
	drain(r)

	mu.Lock()
	defer mu.Unlock()
	if violation != "" {
		t.Fatal(violation)
	}
}

func TestReadOnlyToolsRemainConcurrentWithoutDispatchSerial(t *testing.T) {
	var tracker overlapTracker
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			tracker.enter()
			defer tracker.leave()
			started <- struct{}{}
			<-release
			return session.NewToolResult(in.ID, "read"), nil
		}}

	e := newEngine(agent.Deps{LLM: mockllm.New(
		mockllm.ToolCallTurn(toolCall("one", "Read", `{}`), toolCall("two", "Read", `{}`)),
		mockllm.TextTurn("done"),
	), Catalog: catalogWith(t, read)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	<-started
	<-started
	close(release)
	drain(r)

	if got := tracker.max(); got < 2 {
		t.Fatalf("ordinary read-only tools did not run concurrently: max concurrency = %d, want at least 2", got)
	}
}
