package ui

import (
	"bytes"
	"strings"

	"charm.land/glamour/v2/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmrenderer "github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// streamMarkdown renders the live (streaming) assistant block's markdown
// incrementally. Re-rendering the whole growing answer through glamour on every
// flushed delta made each delta cost more than the last; here the answer's
// finished top-level blocks are rendered once and only the still-open tail is
// re-rendered.
//
// It drives glamour's own ANSI renderer over a goldmark document it parses itself
// with the same extensions and options glamour.NewTermRenderer uses, so the
// output is byte-identical to a full markdownWidth render (the property test in
// stream_markdown_test.go pins this). Splitting relies on three facts about
// glamour's renderer:
//
//   - A top-level block's output depends only on its own subtree and on whether it
//     has a previous sibling (headings and paragraphs emit a leading newline when
//     they do). A tail rendered after a settled prefix therefore starts with an
//     empty placeholder paragraph, which renders nothing but gives the first real
//     block its previous sibling.
//   - The document's closing wrap and padding pass is line-local; its only cross-
//     line state is the current pen, which is reset at a newline. When the prefix's
//     raw output ends in a bare newline (no style re-applied after it), rendering
//     the prefix and the tail as two documents and concatenating them equals one
//     render of the whole document. A prefix that ends mid-line (a heading, whose
//     line is terminated by the next block) is not a split point.
//   - Later lines can rewrite earlier blocks (setext underlines, definition lists,
//     lazy continuation, list items joining a list). Every render re-parses the
//     whole answer and keeps the settled prefix only while its blocks keep the same
//     kinds and source spans and the source before the split is unchanged. Link
//     reference definitions resolve across the whole document, so an answer that
//     defines any is always rendered in full.
//
// State belongs to one block (idx) at one wrap width; a different block or width
// starts over with a full render. It is used only on the update goroutine.
type streamMarkdown struct {
	md    goldmark.Markdown
	width int // glamour wrap width md renders at; 0 until first use
	style ansi.StyleConfig

	idx     int
	started bool // a full render of idx has seeded this state

	// src is the normalized source before the split; settled describes the
	// prefix's top-level blocks, and out is their post-processed output. out ends
	// in a newline whenever settled is non-empty.
	src     string
	settled []mdBlockSpan
	out     string
}

// mdBlockSpan identifies a parsed top-level block: its kind plus the first and
// last source offsets covered by its lines and text (-1 when it has none, as for a
// thematic break).
type mdBlockSpan struct {
	kind        ast.NodeKind
	first, last int
}

// markdownStreamed is markdown for conversation block idx, rendered
// incrementally while that block grows. It produces exactly markdown(src).
func (r *renderer) markdownStreamed(idx int, src string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	if r.stream.md == nil {
		r.stream.style = r.th.GlamourStyle()
	}
	out, ok := r.stream.render(idx, normalizeEmojiWidth(src), glamourWrapWidth(r.contentWidth()-assistantBodyHang))
	if !ok {
		r.stream.started = false
		return r.markdown(src)
	}
	r.mdRenders++
	return out
}

// newStreamMarkdownRenderer builds goldmark with the extensions, parser options,
// and ANSI renderer priority glamour.NewTermRenderer uses, so documents parsed and
// rendered here match glamour's own output.
func newStreamMarkdownRenderer(style ansi.StyleConfig, width int) goldmark.Markdown {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	ar := ansi.NewRenderer(ansi.Options{WordWrap: width, Styles: style})
	md.SetRenderer(gmrenderer.NewRenderer(gmrenderer.WithNodeRenderers(util.Prioritized(ar, 1000))))
	return md
}

// render returns the post-processed glamour output of normalized source norm at
// wrap width w for conversation block idx. ok is false when rendering failed and
// the caller must fall back to the full path.
func (s *streamMarkdown) render(idx int, norm string, w int) (string, bool) {
	if s.md == nil || s.width != w {
		s.md = newStreamMarkdownRenderer(s.style, w)
		s.width = w
		s.started = false
	}
	if !s.started || s.idx != idx {
		s.idx, s.started = idx, true
		s.resetPrefix()
		// A block's first render is a full one: settled blocks re-rendered for
		// another reason (a resize or a rebuild) never pay for splitting. Only a
		// block rendered again, which is a block still streaming, builds a prefix.
		return s.renderAll(norm)
	}

	source := []byte(norm)
	pc := parser.NewContext()
	doc := s.md.Parser().Parse(text.NewReader(source), parser.WithContext(pc))
	if len(pc.References()) > 0 {
		s.resetPrefix()
		return s.renderDoc(doc, source)
	}
	var blocks []ast.Node
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		blocks = append(blocks, c)
	}
	spans := make([]mdBlockSpan, len(blocks))
	for i, b := range blocks {
		spans[i] = blockSpan(b)
	}
	if !s.prefixHolds(norm, spans) {
		s.resetPrefix()
	}

	// Settle finished blocks: every block but the last can join the prefix, at a
	// split point where the next block's source position is known and the
	// prefix's raw output ends in a bare newline.
	k := len(s.settled)
	var pending []ast.Node
	for j := k; j < len(blocks)-1; j++ {
		pending = append(pending, blocks[j])
		split := spans[j+1].first
		if split < 0 {
			continue
		}
		raw, ok := s.renderBlocks(pending, source, k > 0)
		if !ok {
			return "", false
		}
		if !strings.HasSuffix(raw, "\n") {
			continue
		}
		s.out += trimTrailingSpaces(raw)
		s.settled = append(s.settled, spans[k:j+1]...)
		s.src = norm[:split]
		k, pending = j+1, nil
	}

	raw, ok := s.renderBlocks(blocks[k:], source, k > 0)
	if !ok {
		return "", false
	}
	tail := strings.TrimRight(raw, "\n")
	if tail == "" {
		return trimTrailingSpaces(strings.TrimRight(s.out, "\n")), true
	}
	return s.out + trimTrailingSpaces(tail), true
}

