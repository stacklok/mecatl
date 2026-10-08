package client

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// IsContextWindowUnavailable reports a typed pre-execution admission rejection.
// Status text is deliberately not part of this classification.
func IsContextWindowUnavailable(err error) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		return false
	}
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if ok && info.GetDomain() == "mecatl.stacklok.com" && info.GetReason() == "context_window_unavailable" {
			return true
		}
	}
	return false
}
