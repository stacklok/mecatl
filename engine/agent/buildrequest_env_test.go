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

// TestBuildRequestThreadsVolatileEnvAndPlanReminder proves the REAL per-turn request
// assembly (Engine.buildRequest) carries the volatile Env fields that ride from
// Deps.PromptConfig — Shell and the <git-status> snapshot — plus the plan-mode
// reminder, into the LLMRequest.System.VolatileSuffix, and that NONE of them leak into
// the cache-stable StablePrefix. buildRequest is unexported, so this drives the
// smallest real entry point (Engine.Run) and captures the request the provider
// actually received via mockllm.WithRequestObserver.
func TestBuildRequestThreadsVolatileEnvAndPlanReminder(t *testing.T) {
	const (
		shellVal  = "/usr/bin/fish"
		gitStatus = "branch: feature/widget\nstatus:\n(clean)\ncommits:\nabc1234 do the thing"
	)

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
		mockllm.TextTurn("a plan"),
	)

	e := newEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		// The composition layer fills these volatile Env fields once and they ride
		// from Deps.PromptConfig through buildRequest into every turn's request.
		PromptConfig: prompt.Config{
			Env: prompt.Env{
				Shell:     shellVal,
				GitStatus: gitStatus,
			},
		},
	})

	// A session in PLAN mode so buildRequest appends the plan-mode reminder.
	sess := session.New("s1", session.ModePlan, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "make a plan"})
	drain(r)

	mu.Lock()
	defer mu.Unlock()
	if !seen {
		t.Fatal("provider never received a request")
	}

	suffix := got.System.VolatileSuffix
	prefix := got.System.StablePrefix

	// (a) the shell value reached the volatile suffix.
	if !strings.Contains(suffix, shellVal) {
		t.Errorf("VolatileSuffix missing shell %q:\n%s", shellVal, suffix)
	}
	// (b) the <git-status> sub-block + its content reached the volatile suffix.
	if !strings.Contains(suffix, "<git-status>") || !strings.Contains(suffix, "feature/widget") {
		t.Errorf("VolatileSuffix missing <git-status> snapshot:\n%s", suffix)
	}
	// (c) the plan-mode reminder reached the volatile suffix.
	if !strings.Contains(suffix, "Plan mode is active") {
		t.Errorf("VolatileSuffix missing plan-mode reminder:\n%s", suffix)
	}

	// NONE of the volatile values may leak into the cache-stable prefix.
	for _, leak := range []string{shellVal, "<git-status>", "feature/widget", "Plan mode is active"} {
		if strings.Contains(prefix, leak) {
			t.Errorf("StablePrefix leaked volatile value %q:\n%s", leak, prefix)
		}
	}
}
