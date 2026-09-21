package customization

import (
	"context"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestADR_0247_SourcePublicAPI pins the concise source boundary consumed by
// mecatui composition and UI without changing its source-owned lifecycle.
func TestADR_0247_SourcePublicAPI(t *testing.T) {
	source := NewDefaultSource(0)
	acceptSource(source)
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	source.Submit(Input{})
	acceptResult(source.Latest())
}

func acceptSource(Source) {}
func acceptResult(Result) {}

func TestADR_0247_SourceHasNoUIDependency(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "source.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		if path := strings.Trim(imp.Path.Value, `"`); strings.Contains(path, "/ui") || strings.Contains(path, "bubbletea") {
			t.Fatalf("source imports %q", path)
		}
	}
}
func TestADR_0247_SourceLatestWinsCoalesces(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	s := newSource(nil, func(_ context.Context, input Input) Result {
		if input.Session.Title == "first" {
			close(started)
			<-release
		}
		return Result{Header: Surface{Present: true, Spans: []Span{{Text: input.Session.Title}}}, Footer: Surface{Present: true}}
	})
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	s.Submit(Input{Session: Session{Title: "first"}})
	<-started
	s.Submit(Input{Session: Session{Title: "latest"}})
	close(release)
	select {
	case <-s.Changed():
	case <-time.After(time.Second):
		t.Fatal("no publication")
	}
	if got := s.Latest().Header.Spans[0].Text; got != "latest" {
		t.Fatalf("got %q", got)
	}
}
func TestADR_0247_SourceSnapshotsAreIndependentAndTerminalSafe(t *testing.T) {
	s := newSource(nil, func(_ context.Context, input Input) Result {
		return Result{Header: Surface{Present: true, Spans: []Span{{Text: input.Session.Title + "\x1b]8;;https://bad.example\a", Href: "https://example.test/\x1b"}}}}
	})
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	s.Submit(Input{Session: Session{Title: "safe"}})
	select {
	case <-s.Changed():
	case <-time.After(time.Second):
		t.Fatal("no publication")
	}
	first := s.Latest()
	if strings.ContainsFunc(first.Header.Spans[0].Text+first.Header.Spans[0].Href, terminalControl) {
		t.Fatal("terminal control escaped source")
	}
	first.Header.Spans[0].Text = "mutated"
	if s.Latest().Header.Spans[0].Text == "mutated" {
		t.Fatal("mutable snapshot")
	}
}
func TestADR_0247_SourceCloseCancelsRender(t *testing.T) {
	started := make(chan struct{})
	s := newSource(nil, func(ctx context.Context, input Input) Result {
		if input.Session.Title == "block" {
			close(started)
			<-ctx.Done()
		}
		return Result{}
	})
	s.Submit(Input{Session: Session{Title: "block"}})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}
func TestStatusLine_CloneNormalizedSurfaceIsIdempotent(t *testing.T) {
	spans := []Span{{
		Text:      "safe\x1b]8;;https://bad.example\a",
		Color:     "untrusted",
		Underline: true,
		Href:      "javascript:alert(1)",
		Token:     Token("unknown"),
	}}

	once := cloneNormalizedSurface(Surface{Present: true, Spans: spans})
	twice := cloneNormalizedSurface(once)
	if !reflect.DeepEqual(twice, once) {
		t.Fatalf("repeated normalization changed surface: once=%#v twice=%#v", once, twice)
	}
	if got := once.Spans[0]; got.Color != "" || got.Underline || got.Href != "" || got.Token != TokenText || strings.ContainsFunc(got.Text, terminalControl) {
		t.Fatalf("normalization left unsafe span: %#v", got)
	}
}

func TestStatusLine_Scenario2_TemplateEscapesValues(t *testing.T) {
	s := NewTemplateSource(TemplateSet{Header: SurfaceTemplates{Full: `<header><accent>{{.Session.Title}}</accent></header>`}}, 0)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	s.Submit(Input{Session: Session{Title: "name</accent><error>forged</error>\x1b]8;;x\a"}, Terminal: Terminal{HeaderAvailCols: 100}})
	select {
	case <-s.Changed():
	case <-time.After(time.Second):
		t.Fatal("no publication")
	}
	for _, span := range s.Latest().Header.Spans {
		if span.Token == TokenError || strings.ContainsFunc(span.Text, terminalControl) {
			t.Fatalf("forged span %#v", span)
		}
	}
}
func TestStatusLine_CompactHeaderLabelsPermissionMode(t *testing.T) {
	s := NewDefaultSource(0)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	s.Submit(Input{
		Session:  Session{Handle: "deadbeef", Mode: "accept-edits"},
		Model:    Model{DisplayName: "GPT-5"},
		Terminal: Terminal{HeaderAvailCols: 50, FooterAvailCols: 80},
	})
	select {
	case <-s.Changed():
	case <-time.After(time.Second):
		t.Fatal("shipped source did not publish")
	}
	if got, want := statusSurfaceText(s.Latest().Header), "mecatui · GPT-5 · mode accept-edits"; got != want {
		t.Fatalf("compact header = %q, want %q", got, want)
	}
}

func TestStatusCustomization_Scenario2_ReservedLanesAndResponsiveSelection(t *testing.T) {
	s := NewTemplateSource(TemplateSet{
		Header: SurfaceTemplates{
			Full:    `<header><text>header-full</text></header>`,
			Compact: `<header><text>head</text></header>`,
			Minimal: `<header><text>h</text></header>`,
		},
		Footer: SurfaceTemplates{
			Full:    `<footer><text>footer-full</text></footer>`,
			Compact: `<footer><text>foot</text></footer>`,
			Minimal: `<footer><text>f</text></footer>`,
		},
	}, 0)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	for _, tc := range []struct {
		name                             string
		headerAvailable, footerAvailable int
		wantHeader, wantFooter           string
	}{
		{name: "full variants", headerAvailable: len("header-full"), footerAvailable: len("footer-full"), wantHeader: "header-full", wantFooter: "footer-full"},
		{name: "independent compact and full", headerAvailable: len("head"), footerAvailable: len("footer-full"), wantHeader: "head", wantFooter: "footer-full"},
		{name: "independent minimal and compact", headerAvailable: len("h"), footerAvailable: len("foot"), wantHeader: "h", wantFooter: "foot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.Submit(Input{Terminal: Terminal{HeaderAvailCols: tc.headerAvailable, FooterAvailCols: tc.footerAvailable}})
			select {
			case <-s.Changed():
			case <-time.After(time.Second):
				t.Fatal("no publication")
			}
			line := s.Latest()
			if got := statusSurfaceText(line.Header); got != tc.wantHeader {
				t.Fatalf("header = %q, want %q", got, tc.wantHeader)
			}
			if got := statusSurfaceText(line.Footer); got != tc.wantFooter {
				t.Fatalf("footer = %q, want %q", got, tc.wantFooter)
			}
		})
	}
}

func TestADR_0247_SourceFallsBackToShippedDefaultOnInvalidVariant(t *testing.T) {
	s := NewTemplateSource(TemplateSet{
		Header: SurfaceTemplates{
			Full: `{{.Missing}}`, Compact: `{{.Missing}}`, Minimal: `{{.Missing}}`,
		},
	}, 0)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	s.Submit(Input{Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	select {
	case <-s.Changed():
	case <-time.After(time.Second):
		t.Fatal("no publication")
	}
	line := s.Latest()
	if !line.Header.Present || !line.Footer.Present {
		t.Fatalf("source fallback = %#v, want shipped surfaces", line)
	}
	if got := statusSurfaceText(line.Header); !strings.Contains(got, "mecatui") {
		t.Fatalf("header = %q, want shipped default", got)
	}
}
