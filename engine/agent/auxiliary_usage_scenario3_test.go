package agent

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type auxiliaryRecordingDiagnostics struct {
	mu   sync.Mutex
	msgs []string
}

func (d *auxiliaryRecordingDiagnostics) Log(_ context.Context, _ port.Level, msg string, _ ...any) {
	d.mu.Lock()
	d.msgs = append(d.msgs, msg)
	d.mu.Unlock()
}
func (d *auxiliaryRecordingDiagnostics) With(...any) port.Diagnostics { return d }

func (d *auxiliaryRecordingDiagnostics) count(msg string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for _, got := range d.msgs {
		if got == msg {
			count++
		}
	}
	return count
}

type blockingParallelJudgeUsageTool struct {
	entered  chan<- struct{}
	release  <-chan struct{}
	usage    session.AuxiliaryUsage
	readOnly bool
	err      error
	retained chan<- auxiliaryUsageReporter
}

func (*blockingParallelJudgeUsageTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "ParallelUsage", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (t *blockingParallelJudgeUsageTool) ReadOnly() bool { return t.readOnly }
func (*blockingParallelJudgeUsageTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, errors.New("parent capabilities were not supplied")
}
func (t *blockingParallelJudgeUsageTool) ExecuteWithParent(_ context.Context, call session.ToolCall, _ tool.Environment, _ func(session.Event), caps parentCaps) (session.ToolResult, error) {
	caps.recordAuxiliaryUsage.reportAuxiliaryUsage(t.usage)
	if t.retained != nil {
		t.retained <- caps.recordAuxiliaryUsage
	}
	t.entered <- struct{}{}
	<-t.release
	return session.NewToolResult(call.ID, "done"), t.err
}

type parentCapsProducerTool struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (*parentCapsProducerTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "ParentCapsProducer", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*parentCapsProducerTool) ReadOnly() bool { return true }
func (*parentCapsProducerTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, errors.New("parent capabilities were not supplied")
}
func (t *parentCapsProducerTool) ExecuteWithParent(ctx context.Context, call session.ToolCall, _ tool.Environment, _ func(session.Event), caps parentCaps) (session.ToolResult, error) {
	if routed := caps.routeConfigured(ctx, "route child"); !routed.ok {
		return session.NewToolError(call.ID, "router failed"), nil
	}
	if reviewed := caps.adjudicate(shellAsk("git status"), true); !reviewed.allowed {
		return session.NewToolError(call.ID, "reviewer failed"), nil
	}
	t.entered <- struct{}{}
	<-t.release
	return session.NewToolResult(call.ID, "done"), nil
}

func TestParentCapsRouterAndReviewerUseExecutionLocalReporter(t *testing.T) {
	routerUsage := session.Usage{InputTokens: 3}
	reviewerUsage := session.Usage{InputTokens: 4}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	catalog := tool.NewCatalog()
	catalog.MustRegister(&parentCapsProducerTool{entered: entered, release: release})
	call := session.NewToolCall("parent-caps", "ParentCapsProducer", []byte(`{}`))
	engine := NewEngine(Deps{
		LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")), Catalog: catalog, Policy: allowAllInt(),
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			return ModelRouteResult{Category: "large", Model: "large-model", OK: true, Usage: session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
				session.UsageKindRouter: {Models: map[string]session.Usage{"provider/router": routerUsage}},
			}}}
		}},
		ChildAskReviewer: fixedUsageReviewer{usage: session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
			session.UsageKindAskReviewer: {Models: map[string]session.Usage{"provider/reviewer": reviewerUsage}},
		}}},
	})
	env := memEnv("/ws")
	sess := session.New("parent-caps-reporter", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	run := engine.Run(t.Context(), sess, env, RunRequest{Text: "run producers"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range run.Events() {
		}
	}()
	<-entered
	run.auxiliaryUsageMu.Lock()
	pending := len(run.auxiliaryUsagePending.Buckets)
	run.auxiliaryUsageMu.Unlock()
	if pending != 0 {
		t.Fatalf("router/reviewer bypassed execution-local reporter into terminal queue: %d buckets", pending)
	}
	if got := sess.UsageFor(session.UsageKindRouter); got != (session.Usage{}) {
		t.Fatalf("worker mutated router usage before dispatcher drain: %+v", got)
	}
	if got := sess.UsageFor(session.UsageKindAskReviewer); got != (session.Usage{}) {
		t.Fatalf("worker mutated ask-reviewer usage before dispatcher drain: %+v", got)
	}
	close(release)
	<-done
	if got := sess.UsageFor(session.UsageKindRouter); got != routerUsage {
		t.Fatalf("router usage = %+v, want %+v", got, routerUsage)
	}
	if got := sess.UsageFor(session.UsageKindAskReviewer); got != reviewerUsage {
		t.Fatalf("reviewer usage = %+v, want %+v", got, reviewerUsage)
	}
}

func TestParallelJudgeUsageStaysPrivateUntilDispatcherDrain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readOnly bool
		cancel   bool
		toolErr  error
	}{
		{name: "serial_completed"},
		{name: "serial_tool_error", toolErr: errors.New("tool failed after judge")},
		{name: "read_batch_completed", readOnly: true},
		{name: "read_batch_cancelled", readOnly: true, cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := session.Usage{InputTokens: 8, OutputTokens: 2}
			returned := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
				session.UsageKindParallelJudge: {Models: map[string]session.Usage{"provider-p/judge-model": usage}},
			}}
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			catalog := tool.NewCatalog()
			catalog.MustRegister(&blockingParallelJudgeUsageTool{entered: entered, release: release, usage: returned, readOnly: tc.readOnly, err: tc.toolErr})
			call := session.NewToolCall("parallel-usage", "ParallelUsage", []byte(`{}`))
			store := memstore.New()
			engine := NewEngine(Deps{
				LLM:     mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")),
				Catalog: catalog, Policy: allowAllInt(), Store: store,
			})
			env := memEnv("/ws")
			sess := session.New(session.SessionID("parallel-usage-"+tc.name), session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			run := engine.Run(ctx, sess, env, RunRequest{Text: "run parallel judge"})
			done := make(chan struct{})
			var events []session.Event
			go func() {
				defer close(done)
				for event := range run.Events() {
					events = append(events, event)
				}
			}()

			<-entered
			if got := sess.UsageFor(session.UsageKindParallelJudge); got != (session.Usage{}) {
				t.Fatalf("worker mutated parent usage before dispatcher drain: %+v", got)
			}
			if tc.cancel {
				cancel()
			}
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("run did not finish after tool release")
			}
			if tc.cancel {
				sawCancel := false
				for _, event := range events {
					sawCancel = sawCancel || event.Type == session.EvResult && event.Result != nil && event.Result.Stop == session.StopCancelled
				}
				if !sawCancel {
					t.Fatal("cancel row emitted no StopCancelled result")
				}
			}
			if tc.toolErr != nil {
				found := false
				for _, event := range events {
					found = found || event.Type == session.EvToolResult && event.ToolResult != nil && event.ToolResult.IsError
				}
				if !found {
					t.Fatal("tool-error row emitted no error ToolResult")
				}
			}
			bucket := sess.TokenUsageSnapshot()[session.UsageKindParallelJudge]
			if bucket.Total != usage || bucket.Models["provider-p/judge-model"] != usage || len(bucket.Models) != 1 {
				t.Fatalf("drained parallel judge usage = %#v, want exact usage once", bucket)
			}
			loaded, err := store.Load(t.Context(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := loaded.TokenUsageSnapshot()[session.UsageKindParallelJudge]; !reflect.DeepEqual(got, bucket) {
				t.Fatalf("persisted parallel judge usage = %#v, want %#v", got, bucket)
			}
		})
	}
}

