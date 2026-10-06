package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/authorityconformance"
	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/noopauthority"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type recordingAuthorityEvaluator struct {
	mu       sync.Mutex
	requests []port.AuthorityRequest
	decision port.AuthorityDecision
	err      error
}

func (e *recordingAuthorityEvaluator) AuthorizeTool(_ context.Context, request port.AuthorityRequest) (port.AuthorityDecision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, request)
	return e.decision, e.err
}

func (e *recordingAuthorityEvaluator) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.requests)
}

type recordingDiagnostics struct {
	mu   sync.Mutex
	msgs []string
}

func (d *recordingDiagnostics) Log(_ context.Context, _ port.Level, msg string, _ ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.msgs = append(d.msgs, msg)
}
func (d *recordingDiagnostics) With(...any) port.Diagnostics { return d }
func (d *recordingDiagnostics) contains(want string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, msg := range d.msgs {
		if msg == want {
			return true
		}
	}
	return false
}

type authorityTool struct {
	name string
	ran  atomic.Int32
}

func (t *authorityTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*authorityTool) ReadOnly() bool { return false }
func (t *authorityTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.ran.Add(1)
	return session.NewToolResult(in.ID, "ran"), nil
}

func authoritySession(t *testing.T, names ...string) *session.Session {
	t.Helper()
	sess := newSession(t, session.Limits{})
	if err := sess.RestoreLabels(&session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}, session.Authority{
		CapabilitySet: governance.CapabilitySet{Tools: names, RemainingDelegationDepth: 1},
		Provenance:    "test",
	}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	return sess
}

func TestADR_0233_AuthorityEvaluator_Scenario3_EveryDispatchPathConsultsTheEvaluatorOnce(t *testing.T) {
	t.Run("sequential", func(t *testing.T) {
		first := &authorityTool{name: "First"}
		second := &authorityTool{name: "Second"}
		evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Allowed: true}}
		eng := newEngine(agent.Deps{
			LLM: mockllm.New(mockllm.ToolCallTurn(
				toolCall("one", "First", `{}`), toolCall("two", "Second", `{}`))),
			Catalog: catalogWith(t, first, second), AuthorityEvaluator: evaluator,
		})
		drain(eng.Run(context.Background(), authoritySession(t, "First", "Second"), agent.MemEnv("/ws"), agent.RunRequest{Text: "run"}))
		if got := evaluator.calls(); got != 2 {
			t.Fatalf("evaluator calls = %d, want one per execution (2)", got)
		}
		if first.ran.Load() != 1 || second.ran.Load() != 1 {
			t.Fatalf("tool executions = %d, %d; want 1, 1", first.ran.Load(), second.ran.Load())
		}
	})
}

