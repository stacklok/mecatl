package mcpbrokergrpc_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestInvariant_singleton_broker_method_specific_rpc_contract(t *testing.T) {
	service := brokerv1.File_mecatl_broker_v1_broker_proto.Services().ByName("BrokerService")
	if service == nil {
		t.Fatal("BrokerService descriptor is missing")
	}
	inputs := make(map[protoreflect.FullName]protoreflect.Name)
	outputs := make(map[protoreflect.FullName]protoreflect.Name)
	for i := 0; i < service.Methods().Len(); i++ {
		method := service.Methods().Get(i)
		if previous, ok := inputs[method.Input().FullName()]; ok {
			t.Errorf("methods %s and %s share request %s", previous, method.Name(), method.Input().FullName())
		}
		if previous, ok := outputs[method.Output().FullName()]; ok {
			t.Errorf("methods %s and %s share response %s", previous, method.Name(), method.Output().FullName())
		}
		inputs[method.Input().FullName()] = method.Name()
		outputs[method.Output().FullName()] = method.Name()
	}
}

func TestSingletonBrokerRemediation_Scenario1_ExecuteReceiptPreventsRedispatch(t *testing.T) {
	local := newFailureBroker()
	local.blockExecute = make(chan struct{})
	cfg := shortConfig()
	cfg.HandleIdleTimeout = 80 * time.Millisecond
	cfg.SweepInterval = 5 * time.Millisecond
	server, err := mcpbrokergrpc.NewServer(local, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "execute-receipt"})
	if err != nil {
		t.Fatal(err)
	}
	req := &brokerv1.ExecuteRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation(), Name: "read", CallId: "call-1", ItemId: "item-1", Args: []byte(`{"v":1}`)}

	responses := make(chan *brokerv1.ExecuteResponse, 2)
	errs := make(chan error, 2)
	var callers sync.WaitGroup
	for range 2 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			response, callErr := server.Execute(context.Background(), req)
			responses <- response
			errs <- callErr
		}()
	}
	deadline := time.After(testWait)
	for local.executeCalls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("Execute owner did not dispatch")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, mismatchErr := server.Execute(t.Context(), &brokerv1.ExecuteRequest{Handle: req.Handle, BrokerIncarnation: req.BrokerIncarnation, Name: req.Name, CallId: req.CallId, ItemId: req.ItemId, Args: []byte(`{"v":2}`)}); status.Code(mismatchErr) != codes.InvalidArgument {
		t.Fatalf("mismatched call identity = %v, want InvalidArgument", mismatchErr)
	}
	close(local.blockExecute)
	callers.Wait()
	close(responses)
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatalf("duplicate Execute: %v", callErr)
		}
	}
	for response := range responses {
		if response.GetResult().GetCallId() != req.CallId || response.GetResult().GetContent() != "ok" {
			t.Fatalf("receipt = %#v, want exact first response", response)
		}
	}
	if _, err := server.Execute(t.Context(), req); err != nil {
		t.Fatalf("receipt replay: %v", err)
	}
	if got := local.executeCalls.Load(); got != 1 {
		t.Fatalf("underlying Execute calls = %d, want 1", got)
	}
	time.Sleep(2 * cfg.HandleIdleTimeout)
	if _, err := server.Execute(t.Context(), req); !hasBrokerReason(err, brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE) {
		t.Fatalf("expired receipt = %v, want structured state_unavailable", err)
	}
	if got := local.executeCalls.Load(); got != 1 {
		t.Fatalf("expired receipt redispatched: calls = %d", got)
	}
}

