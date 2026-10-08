package main

import (
	"encoding/base64"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
)

func TestMecatedMicroVMGuestEgressFlags(t *testing.T) {
	cfg, err := parseFlagsMode(modeServe, []string{
		"--default-placement=microvm-local",
		"--microvm-guest-egress=allowlist",
		"--microvm-guest-allow=API.Example.COM.:443/tcp",
		"--microvm-guest-allow=dns.example.com:53/udp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.microVMGuestEgress.Mode != microvmmanager.GuestEgressAllowlist || len(cfg.microVMGuestEgress.Allow) != 2 || cfg.microVMGuestEgress.Allow[0].Hostname != "api.example.com" {
		t.Fatalf("parsed egress = %#v", cfg.microVMGuestEgress)
	}
}

func TestMecatedMicroVMGuestEgressDefaultsAndCombinations(t *testing.T) {
	cfg, err := parseFlagsMode(modeServe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.microVMGuestEgress.Mode != microvmmanager.GuestEgressPermissive || len(cfg.microVMGuestEgress.Allow) != 0 {
		t.Fatalf("default egress = %#v", cfg.microVMGuestEgress)
	}
	for _, args := range [][]string{
		{"--default-placement=microvm-local", "--microvm-guest-egress=allowlist"},
		{"--default-placement=microvm-local", "--microvm-guest-egress=deny-all", "--microvm-guest-allow=example.com:443/tcp"},
		{"--default-placement=microvm-local", "--microvm-guest-allow=example.com:443/tcp"},
	} {
		if _, err := parseFlagsMode(modeServe, args); err == nil {
			t.Fatalf("parseFlagsMode(%v) succeeded", args)
		}
	}
	if _, err := parseFlagsMode(modeACP, []string{"--default-placement=microvm-local", "--microvm-guest-egress=deny-all"}); err == nil || !strings.Contains(err.Error(), "mecated serve") {
		t.Fatalf("ACP rejection = %v", err)
	}
}

func TestMecatedMicroVMGuestEgressReachesReadyRequest(t *testing.T) {
	originalVersion, originalDefaults := microVMReleaseVersion, microVMReleaseDefaultsB64
	t.Cleanup(func() { microVMReleaseVersion, microVMReleaseDefaultsB64 = originalVersion, originalDefaults })
	microVMReleaseVersion = "v-test-mecated"
	platform := runtime.GOOS + "-" + runtime.GOARCH
	defaults, err := json.Marshal(map[string]microvmmanager.ReleaseDefaults{platform: {
		Version: microVMReleaseVersion, Platform: platform, URL: "https://example.invalid/microvm.tar.gz", SHA256: strings.Repeat("a", 64),
		PolicyRevision: "policy-test", CertificateIdentity: "identity", OIDCIssuer: "issuer",
	}})
	if err != nil {
		t.Fatal(err)
	}
	microVMReleaseDefaultsB64 = base64.StdEncoding.EncodeToString(defaults)
	selection := microvmmanager.GuestEgressSelection{Mode: microvmmanager.GuestEgressDenyAll}
	request, err := microVMReadyRequest(selection)
	if err != nil {
		t.Fatal(err)
	}
	if request.Policy.GuestEgressMode != microvmmanager.GuestEgressDenyAll || len(request.Policy.GuestAllow) != 0 {
		t.Fatalf("request policy = %#v", request.Policy)
	}
}
