package cliconfig

import "strings"

// JoinPromptBody concatenates a --prompt literal and a --prompt-file body into the
// single prompt text a main hands onward. Both may be supplied; the literal comes
// first, separated by a blank line. Each side is included only when non-empty so a
// lone source never carries a stray separator, and trailing newlines are trimmed so
// a file that ends in a newline joins identically to one that does not.
//
// Shared because both prompt-bearing mains must agree byte-for-byte on the join:
// cmd/mecatequi (the headless one-shot, which then layers its trusted-instructions /
// untrusted-fence wrapping on top) and cmd/mecatui (the interactive seed prompt).
// They had independent identical copies; only one was tested.
func JoinPromptBody(literal, fileBody string) string {
	parts := make([]string, 0, 2)
	if s := strings.TrimRight(literal, "\n"); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimRight(fileBody, "\n"); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n")
}
