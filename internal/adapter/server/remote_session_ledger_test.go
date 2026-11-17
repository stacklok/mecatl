package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const remoteLedgerFile = "notes.txt"

// sharedLedgerBackend stands in for the Redis ledger that every replica reaches.
type sharedLedgerBackend struct {
	mu   sync.Mutex
	byID map[session.SessionID]*memledger.Ledger
}

func (b *sharedLedgerBackend) For(id session.SessionID) tool.ReadLedger {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byID == nil {
		b.byID = map[session.SessionID]*memledger.Ledger{}
	}
	if b.byID[id] == nil {
		b.byID[id] = memledger.New()
	}
	return b.byID[id]
}

// remoteLedgerProvider serves one shared workspace and, like the execution
// client, a fresh ledger on every AcquireRun.
type remoteLedgerProvider struct {
	ref      session.EnvironmentRef
	ws       *memfs.Workspace
	runner   tool.CommandRunner
	released chan struct{}
}

type (
	bindReq      = server.PlacementBindRequest
	reattachReq  = server.PlacementReattachRequest
	successorReq = server.PlacementSuccessorRequest
	rlBinding    = server.PlacementBinding
)

func newRemoteLedgerProvider(t *testing.T) *remoteLedgerProvider {
	t.Helper()
	p := &remoteLedgerProvider{
		ref:      session.EnvironmentRef{Kind: "kubernetes", ID: "env", Revision: "rev"},
		ws:       memfs.NewWorkspace("/remote"),
		released: make(chan struct{}, 8),
	}
	p.runner = clobberRunner{ws: p.ws}
	if err := p.ws.Write(t.Context(), remoteLedgerFile, []byte("alpha\n")); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *remoteLedgerProvider) env(ledger tool.ReadLedger) tool.Environment {
	return tool.MustEnvironment(p.ref, p.ws, ledger, p.runner)
}

func (p *remoteLedgerProvider) binding() rlBinding {
	return rlBinding{
		Ref:                  p.ref,
		Environment:          p.env(memledger.New()),
		GovernanceRoot:       "/remote",
		AdmittedCapabilities: []string{"filesystem", "foreground-command"},
	}
}

func (p *remoteLedgerProvider) Bind(context.Context, bindReq) (rlBinding, error) {
	return p.binding(), nil
}

func (p *remoteLedgerProvider) Reattach(context.Context, reattachReq) (rlBinding, error) {
	return p.binding(), nil
}

func (p *remoteLedgerProvider) ReserveSuccessor(context.Context, successorReq) (rlBinding, error) {
	return p.binding(), nil
}

func (p *remoteLedgerProvider) Applies(ref session.EnvironmentRef) bool { return ref == p.ref }

func (p *remoteLedgerProvider) AcquireRun(
	context.Context, server.ExecutionRunRequest,
) (server.ExecutionRunHandle, error) {
	return &remoteLedgerHandle{provider: p, env: p.env(memledger.New())}, nil
}

