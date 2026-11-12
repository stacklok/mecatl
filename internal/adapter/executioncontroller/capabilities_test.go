package executioncontroller

import (
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
)

func TestResponsesCarryAdmittedCapabilitiesAndRequestsAreRefused(t *testing.T) {
	const clientID = "spiffe://cluster/ns/allowed"
	backend := newFakeBackend()
	policy := ClientPolicy{MayAttestOwner: true, ExecutionTemplates: []string{"go"}}
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{clientID: policy}}, backend)
	ctx := authenticatedContext(clientID)
	owner := &executionv1.Owner{Issuer: "issuer", Subject: "alice"}
	want := []string{"filesystem", "foreground-command"}
	q := &executionv1.EnsureTemplateRequest{
		BindingId: "binding", Owner: owner, OperationId: "op",
		Template:     &executionv1.TemplateSelector{Id: "go", Revision: testTemplateRevision},
		Capabilities: []string{"filesystem"},
	}
	_, err := h.EnsureTemplate(ctx, q)
	if status.Code(err) != codes.InvalidArgument || backend.ensureCalls != 0 {
		t.Fatalf("capability request: %v, ensure calls %d", err, backend.ensureCalls)
	}
	q.Capabilities = nil
	ensured, err := h.EnsureTemplate(ctx, q)
	if err != nil || !slices.Equal(ensured.GetAdmittedCapabilities(), want) {
		t.Fatalf("ensure = %v, %v", ensured, err)
	}
	reserved, err := h.ReserveSuccessor(ctx, &executionv1.ReserveSuccessorRequest{
		Environment: &executionv1.EnvironmentRef{Id: "env-1", Revision: "rev-1"}, Owner: owner,
		SourceBindingId: "binding", DestinationBindingId: "successor", OperationId: "fork",
	})
	if err != nil || !slices.Equal(reserved.GetAdmittedCapabilities(), want) {
		t.Fatalf("successor = %v, %v", reserved, err)
	}
	if got := attachResponse(Allocation{}).GetAdmittedCapabilities(); !slices.Equal(got, want) {
		t.Fatalf("attach admitted = %v", got)
	}
}
