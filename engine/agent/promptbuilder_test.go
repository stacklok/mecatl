package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// captureFirstRequest builds a mockllm provider whose request observer records
// the FIRST LLMRequest the provider receives (the first turn's system prompt),
// returning the provider and a thread-safe accessor. It mirrors the convention
// in buildrequest_env_test.go.
func captureFirstRequest(t *testing.T, turns ...mockllm.Turn) (*mockllm.Provider, func() (port.LLMRequest, bool)) {
	t.Helper()
	var (
		mu   sync.Mutex
		got  port.LLMRequest
		seen bool
	)
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			mu.Lock()
			defer mu.Unlock()
			if !seen { // capture the FIRST turn's request
				got = req
				seen = true
			}
		})},
		turns...,
	)
	return llm, func() (port.LLMRequest, bool) {
		mu.Lock()
		defer mu.Unlock()
		return got, seen
	}
}

// TestBuildRequestNilPromptBuilderIsByteIdenticalToDefault is the v0.0.1
// pinning test (issue #127 acceptance criterion): a Deps with PromptBuilder
// left nil MUST produce an LLMRequest.System byte-identical to prompt.Build
// over the SAME Config the loop builds (catalog Specs + volatile Env filled per
// turn). If the nil-resolution or the default ever drifts, this breaks.
//
// It runs against a SHELL-BEARING Environment (a stub runner) so the issue-#462
// shell-less posture clause (appended by buildRequest only when
// env.CommandRunner()==nil) does NOT fire — keeping this a pure prompt.Build pin.
// The shell-less clause is pinned separately in shell_less_posture_test.go.
func TestBuildRequestNilPromptBuilderIsByteIdenticalToDefault(t *testing.T) {
	cat := catalogWith(t)
	llm, firstReq := captureFirstRequest(t, mockllm.TextTurn("ok"))

	e := newEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		// PromptBuilder intentionally nil — the v0.0.1 default path.
	})

	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, agent.MemEnvRunner("/ws", stubShellRunner{}), agent.RunRequest{Text: "hi"})
	drain(r)

	got, ok := firstReq()
	if !ok {
		t.Fatal("provider never received a request")
	}

	// Reconstruct the EXACT Config buildRequest assembles for the first turn:
	// Tools from the catalog (ProgressiveTools off → Specs), Env with the loop's
	// per-turn fills (Model + Mode + Cwd fallback to the session workspace).
	wantCfg := prompt.Config{
		Tools: cat.Specs(sess.Mode),
		Env: prompt.Env{
			Model: "test-model", // newEngine sets Model="test-model" when empty
			Mode:  string(sess.Mode),
			Cwd:   "/ws",
		},
	}
	want := prompt.Build(wantCfg)

	if got.System.StablePrefix != want.StablePrefix {
		t.Errorf("nil PromptBuilder StablePrefix diverged from prompt.Build:\n got=%q\nwant=%q",
			got.System.StablePrefix, want.StablePrefix)
	}
	if got.System.VolatileSuffix != want.VolatileSuffix {
		t.Errorf("nil PromptBuilder VolatileSuffix diverged from prompt.Build:\n got=%q\nwant=%q",
			got.System.VolatileSuffix, want.VolatileSuffix)
	}
}

// stubShellRunner is a no-op tool.CommandRunner for tests that need a
// SHELL-BEARING Environment (env.CommandRunner() != nil) without running any
// command. It exists so buildRequest's shell-less gate (issue #462 review) can
// be held CLOSED in prompt-pinning tests that are not about the shell-less
// posture.
type stubShellRunner struct{}

func (stubShellRunner) Run(context.Context, string) (tool.CommandResult, error) {
	return tool.CommandResult{}, nil
}

func (stubShellRunner) RunWithEnvironment(context.Context, string, tool.CommandEnvironmentOverlay) (tool.CommandResult, error) {
	return tool.CommandResult{}, nil
}

