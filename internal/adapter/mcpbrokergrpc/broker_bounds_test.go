package mcpbrokergrpc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
