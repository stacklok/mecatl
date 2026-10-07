package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestSuccessfulReadsBoundPendingInstructionTargets(t *testing.T) {
	ws := &countedTargetWorkspace{Workspace: memfs.NewWorkspace("/ws")}
	if err := ws.Write(t.Context(), "AGENTS.md", []byte("root")); err != nil {
		t.Fatal(err)
	}
	var calls []session.ToolCall
	for i := range 70 {
		path := fmt.Sprintf("dir%03d/file", i)
		if err := ws.Write(t.Context(), path, []byte("ok")); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, session.NewToolCall(session.ToolCallID(fmt.Sprintf("r%d", i)), "Read", []byte(fmt.Sprintf(`{"path":%q}`, path))))
	}
	if err := ws.Write(t.Context(), "dir070/file", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	var requests []port.LLMRequest
	var instructionReads []int
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		requests = append(requests, req)
		instructionReads = append(instructionReads, ws.reads)
	})},
		mockllm.ToolCallTurn(calls...), mockllm.ToolCallTurn(session.NewToolCall("late", "Read", []byte(`{"path":"dir070/file"}`))), mockllm.TextTurn("done"))
	catalog := tool.NewCatalog()
	catalog.MustRegister(fstools.ReadTool{})
	engine := NewEngine(Deps{LLM: llm, Catalog: catalog, Policy: allowAllInt(), Instructions: prompt.RootAssembler{Source: ws, SourceID: "ws", SourcePrefix: "."}, ProjectInstructionMaxBytes: 256})
	sess := session.New("bounded", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	run := engine.Run(t.Context(), sess, testEnvironment(ws, nil), RunRequest{Text: "read"})
	results := 0
	var eventTypes []session.EventType
	var final session.Event
	for ev := range run.Events() {
		final = ev
		eventTypes = append(eventTypes, ev.Type)
		if ev.Type == session.EvToolResult {
			results++
			if ev.ToolResult.IsError {
				t.Fatalf("ordinary read denied: %+v", ev.ToolResult)
			}
		}
	}
	if results != 71 || len(requests) != 3 {
		t.Fatalf("results=%d requests=%d events=%v final=%+v", results, len(requests), eventTypes, final.Result)
	}
	state := sess.InstructionSnapshot()
	if !state.DiscoveryExhausted {
		t.Fatalf("discovery did not exhaust: %+v", state)
	}
	metadata := 0
	for _, dir := range state.Directories {
		metadata += len(dir) + 1
	}
	for _, scope := range state.Scopes {
		metadata += len(scope.SourceID) + len(scope.Directory) + len(scope.File) + 1
	}
	pending := 0
	for _, dir := range run.instructionScopes.dirs {
		pending += len(dir) + 1
	}
	if metadata+pending > 256 || len(run.instructionScopes.dirs) != 0 {
		t.Fatalf("unbounded discovery bookkeeping: metadata=%d pending=%d dirs=%d", metadata, pending, len(run.instructionScopes.dirs))
	}
	if !strings.Contains(fmt.Sprint(requests[1].Messages), "metadata budget exhausted") || instructionReads[1] != instructionReads[2] {
		t.Fatalf("missing omission or extra instruction reads: reads=%v", instructionReads)
	}
}

