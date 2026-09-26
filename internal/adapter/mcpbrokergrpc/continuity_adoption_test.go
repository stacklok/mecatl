package mcpbrokergrpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestContinuityClientUnimplementedIsProtocolError(t *testing.T) {
	err := continuityClientError(status.Error(codes.Unimplemented, "missing continuity RPC"))
	if !errors.Is(err, mcpbroker.ErrContinuityProtocol) || errors.Is(err, mcpbroker.ErrContinuityUnavailable) {
		t.Fatalf("unimplemented continuity error = %v, want protocol error only", err)
	}
}

func TestCredentialContinuityRecoveryAdoptsReplacementIncarnation(t *testing.T) {
	client := &Client{instanceID: "old-incarnation"}
	response := continuityAttachResponse("replacement-incarnation")
	_, outcome, err := client.attachResponse(response, true, "old-incarnation")
	if err != nil || outcome != mcpbroker.AttachRecoveredProvisional {
		t.Fatalf("replacement adoption = (%q, %v)", outcome, err)
	}
	if got := client.brokerInstanceID(); got != "replacement-incarnation" {
		t.Fatalf("adopted incarnation = %q", got)
	}
}

func TestExpectedBindingClientRejectsCreatedOutcome(t *testing.T) {
	client := NewClient(continuityDiscardConn{})
	response := continuityAttachResponse("incarnation")
	response.Outcome = string(mcpbroker.AttachCreated)
	if _, _, err := client.attachResponse(response, true, ""); err == nil {
		t.Fatal("expected-binding response with created outcome unexpectedly succeeded")
	}
}

func TestCredentialContinuityConflictingIncarnationAdoptionFailsClosed(t *testing.T) {
	client := NewClient(continuityDiscardConn{})
	client.instanceID = "concurrent-incarnation"
	_, _, err := client.attachResponse(continuityAttachResponse("replacement-incarnation"), true, "old-incarnation")
	if !errors.Is(err, mcpbroker.ErrStateUnavailable) || !errors.Is(err, mcpbroker.ErrBrokerIncarnationLost) {
		t.Fatalf("conflicting adoption error = %v", err)
	}
	if got := client.brokerInstanceID(); got != "concurrent-incarnation" {
		t.Fatalf("conflicting response changed incarnation to %q", got)
	}
}

type continuityDiscardConn struct{}

func (continuityDiscardConn) Invoke(_ context.Context, _ string, _, reply any, _ ...grpc.CallOption) error {
	if out, ok := reply.(*brokerv1.AbortResponse); ok {
		*out = brokerv1.AbortResponse{}
	}
	return nil
}
func (continuityDiscardConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unexpected stream")
}
func continuityAttachResponse(incarnation string) *brokerv1.AttachResponse {
	return &brokerv1.AttachResponse{
		Handle: "recovered-handle", Binding: "recovered-binding", BrokerIncarnation: incarnation,
		Outcome: string(mcpbroker.AttachRecoveredProvisional),
		Tools:   []*brokerv1.ToolDescriptor{{Name: "read", Description: "read", Schema: []byte(`{"type":"object"}`), ReadOnly: true}},
	}
}
