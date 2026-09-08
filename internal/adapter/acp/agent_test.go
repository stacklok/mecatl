package acp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/acp"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
	toolsadapter "github.com/stacklok/mecatl/internal/adapter/tools"
)

// scriptTool is a minimal mutating Tool: it returns a fixed body and records
// that it ran, so the e2e test can assert the approved tool actually executed.
type scriptTool struct {
	name     string
	readOnly bool
	content  string
}

func (s *scriptTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: s.name, Description: s.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (s *scriptTool) ReadOnly() bool { return s.readOnly }
func (s *scriptTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(in.ID, s.content), nil
}

type acpPlacementProvider struct {
	root          string
	ref           session.EnvironmentRef
	bindCalls     *atomic.Int32
	reattachCalls *atomic.Int32
}

func (p acpPlacementProvider) binding() server.PlacementBinding {
	ws, err := osfs.NewWorkspace(p.root)
	if err != nil {
		panic(err)
	}
	env := tool.MustEnvironment(p.ref, ws, memledger.New(), nil)
	return server.PlacementBinding{Environment: env, Ref: p.ref}
}

func (p acpPlacementProvider) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if p.bindCalls != nil {
		p.bindCalls.Add(1)
	}
	if req.Scope != "acp-test" || req.Selector.Kind != server.PlacementSelectorDefault {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	return p.binding(), nil
}

func (p acpPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if p.reattachCalls != nil {
		p.reattachCalls.Add(1)
	}
	if req.Scope != "acp-test" || req.Ref != p.ref {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	return p.binding(), nil
}

func testCWD(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return cwd
}

// resolvedTempDir returns a fresh temp dir with symlinks evaluated, matching
// what osfs.NewWorkspace stores as its Root() (macOS's /tmp is a symlink to
// /private/tmp, so a raw t.TempDir() would fail assertACPPlacementCWD's exact
// string comparison against the workspace's already-resolved root).
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return dir
}

type authorizationScriptTool struct {
	scriptTool
	requests int
}

func (s *authorizationScriptTool) RequestAuthorization(context.Context, session.ToolCall) (session.ExternalAuthorization, bool, error) {
	s.requests++
	return session.ExternalAuthorization{ID: "must-not-be-exposed", Binding: "private", ExpiresAt: time.Now().Add(time.Hour)}, true, nil
}
func (*authorizationScriptTool) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}

// newService wires a real *agent.Engine (mockllm + memfs + permpolicy) into a
// server.Service, mirroring the gRPC adapter's test harness. Optional configFns
// mutate the server.Config before construction (e.g. to inject a CommandLister
// or a shared store).
func newService(t *testing.T, llm *mockllm.Provider, rules []governance.Rule, tools ...tool.Tool) *server.Service {
	t.Helper()
	return newServiceCfg(t, llm, rules, nil, tools...)
}

// newServiceCfg is newService with an extra hook to customize the server.Config.
func newServiceCfg(t *testing.T, llm *mockllm.Provider, rules []governance.Rule, configFn func(*server.Config), tools ...tool.Tool) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	for _, tl := range tools {
		cat.MustRegister(tl)
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(rules, nil),
		Model:   "test-model",
	})
	root := testCWD(t)
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "acp-test-placement", Revision: "v1"}
	cfg := server.Config{
		Engine: engine,
		Store:  memstore.New(),

		SharedEngineRoot:  root,
		PlacementProvider: acpPlacementProvider{root: root, ref: ref},
		PlacementScope:    "acp-test",
		DefaultLimits:     session.Limits{MaxTurns: 10, MaxToolCalls: 20},
		Now:               func() time.Time { return time.Unix(0, 0) },
		// The ACP gate reads ProviderCapabilities() = DefaultCapabilities (composition-
		// computed), not the engine. In these tests there is no catalog/selector, so the
		// intersection is the bare adapter caps — source them from the wired provider.
		DefaultCapabilities: llm.Capabilities(),
		SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: engine, Capabilities: llm.Capabilities(), Close: func() error { return nil }}, nil
		},
	}
	if configFn != nil {
		configFn(&cfg)
	}
	svc, err := server.NewService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// fakeLister is a CommandLister returning a fixed command set, for the
// available_commands_update test.
type fakeLister struct {
	cmds []server.Command
}

func (f fakeLister) List(_ context.Context, _ string) ([]server.Command, error) {
	return f.cmds, nil
}

// editor is the scripted ACP CLIENT side of the test: it owns the agent's stdin
// (it writes requests/responses there) and reads the agent's stdout (the agent's
// requests/notifications/responses). It speaks newline-delimited JSON (ndjson).
type editor struct {
	t        *testing.T
	toAgent  *io.PipeWriter // editor -> agent stdin
	fromAgnt *bufio.Reader  // agent stdout -> editor

	mu    sync.Mutex
	next  int64
	pend  map[int64]chan rpcMsg
	notes chan rpcMsg // session/update notifications
	reqs  chan rpcMsg // agent-initiated requests (request_permission)

	// fsBuffers is the editor's in-memory buffer store for fs/* delegation tests.
	// When non-nil, the readLoop answers fs/read_text_file / fs/write_text_file
	// requests from it (instead of routing them to reqs) and records the call
	// counts, so a test can assert delegation happened and disk was untouched. A
	// fs/read for an unseeded path returns fsDefault (a stable body) so the Edit
	// read-ledger has consistent content across its RecordRead + re-read.
	fsBuffers map[string]string
	fsDefault string
	fsReads   int
	fsWrites  int
}

type rpcMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func (e *editor) writeFrame(m any) {
	body, _ := json.Marshal(m)
	body = append(body, '\n')
	if _, err := e.toAgent.Write(body); err != nil {
		e.t.Errorf("editor write: %v", err)
	}
}

// call issues an editor->agent request and returns its result, blocking.
func (e *editor) call(method string, params any) json.RawMessage {
	e.mu.Lock()
	e.next++
	id := e.next
	ch := make(chan rpcMsg, 1)
	e.pend[id] = ch
	e.mu.Unlock()
	e.writeFrame(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case m := <-ch:
		if len(m.Error) > 0 {
			e.t.Fatalf("%s error: %s", method, m.Error)
		}
		return m.Result
	case <-time.After(5 * time.Second):
		e.t.Fatalf("%s timed out", method)
		return nil
	}
}

