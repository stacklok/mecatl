package mcpbrokergrpc_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestServerLogicalOwnerCapacityRefusesWithoutTakeover(t *testing.T) {
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxOwners = 1
	server, err := mcpbrokergrpc.NewServerWithConfig(newBroker(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	principal := &session.Principal{Subject: "owner-a"}
	ctx := session.WithPrincipal(t.Context(), principal)
	if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "owner-one"}); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "owner-two"}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second owner attach = %v, want ResourceExhausted", err)
	}
	if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "owner-one"}); err != nil {
		t.Fatalf("same owner reattach: %v", err)
	}
}

func TestHandleCapacityAbortsRejectedCreatedLogicalSession(t *testing.T) {
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxHandles = 1
	broker := &abortCountingBroker{}
	server, err := mcpbrokergrpc.NewServerWithConfig(broker, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "owner-a"})
	if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "rejected"}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second attach = %v, want ResourceExhausted", err)
	}
	if got := broker.aborted.Load(); got != 1 {
		t.Fatalf("rejected created attachments aborted = %d, want 1", got)
	}
}

type abortCountingBroker struct{ aborted atomic.Int32 }

func (b *abortCountingBroker) AttachSession(context.Context, session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	return &abortCountingAttachment{owner: b}, mcpbroker.AttachCreated, nil
}

func (*abortCountingBroker) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return mcpbroker.DeleteNotFound, nil
}

type abortCountingAttachment struct {
	attachment
	owner *abortCountingBroker
}

func (a *abortCountingAttachment) Abort(ctx context.Context) error {
	a.owner.aborted.Add(1)
	return a.attachment.Abort(ctx)
}

func TestRetiredLogicalOwnerReleasesCapacityForDifferentSession(t *testing.T) {
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxOwners = 1
	cfg.HandleIdleTimeout = 15 * time.Millisecond
	cfg.OwnerRetention = 30 * time.Millisecond
	cfg.SweepInterval = 5 * time.Millisecond
	server, err := mcpbrokergrpc.NewServerWithConfig(newBroker(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "owner-a"})
	if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "retiring-owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "replacement-owner"}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("capacity before retirement = %v, want ResourceExhausted", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		attached, attachErr := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "replacement-owner"})
		if attachErr == nil {
			if attached.GetOutcome() != string(mcpbroker.AttachCreated) {
				t.Fatalf("replacement attach outcome = %q", attached.GetOutcome())
			}
			break
		}
		if status.Code(attachErr) != codes.ResourceExhausted {
			t.Fatalf("capacity while retirement pending = %v", attachErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("retired logical owner did not release capacity")
		}
		time.Sleep(cfg.SweepInterval)
	}
}

func TestFailedAttachRollsBackNewOwnerAdmission(t *testing.T) {
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxOwners = 1
	service := &failFirstAttach{delegate: newBroker()}
	server, err := mcpbrokergrpc.NewServerWithConfig(service, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "owner-a"})
	if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "failed-owner"}); err == nil {
		t.Fatal("first attach unexpectedly succeeded")
	}
	if _, err := server.Attach(ctx, &brokerv1.AttachRequest{SessionId: "admitted-owner"}); err != nil {
		t.Fatalf("owner retained by failed attach: %v", err)
	}
}

func TestReceiptAggregateByteCapacityRejectsBeforeRetention(t *testing.T) {
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxReceiptBytes = 1
	server, err := mcpbrokergrpc.NewServerWithConfig(newBroker(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "receipt-cap"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.Execute(t.Context(), &brokerv1.ExecuteRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation(), CallId: "call", Name: "read", Args: []byte(`{}`)})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("receipt byte cap = %v, want ResourceExhausted", err)
	}
}

func TestReceiptReservationAdmissionBoundary(t *testing.T) {
	request := &brokerv1.ExecuteRequest{CallId: "reservation-boundary", Name: "exact", Args: []byte(`{"value":"` + strings.Repeat("x", 4096) + `"}`)}
	reservation := invocationBytes(request) + 1024
	for _, test := range []struct {
		name    string
		limit   int
		wantErr codes.Code
	}{
		{name: "exact reservation admitted", limit: reservation, wantErr: codes.OK},
		{name: "one byte short rejected", limit: reservation - 1, wantErr: codes.ResourceExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := mcpbrokergrpc.DefaultConfig()
			cfg.MaxReceiptBytes = test.limit
			server, err := mcpbrokergrpc.NewServerWithConfig(byteExactBroker{}, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
			attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "reservation-boundary"})
			if err != nil {
				t.Fatal(err)
			}
			call := proto.Clone(request).(*brokerv1.ExecuteRequest)
			call.Handle, call.BrokerIncarnation = attached.GetHandle(), attached.GetBrokerIncarnation()
			response, err := server.Execute(t.Context(), call)
			if status.Code(err) != test.wantErr {
				t.Fatalf("Execute error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == codes.OK && (response.GetResult() == nil || !response.GetResult().GetIsError()) {
				t.Fatalf("exact-fit response = %#v, want retained terminal oversize result", response)
			}
		})
	}
}