func TestADR_0233_AuthorityEvaluator_BoundSessionWithoutEvaluatorFailsClosed(t *testing.T) {
	t.Run("nil evaluator retains authority-shaped request and rejects forged calls", func(t *testing.T) {
		read := &authorityTool{name: "Read"}
		omitted := &authorityTool{name: "Write"}
		var request port.LLMRequest
		eng := newEngine(agent.Deps{
			LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) {
				request = got
			})}, mockllm.ToolCallTurn(toolCall("write", "Write", `{"path":"README.md"}`))),
			Catalog: catalogWith(t, read, omitted),
		})

		events := drain(eng.Run(context.Background(), authoritySession(t, "Read"), agent.MemEnv("/ws"), agent.RunRequest{Text: "write"}))
		if _, ok := specByName(request.Tools, "Read"); !ok {
			t.Fatal("bound session did not retain its authority-shaped tool request without an evaluator")
		}
		if _, ok := specByName(request.Tools, "Write"); ok {
			t.Fatal("bound session disclosed a tool absent from its authority without an evaluator")
		}
		if got := omitted.ran.Load(); got != 0 {
			t.Fatalf("omitted tool executions = %d, want 0 when a bound session has no evaluator", got)
		}
		for _, event := range events {
			if event.ToolResult != nil && event.ToolResult.CallID == "write" {
				if !strings.Contains(event.ToolResult.Content, "authority evaluator is not configured") {
					t.Fatalf("missing evaluator result = %q, want a fail-closed configuration error", event.ToolResult.Content)
				}
				return
			}
		}
		t.Fatal("missing fail-closed authority result")
	})

	t.Run("explicit noop evaluator remains valid", func(t *testing.T) {
		read := &authorityTool{name: "Read"}
		var request port.LLMRequest
		eng := newEngine(agent.Deps{
			LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) {
				request = got
			})}, mockllm.ToolCallTurn(toolCall("read", "Read", `{"path":"README.md"}`))),
			Catalog:            catalogWith(t, read),
			AuthorityEvaluator: noopauthority.New(),
		})

		drain(eng.Run(context.Background(), authoritySession(t, "Read"), agent.MemEnv("/ws"), agent.RunRequest{Text: "read"}))
		if _, ok := specByName(request.Tools, "Read"); !ok {
			t.Fatal("explicit noop evaluator did not disclose the capability tool")
		}
		if got := read.ran.Load(); got != 1 {
			t.Fatalf("explicit noop evaluator tool executions = %d, want 1", got)
		}
	})
}

func TestADR_0233_AuthorityEvaluator_OwnerlessBoundSessionUsesEvaluator(t *testing.T) {
	read := &authorityTool{name: "Read"}
	evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Allowed: true}}
	sess := newSession(t, session.Limits{})
	if err := sess.RestoreLabels(nil, session.Authority{
		CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 1},
		Provenance:    "test",
	}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	eng := newEngine(agent.Deps{
		LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("read", "Read", `{"path":"README.md"}`))),
		Catalog:            catalogWith(t, read),
		AuthorityEvaluator: evaluator,
	})

	drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"}))
	if got := evaluator.calls(); got != 1 {
		t.Fatalf("evaluator calls = %d, want 1 for an ownerless bound session", got)
	}
	if got := read.ran.Load(); got != 1 {
		t.Fatalf("tool executions = %d, want 1 for an ownerless bound session", got)
	}
	if got := evaluator.requests[0].Principal; got.OwnerIssuer != "" || got.OwnerSubject != "" {
		t.Fatalf("ownerless request owner = (%q, %q), want absent", got.OwnerIssuer, got.OwnerSubject)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario3_UnavailableEvaluatorIsDistinctFromDenial(t *testing.T) {
	readTool := &authorityTool{name: "Read"}
	unavailable := &recordingAuthorityEvaluator{err: context.DeadlineExceeded}
	diagnostics := &recordingDiagnostics{}
	eng := newEngine(agent.Deps{
		LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("unavailable", "Read", `{"path":"README.md"}`))),
		Catalog:            catalogWith(t, readTool),
		AuthorityEvaluator: unavailable,
		Diagnostics:        diagnostics,
	})
	events := drain(eng.Run(context.Background(), authoritySession(t, "Read"), agent.MemEnv("/ws"), agent.RunRequest{Text: "read"}))
	if readTool.ran.Load() != 0 {
		t.Fatal("tool ran while evaluator was unavailable")
	}
	if !diagnostics.contains("authority evaluator unavailable") {
		t.Fatal("unavailable evaluator did not produce an operator diagnostic")
	}
	for _, event := range events {
		if event.ToolResult != nil && event.ToolResult.CallID == "unavailable" {
			if !strings.Contains(event.ToolResult.Content, "authority evaluator unavailable") || strings.Contains(event.ToolResult.Content, "denied by authority") {
				t.Fatalf("unavailable evaluator result = %q, want a distinct fail-closed message", event.ToolResult.Content)
			}
			return
		}
	}
	t.Fatal("missing unavailable evaluator result")
}

