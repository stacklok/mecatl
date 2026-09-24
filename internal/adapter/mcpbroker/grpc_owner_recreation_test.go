package mcpbroker

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestAuthenticatedOwnerCanRecreateRuntimeState(t *testing.T) {
	for _, operation := range []string{"abort", "delete-drain-error"} {
		t.Run(operation, func(t *testing.T) {
			catalogue, err := Compile(anonymousConfig(), discoveredTools(), nil)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
				close(entered)
				<-release
				return session.NewToolResult("call", "done"), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runtime.Close() })
			backend := &observedRuntimeDelete{Runtime: runtime}
			server, err := mcpbrokergrpc.NewServer(backend, mcpbrokergrpc.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
			ctx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "owner"})
			other := session.WithPrincipal(t.Context(), &session.Principal{Subject: "other"})
			first, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "recreate"})
			if err != nil || first.GetOutcome() != string(contract.AttachCreated) {
				t.Fatalf("initial Attach = %v, %v", first, err)
			}
			if operation == "abort" {
				_, err = server.Abort(ctx, &brokerv1.AbortRequest{Handle: first.GetHandle(), BrokerIncarnation: first.GetBrokerIncarnation()})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				executeDone := make(chan error, 1)
				go func() {
					_, executeErr := server.Execute(ctx, &brokerv1.ExecuteRequest{Handle: first.GetHandle(), BrokerIncarnation: first.GetBrokerIncarnation(), Name: "mcp__search__query", CallId: "call", Args: []byte(`{}`)})
					executeDone <- executeErr
				}()
				t.Cleanup(func() { close(release); <-executeDone })
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("execution did not enter runtime")
				}
				deleteCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
				_, err = server.Delete(deleteCtx, &brokerv1.DeleteRequest{SessionId: "recreate", Binding: first.GetBinding()})
				if status.Code(err) != codes.DeadlineExceeded || backend.outcome != contract.DeleteDeleted || !errors.Is(backend.err, context.DeadlineExceeded) {
					t.Fatalf("Delete = %v; runtime = (%q, %v), want deletion took effect then drain deadline", err, backend.outcome, backend.err)
				}
			}
			// Neither Abort nor a failed Delete releases workload ownership.
			if _, err := server.Attach(other, &brokerv1.AttachRequest{SessionId: "recreate"}); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("other principal before recreation = %v", err)
			}
			fresh, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "recreate"})
			if err != nil || fresh.GetOutcome() != string(contract.AttachCreated) || fresh.GetBinding() == first.GetBinding() {
				t.Fatalf("same-owner recreation = %v, %v; old binding %q", fresh, err, first.GetBinding())
			}
			if _, err := server.Attach(other, &brokerv1.AttachRequest{SessionId: "recreate"}); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("other principal after recreation = %v", err)
			}
		})
	}
}

// Observe the real runtime outcome even though the RPC returns only its error.
type observedRuntimeDelete struct {
	*Runtime
	outcome contract.DeleteOutcome
	err     error
}

func (b *observedRuntimeDelete) DeleteSessionIfBinding(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (contract.DeleteOutcome, error) {
	b.outcome, b.err = b.Runtime.DeleteSessionIfBinding(ctx, id, binding)
	return b.outcome, b.err
}