func TestConcurrentTargetReservationsFitSnapshotBudget(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(t.Context(), "AGENTS.md", []byte("root")); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(Deps{Catalog: tool.NewCatalog(), Instructions: prompt.RootAssembler{Source: ws, SourceID: "ws", SourcePrefix: "."}, ProjectInstructionMaxBytes: 256})
	sess := session.New("concurrent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	run := &Run{diag: engine.deps.Diagnostics, events: make(chan session.Event, 256)}
	engine.buildRequest(t.Context(), run, sess, testEnvironment(ws, nil))
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run.noteInstructionTargets(ws.Root(), session.NewToolCall("read", "Read", []byte(fmt.Sprintf(`{"path":"dir%03d/file"}`, i))))
		}()
	}
	wg.Wait()
	metadata := 0
	for _, dir := range run.instructionScopes.snapshot.Directories {
		metadata += len(dir) + 1
	}
	for _, scope := range run.instructionScopes.snapshot.Scopes {
		metadata += len(scope.SourceID) + len(scope.Directory) + len(scope.File) + 1
	}
	if metadata+run.instructionScopes.reserved > 256 || !run.instructionScopes.exhausted {
		t.Fatalf("concurrent reservations exceeded cap: metadata=%d reserved=%d exhausted=%v", metadata, run.instructionScopes.reserved, run.instructionScopes.exhausted)
	}
	engine.buildRequest(t.Context(), run, sess, testEnvironment(ws, nil))
	if len(run.instructionScopes.dirs) != 0 || !sess.InstructionSnapshot().DiscoveryExhausted {
		t.Fatalf("pending targets survived exhaustion: %+v", run.instructionScopes)
	}
}

func TestScopedTargetsSubmitOnlyPendingDirs(t *testing.T) {
	ws := &countedTargetWorkspace{Workspace: memfs.NewWorkspace("/ws")}
	for _, name := range []string{"AGENTS.md", "shared/AGENTS.md", "shared/one/AGENTS.md"} {
		if err := ws.Write(t.Context(), name, []byte(name)); err != nil {
			t.Fatal(err)
		}
	}
	var batches [][]string
	assembler := recordingTargetAssembler{root: prompt.RootAssembler{Source: ws, SourceID: "ws", SourcePrefix: "."}, batches: &batches}
	engine := NewEngine(Deps{Catalog: tool.NewCatalog(), Instructions: assembler, ProjectInstructionMaxBytes: 1024})
	sess := session.New("pending", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	run := &Run{diag: engine.deps.Diagnostics}
	env := testEnvironment(ws, nil)
	engine.buildRequest(t.Context(), run, sess, env)
	run.noteInstructionTargets(ws.Root(), session.NewToolCall("one", "Read", []byte(`{"path":"shared/one/file"}`)))
	engine.buildRequest(t.Context(), run, sess, env)
	firstReads := ws.reads
	run.noteInstructionTargets(ws.Root(), session.NewToolCall("repeat", "Read", []byte(`{"path":"shared/one/again"}`)))
	run.noteInstructionTargets(ws.Root(), session.NewToolCall("two", "Read", []byte(`{"path":"shared/two/file"}`)))
	engine.buildRequest(t.Context(), run, sess, env)
	if len(batches) != 3 || fmt.Sprint(batches[0]) != "[.]" || fmt.Sprint(batches[1]) != "[. shared/one]" || fmt.Sprint(batches[2]) != "[. shared/two]" || ws.reads != firstReads+2 || len(run.instructionScopes.dirs) != 0 {
		t.Fatalf("batches=%v reads=%d prior=%d pending=%v", batches, ws.reads, firstReads, run.instructionScopes.dirs)
	}
}

type recordingTargetAssembler struct {
	root    prompt.RootAssembler
	batches *[][]string
}

func (recordingTargetAssembler) TargetScoped() bool { return true }
func (a recordingTargetAssembler) Assemble(ctx context.Context, dirs []string, state *session.InstructionSnapshot, limit int) ([]session.Message, []prompt.InstructionManifest, error) {
	*a.batches = append(*a.batches, append([]string(nil), dirs...))
	return a.root.Assemble(ctx, dirs, state, limit)
}

type countedTargetWorkspace struct {
	*memfs.Workspace
	reads int
}

func (ws *countedTargetWorkspace) Read(ctx context.Context, name string) ([]byte, error) {
	if strings.HasSuffix(name, "AGENTS.md") || strings.HasSuffix(name, "CLAUDE.md") {
		ws.reads++
	}
	return ws.Workspace.Read(ctx, name)
}