type auxiliaryDrainRecorder struct {
	sess      *session.Session
	calls     []session.ToolCallID
	snapshots []session.Usage
}

func (r *auxiliaryDrainRecorder) ToolCall(_ session.SessionID, call session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
	r.calls = append(r.calls, call.ID)
	r.snapshots = append(r.snapshots, r.sess.UsageFor(session.UsageKindParallelJudge))
}

func TestParallelJudgeUsageReadBatchDrainsInCallOrder(t *testing.T) {
	usage := session.Usage{InputTokens: 5}
	returned := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindParallelJudge: {Models: map[string]session.Usage{"provider-p/judge-model": usage}},
	}}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	catalog := tool.NewCatalog()
	catalog.MustRegister(&blockingParallelJudgeUsageTool{entered: entered, release: release, usage: returned, readOnly: true})
	calls := []session.ToolCall{
		session.NewToolCall("first", "ParallelUsage", []byte(`{}`)),
		session.NewToolCall("second", "ParallelUsage", []byte(`{}`)),
	}
	env := memEnv("/ws")
	sess := session.New("parallel-ordered", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	recorder := &auxiliaryDrainRecorder{sess: sess}
	engine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("done")),
		Catalog: catalog, Policy: allowAllInt(), ToolCallRecorder: recorder,
	})
	run := engine.Run(t.Context(), sess, env, RunRequest{Text: "run parallel judges"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range run.Events() {
		}
	}()
	<-entered
	<-entered
	if got := sess.UsageFor(session.UsageKindParallelJudge); got != (session.Usage{}) {
		t.Fatalf("parallel workers mutated parent before ordered drain: %+v", got)
	}
	close(release)
	<-done

	if !reflect.DeepEqual(recorder.calls, []session.ToolCallID{"first", "second"}) {
		t.Fatalf("drain order = %v, want first then second", recorder.calls)
	}
	if len(recorder.snapshots) != 2 || recorder.snapshots[0] != usage || recorder.snapshots[1] != usage.Add(usage) {
		t.Fatalf("ordered usage snapshots = %+v, want one then two reports", recorder.snapshots)
	}
}

type blockingPostToolHook struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (h blockingPostToolHook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePostToolUse {
		h.entered <- struct{}{}
		<-h.release
	}
	return governance.HookOutcome{}, nil
}

