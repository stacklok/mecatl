package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolcallPresentations(t *testing.T) {
	tests := []struct {
		name       string
		intent     string
		detailKeys []string
	}{
		{"Read", "Read Read-path", []string{"path", "offset", "limit"}},
		{"ListDir", "List ListDir-path", []string{"path", "depth"}},
		{"Glob", "Find Glob-pattern", []string{"pattern", "path"}},
		{"Grep", "Search Grep-pattern", []string{"pattern", "path"}},
		{toolEditName, "Edit Edit-path", []string{"path", "old_string", "new_string"}},
		{toolWriteName, "Write Write-path", []string{"path", "content"}},
		{"Copy", "Copy Copy-source → Copy-destination", []string{"source", "destination"}},
		{"Move", "Move Move-source → Move-destination", []string{"source", "destination"}},
		{"Remove", "Remove Remove-path", []string{"path"}},
		{"Shell", "Run Shell-command", []string{"command"}},
		{"WebFetch", "Fetch WebFetch-url", []string{"url"}},
		{"FetchMcpResource", "Fetch FetchMcpResource-uri", []string{"uri"}},
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

			want := make([]string, 0, len(test.detailKeys)+2)
			for _, key := range append(append([]string(nil), test.detailKeys...), "a_extra", "z_extra") {
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
	if got, want := toolcallIntentFor("McpCustom", arguments), "McpCustom target"; got != want {
		t.Fatalf("unknown intent = %q, want %q", got, want)
	}
	if got, want := strings.Join(toolcallArgumentLines("McpCustom", arguments), "\n"), "A: first\nPath: target\nZ: last"; got != want {
		t.Fatalf("unknown detail arguments = %q, want %q", got, want)
	}
	if got, want := toolcallIntentFor("Copy", `{}`), "Copy  → "; got != want {
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