// TestBuildRequestHostPromptBuilderOwnsSystemPrompt is the headline acceptance
// criterion (issue #127): a host-supplied PromptBuilder produces a fully
// host-owned system prompt with NONE of the coding-agent defaults
// (defaultRole/defaultTone/defaultSafety) and NO "Available tools:" block, even
// when tools ARE registered. The host builder receives cfg with Tools populated
// but is free to ignore them — that is the whole point.
func TestBuildRequestHostPromptBuilderOwnsSystemPrompt(t *testing.T) {
	const (
		hostPrefix = "You are a scheduling assistant that talks to Slack."
		hostSuffix = "tenant: acme"
	)
	// Register a real tool so we can prove the host builder can omit the tool
	// inventory even with tools present. fakeTool's Spec renders a deterministic
	// description ("Read: test tool") whose name must NOT leak into the prompt.
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})

	llm, firstReq := captureFirstRequest(t, mockllm.TextTurn("ok"))

	e := newEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		PromptBuilder: func(prompt.Config) prompt.Layered {
			return prompt.Layered{StablePrefix: hostPrefix, VolatileSuffix: hostSuffix}
		},
	})

	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "draft the weekly update"})
	drain(r)

	got, ok := firstReq()
	if !ok {
		t.Fatal("provider never received a request")
	}

	if got.System.StablePrefix != hostPrefix {
		t.Errorf("host builder StablePrefix not honored:\n got=%q\nwant=%q",
			got.System.StablePrefix, hostPrefix)
	}
	if got.System.VolatileSuffix != hostSuffix {
		t.Errorf("host builder VolatileSuffix not honored:\n got=%q\nwant=%q",
			got.System.VolatileSuffix, hostSuffix)
	}

	// NO coding-agent defaults and NO tool inventory may leak into the
	// host-owned prompt, even though tools are registered and cfg.Tools is
	// populated for the builder.
	for _, forbidden := range []string{
		"headless agentic coding harness", // defaultRole
		"file_path:line_number",           // defaultTone
		"git add -A",                      // defaultTone
		"immutable safety rules",          // defaultSafety
		"Available tools:",                // toolInventory
		"Read",                            // the registered tool name
	} {
		if strings.Contains(got.System.StablePrefix, forbidden) ||
			strings.Contains(got.System.VolatileSuffix, forbidden) {
			t.Errorf("host-owned system prompt leaked default/tool content %q:\n%s",
				forbidden, got.System.Render())
		}
	}
}

// TestPromptBuilderHostCanStillUseInventoryAndEnv proves the seam is flexible:
// a host builder MAY opt back into the built-in tool inventory and env block by
// calling prompt.Build from inside its builder. This documents that the seam is
// not all-or-nothing — a non-coding host can keep the tool list while replacing
// the role/tone/safety.
func TestPromptBuilderHostCanStillUseInventoryAndEnv(t *testing.T) {
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})

	llm, firstReq := captureFirstRequest(t, mockllm.TextTurn("ok"))

	hostRole := "You are a scheduling assistant."
	e := newEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		PromptBuilder: func(cfg prompt.Config) prompt.Layered {
			// A host that wants the tool inventory + env block but its OWN role:
			// reuse prompt.Build, then overlay the role. This is the documented
			// "host can still use cfg.Tools/cfg.Env" path from the issue.
			base := prompt.Build(cfg)
			return prompt.Layered{
				StablePrefix:   hostRole + "\n\n" + base.StablePrefix,
				VolatileSuffix: base.VolatileSuffix,
			}
		},
	})

	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hi"})
	drain(r)

	got, ok := firstReq()
	if !ok {
		t.Fatal("provider never received a request")
	}
	if !strings.HasPrefix(got.System.StablePrefix, hostRole) {
		t.Errorf("host role not prepended:\n%s", got.System.StablePrefix)
	}
	if !strings.Contains(got.System.StablePrefix, "Available tools:") {
		t.Errorf("host builder that opted into prompt.Build lost the tool inventory:\n%s",
			got.System.StablePrefix)
	}
	if !strings.Contains(got.System.VolatileSuffix, "/ws") {
		t.Errorf("host builder that opted into prompt.Build lost the env cwd:\n%s",
			got.System.VolatileSuffix)
	}
}