func (e *editor) respond(id json.RawMessage, result any) {
	e.writeFrame(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// handleFS answers an agent fs/read_text_file or fs/write_text_file request from
// the editor's in-memory buffers, recording the call. fs/read returns the stored
// buffer (or fsDefault if unseeded); fs/write stores the content.
func (e *editor) handleFS(m rpcMsg) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(m.Params, &req)
	e.mu.Lock()
	switch m.Method {
	case "fs/read_text_file":
		e.fsReads++
		content, ok := e.fsBuffers[req.Path]
		if !ok {
			content = e.fsDefault
		}
		e.mu.Unlock()
		e.respond(m.ID, map[string]any{"content": content})
	case "fs/write_text_file":
		e.fsWrites++
		e.fsBuffers[req.Path] = req.Content
		e.mu.Unlock()
		e.respond(m.ID, map[string]any{})
	default:
		e.mu.Unlock()
	}
}

func (e *editor) fsCounts() (reads, writes int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fsReads, e.fsWrites
}

// readLoop reads agent frames, routing responses to pending calls, notifications
// to the notes channel, and agent requests to the reqs channel.
func (e *editor) readLoop() {
	for {
		raw, err := e.fromAgnt.ReadBytes('\n')
		line := strings.TrimRight(string(raw), "\r\n")
		if line == "" {
			if err != nil {
				return // EOF / closed pipe on a blank trailing line
			}
			continue // bare blank line between messages
		}
		var m rpcMsg
		if uerr := json.Unmarshal([]byte(line), &m); uerr != nil {
			return
		}
		switch {
		case m.Method == "" && len(m.ID) > 0: // response
			id, _ := strconv.ParseInt(strings.TrimSpace(string(m.ID)), 10, 64)
			e.mu.Lock()
			ch := e.pend[id]
			delete(e.pend, id)
			e.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.Method != "" && len(m.ID) > 0: // agent request
			if e.fsBuffers != nil && (m.Method == "fs/read_text_file" || m.Method == "fs/write_text_file") {
				e.handleFS(m)
				continue
			}
			e.reqs <- m
		case m.Method != "": // notification
			e.notes <- m
		}
	}
}

// TestEndToEndPromptWithPermission drives the full Phase 1 loop over an in-memory
// stdio pipe: initialize -> session/new -> session/prompt; the agent streams a
// message chunk, a tool_call, then issues a request_permission that the editor
// answers allow_once; the tool runs; a tool_call_update follows; and the prompt
// returns stopReason end_turn.
func TestEndToEndPromptWithPermission(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	llm := mockllm.New(
		// Turn 1: a text delta + a Write tool call (Write asks for permission).
		mockllm.ChunksTurn(
			port.Chunk{Kind: port.ChunkText, Text: "Working on it"},
			port.Chunk{Kind: port.ChunkToolCall, ToolCall: callP("c1", "Write", `{"path":"a.txt"}`)},
			port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		),
		// Turn 2: after the tool result, a final message and end the turn.
		mockllm.TextTurn("All done"),
	)
	// nil rules => default decision is Ask, so Write triggers a permission.ask.
	svc := newService(t, llm, nil, write)
	cwd := testCWD(t) // session/new now requires an existing absolute dir

	// Wire the agent over a pair of pipes: editorIn -> agent stdin,
	// agent stdout -> editorOut.
	agentStdinR, editorToAgentW := io.Pipe()
	agentStdoutR, agentStdoutW := io.Pipe()

	a := acp.NewAgent(svc)
	conn := acp.NewConn(agentStdinR, agentStdoutW, a.Handle)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serveDone := make(chan error, 1)
	go func() { serveDone <- a.Serve(ctx, conn) }()

	e := &editor{
		t:        t,
		toAgent:  editorToAgentW,
		fromAgnt: bufio.NewReader(agentStdoutR),
		pend:     map[int64]chan rpcMsg{},
		notes:    make(chan rpcMsg, 64),
		reqs:     make(chan rpcMsg, 8),
	}
	go e.readLoop()

	// 1) initialize
	initRes := e.call("initialize", map[string]any{"protocolVersion": 1})
	var init struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession        bool `json:"loadSession"`
			PromptCapabilities struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(initRes, &init); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if init.ProtocolVersion != 1 || init.AgentCapabilities.LoadSession || init.AgentCapabilities.PromptCapabilities.Image {
		t.Fatalf("unexpected initialize result: %+v", init)
	}

	// 2) session/new
	newRes := e.call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newRes, &ns); err != nil || ns.SessionID == "" {
		t.Fatalf("session/new result: %s err=%v", newRes, err)
	}

	// 3) session/prompt — issue it concurrently (it blocks until the turn ends),
	// then service the permission request and drain notifications.
	promptDone := make(chan json.RawMessage, 1)
	go func() {
		promptDone <- e.call("session/prompt", map[string]any{
			"sessionId": ns.SessionID,
			"prompt":    []any{map[string]any{"type": "text", "text": "please write"}},
		})
	}()

	// 4) Answer the request_permission with allow_once.
	select {
	case req := <-e.reqs:
		if req.Method != "session/request_permission" {
			t.Fatalf("expected request_permission, got %q", req.Method)
		}
		var rp struct {
			ToolCall struct {
				ToolCallID string `json:"toolCallId"`
				Title      string `json:"title"`
			} `json:"toolCall"`
			Options []struct {
				OptionID string `json:"optionId"`
			} `json:"options"`
		}
		if err := json.Unmarshal(req.Params, &rp); err != nil {
			t.Fatalf("request_permission params: %v", err)
		}
		if rp.ToolCall.Title != "Write" || len(rp.Options) != 4 {
			t.Fatalf("unexpected request_permission: %+v", rp)
		}
		e.respond(req.ID, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow_once"}})
	case <-time.After(5 * time.Second):
		t.Fatal("no request_permission arrived")
	}

	// 5) The prompt returns end_turn.
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	select {
	case res := <-promptDone:
		if err := json.Unmarshal(res, &pr); err != nil {
			t.Fatalf("prompt result: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session/prompt did not return")
	}
	if pr.StopReason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", pr.StopReason)
	}

	// No run-registry leak: once the prompt returns the run must be deregistered
	// (FinishRun), so LookupRun reports no in-flight run for the session.
	if _, ok := svc.LookupRun(session.SessionID(ns.SessionID)); ok {
		t.Fatalf("run leaked in registry after prompt completed")
	}

	// 6) Assert the streamed session/update sequence: a message chunk, a tool_call
	// (Write), and a tool_call_update completed.
	updates := drainUpdates(e.notes)
	assertHasUpdate(t, updates, "agent_message_chunk", "")
	assertHasUpdate(t, updates, "tool_call", "Write")
	assertHasToolStatus(t, updates, "tool_call_update", "completed")

	// Clean shutdown: closing the editor's writer ends the agent read loop.
	_ = editorToAgentW.Close()
	select {
	case <-serveDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after EOF")
	}
}

// drainUpdates collects all session/update notifications currently buffered.
func drainUpdates(notes chan rpcMsg) []map[string]any {
	var out []map[string]any
	for {
		select {
		case n := <-notes:
			if n.Method != "session/update" {
				continue
			}
			var p struct {
				Update map[string]any `json:"update"`
			}
			if err := json.Unmarshal(n.Params, &p); err == nil {
				out = append(out, p.Update)
			}
		case <-time.After(200 * time.Millisecond):
			return out
		}
	}
}

func assertHasUpdate(t *testing.T, updates []map[string]any, kind, title string) {
	t.Helper()
	for _, u := range updates {
		if u["sessionUpdate"] == kind {
			if title == "" || u["title"] == title {
				return
			}
		}
	}
	t.Fatalf("missing %s update (title %q) in %v", kind, title, updates)
}

func assertHasToolStatus(t *testing.T, updates []map[string]any, kind, status string) {
	t.Helper()
	for _, u := range updates {
		if u["sessionUpdate"] == kind && u["status"] == status {
			return
		}
	}
	t.Fatalf("missing %s update with status %q in %v", kind, status, updates)
}

func TestACPProtectedAuthorizationFailsWithoutParkingAndSessionContinues(t *testing.T) {
	protected := &authorizationScriptTool{scriptTool: scriptTool{name: "protected", content: "must not execute"}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "protected", `{}`)),
		mockllm.TextTurn("authorization unavailable"),
		mockllm.TextTurn("second prompt works"),
	)
	svc := newService(t, llm, allowRules(), protected)
	agentStdinR, editorToAgentW := io.Pipe()
	agentStdoutR, agentStdoutW := io.Pipe()
	a := acp.NewAgent(svc)
	conn := acp.NewConn(agentStdinR, agentStdoutW, a.Handle)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = a.Serve(ctx, conn) }()
	e := &editor{t: t, toAgent: editorToAgentW, fromAgnt: bufio.NewReader(agentStdoutR), pend: map[int64]chan rpcMsg{}, notes: make(chan rpcMsg, 64), reqs: make(chan rpcMsg, 8)}
	go e.readLoop()

	res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &ns); err != nil {
		t.Fatal(err)
	}
	prompt := func(text string) json.RawMessage {
		return e.call("session/prompt", map[string]any{"sessionId": ns.SessionID, "prompt": []any{map[string]any{"type": "text", "text": text}}})
	}
	first := prompt("use protected")
	updates := drainUpdates(e.notes)
	updatesJSON, _ := json.Marshal(updates)
	if strings.Contains(string(first), "must-not-be-exposed") || strings.Contains(string(updatesJSON), "must-not-be-exposed") || strings.Contains(string(updatesJSON), "https://") || protected.requests != 0 {
		t.Fatalf("ACP obtained authorization identity/URL or invoked requester: response=%s updates=%s requests=%d", first, updatesJSON, protected.requests)
	}
	sess, err := svc.GetSession(t.Context(), session.SessionID(ns.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if sess.State == session.StateAuthorizing {
		t.Fatal("ACP session parked in authorizing state")
	}
	second := prompt("continue")
	if strings.Contains(string(second), "error") {
		t.Fatalf("second prompt unusable: %s", second)
	}
}

// TestEndToEndEditDiffBlock drives a prompt whose model emits an Edit tool call
// (auto-allowed), and asserts the streamed tool_call carries an ACP diff content
// block synthesized from the Edit args (oldText/newText/path) so the editor can
// render a native inline diff.
func TestEndToEndEditDiffBlock(t *testing.T) {
	edit := &scriptTool{name: "Edit", readOnly: false, content: "edited"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Edit", `{"path":"main.go","old_string":"foo","new_string":"bar"}`)),
		mockllm.TextTurn("done"),
	)
	svc := newService(t, llm, allowRules(), edit) // allow => no permission ask

	agentStdinR, editorToAgentW := io.Pipe()
	agentStdoutR, agentStdoutW := io.Pipe()
	a := acp.NewAgent(svc)
	conn := acp.NewConn(agentStdinR, agentStdoutW, a.Handle)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = a.Serve(ctx, conn) }()

	e := &editor{t: t, toAgent: editorToAgentW, fromAgnt: bufio.NewReader(agentStdoutR),
		pend: map[int64]chan rpcMsg{}, notes: make(chan rpcMsg, 64), reqs: make(chan rpcMsg, 8)}
	go e.readLoop()

	res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)

	out := e.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
		"prompt": []any{map[string]any{"type": "text", "text": "edit it"}}})
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out, &pr); err != nil || pr.StopReason != "end_turn" {
		t.Fatalf("prompt stopReason %q err %v", pr.StopReason, err)
	}

	updates := drainUpdates(e.notes)
	// Find the Edit tool_call and assert its content carries a diff block.
	var found bool
	for _, u := range updates {
		if u["sessionUpdate"] != "tool_call" || u["title"] != "Edit" {
			continue
		}
		content, ok := u["content"].([]any)
		if !ok || len(content) == 0 {
			t.Fatalf("Edit tool_call has no content array: %v", u)
		}
		block := content[0].(map[string]any)
		if block["type"] != "diff" {
			t.Fatalf("Edit content block type = %v, want diff", block["type"])
		}
		if block["path"] != "main.go" || block["oldText"] != "foo" || block["newText"] != "bar" {
			t.Fatalf("diff block = %v", block)
		}
		found = true
	}
	if !found {
		t.Fatalf("no Edit tool_call with a diff block in %v", updates)
	}

	_ = editorToAgentW.Close()
}

