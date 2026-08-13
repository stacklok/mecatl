package webfetch

import (
	"strings"
	"testing"
)

func TestHTMLToTextPrefersMainAndExtractsTitle(t *testing.T) {
	text, title, err := htmlToText([]byte(`<!doctype html><html><head><title> A &amp; B </title></head><body><nav>Navigation</nav><main><h1>Heading &amp; more</h1><p>Useful&nbsp;text</p></main><footer>Footer</footer></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if title != "A & B" {
		t.Fatalf("title = %q", title)
	}
	if text != "Heading & more\nUseful text" {
		t.Fatalf("text = %q", text)
	}
}

func TestHTMLToTextExcludesNonContentElements(t *testing.T) {
	text, _, err := htmlToText([]byte(`<body><p>Visible</p><script>secret()</script><style>.hidden {}</style><template>template</template><noscript>fallback</noscript><svg><text>vector</text></svg><form>form</form><iframe>frame</iframe><object>object</object><embed src="ignored"></body>`))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Visible" {
		t.Fatalf("text = %q", text)
	}
}

func TestHTMLToTextFormatsListsTablesAndPre(t *testing.T) {
	text, _, err := htmlToText([]byte(`<body><ul><li>first</li><li>second</li></ul><table><tr><th>Name</th><th>Value</th></tr><tr><td>A</td><td>B</td></tr></table><pre>  keep
    spacing
</pre></body>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "- first\n- second\nName\tValue\nA\tB\n  keep\n    spacing"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestHTMLToTextMalformedHTMLIsDeterministic(t *testing.T) {
	input := []byte(`<html><head><title>Broken</title></head><body><main><p>one<div>two &amp; three`)
	first, firstTitle, err := htmlToText(input)
	if err != nil {
		t.Fatal(err)
	}
	second, secondTitle, err := htmlToText(input)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || firstTitle != secondTitle {
		t.Fatalf("non-deterministic output: (%q, %q), (%q, %q)", first, firstTitle, second, secondTitle)
	}
	if !strings.Contains(first, "one") || !strings.Contains(first, "two & three") {
		t.Fatalf("text = %q", first)
	}
}

func TestHTMLToTextDoesNotAccessURLs(t *testing.T) {
	text, _, err := htmlToText([]byte(`<body><main><img src="https://example.invalid/image"><link rel="stylesheet" href="https://example.invalid/site.css"><a href="https://example.invalid/">local text</a></main></body>`))
	if err != nil {
		t.Fatal(err)
	}
	if text != "local text" {
		t.Fatalf("text = %q", text)
	}
}
