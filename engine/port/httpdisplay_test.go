package port

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestHTTPDisplayTarget(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"https://user:secret@example.test:8443/a/../b/%E2%98%83?q=secret#fragment", "https://example.test:8443/b/%E2%98%83"},
		{"http://[2001:db8::1]:8080/a//b", "http://[2001:db8::1]:8080/a/b"},
		{"https://example.test", "https://example.test/"},
		{"ftp://example.test/a", ""},
	} {
		u, err := url.Parse(tc.in)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", tc.in, err)
		}
		if got := httpDisplayTarget(u); got != tc.want {
			t.Errorf("HTTPDisplayTarget(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := httpDisplayTarget(&url.URL{Scheme: "https", Host: "bad host", Path: "/a"}); got != "" {
		t.Errorf("HTTPDisplayTarget(invalid host) = %q, want empty", got)
	}
}

func TestAppendHTTPErrorDisplayRejectsUnsafeID(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://user:secret@example.test/a?token=secret#fragment", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := AppendHTTPErrorDisplay("rejected", req, "req_123-abc"), "rejected (target: https://example.test/a; request ID: req_123-abc)"; got != want {
		t.Fatalf("AppendHTTPErrorDisplay() = %q, want %q", got, want)
	}
	for _, id := range []string{"bad id", "bad\nvalue", "secret/value", strings.Repeat("a", maxHTTPDisplayIDBytes+1)} {
		if got := httpDisplayID(id); got != "" {
			t.Errorf("HTTPDisplayID(%q) = %q, want empty", id, got)
		}
	}
}
