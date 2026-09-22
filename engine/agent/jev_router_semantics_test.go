package agent

import (
	"context"
	"testing"
	"time"
)

// TestADR_0350_Scenario2_ExistingRoutingSemantics pins that selecting a different
// composition backend does not alter any consumer of the existing routeTask callback.
func TestADR_0350_Scenario2_ExistingRoutingSemantics(t *testing.T) {
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

func TestRunModelRouterOperationDeadlineIsTimeout(t *testing.T) {
	_, _, reason, ok := runModelRouter(context.Background(), markerEngine("classifier"), ModelRouteRequest{
		TaskPrompt: "task",
		Categories: []ModelRouteCategory{{Name: "small", Description: "small task"}},
	}, 0)
	if ok || reason != RouterMissTimeout {
		t.Fatalf("operation deadline reason=%q ok=%v", reason, ok)
	}
	if modelRouterTimeout != 30*time.Second {
		t.Fatalf("production router timeout = %v", modelRouterTimeout)
	}
}