// TestPromptBuilderDoesNotRouteThroughCompactionSummarizer is the isolation
// guard (issue #127): only the MAIN loop's buildRequest routes through
// Deps.PromptBuilder. The compaction summarizer (cascade.go) builds its own
// prompt.Layered directly and must be UNAFFECTED by a host builder — otherwise a
// host plugging in a non-coding prompt would corrupt the summariser's
// structured-output contract. We drive a real compaction through the loop with
// a real CascadeCompactor wired to the same provider, capture EVERY request the
// provider sees, and assert the summarizer request carries the summarizer's
// system prompt, NOT the host builder's output.
func TestPromptBuilderDoesNotRouteThroughCompactionSummarizer(t *testing.T) {
	const hostPrefix = "HOST-BUILDER-MARKER must not reach the summarizer"

	cat := catalogWith(t)
	var (
		mu       sync.Mutex
		requests []port.LLMRequest
	)
	scriptedLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, req)
		})},
		// The summarizer call must return non-empty text or Compact aborts to the
		// original history (fail-safe). TextTurn gives it a one-line summary so
		// tier-4 completes and its request is observable.
		mockllm.TextTurn("summary"), // summarizer turn (cascade.go summarize)
		mockllm.TextTurn("done"),    // main loop turn after compaction
	)
	llm := &contextObservingProvider{inner: scriptedLLM}

	// A REAL CascadeCompactor wired to the provider so summarize() actually
	// fires a provider request we can observe. Tiny BudgetTokens forces tier-4
	// on any non-trivial middle segment.
	realCompactor := agent.CascadeCompactor{
		LLM:          llm,
		Counter:      agent.HeuristicTokenCounter{},
		BudgetTokens: 1,
	}

	e := newEngine(agent.Deps{
		LLM:             llm,
		Catalog:         cat,
		Compactor:       &cascadeCompactorWrapper{inner: realCompactor},
		ContextWindow:   func() int { return 10 }, // tiny: trip compaction on a big prompt
		CompactionRatio: 0.8,
		PromptBuilder: func(prompt.Config) prompt.Layered {
			return prompt.Layered{StablePrefix: hostPrefix}
		},
	})

	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	bigPrompt := strings.Repeat("word ", 200) // ~250 tokens >> threshold of 8
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: bigPrompt})
	drain(r)

	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("provider never received a request")
	}

	// At least one request must be the summarizer's: its StablePrefix is the
	// cascade summarizerSystemPrompt, which begins with this known first line
	// (it is unexported in package agent, so we assert on the public surface).
	const summarizerFirstLine = "You are a context-compaction summariser for a coding agent."
	var sawSummarizer bool
	for _, req := range requests {
		if strings.HasPrefix(req.System.StablePrefix, summarizerFirstLine) {
			sawSummarizer = true
			if strings.Contains(req.System.StablePrefix, hostPrefix) ||
				strings.Contains(req.System.VolatileSuffix, hostPrefix) {
				t.Errorf("summarizer request leaked the host builder marker %q:\n%s",
					hostPrefix, req.System.Render())
			}
		}
	}
	if !sawSummarizer {
		// The test only proves isolation IF the summarizer actually ran. If
		// compaction didn't trip tier-4, surface it rather than silently passing.
		t.Fatalf("summarizer request not observed (compaction did not reach tier-4); "+
			"saw %d requests, first prefix=%q", len(requests),
			firstNonEmptyPrefix(requests))
	}
	for i, id := range llm.sessionIDs() {
		if id != "s1" {
			t.Errorf("provider call %d session id = %q, want parent session s1 (including compaction)", i, id)
		}
	}
}

// firstNonEmptyPrefix returns the first non-empty StablePrefix among reqs, or
// "" if all are empty (a debugging aid for the isolation test's failure
// message).
func firstNonEmptyPrefix(reqs []port.LLMRequest) string {
	for _, r := range reqs {
		if r.System.StablePrefix != "" {
			return r.System.StablePrefix
		}
	}
	return ""
}