func TestADR_0233_AuthorityEvaluator_Scenario3_AdaptersSatisfyConformanceSuite(t *testing.T) {
	t.Run("noop", func(t *testing.T) {
		authorityconformance.Run(t, func(*testing.T) port.AuthorityEvaluator { return noopauthority.New() })
	})
	t.Run("local", func(t *testing.T) {
		authorityconformance.Run(t, func(*testing.T) port.AuthorityEvaluator { return localauthority.New() })
	})
	// Cedar is intentionally not linked into the engine module. Its opt-in adapter
	// joins this same suite in the later Cedar task.
	t.Run("cedar unavailable in this build", func(t *testing.T) { t.Skip("Cedar adapter is not part of this task") })
}

func TestADR_0233_AuthorityEvaluator_Scenario3_MetaToolIsAuthorizedAgainstItsTarget(t *testing.T) {
	const (
		metaTool      = "CallMcpWithQuery"
		allowedTarget = "mcp__github__create_issue"
		deniedTarget  = "mcp__github__list_issues"
	)

	t.Run("allowed target works while the meta-tool is absent", func(t *testing.T) {
		var request port.LLMRequest
		meta := &authorityTool{name: metaTool}
		eng := newEngine(agent.Deps{
			LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) {
				request = got
			})}, mockllm.ToolCallTurn(toolCall("meta", metaTool, `{"server":"github","tool":"create_issue"}`)), mockllm.TextTurn("done")),
			Catalog:            catalogWith(t, meta),
			AuthorityEvaluator: localauthority.New(),
		})

		drain(eng.Run(context.Background(), authoritySession(t, allowedTarget), agent.MemEnv("/ws"), agent.RunRequest{Text: "call"}))
		if meta.ran.Load() != 1 {
			t.Fatalf("allowed target did not execute through the meta-tool: ran %d times", meta.ran.Load())
		}
		if _, ok := specByName(request.Tools, metaTool); !ok {
			t.Fatal("meta-tool was not disclosed despite a reachable remote target")
		}
	})

	t.Run("denied target is refused while the meta-tool is absent", func(t *testing.T) {
		meta := &authorityTool{name: metaTool}
		eng := newEngine(agent.Deps{
			LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("meta", metaTool, `{"server":"github","tool":"list_issues"}`))),
			Catalog:            catalogWith(t, meta),
			AuthorityEvaluator: localauthority.New(),
		})

		events := drain(eng.Run(context.Background(), authoritySession(t, allowedTarget), agent.MemEnv("/ws"), agent.RunRequest{Text: "call"}))
		if meta.ran.Load() != 0 {
			t.Fatalf("denied target executed through the meta-tool %d times", meta.ran.Load())
		}
		for _, event := range events {
			if event.ToolResult != nil && event.ToolResult.CallID == "meta" {
				if !strings.Contains(event.ToolResult.Content, deniedTarget) {
					t.Fatalf("target denial did not name reconstructed target: %q", event.ToolResult.Content)
				}
				return
			}
		}
		t.Fatal("missing meta tool result")
	})

	t.Run("meta-tool is hidden without a reachable target", func(t *testing.T) {
		var request port.LLMRequest
		eng := newEngine(agent.Deps{
			LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) {
				request = got
			})}, mockllm.TextTurn("done")),
			Catalog: catalogWith(t, &authorityTool{name: metaTool}),
		})
		drain(eng.Run(context.Background(), authoritySession(t, "Read"), agent.MemEnv("/ws"), agent.RunRequest{Text: "call"}))
		if _, ok := specByName(request.Tools, metaTool); ok {
			t.Fatal("meta-tool was disclosed without a reachable remote target")
		}
	})
}

