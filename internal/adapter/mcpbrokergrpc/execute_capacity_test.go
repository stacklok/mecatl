package mcpbrokergrpc_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestExecuteCapacityRetainsRejectionAndRecovers(t *testing.T) {
	gate := make(chan struct{})
	service := &executeLimitBroker{gate: gate, started: make(chan struct{})}
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxActiveExecutes = 1
	server, err := mcpbrokergrpc.NewServer(service, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached := attachExecuteLimit(t, server, "execute-limit")

	firstDone := make(chan error, 1)
	go func() {
		_, err := server.Execute(t.Context(), executeLimitRequest(attached, "first"))
		firstDone <- err
	}()
	<-service.started

	capacityRequest := executeLimitRequest(attached, "capacity")
	for attempt := 0; attempt < 2; attempt++ {
		_, err := server.Execute(t.Context(), capacityRequest)
		if status.Code(err) != codes.ResourceExhausted || !hasBrokerReason(err, brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED) {
			t.Fatalf("capacity receipt attempt %d = %v, want structured ResourceExhausted", attempt, err)
		}
	}
	if got := service.dispatches.Load(); got != 1 {
		t.Fatalf("dispatches while saturated = %d, want 1", got)
	}

	close(gate)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if _, err := server.Execute(t.Context(), executeLimitRequest(attached, "after-release")); err != nil {
		t.Fatalf("Execute after release: %v", err)
	}
	if got := service.dispatches.Load(); got != 2 {
		t.Fatalf("dispatches after recovery = %d, want 2", got)
	}
	if _, err := server.Execute(t.Context(), capacityRequest); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("capacity receipt changed after recovery: %v", err)
	}
}

func TestExecuteCapacityReleasedAfterPanic(t *testing.T) {
	service := &executeLimitBroker{panicFirst: true, gate: closedSignal(), started: make(chan struct{})}
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxActiveExecutes = 1
	server, err := mcpbrokergrpc.NewServer(service, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached := attachExecuteLimit(t, server, "execute-panic")

	panicRequest := executeLimitRequest(attached, "panic")
	if _, err := server.Execute(t.Context(), panicRequest); status.Code(err) != codes.Internal {
		t.Fatalf("panicking Execute = %v, want Internal receipt", err)
	}
	if _, err := server.Execute(t.Context(), executeLimitRequest(attached, "recovered")); err != nil {
		t.Fatalf("Execute after panic: %v", err)
	}
	if _, err := server.Execute(t.Context(), panicRequest); status.Code(err) != codes.Internal {
		t.Fatalf("panic receipt replay = %v, want immutable Internal", err)
	}
	if got := service.dispatches.Load(); got != 2 {
		t.Fatalf("panic replay redispatched: %d calls, want 2", got)
	}
}

func TestExecuteCapacityShutdownCancelsAndJoinsActiveSlot(t *testing.T) {
	service := &executeLimitBroker{waitForCancel: true, started: make(chan struct{})}
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxActiveExecutes = 1
	server, err := mcpbrokergrpc.NewServer(service, cfg)
	if err != nil {
		t.Fatal(err)
	}
	attached := attachExecuteLimit(t, server, "execute-shutdown")
	executeDone := make(chan error, 1)
	go func() {
		_, err := server.Execute(context.Background(), executeLimitRequest(attached, "active"))
		executeDone <- err
	}()
	<-service.started

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-executeDone; status.Code(err) != codes.Canceled {
		t.Fatalf("active Execute after shutdown = %v, want Canceled", err)
	}
	if got := service.dispatches.Load(); got != 1 {
		t.Fatalf("shutdown dispatches = %d, want 1", got)
	}
}

func TestExecuteReceiptPollingDoesNotExtendAbsoluteHandleExpiry(t *testing.T) {
	service := &executeLimitBroker{gate: closedSignal(), started: make(chan struct{})}
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.HandleIdleTimeout = 70 * time.Millisecond
	cfg.SweepInterval = 5 * time.Millisecond
	server, err := mcpbrokergrpc.NewServer(service, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached := attachExecuteLimit(t, server, "absolute-receipt-expiry")
	request := executeLimitRequest(attached, "receipt")
	if _, err := server.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(cfg.HandleIdleTimeout)
	polls := 0
	for time.Now().Before(deadline) {
		if _, err := server.Execute(t.Context(), request); err != nil {
			if status.Code(err) == codes.FailedPrecondition {
				break
			}
			t.Fatalf("receipt poll: %v", err)
		}
		polls++
		time.Sleep(5 * time.Millisecond)
	}
	if polls < 3 {
		t.Fatalf("receipt polls before expiry = %d, want repeated polling", polls)
	}
	var expiryErr error
	for limit := time.Now().Add(3 * cfg.SweepInterval); time.Now().Before(limit); time.Sleep(time.Millisecond) {
		_, expiryErr = server.Execute(t.Context(), request)
		if status.Code(expiryErr) == codes.FailedPrecondition {
			break
		}
	}
	if status.Code(expiryErr) != codes.FailedPrecondition || !hasBrokerReason(expiryErr, brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE) {
		t.Fatalf("receipt after original deadline = %v, want structured state unavailable", expiryErr)
	}
	for limit := time.Now().Add(3 * cfg.SweepInterval); service.closes.Load() == 0 && time.Now().Before(limit); time.Sleep(time.Millisecond) {
	}
	if got := service.closes.Load(); got != 1 {
		t.Fatalf("expiry lifecycle count = %d, want 1", got)
	}
	for i := 0; i < 3; i++ {
		_, _ = server.Execute(t.Context(), request)
		_, _ = server.Close(t.Context(), &brokerv1.CloseRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation()})
	}
	if got := service.dispatches.Load(); got != 1 {
		t.Fatalf("post-expiry replay changed dispatch count to %d, want 1", got)
	}
	if got := service.closes.Load(); got != 1 {
		t.Fatalf("post-expiry polling changed lifecycle count to %d, want 1", got)
	}
}

type executeLimitBroker struct {
	gate          chan struct{}
	started       chan struct{}
	startedOnce   sync.Once
	dispatches    atomic.Int32
	closes        atomic.Int32
	panicFirst    bool
	waitForCancel bool
}

func (b *executeLimitBroker) AttachSession(context.Context, session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	return &executeLimitAttachment{broker: b}, mcpbroker.AttachCreated, nil
}
func (*executeLimitBroker) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return mcpbroker.DeleteNotFound, nil
}

