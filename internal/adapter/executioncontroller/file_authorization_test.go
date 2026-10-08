package executioncontroller

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type countedFileBackend struct {
	Backend
	calls int
}

func (b *countedFileBackend) File(context.Context, string, string, executionenv.FileRequest) (executionenv.FileResponse, error) {
	b.calls++
	return executionenv.FileResponse{}, nil
}

func TestFileOperationFailsClosedWithoutDurableValidator(t *testing.T) {
	const client = "spiffe://cluster/ns/mecak8s"
	backend := &countedFileBackend{Backend: newFakeBackend()}
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{client: {MayAttestOwner: true}}}, backend)
	request := &executionv1.FileRequest{Context: &executionv1.RequestContext{Environment: &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}, Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, BindingId: "binding", RunId: "run", ClaimId: "claim", Epoch: 1, GrantGeneration: 1}, Operation: executionv1.FileOperation_FILE_OPERATION_READ, Path: "file"}
	if _, err := h.Files(authenticatedContext(client), request); status.Code(err) != codes.Unavailable || backend.calls != 0 {
		t.Fatalf("missing durable validator dispatched: calls=%d err=%v", backend.calls, err)
	}
}