func TestADR_0233_AuthorityEvaluator_Scenario7_ResourceAttributeIsDerivedWithoutRawArguments(t *testing.T) {
	t.Run("normalized workspace target", func(t *testing.T) {
		write := &authorityTool{name: "Write"}
		evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Allowed: true}}
		eng := newEngine(agent.Deps{
			LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("write", "Write", `{"path":"./README.md","content":"credential=must-not-leak"}`))),
			Catalog:            catalogWith(t, write),
			AuthorityEvaluator: evaluator,
		})

		env := agent.MemEnv("/workspace")
		sess := authoritySession(t, "Write")
		if err := sess.Rehome(env.Ref()); err != nil {
			t.Fatal(err)
		}
		drain(eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "write"}))
		if got := write.ran.Load(); got != 1 {
			t.Fatalf("write executions = %d, want 1", got)
		}
		if got := evaluator.calls(); got != 1 {
			t.Fatalf("evaluator calls = %d, want 1", got)
		}
		evaluator.mu.Lock()
		defer evaluator.mu.Unlock()
		request := evaluator.requests[0]
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("marshal authority request: %v", err)
		}
		if strings.Contains(string(encoded), "must-not-leak") {
			t.Fatalf("authority request forwarded raw tool content: %s", encoded)
		}
		if request.ToolName != "Write" {
			t.Fatalf("tool name = %q, want exact tool name Write", request.ToolName)
		}
		if request.Resource == nil {
			t.Fatal("resource descriptor is missing")
		}
		if request.Resource.Kind != port.AuthorityResourceWorkspaceFile || request.Resource.Path != "/workspace/README.md" || request.Resource.Workspace != "/workspace" {
			t.Fatalf("resource = %+v, want normalized workspace file /workspace/README.md", request.Resource)
		}
	})

	for _, args := range []string{
		`{"path":42}`,
		`{"path":"one","path":"two"}`,
	} {
		t.Run("malformed or ambiguous path is fail closed", func(t *testing.T) {
			read := &authorityTool{name: "Read"}
			evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Allowed: true}}
			eng := newEngine(agent.Deps{
				LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("read", "Read", args))),
				Catalog:            catalogWith(t, read),
				AuthorityEvaluator: evaluator,
			})

			drain(eng.Run(context.Background(), authoritySession(t, "Read"), agent.MemEnv("/workspace"), agent.RunRequest{Text: "read"}))
			if got := read.ran.Load(); got != 0 {
				t.Fatalf("read executions = %d, want 0", got)
			}
			if got := evaluator.calls(); got != 0 {
				t.Fatalf("evaluator calls = %d, want 0", got)
			}
		})
	}

	// Copy/Move carry TWO paths (source AND destination): the evaluator must be
	// consulted once per resource, both normalized against the workspace, and
	// BOTH must be authorized for the call to execute.
	t.Run("Copy authorizes both source and destination as distinct resources", func(t *testing.T) {
		cp := &authorityTool{name: "Copy"}
		evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Allowed: true}}
		eng := newEngine(agent.Deps{
			LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("copy", "Copy", `{"source":"a.txt","destination":"b.txt"}`))),
			Catalog:            catalogWith(t, cp),
			AuthorityEvaluator: evaluator,
		})

		env := agent.MemEnv("/workspace")
		sess := authoritySession(t, "Copy")
		if err := sess.Rehome(env.Ref()); err != nil {
			t.Fatal(err)
		}
		drain(eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "copy"}))
		if got := cp.ran.Load(); got != 1 {
			t.Fatalf("copy executions = %d, want 1", got)
		}
		if got := evaluator.calls(); got != 2 {
			t.Fatalf("evaluator calls = %d, want 2 (one per resource)", got)
		}
		evaluator.mu.Lock()
		defer evaluator.mu.Unlock()
		if evaluator.requests[0].Resource == nil || evaluator.requests[0].Resource.Path != "/workspace/a.txt" {
			t.Fatalf("first request resource = %+v, want normalized source /workspace/a.txt", evaluator.requests[0].Resource)
		}
		if evaluator.requests[1].Resource == nil || evaluator.requests[1].Resource.Path != "/workspace/b.txt" {
			t.Fatalf("second request resource = %+v, want normalized destination /workspace/b.txt", evaluator.requests[1].Resource)
		}
	})

	// A destination-only denial must still block the call: authorizing the
	// source is not sufficient when the destination is denied.
	t.Run("Copy is denied when only the destination resource is refused", func(t *testing.T) {
		cp := &authorityTool{name: "Copy"}
		evaluator := &sequencedAuthorityEvaluator{
			decisions: []port.AuthorityDecision{
				{Allowed: true},                         // source: allowed
				{Allowed: false, Reason: "dest denied"}, // destination: denied
			},
		}
		eng := newEngine(agent.Deps{
			LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("copy", "Copy", `{"source":"a.txt","destination":"b.txt"}`))),
			Catalog:            catalogWith(t, cp),
			AuthorityEvaluator: evaluator,
		})

		env := agent.MemEnv("/workspace")
		sess := authoritySession(t, "Copy")
		if err := sess.Rehome(env.Ref()); err != nil {
			t.Fatal(err)
		}
		events := drain(eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "copy"}))
		if got := cp.ran.Load(); got != 0 {
			t.Fatalf("copy executions = %d, want 0 (destination denial must block execution)", got)
		}
		for _, event := range events {
			if event.ToolResult != nil && event.ToolResult.CallID == "copy" {
				if !event.ToolResult.IsError || !strings.Contains(event.ToolResult.Content, "dest denied") {
					t.Fatalf("copy result = %+v, want a denial naming the destination reason", event.ToolResult)
				}
				return
			}
		}
		t.Fatal("missing authority denial result for Copy")
	})
}

