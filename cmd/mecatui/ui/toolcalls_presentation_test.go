package ui

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSkillIntentRequiresReceivedStringName(t *testing.T) {
	for _, tc := range []struct{ args, want string }{
		{`{"name":"test-writer","asset":"references/style.md"}`, "test-writer"},
		{`{"asset":"references/style.md"}`, "Skill"},
		{`{"name":42}`, "Skill"},
		{`{"name":"  "}`, "Skill"},
		{`not json`, "Skill"},
	} {
		if got := toolcallIntentFor("Skill", tc.args); got != tc.want {
			t.Errorf("Skill(%q) intent = %q, want %q", tc.args, got, tc.want)
		}
	}
	hostile := toolcallIntentFor("Skill", `{"name":"bad\u001b]8;;https://example.com\u0007\n\t\u202e界😀"}`)
	if strings.ContainsAny(hostile, "\x1b\a\n\r\t") || !strings.Contains(hostile, "bad") || !strings.Contains(hostile, "界😀") {
		t.Fatalf("Skill name is not safe single-line text: %q", hostile)
	}
}

func TestGrepIntentQuotesPatternAndShowsScope(t *testing.T) {
	for _, tc := range []struct{ args, want string }{
		{`{"pattern":"TODO","path":"cmd/mecatui/ui/*.go"}`, `"TODO" in cmd/mecatui/ui/*.go`},
		{`{"pattern":"a\\b\"c","path":"cmd/**"}`, `"a\\b\"c" in cmd/**`},
		{`{"pattern":"TODO"}`, `"TODO"`},
		{`{"pattern":"TODO","path":""}`, `"TODO"`},
		{`{"path":"cmd/**"}`, "cmd/**"},
	} {
		if got := toolcallIntentFor("Grep", tc.args); got != tc.want {
			t.Errorf("Grep(%s) intent = %q, want %q", tc.args, got, tc.want)
		}
	}
	hostile := toolcallIntentFor("Grep", `{"pattern":"bad\u001b[31m\nneedle","path":"cmd/\u001b]8;;https://example.com\u0007*.go"}`)
	if strings.ContainsAny(hostile, "\x1b\a\n\r\t") || !strings.HasPrefix(hostile, `"bad`) || !strings.Contains(hostile, `" in cmd/`) || !strings.HasSuffix(hostile, "*.go") {
		t.Fatalf("Grep intent lost scope or retained terminal controls: %q", hostile)
	}
}

func TestGrepIntentRenderingParityAndBounds(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("grep", "Grep", `{"pattern":"TODO","path":"cmd/**/*.go"}`)
	m.conv.resolveTool("grep", "matched", false)
	m.conv.addTool("hostile-grep", "Grep", `{"pattern":"bad\u001b[31m\nneedle","path":"cmd/\u001b]8;;https://example.com\u0007*.go"}`)
	m.conv.resolveTool("hostile-grep", "matched", false)

	m.rend.setWidth(52)
	frame := m.rend.renderConversationFrame(&m.conv.scrollback, false)
	rows := blockRows(frame, toolBlockID(t, m.conv.scrollback, "grep"))
	const want = `✓ Grep · "TODO" in cmd/**/*.go`
	if len(rows) != 1 || strings.TrimSpace(stripANSIstr(rows[0])) != want {
		t.Fatalf("scoped Grep conversation line = %q, want %q", rows, want)
	}
	inspector := openToolcallsForTest(t, m)
	list, _ := toolcallsForTest(t, inspector).Render(52, 24)
	if !strings.Contains(stripANSIstr(list), want) {
		t.Fatalf("inspector lost scoped Grep summary: %q", stripANSIstr(list))
	}

	m.rend.setWidth(18)
	frame = m.rend.renderConversationFrame(&m.conv.scrollback, false)
	for _, id := range []string{"grep", "hostile-grep"} {
		rows = blockRows(frame, toolBlockID(t, m.conv.scrollback, id))
		if len(rows) != 1 || strings.ContainsAny(stripANSIstr(rows[0]), "\x1b\a\n\r\t") || ansi.StringWidth(rows[0]) > 18 {
			t.Fatalf("narrow %s Grep row unsafe or unbounded: %q", id, rows)
		}
	}
	list, _ = toolcallsForTest(t, inspector).Render(52, 24)
	if strings.ContainsAny(stripANSIstr(list), "\x1b\a\r\t") || !strings.Contains(stripANSIstr(list), `Grep · "bad`) {
		t.Fatalf("hostile Grep escaped the inspector or lost pattern: %q", list)
	}
}