func TestParentCapsAuxiliaryCallbackQueuesWhileOwnershipActive(t *testing.T) {
	askUsage := session.Usage{InputTokens: 6, OutputTokens: 1}
	returned := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindAskReviewer: {Models: map[string]session.Usage{"provider-a/reviewer": askUsage}},
	}}
	toolRelease := make(chan struct{})
	close(toolRelease)
	toolEntered := make(chan struct{}, 1)
	retained := make(chan auxiliaryUsageReporter, 1)
	hookEntered := make(chan struct{}, 1)
	hookRelease := make(chan struct{})
	catalog := tool.NewCatalog()
	catalog.MustRegister(&blockingParallelJudgeUsageTool{entered: toolEntered, release: toolRelease, retained: retained})
	call := session.NewToolCall("background-review", "ParallelUsage", []byte(`{}`))
	engine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")),
		Catalog: catalog, Policy: allowAllInt(), Hooks: blockingPostToolHook{entered: hookEntered, release: hookRelease},
	})
	env := memEnv("/ws")
	sess := session.New("background-review", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	run := engine.Run(t.Context(), sess, env, RunRequest{Text: "start background review"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range run.Events() {
		}
	}()
	<-toolEntered
	callback := <-retained
	<-hookEntered
	callback.reportAuxiliaryUsage(returned)
	if got := sess.UsageFor(session.UsageKindAskReviewer); got != (session.Usage{}) {
		t.Fatalf("late active callback mutated parent before dispatcher drain: %+v", got)
	}
	close(hookRelease)
	<-done
	bucket := sess.TokenUsageSnapshot()[session.UsageKindAskReviewer]
	if bucket.Total != askUsage || bucket.Models["provider-a/reviewer"] != askUsage || len(bucket.Models) != 1 {
		t.Fatalf("queued background ask-reviewer usage = %#v, want exact attribution once", bucket)
	}
}

func TestParentCapsAuxiliaryCallbackDropsAfterOwnershipCloses(t *testing.T) {
	usage := session.Usage{InputTokens: 4, OutputTokens: 1}
	returned := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindParallelJudge: {Models: map[string]session.Usage{"provider-p/judge-model": usage}},
	}}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	retained := make(chan auxiliaryUsageReporter, 1)
	diag := &auxiliaryRecordingDiagnostics{}
	catalog := tool.NewCatalog()
	catalog.MustRegister(&blockingParallelJudgeUsageTool{entered: entered, release: release, usage: returned, retained: retained})
	call := session.NewToolCall("parallel-retained", "ParallelUsage", []byte(`{}`))
	engine := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")),
		Catalog: catalog, Policy: allowAllInt(), Diagnostics: diag,
	})
	env := memEnv("/ws")
	sess := session.New("parallel-retained", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	run := engine.Run(t.Context(), sess, env, RunRequest{Text: "run parallel judge"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range run.Events() {
		}
	}()
	<-entered
	callback := <-retained
	close(release)
	<-done

	callback.reportAuxiliaryUsage(returned)
	if got := sess.UsageFor(session.UsageKindParallelJudge); got != usage {
		t.Fatalf("retained callback changed closed parent usage: %+v", got)
	}
	if got := diag.count("late auxiliary usage dropped after parent run ended"); got != 1 {
		t.Fatalf("late callback diagnostics = %d, want one bounded line", got)
	}
}

type blockingAuxiliaryProvider struct {
	started chan<- struct{}
	once    sync.Once
}

func (*blockingAuxiliaryProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *blockingAuxiliaryProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		p.once.Do(func() { p.started <- struct{}{} })
		<-ctx.Done()
		yield(port.Chunk{}, ctx.Err())
	}, nil
}

type blockingCompletionObserver struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (o blockingCompletionObserver) Observe(context.Context, learning.Trajectory) error {
	o.entered <- struct{}{}
	<-o.release
	return nil
}

