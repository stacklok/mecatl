package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// streamAnswerSection is one realistic chunk of an assistant answer: headings,
// prose with inline styles, nested and ordered lists, a fenced code block, a
// table, a blockquote, and emoji that exercise the width normalizer.
const streamAnswerSection = `## Summary of the change

The handler now validates the request **before** it touches the store, so an
empty payload returns ` + "`errEmpty`" + ` instead of panicking. I also moved the
retry loop into ` + "`internal/retry`" + ` so the other callers can share it ✅.

### What changed

1. Input validation
   - the request is rejected when ` + "`ID`" + ` is empty
   - oversized bodies are truncated with a warning ⚠️
2. Store access
   - reads are batched in groups of 64
   - writes still go one at a time, behind the barrier
3. Tests
   1. a table test for every rejected shape
   2. a fuzz target for the decoder 👩‍💻

` + "```go" + `
func Handle(ctx context.Context, req *Request) (*Response, error) {
	if req == nil || req.ID == "" {
		return nil, errEmpty
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := store.Get(ctx, req.ID)
		if err == nil {
			return resp, nil
		}
	}
	return nil, errExhausted
}
` + "```" + `

| file | change | lines |
|------|--------|------:|
| handler.go | validation + retry | 42 |
| retry/retry.go | new package | 87 |
| handler_test.go | table + fuzz tests | 160 |

> Note: the retry budget is still global. If you want it per tenant, that is a
> separate change and needs a config key ❤️.

---

`

// streamAnswer returns a ~7 KB markdown answer built from repeated sections so
// the streaming benchmark grows the live block the way a long real answer does.
func streamAnswer() string {
	return strings.Repeat(streamAnswerSection, 6) + "That's everything; run `task test` to verify.\n"
}

// streamChunks splits s into deltas of n bytes without splitting a UTF-8 rune.
func streamChunks(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		end := min(n, len(s))
		for end < len(s) && end > 0 && !utf8.RuneStart(s[end]) {
			end++
		}
		out = append(out, s[:end])
		s = s[end:]
	}
	return out
}

// headingRunsAnswer is a step-by-step answer of n headings, each followed by a
// fenced block and no prose. No block in it can end a settled prefix, so every
// frame renders the whole answer; the benchmark keeps that case linear.
func headingRunsAnswer(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "## Step %d\n\n```bash\necho step %d\n```\n\n", i, i)
	}
	return b.String()
}

// newStreamBenchModel returns a connected, sized Model with an open assistant
// block ready to receive streamed deltas.
func newStreamBenchModel(tb testing.TB) Model {
	tb.Helper()
	m := newTestModelFromDeps(Deps{
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		client.SessionReadyMsg{SessionID: "perf-stream-markdown"},
		client.TurnStartMsg{Turn: 1},
	)
	m.phase = phaseRunning
	m.conv.addUser("Explain the change.")
	m.conv.startAssistant()
	m.refreshView()
	return m
}

// BenchmarkStreamMarkdownAnswer streams one long markdown answer delta by delta
// through the real conversation render path, flushing a frame after every delta.
// One op is the whole answer, so ns/op is the CPU a user pays for one streamed
// response.
func BenchmarkStreamMarkdownAnswer(b *testing.B) {
	chunks := streamChunks(streamAnswer(), 32)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		m := newStreamBenchModel(b)
		b.StartTimer()
		for _, chunk := range chunks {
			m.conv.appendAssistant(chunk)
			m.refreshView()
		}
	}
}

// BenchmarkStreamMarkdownHeadingRuns streams headingRunsAnswer(40) the same way
// as BenchmarkStreamMarkdownAnswer.
func BenchmarkStreamMarkdownHeadingRuns(b *testing.B) {
	chunks := streamChunks(headingRunsAnswer(40), 32)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		m := newStreamBenchModel(b)
		b.StartTimer()
		for _, chunk := range chunks {
			m.conv.appendAssistant(chunk)
			m.refreshView()
		}
	}
}
