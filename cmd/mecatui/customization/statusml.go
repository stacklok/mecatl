// Package customization defines the display-only, dependency-leaf presentation customization protocol
// shared by mecatui composition and UI status-line renderers.
package customization

import (
	"html"
	"net/url"
	"strings"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

const (
	// ProtocolVersion is the current Input wire-independent contract version.
	ProtocolVersion uint8 = 3

	maxMarkupBytes = 16 << 10
	maxSpanRunes   = 4 << 10
	maxSpans       = 256
	maxLinkBytes   = 2 << 10
)

// Token is a closed semantic style vocabulary for StatusML text.
type Token string

// Token values are the only semantic styles StatusML accepts.
const (
	TokenText      Token = "text"
	TokenMuted     Token = "muted"
	TokenPrimary   Token = "primary"
	TokenSecondary Token = "secondary"
	TokenAccent    Token = "accent"
	TokenSuccess   Token = "success"
	TokenWarning   Token = "warning"
	TokenError     Token = "error"
	TokenInfo      Token = "info"
)

// Palette resolves a StatusML semantic token through the active theme. It
// returns descriptive style data, never terminal control sequences.
type Palette interface {
	StatusColor(Token) string
}

// LinkPalette is an optional theme extension for StatusML links. Render never
// emits OSC 8; consumers decide how to display the validated destination.
type LinkPalette interface {
	StatusLinkColor() string
	StatusLinkUnderline() bool
}

// Span is one terminal-safe text run resolved through a Palette. Href is a
// separately validated link destination and is never included in Text.
type Span struct {
	Text      string
	Token     Token
	Color     string
	Href      string
	Underline bool
}

// Surface is an optional header or footer. Present distinguishes an absent surface
// from a present empty one. The renderer determines alignment by surface: headers
// are left-aligned and footers are right-aligned.
type Surface struct {
	Present bool
	Spans   []Span
}

// Document is a parsed StatusML document. Header and Footer are independently
// optional through their Present fields.
type Document struct {
	Header Surface
	Footer Surface
}

// Render parses bounded StatusML, safely literalizing malformed or unknown
// markup, then resolves every semantic token via palette. It never returns raw
// terminal control bytes from either markup text or palette data.
func Render(markup string, palette Palette) Document {
	doc, ok := parse(markup)
	if !ok {
		doc = literalDocument(markup)
	}
	for _, surface := range []*Surface{&doc.Header, &doc.Footer} {
		resolveSpans(surface.Spans, palette)
	}
	return doc
}

func resolveSpans(spans []Span, palette Palette) {
	for i := range spans {
		if spans[i].Href != "" {
			if links, ok := palette.(LinkPalette); ok {
				spans[i].Color = terminaltext.SanitizeSingleLine(links.StatusLinkColor())
				spans[i].Underline = links.StatusLinkUnderline()
				continue
			}
		}
		if palette != nil {
			spans[i].Color = terminaltext.SanitizeSingleLine(palette.StatusColor(spans[i].Token))
		}
	}
}

type element struct {
	name string
	href string
}

type parseState struct {
	doc     Document
	stack   []element
	surface *Surface
	spans   *[]Span
}

func parse(markup string) (Document, bool) {
	if len(markup) > maxMarkupBytes {
		return Document{}, false
	}
	if strings.HasPrefix(markup, "<status>") {
		if !strings.HasSuffix(markup, "</status>") {
			return Document{}, false
		}
		markup = strings.TrimSuffix(strings.TrimPrefix(markup, "<status>"), "</status>")
	}
	state := parseState{}
	for len(markup) > 0 {
		i := strings.IndexByte(markup, '<')
		if i < 0 {
			return state.doc, state.text(markup)
		}
		if i > 0 && !state.text(markup[:i]) {
			return Document{}, false
		}
		markup = markup[i:]
		end := strings.IndexByte(markup, '>')
		if end < 0 || end == 1 || !state.tag(markup[1:end]) {
			return Document{}, false
		}
		markup = markup[end+1:]
	}
	if len(state.stack) != 0 || (!state.doc.Header.Present && !state.doc.Footer.Present) {
		return Document{}, false
	}
	return state.doc, true
}

func (s *parseState) text(raw string) bool {
	if raw == "" {
		return true
	}
	if s.spans == nil {
		return false
	}
	text := truncateRunes(terminaltext.SanitizeSingleLine(html.UnescapeString(terminaltext.SanitizeSingleLine(raw))), maxSpanRunes)
	if text == "" {
		return true
	}
	token, href := TokenText, ""
	for i := len(s.stack) - 1; i >= 0; i-- {
		if isToken(s.stack[i].name) {
			token = Token(s.stack[i].name)
		}
		if s.stack[i].href != "" {
			href = s.stack[i].href
		}
	}
	spans := *s.spans
	if len(spans) > 0 && spans[len(spans)-1].Token == token && spans[len(spans)-1].Href == href {
		spans[len(spans)-1].Text = truncateRunes(spans[len(spans)-1].Text+text, maxSpanRunes)
		*s.spans = spans
		return true
	}
	if len(spans) == maxSpans {
		return false
	}
	*s.spans = append(spans, Span{Text: text, Token: token, Href: href})
	return true
}

func (s *parseState) tag(tag string) bool {
	if strings.HasPrefix(tag, "/") {
		name := strings.TrimPrefix(tag, "/")
		if name == "" || strings.ContainsAny(name, " \t\n") || len(s.stack) == 0 || s.stack[len(s.stack)-1].name != name {
			return false
		}
		s.stack = s.stack[:len(s.stack)-1]
		s.updateLocation()
		return true
	}
	el, ok := openingElement(tag)
	if !ok {
		return false
	}
	if len(s.stack) == 0 {
		switch el.name {
		case "header":
			if s.doc.Header.Present {
				return false
			}
			s.doc.Header.Present, s.surface = true, &s.doc.Header
		case "footer":
			if s.doc.Footer.Present {
				return false
			}
			s.doc.Footer.Present, s.surface = true, &s.doc.Footer
		default:
			return false
		}
	} else if !s.validChild(el.name) {
		return false
	}
	s.stack = append(s.stack, el)
	s.updateLocation()
	return true
}

func openingElement(tag string) (element, bool) {
	if strings.HasPrefix(tag, "link") {
		const prefix = `link href="`
		if !strings.HasPrefix(tag, prefix) || !strings.HasSuffix(tag, `"`) {
			return element{}, false
		}
		href := strings.TrimSuffix(strings.TrimPrefix(tag, prefix), `"`)
		if !validLink(href) {
			// A syntactically valid but unsafe destination degrades to its display
			// text. The parser never keeps the destination or emits terminal links.
			return element{name: "link"}, true
		}
		return element{name: "link", href: href}, true
	}
	if !validName(tag) {
		return element{}, false
	}
	return element{name: tag}, true
}

func validLink(href string) bool {
	if href == "" || len(href) > maxLinkBytes || href != terminaltext.SanitizeSingleLine(href) {
		return false
	}
	u, err := url.ParseRequestURI(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return false
	}
	return true
}

func (s *parseState) validChild(name string) bool {
	parent := s.stack[len(s.stack)-1].name
	return (parent == "header" || parent == "footer") && (isToken(name) || name == "link")
}

func (s *parseState) updateLocation() {
	s.spans = nil
	if len(s.stack) == 0 || s.surface == nil {
		return
	}
	s.spans = &s.surface.Spans
}

func validName(name string) bool {
	return name == "header" || name == "footer" || isToken(name)
}

func isToken(name string) bool {
	switch Token(name) {
	case TokenText, TokenMuted, TokenPrimary, TokenSecondary, TokenAccent, TokenSuccess, TokenWarning, TokenError, TokenInfo:
		return true
	default:
		return false
	}
}

func literalDocument(markup string) Document {
	return Document{Footer: Surface{
		Present: true,
		Spans:   []Span{{Text: truncateRunes(terminaltext.SanitizeSingleLine(markup), maxSpanRunes), Token: TokenText}},
	}}
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == limit {
			return s[:i] + "…"
		}
		count++
	}
	return s
}