func TestCompletionObserverPersistsPendingParentCapsUsage(t *testing.T) {
	usage := session.Usage{InputTokens: 5, OutputTokens: 1}
	returned := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindParallelJudge: {Models: map[string]session.Usage{"provider/judge": usage}},
	}}
	toolRelease := make(chan struct{})
	close(toolRelease)
	toolEntered := make(chan struct{}, 1)
	retained := make(chan auxiliaryUsageReporter, 1)
	observerEntered := make(chan struct{}, 1)
	observerRelease := make(chan struct{})
	store := memstore.New()
	catalog := tool.NewCatalog()
	catalog.MustRegister(&blockingParallelJudgeUsageTool{entered: toolEntered, release: toolRelease, retained: retained})
	call := session.NewToolCall("observer-pending", "ParallelUsage", []byte(`{}`))
	engine := NewEngine(Deps{
		LLM: mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")), Catalog: catalog, Policy: allowAllInt(), Store: store,
		LearningMode: learning.Auto, LearningObserver: blockingCompletionObserver{entered: observerEntered, release: observerRelease},
	})
	env := memEnv("/ws")
	sess := session.New("observer-pending", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	run := engine.Run(t.Context(), sess, env, RunRequest{Text: "complete"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range run.Events() {
		}
	}()
	<-toolEntered
	callback := <-retained
	<-observerEntered
	callback.reportAuxiliaryUsage(returned)
	close(observerRelease)
	<-done
	loaded, err := store.Load(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	bucket := loaded.TokenUsageSnapshot()[session.UsageKindParallelJudge]
	if bucket.Total != usage || bucket.Models["provider/judge"] != usage || len(bucket.Models) != 1 {
		t.Fatalf("persisted observer-time usage = %#v, want exact pending usage", bucket)
	}
}

func TestParallelBranchRouterUsageStaysPrivateUntilDispatcherDrain(t *testing.T) {
	usage := session.Usage{InputTokens: 7, OutputTokens: 2}
	started := make(chan struct{})
	child := NewEngine(Deps{
		LLM: &blockingAuxiliaryProvider{started: started}, Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "routed-model",
	})
	parallel := NewParallelTool(child, auxiliaryUsageForker{}, WithParallelEngineFactory(func(model string) (*Engine, bool) {
		return child, model == "routed-model"
	})).(*ParallelTool)
	catalog := tool.NewCatalog()
	catalog.MustRegister(parallel)
	router := &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
		return ModelRouteResult{
			Category: "large", Model: "routed-model", OK: true,
			Usage: session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
				session.UsageKindRouter: {Models: map[string]session.Usage{"provider-r/classifier": usage}},
			}},
		}
	}}
	call := session.NewToolCall("parallel-route", parallelToolName, []byte(`{"tasks":["one"]}`))
	owner := NewEngine(Deps{
		LLM: mockllm.New(mockllm.ToolCallTurn(call)), Catalog: catalog, Policy: allowAllInt(), SubagentModelRouter: router,
	})
	sess := session.New("parallel-route-parent", session.ModeDefault, judgeEnvironment.Ref(), session.Limits{}, time.Unix(0, 0))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run := owner.Run(ctx, sess, judgeEnvironment, RunRequest{Text: "route branch"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range run.Events() {
		}
	}()
	<-started
	if got := sess.UsageFor(session.UsageKindRouter); got != (session.Usage{}) {
		t.Fatalf("Parallel branch router mutated parent before drain: %+v", got)
	}
	cancel()
	<-done
	bucket := sess.TokenUsageSnapshot()[session.UsageKindRouter]
	if bucket.Total != usage || bucket.Models["provider-r/classifier"] != usage || len(bucket.Models) != 1 {
		t.Fatalf("Parallel branch router usage = %#v, want exact routed attribution once", bucket)
	}
}

func TestAuxiliaryUsageRealProducerExcludesSensitiveInputsAndOutputs(t *testing.T) {
	const (
		prompt = "private guardrail prompt with request-secret-123 and Bearer accounting-secret"
		output = "private model output"
	)
	usage := session.Usage{InputTokens: 7, OutputTokens: 3}
	checker := NewEngine(Deps{
		LLM: mockllm.New(mockllm.ChunksTurn(
			mockllm.TextChunk(output),
			mockllm.UsageChunk(usage),
			mockllm.DoneChunk(session.StopEndTurn),
		)),
		Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "server-model",
		ProviderModel: session.ProviderModelID{ProviderID: "server-provider", ModelID: "server-model"},
	})
	gotOutput, record, err := RunGuardrailCheck(t.Context(), checker, prompt)
	if err != nil || gotOutput != output {
		t.Fatalf("RunGuardrailCheck output = %q, err=%v", gotOutput, err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{prompt, output, "request-secret-123", "Bearer accounting-secret", "client-selected-provider", "client-selected-model"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("auxiliary usage retained sensitive producer data %q: %s", forbidden, raw)
		}
	}
	bucket := record.Buckets[session.UsageKindGuardrail]
	if bucket.Total != usage || bucket.Models["server-provider/server-model"] != usage || len(bucket.Models) != 1 {
		t.Fatalf("real producer accounting = %#v, want tokens plus selected server identity only", bucket)
	}
}

func TestAuxiliaryTokenUsage_Scenario3_ComposedRouterRecordsSelectedProviderModel(t *testing.T) {
	usage := session.Usage{InputTokens: 11, OutputTokens: 4}
	classifier := NewEngine(Deps{
		LLM: mockllm.New(mockllm.ChunksTurn(
			mockllm.TextChunk(`{"category":"large"}`),
			mockllm.UsageChunk(usage),
			mockllm.DoneChunk(session.StopEndTurn),
		)),
		Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "classifier-model",
		ProviderModel: session.ProviderModelID{ProviderID: "classifier-provider", ModelID: "classifier-model"},
	})
	router := &SubagentModelRouter{Backend: "llm", ClassifierModel: "classifier-model"}
	router.Route = func(ctx context.Context, prompt string) ModelRouteResult {
		category, returned, reason, ok := RunModelRouter(ctx, classifier, ModelRouteRequest{
			TaskPrompt: prompt,
			Categories: []ModelRouteCategory{{Name: "small", Description: "small work"}, {Name: "large", Description: "large work"}},
			Default:    "small",
		})
		return ModelRouteResult{Category: category, Model: "large-model", Usage: returned, Reason: reason, OK: ok}
	}
	owner := NewEngine(Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), SubagentModelRouter: router})
	parent := runningAuxiliaryParent(t, "composed-router-parent")
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry(), diag: port.NopDiagnostics{}, auxiliaryUsageActive: true}

	if got := owner.parentCaps(run, parent, 0).routeDecision(t.Context(), "classify this task"); !got.ok {
		t.Fatalf("composed route failed: %+v", got)
	}
	if got := parent.UsageFor(session.UsageKindRouter); got != (session.Usage{}) {
		t.Fatalf("router mutated parent before owner drain: %+v", got)
	}
	run.drainPendingAuxiliaryUsage(parent)
	bucket := parent.TokenUsageSnapshot()[session.UsageKindRouter]
	if bucket.Total != usage || bucket.Models["classifier-provider/classifier-model"] != usage || len(bucket.Models) != 1 {
		t.Fatalf("composed router attribution = %#v, want selected classifier provider/model", bucket)
	}
}

