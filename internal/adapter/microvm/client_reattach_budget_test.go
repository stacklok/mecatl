package microvm

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestReattachWholeColdAcquisitionUsesCallerBudgetNotHotResolveBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		delay    time.Duration
		deadline time.Duration
		cancelAt time.Duration
		wantErr  error
	}{
		{name: "hot resolve", delay: time.Second},
		{name: "cold acquisition without deadline", delay: 30 * time.Second},
		{name: "cold acquisition within caller deadline", delay: 30 * time.Second, deadline: time.Minute},
		{name: "caller cancels cold acquisition", delay: 30 * time.Second, cancelAt: 20 * time.Second, wantErr: context.Canceled},
		{name: "short caller deadline", delay: 30 * time.Second, deadline: 5 * time.Second, wantErr: context.DeadlineExceeded},
		{name: "caller deadline during cold acquisition", delay: 30 * time.Second, deadline: 20 * time.Second, wantErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, err := NewPlacementProvider("unix:///test-microvmd", "/source", "microvm-local", "deployment", nil)
				if err != nil {
					t.Fatal(err)
				}
				clientConn, daemonConn := net.Pipe()
				defer clientConn.Close()
				defer daemonConn.Close()
				client.acquisitionDial = func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }
				claim := binding{Owner: `["","alice"]`, SessionID: "placement", EnvironmentID: "logical-existing", Ref: "logical-existing@7", Generation: 7}
				const acquisitionID = "0123456789abcdef0123456789abcdef"
				active, detaches := 0, 0
				go func() {
					defer daemonConn.Close()
					var request lifecycleRequest
					if err := readFrame(daemonConn, &request); err != nil {
						t.Error(err)
						return
					}
					if request.Operation != "resolve" || request.Binding != claim || request.AcquisitionID != "" || request.Provision == nil || *request.Provision != (provisionRequest{Owner: `["","alice"]`, SessionID: "placement", Profile: "microvm-local", SourceCheckout: "/source"}) {
						t.Errorf("not an exact resolve: %+v", request)
						return
					}
					active++
					defer func() { active-- }()
					// Like the daemon's single terminal reader, EOF cancels cold work
					// before publication; successful publication retains this socket.
					type terminalResult struct {
						request lifecycleRequest
						err     error
					}
					terminal := make(chan terminalResult, 1)
					go func() {
						var release lifecycleRequest
						err := readFrame(daemonConn, &release)
						terminal <- terminalResult{release, err}
					}()
					select {
					case result := <-terminal:
						if !errors.Is(result.err, io.EOF) {
							t.Errorf("abandoned cold acquisition must close, not mutate: %+v", result)
						}
						return
					case <-time.After(tc.delay):
					}
					if err := writeFrame(daemonConn, lifecycleResponse{Binding: claim, AcquisitionID: acquisitionID}); err != nil {
						t.Error(err)
						return
					}
					result := <-terminal
					if result.err != nil || result.request.Operation != "detach" || result.request.Binding != claim || result.request.AcquisitionID != acquisitionID {
						t.Errorf("release lost exact socket ownership: %+v", result)
						return
					}
					detaches++
					if err := writeFrame(daemonConn, lifecycleResponse{Binding: claim, AcquisitionID: acquisitionID}); err != nil {
						t.Error(err)
					}
				}()

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tc.deadline != 0 {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, tc.deadline)
					defer stop()
				}
				if tc.cancelAt != 0 {
					stop := time.AfterFunc(tc.cancelAt, cancel)
					defer stop.Stop()
				}
				start := time.Now()
				placement, err := client.Reattach(ctx, server.PlacementReattachRequest{Ref: refForBinding(claim), Principal: &session.Principal{Subject: "alice"}, Scope: "deployment"})
				elapsed := time.Since(start)
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("Reattach after %s = %v, want %v", elapsed, err, tc.wantErr)
				}
				wantElapsed := tc.delay
				if tc.cancelAt != 0 {
					wantElapsed = tc.cancelAt
				} else if tc.wantErr != nil {
					wantElapsed = tc.deadline
				}
				if elapsed != wantElapsed {
					t.Errorf("whole acquisition took %s, want %s", elapsed, wantElapsed)
				}
				if err == nil {
					if placement.Ref != refForBinding(claim) || placement.Close == nil || placement.Rollback != nil {
						t.Fatalf("reattachment changed ref or rollback authority: %+v", placement)
					}
					cancel()
					synctest.Wait()
					for range 2 {
						if err := placement.Close(); err != nil {
							t.Errorf("detach after caller cancellation: %v", err)
						}
					}
				} else if placement.Ref.Valid() || placement.Close != nil {
					t.Error("failed acquisition published a binding")
				}
				synctest.Wait()
				wantDetaches := 0
				if err == nil {
					wantDetaches = 1
				}
				if active != 0 || detaches != wantDetaches {
					t.Errorf("active=%d detaches=%d, want 0/%d", active, detaches, wantDetaches)
				}
				var extra lifecycleRequest
				if err := readFrame(clientConn, &extra); err == nil {
					t.Error("owner socket remained open")
				}
			})
		})
	}
}