// TestEndToEndFSDelegation drives the capability-gated fs/* delegation end to
// end. With clientCapabilities.fs.{readTextFile,writeTextFile}=true at
// initialize, a Read-then-Edit turn must flow through the editor's buffers
// (fs/read_text_file + fs/write_text_file) and NOT touch disk. With the caps
// absent, the same turn must issue NO fs/* calls (the osfs fallback).
func TestEndToEndFSDelegation(t *testing.T) {
	const seed = "package x\nvar A = 1\n"

	runScenario := func(t *testing.T, caps bool) (reads, writes int, diskTouched bool, root string) {
		t.Helper()
		// Real Read + Edit tools so the engine exercises the Workspace version
		// contract (ReadVersion/RecordRead, then RecordedVersion/ReplaceFile).
		llm := mockllm.New(
			mockllm.ToolCallTurn(call("c1", "Read", `{"path":"main.go"}`)),
			mockllm.ToolCallTurn(call("c2", "Edit", `{"path":"main.go","old_string":"var A = 1","new_string":"var A = 2"}`)),
			mockllm.TextTurn("done"),
		)
		root = resolvedTempDir(t)
		svc := newServiceCfg(t, llm, allowRules(), func(cfg *server.Config) {
			cfg.SharedEngineRoot = root
			cfg.PlacementProvider = acpPlacementProvider{root: root, ref: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "fs-test-placement", Revision: "v1"}}
		}, toolsadapter.ReadTool{}, toolsadapter.EditTool{})
		// Seed the file on DISK so that, in the caps-absent (osfs) scenario, Read+Edit
		// have a real file to operate on. In the caps-present scenario the editor
		// buffer (fsDefault) supplies the content and disk must stay as-is.
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(seed), 0o644); err != nil {
			t.Fatalf("seed disk: %v", err)
		}

		agentStdinR, editorToAgentW := io.Pipe()
		agentStdoutR, agentStdoutW := io.Pipe()
		a := acp.NewAgent(svc)
		conn := acp.NewConn(agentStdinR, agentStdoutW, a.Handle)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		go func() { _ = a.Serve(ctx, conn) }()

		e := &editor{t: t, toAgent: editorToAgentW, fromAgnt: bufio.NewReader(agentStdoutR),
			pend: map[int64]chan rpcMsg{}, notes: make(chan rpcMsg, 64), reqs: make(chan rpcMsg, 8),
			fsBuffers: map[string]string{}, fsDefault: seed}
		go e.readLoop()

		initParams := map[string]any{"protocolVersion": 1}
		if caps {
			initParams["clientCapabilities"] = map[string]any{
				"fs": map[string]any{"readTextFile": true, "writeTextFile": true},
			}
		}
		e.call("initialize", initParams)

		res := e.call("session/new", map[string]any{"cwd": root, "mcpServers": []any{}})
		var ns struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(res, &ns); err != nil || ns.SessionID == "" {
			t.Fatalf("session/new: %s err=%v", res, err)
		}

		out := e.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
			"prompt": []any{map[string]any{"type": "text", "text": "fix it"}}})
		var pr struct {
			StopReason string `json:"stopReason"`
		}
		if err := json.Unmarshal(out, &pr); err != nil || pr.StopReason != "end_turn" {
			t.Fatalf("prompt stopReason %q err %v", pr.StopReason, err)
		}

		reads, writes = e.fsCounts()
		// Did disk change? In the caps-present case the edit went to the buffer, so
		// disk must still equal seed.
		onDisk, _ := os.ReadFile(filepath.Join(root, "main.go"))
		diskTouched = string(onDisk) != seed
		_ = editorToAgentW.Close()
		return reads, writes, diskTouched, root
	}

	t.Run("caps present -> delegate, disk untouched", func(t *testing.T) {
		reads, writes, diskTouched, root := runScenario(t, true)
		if reads == 0 {
			t.Errorf("expected fs/read_text_file calls, got 0")
		}
		if writes == 0 {
			t.Errorf("expected fs/write_text_file calls, got 0")
		}
		if diskTouched {
			t.Errorf("disk file %s was modified; the edit must flow through the editor buffer", filepath.Join(root, "main.go"))
		}
	})

	t.Run("caps absent -> osfs fallback, no fs/* calls", func(t *testing.T) {
		reads, writes, _, _ := runScenario(t, false)
		if reads != 0 || writes != 0 {
			t.Errorf("expected no fs/* calls without caps, got reads=%d writes=%d", reads, writes)
		}
	})
}

// fakeSessionEngine records the specs it received and returns a stub engine plus
// a no-op close, so the accept-path tests can assert the factory was called with
// the right URL+headers WITHOUT connecting a real MCP server (offline).
type fakeSessionEngine struct {
	mu     sync.Mutex
	called int
	specs  []mcp.ServerConfig
	engine *agent.Engine
}

func (f *fakeSessionEngine) factory(_ context.Context, _ server.ProviderSelector, specs []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
	f.mu.Lock()
	f.called++
	f.specs = specs
	f.mu.Unlock()
	return server.SessionEngineResult{Engine: f.engine, Close: func() error { return nil }}, nil
}

// stubEngine builds a minimal mockllm-backed engine the fake factory hands back as
// the per-session engine (it never has to actually run in the accept tests).
func stubEngine(t *testing.T) *agent.Engine {
	t.Helper()
	return agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("done")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
}

// TestSessionNewAcceptsHTTPMCP asserts a client-provided streaming-HTTP MCP server
// remains an ACP-owned declaration: it is passed to the per-session factory with
// its header, but initialize and a complete prompt emit no auth, elicitation, or
// browser round-trip and never contact an OAuth endpoint.
func TestSessionNewAcceptsHTTPMCP(t *testing.T) {
	var oauthCalls atomic.Int32
	oauthFixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		oauthCalls.Add(1)
		http.Error(w, "unexpected OAuth fixture call", http.StatusInternalServerError)
	}))
	defer oauthFixture.Close()

	fake := &fakeSessionEngine{engine: stubEngine(t)}
	svc := newServiceCfg(t, mockllm.New(), nil, func(c *server.Config) { c.SessionEngine = fake.factory })
	e, cleanup := startAgent(t, svc)
	defer cleanup()

	initResult := e.call("initialize", map[string]any{"protocolVersion": 1})
	var initialized struct {
		AuthMethods []any `json:"authMethods"`
	}
	if err := json.Unmarshal(initResult, &initialized); err != nil || len(initialized.AuthMethods) != 0 {
		t.Fatalf("initialize auth methods = %s, err=%v", initResult, err)
	}

	const acpHeaderCanary = "acp-header-secret-canary"
	newResult := e.call("session/new", map[string]any{
		"cwd": testCWD(t),
		"mcpServers": []any{map[string]any{
			"type": "http", "name": "docs", "url": oauthFixture.URL + "/mcp",
			"headers": []any{map[string]any{"name": "Authorization", "value": "Bearer " + acpHeaderCanary}},
		}},
	})
	if strings.Contains(string(newResult), acpHeaderCanary) {
		t.Fatal("session/new response exposed an MCP authorization header")
	}
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newResult, &ns); err != nil || ns.SessionID == "" {
		t.Fatalf("session/new result: %s err=%v", newResult, err)
	}
	promptResult := e.call("session/prompt", map[string]any{
		"sessionId": ns.SessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": "finish without authentication"}},
	})
	var prompt struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(promptResult, &prompt); err != nil || prompt.StopReason != "end_turn" {
		t.Fatalf("session/prompt result: %s err=%v", promptResult, err)
	}
	select {
	case request := <-e.reqs:
		t.Fatalf("ACP client MCP declaration emitted unexpected %q request", request.Method)
	default:
	}
	if got := oauthCalls.Load(); got != 0 {
		t.Fatalf("ACP client MCP declaration contacted OAuth fixture %d times", got)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.called != 1 {
		t.Fatalf("factory called %d times, want 1", fake.called)
	}
	if len(fake.specs) != 1 {
		t.Fatalf("factory got %d specs, want 1: %+v", len(fake.specs), fake.specs)
	}
	spec := fake.specs[0]
	if spec.Name != "docs" || spec.URL != oauthFixture.URL+"/mcp" {
		t.Fatalf("spec name/url = %q/%q", spec.Name, spec.URL)
	}
	if spec.OAuth != nil {
		t.Fatal("ACP client MCP declaration unexpectedly gained an OAuth presenter/controller")
	}
	if spec.Headers["Authorization"] != "Bearer "+acpHeaderCanary {
		t.Fatalf("spec Authorization header did not preserve the input value")
	}
}

// TestSessionNewRejectsStdioMCP asserts a client-provided stdio MCP server is
// rejected (mecatl is streaming-HTTP MCP only), in both the command-shaped and the
// explicit type:"stdio" forms.
func TestSessionNewRejectsStdioMCP(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc)
	cwd := testCWD(t)
	cases := []string{
		// command-shaped, no type.
		fmt.Sprintf(`{"cwd":%q,"mcpServers":[{"name":"local","command":"some-bin"}]}`, cwd),
		// explicit type:"stdio".
		fmt.Sprintf(`{"cwd":%q,"mcpServers":[{"name":"local","type":"stdio","command":"some-bin"}]}`, cwd),
	}
	for _, params := range cases {
		_, err := a.Handle(context.Background(), "session/new", json.RawMessage(params), true)
		if err == nil || !strings.Contains(err.Error(), "stdio MCP") {
			t.Fatalf("expected stdio MCP rejection, got %v (params=%s)", err, params)
		}
	}
}

// TestSessionNewRejectsSSEMCP asserts a type:"sse" client MCP server is rejected
// (streaming-HTTP only).
func TestSessionNewRejectsSSEMCP(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc)
	params := fmt.Sprintf(`{"cwd":%q,"mcpServers":[{"name":"stream","type":"sse","url":"https://example.test/sse"}]}`, testCWD(t))
	_, err := a.Handle(context.Background(), "session/new", json.RawMessage(params), true)
	if err == nil || !strings.Contains(err.Error(), "sse") {
		t.Fatalf("expected sse rejection, got %v", err)
	}
}

// TestSessionNewRejectsBadScheme asserts an http MCP server with a non-allowed URL
// scheme (file://) is rejected by the SSRF scheme allowlist.
func TestSessionNewRejectsBadScheme(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc)
	params := fmt.Sprintf(`{"cwd":%q,"mcpServers":[{"name":"bad","type":"http","url":"file:///etc/passwd"}]}`, testCWD(t))
	_, err := a.Handle(context.Background(), "session/new", json.RawMessage(params), true)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected bad-scheme rejection, got %v", err)
	}
}

