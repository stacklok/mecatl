package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type tokenSourceFunc func(context.Context) (string, error)

func (f tokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// TestDialBearerCleartextGuard covers the security contract: a bearer token may
// ride plaintext only to a loopback target; any non-loopback plaintext target is
// hard-refused, and TLS targets always succeed.
func TestDialBearerCleartextGuard(t *testing.T) {
	cases := []struct {
		name    string
		cfg     DialConfig
		wantErr bool
	}{
		{
			name:    "non-loopback plaintext with token is refused even when explicitly allowed",
			cfg:     DialConfig{Server: "10.0.0.5:8080", AuthToken: "x", UseTLS: false, RemotePlaintextAllowed: true},
			wantErr: true,
		},
		{
			name:    "loopback plaintext with token is allowed",
			cfg:     DialConfig{Server: "127.0.0.1:8080", AuthToken: "x", UseTLS: false},
			wantErr: false,
		},
		{
			name:    "non-loopback with token over TLS is allowed",
			cfg:     DialConfig{Server: "example.com:8080", AuthToken: "x", UseTLS: true},
			wantErr: false,
		},
		{
			name:    "non-loopback plaintext without authorization is refused",
			cfg:     DialConfig{Server: "10.0.0.5:8080", UseTLS: false},
			wantErr: true,
		},
		{
			name:    "non-loopback plaintext without token is explicitly allowed",
			cfg:     DialConfig{Server: "10.0.0.5:8080", UseTLS: false, RemotePlaintextAllowed: true},
			wantErr: false,
		},
		{
			name:    "localhost name plaintext with token is allowed",
			cfg:     DialConfig{Server: "localhost:8080", AuthToken: "x", UseTLS: false},
			wantErr: false,
		},
		{
			// Encrypted but unauthenticated: an MITM with any certificate reads
			// the bearer, so this is refused exactly like the cleartext case.
			name:    "non-loopback with token over unverified TLS is refused",
			cfg:     DialConfig{Server: "example.com:8080", AuthToken: "x", UseTLS: true, Insecure: true},
			wantErr: true,
		},
		{
			name:    "loopback with token over unverified TLS is allowed",
			cfg:     DialConfig{Server: "127.0.0.1:8080", AuthToken: "x", UseTLS: true, Insecure: true},
			wantErr: false,
		},
		{
			name:    "unverified TLS without a token is allowed",
			cfg:     DialConfig{Server: "example.com:8080", UseTLS: true, Insecure: true},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl, err := Dial(tc.cfg)
			if cl != nil {
				_ = cl.Close()
			}
			if tc.wantErr && err == nil {
				t.Fatalf("Dial(%+v) = nil error, want error", tc.cfg)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Dial(%+v) = %v, want nil error", tc.cfg, err)
			}
		})
	}
}

func TestDynamicBearerCredentials(t *testing.T) {
	calls := 0
	creds := bearerCreds{source: tokenSourceFunc(func(context.Context) (string, error) {
		calls++
		return "refreshed", nil
	})}
	metadata, err := creds.GetRequestMetadata(t.Context())
	if err != nil || metadata["authorization"] != "Bearer refreshed" {
		t.Fatalf("metadata = %#v, %v", metadata, err)
	}
	if calls != 1 {
		t.Fatalf("source calls = %d, want 1", calls)
	}
	if !creds.RequireTransportSecurity() {
		t.Fatal("dynamic non-loopback bearer must require TLS")
	}
	if _, err := (bearerCreds{source: tokenSourceFunc(func(context.Context) (string, error) { return "", errors.New("unavailable") })}).GetRequestMetadata(t.Context()); err == nil {
		t.Fatal("source error was not returned")
	}
}

func TestDialDynamicBearerCleartextGuard(t *testing.T) {
	cl, err := Dial(DialConfig{Server: "example.com:443", TokenSource: tokenSourceFunc(func(context.Context) (string, error) {
		t.Fatal("Dial must not call token source")
		return "", nil
	})})
	if cl != nil {
		_ = cl.Close()
	}
	if err == nil {
		t.Fatal("dynamic bearer plaintext dial succeeded")
	}

	// A dynamic bearer leaks to an MITM over unverified TLS exactly as a static
	// one does, so both guards must read the TokenSource arm too.
	cl, err = Dial(DialConfig{Server: "example.com:443", UseTLS: true, Insecure: true,
		TokenSource: tokenSourceFunc(func(context.Context) (string, error) {
			t.Fatal("Dial must not call token source")
			return "", nil
		})})
	if cl != nil {
		_ = cl.Close()
	}
	if err == nil {
		t.Fatal("dynamic bearer unverified-TLS dial succeeded")
	}
}

