package cliconfig

import "testing"

// TestJoinPromptBody is the pure table test of the shared --prompt/--prompt-file
// join: both empty → empty; literal only; file only; both → literal + blank line +
// fileBody. It covers BOTH prompt-bearing mains (mecatequi's one-shot and mecatui's
// interactive seed), which is why the function lives here rather than in either.
func TestJoinPromptBody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, literal, fileBody, want string
	}{
		{name: "both empty", literal: "", fileBody: "", want: ""},
		{name: "literal only", literal: "hello", fileBody: "", want: "hello"},
		{name: "file only", literal: "", fileBody: "world", want: "world"},
		{name: "both joined by blank line", literal: "hello", fileBody: "world", want: "hello\n\nworld"},
		{name: "trailing newlines trimmed", literal: "a\n\n", fileBody: "b\n\n", want: "a\n\nb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := JoinPromptBody(tc.literal, tc.fileBody); got != tc.want {
				t.Errorf("JoinPromptBody(%q, %q) = %q, want %q", tc.literal, tc.fileBody, got, tc.want)
			}
		})
	}
}