// TestSessionNewRejectsTooManyMCP asserts a client declaring more than the cap of
// MCP servers is rejected (CWE-400: the servers connect CONCURRENTLY under a
// bounded fan-out, so the cap bounds the goroutine and connection blast of one
// session/new rather than its wall-clock). It is rejected BEFORE the factory is
// consulted.
func TestSessionNewRejectsTooManyMCP(t *testing.T) {
	fake := &fakeSessionEngine{engine: stubEngine(t)}
	svc := newServiceCfg(t, mockllm.New(), nil, func(c *server.Config) { c.SessionEngine = fake.factory })
	a := acp.NewAgent(svc)

	var entries []string
	for i := 0; i < 9; i++ { // 9 > the cap of 8
		entries = append(entries, fmt.Sprintf(`{"type":"http","name":"s%d","url":"https://s%d.test/mcp"}`, i, i))
	}
	params := fmt.Sprintf(`{"cwd":%q,"mcpServers":[%s]}`, testCWD(t), strings.Join(entries, ","))
	_, err := a.Handle(context.Background(), "session/new", json.RawMessage(params), true)
	if err == nil || !strings.Contains(err.Error(), "too many MCP servers") {
		t.Fatalf("expected too-many-servers rejection, got %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.called != 0 {
		t.Fatalf("factory called %d times; the cap must reject before any connect", fake.called)
	}
}

// TestSessionNewSetsClientMCPTimeout asserts each accepted spec carries the bounded
// per-server connect timeout (so a slow client server cannot hold session/new for
// the full operator budget).
func TestSessionNewSetsClientMCPTimeout(t *testing.T) {
	fake := &fakeSessionEngine{engine: stubEngine(t)}
	svc := newServiceCfg(t, mockllm.New(), nil, func(c *server.Config) { c.SessionEngine = fake.factory })
	a := acp.NewAgent(svc)

	params := fmt.Sprintf(`{"cwd":%q,"mcpServers":[{"type":"http","name":"docs","url":"https://example.test/mcp"}]}`, testCWD(t))
	if _, err := a.Handle(context.Background(), "session/new", json.RawMessage(params), true); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(fake.specs))
	}
	if fake.specs[0].Timeout <= 0 || fake.specs[0].Timeout >= 30*time.Second {
		t.Fatalf("spec Timeout = %v, want a bounded client-path value (< operator 30s)", fake.specs[0].Timeout)
	}
}

// TestInitializeAdvertisesHTTPMCP asserts initialize advertises http:true / sse:false
// so an editor offers its streaming-HTTP MCP servers.
func TestInitializeAdvertisesHTTPMCP(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc)
	out, err := a.Handle(context.Background(), "initialize", json.RawMessage(`{"protocolVersion":1}`), true)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	b, _ := json.Marshal(out)
	var init struct {
		AgentCapabilities struct {
			McpCapabilities struct {
				HTTP bool `json:"http"`
				SSE  bool `json:"sse"`
			} `json:"mcpCapabilities"`
		} `json:"agentCapabilities"`
	}
	if jerr := json.Unmarshal(b, &init); jerr != nil {
		t.Fatalf("initialize result: %v", jerr)
	}
	if !init.AgentCapabilities.McpCapabilities.HTTP || init.AgentCapabilities.McpCapabilities.SSE {
		t.Fatalf("mcpCapabilities = %+v, want http:true sse:false", init.AgentCapabilities.McpCapabilities)
	}
}

func TestADR_0291_ACPBindAndLoadAssertConfiguredPlacement(t *testing.T) {
	root := testCWD(t)
	ref := session.EnvironmentRef{Kind: "remote", ID: "private-placement-id", Revision: "private-revision"}
	var binds, reattaches atomic.Int32
	llm := mockllm.New(mockllm.TextTurn("unused"))
	svc := newServiceCfg(t, llm, allowRules(), func(cfg *server.Config) {
		cfg.SharedEngineRoot = root
		cfg.PlacementProvider = acpPlacementProvider{root: root, ref: ref, bindCalls: &binds, reattachCalls: &reattaches}
	})
	a := acp.NewAgent(svc, acp.WithResume(true))

	createdAny, err := a.Handle(context.Background(), "session/new", json.RawMessage(fmt.Sprintf(`{"cwd":%q,"mcpServers":[]}`, root)), true)
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	createdJSON, _ := json.Marshal(createdAny)
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(createdJSON, &created); err != nil || created.SessionID == "" {
		t.Fatalf("decode session/new response: %s: %v", createdJSON, err)
	}
	stored, err := svc.GetSession(context.Background(), session.SessionID(created.SessionID))
	if err != nil || stored == nil {
		t.Fatalf("load persisted session: %v", err)
	}
	if stored.EnvironmentRef != ref {
		t.Fatalf("persisted placement = %+v; want exact %+v", stored.EnvironmentRef, ref)
	}
	mismatch := filepath.Join(root, "other")
	if _, err := a.Handle(context.Background(), "session/new", json.RawMessage(fmt.Sprintf(`{"cwd":%q,"mcpServers":[]}`, mismatch)), true); err == nil || !strings.Contains(err.Error(), "cwd does not match") {
		t.Fatalf("session/new cwd mismatch = %v", err)
	}
	loadParams := json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"mcpServers":[]}`, created.SessionID, root))
	if _, err := a.Handle(context.Background(), "session/load", loadParams, true); err != nil {
		t.Fatalf("session/load exact placement: %v", err)
	}
	badLoad := json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"mcpServers":[]}`, created.SessionID, mismatch))
	if _, err := a.Handle(context.Background(), "session/load", badLoad, true); err == nil || !strings.Contains(err.Error(), "cwd does not match") {
		t.Fatalf("session/load cwd mismatch = %v", err)
	}
	if got := binds.Load(); got != 3 { // startup validation + both session/new calls
		t.Fatalf("Bind calls = %d, want 3", got)
	}
	if got := reattaches.Load(); got != 4 { // create/load discovery plus both load attempts
		t.Fatalf("Reattach calls = %d, want 4", got)
	}
}