func (p *remoteLedgerProvider) content(t *testing.T) string {
	t.Helper()
	data, err := p.ws.Read(t.Context(), remoteLedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type remoteLedgerHandle struct {
	provider *remoteLedgerProvider
	env      tool.Environment
}

func (h *remoteLedgerHandle) Environment() tool.Environment { return h.env }
func (*remoteLedgerHandle) Renew(context.Context) error     { return nil }
func (h *remoteLedgerHandle) Release(context.Context) error {
	select {
	case h.provider.released <- struct{}{}:
	default:
	}
	return nil
}

// clobberRunner rewrites the file whatever the command, as a Shell edit would.
type clobberRunner struct{ ws *memfs.Workspace }

func (r clobberRunner) BoundWorkspaceRoot() string { return r.ws.Root() }

func (r clobberRunner) Run(ctx context.Context, _ string) (tool.CommandResult, error) {
	return tool.CommandResult{}, r.ws.Write(ctx, remoteLedgerFile, []byte("alpha shell\n"))
}

func ledgerToolCall(id, name string, args map[string]string) session.ToolCall {
	raw, _ := json.Marshal(args)
	return session.NewToolCall(session.ToolCallID(id), name, raw)
}

func readCall(id string) session.ToolCall {
	return ledgerToolCall(id, "Read", map[string]string{"path": remoteLedgerFile})
}

func editCall(id string) session.ToolCall {
	return ledgerToolCall(id, "Edit", map[string]string{
		"path": remoteLedgerFile, "old_string": "alpha", "new_string": "beta",
	})
}

func newRemoteLedgerService(t *testing.T, store port.SessionStore, p *remoteLedgerProvider,
	ledgers func(session.SessionID) tool.ReadLedger, turns ...mockllm.Turn,
) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	for _, tl := range []tool.Tool{fstools.ReadTool{}, fstools.EditTool{}, fstools.NewShellTool()} {
		cat.MustRegister(tl)
	}
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(turns...), Catalog: cat, Policy: permpolicy.NewPolicy(nil, nil),
		Model: "test",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine, Store: store, PlacementProvider: p, PlacementScope: "test",
		ExecutionAccess: p, SharedEngineRoot: "/remote", SessionReadLedger: ledgers,
		Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func saveRemoteSession(t *testing.T, store port.SessionStore, p *remoteLedgerProvider,
	id session.SessionID,
) {
	t.Helper()
	limits := session.Limits{MaxTurns: 5}
	sess := session.New(id, session.ModeDefault, p.ref, limits, time.Unix(1, 0))
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
}

// drainLedgerRun approves every ask and returns the tool results by call id.
func drainLedgerRun(t *testing.T, svc *server.Service, id session.SessionID,
	run *agent.Run,
) map[string]session.ToolResult {
	t.Helper()
	results := map[string]session.ToolResult{}
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			results[string(ev.ToolResult.CallID)] = *ev.ToolResult
		}
	}
	svc.Persist(t.Context(), id)
	svc.FinishRun(id, run)
	return results
}

func promptLedgerRun(t *testing.T, svc *server.Service,
	id session.SessionID,
) map[string]session.ToolResult {
	t.Helper()
	run, err := svc.StartRunContent(t.Context(), id, "go", nil)
	if err != nil {
		t.Fatal(err)
	}
	return drainLedgerRun(t, svc, id, run)
}

func requireEditRefused(t *testing.T, result session.ToolResult) {
	t.Helper()
	if !result.IsError || !strings.Contains(result.Content, "refusing to edit") {
		t.Fatalf("edit result = %+v, want read-before-edit refusal", result)
	}
}