// TestIsLoopbackHost covers the host classification used to gate the token and remote workspace authority.
func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080":     true,
		"127.0.0.1":          true,
		"127.5.6.7:80":       true, // 127.0.0.0/8
		"[::1]:8080":         true,
		"::1":                true,
		"localhost:8080":     true,
		"LocalHost":          true,
		"localhost:not-port": false,
		"localhost:":         false,
		// A named service port is rare in a gRPC target and classifying it
		// fails CLOSED (verified TLS), which is the safe direction. Pinned so
		// the choice is a recorded decision rather than a ParseUint accident.
		"localhost:http": false,
		"10.0.0.5:8080":  false,
		"example.com:80": false,
		"0.0.0.0:8080":   false,
		"":               false,
	}
	for host, want := range cases {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// TestBearerRequireTransportSecurity asserts the credential demands TLS for any
// non-loopback target and permits plaintext only for loopback.
func TestBearerRequireTransportSecurity(t *testing.T) {
	if (bearerCreds{allowInsecure: true}).RequireTransportSecurity() {
		t.Error("loopback bearer must NOT require transport security")
	}
	if !(bearerCreds{allowInsecure: false}).RequireTransportSecurity() {
		t.Error("non-loopback bearer MUST require transport security")
	}
}

// TestCredentialFreeDialHintClassifiesOnlyServerAuthDecisions pins the hint a
// credential-free client adds only after the server is authoritative.
func TestCredentialFreeDialHintClassifiesOnlyServerAuthDecisions(t *testing.T) {
	const server = "mecak8s.example:18081"

	if got := credentialFreeDialHint(server, false, nil); got != nil {
		t.Fatalf("nil error became %v", got)
	}

	unauth := status.Error(codes.Unauthenticated, "missing or invalid bearer token")
	got := credentialFreeDialHint(server, false, unauth)
	for _, want := range []string{"server requires caller authentication", "use --auth-token", "if this server supports OIDC enrollment", "mecatui login " + server} {
		if !strings.Contains(got.Error(), want) {
			t.Fatalf("hint missing %q: %v", want, got)
		}
	}
	if strings.Contains(got.Error(), "no saved credential") {
		t.Fatalf("server rejection was misreported as a registry fact: %v", got)
	}
	if reason, ok := AuthFailure(got, false); !ok || reason != AuthNotEnrolled {
		t.Fatalf("reason=%q ok=%v, want server auth required", reason, ok)
	}
	if status.Code(got) != codes.Unauthenticated {
		t.Fatalf("status code = %v, want Unauthenticated", status.Code(got))
	}

	explicit := credentialFreeDialHint(server, true, unauth)
	if reason, ok := AuthFailure(explicit, false); !ok || reason != AuthAnonymousRejected || !strings.Contains(explicit.Error(), "rejected the explicit --anonymous connection") {
		t.Fatalf("explicit anonymous rejection = %v, reason=%q ok=%v", explicit, reason, ok)
	}

	denied := credentialFreeDialHint(server, false, status.Error(codes.PermissionDenied, "policy"))
	if reason, ok := AuthFailure(denied, false); ok || reason != "" {
		t.Fatalf("PermissionDenied became auth recovery: reason=%q ok=%v", reason, ok)
	}
	if status.Code(denied) != codes.PermissionDenied || !strings.Contains(denied.Error(), "authorization denied") {
		t.Fatalf("PermissionDenied presentation = %v", denied)
	}

	// Network/TLS and other RPC failures must not acquire authentication recovery.
	for _, code := range []codes.Code{codes.Internal, codes.Unavailable, codes.InvalidArgument} {
		in := status.Error(code, "boom")
		out := credentialFreeDialHint(server, false, in)
		if out.Error() != in.Error() {
			t.Fatalf("%v was annotated: %v", code, out)
		}
		if reason, ok := AuthFailure(out, false); ok || reason != "" {
			t.Fatalf("%v became auth recovery: reason=%q ok=%v", code, reason, ok)
		}
	}
}

// TestIsLocalTarget covers the predicate that gates every plaintext decision:
// loopback host:port PLUS unix:// sockets, which the filesystem protects.
func TestIsLocalTarget(t *testing.T) {
	cases := map[string]bool{
		"unix:///run/user/1000/mecated.sock": true,
		"unix://relative.sock":               true,
		"  unix:///tmp/a.sock  ":             true,
		"127.0.0.1:8080":                     true,
		"localhost":                          true,
		"unix-abstract:mecated":              false, // not the unix:// scheme
		"10.0.0.5:8080":                      false,
		"":                                   false,
	}
	for target, want := range cases {
		if got := IsLocalTarget(target); got != want {
			t.Errorf("IsLocalTarget(%q) = %v, want %v", target, got, want)
		}
	}
}

// TestDialUnixSocketWithBearer pins the guard/credential agreement: a unix://
// target accepted by the pre-dial guards must ALSO be accepted by the per-RPC
// credential. When allowInsecure tracked only loopback, this dial failed with
// gRPC's opaque "credentials require transport level security" -- exactly the
// error the pre-dial guard exists to replace.
func TestDialUnixSocketWithBearer(t *testing.T) {
	for _, cfg := range []DialConfig{
		{Server: "unix:///tmp/mecatl-test.sock", AuthToken: "tok"},
		{Server: "unix:///tmp/mecatl-test.sock", TokenSource: tokenSourceFunc(func(context.Context) (string, error) { return "tok", nil })},
	} {
		cl, err := Dial(cfg)
		if err != nil {
			t.Fatalf("Dial(%+v) = %v, want a plaintext unix dial to succeed", cfg.Server, err)
		}
		_ = cl.Close()
	}
}
