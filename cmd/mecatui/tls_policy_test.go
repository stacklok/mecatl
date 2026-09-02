package main

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestResolveRemoteTLSPolicy(t *testing.T) {
	cases := []struct {
		name    string
		address string
		cfg     config
		wantTLS bool
		wantErr string
	}{
		{name: "localhost defaults plaintext", address: "localhost:8080"},
		{name: "127 slash 8 defaults plaintext", address: "127.12.34.56:8080"},
		{name: "ipv6 loopback defaults plaintext", address: "[::1]:8080"},
		{name: "remote defaults verified TLS", address: "mecated.example:443", wantTLS: true},
		// A unix:// socket is local but is NOT a loopback host:port. Defaulting it
		// to TLS would attempt a handshake against a plaintext socket that
		// client.Dial is happy to dial in the clear -- the policy layer and the
		// transport layer must classify a target the same way.
		{name: "unix socket defaults plaintext", address: "unix:///run/user/1000/mecated.sock"},
		{name: "unix socket static bearer is allowed", address: "unix:///run/mecated.sock", cfg: config{tlsExplicit: true, authToken: "token"}},
		{name: "malformed defaults verified TLS", address: "not a target", wantTLS: true},
		{name: "explicit true", address: "localhost:8080", cfg: config{useTLS: true, tlsExplicit: true}, wantTLS: true},
		{name: "explicit false loopback", address: "localhost:8080", cfg: config{tlsExplicit: true}, wantTLS: false},
		{name: "ca implies TLS", address: "localhost:8080", cfg: config{tlsCA: "ca.pem"}, wantTLS: true},
		{name: "insecure implies TLS", address: "localhost:8080", cfg: config{insecure: true}, wantTLS: true},
		{name: "ca insecure conflict", address: "localhost:8080", cfg: config{tlsCA: "ca.pem", insecure: true}, wantErr: "mutually exclusive"},
		{name: "explicit false ca conflict", address: "localhost:8080", cfg: config{tlsExplicit: true, tlsCA: "ca.pem"}, wantErr: "--tls=false conflicts"},
		{name: "explicit false insecure conflict", address: "localhost:8080", cfg: config{tlsExplicit: true, insecure: true}, wantErr: "--tls=false conflicts"},
		{name: "explicit false remote static bearer conflict", address: "example.com:80", cfg: config{tlsExplicit: true, authToken: "token"}, wantErr: "static bearer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.transportMode = modeConnect
			cfg.connectAddress = tc.address
			err := resolveRemoteTLSPolicy(&cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolveRemoteTLSPolicy() = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || cfg.useTLS != tc.wantTLS {
				t.Fatalf("resolveRemoteTLSPolicy() = %v, tls=%v; want nil, %v", err, cfg.useTLS, tc.wantTLS)
			}
		})
	}
}

func TestApplySavedRemoteTLSPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     config
		wantErr bool
	}{
		{name: "loopback is upgraded", cfg: config{}},
		{name: "canonical target is upgraded", cfg: config{useTLS: false}},
		{name: "explicit plaintext is rejected", cfg: config{tlsExplicit: true}, wantErr: true},
		{name: "insecure is rejected", cfg: config{useTLS: true, insecure: true}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dial := client.DialConfig{TLSCAFile: "server-ca.pem", RemotePlaintextAllowed: true}
			err := applySavedRemoteTLSPolicy(tc.cfg, &dial)
			if tc.wantErr {
				if err == nil {
					t.Fatal("applySavedRemoteTLSPolicy() succeeded, want error")
				}
				return
			}
			if err != nil || !dial.UseTLS || dial.Insecure || dial.RemotePlaintextAllowed || dial.TLSCAFile != "server-ca.pem" {
				t.Fatalf("dial=%+v err=%v; want verified TLS and unchanged server CA", dial, err)
			}
		})
	}
}

func TestParseRunConfigAppliesTLSPolicy(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, tc := range []struct {
		name    string
		address string
		args    []string
		wantTLS bool
	}{
		{name: "remote defaults TLS", address: "mecated.example:443", wantTLS: true},
		{name: "loopback defaults plaintext", address: "localhost:8080"},
		{name: "explicit plaintext", address: "mecated.example:8080", args: []string{"--tls=false"}},
		{name: "CA implies TLS", address: "localhost:8080", args: []string{"--tls-ca", "ignored.pem"}, wantTLS: true},
		{name: "insecure implies TLS", address: "localhost:8080", args: []string{"--insecure"}, wantTLS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseRunConfig(invocationResolution{mode: modeConnect, address: tc.address, remaining: tc.args})
			if err != nil || cfg.useTLS != tc.wantTLS {
				t.Fatalf("parseRunConfig() = (%+v, %v), want TLS=%v", cfg, err, tc.wantTLS)
			}
		})
	}
}

func TestParseTransportFlagsRecordsExplicitTLS(t *testing.T) {
	_, cfg, err := parseTransportFlags(modeConnect, t.Output(), []string{"--tls=false"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.tlsExplicit || cfg.useTLS {
		t.Fatalf("tls explicit=%v value=%v, want true false", cfg.tlsExplicit, cfg.useTLS)
	}
}