func TestRemapAuxiliaryUsageMissingPurposeRetainsSpend(t *testing.T) {
	spend := session.Usage{InputTokens: 9, OutputTokens: 4}
	in := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		"": {Models: map[string]session.Usage{"": spend, "provider/model": spend}},
	}}
	diag := &auxiliaryRecordingDiagnostics{}
	out := RemapAuxiliaryUsage(t.Context(), diag, session.UsageKindRouter, in)
	bucket := out.Buckets[session.UsageKindRouter]
	if bucket.Total != spend.Add(spend) || bucket.Models["unknown"] != spend || bucket.Models["provider/model"] != spend {
		t.Fatalf("remapped bucket = %#v", bucket)
	}
	if got := diag.count("auxiliary usage missing purpose remapped"); got != 1 {
		t.Fatalf("missing-purpose warnings = %d, want one for the result", got)
	}
	merged := in.Merge(session.AuxiliaryUsage{})
	if got := RemapAuxiliaryUsage(t.Context(), nil, session.UsageKindRouter, merged).Buckets[session.UsageKindRouter]; got.Total != bucket.Total || !reflect.DeepEqual(got.Models, bucket.Models) {
		t.Fatalf("remap after merge = %#v, want %#v", got, bucket)
	}
}

func TestAuxiliaryTokenUsage_Scenario3_RouterDoesNotFoldIntoMain(t *testing.T) {
	usage := session.Usage{InputTokens: 7, OutputTokens: 3}
	injected := session.Usage{InputTokens: 2, OutputTokens: 1}
	aux := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindMain:   {Models: map[string]session.Usage{"provider-r/classifier": injected}},
		session.UsageKindRouter: {Models: map[string]session.Usage{"provider-r/classifier": usage}},
	}}
	engine := NewEngine(Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "main",
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			return ModelRouteResult{Category: "large", Model: "big", Usage: aux, OK: true}
		}},
	})
	parent := runningAuxiliaryParent(t, "router-parent")
	diag := &auxiliaryRecordingDiagnostics{}
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry(), diag: diag, auxiliaryUsageActive: true}

	got := engine.parentCaps(run, parent, 0).routeDecision(t.Context(), "classify")
	if !got.ok {
		t.Fatalf("routeDecision ok = false, reason %q", got.reason)
	}
	run.drainPendingAuxiliaryUsage(parent)
	if main := parent.UsageFor(session.UsageKindMain); main != (session.Usage{}) {
		t.Fatalf("main usage = %+v, want zero", main)
	}
	wantRouter := usage.Add(injected)
	if router := parent.UsageFor(session.UsageKindRouter); router != wantRouter {
		t.Fatalf("router usage = %+v, want remapped total %+v", router, wantRouter)
	}
	bucket := parent.TokenUsageSnapshot()[session.UsageKindRouter]
	if got := bucket.Models["provider-r/classifier"]; got != wantRouter {
		t.Fatalf("router attribution = %+v, want %+v", bucket.Models, wantRouter)
	}
	diag.mu.Lock()
	defer diag.mu.Unlock()
	normalized := 0
	for _, msg := range diag.msgs {
		if msg == "auxiliary usage result normalized" {
			normalized++
		}
	}
	if normalized != 1 {
		t.Fatalf("normalization diagnostics = %v, want one bounded normalization line", diag.msgs)
	}
}

type fixedUsageReviewer struct {
	usage session.AuxiliaryUsage
}

func (r fixedUsageReviewer) Review(context.Context, ChildAskReviewRequest) (ChildAskReview, session.AuxiliaryUsage, error) {
	return ChildAskReview{Allowed: true}, r.usage, nil
}