type failingAuthorityWorkspace struct {
	tool.Workspace
	err error
}

func (w failingAuthorityWorkspace) AuthorityResourcePath(string) (string, string, error) {
	return "", "", w.err
}

func TestAuthorityResourceResolutionFailureDeniesBeforeEvaluator(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "remote resolver unavailable", err: context.DeadlineExceeded},
		{name: "physical symlink escape", err: errors.New("path is outside workspace")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := &authorityTool{name: "Read"}
			evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Allowed: true}}
			eng := newEngine(agent.Deps{
				LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("read", "Read", `{"path":"escape"}`))),
				Catalog:            catalogWith(t, read),
				AuthorityEvaluator: evaluator,
			})
			base := agent.MemEnv("/workspace")
			env := tool.MustEnvironment(base.Ref(), failingAuthorityWorkspace{Workspace: base.Workspace(), err: tc.err}, base.ReadLedger(), nil)
			sess := authoritySession(t, "Read")
			if err := sess.Rehome(env.Ref()); err != nil {
				t.Fatal(err)
			}
			events := drain(eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "read"}))
			if read.ran.Load() != 0 || evaluator.calls() != 0 {
				t.Fatalf("tool executions=%d evaluator calls=%d, want 0/0", read.ran.Load(), evaluator.calls())
			}
			for _, event := range events {
				if event.ToolResult != nil && event.ToolResult.CallID == "read" {
					if !event.ToolResult.IsError || !strings.Contains(event.ToolResult.Content, "authority resource") {
						t.Fatalf("tool result=%+v, want fail-closed authority resolution error", event.ToolResult)
					}
					return
				}
			}
			t.Fatal("missing fail-closed tool result")
		})
	}
}

// sequencedAuthorityEvaluator returns one decision per call in order, so a test
// can distinguish the source-resource request from the destination-resource
// request in a dual-resource call (Copy/Move).
type sequencedAuthorityEvaluator struct {
	mu        sync.Mutex
	decisions []port.AuthorityDecision
	i         int
}

func (e *sequencedAuthorityEvaluator) AuthorizeTool(_ context.Context, _ port.AuthorityRequest) (port.AuthorityDecision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.i >= len(e.decisions) {
		return port.AuthorityDecision{Allowed: false, Reason: "sequencedAuthorityEvaluator exhausted"}, nil
	}
	d := e.decisions[e.i]
	e.i++
	return d, nil
}

