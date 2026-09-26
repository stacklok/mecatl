package microvm

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestAcquisitionCancellationReleasesEveryReturnedOwner(t *testing.T) {
	for _, operation := range []string{"resolve", "create", "fork"} {
		for _, stopped := range []bool{true, false} {
			t.Run(operation+map[bool]string{true: "/callback-stopped", false: "/callback-started"}[stopped], func(t *testing.T) {
				socket := testUnixSocketPath(t)
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				requests := make(chan lifecycleRequest, 8)
				var active atomic.Int32
				var handlers sync.WaitGroup
				accepted := make(chan struct{})
				go func() {
					defer close(accepted)
					for {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						handlers.Add(1)
						go func() {
							defer handlers.Done()
							defer conn.Close()
							var request lifecycleRequest
							if readFrame(conn, &request) != nil {
								return
							}
							if request.Operation == "delete" {
								requests <- request
								_ = writeFrame(conn, lifecycleResponse{Binding: request.Binding, Payload: []byte(`{"worktree_retained":false}`)})
								return
							}
							active.Add(1)
							defer active.Add(-1)
							claim := request.Binding
							if request.Operation == "create" {
								claim.EnvironmentID = "logical-created"
								claim.Ref = "logical-created@7"
								claim.Generation = 7
							}
							if request.Operation == "fork" {
								claim.SessionID += ":branch"
								claim.EnvironmentID = "logical-existing-sibling"
								claim.Ref = "logical-existing-sibling@7"
							}
							if writeFrame(conn, lifecycleResponse{Binding: claim, AcquisitionID: "0123456789abcdef0123456789abcdef"}) != nil {
								return
							}
							if readFrame(conn, &request) != nil {
								return
							}
							requests <- request
							_ = writeFrame(conn, lifecycleResponse{Binding: request.Binding, AcquisitionID: request.AcquisitionID, Payload: []byte(`{"worktree_retained":false}`)})
						}()
					}
				}()
				client, err := NewPlacementProvider("unix://"+socket, "/source", "microvm-local", "deployment", nil)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var cancellation func()
				// Control the two possible AfterFunc stop outcomes without scheduler timing.
				client.acquisitionAfterFunc = func(_ context.Context, f func()) func() bool { cancellation = f; return func() bool { return stopped } }
				client.acquisitionResponseDecoded = func() {
					cancel()
					if !stopped {
						cancellation()
					}
				}
				switch operation {
				case "resolve":
					_, err = client.Reattach(ctx, server.PlacementReattachRequest{Ref: session.EnvironmentRef{Kind: kindMicroVM, ID: "placement.logical-parent", Revision: "7"}, Scope: "deployment"})
				case "create":
					_, err = client.Bind(ctx, server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "deployment", Operation: server.PlacementOperationCreate})
				case "fork":
					claim := binding{Owner: "local", SessionID: "placement", EnvironmentID: "logical-parent", Ref: "logical-parent@7", Generation: 7}
					_, _, _, err = client.Fork(ctx, client.environment(refForBinding(claim), claim), "branch")
				}
				listener.Close()
				<-accepted
				joined := make(chan struct{})
				go func() { handlers.Wait(); close(joined) }()
				select {
				case <-joined:
				case <-time.After(time.Second):
					cancellation()
					<-joined
					t.Fatal("cancelled acquisition leaked retained socket/active registration")
				}
				if err == nil || !strings.Contains(err.Error(), "canceled") {
					t.Errorf("cancellation result = %v", err)
				}
				if active.Load() != 0 {
					t.Errorf("active registrations = %d", active.Load())
				}
				close(requests)
				count := 0
				for request := range requests {
					count++
					want := "detach"
					if operation == "create" {
						want = "delete"
					}
					if operation == "fork" {
						want = "child-delete"
					}
					if request.Operation != want {
						t.Errorf("terminal operation = %s, want %s", request.Operation, want)
					}
					if operation == "fork" && !stopped {
						t.Errorf("cancelled socket granted ref-only deletion of sibling: %+v", request)
					}
					if stopped && request.AcquisitionID == "" {
						t.Error("lost originating-socket rollback authority")
					}
				}
				wantCount := 1
				if !stopped && operation != "create" {
					wantCount = 0
				}
				if count != wantCount {
					t.Errorf("cleanup operations = %d, want %d", count, wantCount)
				}
				if operation == "fork" && !stopped && !strings.Contains(err.Error(), "automatic cleanup was not authorized") {
					t.Errorf("missing retained/uncertain diagnostic: %v", err)
				}
			})
		}
	}
}
