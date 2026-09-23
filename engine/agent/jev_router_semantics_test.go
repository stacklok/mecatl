package agent

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestADR_0352_Scenario2_ExistingRoutingSemantics pins that selecting a different
// composition backend does not alter any consumer of the existing routeTask callback.
func TestADR_0352_Scenario2_ExistingRoutingSemantics(t *testing.T) {
	t.Run("plain Subagent", TestRunRouteTaskRoutesPlainDelegation)
	t.Run("explicit per-call model", TestRunExplicitModelBeatsRouter)
	t.Run("fork", TestRunForkDoesNotRoute)
	t.Run("pinned named specialist including inherit", TestRunNamedAgentBeatsRouter)
	t.Run("unpinned named specialist", TestRunRoutableAgentRoutesViaFactory)
	t.Run("resume", TestRunResumeDoesNotRoute)
	t.Run("writable explorer", TestRunWritableRoutesWhenFactoryWired)
	t.Run("writable specialist", TestRunWritableRoutableAgentRoutesViaFactory)
	t.Run("team member route once at add", TestMemberRoutesAtAddMember)
	t.Run("team member retained across rounds", TestMemberRoutesOncePerRun)
	t.Run("Parallel branch", TestParallelRoutesBranchOnClassifiedModel)
	t.Run("Parallel branch routes once", TestParallelRoutesEachBranchExactlyOnce)
}

type blockingRouterProvider struct {
	entered   chan struct{}
	cancelled chan struct{}
}

func (*blockingRouterProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *blockingRouterProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		close(p.entered)
		if !yield(port.Chunk{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 7, OutputTokens: 3}}, nil) {
			return
		}
		<-ctx.Done()
		close(p.cancelled)
	}, nil
}

func TestRunModelRouterOperationDeadlineIsTimeout(t *testing.T) {
	provider := &blockingRouterProvider{entered: make(chan struct{}), cancelled: make(chan struct{})}
	engine := NewEngine(Deps{LLM: provider, Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "classifier"})
	caller := t.Context()
	type result struct {
		usage  session.Usage
		reason string
		ok     bool
	}
	resultCh := make(chan result, 1)
	go func() {
		_, usage, reason, ok := runModelRouter(caller, engine, ModelRouteRequest{
			TaskPrompt: "task",
			Categories: []ModelRouteCategory{{Name: "small", Description: "small task"}},
		}, 50*time.Millisecond)
		resultCh <- result{usage: usage, reason: reason, ok: ok}
	}()

	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("model router did not start the classifier operation")
	}
	select {
	case <-provider.cancelled:
	case <-time.After(time.Second):
		t.Fatal("operation deadline did not cancel the live classifier")
	}
	select {
	case got := <-resultCh:
		if got.ok || got.reason != RouterMissTimeout {
			t.Fatalf("operation deadline reason=%q ok=%v", got.reason, got.ok)
		}
		if got.usage.InputTokens != 7 || got.usage.OutputTokens != 3 {
			t.Fatalf("operation deadline dropped retained usage: %+v", got.usage)
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out classifier did not return promptly")
	}
	if err := caller.Err(); err != nil {
		t.Fatalf("operation deadline cancelled caller context: %v", err)
	}
	if modelRouterTimeout != 30*time.Second {
		t.Fatalf("production router timeout = %v", modelRouterTimeout)
	}
}
