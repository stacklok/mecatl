package mcpbroker

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestSessionAPICancelResolvedAuthorizationPreservesRunningResult(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		close(started)
		<-release
		return slotResult(), nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	attempt := c.BrokerAttempt{ID: apiRef()}
	result := make(chan c.InvocationOutcome, 1)
	go func() {
		out, _ := api.InvokeTool(ctx, opened.Ref, cat.Ref(), c.Call{ID: "call", Name: "mcp__slots__echo", Arguments: []byte(`{}`)}, attempt)
		result <- out
	}()
	awaitCoordination(t, started)

	// A resumed authorization remains addressable while its worker is running.
	// Cancellation of that resolved park must not invalidate execution ownership.
	op, unlock, err := api.operation(ctx, opened.Ref, apiControl{})
	if err != nil {
		t.Fatal(err)
	}
	st := op.Value(apiOperationKey{}).(*apiOperation).state
	auth := c.AuthorizationRef(apiRef())
	st.parked[auth] = &apiParked{attempt: attempt, terminal: &c.FlowStatus{Kind: c.FlowCompleted, Catalogue: cat}}
	generation := st.generation
	unlock()

	cancelled, err := api.CancelAuthorization(ctx, opened.Ref, auth, attempt)
	if err != nil || cancelled != c.AlreadyResolved {
		t.Fatalf("cancel resolved park: %v %v", cancelled, err)
	}
	close(release)
	if out := awaitCoordination(t, result); out.Kind != c.InvocationCompleted {
		t.Fatalf("cancellation invalidated execution result: %+v", out)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if st.generation != generation {
		t.Fatal("authorization cancellation changed session generation")
	}
}
