package server_test

import (
	"context"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// bearerRejectedTag reports whether err carries the authenticator's
// bearer_rejected ErrorInfo, the only Unauthenticated a client may refresh and
// retry on.
func bearerRejectedTag(err error) bool {
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == "mecatl.stacklok.com" && info.GetReason() == "bearer_rejected" {
			return true
		}
	}
	return false
}

func TestAuthenticatorBearerRejectionsCarryReason(t *testing.T) {
	t.Parallel()

	identity := oidcOnly(t, fakeValidator{ok: map[string]session.Principal{"good": edgeAlice}}, 0, 0)
	static := server.NewAuthenticator(server.SecurityConfig{AuthToken: "static-secret"})
	for _, tc := range []struct {
		name   string
		auth   *server.Authenticator
		bearer string
	}{
		{name: "invalid identity bearer", auth: identity, bearer: "expired"},
		{name: "missing identity bearer", auth: identity},
		{name: "mismatched static bearer", auth: static, bearer: "wrong"},
		{name: "missing static bearer", auth: static},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for transport, call := range map[string]func(*server.Authenticator, string) (context.Context, error){"unary": callUnary, "stream": callStream} {
				handlerCtx, err := call(tc.auth, tc.bearer)
				if handlerCtx != nil {
					t.Fatalf("%s: handler ran after a rejected bearer", transport)
				}
				st := status.Convert(err)
				if st.Code() != codes.Unauthenticated || st.Message() != "missing or invalid bearer token" {
					t.Fatalf("%s: status = %v %q, want Unauthenticated with the generic message", transport, st.Code(), st.Message())
				}
				if !bearerRejectedTag(err) {
					t.Fatalf("%s: rejection lacks the bearer_rejected ErrorInfo: %v", transport, st.Details())
				}
			}
		})
	}
}

// A refresh cannot fix duplicate Authorization metadata or an IdP outage, so
// neither carries the retryable tag.
func TestAuthenticatorNonBearerRejectionsAreUntagged(t *testing.T) {
	t.Parallel()

	identity := oidcOnly(t, fakeValidator{transient: map[string]bool{"outage": true}}, 0, 0)
	duplicate := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer a", "authorization", "Bearer b"))
	_, err := identity.UnaryInterceptor()(duplicate, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		t.Fatal("handler ran with duplicate authorization metadata")
		return nil, nil
	})
	if status.Code(err) != codes.Unauthenticated || bearerRejectedTag(err) {
		t.Fatalf("duplicate metadata = %v (tagged=%v), want untagged Unauthenticated", err, bearerRejectedTag(err))
	}
	if _, err := callUnary(identity, "outage"); status.Code(err) != codes.Unavailable || bearerRejectedTag(err) {
		t.Fatalf("IdP outage = %v (tagged=%v), want untagged Unavailable", err, bearerRejectedTag(err))
	}
}
