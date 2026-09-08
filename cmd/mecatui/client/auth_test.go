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
		{"explicit anonymous server rejection", &credentialFreeAuthError{cause: status.Error(codes.Unauthenticated, "anything"), reason: AuthAnonymousRejected, msg: "rejected"}, false, AuthAnonymousRejected},
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
	for _, r := range []AuthReason{AuthNotEnrolled, AuthAnonymousRejected, AuthSessionExpired, AuthCredentialUnusable, AuthCredentialUnusable, AuthStorageUnavailable, AuthStorageUnavailable, AuthCredentialCleanup, AuthRejected} {
		if got := (&AuthError{Reason: r}).Error(); got != "authentication unavailable: "+string(r) {
			t.Fatalf("AuthError text = %q; must be the closed label alone", got)
		}
	}
}

func TestStorageAuthErrorAddsOnlyClosedActionableDetail(t *testing.T) {
	for _, tc := range []struct {
		stage AuthStorageStage
		want  string
	}{
		{AuthStorageTLSCA, "authentication unavailable: storage_unavailable: TLS CA file could not be read; check the --tls-ca path and file permissions"},
		{AuthStorageConfigDirectory, "authentication unavailable: storage_unavailable: authentication config directory is unavailable; check its ownership and permissions"},
		{AuthStorageKeyring, "authentication unavailable: storage_unavailable: OS keyring is unavailable; unlock or enable the keyring, then retry"},
		{AuthStorageCredentialStore, "authentication unavailable: storage_unavailable: encrypted credential store is unavailable; check the authentication config directory ownership and permissions"},
		{AuthStorageRegistry, "authentication unavailable: storage_unavailable: login registry is unavailable; check the authentication config directory ownership and permissions"},
	} {
		err := &AuthError{Reason: AuthStorageUnavailable, StorageStage: tc.stage}
		if got := err.Error(); got != tc.want {
			t.Errorf("AuthError{%q}.Error() = %q, want %q", tc.stage, got, tc.want)
		}
		if reason, ok := AuthFailure(err, true); !ok || reason != AuthStorageUnavailable {
			t.Errorf("AuthFailure(AuthError{%q}) = %q, %v", tc.stage, reason, ok)
		}
	}
	if got := (&AuthError{Reason: AuthStorageUnavailable, StorageStage: "untrusted detail"}).Error(); got != "authentication unavailable: storage_unavailable" {
		t.Fatalf("unknown storage stage leaked into AuthError text: %q", got)
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
