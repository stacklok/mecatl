package mcpbrokergrpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

// Drive real Attach/Delete calls; channels establish the pending-bind/fence/drain
// ordering without sleeps or a scheduler-dependent race window.
func TestDeleteDrainsPreexistingAttach(t *testing.T) {
	for _, scenario := range []string{"complete", "cancel", "cancel-attach", "shutdown", "replacement"} {
		t.Run(scenario, func(t *testing.T) {
			backend := &drainingDeleteBroker{entered: make(chan struct{}), resume: make(chan struct{}), deleted: make(chan struct{}, 1)}
			cfg := DefaultConfig()
			cfg.SweepInterval = time.Hour
			server, err := NewServer(backend, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
			ctx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "owner"})
			first, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "draining"})
			if err != nil {
				t.Fatal(err)
			}
			backend.block = true // No backend call is in flight yet.
			attachCtx, cancelAttach := context.WithCancel(ctx)
			defer cancelAttach()
			attachDone := make(chan error, 1)
			go func() {
				_, attachErr := server.Attach(attachCtx, &brokerv1.AttachRequest{SessionId: "draining"})
				attachDone <- attachErr
			}()
			awaitDeleteDrainSignal(t, backend.entered)
			server.mu.Lock()
			owner := server.owners["draining"]
			fenced := owner.changed
			server.mu.Unlock()
			deleteCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			deleteDone := make(chan error, 1)
			go func() {
				_, deleteErr := server.Delete(deleteCtx, &brokerv1.DeleteRequest{SessionId: "draining", Binding: first.GetBinding()})
				deleteDone <- deleteErr
			}()
			awaitDeleteDrainSignal(t, fenced)
			server.mu.Lock()
			pending, retiring := owner.pending, owner.retiring
			server.mu.Unlock()
			if pending != 1 || !retiring {
				t.Fatalf("fence = pending %d, retiring %v", pending, retiring)
			}
			if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "draining"}); status.Code(err) != codes.Unavailable {
				t.Fatalf("new attach while draining = %v", err)
			}
			var replacement *sessionOwner
			switch scenario {
			case "cancel":
				cancel()
				if err := awaitDeleteDrainResult(t, deleteDone); status.Code(err) != codes.Canceled {
					t.Fatalf("cancelled delete = %v", err)
				}
			case "shutdown":
				if err := server.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := awaitDeleteDrainResult(t, deleteDone); status.Code(err) != codes.Unavailable {
					t.Fatalf("shutdown delete = %v", err)
				}
			case "replacement":
				server.mu.Lock()
				replacement = &sessionOwner{principal: owner.principal, retiring: true, handles: 7, changed: make(chan struct{})}
				server.owners["draining"] = replacement
				signalOwner(owner)
				server.mu.Unlock()
				if err := awaitDeleteDrainResult(t, deleteDone); status.Code(err) != codes.Unavailable {
					t.Fatalf("replaced owner delete = %v", err)
				}
			}
			if scenario == "cancel-attach" {
				cancelAttach()
			} else {
				close(backend.resume)
			}
			attachErr := awaitDeleteDrainResult(t, attachDone)
			if scenario == "cancel-attach" {
				if status.Code(attachErr) != codes.Canceled {
					t.Fatalf("cancelled pending attach = %v", attachErr)
				}
			} else if scenario == "cancel" {
				if attachErr != nil {
					t.Fatalf("attach after cancellation = %v", attachErr)
				}
			} else if status.Code(attachErr) != codes.Unavailable {
				t.Fatalf("fenced attach = %v", attachErr)
			}
			if scenario == "complete" || scenario == "cancel-attach" {
				if err := awaitDeleteDrainResult(t, deleteDone); err != nil {
					t.Fatalf("delete after attach drained = %v", err)
				}
				awaitDeleteDrainSignal(t, backend.deleted)
			} else {
				select {
				case <-backend.deleted:
					t.Fatal("abandoned delete reached backend")
				default:
				}
			}
			server.mu.Lock()
			defer server.mu.Unlock()
			if owner.pending != 0 {
				t.Fatalf("pending binds = %d", owner.pending)
			}
			switch scenario {
			case "complete", "cancel-attach":
				if server.owners["draining"] != nil {
					t.Fatal("completed delete left owner stuck")
				}
			case "cancel":
				if server.owners["draining"] != owner || owner.retiring || owner.handles != 2 {
					t.Fatalf("cancelled drain owner accounting: retiring=%v handles=%d", owner.retiring, owner.handles)
				}
			case "replacement":
				if server.owners["draining"] != replacement || !replacement.retiring || replacement.handles != 7 {
					t.Fatal("old drain changed replacement owner")
				}
			}
		})
	}
}

func awaitDeleteDrainSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for drain signal")
	}
}

func awaitDeleteDrainResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for drain result")
		return nil
	}
}

type drainingDeleteBroker struct {
	mcpbroker.Service
	block                    bool
	entered, resume, deleted chan struct{}
}

func (b *drainingDeleteBroker) AttachSession(ctx context.Context, _ session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	if b.block {
		close(b.entered)
		select {
		case <-b.resume:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	return drainingDeleteAttachment{}, mcpbroker.AttachReattached, nil
}

func (b *drainingDeleteBroker) DeleteSessionIfBinding(context.Context, session.SessionID, session.ExternalBinding) (mcpbroker.DeleteOutcome, error) {
	b.deleted <- struct{}{}
	return mcpbroker.DeleteDeleted, nil
}

type drainingDeleteAttachment struct{ mcpbroker.Attachment }

func (drainingDeleteAttachment) Binding() session.ExternalBinding { return "binding" }
func (drainingDeleteAttachment) Tools() []tool.Tool               { return nil }
func (drainingDeleteAttachment) Close(context.Context) (mcpbroker.CloseOutcome, error) {
	return mcpbroker.CloseClosed, nil
}
