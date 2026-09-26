package mcpbrokergrpc_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestRemoteWorkspaceObserveTransportDeadlineAndSameHandleRetry(t *testing.T) {
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.RPCDeadline = 400 * time.Millisecond
	broker := &timeoutObserveBroker{attachment: &timeoutObserveAttachment{
		ref:      mcpbroker.WorkspaceEnrollmentRef{ID: "transport-enrollment", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)},
		entered:  make(chan struct{}),
		finished: make(chan struct{}),
	}}
	remote := newDeadlineRemote(t, broker, cfg)
	attached, _, err := remote.AttachSession(t.Context(), "transport-session")
	if err != nil {
		t.Fatal(err)
	}
	enroller := attached.(mcpbroker.WorkspaceEnrollmentAttachment)
	presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	first := make(chan error, 1)
	go func() {
		_, observeErr := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
		first <- observeErr
	}()
	select {
	case <-broker.attachment.entered:
	case err := <-first:
		t.Fatalf("Observe returned before the fake attachment blocked: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Observe never reached the fake attachment")
	}
	select {
	case err := <-first:
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("Observe error = %v, want RPC deadline exceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Observe did not respect RPCDeadline")
	}
	select {
	case <-broker.attachment.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("timed-out RPC did not cancel the server-side observation")
	}

	result, err := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || result.Status != mcpbroker.WorkspaceEnrollmentConnected || result.Catalogue == nil {
		t.Fatalf("same-handle retry = (%+v, %v)", result, err)
	}
	if result.Ref.ID != presentation.Ref.ID || result.Ref.RequiredServices != presentation.Ref.RequiredServices || !result.Ref.ExpiresAt.Equal(presentation.Ref.ExpiresAt) {
		t.Fatalf("retry returned a different reference: %+v", result.Ref)
	}
	if names := result.Catalogue.ToolNames(); len(names) != 1 || names[0] != "enrolled_only" {
		t.Fatalf("retry catalogue = %v, want [enrolled_only]", names)
	}
	broker.attachment.mu.Lock()
	attempts := broker.attachment.attempts
	broker.attachment.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("server-side Observe calls = %d, want 2", attempts)
	}
}

type timeoutObserveBroker struct{ attachment *timeoutObserveAttachment }

func (b *timeoutObserveBroker) AttachSession(context.Context, session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	return b.attachment, mcpbroker.AttachCreated, nil
}
func (*timeoutObserveBroker) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return mcpbroker.DeleteNotFound, nil
}

type timeoutObserveAttachment struct {
	enrollmentAttachment
	mu       sync.Mutex
	ref      mcpbroker.WorkspaceEnrollmentRef
	attempts int
	entered  chan struct{}
	finished chan struct{}
}

func (a *timeoutObserveAttachment) BeginWorkspaceEnrollment(context.Context) (mcpbroker.WorkspaceEnrollmentPresentation, error) {
	return mcpbroker.WorkspaceEnrollmentPresentation{Ref: a.ref, URL: "https://broker.invalid/enroll"}, nil
}

func (a *timeoutObserveAttachment) ObserveWorkspaceEnrollment(ctx context.Context, ref mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	if ref.ID != a.ref.ID || ref.RequiredServices != a.ref.RequiredServices || !ref.ExpiresAt.Equal(a.ref.ExpiresAt) {
		return mcpbroker.WorkspaceEnrollmentResult{}, mcpbroker.ErrAuthorizationNotFound
	}
	a.mu.Lock()
	a.attempts++
	attempt := a.attempts
	a.mu.Unlock()
	if attempt == 1 {
		close(a.entered)
		<-ctx.Done()
		close(a.finished)
		return mcpbroker.WorkspaceEnrollmentResult{}, ctx.Err()
	}
	catalogue, err := mcpbroker.NewWorkspaceCatalogue(ref, append(a.Tools(), enrolledOnlyTool{}))
	if err != nil {
		return mcpbroker.WorkspaceEnrollmentResult{}, err
	}
	return mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentConnected, Catalogue: catalogue}, nil
}

func newDeadlineRemote(t *testing.T, broker mcpbroker.Service, cfg mcpbrokergrpc.Config) mcpbroker.Service {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	brokerServer, err := mcpbrokergrpc.NewServer(broker, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	mcpbrokergrpc.RegisterServer(server, brokerServer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = brokerServer.Shutdown(context.Background())
		server.Stop()
		_ = listener.Close()
	})
	conn, err := grpc.NewClient("passthrough:///broker", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := mcpbrokergrpc.NewClientWithConfig(conn, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

var _ mcpbroker.Service = (*timeoutObserveBroker)(nil)
var _ mcpbroker.WorkspaceEnrollmentAttachment = (*timeoutObserveAttachment)(nil)