func TestRemoteRunsShareSessionLedgerAcrossServices(t *testing.T) {
	store := memstore.New()
	p := newRemoteLedgerProvider(t)
	backend := &sharedLedgerBackend{}
	reader := newRemoteLedgerService(t, store, p, backend.For,
		mockllm.ToolCallTurn(readCall("r1")), mockllm.TextTurn("read"))
	editor := newRemoteLedgerService(t, store, p, backend.For,
		mockllm.ToolCallTurn(editCall("e1")), mockllm.TextTurn("edited"))
	saveRemoteSession(t, store, p, "remote-ledger")

	if got := promptLedgerRun(t, reader, "remote-ledger")["r1"]; got.IsError {
		t.Fatalf("read = %+v", got)
	}
	if got := promptLedgerRun(t, editor, "remote-ledger")["e1"]; got.IsError {
		t.Fatalf("edit on another service = %+v", got)
	}
	if got := p.content(t); got != "beta\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestRemoteSessionLedgerRefusesFileChangedBetweenRuns(t *testing.T) {
	shell := ledgerToolCall("s1", "Shell", map[string]string{"command": "touch notes.txt"})
	cases := map[string]struct {
		change func(*testing.T, *remoteLedgerProvider)
		turns  []mockllm.Turn
		want   string
	}{
		"directly": {
			change: func(t *testing.T, p *remoteLedgerProvider) {
				err := p.ws.Write(t.Context(), remoteLedgerFile, []byte("alpha changed\n"))
				if err != nil {
					t.Fatal(err)
				}
			},
			turns: []mockllm.Turn{mockllm.ToolCallTurn(editCall("e1")), mockllm.TextTurn("done")},
			want:  "alpha changed\n",
		},
		"by shell": {
			change: func(*testing.T, *remoteLedgerProvider) {},
			turns: []mockllm.Turn{
				mockllm.ToolCallTurn(shell), mockllm.ToolCallTurn(editCall("e1")),
				mockllm.TextTurn("done"),
			},
			want: "alpha shell\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := memstore.New()
			p := newRemoteLedgerProvider(t)
			backend := &sharedLedgerBackend{}
			reader := newRemoteLedgerService(t, store, p, backend.For,
				mockllm.ToolCallTurn(readCall("r1")), mockllm.TextTurn("read"))
			editor := newRemoteLedgerService(t, store, p, backend.For, tc.turns...)
			saveRemoteSession(t, store, p, "remote-stale")

			promptLedgerRun(t, reader, "remote-stale")
			tc.change(t, p)
			requireEditRefused(t, promptLedgerRun(t, editor, "remote-stale")["e1"])
			if got := p.content(t); got != tc.want {
				t.Fatalf("content = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRemoteRunsWithoutSessionLedgerKeepPerRunLedger(t *testing.T) {
	store := memstore.New()
	p := newRemoteLedgerProvider(t)
	svc := newRemoteLedgerService(t, store, p, nil,
		mockllm.ToolCallTurn(readCall("r1")), mockllm.TextTurn("read"),
		mockllm.ToolCallTurn(editCall("e1")), mockllm.TextTurn("edited"))
	saveRemoteSession(t, store, p, "remote-per-run")

	promptLedgerRun(t, svc, "remote-per-run")
	requireEditRefused(t, promptLedgerRun(t, svc, "remote-per-run")["e1"])
	if got := p.content(t); got != "alpha\n" {
		t.Fatalf("content = %q", got)
	}
}

func saveParkedRemoteEdit(t *testing.T, store port.SessionStore, p *remoteLedgerProvider,
	id session.SessionID,
) session.PendingAsk {
	t.Helper()
	limits := session.Limits{MaxTurns: 5}
	sess := session.New(id, session.ModeDefault, p.ref, limits, time.Unix(1, 0))
	sess.BeginRun("run-" + string(id))
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := editCall("parked-edit")
	msg := session.NewAssistantMessage("", "", []session.ToolCall{call})
	if err := sess.RecordAssistant(msg); err != nil {
		t.Fatal(err)
	}
	ask := session.PendingAsk{
		AskID: "parked-ask", Tool: "Edit", Call: call.ID, Origin: session.ApprovalOriginPermission,
	}
	if err := sess.PauseForApproval(ask); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	return ask
}

type ledgerPath struct {
	store port.SessionStore
	p     *remoteLedgerProvider
	svc   *server.Service
	id    session.SessionID
}

func requireEdited(t *testing.T, result session.ToolResult) {
	t.Helper()
	if result.IsError {
		t.Fatalf("edit = %+v", result)
	}
}

func TestRemoteRunPathsBindSessionLedger(t *testing.T) {
	retryable := mockllm.ErrorTurn(
		&retryFailure{session.RetryDispositionRetryable, session.StreamProgressPrecommit})
	edit := mockllm.ToolCallTurn(editCall("e1"))
	paths := map[string]struct {
		turns []mockllm.Turn
		run   func(*testing.T, ledgerPath)
	}{
		"new run": {
			turns: []mockllm.Turn{edit, mockllm.TextTurn("done")},
			run: func(t *testing.T, lp ledgerPath) {
				saveRemoteSession(t, lp.store, lp.p, lp.id)
				requireEdited(t, promptLedgerRun(t, lp.svc, lp.id)["e1"])
			},
		},
		"retry": {
			turns: []mockllm.Turn{retryable, edit, mockllm.TextTurn("done")},
			run: func(t *testing.T, lp ledgerPath) {
				saveRemoteSession(t, lp.store, lp.p, lp.id)
				promptLedgerRun(t, lp.svc, lp.id)
				run, err := lp.svc.RetryFailedRun(t.Context(), lp.id)
				if err != nil {
					t.Fatal(err)
				}
				requireEdited(t, drainLedgerRun(t, lp.svc, lp.id, run)["e1"])
			},
		},
		"persisted ask resolution": {
			turns: []mockllm.Turn{mockllm.TextTurn("done")},
			run: func(t *testing.T, lp ledgerPath) {
				ask := saveParkedRemoteEdit(t, lp.store, lp.p, lp.id)
				runID := "run-" + string(lp.id)
				_, err := lp.svc.ResolveRunAsk(t.Context(), lp.id, runID, ask.AskID,
					session.VerdictAllowOnce)
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-lp.p.released:
				case <-time.After(5 * time.Second):
					t.Fatal("resolved run did not release its execution claim")
				}
			},
		},
		"resume after approval": {
			turns: []mockllm.Turn{mockllm.TextTurn("done")},
			run: func(t *testing.T, lp ledgerPath) {
				ask := saveParkedRemoteEdit(t, lp.store, lp.p, lp.id)
				run, err := lp.svc.ApproveRun(t.Context(), lp.id, ask.AskID,
					session.VerdictAllowOnce, "")
				if err != nil || run == nil {
					t.Fatalf("ApproveRun = %v, %v", run, err)
				}
				requireEdited(t, drainLedgerRun(t, lp.svc, lp.id, run)["parked-edit"])
			},
		},
	}
	for name, tc := range paths {
		t.Run(name, func(t *testing.T) {
			p := newRemoteLedgerProvider(t)
			lp := ledgerPath{store: memstore.New(), p: p, id: "remote-path"}
			backend := &sharedLedgerBackend{}
			lp.svc = newRemoteLedgerService(t, lp.store, lp.p, backend.For, tc.turns...)
			seed, err := fstools.ReadTool{}.Execute(t.Context(), readCall("seed"),
				lp.p.env(backend.For(lp.id)))
			if err != nil || seed.IsError {
				t.Fatalf("seed read = %+v, %v", seed, err)
			}
			tc.run(t, lp)
			if got := lp.p.content(t); got != "beta\n" {
				t.Fatalf("content = %q, want the edit applied through the session ledger", got)
			}
		})
	}
}

func TestRemoteSessionLedgerIsKeyedBySession(t *testing.T) {
	store := memstore.New()
	p := newRemoteLedgerProvider(t)
	backend := &sharedLedgerBackend{}
	svc := newRemoteLedgerService(t, store, p, backend.For,
		mockllm.ToolCallTurn(readCall("r1")), mockllm.TextTurn("read"),
		mockllm.ToolCallTurn(editCall("e1")), mockllm.TextTurn("refused"),
		mockllm.ToolCallTurn(editCall("e2")), mockllm.TextTurn("refused"),
		mockllm.ToolCallTurn(editCall("e3")), mockllm.TextTurn("refused"))
	const source = session.SessionID("remote-source")
	saveRemoteSession(t, store, p, source)
	saveRemoteSession(t, store, p, "remote-peer")
	promptLedgerRun(t, svc, source)

	requireEditRefused(t, promptLedgerRun(t, svc, "remote-peer")["e1"])
	fork, err := svc.ForkSessionSuccessor(t.Context(), server.ForkSuccessorRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	requireEditRefused(t, promptLedgerRun(t, svc, fork)["e2"])
	cleared, err := svc.ClearSessionSuccessor(t.Context(), source, server.SuccessorPlacement{})
	if err != nil {
		t.Fatal(err)
	}
	requireEditRefused(t, promptLedgerRun(t, svc, cleared)["e3"])
	if got := p.content(t); got != "alpha\n" {
		t.Fatalf("content = %q", got)
	}
}
