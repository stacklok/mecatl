package mcpbrokergrpc

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

type blockedLifecycleAttachment struct {
	mcpbroker.Attachment
	release chan struct{}
	calls   int
}

func (a *blockedLifecycleAttachment) Close(ctx context.Context) (mcpbroker.CloseOutcome, error) {
	a.calls++
	select {
	case <-a.release:
		return mcpbroker.CloseClosed, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *blockedLifecycleAttachment) Abort(ctx context.Context) error {
	_, err := a.Close(ctx)
	return err
}

func TestLifecycleDuplicateAdmission(t *testing.T) {
	for _, operation := range []string{"Close", "Abort"} {
		for _, capacity := range []int{1, 2} {
			t.Run(operation+"/"+string(rune('0'+capacity)), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					backing := &blockedLifecycleAttachment{release: make(chan struct{})}
					defer close(backing.release)
					a := &serverHandle{sessionHandle: backing, expiresAt: time.Now().Add(time.Hour), changed: make(chan struct{})}
					cfg := DefaultConfig()
					cfg.MaxPendingControls = capacity
					s := &Server{cfg: cfg, instanceID: "instance", handles: map[string]*serverHandle{"handle": a}}
					invoke := func(ctx context.Context) error {
						if operation == "Abort" {
							_, err := s.Abort(ctx, &brokerv1.AbortRequest{Handle: "handle", BrokerIncarnation: "instance"})
							return err
						}
						out, err := s.Close(ctx, &brokerv1.CloseRequest{Handle: "handle", BrokerIncarnation: "instance"})
						if err == nil && out.GetOutcome() != string(mcpbroker.CloseClosed) {
							t.Errorf("close outcome = %q", out.GetOutcome())
						}
						return err
					}
					first := make(chan error, 1)
					go func() { first <- invoke(t.Context()) }()
					synctest.Wait()
					if backing.calls != 1 || s.pendingControls != 1 {
						t.Fatalf("running calls=%d controls=%d", backing.calls, s.pendingControls)
					}
					duplicate := make(chan error, 1)
					if capacity == 2 {
						ctx, cancel := context.WithCancel(t.Context())
						go func() { duplicate <- invoke(ctx) }()
						synctest.Wait()
						if s.pendingControls != 2 {
							cancel()
							t.Fatalf("waiting duplicate controls=%d, want 2", s.pendingControls)
						}
						cancel()
						if err := <-duplicate; status.Code(err) != codes.Canceled {
							t.Fatalf("cancelled duplicate: %v", err)
						}
						if s.pendingControls != 1 {
							t.Fatalf("after cancellation controls=%d", s.pendingControls)
						}
						go func() { duplicate <- invoke(t.Context()) }()
						synctest.Wait()
					}
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					err := invoke(ctx)
					reason, _, _, reasonErr := brokerReason(err)
					if status.Code(err) != codes.ResourceExhausted || reasonErr != nil || reason != brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED {
						t.Fatalf("excess duplicate = %v, want structured ResourceExhausted", err)
					}
					backing.release <- struct{}{}
					if err := <-first; err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					if capacity == 2 {
						if err := <-duplicate; err != nil {
							t.Fatalf("joined duplicate: %v", err)
						}
					}
					if err := invoke(t.Context()); err != nil {
						t.Fatalf("terminal replay: %v", err)
					}
					if backing.calls != 1 || s.pendingControls != 0 || a.active != 0 {
						t.Fatalf("settled calls=%d controls=%d active=%d", backing.calls, s.pendingControls, a.active)
					}
				})
			})
		}
	}
}
