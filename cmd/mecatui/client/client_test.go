package client

import "testing"

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
			name:    "non-loopback plaintext with token is refused",
			cfg:     DialConfig{Server: "10.0.0.5:8080", AuthToken: "x", UseTLS: false},
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
			name:    "non-loopback plaintext WITHOUT a token is allowed",
			cfg:     DialConfig{Server: "10.0.0.5:8080", UseTLS: false},
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

// TestIsLoopbackHost covers the host classification used to gate the token and remote workspace authority.
func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080": true,
		"127.0.0.1":      true,
		"127.5.6.7:80":   true, // 127.0.0.0/8
		"[::1]:8080":     true,
		"::1":            true,
		"localhost:8080": true,
		"LocalHost":      true,
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