type executeLimitAttachment struct {
	attachment
	broker *executeLimitBroker
}

func (*executeLimitAttachment) Binding() session.ExternalBinding { return "binding" }
func (*executeLimitAttachment) Commit(context.Context) error     { return nil }
func (*executeLimitAttachment) Abort(context.Context) error      { return nil }
func (a *executeLimitAttachment) Close(context.Context) (mcpbroker.CloseOutcome, error) {
	a.broker.closes.Add(1)
	return mcpbroker.CloseClosed, nil
}
func (a *executeLimitAttachment) Tools() []tool.Tool {
	return []tool.Tool{executeLimitTool{broker: a.broker}}
}

type executeLimitTool struct{ broker *executeLimitBroker }

func (executeLimitTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "limited", Schema: []byte(`{"type":"object"}`)}
}
func (executeLimitTool) ReadOnly() bool { return true }
func (t executeLimitTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	n := t.broker.dispatches.Add(1)
	t.broker.startedOnce.Do(func() {
		if t.broker.started == nil {
			t.broker.started = make(chan struct{})
		}
		close(t.broker.started)
	})
	if t.broker.panicFirst && n == 1 {
		panic("planted Execute panic")
	}
	if t.broker.waitForCancel {
		<-ctx.Done()
		return session.ToolResult{}, ctx.Err()
	}
	if t.broker.gate != nil {
		select {
		case <-t.broker.gate:
		case <-ctx.Done():
			return session.ToolResult{}, ctx.Err()
		}
	}
	return session.NewToolResult(call.ID, "ok"), nil
}

func attachExecuteLimit(t *testing.T, server *mcpbrokergrpc.Server, id string) *brokerv1.AttachResponse {
	t.Helper()
	attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: id})
	if err != nil {
		t.Fatal(err)
	}
	return attached
}

func executeLimitRequest(attached *brokerv1.AttachResponse, callID string) *brokerv1.ExecuteRequest {
	return &brokerv1.ExecuteRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation(), CallId: callID, Name: "limited", Args: []byte(`{}`)}
}

func closedSignal() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