func TestAuxiliaryTokenUsage_Scenario3_SafetyChecksRecordParentUsage(t *testing.T) {
	reviewerUsage := session.Usage{InputTokens: 4, OutputTokens: 1}
	reviewer := NewEngineAskReviewer(
		reviewerEngine(mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
			{Kind: port.ChunkText, Text: `{"allow":true,"reason":"safe"}`},
			{Kind: port.ChunkUsage, Usage: &reviewerUsage},
			{Kind: port.ChunkDone},
		}})),
	)
	review, reviewUsage, err := reviewer.Review(t.Context(), ChildAskReviewRequest{Ask: shellAsk("git status"), Isolated: true})
	if err != nil || !review.Allowed {
		t.Fatalf("Review() = %+v, %v", review, err)
	}

	guardrailUsage := session.Usage{InputTokens: 6, OutputTokens: 2}
	checker := reviewerEngine(mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkText, Text: `{"safe":true}`},
		{Kind: port.ChunkUsage, Usage: &guardrailUsage},
		{Kind: port.ChunkDone},
	}}))
	_, checkedUsage, err := RunGuardrailCheck(t.Context(), checker, "check")
	if err != nil {
		t.Fatal(err)
	}
	if got := checkedUsage.Buckets[session.UsageKindGuardrail].Total; got != guardrailUsage {
		t.Fatalf("guardrail usage = %+v, want %+v", got, guardrailUsage)
	}

	parent := runningAuxiliaryParent(t, "safety-parent")
	reviewUsage = reviewUsage.Merge(session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindMain: {Models: map[string]session.Usage{"provider-a/reviewer": {InputTokens: 2}}},
	}})
	reviewEngine := NewEngine(Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "main",
		ChildAskReviewer: fixedUsageReviewer{usage: reviewUsage},
	})
	reviewRun := &Run{
		askReview: &askReviewBreaker{max: DefaultAskReviewMaxDenies}, hardAbort: make(chan struct{}),
		diag: port.NopDiagnostics{}, children: newChildRunRegistry(), auxiliaryUsageActive: true,
	}
	if outcome := reviewEngine.parentCaps(reviewRun, parent, 0).adjudicate(shellAsk("git status"), true); !outcome.allowed {
		t.Fatalf("review outcome = %+v, want allowed", outcome)
	}
	if got := parent.UsageFor(session.UsageKindAskReviewer); got != (session.Usage{}) {
		t.Fatalf("ask reviewer mutated parent before owner drain: %+v", got)
	}
	reviewRun.drainPendingAuxiliaryUsage(parent)
	wantReview := reviewerUsage.Add(session.Usage{InputTokens: 2})
	if got := parent.UsageFor(session.UsageKindAskReviewer); got != wantReview {
		t.Fatalf("ask reviewer usage = %+v, want remapped %+v", got, wantReview)
	}
	if got := parent.UsageFor(session.UsageKindMain); got != (session.Usage{}) {
		t.Fatalf("main usage = %+v, want zero", got)
	}
}

type blockingUsageReviewer struct {
	started chan struct{}
	release <-chan struct{}
	usage   session.AuxiliaryUsage
}

func (r blockingUsageReviewer) Review(context.Context, ChildAskReviewRequest) (ChildAskReview, session.AuxiliaryUsage, error) {
	r.started <- struct{}{}
	<-r.release
	return ChildAskReview{Allowed: true}, r.usage, nil
}

func TestChildAskReviewerUsageQueuesUntilParentOwnerDrain(t *testing.T) {
	usage := session.Usage{InputTokens: 4, OutputTokens: 1}
	parent := runningAuxiliaryParent(t, "queued-review-parent")
	reviewerStarted := make(chan struct{}, 1)
	releaseReviewer := make(chan struct{})
	engine := NewEngine(Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(),
		ChildAskReviewer: blockingUsageReviewer{started: reviewerStarted, release: releaseReviewer,
			usage: auxiliaryUsage(session.UsageKindAskReviewer,
				session.ProviderModelID{ProviderID: "provider-a", ModelID: "reviewer"}, usage)},
	})
	run := &Run{
		askReview: &askReviewBreaker{max: DefaultAskReviewMaxDenies}, hardAbort: make(chan struct{}),
		diag: port.NopDiagnostics{}, children: newChildRunRegistry(), auxiliaryUsageActive: true,
	}
	caps := engine.parentCaps(run, parent, 0)
	outcomeReady := make(chan askReviewOutcome, 1)
	go func() { outcomeReady <- caps.adjudicate(shellAsk("git status"), true) }()
	<-reviewerStarted
	close(releaseReviewer)
	if outcome := <-outcomeReady; !outcome.allowed {
		t.Fatalf("review outcome = %+v, want allowed", outcome)
	}
	if got := parent.UsageFor(session.UsageKindAskReviewer); got != (session.Usage{}) {
		t.Fatalf("ask reviewer mutated parent before owner drain: %+v", got)
	}
	run.drainPendingAuxiliaryUsage(parent)
	bucket := parent.TokenUsageSnapshot()[session.UsageKindAskReviewer]
	if bucket.Total != usage || bucket.Models["provider-a/reviewer"] != usage || len(bucket.Models) != 1 {
		t.Fatalf("queued ask-reviewer usage = %#v, want exact attribution once", bucket)
	}
}

func TestChildAskReviewerUsageDropsAfterParentRunOwnershipEnds(t *testing.T) {
	usage := session.Usage{InputTokens: 4, OutputTokens: 1}
	diag := &auxiliaryRecordingDiagnostics{}
	parent := runningAuxiliaryParent(t, "late-review-parent")
	reviewerStarted := make(chan struct{}, 1)
	releaseReviewer := make(chan struct{})
	engine := NewEngine(Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Diagnostics: diag,
		ChildAskReviewer: blockingUsageReviewer{started: reviewerStarted, release: releaseReviewer,
			usage: auxiliaryUsage(session.UsageKindAskReviewer,
				session.ProviderModelID{ProviderID: "provider-a", ModelID: "reviewer"}, usage)},
	})
	capsReady := make(chan parentCaps, 1)
	releaseRun := make(chan struct{})
	prepared := engine.prepareRun(t.Context(), parent, RunRequest{}, session.Usage{}, func(_ context.Context, run *Run) {
		capsReady <- engine.parentCaps(run, parent, 0)
		<-releaseRun
	})
	run, transition := prepared.Start()
	if transition != PreparedRunStarted {
		t.Fatalf("Start transition = %q", transition)
	}
	caps := <-capsReady
	outcomeReady := make(chan askReviewOutcome, 1)
	go func() { outcomeReady <- caps.adjudicate(shellAsk("git status"), true) }()
	<-reviewerStarted

	// End the run-owned mutation capability while the reviewer is still blocked,
	// then let its detached result arrive late.
	close(releaseRun)
	for range run.Events() {
	}
	close(releaseReviewer)
	if outcome := <-outcomeReady; !outcome.allowed {
		t.Fatalf("review outcome = %+v, want allowed", outcome)
	}
	if got := parent.UsageFor(session.UsageKindAskReviewer); got != (session.Usage{}) {
		t.Fatalf("late reviews mutated parent usage = %+v, want zero", got)
	}
	if got := diag.count("late auxiliary usage dropped after parent run ended"); got != 1 {
		t.Fatalf("late-drop diagnostics = %d, want one bounded line", got)
	}
}