func TestContinuityUnavailableExactCodeMessageDetailMapping(t *testing.T) {
	continuity := status.New(codes.FailedPrecondition, "broker continuity is not available")
	withContinuity, err := continuity.WithDetails(&brokerv1.BrokerErrorDetail{Reason: brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CONTINUITY_UNAVAILABLE})
	if err != nil {
		t.Fatal(err)
	}
	client := mcpbrokergrpc.NewClient(reasonConn{err: withContinuity.Err()})
	if _, _, err := client.AttachSession(t.Context(), "continuity-unavailable"); !errors.Is(err, mcpbroker.ErrContinuityUnavailable) {
		t.Fatalf("continuity mapping = %v", err)
	}

	client = mcpbrokergrpc.NewClient(reasonConn{err: status.Error(codes.Unavailable, mcpbroker.ErrStateUnavailable.Error())})
	if _, _, err := client.AttachSession(t.Context(), "text-only"); errors.Is(err, mcpbroker.ErrStateUnavailable) {
		t.Fatalf("status text was inferred as structured state loss: %v", err)
	}
	structuredState := status.New(codes.Unavailable, "structured state unavailable")
	withStateDetail, err := structuredState.WithDetails(&brokerv1.BrokerErrorDetail{Reason: brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE})
	if err != nil {
		t.Fatal(err)
	}
	client = mcpbrokergrpc.NewClient(reasonConn{err: withStateDetail.Err()})
	_, _, stateErr := client.AttachSession(t.Context(), "state-unavailable")
	if !errors.Is(stateErr, mcpbroker.ErrStateUnavailable) || errors.Is(stateErr, mcpbroker.ErrBrokerIncarnationLost) {
		t.Fatalf("ordinary structured state loss = %v, want state unavailable without incarnation-loss proof", stateErr)
	}
	structuredIncarnation := status.New(codes.FailedPrecondition, "broker incarnation mismatch")
	withIncarnationDetail, err := structuredIncarnation.WithDetails(&brokerv1.BrokerErrorDetail{Reason: brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_INCARNATION_LOST})
	if err != nil {
		t.Fatal(err)
	}
	client = mcpbrokergrpc.NewClient(reasonConn{err: withIncarnationDetail.Err()})
	_, _, incarnationErr := client.AttachSession(t.Context(), "incarnation-lost")
	if !errors.Is(incarnationErr, mcpbroker.ErrStateUnavailable) || !errors.Is(incarnationErr, mcpbroker.ErrBrokerIncarnationLost) {
		t.Fatalf("structured incarnation loss = %v, want both loss markers", incarnationErr)
	}

	unknown := status.New(codes.FailedPrecondition, "unknown structured reason")
	withDetail, err := unknown.WithDetails(&brokerv1.BrokerErrorDetail{Reason: brokerv1.BrokerErrorReason(999)})
	if err != nil {
		t.Fatal(err)
	}
	client = mcpbrokergrpc.NewClient(reasonConn{err: withDetail.Err()})
	if _, _, err := client.AttachSession(t.Context(), "unknown-reason"); err == nil || errors.Is(err, mcpbroker.ErrStateUnavailable) {
		t.Fatalf("unknown structured reason = %v, want protocol failure", err)
	}
}

func TestContinuityUnavailableMalformedMappingFailsClosed(t *testing.T) {
	for _, malformed := range []error{
		status.Error(codes.FailedPrecondition, "broker continuity is not available"),
		func() error {
			st := status.New(codes.FailedPrecondition, "wrong message")
			with, err := st.WithDetails(&brokerv1.BrokerErrorDetail{Reason: brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CONTINUITY_UNAVAILABLE})
			if err != nil {
				t.Fatal(err)
			}
			return with.Err()
		}(),
	} {
		client := mcpbrokergrpc.NewClient(reasonConn{err: malformed})
		_, _, err := client.AttachSession(t.Context(), "malformed-continuity")
		if err == nil || errors.Is(err, mcpbroker.ErrContinuityUnavailable) {
			t.Fatalf("malformed continuity mapping = %v", err)
		}
	}
}
func hasBrokerReason(err error, want brokerv1.BrokerErrorReason) bool {
	for _, detail := range status.Convert(err).Details() {
		if typed, ok := detail.(*brokerv1.BrokerErrorDetail); ok && typed.GetReason() == want {
			return true
		}
	}
	return false
}

type reasonConn struct{ err error }

func (c reasonConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error { return c.err }
func (reasonConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unexpected stream")
}
