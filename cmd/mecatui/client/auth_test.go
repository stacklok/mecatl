package client

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthFailureUsesTypedLocalCausesAndBearerProvenance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		bearer bool
		want   AuthReason
	}{
		{"not enrolled", &AuthError{Reason: AuthNotEnrolled}, false, AuthNotEnrolled},
		{"expired", &AuthError{Reason: AuthSessionExpired}, true, AuthSessionExpired},
		{"unusable", &AuthError{Reason: AuthCredentialUnusable}, true, AuthCredentialUnusable},
		{"corrupt", &AuthError{Reason: AuthCredentialUnusable}, true, AuthCredentialUnusable},
		{"storage", &AuthError{Reason: AuthStorageUnavailable}, true, AuthStorageUnavailable},
		{"issuer", &AuthError{Reason: AuthStorageUnavailable}, true, AuthStorageUnavailable},
		{"cleanup", &AuthError{Reason: AuthCredentialCleanup}, true, AuthCredentialCleanup},
		{"anonymous server rejection", status.Error(codes.Unauthenticated, "anything"), false, AuthNotEnrolled},
		{"bearer server rejection", status.Error(codes.Unauthenticated, "anything"), true, AuthRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AuthFailure(tc.err, tc.bearer)
			if !ok || got != tc.want {
				t.Fatalf("AuthFailure() = %q, %v; want %q, true", got, ok, tc.want)
			}
		})
	}
	if _, ok := AuthFailure(errors.New("clientauth: login required: session_expired"), true); ok {
		t.Fatal("arbitrary error text must not become an auth reason")
	}
	for _, reason := range []AuthReason{"", "future_reason"} {
		if got, ok := AuthFailure(&AuthError{Reason: reason}, true); ok || got != "" {
			t.Fatalf("AuthError reason %q classified as %q, %v; want unclassified", reason, got, ok)
		}
	}
	// An unclassified token-path error stays unclassified. Relabelling it
	// AuthCredentialUnusable names the wrong fault: arbitrary provider failures
	// are not broken local storage. Explicit issuer discovery failures map to the
	// separate AuthStorageUnavailable recovery path in the composition layer.
	unknown := errors.New("provider unreachable: dial tcp: connection refused")
	if reason, ok := AuthFailure(unknown, true); ok {
		t.Fatalf("unknown source error classified as %q; want unclassified", reason)
	}
	// A reason label never carries caller-supplied text, whichever path produced it.
	for _, r := range []AuthReason{AuthNotEnrolled, AuthSessionExpired, AuthCredentialUnusable, AuthCredentialUnusable, AuthStorageUnavailable, AuthStorageUnavailable, AuthCredentialCleanup, AuthRejected} {
		if got := (&AuthError{Reason: r}).Error(); got != "authentication unavailable: "+string(r) {
			t.Fatalf("AuthError text = %q; must be the closed label alone", got)
		}
	}
}

func TestTokenSourceUnaryCachesOneTokenForInterceptorAndCredentials(t *testing.T) {
	calls := 0
	source := tokenSourceFunc(func(context.Context) (string, error) { calls++; return "opaque", nil })
	var got error
	interceptor := tokenSourceUnary(source)
	got = interceptor(t.Context(), "/test", nil, nil, nil, func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		_, got = (bearerCreds{source: source}).GetRequestMetadata(ctx)
		return got
	})
	if got != nil || calls != 1 {
		t.Fatalf("unary error=%v token calls=%d, want nil/1", got, calls)
	}
}

func TestTokenSourceStreamCachesOneTokenForInterceptorAndCredentials(t *testing.T) {
	calls := 0
	source := tokenSourceFunc(func(context.Context) (string, error) { calls++; return "opaque", nil })
	var metadataErr error
	_, err := tokenSourceStream(source)(t.Context(), nil, nil, "/test", func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
		_, metadataErr = (bearerCreds{source: source}).GetRequestMetadata(ctx)
		return nil, metadataErr
	})
	if err != nil || calls != 1 {
		t.Fatalf("stream error=%v token calls=%d, want nil/1", err, calls)
	}
}

func TestTokenSourceStreamPreservesReason(t *testing.T) {
	calls := 0
	want := &AuthError{Reason: AuthSessionExpired}
	source := tokenSourceFunc(func(context.Context) (string, error) {
		calls++
		return "", want
	})
	_, err := tokenSourceStream(source)(t.Context(), nil, nil, "/test", func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
		t.Fatal("streamer must not run after token failure")
		return nil, nil
	})
	if calls != 1 {
		t.Fatalf("stream token calls=%d, want 1", calls)
	}
	if err != want {
		t.Fatalf("stream error = %v; want wrapper error preserved", err)
	}
	if reason, ok := AuthFailure(err, true); !ok || reason != AuthSessionExpired {
		t.Fatalf("stream reason=%q ok=%v", reason, ok)
	}
}