func TestServerOwnedSessionPlacement_Scenario7_ACPProjectsNoPhysicalPaths(t *testing.T) {
	root := resolvedTempDir(t)
	ref := session.EnvironmentRef{Kind: "remote", ID: "private-ref-id", Revision: "private-ref-revision"}
	svc := newServiceCfg(t, mockllm.New(), nil, func(cfg *server.Config) {
		cfg.SharedEngineRoot = root
		cfg.PlacementProvider = acpPlacementProvider{root: root, ref: ref}
	})
	a := acp.NewAgent(svc, acp.WithResume(true))
	created, err := a.Handle(context.Background(), "session/new", json.RawMessage(fmt.Sprintf(`{"cwd":%q,"mcpServers":[]}`, root)), true)
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	createdJSON, _ := json.Marshal(created)
	var envelope struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(createdJSON, &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	loaded, err := a.Handle(context.Background(), "session/load", json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"mcpServers":[]}`, envelope.SessionID, root)), true)
	if err != nil {
		t.Fatalf("session/load: %v", err)
	}
	loadedJSON, _ := json.Marshal(loaded)
	projection := string(createdJSON) + string(loadedJSON)
	for _, private := range []string{root, ref.ID, ref.Revision} {
		if strings.Contains(projection, private) {
			t.Fatalf("ACP projection exposed private placement data %q: %s", private, projection)
		}
	}
}

// TestSessionNewRejectsBadCwd asserts cwd validation: a relative path and a
// nonexistent absolute path are both rejected (and no session is created).
func TestSessionNewRejectsBadCwd(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc)
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"empty", "", "cwd is required"},
		{"relative", "relative/dir", "must be an absolute path"},
		{"nonexistent", filepath.Join(t.TempDir(), "does-not-exist"), "does not match the configured session placement"},
		{"file not dir", writeTempFile(t), "does not match the configured session placement"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := fmt.Sprintf(`{"cwd":%q,"mcpServers":[]}`, tc.cwd)
			_, err := a.Handle(context.Background(), "session/new", json.RawMessage(params), true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("cwd %q: want error containing %q, got %v", tc.cwd, tc.want, err)
			}
		})
	}
}

// writeTempFile creates a regular file and returns its path (a non-directory cwd
// must be rejected by the IsDir check).
func writeTempFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return p
}

// TestSecondPromptRejected asserts only one in-flight prompt per session.
func TestSecondPromptRejected(t *testing.T) {
	// A tool call requiring approval keeps the first prompt in flight (awaiting),
	// so the second prompt observes the in-flight guard.
	write := &scriptTool{name: "Write", content: "x"}
	llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "Write", `{}`)), mockllm.TextTurn("done"))
	svc := newService(t, llm, nil, write)

	agentStdinR, editorToAgentW := io.Pipe()
	agentStdoutR, agentStdoutW := io.Pipe()
	a := acp.NewAgent(svc)
	conn := acp.NewConn(agentStdinR, agentStdoutW, a.Handle)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = a.Serve(ctx, conn) }()

	e := &editor{t: t, toAgent: editorToAgentW, fromAgnt: bufio.NewReader(agentStdoutR),
		pend: map[int64]chan rpcMsg{}, notes: make(chan rpcMsg, 64), reqs: make(chan rpcMsg, 8)}
	go e.readLoop()

	res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)

	// First prompt blocks on the permission ask (we never answer it).
	go func() {
		_ = e.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
			"prompt": []any{map[string]any{"type": "text", "text": "go"}}})
	}()
	// Wait until the agent has asked for permission (the first prompt is now in
	// flight and paused).
	select {
	case <-e.reqs:
	case <-time.After(5 * time.Second):
		t.Fatal("no permission ask from first prompt")
	}

	// Second prompt for the SAME session must be rejected.
	e.mu.Lock()
	e.next++
	id := e.next
	ch := make(chan rpcMsg, 1)
	e.pend[id] = ch
	e.mu.Unlock()
	e.writeFrame(map[string]any{"jsonrpc": "2.0", "id": id, "method": "session/prompt",
		"params": map[string]any{"sessionId": ns.SessionID, "prompt": []any{map[string]any{"type": "text", "text": "again"}}}})
	select {
	case m := <-ch:
		if len(m.Error) == 0 {
			t.Fatalf("second prompt should have errored, got result %s", m.Result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second prompt did not respond")
	}
}

// TestSequentialPromptsNoRunLeak drives two prompts in sequence on one session
// and asserts the run registry holds no entry for the session after each
// completes — i.e. FinishRun deregisters the run, so a long-lived editor session
// does not leak a dead run per prompt.
func TestSequentialPromptsNoRunLeak(t *testing.T) {
	read := &scriptTool{name: "Read", readOnly: true, content: "body"}
	// Two prompts, each: a read tool call (auto-allowed) then a closing message.
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("done one"),
		mockllm.ToolCallTurn(call("c2", "Read", `{"path":"b"}`)),
		mockllm.TextTurn("done two"),
	)
	svc := newService(t, llm, allowRules(), read)

	agentStdinR, editorToAgentW := io.Pipe()
	agentStdoutR, agentStdoutW := io.Pipe()
	a := acp.NewAgent(svc)
	conn := acp.NewConn(agentStdinR, agentStdoutW, a.Handle)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = a.Serve(ctx, conn) }()

	e := &editor{t: t, toAgent: editorToAgentW, fromAgnt: bufio.NewReader(agentStdoutR),
		pend: map[int64]chan rpcMsg{}, notes: make(chan rpcMsg, 128), reqs: make(chan rpcMsg, 8)}
	go e.readLoop()

	res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)
	sid := session.SessionID(ns.SessionID)

	for i, text := range []string{"first", "second"} {
		var pr struct {
			StopReason string `json:"stopReason"`
		}
		out := e.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
			"prompt": []any{map[string]any{"type": "text", "text": text}}})
		if err := json.Unmarshal(out, &pr); err != nil || pr.StopReason != "end_turn" {
			t.Fatalf("prompt %d: stopReason %q err %v", i, pr.StopReason, err)
		}
		// The run for this session must be gone once the (blocking) prompt returns.
		if _, ok := svc.LookupRun(sid); ok {
			t.Fatalf("run leaked in registry after prompt %d completed", i)
		}
	}
}

// allowRules makes every tool auto-allowed (no permission ask), so a prompt runs
// straight to completion. It is the canonical allow-all FLOOR
// (permpolicy.AllowAllFloorRules), so the blanket allow never registers as a
// CONFIGURED rule under the issue-#32 decision bits.
func allowRules() []governance.Rule {
	return permpolicy.AllowAllFloorRules()
}

// call / callP build session.ToolCall values for the tests.
func call(id, name, args string) session.ToolCall {
	return session.NewToolCall(session.ToolCallID(id), name, json.RawMessage(args))
}

func callP(id, name, args string) *session.ToolCall {
	c := call(id, name, args)
	return &c
}

// startAgent wires an Agent over a pipe pair, starts Serve on a goroutine, and
// returns a connected editor plus a cleanup that closes the editor's writer and
// waits for Serve. It centralizes the boilerplate the new Phase-3 tests share.
func startAgent(t *testing.T, svc *server.Service, opts ...acp.AgentOption) (*editor, func()) {
	t.Helper()
	agentStdinR, editorToAgentW := io.Pipe()
	agentStdoutR, agentStdoutW := io.Pipe()
	a := acp.NewAgent(svc, opts...)
	conn := acp.NewConn(agentStdinR, agentStdoutW, a.Handle)
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	go func() { _ = a.Serve(ctx, conn); close(serveDone) }()
	e := &editor{t: t, toAgent: editorToAgentW, fromAgnt: bufio.NewReader(agentStdoutR),
		pend: map[int64]chan rpcMsg{}, notes: make(chan rpcMsg, 128), reqs: make(chan rpcMsg, 8)}
	go e.readLoop()
	cleanup := func() {
		_ = editorToAgentW.Close()
		select {
		case <-serveDone:
		case <-time.After(3 * time.Second):
		}
		cancel()
	}
	return e, cleanup
}

// waitForUpdate drains notifications until one matching sessionUpdate==kind
// arrives (or it times out), returning the matched update map.
func waitForUpdate(t *testing.T, e *editor, kind string) map[string]any {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case n := <-e.notes:
			if n.Method != "session/update" {
				continue
			}
			var p struct {
				Update map[string]any `json:"update"`
			}
			if err := json.Unmarshal(n.Params, &p); err != nil {
				continue
			}
			if p.Update["sessionUpdate"] == kind {
				return p.Update
			}
		case <-deadline:
			t.Fatalf("no %q session/update arrived", kind)
			return nil
		}
	}
}

// TestAvailableCommandsUpdateOnSessionNew asserts that session/new emits an
// available_commands_update listing the workspace's slash commands (mapped from
// the injected CommandLister) so the editor's palette is seeded.
func TestAvailableCommandsUpdateOnSessionNew(t *testing.T) {
	lister := fakeLister{cmds: []server.Command{
		{Name: "plan", Description: "Draft a plan"},
		{Name: "review", Description: "Review the diff"},
	}}
	svc := newServiceCfg(t, mockllm.New(), nil, func(c *server.Config) { c.Commands = lister })
	e, cleanup := startAgent(t, svc)
	defer cleanup()

	res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &ns); err != nil || ns.SessionID == "" {
		t.Fatalf("session/new: %s err=%v", res, err)
	}

	u := waitForUpdate(t, e, "available_commands_update")
	cmds, ok := u["availableCommands"].([]any)
	if !ok || len(cmds) != 2 {
		t.Fatalf("availableCommands = %v, want 2 entries", u["availableCommands"])
	}
	first := cmds[0].(map[string]any)
	if first["name"] != "plan" || first["description"] != "Draft a plan" {
		t.Fatalf("first command = %v", first)
	}
}

// TestNoAvailableCommandsUpdateWhenEmpty asserts that with no command lister (the
// default), session/new emits NO available_commands_update.
func TestNoAvailableCommandsUpdateWhenEmpty(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	e, cleanup := startAgent(t, svc)
	defer cleanup()

	_ = e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})

	// Briefly drain: any available_commands_update within the window is a failure.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case n := <-e.notes:
			if n.Method != "session/update" {
				continue
			}
			var p struct {
				Update map[string]any `json:"update"`
			}
			_ = json.Unmarshal(n.Params, &p)
			if p.Update["sessionUpdate"] == "available_commands_update" {
				t.Fatalf("unexpected available_commands_update with no lister")
			}
		case <-deadline:
			return
		}
	}
}

// TestSetModeAppliesAndEmitsCurrentModeUpdate asserts session/set_mode maps the
// modeId to the session's PermissionMode, persists it, and emits a
// current_mode_update reflecting the new mode.
func TestSetModeAppliesAndEmitsCurrentModeUpdate(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	e, cleanup := startAgent(t, svc)
	defer cleanup()

	res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)

	// Switch to plan mode.
	_ = e.call("session/set_mode", map[string]any{"sessionId": ns.SessionID, "modeId": "plan"})

	u := waitForUpdate(t, e, "current_mode_update")
	if u["currentModeId"] != "plan" {
		t.Fatalf("currentModeId = %v, want plan", u["currentModeId"])
	}

	// The persisted session reflects the new mode.
	sess, err := svc.GetSession(context.Background(), session.SessionID(ns.SessionID))
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Mode != session.ModePlan {
		t.Fatalf("session mode = %q, want plan", sess.Mode)
	}
}

// TestSetModeRejectsUnknownMode asserts an unknown modeId is rejected.
func TestSetModeRejectsUnknownMode(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc)
	res := e2eSessionID(t, a)
	params := fmt.Sprintf(`{"sessionId":%q,"modeId":"bogus"}`, res)
	_, err := a.Handle(context.Background(), "session/set_mode", json.RawMessage(params), true)
	if err == nil || !strings.Contains(err.Error(), "unknown modeId") {
		t.Fatalf("expected unknown modeId rejection, got %v", err)
	}
}

// e2eSessionID creates a session through the Handle entrypoint and returns its
// id (used by the direct-Handle tests that do not need the full pipe harness).
func e2eSessionID(t *testing.T, a *acp.Agent) string {
	t.Helper()
	params := fmt.Sprintf(`{"cwd":%q,"mcpServers":[]}`, testCWD(t))
	out, err := a.Handle(context.Background(), "session/new", json.RawMessage(params), true)
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	// out is the adapter's newSessionResponse value; re-marshal to read the id.
	b, _ := json.Marshal(out)
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(b, &ns)
	return ns.SessionID
}

// TestSessionLoadRestoresPersistedSession asserts session/load (with resume
// enabled) restores a completed session so a subsequent session/prompt continues
// it, preserving the conversation history.
func TestSessionLoadRestoresPersistedSession(t *testing.T) {
	read := &scriptTool{name: "Read", readOnly: true, content: "body"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("first done"),
		mockllm.TextTurn("second done"),
	)
	// A shared store object persists across the two agent connections.
	store := memstore.New()
	svc := newServiceCfg(t, llm, allowRules(), func(c *server.Config) { c.Store = store }, read)

	// Connection 1: create + run one prompt to completion, then disconnect.
	e1, cleanup1 := startAgent(t, svc, acp.WithResume(true))
	res := e1.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)
	out := e1.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
		"prompt": []any{map[string]any{"type": "text", "text": "first"}}})
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out, &pr); err != nil || pr.StopReason != "end_turn" {
		t.Fatalf("first prompt stopReason %q err %v", pr.StopReason, err)
	}
	cleanup1()

	// Connection 2: load the session, then continue with a second prompt.
	e2, cleanup2 := startAgent(t, svc, acp.WithResume(true))
	defer cleanup2()
	loadRes := e2.call("session/load", map[string]any{"sessionId": ns.SessionID, "cwd": testCWD(t), "mcpServers": []any{}})
	var lr struct {
		Modes *struct {
			CurrentModeID string `json:"currentModeId"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(loadRes, &lr); err != nil || lr.Modes == nil {
		t.Fatalf("session/load result: %s err=%v", loadRes, err)
	}

	out2 := e2.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
		"prompt": []any{map[string]any{"type": "text", "text": "second"}}})
	var pr2 struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out2, &pr2); err != nil || pr2.StopReason != "end_turn" {
		t.Fatalf("second prompt stopReason %q err %v", pr2.StopReason, err)
	}

	// The continued session preserved its history: the second turn ran on top of
	// the first prompt + tool result + assistant message, so the conversation has
	// grown beyond a single prompt.
	sess, err := svc.GetSession(context.Background(), session.SessionID(ns.SessionID))
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if len(sess.Conversation.Messages) < 4 {
		t.Fatalf("conversation has %d messages, want >=4 (history preserved across load)", len(sess.Conversation.Messages))
	}
}

