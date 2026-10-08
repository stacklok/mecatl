package grpcdriver

import (
	"context"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
)

// TestDialRefusesCleartextNonLoopback pins the hardened transport posture: a
// NON-LOCAL driver target must never ride cleartext at all — token or not —
// because drivers deliver session payloads, memory, model-steering skill
// bodies, and executable assets. Dial refuses pre-dial with an actionable
// error. (This supersedes the Phase-B token-only rule for every driver seam.)
func TestDialRefusesCleartextNonLoopback(t *testing.T) {
	for _, opts := range [][]Option{
		{WithBearerToken("secret")}, // token over cleartext: still refused
		nil,                         // token-LESS cleartext: refused too (the hardening)
	} {
		_, err := Dial("driver.example.com:7443", opts...)
		if err == nil {
			t.Fatalf("Dial(non-loopback, no TLS, opts=%v) = nil error, want refusal", opts)
		}
		if !strings.Contains(err.Error(), "CLEARTEXT") || !strings.Contains(err.Error(), "--driver-tls") {
			t.Errorf("refusal error %q should name the cleartext hazard and the --driver-tls fix", err)
		}
	}
}

// TestDialLocalCleartextAllowed pins the LOCAL plaintext single-user default:
// loopback hosts and unix sockets dial (lazily) without TLS, with or without
// a token.
func TestDialLocalCleartextAllowed(t *testing.T) {
	for _, target := range []string{"127.0.0.1:7443", "localhost:7443", "[::1]:7443", "unix:///run/mecatl/driver.sock"} {
		conn, err := Dial(target)
		if err != nil {
			t.Errorf("Dial(%q) = %v, want lazy success (local plaintext default)", target, err)
			continue
		}
		_ = conn.Close()
	}
}

// TestDialLoopbackTokenAllowed pins the loopback plaintext single-user
// default: a token to a loopback target dials (lazily) without TLS.
func TestDialLoopbackTokenAllowed(t *testing.T) {
	for _, target := range []string{"127.0.0.1:7443", "localhost:7443", "[::1]:7443"} {
		conn, err := Dial(target, WithBearerToken("secret"))
		if err != nil {
			t.Errorf("Dial(%q, token) = %v, want lazy success (loopback plaintext default)", target, err)
			continue
		}
		_ = conn.Close()
	}
}

// TestDialAssembledBearerMetadataOverBufconn pins DIAL's OWN plumbing of the
// per-RPC bearer credential, without a real socket: it takes the EXACT option
// set Dial assembles (dialOptions is Dial's body — Dial is dialOptions +
// grpc.NewClient), appends only a bufconn dialer, and asserts the
// "authorization: Bearer <token>" metadata arrives at the server. A loopback
// target keeps the documented plaintext-token path.
func TestDialAssembledBearerMetadataOverBufconn(t *testing.T) {
	var gotAuth string
	intercept := grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if vals := md.Get("authorization"); len(vals) > 0 {
				gotAuth = vals[0]
			}
		}
		return handler(ctx, req)
	})
	lis := startBufconnServer(t, func(gs *grpc.Server) {
		driverv1.RegisterSessionStoreServiceServer(gs, NewSessionStoreServer(memstore.New()))
	}, intercept)

	// The production assembly, exactly as Dial("127.0.0.1:7443",
	// WithBearerToken("tok-123")) would build it.
	opts, err := dialOptions("127.0.0.1:7443", dialConfig{token: "tok-123"})
	if err != nil {
		t.Fatalf("dialOptions: %v", err)
	}
	opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}))
	conn, err := grpc.NewClient("passthrough:///bufnet", opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	st := mustNewSessionStore(t, conn)
	// Any RPC carries the credential; a Load miss is fine.
	_, _ = st.Load(context.Background(), "whatever")
	if want := "Bearer tok-123"; gotAuth != want {
		t.Errorf("server saw authorization = %q, want %q", gotAuth, want)
	}
}

// TestIsLoopbackHost pins the loopback classifier (fail-safe: unparseable
// hosts are NON-loopback so the token path demands TLS).
func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		target string
		want   bool
	}{
		{"127.0.0.1:1", true},
		{"127.9.9.9:1", true},
		{"[::1]:1", true},
		{"localhost:1", true},
		{"LOCALHOST:1", true},
		{"10.0.0.1:1", false},
		{"driver.example.com:1", false},
		{"", false},
		{":1", false},
	}
	for _, c := range cases {
		if got := isLoopbackHost(c.target); got != c.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", c.target, got, c.want)
		}
	}
}
