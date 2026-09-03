package resourceurl

import "testing"

func TestCanonicalResourceIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://API.example.com/", "https://api.example.com"},
		{"https://api.example.com:443", "https://api.example.com"},
		{"https://API.example.com:443/", "https://api.example.com"},
		{"https://api.example.com:8443/", "https://api.example.com:8443"},
		{"https://api.example.com/v1.0/resource", "https://api.example.com/v1.0/resource"},
		{"https://api.example.com/..well-known/resource", "https://api.example.com/..well-known/resource"},
		{"https://api.example.com/path%2Fitem", "https://api.example.com/path%2Fitem"},
		{"https://[2001:DB8::1]/", "https://[2001:db8::1]"},
		{"https://[2001:0DB8:0:0:0:0:0:1]/", "https://[2001:db8::1]"},
		{"https://[2001:DB8::1]:8443/", "https://[2001:db8::1]:8443"},
	} {
		got, err := Canonical(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"https://api.example.com?", "https://api.example.com/?", "https://api.example.com/%zz", "https://api.example.com/a/../b", "https://api.example.com/a/./b", "https://api.example.com/a/%2e%2e/b", "https://api.example.com/a/%2E%2E/b", "http://api.example.com", "https://user@api.example.com"} {
		if _, err := Canonical(raw); err == nil {
			t.Errorf("Canonical(%q) accepted invalid resource", raw)
		}
	}
}

func TestMetadataURLUsesCanonicalResource(t *testing.T) {
	if got, want := MetadataURL("https://API.example.com/"), "https://api.example.com/.well-known/oauth-protected-resource"; got != want {
		t.Fatalf("MetadataURL root = %q, want %q", got, want)
	}
	if got, want := MetadataURL("https://API.example.com:443/base%2Fitem"), "https://api.example.com/.well-known/oauth-protected-resource/base%2Fitem"; got != want {
		t.Fatalf("MetadataURL escaped path = %q, want %q", got, want)
	}
}
