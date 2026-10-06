package microvmmanager

import (
	"encoding/base64"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestGuestEgressSelectionParsesAndNormalizes(t *testing.T) {
	selection := NewGuestEgressSelection()
	if err := selection.ModeValue().Set("allowlist"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"API.Example.COM.:443/tcp", "dns.example.com:53/udp"} {
		if err := selection.AllowValue().Set(value); err != nil {
			t.Fatalf("Set(%q): %v", value, err)
		}
	}
	if err := selection.Validate(); err != nil {
		t.Fatal(err)
	}
	want := []EgressRule{{Hostname: "api.example.com", Port: 443, Protocol: 6}, {Hostname: "dns.example.com", Port: 53, Protocol: 17}}
	if len(selection.Allow) != len(want) {
		t.Fatalf("rules = %#v", selection.Allow)
	}
	for i := range want {
		if selection.Allow[i] != want[i] {
			t.Fatalf("rule %d = %#v, want %#v", i, selection.Allow[i], want[i])
		}
	}
}

func TestGuestEgressSelectionRejectsInvalidRules(t *testing.T) {
	invalid := []string{
		"example.com:0/tcp", "example.com:65536/tcp", "example.com:443/sctp",
		"127.0.0.1:443/tcp", "[2001:db8::1]:443/tcp", "*.example.com:443/tcp",
		"bad host:443/tcp", "example.com:443/TCP", "example.com/tcp", "example.com:443/tcp\n",
	}
	for _, value := range invalid {
		t.Run(strings.ReplaceAll(value, "/", "_"), func(t *testing.T) {
			selection := NewGuestEgressSelection()
			if err := selection.AllowValue().Set(value); err == nil {
				t.Fatalf("Set(%q) succeeded", value)
			}
		})
	}
}

func TestGuestEgressSelectionRejectsCombinationsAndDuplicates(t *testing.T) {
	selection := NewGuestEgressSelection()
	if err := selection.AllowValue().Set("example.com:443/tcp"); err != nil {
		t.Fatal(err)
	}
	if err := selection.Validate(); err == nil {
		t.Fatal("permissive mode accepted an allow rule")
	}
	if err := selection.ModeValue().Set("allowlist"); err != nil {
		t.Fatal(err)
	}
	if err := selection.AllowValue().Set("EXAMPLE.COM.:443/tcp"); err == nil {
		t.Fatal("normalized duplicate accepted")
	}
	empty := NewGuestEgressSelection()
	if err := empty.ModeValue().Set("allowlist"); err != nil {
		t.Fatal(err)
	}
	if err := empty.Validate(); err == nil {
		t.Fatal("empty allowlist accepted")
	}
}

func TestReadyRequestFromDefaultsOverlaysOnlyGuestEgress(t *testing.T) {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	defaults := map[string]ReleaseDefaults{platform: {
		Version: "v1", Platform: platform, URL: "https://example.com/release", SHA256: strings.Repeat("a", 64),
		PolicyRevision: "release-policy", CertificateIdentity: "release-identity", OIDCIssuer: "release-issuer",
	}}
	data, err := json.Marshal(defaults)
	if err != nil {
		t.Fatal(err)
	}
	selection := GuestEgressSelection{Mode: GuestEgressAllowlist, Allow: []EgressRule{{Hostname: "api.example.com", Port: 443, Protocol: 6}}}
	request, err := ReadyRequestFromDefaults(base64.StdEncoding.EncodeToString(data), "v1", selection)
	if err != nil {
		t.Fatal(err)
	}
	if request.Policy.GuestEgressMode != GuestEgressAllowlist || len(request.Policy.GuestAllow) != 1 {
		t.Fatalf("egress overlay = %#v", request.Policy)
	}
	if request.Policy.PolicyRevision != "release-policy" || request.Policy.CertificateIdentity != "release-identity" || len(request.Policy.RequiredAttestations) != 4 {
		t.Fatalf("release defaults changed = %#v", request.Policy)
	}

	defaultRequest, err := ReadyRequestFromDefaults(base64.StdEncoding.EncodeToString(data), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if defaultRequest.Policy.GuestEgressMode != GuestEgressPermissive || len(defaultRequest.Policy.GuestAllow) != 0 {
		t.Fatalf("default egress = %#v", defaultRequest.Policy)
	}
}