func TestEngineJudgeReturnsParallelUsage(t *testing.T) {
	usage := session.Usage{InputTokens: 8, OutputTokens: 2}
	judge := NewEngineJudge(NewEngine(Deps{
		LLM: mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
			{Kind: port.ChunkText, Text: `{"winner":1,"rationale":"best"}`},
			{Kind: port.ChunkUsage, Usage: &usage},
			{Kind: port.ChunkDone},
		}}), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "inherited-model",
		ProviderModel: session.ProviderModelID{ProviderID: "provider-p", ModelID: "inherited-model"},
	}))
	winner, _, gotUsage, err := judge.Judge(t.Context(), []BranchSummary{{Label: "one", Summary: "one"}, {Label: "two", Summary: "two"}}, "best")
	if err != nil || winner != 0 {
		t.Fatalf("Judge() winner=%d err=%v", winner, err)
	}
	bucket := gotUsage.Buckets[session.UsageKindParallelJudge]
	if got := bucket.Models["provider-p/inherited-model"]; got != usage {
		t.Fatalf("parallel judge attribution = %+v, want %+v", bucket, usage)
	}
}

type zeroUsageReviewer struct{}

func (zeroUsageReviewer) Review(context.Context, ChildAskReviewRequest) (ChildAskReview, session.AuxiliaryUsage, error) {
	return ChildAskReview{Allowed: true}, session.AuxiliaryUsage{}, nil
}

type zeroUsageJudge struct{}

func (zeroUsageJudge) Judge(context.Context, []BranchSummary, string) (int, string, session.AuxiliaryUsage, error) {
	return 0, "custom", session.AuxiliaryUsage{}, nil
}

func TestUtilityEngineUsageReturnsActualTier4CompactionToCallerOwner(t *testing.T) {
	mainUsage := session.Usage{InputTokens: 7, OutputTokens: 2}
	compactionUsage := session.Usage{InputTokens: 3, OutputTokens: 1}
	provider := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("## Goal\nretain the utility decision"),
			mockllm.UsageChunk(compactionUsage),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk(`{"category":"large"}`),
			mockllm.UsageChunk(mainUsage),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	compactionIdentity := session.ProviderModelID{ProviderID: "provider-c", ModelID: "summary"}
	utilityIdentity := session.ProviderModelID{ProviderID: "provider-u", ModelID: "utility"}
	counter := HeuristicTokenCounter{CharsPerToken: 1}
	engine := NewEngine(Deps{
		LLM: provider, Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: utilityIdentity.ModelID,
		ProviderModel: utilityIdentity, ContextWindow: func() int { return 1_000 }, CompactionRatio: 0.8,
		TokenCounter: counter,
		Compactor:    CascadeCompactor{Counter: counter, LLM: provider, Model: compactionIdentity.ModelID, ProviderModel: compactionIdentity},
	})
	utility := session.New("utility-compaction", session.ModeDefault, judgeEnvironment.Ref(), session.Limits{MaxTurns: 1}, time.Unix(1, 0))
	history := make([]session.Message, 0, 20)
	for i := range 10 {
		history = append(history,
			session.NewUserMessage(strings.Repeat("old utility request ", 12)),
			session.NewAssistantMessage(strings.Repeat("old utility answer ", 12)+string(rune('a'+i)), "", nil),
		)
	}
	if err := utility.SeedHistory(history); err != nil {
		t.Fatal(err)
	}
	for range engine.Run(t.Context(), utility, judgeEnvironment, RunRequest{Text: "classify"}).Events() {
	}
	if calls := provider.Calls(); calls != 2 {
		t.Fatalf("provider calls = %d, want tier-4 summary plus utility call", calls)
	}
	if got := utility.UsageFor(session.UsageKindCompaction); got != compactionUsage {
		t.Fatalf("utility tier-4 usage = %+v, want %+v", got, compactionUsage)
	}

	returned := UtilityEngineUsage(session.UsageKindRouter, utilityIdentity, utility)
	owned := RemapAuxiliaryUsage(t.Context(), port.NopDiagnostics{}, session.UsageKindRouter, returned)
	bucket := owned.Buckets[session.UsageKindRouter]
	wantTotal := mainUsage.Add(compactionUsage)
	if bucket.Total != wantTotal || bucket.Models["provider-u/utility"] != mainUsage || bucket.Models["provider-c/summary"] != compactionUsage {
		t.Fatalf("caller-owned utility usage = %#v, want total %+v with exact utility and compaction attribution", bucket, wantTotal)
	}
}