// TestSessionLoadReplaysTranscript asserts session/load re-streams the persisted
// conversation as session/update notifications so a re-attaching editor rebuilds
// the transcript: it sees the assistant message chunk, the tool_call card (c1),
// and its tool_call_update (completed), with the tool_call arriving BEFORE the
// update (open-before-update). It also asserts no request_permission is issued
// during load (replay must never re-prompt for a historical, already-resolved
// approval).
func TestSessionLoadReplaysTranscript(t *testing.T) {
	read := &scriptTool{name: "Read", readOnly: true, content: "body"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("first done"),
	)
	store := memstore.New()
	svc := newServiceCfg(t, llm, allowRules(), func(c *server.Config) { c.Store = store }, read)

	// Connection 1: create + run a prompt that produces a tool_call(c1) + result +
	// final message, then disconnect.
	e1, cleanup1 := startAgent(t, svc, acp.WithResume(true))
	res := e1.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)
	out := e1.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
		"prompt": []any{map[string]any{"type": "text", "text": "first"}}})
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out, &pr); err != nil || pr.StopReason != "end_turn" {
		t.Fatalf("first prompt stopReason %q err %v", pr.StopReason, err)
	}
	cleanup1()

	// Connection 2: load the session and collect the replayed notifications, in
	// arrival order.
	e2, cleanup2 := startAgent(t, svc, acp.WithResume(true))
	defer cleanup2()
	_ = e2.call("session/load", map[string]any{"sessionId": ns.SessionID, "cwd": testCWD(t), "mcpServers": []any{}})

	updates := drainUpdates(e2.notes)

	// Security assertion: no request_permission was received during load.
	select {
	case req := <-e2.reqs:
		t.Fatalf("unexpected agent request during load: %s", req.Method)
	default:
	}

	// At least one agent_message_chunk (the "first done" reply).
	assertHasUpdate(t, updates, "agent_message_chunk", "")
	// A tool_call card for c1 and a completed tool_call_update for c1.
	callIdx, updateIdx := -1, -1
	for i, u := range updates {
		switch u["sessionUpdate"] {
		case "tool_call":
			if u["toolCallId"] == "c1" && callIdx < 0 {
				callIdx = i
			}
		case "tool_call_update":
			if u["toolCallId"] == "c1" && u["status"] == "completed" && updateIdx < 0 {
				updateIdx = i
			}
		}
	}
	if callIdx < 0 {
		t.Fatalf("no tool_call for c1 in replayed updates %v", updates)
	}
	if updateIdx < 0 {
		t.Fatalf("no completed tool_call_update for c1 in replayed updates %v", updates)
	}
	if callIdx >= updateIdx {
		t.Fatalf("tool_call (idx %d) must arrive before tool_call_update (idx %d) for c1", callIdx, updateIdx)
	}
}