func TestReceiptResponseBytesAreBoundedAndIdempotent(t *testing.T) {
	request := &brokerv1.ExecuteRequest{CallId: "response-cap", Name: "exact", Args: []byte(`{"value":"` + strings.Repeat("x", 2048) + `"}`)}
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxReceiptBytes = invocationBytes(request) + 1024
	server, err := mcpbrokergrpc.NewServerWithConfig(byteExactBroker{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "response-cap"})
	if err != nil {
		t.Fatal(err)
	}
	request.Handle, request.BrokerIncarnation = attached.GetHandle(), attached.GetBrokerIncarnation()
	for attempt := 0; attempt < 2; attempt++ {
		response, err := server.Execute(t.Context(), request)
		if err != nil || response.GetResult() == nil || !response.GetResult().GetIsError() || !strings.Contains(response.GetResult().GetContent(), "exceeded the broker retained-receipt limit") {
			t.Fatalf("attempt %d = (%#v, %v), want immutable terminal oversize result", attempt, response, err)
		}
	}
}

func TestReceiptAggregateCountsTerminalResponses(t *testing.T) {
	first := &brokerv1.ExecuteRequest{CallId: "near-one", Name: "exact", Args: []byte(`{"value":"` + strings.Repeat("x", 2048) + `"}`)}
	second := &brokerv1.ExecuteRequest{CallId: "near-two", Name: "exact", Args: []byte(`{"value":"` + strings.Repeat("x", 2048) + `"}`)}
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxReceiptBytes = invocationBytes(first) + 1024
	server, err := mcpbrokergrpc.NewServerWithConfig(byteExactBroker{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "near-cap"})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*brokerv1.ExecuteRequest{first, second} {
		request.Handle, request.BrokerIncarnation = attached.GetHandle(), attached.GetBrokerIncarnation()
	}
	if response, err := server.Execute(t.Context(), first); err != nil || !response.GetResult().GetIsError() {
		t.Fatalf("first terminal response = (%#v, %v)", response, err)
	}
	if _, err := server.Execute(t.Context(), second); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second response = %v, want ResourceExhausted", err)
	}
}

func TestReceiptBinaryPartBytesAreBounded(t *testing.T) {
	request := &brokerv1.ExecuteRequest{CallId: "binary-cap", Name: "binary", Args: []byte(`{}`)}
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxReceiptBytes = invocationBytes(request) + 1024
	server, err := mcpbrokergrpc.NewServerWithConfig(binaryReceiptBroker{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "binary-cap"})
	if err != nil {
		t.Fatal(err)
	}
	request.Handle, request.BrokerIncarnation = attached.GetHandle(), attached.GetBrokerIncarnation()
	response, err := server.Execute(t.Context(), request)
	if err != nil || response.GetResult() == nil || !response.GetResult().GetIsError() || !strings.Contains(response.GetResult().GetContent(), "retained-receipt limit") {
		t.Fatalf("binary oversize terminal = (%#v, %v)", response, err)
	}
}

type binaryReceiptBroker struct{}

func (binaryReceiptBroker) AttachSession(context.Context, session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	return &binaryReceiptAttachment{}, mcpbroker.AttachCreated, nil
}

func (binaryReceiptBroker) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return mcpbroker.DeleteNotFound, nil
}

type binaryReceiptAttachment struct{ attachment }

func (*binaryReceiptAttachment) Tools() []tool.Tool { return []tool.Tool{binaryReceiptTool{}} }

type binaryReceiptTool struct{}

func (binaryReceiptTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "binary", Schema: []byte(`{"type":"object"}`)}
}
func (binaryReceiptTool) ReadOnly() bool { return true }
func (binaryReceiptTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResultWithParts(call.ID, "binary", []session.Content{{BlockKind: session.BlockEmbeddedResource, MIMEType: "application/octet-stream", Data: make([]byte, 4096)}}), nil
}

func invocationBytes(request *brokerv1.ExecuteRequest) int {
	return len(request.GetName()) + len(request.GetCallId()) + len(request.GetItemId()) + len(request.GetArgs())
}

func TestRetainedInvocationInputsAreBounded(t *testing.T) {
	remote := newRemote(t, newBroker())
	attachment, _, err := remote.AttachSession(t.Context(), "bounded-inputs")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []session.ToolCall{
		session.NewToolCall(session.ToolCallID(strings.Repeat("c", 257)), "read", []byte(`{}`)),
		session.NewToolCall("long-name", strings.Repeat("n", 257), []byte(`{}`)),
		session.NewToolCall("large-args", "read", []byte(`{"value":"`+strings.Repeat("x", 256<<10)+`"}`)),
		session.NewToolCall("scalar", "read", []byte(`[]`)),
	} {
		if _, err := attachment.Tools()[0].Execute(t.Context(), call, tool.Environment{}); err == nil {
			t.Fatalf("unbounded invocation %+v was accepted", call)
		}
	}
}

type failFirstAttach struct {
	delegate mcpbroker.Service
	failed   bool
}

func (s *failFirstAttach) AttachSession(ctx context.Context, id session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	if !s.failed {
		s.failed = true
		return nil, "", errors.New("attach failed")
	}
	return s.delegate.AttachSession(ctx, id)
}

func (s *failFirstAttach) DeleteSession(ctx context.Context, id session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return s.delegate.DeleteSession(ctx, id)
}
