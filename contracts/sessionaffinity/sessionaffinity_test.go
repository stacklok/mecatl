package sessionaffinity_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/contracts/sessionaffinity"
)

func TestADR_0294_SessionHeaderLegalValue(t *testing.T) {
	if sessionaffinity.HeaderName != "X-Mecatl-Session-ID" {
		t.Fatalf("HeaderName = %q", sessionaffinity.HeaderName)
	}
	data, err := os.ReadFile("../../testdata/session_header_values.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Legal bool   `json:"legal"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			if got := sessionaffinity.ValidValue(vector.Value); got != vector.Legal {
				t.Fatalf("ValidValue(%q) = %v, want %v", vector.Value, got, vector.Legal)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{"maximum", strings.Repeat("x", sessionaffinity.MaxValueBytes), true},
		{"too long", strings.Repeat("x", sessionaffinity.MaxValueBytes+1), false},
		{"empty", "", false},
		{"leading space", " x", false},
		{"trailing space", "x ", false},
		{"control", "x\n", false},
		{"non ascii", "session-α", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionaffinity.ValidValue(tc.value); got != tc.want {
				t.Fatalf("ValidValue(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