func TestADR_0233_AuthorityEvaluator_Scenario3_DisclosureIsNotLoadBearing(t *testing.T) {
	t.Run("required control tools remain disclosed", func(t *testing.T) {
		var request port.LLMRequest
		eng := newEngine(agent.Deps{
			LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) {
				request = got
			})}, mockllm.TextTurn("done")),
			Catalog:            catalogWith(t, &authorityTool{name: "Read"}),
			AuthorityEvaluator: localauthority.New(),
			ProgressiveTools:   true,
		})
		drain(eng.Run(context.Background(), authoritySession(t, "Read"), agent.MemEnv("/ws"), agent.RunRequest{
			Text:       "work",
			ExtraTools: []tool.Tool{&authorityTool{name: "RunControl"}},
		}))
		if _, ok := specByName(request.Tools, tool.ToolSearchName); !ok {
			t.Fatal("required ToolSearch control tool is missing from the request")
		}
		if _, ok := specByName(request.Tools, "RunControl"); !ok {
			t.Fatal("run-scoped control tool is missing from the request")
		}
	})

	t.Run("request and ToolSearch expose only carried tools", func(t *testing.T) {
		var request port.LLMRequest
		read := &authorityTool{name: "Read"}
		write := &authorityTool{name: "Write"}
		eng := newEngine(agent.Deps{
			LLM: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) {
				request = got
			})}, mockllm.ToolCallTurn(toolCall("search", tool.ToolSearchName, `{"query":""}`)), mockllm.TextTurn("done")),
			Catalog:            catalogWith(t, read, write),
			AuthorityEvaluator: localauthority.New(),
			ProgressiveTools:   true,
		})

		events := drain(eng.Run(context.Background(), authoritySession(t, "Read", tool.ToolSearchName), agent.MemEnv("/ws"), agent.RunRequest{Text: "search"}))
		if _, ok := specByName(request.Tools, "Read"); !ok {
			t.Fatal("carried tool is missing from the request")
		}
		if _, ok := specByName(request.Tools, "Write"); ok {
			t.Fatal("tool absent from carried authority was disclosed")
		}
		if _, ok := specByName(request.Tools, tool.ToolSearchName); !ok {
			t.Fatal("required ToolSearch control tool is missing from the request")
		}
		for _, event := range events {
			if event.ToolResult == nil || event.ToolResult.CallID != "search" {
				continue
			}
			if strings.Contains(event.ToolResult.Content, `"name":"Write"`) {
				t.Fatalf("ToolSearch returned tool absent from carried authority: %s", event.ToolResult.Content)
			}
			if !strings.Contains(event.ToolResult.Content, `"name":"Read"`) {
				t.Fatalf("ToolSearch omitted carried tool: %s", event.ToolResult.Content)
			}
			return
		}
		t.Fatal("missing ToolSearch result")
	})

	t.Run("stale call is independently refused at execute", func(t *testing.T) {
		denied := &authorityTool{name: "Write"}
		evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Reason: "tool is absent from the capability set"}}
		eng := newEngine(agent.Deps{
			LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", "Write", `{"path":"README.md","content":"x"}`))),
			Catalog:            catalogWith(t, denied),
			AuthorityEvaluator: evaluator,
		})

		events := drain(eng.Run(context.Background(), authoritySession(t, "Read"), agent.MemEnv("/ws"), agent.RunRequest{Text: "write"}))
		if got := denied.ran.Load(); got != 0 {
			t.Fatalf("undisclosed stale tool executed %d times despite authority denial", got)
		}
		if got := evaluator.calls(); got != 1 {
			t.Fatalf("evaluator calls = %d, want 1", got)
		}
		for _, event := range events {
			if event.ToolResult != nil && event.ToolResult.CallID == "call-1" {
				if !event.ToolResult.IsError || !strings.Contains(event.ToolResult.Content, "denied by authority") {
					t.Fatalf("authority result = %+v, want distinct authority denial", event.ToolResult)
				}
				return
			}
		}
		t.Fatal("missing authority denial result")
	})
}

