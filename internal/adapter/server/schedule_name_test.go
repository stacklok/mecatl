package server_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestLiteralScheduleName(t *testing.T) {
	digest := sha256.Sum256([]byte("https://issuer.example\x00alice"))
	physical := fmt.Sprintf("schedule/%x\x00nightly cleanup", digest[:])

	got := server.LiteralScheduleName(physical)
	if got != "nightly cleanup" {
		t.Fatalf("LiteralScheduleName(%q) = %q, want literal name", physical, got)
	}
	if strings.ContainsRune(got, '\x00') || strings.Contains(got, fmt.Sprintf("%x", digest[:])) {
		t.Fatalf("literal name leaked physical namespace: %q", got)
	}

	if got := server.LiteralScheduleName("flat-name"); got != "flat-name" {
		t.Fatalf("ownership-disabled name = %q, want unchanged", got)
	}
	if got := server.LiteralScheduleName("schedule/not-a-digest\x00literal"); got != "schedule/not-a-digest\x00literal" {
		t.Fatalf("malformed physical key = %q, want unchanged", got)
	}
}
