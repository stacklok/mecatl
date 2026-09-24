package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
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
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry(), diag: diag}

	got := engine.parentCaps(run, parent, 0).routeDecision(t.Context(), "classify")
	if !got.ok {
		t.Fatalf("routeDecision ok = false, reason %q", got.reason)
	}
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

type replayingUsageHook struct {
	usage    session.AuxiliaryUsage
	reporter port.AuxiliaryUsageReporter
}

func (h *replayingUsageHook) Run(ctx context.Context, _ governance.HookEvent) (governance.HookOutcome, error) {
	h.reporter = port.AuxiliaryUsageReporterFromContext(ctx)
	if h.reporter != nil {
		h.reporter(h.usage)
	}
	return governance.HookOutcome{}, nil
}

type fixedUsageReviewer struct {
	usage session.AuxiliaryUsage
}

func (r fixedUsageReviewer) Review(context.Context, ChildAskReviewRequest) (ChildAskReview, session.AuxiliaryUsage, error) {
	return ChildAskReview{Allowed: true}, r.usage, nil
}

type concurrentUsageHook struct {
	usage session.AuxiliaryUsage
}

func (h concurrentUsageHook) Run(ctx context.Context, _ governance.HookEvent) (governance.HookOutcome, error) {
	port.AuxiliaryUsageReporterFromContext(ctx)(h.usage)
	return governance.HookOutcome{}, nil
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
	unexpectedGuardrailUsage := session.Usage{InputTokens: 3, OutputTokens: 1}
	checker := reviewerEngine(mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkText, Text: `{"safe":true}`},
		{Kind: port.ChunkUsage, Usage: &guardrailUsage},
		{Kind: port.ChunkDone},
	}}))
	_, checkedUsage, err := RunGuardrailCheck(t.Context(), checker, "check")
	if err != nil {
		t.Fatal(err)
	}

	checkedUsage = checkedUsage.Merge(session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindMain: {Models: map[string]session.Usage{"provider-g/checker": unexpectedGuardrailUsage}},
	}})
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
	hook := &replayingUsageHook{usage: checkedUsage}
	hookEngine := NewEngine(Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "main", Hooks: hook})
	if _, err := hookEngine.runOwnedHook(t.Context(), &Run{diag: port.NopDiagnostics{}}, parent, governance.HookEvent{Phase: governance.PhasePreToolUse}); err != nil {
		t.Fatal(err)
	}
	if hook.reporter == nil {
		t.Fatal("Engine did not install AuxiliaryUsageReporter during hook request")
	}
	// A retained callback is inert as soon as HookRunner.Run returns.
	hook.reporter(checkedUsage)
	wantReview := reviewerUsage.Add(session.Usage{InputTokens: 2})
	if got := parent.UsageFor(session.UsageKindAskReviewer); got != wantReview {
		t.Fatalf("ask reviewer usage = %+v, want remapped %+v", got, wantReview)
	}
	wantGuardrail := guardrailUsage.Add(unexpectedGuardrailUsage)
	if got := parent.UsageFor(session.UsageKindGuardrail); got != wantGuardrail {
		t.Fatalf("guardrail usage = %+v, want remapped %+v", got, wantGuardrail)
	}
	if got := parent.UsageFor(session.UsageKindMain); got != (session.Usage{}) {
		t.Fatalf("main usage = %+v, want zero", got)
	}

	// PostToolUse guardrails run in read-parallel dispatch goroutines. Exercise that
	// exact concurrent reporting shape against one run-owned Session under -race.
	concurrentParent := runningAuxiliaryParent(t, "concurrent-guardrail-parent")
	concurrentRun := &Run{diag: port.NopDiagnostics{}}
	one := session.Usage{InputTokens: 1}
	concurrentEngine := NewEngine(Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "main",
		Hooks: concurrentUsageHook{usage: auxiliaryUsage(session.UsageKindGuardrail, session.ProviderModelID{ProviderID: "provider-g", ModelID: "checker"}, one)},
	})
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := concurrentEngine.runOwnedHook(t.Context(), concurrentRun, concurrentParent, governance.HookEvent{Phase: governance.PhasePostToolUse}); err != nil {
				t.Errorf("runOwnedHook: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := concurrentParent.UsageFor(session.UsageKindGuardrail); got.InputTokens != 32 {
		t.Fatalf("concurrent guardrail usage = %+v, want 32 input tokens", got)
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

	returned := utilityEngineUsage(session.UsageKindRouter, utilityIdentity, utility)
	owned := remapAuxiliaryUsage(t.Context(), port.NopDiagnostics{}, session.UsageKindRouter, returned)
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
			parent := runningAuxiliaryParent(t, session.SessionID("parallel-parent-"+join))
			owner := NewEngine(Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt()})
			caps := owner.parentCaps(&Run{children: newChildRunRegistry(), diag: port.NopDiagnostics{}}, parent, 0)
			result, err := parallel.ExecuteWithParent(t.Context(), session.NewToolCall("parallel-call", parallelToolName, []byte(`{"tasks":["one","two"],"join":"`+join+`"}`)), judgeEnvironment, nil, caps)
			if err != nil || result.IsError {
				t.Fatalf("Parallel %s result = %#v, %v", join, result, err)
			}
			if !strings.Contains(result.Content, "branch-2 [WINNER]") {
				t.Fatalf("Parallel %s did not apply actual judge result: %s", join, result.Content)
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
