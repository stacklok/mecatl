package server

import (
	"strings"
	"testing"
)

func TestServerInfoEndpointProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "origin and escaped clean path", raw: "HTTPS://user:secret@provider.example:8443/a/../v%201?token=secret#fragment", want: "https://provider.example:8443/v%201"},
		{name: "query and fragment only", raw: "https://provider.example/v1?key=secret#x", want: "https://provider.example/v1"},
		{name: "control", raw: "https://provider.example/v1\nsecret", want: ""},
		{name: "invalid", raw: "not a URL", want: ""},
		{name: "oversized", raw: "https://provider.example/" + strings.Repeat("x", maxDiagnosticEndpointBytes), want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (&Service{cfg: Config{ProviderEndpoint: func(_ string) string { return tc.raw }}}).serverInfoResponse("provider").GetLlmProviderDisplayEndpoint(); got != tc.want {
				t.Fatalf("endpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServerInfoEndpointRequiresProviderSelector(t *testing.T) {
	calls := 0
	svc := &Service{cfg: Config{ProviderEndpoint: func(providerID string) string {
		calls++
		if providerID == "known" {
			return "https://provider.example/v1"
		}
		return ""
	}}}
	if got := svc.serverInfoResponse("").GetLlmProviderDisplayEndpoint(); got != "" || calls != 0 {
		t.Fatalf("empty selector endpoint/calls = %q/%d, want unavailable/no lookup", got, calls)
	}
	if got := svc.serverInfoResponse("unknown").GetLlmProviderDisplayEndpoint(); got != "" {
		t.Fatalf("unknown selector endpoint = %q, want unavailable", got)
	}
	if got := svc.serverInfoResponse("known").GetLlmProviderDisplayEndpoint(); got != "https://provider.example/v1" {
		t.Fatalf("known selector endpoint = %q", got)
	}
}

func TestServerInfoUnavailableWithoutConfiguredEndpoint(t *testing.T) {
	if got := (&Service{}).serverInfoResponse("provider").GetLlmProviderDisplayEndpoint(); got != "" {
		t.Fatalf("endpoint = %q, want unavailable", got)
	}
}
