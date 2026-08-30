package server

import "testing"

func TestNormalizeServerImplementation(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  string
	}{
		{value: "mecated", want: "mecated"},
		{value: "mecak8s", want: "mecak8s"},
		{value: "mecatui", want: "mecatui"},
		{value: "", want: "unknown"},
		{value: "grpc://10.0.0.1", want: "unknown"},
		{value: "control\nvalue", want: "unknown"},
		{value: "token=secret", want: "unknown"},
		{value: "Mecated", want: "unknown"},
		{value: "1mecated", want: "unknown"},
		{value: "mecated_unsafe", want: "unknown"},
	} {
		if got := normalizeServerImplementation(tc.value); got != tc.want {
			t.Errorf("normalizeServerImplementation(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
}
