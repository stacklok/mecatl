package prompt

import (
	"sort"
	"strings"
)

// Env is the per-turn environment that renders into the volatile suffix of the
// system prompt. Every field is injected by the composition root; the prompt
// package never touches os or time so the domain stays infra-free and the render
// stays deterministic and testable. Because these values change between turns
// (date, possibly cwd/mode) they live in the volatile suffix, never the
// cache-stable prefix.
type Env struct {
	// Cwd is the session working directory.
	Cwd string
	// OS is the host operating system / platform string (e.g. "linux").
	OS string
	// Model is the model identifier in use for the session.
	Model string
	// Date is the current date, pre-formatted by the caller (e.g. "2026-05-29").
	// It is a string, not a time.Time, so the prompt package imports no time.
	Date string
	// Mode is the session permission mode (e.g. "default", "plan",
	// "acceptEdits").
	Mode string
	// Shell is the shell the Shell tool executes against (e.g. "/bin/sh").
	Shell string
	// GitStatus is a start-of-session git snapshot (branch + short status +
	// recent commits). It may be multi-line and is rendered as a dedicated
	// sub-block; an empty value emits no sub-block.
	GitStatus string
}

// EnvBlock renders env as an <env>...</env> block with a stable, deterministic
// key order. The same Env always produces byte-identical output. Empty fields
// are still emitted (with empty values) so the block's shape is constant across
// turns, which keeps diffing and testing simple.
func EnvBlock(env Env) string {
	// Fixed key order — sorted once and asserted below — so the rendering is
	// deterministic regardless of map iteration or future field additions.
	pairs := [][2]string{
		{"cwd", env.Cwd},
		{"date", env.Date},
		{"model", env.Model},
		{"os", env.OS},
		{"permission-mode", env.Mode},
		{"shell", env.Shell},
	}
	// Defensive: guarantee deterministic ordering even if the literal above is
	// ever reordered by mistake.
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })

	var b strings.Builder
	b.WriteString("<env>")
	for _, p := range pairs {
		b.WriteByte('\n')
		b.WriteString(p[0])
		b.WriteString(": ")
		b.WriteString(p[1])
	}
	// GitStatus is multi-line, so it rides a dedicated sub-block AFTER the sorted
	// scalar pairs and before </env>; an empty snapshot emits nothing.
	if gs := strings.TrimSpace(env.GitStatus); gs != "" {
		b.WriteString("\n<git-status>\n")
		b.WriteString(gs)
		b.WriteString("\n</git-status>")
	}
	b.WriteString("\n</env>")
	return b.String()
}