func TestToolcallPresentations(t *testing.T) {
	tests := []struct {
		name       string
		intent     string
		detailKeys []string
	}{
		{"Read", "Read-path", []string{"path", "offset", "limit"}},
		{"ListDir", "ListDir-path", []string{"path", "depth"}},
		{"Glob", "Glob-pattern", []string{"pattern", "path"}},
		{"Grep", `"Grep-pattern" in Grep-path`, []string{"pattern", "path"}},
		{toolEditName, "Edit-path", []string{"path", "old_string", "new_string"}},
		{toolWriteName, "Write-path", []string{"path", "content"}},
		{"Copy", "Copy-source → Copy-destination", []string{"source", "destination"}},
		{"Move", "Move-source → Move-destination", []string{"source", "destination"}},
		{"Remove", "Remove-path", []string{"path"}},
		{"Shell", "Shell-command", []string{"command"}},
		{"WebFetch", "WebFetch-url", []string{"url"}},
		{"FetchMcpResource", "FetchMcpResource-uri", []string{"uri"}},
		{"Skill", "Skill-name", []string{"asset", "name"}},
	}

	if got, want := len(toolcallPresentations), len(tests); got != want {
		t.Fatalf("presentation entries = %d, want %d", got, want)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := map[string]string{"z_extra": test.name + "-z", "a_extra": test.name + "-a"}
			for _, key := range test.detailKeys {
				fields[key] = test.name + "-" + key
			}
			arguments, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if got := toolcallIntentFor(test.name, string(arguments)); got != test.intent {
				t.Fatalf("intent = %q, want %q", got, test.intent)
			}

			keys := append(append([]string(nil), test.detailKeys...), "a_extra", "z_extra")
			if len(toolcallPresentations[test.name].argumentKeys) == 0 {
				sort.Strings(keys)
			}
			want := make([]string, 0, len(keys))
			for _, key := range keys {
				want = append(want, argumentLabel(key)+": "+fields[key])
			}
			if got := toolcallArgumentLines(test.name, string(arguments)); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("detail arguments = %q, want %q", got, want)
			}
		})
	}
}

func TestToolcallPresentationUnknownFallback(t *testing.T) {
	arguments := `{"z":"last","path":"target","a":"first"}`
	if got, want := toolcallIntentFor("McpCustom", arguments), "target"; got != want {
		t.Fatalf("unknown intent = %q, want %q", got, want)
	}
	if got, want := strings.Join(toolcallArgumentLines("McpCustom", arguments), "\n"), "A: first\nPath: target\nZ: last"; got != want {
		t.Fatalf("unknown detail arguments = %q, want %q", got, want)
	}
	if got, want := toolcallIntentFor("Copy", `{}`), "Copy"; got != want {
		t.Fatalf("known missing fields intent = %q, want %q", got, want)
	}
	if got := toolcallArgumentLines("Copy", `{}`); len(got) != 0 {
		t.Fatalf("known missing fields detail = %q, want no lines", got)
	}

	if got, want := toolcallIntentFor("bad\x1bname", `not json`), "badname"; got != want {
		t.Fatalf("malformed intent = %q, want %q", got, want)
	}
	if got := strings.Join(toolcallArgumentLines("McpCustom", "not\x1b json"), "\n"); got != "Original arguments: not json" {
		t.Fatalf("malformed detail arguments = %q", got)
	}
}

func TestToolcallPresentationDetailDoesNotTruncateLongValues(t *testing.T) {
	content := strings.Repeat("long-content-", 100)
	arguments := `{"path":"file.txt","content":"` + content + `"}`
	lines := toolcallArgumentLines(toolWriteName, arguments)
	if got, want := strings.Join(lines, "\n"), "Path: file.txt\nContent: "+content; got != want {
		t.Fatalf("long detail was changed or truncated: %d bytes", len(got))
	}
}
