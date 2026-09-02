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
		"10.0.0.5:8080":      false,
		"example.com:80":     false,
		"0.0.0.0:8080":       false,
		"":                   false,
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

// TestAnonymousDialHintOnlyAnnotatesUnauthenticated pins the hint a client adds
// when it dialled with no bearer credential. A registry miss is not an error at
// dial time (an unauthenticated mecated needs no credential), so the server's
// rejection is the first actionable moment — and it must not read as a server
// fault. The gRPC status has to survive, since callers classify on it.
func TestAnonymousDialHintOnlyAnnotatesUnauthenticated(t *testing.T) {
	const server = "mecak8s.example:18081"

	if got := anonymousDialHint(server, nil); got != nil {
		t.Fatalf("nil error became %v", got)
	}

	unauth := status.Error(codes.Unauthenticated, "missing or invalid bearer token")
	got := anonymousDialHint(server, unauth)
	if !strings.Contains(got.Error(), "run 'mecatui login "+server+"'") {
		t.Fatalf("hint missing: %v", got)
	}
	if !strings.Contains(got.Error(), "missing or invalid bearer token") {
		t.Fatalf("server message lost: %v", got)
	}
	if status.Code(got) != codes.Unauthenticated {
		t.Fatalf("status code = %v, want Unauthenticated", status.Code(got))
	}

	// Any other failure is not an enrolment problem and must be left alone.
	for _, code := range []codes.Code{codes.Internal, codes.Unavailable, codes.InvalidArgument} {
		in := status.Error(code, "boom")
		if out := anonymousDialHint(server, in); out.Error() != in.Error() {
			t.Fatalf("%v was annotated: %v", code, out)
		}
	}
}