// prefixHolds reports whether the settled prefix is still the start of the
// document parsed from norm.
func (s *streamMarkdown) prefixHolds(norm string, spans []mdBlockSpan) bool {
	k := len(s.settled)
	if k == 0 {
		return true
	}
	if len(spans) <= k || !strings.HasPrefix(norm, s.src) {
		return false
	}
	for i := range k {
		if spans[i] != s.settled[i] {
			return false
		}
	}
	return true
}

func (s *streamMarkdown) resetPrefix() {
	s.src, s.settled, s.out = "", s.settled[:0], ""
}

// renderAll renders norm as one document.
func (s *streamMarkdown) renderAll(norm string) (string, bool) {
	source := []byte(norm)
	return s.renderDoc(s.md.Parser().Parse(text.NewReader(source)), source)
}

// renderDoc renders a whole document with markdownWidth's post-processing.
func (s *streamMarkdown) renderDoc(doc ast.Node, source []byte) (string, bool) {
	var buf bytes.Buffer
	if err := s.md.Renderer().Render(&buf, source, doc); err != nil {
		return "", false
	}
	return trimTrailingSpaces(strings.TrimRight(buf.String(), "\n")), true
}

// renderBlocks moves blocks into a fresh document and returns its raw glamour
// output. afterPrefix prepends an empty paragraph so the first block renders as
// it does after a previous sibling. The moved blocks leave the parsed document;
// callers render each block at most once per parse, except that a pending run
// rejected as a split point is moved again, with more blocks, on the next try.
func (s *streamMarkdown) renderBlocks(blocks []ast.Node, source []byte, afterPrefix bool) (string, bool) {
	doc := ast.NewDocument()
	if afterPrefix {
		doc.AppendChild(doc, ast.NewParagraph())
	}
	for _, b := range blocks {
		doc.AppendChild(doc, b)
	}
	var buf bytes.Buffer
	if err := s.md.Renderer().Render(&buf, source, doc); err != nil {
		return "", false
	}
	return buf.String(), true
}

// blockSpan returns the kind and source extent of a top-level block from the
// lines of its block descendants, its text segments, and fenced-code info.
func blockSpan(n ast.Node) mdBlockSpan {
	span := mdBlockSpan{kind: n.Kind(), first: -1, last: -1}
	note := func(seg text.Segment) {
		if span.first < 0 || seg.Start < span.first {
			span.first = seg.Start
		}
		if seg.Stop > span.last {
			span.last = seg.Stop
		}
	}
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			note(c.Segment)
		case *ast.FencedCodeBlock:
			if c.Info != nil {
				note(c.Info.Segment)
			}
		}
		if c.Type() == ast.TypeBlock {
			lines := c.Lines()
			for i := range lines.Len() {
				note(lines.At(i))
			}
		}
		return ast.WalkContinue, nil
	})
	return span
}