// TestSessionLoadDisabledWithoutStore asserts that when resume is disabled (no
// durable store), initialize advertises loadSession:false and session/load
// returns a method error.
func TestSessionLoadDisabledWithoutStore(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	e, cleanup := startAgent(t, svc) // resume not enabled
	defer cleanup()

	initRes := e.call("initialize", map[string]any{"protocolVersion": 1})
	var init struct {
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(initRes, &init)
	if init.AgentCapabilities.LoadSession {
		t.Fatalf("loadSession advertised true with resume disabled")
	}

	// A session/load call must error rather than load.
	e.mu.Lock()
	e.next++
	id := e.next
	ch := make(chan rpcMsg, 1)
	e.pend[id] = ch
	e.mu.Unlock()
	e.writeFrame(map[string]any{"jsonrpc": "2.0", "id": id, "method": "session/load",
		"params": map[string]any{"sessionId": "whatever", "cwd": testCWD(t), "mcpServers": []any{}}})
	select {
	case m := <-ch:
		if len(m.Error) == 0 {
			t.Fatalf("session/load should have errored with resume disabled, got %s", m.Result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session/load did not respond")
	}
}

// closingSessionEngine is a SessionEngineFactory that hands back a stub engine and
// a close func that increments a counter, so the disconnect-teardown test can assert
// the re-mounted client MCP manager is drained on disconnect (the no-op
// fakeSessionEngine cannot prove teardown).
type closingSessionEngine struct {
	mu     sync.Mutex
	called int
	closed int
	engine *agent.Engine
}

func (f *closingSessionEngine) factory(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
	f.mu.Lock()
	f.called++
	f.mu.Unlock()
	return server.SessionEngineResult{Engine: f.engine, Close: func() error {
		f.mu.Lock()
		f.closed++
		f.mu.Unlock()
		return nil
	}}, nil
}

func (f *closingSessionEngine) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

func (f *closingSessionEngine) closes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// TestSessionLoadAcceptsHTTPMCP asserts session/load (resume enabled) RE-MOUNTS a
// client-provided streaming-HTTP MCP server: the per-session engine factory is
// invoked on load, the resumed session runs on it, and disconnect tears it down.
func TestSessionLoadAcceptsHTTPMCP(t *testing.T) {
	fake := &closingSessionEngine{engine: stubEngine(t)}
	store := memstore.New()
	svc := newServiceCfg(t, mockllm.New(mockllm.TextTurn("done")), allowRules(), func(c *server.Config) {
		c.Store = store
		c.SessionEngine = fake.factory
	})

	// Connection 1: create a session (no MCP) + run one prompt to completion so a
	// persisted, resumable snapshot exists.
	e1, cleanup1 := startAgent(t, svc, acp.WithResume(true))
	res := e1.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)
	out := e1.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
		"prompt": []any{map[string]any{"type": "text", "text": "first"}}})
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out, &pr); err != nil || pr.StopReason != "end_turn" {
		t.Fatalf("first prompt stopReason %q err %v", pr.StopReason, err)
	}
	cleanup1()

	// Connection 2: load the session WITH a streaming-HTTP MCP server. The factory
	// must be invoked exactly once to build the re-mounted per-session engine.
	e2, cleanup2 := startAgent(t, svc, acp.WithResume(true))
	loadRes := e2.call("session/load", map[string]any{
		"sessionId": ns.SessionID,
		"cwd":       testCWD(t),
		"mcpServers": []any{
			map[string]any{"type": "http", "name": "docs", "url": "https://example.test/mcp"},
		},
	})
	var lr struct {
		Modes *struct {
			CurrentModeID string `json:"currentModeId"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(loadRes, &lr); err != nil || lr.Modes == nil {
		t.Fatalf("session/load result: %s err=%v", loadRes, err)
	}
	if fake.calls() != 1 {
		t.Fatalf("factory called %d times on load with MCP, want 1", fake.calls())
	}

	// The resumed session runs on the re-mounted per-session engine.
	out2 := e2.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
		"prompt": []any{map[string]any{"type": "text", "text": "second"}}})
	var pr2 struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out2, &pr2); err != nil || pr2.StopReason != "end_turn" {
		t.Fatalf("second prompt stopReason %q err %v", pr2.StopReason, err)
	}

	// Disconnect tears the re-mounted per-session MCP manager down (the session was
	// tracked on load exactly like session/new).
	cleanup2()
	deadline := time.Now().Add(3 * time.Second)
	for fake.closes() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.closes() != 1 {
		t.Fatalf("per-session engine close called %d times after disconnect, want 1", fake.closes())
	}
}

// TestSessionLoadRejectsStdioMCP asserts a stdio client MCP server on session/load
// is rejected (mecatl is streaming-HTTP MCP only), via the SAME partitionClientMCP
// guard session/new uses — now with the "session/load" error prefix.
func TestSessionLoadRejectsStdioMCP(t *testing.T) {
	a := acp.NewAgent(newService(t, mockllm.New(), nil), acp.WithResume(true))
	cwd := testCWD(t)
	cases := []string{
		fmt.Sprintf(`{"sessionId":"s","cwd":%q,"mcpServers":[{"name":"local","command":"some-bin"}]}`, cwd),
		fmt.Sprintf(`{"sessionId":"s","cwd":%q,"mcpServers":[{"name":"local","type":"stdio","command":"some-bin"}]}`, cwd),
	}
	for _, params := range cases {
		_, err := a.Handle(context.Background(), "session/load", json.RawMessage(params), true)
		if err == nil || !strings.Contains(err.Error(), "stdio MCP") || !strings.Contains(err.Error(), "session/load") {
			t.Fatalf("expected stdio MCP rejection with session/load prefix, got %v (params=%s)", err, params)
		}
	}
}

// TestSessionLoadRejectsSSEMCP asserts a type:"sse" client MCP server on session/load
// is rejected (streaming-HTTP only).
func TestSessionLoadRejectsSSEMCP(t *testing.T) {
	a := acp.NewAgent(newService(t, mockllm.New(), nil), acp.WithResume(true))
	params := fmt.Sprintf(`{"sessionId":"s","cwd":%q,"mcpServers":[{"name":"stream","type":"sse","url":"https://example.test/sse"}]}`, testCWD(t))
	_, err := a.Handle(context.Background(), "session/load", json.RawMessage(params), true)
	if err == nil || !strings.Contains(err.Error(), "sse") {
		t.Fatalf("expected sse rejection, got %v", err)
	}
}

// TestSessionLoadRejectsBadScheme asserts an http MCP server with a non-allowed URL
// scheme on session/load is rejected by the SSRF scheme allowlist.
func TestSessionLoadRejectsBadScheme(t *testing.T) {
	a := acp.NewAgent(newService(t, mockllm.New(), nil), acp.WithResume(true))
	params := fmt.Sprintf(`{"sessionId":"s","cwd":%q,"mcpServers":[{"name":"bad","type":"http","url":"file:///etc/passwd"}]}`, testCWD(t))
	_, err := a.Handle(context.Background(), "session/load", json.RawMessage(params), true)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected bad-scheme rejection, got %v", err)
	}
}

// TestSessionLoadRejectsTooManyMCP asserts a client declaring more than the cap of
// MCP servers on session/load is rejected (CWE-400), BEFORE the factory is consulted.
func TestSessionLoadRejectsTooManyMCP(t *testing.T) {
	fake := &fakeSessionEngine{engine: stubEngine(t)}
	svc := newServiceCfg(t, mockllm.New(), nil, func(c *server.Config) { c.SessionEngine = fake.factory })
	a := acp.NewAgent(svc, acp.WithResume(true))

	var entries []string
	for i := 0; i < 9; i++ { // 9 > the cap of 8
		entries = append(entries, fmt.Sprintf(`{"type":"http","name":"s%d","url":"https://s%d.test/mcp"}`, i, i))
	}
	params := fmt.Sprintf(`{"sessionId":"s","cwd":%q,"mcpServers":[%s]}`, testCWD(t), strings.Join(entries, ","))
	_, err := a.Handle(context.Background(), "session/load", json.RawMessage(params), true)
	if err == nil || !strings.Contains(err.Error(), "too many MCP servers") {
		t.Fatalf("expected too-many-servers rejection, got %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.called != 0 {
		t.Fatalf("factory called %d times; the cap must reject before any connect", fake.called)
	}
}

// TestSessionLoadClientMCPTornDownOnDisconnect asserts that a session resumed with
// client MCP is tracked, so when the Serve loop ends (editor disconnect) the
// re-mounted per-session MCP manager is drained via CloseSession. It is the load-path
// twin of the session/new disconnect-teardown behaviour.
func TestSessionLoadClientMCPTornDownOnDisconnect(t *testing.T) {
	fake := &closingSessionEngine{engine: stubEngine(t)}
	store := memstore.New()
	svc := newServiceCfg(t, mockllm.New(mockllm.TextTurn("done")), allowRules(), func(c *server.Config) {
		c.Store = store
		c.SessionEngine = fake.factory
	})

	// Persist a resumable session.
	e1, cleanup1 := startAgent(t, svc, acp.WithResume(true))
	res := e1.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)
	out := e1.call("session/prompt", map[string]any{"sessionId": ns.SessionID,
		"prompt": []any{map[string]any{"type": "text", "text": "first"}}})
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out, &pr); err != nil || pr.StopReason != "end_turn" {
		t.Fatalf("first prompt stopReason %q err %v", pr.StopReason, err)
	}
	cleanup1()

	// Resume WITH client MCP, then end the Serve loop.
	e2, cleanup2 := startAgent(t, svc, acp.WithResume(true))
	_ = e2.call("session/load", map[string]any{
		"sessionId": ns.SessionID,
		"cwd":       testCWD(t),
		"mcpServers": []any{
			map[string]any{"type": "http", "name": "docs", "url": "https://example.test/mcp"},
		},
	})
	if fake.calls() != 1 {
		t.Fatalf("factory called %d times on load, want 1", fake.calls())
	}
	cleanup2() // ends Serve -> closeTrackedSessions -> CloseSession drains the manager.

	deadline := time.Now().Add(3 * time.Second)
	for fake.closes() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.closes() != 1 {
		t.Fatalf("per-session engine close called %d times after disconnect, want 1", fake.closes())
	}
}

// callErr issues an editor->agent request and returns the JSON-RPC error (failing
// if the call succeeded instead). It mirrors call but for the loud-reject paths.
func (e *editor) callErr(method string, params any) json.RawMessage {
	e.mu.Lock()
	e.next++
	id := e.next
	ch := make(chan rpcMsg, 1)
	e.pend[id] = ch
	e.mu.Unlock()
	e.writeFrame(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case m := <-ch:
		if len(m.Error) == 0 {
			e.t.Fatalf("%s should have errored, got result %s", method, m.Result)
		}
		return m.Error
	case <-time.After(5 * time.Second):
		e.t.Fatalf("%s timed out", method)
		return nil
	}
}

// initCaps issues initialize and returns the advertised promptCapabilities.
func initCaps(t *testing.T, e *editor) (image, audio, embedded bool) {
	t.Helper()
	res := e.call("initialize", map[string]any{"protocolVersion": 1})
	var init struct {
		AgentCapabilities struct {
			PromptCapabilities struct {
				Image           bool `json:"image"`
				Audio           bool `json:"audio"`
				EmbeddedContext bool `json:"embeddedContext"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(res, &init); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	pc := init.AgentCapabilities.PromptCapabilities
	return pc.Image, pc.Audio, pc.EmbeddedContext
}

var pngBlockB64 = "iVBORw0KGgo=" // any valid base64; validators check the mime, not the bytes

// TestInitializeTextOnlyProviderAdvertisesNoMedia asserts a text-only provider
// (the mockllm default) advertises image:false / audio:false / embeddedContext:false.
func TestInitializeTextOnlyProviderAdvertisesNoMedia(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	e, cleanup := startAgent(t, svc)
	defer cleanup()
	image, audio, embedded := initCaps(t, e)
	if image || audio || embedded {
		t.Fatalf("text-only provider advertised media: image=%v audio=%v embedded=%v", image, audio, embedded)
	}
}

// TestInitializeImageCapableProviderAdvertisesImage asserts an image-capable
// provider advertises image:true (and embeddedContext:true), audio:false.
func TestInitializeImageCapableProviderAdvertisesImage(t *testing.T) {
	llm := mockllm.NewWith([]mockllm.Option{
		mockllm.WithCapabilities(port.ProviderCapabilities{Image: true, EmbeddedContext: true}),
	})
	svc := newServiceCfg(t, llm, nil, nil)
	e, cleanup := startAgent(t, svc)
	defer cleanup()
	image, audio, embedded := initCaps(t, e)
	if !image || audio || !embedded {
		t.Fatalf("image-capable provider: image=%v audio=%v embedded=%v, want true/false/true", image, audio, embedded)
	}
}

// TestPromptImageWithImageCapableProvider drives a full prompt carrying an image
// block against an image-capable provider: the run starts and completes (proving
// StartRunContent was called with the part and the loop consumed the prompt).
func TestPromptImageWithImageCapableProvider(t *testing.T) {
	llm := mockllm.NewWith([]mockllm.Option{
		mockllm.WithCapabilities(port.ProviderCapabilities{Image: true, EmbeddedContext: true}),
	}, mockllm.TextTurn("saw the image"))
	svc := newServiceCfg(t, llm, allowRules(), nil)
	e, cleanup := startAgent(t, svc)
	defer cleanup()

	res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)

	out := e.call("session/prompt", map[string]any{
		"sessionId": ns.SessionID,
		"prompt": []any{
			map[string]any{"type": "text", "text": "what is this"},
			map[string]any{"type": "image", "mimeType": "image/png", "data": pngBlockB64},
		},
	})
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(out, &pr); err != nil || pr.StopReason != "end_turn" {
		t.Fatalf("prompt: stopReason %q err %v", pr.StopReason, err)
	}
	if llm.Calls() == 0 {
		t.Fatalf("provider.Stream was never called; the image prompt did not start a run")
	}
}

// TestPromptImageWithTextOnlyProviderRejected asserts an image block against a
// text-only provider is loud-rejected and NO run is ever started.
func TestPromptImageWithTextOnlyProviderRejected(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("should not run"))
	svc := newService(t, llm, allowRules())
	e, cleanup := startAgent(t, svc)
	defer cleanup()

	res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)

	errBody := e.callErr("session/prompt", map[string]any{
		"sessionId": ns.SessionID,
		"prompt": []any{
			map[string]any{"type": "image", "mimeType": "image/png", "data": pngBlockB64},
		},
	})
	if !strings.Contains(string(errBody), "image content not supported") {
		t.Fatalf("error should say image unsupported, got %s", errBody)
	}
	// The run was never started: the provider's Stream must not have been called and
	// no run leaked in the registry.
	if _, ok := svc.LookupRun(session.SessionID(ns.SessionID)); ok {
		t.Fatalf("a run was started for a rejected image prompt")
	}
	if llm.Calls() != 0 {
		t.Fatalf("provider.Stream was called %d times for a rejected prompt; want 0", llm.Calls())
	}
}