func TestAuxiliaryTokenUsage_Scenario3_NoUsageFromNonLLMHelpers(t *testing.T) {
	parent := runningAuxiliaryParent(t, "non-llm-parent")
	_, reviewUsage, err := (zeroUsageReviewer{}).Review(t.Context(), ChildAskReviewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, judgeUsage, err := (zeroUsageJudge{}).Judge(t.Context(), []BranchSummary{{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.RecordAuxiliaryUsage(reviewUsage.Merge(judgeUsage))
	for _, kind := range []session.UsageKind{session.UsageKindAskReviewer, session.UsageKindParallelJudge} {
		if got := parent.UsageFor(kind); got != (session.Usage{}) {
			t.Fatalf("non-LLM helper fabricated %s usage: %+v", kind, got)
		}
	}
}

func TestAuxiliaryTokenUsage_Scenario3_NormalChildRunsRemainMain(t *testing.T) {
	usage := session.Usage{InputTokens: 9, OutputTokens: 1}
	engine := reviewerEngine(mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkText, Text: "done"},
		{Kind: port.ChunkUsage, Usage: &usage},
		{Kind: port.ChunkDone},
	}}))
	sess := session.New("ordinary-child", session.ModeDefault, judgeEnvironment.Ref(), session.Limits{MaxTurns: 1}, time.Unix(1, 0))
	_, stop := drainChild(engine.Run(t.Context(), sess, judgeEnvironment, RunRequest{Text: "work"}), childPosture{role: "child"})
	if stop != session.StopEndTurn {
		t.Fatalf("stop = %q", stop)
	}
	if got := sess.UsageFor(session.UsageKindMain); got != usage {
		t.Fatalf("main usage = %+v, want %+v", got, usage)
	}
	for _, kind := range []session.UsageKind{session.UsageKindRouter, session.UsageKindAskReviewer, session.UsageKindGuardrail, session.UsageKindParallelJudge} {
		if got := sess.UsageFor(kind); got != (session.Usage{}) {
			t.Fatalf("%s usage = %+v, want zero", kind, got)
		}
	}
}

func runningAuxiliaryParent(t *testing.T, id session.SessionID) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/", Revision: "test"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.RecordUserPrompt("go", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	return sess
}

type auxiliaryUsageForker struct{}

func (auxiliaryUsageForker) Fork(context.Context, tool.Environment, string) (tool.Environment, func() error, string, error) {
	return judgeEnvironment, func() error { return nil }, "", nil
}

func TestAuxiliaryTokenUsage_Scenario3_ParallelJudgeRecordsInheritedModel(t *testing.T) {
	usage := session.Usage{InputTokens: 8, OutputTokens: 2}
	for _, join := range []string{"judge", "best"} {
		t.Run(join, func(t *testing.T) {
			child := NewEngine(Deps{
				LLM:     mockllm.New(mockllm.TextTurn("first branch"), mockllm.TextTurn("second branch")),
				Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "inherited-model",
			})
			judge := NewEngineJudge(NewEngine(Deps{
				LLM: mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
					{Kind: port.ChunkText, Text: `{"winner":2,"rationale":"second is best"}`},
					{Kind: port.ChunkUsage, Usage: &usage},
					{Kind: port.ChunkDone, Stop: session.StopEndTurn},
				}}), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "inherited-model",
				ProviderModel: session.ProviderModelID{ProviderID: "provider-p", ModelID: "inherited-model"},
			}))

			parallel := NewParallelTool(child, auxiliaryUsageForker{}, WithParallelJudge(judge), WithParallelConcurrency(1)).(*ParallelTool)
			catalog := tool.NewCatalog()
			catalog.MustRegister(parallel)
			call := session.NewToolCall("parallel-call", parallelToolName, []byte(`{"tasks":["one","two"],"join":"`+join+`"}`))
			owner := NewEngine(Deps{
				LLM:     mockllm.New(mockllm.ToolCallTurn(call), mockllm.TextTurn("done")),
				Catalog: catalog, Policy: allowAllInt(),
			})
			parent := session.New(session.SessionID("parallel-parent-"+join), session.ModeDefault, judgeEnvironment.Ref(), session.Limits{}, time.Unix(1, 0))
			var result session.ToolResult
			for ev := range owner.Run(t.Context(), parent, judgeEnvironment, RunRequest{Text: "compare branches"}).Events() {
				if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == call.ID {
					result = *ev.ToolResult
				}
			}
			if result.IsError || !strings.Contains(result.Content, "branch-2 [WINNER]") {
				t.Fatalf("Parallel %s result = %#v, want actual judge winner", join, result)
			}
			bucket := parent.TokenUsageSnapshot()[session.UsageKindParallelJudge]
			if bucket.Total != usage || bucket.Models["provider-p/inherited-model"] != usage {
				t.Fatalf("Parallel %s parent usage = %#v, want %#v under inherited model", join, bucket, usage)
			}
			if main := parent.UsageFor(session.UsageKindMain); main != (session.Usage{}) {
				t.Fatalf("Parallel %s folded judge usage into main: %+v", join, main)
			}
		})
	}
}
