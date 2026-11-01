package ui

// Benchmarks for the assistant markdown cache: the memory a long session keeps
// per answer, and the cost of a conversation-detail flip over a long session.
// Neither runs under `task test`; run them with, for example:
//
//	go test ./cmd/mecatui/ui/ -run '^$' -bench 'LongSessionRetainedHeap|ExpandToggle' -benchtime 300x -count 10

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// expandToggleTurns is the session length the detail-flip benchmark builds.
const expandToggleTurns = 200

// markdownCacheAnswer is a ~1 KB assistant answer with a heading, a list, a
// fenced code block, and a table, numbered so every answer is distinct.
func markdownCacheAnswer(i int) string {
	var b strings.Builder
	b.WriteString("## Result for step " + strconv.Itoa(i) + "\n\n")
	b.WriteString("I looked at the **handler** and the `Store` interface. Here is what changed and why it matters.\n\n")
	for j := range 6 {
		b.WriteString("- item " + strconv.Itoa(j) + ": the function now returns an error instead of panicking on empty input\n")
	}
	b.WriteString("\n```go\nfunc Handle(ctx context.Context, req *Request) (*Response, error) {\n\tif req == nil {\n\t\treturn nil, errEmpty\n\t}\n\treturn &Response{ID: req.ID}, nil\n}\n```\n\n")
	b.WriteString("| file | change | lines |\n|---|---|---|\n| a.go | refactor | 12 |\n| b.go | fix | 3 |\n\n")
	b.WriteString("The change is small and self-contained.\n")
	return b.String()
}

func newMarkdownCacheBenchModel(tb testing.TB) Model {
	tb.Helper()
	m := newTestModelFromDeps(Deps{
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		client.SessionReadyMsg{SessionID: "bench-markdown-cache"},
	)
	m.phase = phaseRunning
	return m
}

// addMarkdownCacheTurn appends one prompt and one answer, optionally with a
// reasoning summary, and renders the frame the way a finished turn does.
func addMarkdownCacheTurn(m *Model, i int, reasoning bool) {
	m.conv.addUser("question " + strconv.Itoa(i))
	if reasoning {
		m.conv.appendReasoning("**Weighing the options**\nRead the handler first, then the tests.\n")
	}
	m.conv.appendAssistant(markdownCacheAnswer(i))
	m.refreshView()
}

func liveHeapBytes() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// BenchmarkLongSessionRetainedHeap adds one turn per op to a single session.
// retained-B/turn is the live heap the session grew by, divided by its turns.
func BenchmarkLongSessionRetainedHeap(b *testing.B) {
	m := newMarkdownCacheBenchModel(b)
	m.refreshView()
	before := liveHeapBytes()
	turns := 0
	for b.Loop() {
		addMarkdownCacheTurn(&m, turns, false)
		turns++
	}
	after := liveHeapBytes()
	runtime.KeepAlive(&m) // the session must still be live when after is read
	b.ReportMetric((float64(after)-float64(before))/float64(turns), "retained-B/turn")
}

// BenchmarkExpandToggle flips conversation details over a long session, with
// and without a reasoning summary on every answer. glamour/op counts the
// markdown renders each flip performs.
func BenchmarkExpandToggle(b *testing.B) {
	for _, reasoning := range []bool{false, true} {
		name := "plain"
		if reasoning {
			name = "reasoning"
		}
		b.Run(name, func(b *testing.B) {
			m := newMarkdownCacheBenchModel(b)
			for i := range expandToggleTurns {
				addMarkdownCacheTurn(&m, i, reasoning)
			}
			base, flips := m.rend.mdRenders, 0
			for b.Loop() {
				m.expandConversation = !m.expandConversation
				m.refreshView()
				flips++
			}
			b.ReportMetric(float64(m.rend.mdRenders-base)/float64(flips), "glamour/op")
		})
	}
}
