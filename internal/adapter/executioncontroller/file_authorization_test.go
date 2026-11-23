package executioncontroller

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
)

func TestFileOperationFailsClosedWithoutExecutorRouter(t *testing.T) {
	const client = "spiffe://cluster/ns/mecak8s"
	h := NewHandler(
		HandlerConfig{Clients: map[string]ClientPolicy{client: {MayAttestOwner: true}}},
		newFakeBackend(),
	)
	connector := &recordingConnector{}
	proxy := NewFileProxy(h, connector.Connect)
	request := &executorv1.StatFileRequest{Context: proxyRequestContext(), Path: "file"}
	_, err := proxy.StatFile(authenticatedContext(client), request)
	if status.Code(err) != codes.Unavailable || connector.count() != 0 {
		t.Fatalf("backend without a router dispatched: calls=%d err=%v", connector.count(), err)
	}
}
