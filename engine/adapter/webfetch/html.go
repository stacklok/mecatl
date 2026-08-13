package webfetch

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

var skippedHTMLNodes = map[string]bool{
	"script": true, "style": true, "template": true, "noscript": true,
	"svg": true, "form": true, "iframe": true, "object": true, "embed": true,
}

// htmlToText extracts readable text from HTML without loading any external resources.
func htmlToText(data []byte) (text, title string, err error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", "", fmt.Errorf("parse HTML: %w", err)
	}

	title = normalizeInline(textContent(firstElement(doc, "title")))
	root := firstElement(doc, "main")
	if root == nil {
		root = firstElement(doc, "article")
	}
	if root == nil {
		root = firstElement(doc, "body")
	}
	if root == nil {
		root = doc
	}

	var out textWriter
	out.visit(root, false)
	return strings.TrimSpace(trimBlankLines(out.String())), title, nil
}

func firstElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := firstElement(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return b.String()
}

type textWriter struct{ b strings.Builder }

func (w *textWriter) visit(n *html.Node, pre bool) {
	if n.Type == html.TextNode {
		if pre {
			w.b.WriteString(n.Data)
		} else {
			w.inline(n.Data)
		}
		return
	}
	if n.Type != html.ElementNode && n.Type != html.DocumentNode {
		return
	}
	if skippedHTMLNodes[n.Data] {
		return
	}

	tag := n.Data
	isPre := pre || tag == "pre"
	if tag == "br" {
		w.line()
		return
	}
	if tag == "li" {
		w.line()
		w.b.WriteString("- ")
	}
	if isBlock(tag) && tag != "li" {
		w.line()
	}
	if tag == "tr" {
		w.line()
	}
	if isTableCell(tag) {
		if w.b.Len() > 0 && !strings.HasSuffix(w.b.String(), "\n") && !strings.HasSuffix(w.b.String(), "\t") {
			w.b.WriteString("\t")
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		w.visit(child, isPre)
	}
	if endsWithLine(tag) {
		w.line()
	}
}

func (w *textWriter) inline(s string) {
	for _, r := range s {
		if unicode.IsSpace(r) {
			if w.b.Len() > 0 && !strings.HasSuffix(w.b.String(), " ") && !strings.HasSuffix(w.b.String(), "\n") && !strings.HasSuffix(w.b.String(), "\t") {
				w.b.WriteByte(' ')
			}
			continue
		}
		w.b.WriteRune(r)
	}
}

func (w *textWriter) line() {
	if w.b.Len() == 0 || strings.HasSuffix(w.b.String(), "\n") {
		return
	}
	w.b.WriteByte('\n')
}

func (w *textWriter) String() string { return w.b.String() }

func isTableCell(tag string) bool { return tag == "td" || tag == "th" }

func endsWithLine(tag string) bool { return isBlock(tag) || tag == "tr" || tag == "li" }

func isBlock(tag string) bool {
	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6", "p", "div", "section", "article", "main", "pre", "code":
		return true
	default:
		return false
	}
}

func normalizeInline(s string) string { return strings.Join(strings.Fields(s), " ") }

func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if strings.TrimSpace(line) == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
