//go:build e2e

package e2e_test

import (
	"strings"
	"testing"
)

func TestParseSSEEventsRejectsMalformedFrame(t *testing.T) {
	_, err := parseSSEEvents([]byte("data: {not-json}\n\n"))
	if err == nil || !strings.Contains(err.Error(), "decode SSE data frame") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSSEEventsRejectsOversizedFrame(t *testing.T) {
	_, err := parseSSEEvents([]byte("data: \"" + strings.Repeat("x", 4*1024*1024) + "\"\n\n"))
	if err == nil || !strings.Contains(err.Error(), "scan SSE event replay") {
		t.Fatalf("err = %v", err)
	}
}
