package client

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestProviderModelDiscovery_Scenario5_TypedAdmissionError(t *testing.T) {
	for _, tc := range []struct {
		name           string
		code           codes.Code
		domain, reason string
		want           bool
	}{
		{"exact", codes.Unavailable, "mecatl.stacklok.com", "context_window_unavailable", true},
		{"wrong code", codes.Internal, "mecatl.stacklok.com", "context_window_unavailable", false},
		{"wrong domain", codes.Unavailable, "attacker.invalid", "context_window_unavailable", false},
		{"wrong reason", codes.Unavailable, "mecatl.stacklok.com", "CONTEXT_WINDOW_UNAVAILABLE", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := status.New(tc.code, "context_window_unavailable https://untrusted.invalid/secret").WithDetails(&errdetails.ErrorInfo{Domain: tc.domain, Reason: tc.reason})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range []error{st.Err(), fmt.Errorf("wrapped: %w", st.Err())} {
				if got := IsContextWindowUnavailable(e); got != tc.want {
					t.Fatalf("classifier = %v, want %v", got, tc.want)
				}
			}
		})
	}
	for _, err := range []error{nil, errors.New("context_window_unavailable"), status.Error(codes.Unavailable, "context_window_unavailable")} {
		if IsContextWindowUnavailable(err) {
			t.Fatalf("untyped error classified: %v", err)
		}
	}
}