// TestPromptAudioWithAudioIncapableProviderRejected asserts an audio block against
// an image-only (audio:false) provider is loud-rejected before the run — proving
// audio is wired but provider-gated off.
func TestPromptAudioWithAudioIncapableProviderRejected(t *testing.T) {
	llm := mockllm.NewWith([]mockllm.Option{
		mockllm.WithCapabilities(port.ProviderCapabilities{Image: true, EmbeddedContext: true}),
	}, mockllm.TextTurn("should not run"))
	svc := newServiceCfg(t, llm, allowRules(), nil)
	e, cleanup := startAgent(t, svc)
	defer cleanup()

	res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(res, &ns)

	errBody := e.callErr("session/prompt", map[string]any{
		"sessionId": ns.SessionID,
		"prompt": []any{
			map[string]any{"type": "audio", "mimeType": "audio/wav", "data": pngBlockB64},
		},
	})
	if !strings.Contains(string(errBody), "audio content not supported") {
		t.Fatalf("error should say audio unsupported, got %s", errBody)
	}
	if llm.Calls() != 0 {
		t.Fatalf("provider.Stream was called %d times for a rejected audio prompt; want 0", llm.Calls())
	}
}

// TestSessionLoadUnknownSession asserts session/load with resume enabled errors
// cleanly for a session id the store has never seen.
func TestSessionLoadUnknownSession(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc, acp.WithResume(true))
	params := fmt.Sprintf(`{"sessionId":"nope","cwd":%q,"mcpServers":[]}`, testCWD(t))
	_, err := a.Handle(context.Background(), "session/load", json.RawMessage(params), true)
	if err == nil {
		t.Fatal("expected session/load error for unknown session")
	}
}

// newSessionWithHTTPMCP creates a session over the pipe harness carrying one
// streaming-HTTP MCP server, so the per-session engine factory is invoked. It
// returns the new session id. Shared by the session/close teardown tests.
func newSessionWithHTTPMCP(t *testing.T, e *editor) string {
	t.Helper()
	res := e.call("session/new", map[string]any{
		"cwd": testCWD(t),
		"mcpServers": []any{
			map[string]any{"type": "http", "name": "docs", "url": "https://example.test/mcp"},
		},
	})
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &ns); err != nil || ns.SessionID == "" {
		t.Fatalf("session/new: %s err=%v", res, err)
	}
	return ns.SessionID
}

// TestSessionClose_TearsDownPerSessionEngine asserts the ACP session/close
// request tears down a session's per-session engine: with a streaming-HTTP MCP
// server mounted, session/close returns {} and fires the engine's close exactly
// once (the mid-session twin of the gRPC CloseSession RPC / HTTP DELETE).
func TestSessionClose_TearsDownPerSessionEngine(t *testing.T) {
	fake := &closingSessionEngine{engine: stubEngine(t)}
	svc := newServiceCfg(t, mockllm.New(mockllm.TextTurn("done")), allowRules(), func(c *server.Config) {
		c.SessionEngine = fake.factory
	})
	e, cleanup := startAgent(t, svc)
	defer cleanup()

	sid := newSessionWithHTTPMCP(t, e)
	if fake.calls() != 1 {
		t.Fatalf("factory called %d times on session/new, want 1", fake.calls())
	}

	res := e.call("session/close", map[string]any{"sessionId": sid})
	// The result is the empty object.
	var empty map[string]any
	if err := json.Unmarshal(res, &empty); err != nil || len(empty) != 0 {
		t.Fatalf("session/close result = %s err=%v, want {}", res, err)
	}
	if fake.closes() != 1 {
		t.Fatalf("per-session engine close called %d times after session/close, want 1", fake.closes())
	}
}

// TestSessionClose_NoDoubleCloseOnDisconnect is the load-bearing guard: after a
// session/close, ending the Serve loop (disconnect) must NOT close the engine
// again — untrackSession removed it from the tracked set, so closeTrackedSessions
// skips it. The close callback fires EXACTLY ONCE total.
func TestSessionClose_NoDoubleCloseOnDisconnect(t *testing.T) {
	fake := &closingSessionEngine{engine: stubEngine(t)}
	svc := newServiceCfg(t, mockllm.New(mockllm.TextTurn("done")), allowRules(), func(c *server.Config) {
		c.SessionEngine = fake.factory
	})
	e, cleanup := startAgent(t, svc)

	sid := newSessionWithHTTPMCP(t, e)
	_ = e.call("session/close", map[string]any{"sessionId": sid})
	if fake.closes() != 1 {
		t.Fatalf("close after session/close = %d, want 1", fake.closes())
	}

	// End the Serve loop. The backstop closeTrackedSessions must skip the already
	// closed (and untracked) session: no second close.
	cleanup()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if fake.closes() != 1 {
			t.Fatalf("double-close: engine close fired %d times after disconnect, want exactly 1", fake.closes())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fake.closes() != 1 {
		t.Fatalf("engine close fired %d times total, want exactly 1", fake.closes())
	}
}

// TestSessionClose_CancelsInFlightRun asserts session/close cancels a blocked
// in-flight run: a prompt paused on a permission ask resolves with stopReason
// "cancelled" once session/close cancels it, and the per-session engine tears
// down afterwards.
func TestSessionClose_CancelsInFlightRun(t *testing.T) {
	// A mutating tool with default (nil) rules => Ask, so the prompt blocks on the
	// permission ask we never answer — keeping the run in flight until cancelled.
	write := &scriptTool{name: "Write", content: "x"}
	cat := tool.NewCatalog()
	cat.MustRegister(write)
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(call("c1", "Write", `{}`)), mockllm.TextTurn("done")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, nil), // nil rules => Ask
		Model:   "test-model",
	})
	fake := &closingSessionEngine{engine: perSession}
	svc := newServiceCfg(t, mockllm.New(), nil, func(c *server.Config) {
		c.SessionEngine = fake.factory
	}, write)
	e, cleanup := startAgent(t, svc)
	defer cleanup()

	sid := newSessionWithHTTPMCP(t, e)

	// Start the prompt; it blocks on the permission ask (never answered).
	promptDone := make(chan json.RawMessage, 1)
	go func() {
		promptDone <- e.call("session/prompt", map[string]any{
			"sessionId": sid,
			"prompt":    []any{map[string]any{"type": "text", "text": "write it"}},
		})
	}()

	// Wait until the agent asks for permission (the run is now in flight + paused).
	select {
	case req := <-e.reqs:
		if req.Method != "session/request_permission" {
			t.Fatalf("expected request_permission, got %q", req.Method)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no permission ask; run never went in flight")
	}

	// session/close cancels the in-flight run, then tears the session down.
	res := e.call("session/close", map[string]any{"sessionId": sid})
	var empty map[string]any
	if err := json.Unmarshal(res, &empty); err != nil || len(empty) != 0 {
		t.Fatalf("session/close result = %s err=%v, want {}", res, err)
	}

	// The blocked prompt resolves with stopReason "cancelled".
	select {
	case out := <-promptDone:
		var pr struct {
			StopReason string `json:"stopReason"`
		}
		if err := json.Unmarshal(out, &pr); err != nil {
			t.Fatalf("prompt result: %v", err)
		}
		if pr.StopReason != "cancelled" {
			t.Fatalf("stopReason = %q, want cancelled", pr.StopReason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not resolve after session/close")
	}

	// The per-session engine tore down (after the run unwound).
	if fake.closes() != 1 {
		t.Fatalf("per-session engine close called %d times after session/close, want 1", fake.closes())
	}
}

// TestSessionClose_UnknownSession asserts session/close for a never-created id is
// rejected with a method error (from EndSession's ErrNotFound -> invalidParams),
// mirroring the gRPC unknown-id behaviour.
func TestSessionClose_UnknownSession(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc)
	_, err := a.Handle(context.Background(), "session/close", json.RawMessage(`{"sessionId":"never-created"}`), true)
	if err == nil || !strings.Contains(err.Error(), "session/close") {
		t.Fatalf("expected session/close error for unknown id, got %v", err)
	}
}

// TestSessionClose_Idempotent asserts two session/close for the same created id
// both succeed: the persisted snapshot still resolves, so EndSession returns nil
// on the second. Mirrors the gRPC double-close test.
func TestSessionClose_Idempotent(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc)
	sid := e2eSessionID(t, a) // a plain (shared-engine) session is enough here

	for i := 0; i < 2; i++ {
		out, err := a.Handle(context.Background(), "session/close",
			json.RawMessage(fmt.Sprintf(`{"sessionId":%q}`, sid)), true)
		if err != nil {
			t.Fatalf("session/close #%d: %v", i+1, err)
		}
		b, _ := json.Marshal(out)
		if string(b) != "{}" {
			t.Fatalf("session/close #%d result = %s, want {}", i+1, b)
		}
	}
}

// TestSessionClose_EmptySessionID asserts an empty sessionId is rejected with
// codeInvalidParams, mirroring handleSetMode's validation.
func TestSessionClose_EmptySessionID(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc)
	_, err := a.Handle(context.Background(), "session/close", json.RawMessage(`{"sessionId":""}`), true)
	if err == nil || !strings.Contains(err.Error(), "sessionId is required") {
		t.Fatalf("expected sessionId-required rejection, got %v", err)
	}
}

// TestInitialize_AdvertisesCloseCapability asserts the initialize response carries
// sessionCapabilities.close=true at the confirmed JSON path (unconditionally —
// mecatl can always end a session), even with resume disabled.
func TestInitialize_AdvertisesCloseCapability(t *testing.T) {
	svc := newService(t, mockllm.New(), nil)
	a := acp.NewAgent(svc) // resume disabled => loadSession false, close still true
	out, err := a.Handle(context.Background(), "initialize", json.RawMessage(`{"protocolVersion":1}`), true)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	b, _ := json.Marshal(out)
	var init struct {
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				Close bool `json:"close"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if jerr := json.Unmarshal(b, &init); jerr != nil {
		t.Fatalf("initialize result: %v", jerr)
	}
	if !init.AgentCapabilities.SessionCapabilities.Close {
		t.Fatalf("sessionCapabilities.close = false, want true; result=%s", b)
	}
	if init.AgentCapabilities.LoadSession {
		t.Fatalf("loadSession advertised true with resume disabled")
	}
}