func TestADR_0233_AuthorityEvaluator_Scenario3_ResourceReachDerivesFromToolNames(t *testing.T) {
	const server = "resource-only"
	capability := governance.MCPResourceCapability(server)

	t.Run("resource-only server is authorized by its derived capability", func(t *testing.T) {
		resourceTool := &authorityTool{name: "ReadMcpResource"}
		evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Allowed: true}}
		eng := newEngine(agent.Deps{
			LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("resource", "ReadMcpResource", `{"server":"resource-only","uri":"secret://must-not-forward"}`))),
			Catalog:            catalogWith(t, resourceTool),
			AuthorityEvaluator: evaluator,
		})

		drain(eng.Run(context.Background(), authoritySession(t, capability), agent.MemEnv("/ws"), agent.RunRequest{Text: "read resource"}))
		if got := resourceTool.ran.Load(); got != 1 {
			t.Fatalf("resource-only server execution = %d, want 1", got)
		}
		evaluator.mu.Lock()
		defer evaluator.mu.Unlock()
		if len(evaluator.requests) != 1 {
			t.Fatalf("evaluator requests = %d, want 1", len(evaluator.requests))
		}
		request := evaluator.requests[0]
		if request.ToolName != capability || request.Action != "ReadMcpResource" {
			t.Fatalf("request capability/action = %q/%q, want %q/ReadMcpResource", request.ToolName, request.Action, capability)
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("marshal authority request: %v", err)
		}
		if strings.Contains(string(encoded), "secret://must-not-forward") {
			t.Fatalf("authority request forwarded resource URI: %s", encoded)
		}
	})

	t.Run("local evaluator checks the derived capability", func(t *testing.T) {
		resourceTool := &authorityTool{name: "ListMcpResources"}
		eng := newEngine(agent.Deps{
			LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("local", "ListMcpResources", `{"server":"resource-only"}`))),
			Catalog:            catalogWith(t, resourceTool),
			AuthorityEvaluator: localauthority.New(),
		})

		drain(eng.Run(context.Background(), authoritySession(t, capability), agent.MemEnv("/ws"), agent.RunRequest{Text: "list resource"}))
		if got := resourceTool.ran.Load(); got != 1 {
			t.Fatalf("local evaluator resource execution = %d, want 1", got)
		}
	})

	t.Run("aggregate resource operation fails closed before evaluation", func(t *testing.T) {
		resourceTool := &authorityTool{name: "ListMcpResources"}
		evaluator := &recordingAuthorityEvaluator{decision: port.AuthorityDecision{Allowed: true}}
		eng := newEngine(agent.Deps{
			LLM:                mockllm.New(mockllm.ToolCallTurn(toolCall("aggregate", "ListMcpResources", `{}`))),
			Catalog:            catalogWith(t, resourceTool),
			AuthorityEvaluator: evaluator,
		})

		events := drain(eng.Run(context.Background(), authoritySession(t, capability), agent.MemEnv("/ws"), agent.RunRequest{Text: "list resources"}))
		if resourceTool.ran.Load() != 0 || evaluator.calls() != 0 {
			t.Fatalf("aggregate resource call ran=%d evaluations=%d, want 0/0", resourceTool.ran.Load(), evaluator.calls())
		}
		for _, event := range events {
			if event.ToolResult != nil && event.ToolResult.CallID == "aggregate" {
				if !strings.Contains(event.ToolResult.Content, "target is invalid") {
					t.Fatalf("aggregate result = %q, want concrete-server failure", event.ToolResult.Content)
				}
				return
			}
		}
		t.Fatal("missing aggregate resource denial")
	})
}
