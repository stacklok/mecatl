package main

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
)

func TestMecatuiMicroVMGuestEgressFlags(t *testing.T) {
	cfg, err := parseFlags([]string{
		"--default-placement=microvm-local",
		"--microvm-guest-egress=allowlist",
		"--microvm-guest-allow=API.Example.COM.:443/tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.microVMGuestEgress.Mode != microvmmanager.GuestEgressAllowlist || len(cfg.microVMGuestEgress.Allow) != 1 || cfg.microVMGuestEgress.Allow[0].Hostname != "api.example.com" {
		t.Fatalf("parsed egress = %#v", cfg.microVMGuestEgress)
	}
}

func TestMecatuiMicroVMGuestEgressScopeAndCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"--microvm-guest-egress=deny-all"},
		{"--default-placement=other", "--microvm-guest-egress=deny-all"},
		{"--default-placement=microvm-local", "--microvm-guest-egress=allowlist"},
		{"--default-placement=microvm-local", "--microvm-guest-allow=example.com:443/tcp"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("parseFlags(%v) succeeded", args)
		}
	}
	_, _, err := parseTransportFlags(modeConnect, &strings.Builder{}, []string{"--microvm-guest-egress=deny-all"})
	if err == nil || !strings.Contains(err.Error(), "not applicable") {
		t.Fatalf("remote rejection = %v", err)
	}
}

func TestMecatuiMicroVMGuestEgressDefaultIsPermissive(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.microVMGuestEgress.Mode != microvmmanager.GuestEgressPermissive || len(cfg.microVMGuestEgress.Allow) != 0 {
		t.Fatalf("default egress = %#v", cfg.microVMGuestEgress)
	}
}
