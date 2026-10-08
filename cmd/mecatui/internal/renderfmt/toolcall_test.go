package renderfmt_test

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/renderfmt"
)

func TestToolIntentAndArgumentOrder(t *testing.T) {
	tests := []struct {
		name, arguments, intent string
		order                   []string
	}{
		{"Read", `{"path":"read-path"}`, "read-path", []string{"path", "offset", "limit"}},
		{"ListDir", `{"path":"list-path"}`, "list-path", []string{"path", "depth"}},
		{"Glob", `{"pattern":"*.go"}`, "*.go", []string{"pattern", "path"}},
		{"Grep", `{"pattern":"TODO","path":"cmd/**/*.go"}`, `"TODO" in cmd/**/*.go`, []string{"pattern", "path"}},
		{"Edit", `{"path":"edit-path"}`, "edit-path", []string{"path", "old_string", "new_string"}},
		{"Write", `{"path":"write-path"}`, "write-path", []string{"path", "content"}},
		{"Copy", `{"source":"from","destination":"to"}`, "from → to", []string{"source", "destination"}},
		{"Move", `{"source":"from","destination":"to"}`, "from → to", []string{"source", "destination"}},
		{"Remove", `{"path":"remove-path"}`, "remove-path", []string{"path"}},
		{"Shell", `{"command":"go test ./..."}`, "go test ./...", []string{"command"}},
		{"WebFetch", `{"url":"https://example.test"}`, "https://example.test", []string{"url"}},
		{"FetchMcpResource", `{"uri":"mcp://example"}`, "mcp://example", []string{"uri"}},
		{"Skill", `{"name":"test-writer"}`, "test-writer", nil},
		{"McpCustom", `{"target":"target"}`, "target", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := renderfmt.ToolIntent(test.name, test.arguments); got != test.intent {
				t.Fatalf("ToolIntent() = %q, want %q", got, test.intent)
			}
			if got := renderfmt.ArgumentOrder(test.name); !equalStrings(got, test.order) {
				t.Fatalf("ArgumentOrder() = %q, want %q", got, test.order)
			}
		})
	}

	order := renderfmt.ArgumentOrder("Read")
	order[0] = "changed"
	if got := renderfmt.ArgumentOrder("Read")[0]; got != "path" {
		t.Fatalf("ArgumentOrder returned shared storage: %q", got)
	}
}

func TestToolIntentFallbacksAreSafeAndPrecise(t *testing.T) {
	for _, test := range []struct {
		name, arguments, want string
	}{
		{"Copy", `{}`, "Copy"},
		{"McpCustom", `{"precise":12345678901234567890123456789,"path":"target"}`, "target"},
		{"McpCustom", `{"precise":12345678901234567890123456789}`, "McpCustom"},
		{"Shell", `{"command":12345678901234567890123456789}`, "12345678901234567890123456789"},
		{"bad\x1bname", `not json`, "badname"},
		{"Read", `{"path":"truncated"`, "Read"},
	} {
		if got := renderfmt.ToolIntent(test.name, test.arguments); got != test.want {
			t.Errorf("ToolIntent(%q, %q) = %q, want %q", test.name, test.arguments, got, test.want)
		}
	}

	hostile := renderfmt.ToolIntent("Grep", `{"pattern":"bad\u001b[31m\nneedle","path":"cmd/\u001b]8;;https://example.com\u0007*.go"}`)
	if strings.ContainsAny(hostile, "\x1b\a\n\r\t") || !strings.HasPrefix(hostile, `"bad`) || !strings.HasSuffix(hostile, "*.go") {
		t.Fatalf("ToolIntent retained terminal controls or lost content: %q", hostile)
	}
}

func TestPresentToolLineStates(t *testing.T) {
	for _, test := range []struct {
		name, glyph, status, style string
		state                      renderfmt.ToolState
	}{
		{"running", "…", "running", "toolName", renderfmt.ToolRunning},
		{"awaiting result", "…", "awaiting result", "toolName", renderfmt.ToolAwaitingResult},
		{"finalizing success", "✓", "result received · finalizing", "toolOk", renderfmt.ToolFinalizing},
		{"finalizing failure", "✗", "failed · finalizing", "toolErr", renderfmt.ToolFinalizingFailed},
		{"delegated pending", "…", "pending", "toolName", renderfmt.ToolDelegatedPending},
		{"success", "✓", "", "toolOk", renderfmt.ToolSucceeded},
		{"failure", "✗", "failed", "toolErr", renderfmt.ToolFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			line := renderfmt.PresentToolLine("Read\n", "notes.txt\x1b[31m", test.state)
			if line.Glyph() != test.glyph || line.Status() != test.status || line.StatusStyle() != test.style {
				t.Fatalf("line = %#v, want glyph=%q status=%q style=%q", line, test.glyph, test.status, test.style)
			}
			want := test.glyph + " Read · notes.txt[31m"
			if test.status != "" {
				want += " · " + test.status
			}
			if got := line.Text(); got != want {
				t.Fatalf("Text() = %q, want %q", got, want)
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
