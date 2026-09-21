package customization

import (
	"context"
	"strings"
	"testing"
)

func TestLookupAvailableToTitleAndStatusTemplates(t *testing.T) {
	title, err := NewTitleRenderer(`{{lookup .Session.Title "match" "title"}}`)
	if err != nil {
		t.Fatalf("NewTitleRenderer() error = %v", err)
	}
	gotTitle, err := title.Render(Input{Session: Session{Title: "match"}})
	if err != nil {
		t.Fatalf("TitleRenderer.Render() error = %v", err)
	}
	if want := "title"; gotTitle != want {
		t.Fatalf("title = %q, want %q", gotTitle, want)
	}

	status := parseStatusTemplate("footer", `<footer><text>{{lookup .Session.Title "match" "status"}}</text></footer>`, defaultTemplateSet().Footer.Full)
	doc := status.render(context.Background(), newTemplateInput(Input{Session: Session{Title: "match"}}))
	if got, want := statusSurfaceText(doc.Footer), "status"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func TestLookupMatchesVisibleTextAndReturnsFirstMatch(t *testing.T) {
	title, err := NewTitleRenderer(`{{lookup .Session.Title "<key>" "first" "<key>" "second"}}`)
	if err != nil {
		t.Fatalf("NewTitleRenderer() error = %v", err)
	}
	got, err := title.Render(Input{Session: Session{Title: "<key>"}})
	if err != nil {
		t.Fatalf("TitleRenderer.Render() error = %v", err)
	}
	if want := "first"; got != want {
		t.Fatalf("lookup first match = %q, want %q", got, want)
	}
}

func TestLookupReturnsEmptyForUnmatchedKey(t *testing.T) {
	title, err := NewTitleRenderer(`before{{lookup .Session.Title "match" "value"}}after`)
	if err != nil {
		t.Fatalf("NewTitleRenderer() error = %v", err)
	}
	got, err := title.Render(Input{Session: Session{Title: "other"}})
	if err != nil {
		t.Fatalf("TitleRenderer.Render() error = %v", err)
	}
	if want := "beforeafter"; got != want {
		t.Fatalf("lookup unmatched = %q, want %q", got, want)
	}
}

func TestLookupRejectsOddPairsDuringTemplateExecution(t *testing.T) {
	if _, err := NewTitleRenderer(`{{lookup "key" "unpaired"}}`); err == nil {
		t.Fatal("NewTitleRenderer() error = nil, want lookup pair validation error")
	}

	status := parseStatusTemplate("footer", `<footer>{{lookup "key" "unpaired"}}</footer>`, defaultTemplateSet().Footer.Full)
	if status.template == nil {
		t.Fatal("status template did not parse")
	}
	var output strings.Builder
	if err := status.template.Execute(&output, newTemplateInput(Input{})); err == nil {
		t.Fatal("status template execution error = nil, want lookup pair validation error")
	}
}

func TestLookupEscapesStatusValues(t *testing.T) {
	status := parseStatusTemplate("footer", `<footer><text>{{lookup "match" "match" "<primary>forged</primary>\x1b]8;;https://bad.example\x07"}}</text></footer>`, defaultTemplateSet().Footer.Full)
	doc := status.render(context.Background(), newTemplateInput(Input{}))
	if got, want := len(doc.Footer.Spans), 1; got != want {
		t.Fatalf("footer spans = %d, want %d", got, want)
	}
	span := doc.Footer.Spans[0]
	if got, want := span.Token, TokenText; got != want {
		t.Fatalf("status token = %q, want %q", got, want)
	}
	if got, want := span.Text, "<primary>forged</primary>]8;;https://bad.example"; got != want {
		t.Fatalf("status text = %q, want %q", got, want)
	}
	if strings.ContainsFunc(span.Text, terminalControl) {
		t.Fatalf("status text contains terminal control: %q", span.Text)
	}
}
