package openai

import (
	"strings"
	"testing"
)

// TestSelectedDownstreamProvider pins the tolerant, fail-empty parse of the
// opt-in openrouter_metadata block (issue #480): the selected endpoint wins, the
// last attempt is the fallback, and every absent/malformed shape yields "".
func TestSelectedDownstreamProvider(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "selected endpoint wins",
			raw:  `{"openrouter_metadata":{"endpoints":{"available":[{"provider":"Google Vertex","selected":false},{"provider":"Anthropic","selected":true}]},"attempts":[{"provider":"Google Vertex"}]}}`,
			want: "Anthropic",
		},
		{
			name: "last attempt is the fallback when nothing is selected",
			raw:  `{"openrouter_metadata":{"endpoints":{"available":[]},"attempts":[{"provider":"Amazon Bedrock"},{"provider":"DeepInfra"}]}}`,
			want: "DeepInfra",
		},
		{
			name: "selected with empty provider falls through to attempts",
			raw:  `{"openrouter_metadata":{"endpoints":{"available":[{"provider":"","selected":true}]},"attempts":[{"provider":"Together"}]}}`,
			want: "Together",
		},
		{
			name: "display label is flattened and controls removed",
			raw:  `{"openrouter_metadata":{"endpoints":{"available":[{"provider":"Google\n\u001bVertex","selected":true}]}}}`,
			want: "Google Vertex",
		},
		{
			name: "display label is bounded",
			raw:  `{"openrouter_metadata":{"endpoints":{"available":[{"provider":"` + strings.Repeat("a", maxDownstreamProviderLabelRunes+1) + `","selected":true}]}}}`,
			want: strings.Repeat("a", maxDownstreamProviderLabelRunes),
		},
		{
			name: "cache hit: metadata absent",
			raw:  `{"status":"completed","usage":{"input_tokens":1}}`,
			want: "",
		},
		{
			name: "metadata present but names nothing",
			raw:  `{"openrouter_metadata":{"endpoints":{"available":[]},"attempts":[]}}`,
			want: "",
		},
		{
			name: "malformed metadata object",
			raw:  `{"openrouter_metadata":"not-an-object"}`,
			want: "",
		},
		{
			name: "malformed top-level JSON",
			raw:  `{"openrouter_metadata":`,
			want: "",
		},
		{
			name: "empty raw",
			raw:  ``,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectedDownstreamProvider(tc.raw); got != tc.want {
				t.Errorf("selectedDownstreamProvider() = %q, want %q", got, tc.want)
			}
		})
	}
}
