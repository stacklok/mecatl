package server

import (
	"fmt"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Handler-level Unauthenticated errors run after the request was admitted, so
// they must keep the "unauthenticated" reason and never the retryable
// bearer_rejected one.
func TestHandlerUnauthenticatedIsNotBearerRejected(t *testing.T) {
	t.Parallel()

	for _, sentinel := range []error{errExecutionUnauthenticated, errConnectorUnauthenticated} {
		st := status.Convert(toStatus(fmt.Errorf("handler: %w", sentinel)))
		if st.Code() != codes.Unauthenticated {
			t.Fatalf("%v: code = %v, want Unauthenticated", sentinel, st.Code())
		}
		var reasons []string
		for _, detail := range st.Details() {
			if info, ok := detail.(*errdetails.ErrorInfo); ok {
				reasons = append(reasons, info.GetReason())
			}
		}
		if len(reasons) != 1 || reasons[0] != "unauthenticated" {
			t.Fatalf("%v: ErrorInfo reasons = %v, want only unauthenticated", sentinel, reasons)
		}
	}
}