// TestPromptBuilderAppliesEveryTurn pins the seam's "every model turn carries"
// contract (issue #127): the host builder is consulted on EACH turn, not just
// the first. captureFirstRequest only records turn 0; this test captures EVERY
// request across a text→tool-call→text run and asserts the host marker is
// present on both the pre-tool turn and the post-tool turn. If the builder were
// cached/cleared after turn 0, the second request would carry the default
// coding prompt instead of the host marker and this test fails.
func TestPromptBuilderAppliesEveryTurn(t *testing.T) {
	const hostMarker = "HOST-MARKER-every-turn"
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})

	var (
		mu       sync.Mutex
		requests []port.LLMRequest
	)
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, req)
		})},
		// Turn 1: a tool call. Turn 2: terminal text. (two model calls.)
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "Read", `{"path":"a.go"}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("done"),
	)

	e := newEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		PromptBuilder: func(prompt.Config) prompt.Layered {
			return prompt.Layered{StablePrefix: hostMarker}
		},
	})

	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "look at a.go"})
	drain(r)

	mu.Lock()
	defer mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("expected at least 2 provider requests (one per turn), got %d", len(requests))
	}
	for i, req := range requests {
		if req.System.StablePrefix != hostMarker {
			t.Errorf("turn %d: host marker missing from system prompt (builder not applied every turn):\n got=%q",
				i, req.System.StablePrefix)
		}
		// And the default coding role must NOT have leaked back in on any turn.
		if strings.Contains(req.System.StablePrefix, "headless agentic coding harness") {
			t.Errorf("turn %d: default coding role leaked into a host-owned prompt", i)
		}
	}
}

// TestPromptBuilderEmptyLayeredIsHonoredNotBackfilled pins the acceptance
// criterion "NO default… unless the host included it" at its extreme: a host
// builder that returns a fully EMPTY Layered (both fields empty) MUST be
// honored — the loop must NOT panic and must NOT silently back-fill the coding
// defaults. A regression where buildRequest coalesced an empty Layered to
// prompt.Build(cfg) would re-introduce every default and pass the other tests
// (which use non-empty host prompts); this test catches it.
func TestPromptBuilderEmptyLayeredIsHonoredNotBackfilled(t *testing.T) {
	cat := catalogWith(t, &fakeTool{name: "Read", readOnly: true, exec: okExec})
	llm, firstReq := captureFirstRequest(t, mockllm.TextTurn("ok"))

	e := newEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		PromptBuilder: func(prompt.Config) prompt.Layered {
			return prompt.Layered{} // both fields intentionally empty
		},
	})

	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hi"})
	drain(r)

	got, ok := firstReq()
	if !ok {
		t.Fatal("provider never received a request")
	}
	if got.System.StablePrefix != "" {
		t.Errorf("empty host Layered back-filled StablePrefix with defaults:\n%q",
			got.System.StablePrefix)
	}
	if got.System.VolatileSuffix != "" {
		t.Errorf("empty host Layered back-filled VolatileSuffix with defaults:\n%q",
			got.System.VolatileSuffix)
	}
	// Belt-and-suspenders: none of the coding defaults may appear anywhere.
	for _, forbidden := range []string{
		"headless agentic coding harness", "file_path:line_number",
		"git add -A", "immutable safety rules", "Available tools:",
	} {
		if strings.Contains(got.System.StablePrefix, forbidden) ||
			strings.Contains(got.System.VolatileSuffix, forbidden) {
			t.Errorf("empty host Layered leaked default %q:\n%s",
				forbidden, got.System.Render())
		}
	}
}

// cascadeCompactorWrapper delegates to an inner Compactor so the test can wire
// a real CascadeCompactor (with an LLM) while satisfying the agent.Compactor
// interface through a local pointer type (CascadeCompactor's Compact is on the
// value receiver; a pointer wrapper avoids re-implementing its method set here
// and keeps the wrapper distinct from the recordingCompactor helper).
type cascadeCompactorWrapper struct {
	inner agent.Compactor
}

func (w *cascadeCompactorWrapper) Compact(ctx context.Context, conv *session.Conversation) ([]session.Message, string, session.AuxiliaryUsage, error) {
	return w.inner.Compact(ctx, conv)
}
