package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
)

type pinDrainWorkspace struct {
	tool.Workspace
	entered chan struct{}
	finish  chan struct{}
}

func (w *pinDrainWorkspace) ReadVersion(ctx context.Context, _ string) ([]byte, tool.FileVersion, error) {
	close(w.entered)
	select {
	case <-w.finish:
		return []byte("operation finished"), tool.NewFileVersion("finished"), nil
	case <-ctx.Done():
		return nil, tool.FileVersion{}, ctx.Err()
	}
}

func TestFinalRetirementWaitsForOperationBeforeCleanupTimeout(t *testing.T) {
	for _, mode := range []string{"abandoned-resolve", "explicit-detach", "daemon-shutdown"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fixture := newOwnershipFixture(t)
				attachment := fixture.daemon.repositoryAttachment(fixture.binding)
				ws := &pinDrainWorkspace{Workspace: attachment.Environment.Workspace(), entered: make(chan struct{}), finish: make(chan struct{})}
				attachment.Environment = tool.MustEnvironment(attachment.Environment.Ref(), ws, memledger.New(), nil)
				owner := acquireTestOwner(t, fixture)
				defer owner.conn.Close()
				operation := make(chan LifecycleResponse, 1)
				go func() {
					operation <- repairExchange(t, fixture.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleWorkspace, Binding: fixture.binding, AcquisitionID: owner.id, Payload: []byte(`{"operation":"read","path":"marker"}`)})
				}()
				<-ws.entered

				daemonCtx, shutdown := context.WithCancel(t.Context())
				defer shutdown()
				retired := make(chan error, 1)
				if mode == "explicit-detach" {
					go func() {
						response := closeTestOwner(t, fixture, owner)
						var err error
						if response.ErrorCode != "" {
							err = errors.New(response.ErrorText)
						}
						retired <- err
					}()
				} else {
					prepared := make(chan struct{})
					fixture.daemon.beforeAcquisitionPublish = func(ctx context.Context) { close(prepared); <-ctx.Done() }
					server, borrower := net.Pipe()
					go func() { retired <- fixture.daemon.ServeConn(daemonCtx, server) }()
					codec := control.NewCodec(control.DefaultMaxMessageBytes)
					if err := codec.Write(borrower, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: fixture.binding}); err != nil {
						t.Fatal(err)
					}
					<-prepared
					if response := closeTestOwner(t, fixture, owner); response.ErrorCode != "" {
						t.Fatal(response.ErrorText)
					}
					borrower.Close()
				}
				synctest.Wait()
				fixture.daemon.ownershipMu.Lock()
				state := fixture.daemon.refOwnership[fixture.binding.Ref]
				waiting := state != nil && state.phase == refPhaseDetaching && state.pins == 1 && state.resolves == 0 && len(state.owners) == 0
				fixture.daemon.ownershipMu.Unlock()
				if !waiting {
					t.Error("final retirement did not wait for the live operation")
				}

				if mode == "daemon-shutdown" {
					shutdown()
					synctest.Wait()
					select {
					case err := <-retired:
						if !errors.Is(err, context.Canceled) {
							t.Errorf("shutdown retirement error = %v", err)
						}
					default:
						t.Error("daemon shutdown did not cancel the pin wait")
					}
				} else {
					// Advance fake time only after the operation and final retire are blocked.
					time.Sleep(repositoryRollbackTimeout + time.Second)
					synctest.Wait()
				}
				if fixture.guest.unregisters.Load() != 0 {
					t.Error("retirement unregistered a still-running operation")
				}
				close(ws.finish)
				response := <-operation
				var result proxyWorkspaceResponse
				if err := json.Unmarshal(response.Payload, &result); err != nil || response.ErrorCode != "" || string(result.Data) != "operation finished" {
					t.Errorf("owner retirement cancelled or broke the operation: %+v, %v", response, err)
				}
				synctest.Wait()
				if mode == "daemon-shutdown" {
					return
				}
				if err := <-retired; errors.Is(err, context.DeadlineExceeded) || (mode == "explicit-detach" && err != nil) {
					t.Errorf("pin wait consumed the cleanup timeout: %v", err)
				}
				fixture.daemon.ownershipMu.Lock()
				owners, refs := len(fixture.daemon.acquisitions), len(fixture.daemon.refOwnership)
				fixture.daemon.ownershipMu.Unlock()
				if fixture.guest.unregisters.Load() != 1 || owners != 0 || refs != 0 || fixture.daemon.repositoryAttachment(fixture.binding) != nil {
					t.Errorf("operation completion did not automatically unregister: unregisters=%d owners=%d refs=%d", fixture.guest.unregisters.Load(), owners, refs)
				}
			})
		})
	}
}
