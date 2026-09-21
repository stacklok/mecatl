package statusline

import (
	"fmt"
	"html"
	"text/template"

	"github.com/charmbracelet/x/ansi"
)

func commonTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"elide":  elide,
		"lookup": lookup,
	}
}

func elide(width int, value any) string {
	if width <= 0 {
		return ""
	}
	text := visibleTemplateText(value)
	if ansi.StringWidth(text) <= width {
		return html.EscapeString(text)
	}
	if width == 1 {
		return "…"
	}
	return html.EscapeString(ansi.Truncate(text, width, "…"))
}

// lookup selects the first value whose key exactly matches key's visible text.
// Its output is safe to embed in StatusML templates.
func lookup(key any, pairs ...any) (string, error) {
	if len(pairs)%2 != 0 {
		return "", fmt.Errorf("lookup: expected key/value pairs")
	}
	keyText, ok := templateString(key)
	if !ok {
		return "", fmt.Errorf("lookup: key must be a string")
	}
	keyText = html.UnescapeString(keyText)
	for i := 0; i < len(pairs); i += 2 {
		candidate, ok := templateString(pairs[i])
		if !ok {
			return "", fmt.Errorf("lookup: pair key %d must be a string", i/2+1)
		}
		value, ok := templateString(pairs[i+1])
		if !ok {
			return "", fmt.Errorf("lookup: pair value %d must be a string", i/2+1)
		}
		if keyText == html.UnescapeString(candidate) {
			return html.EscapeString(sanitizeTerminal(html.UnescapeString(value))), nil
		}
	}
	return "", nil
}

func visibleTemplateText(value any) string {
	return html.UnescapeString(stringifyTemplateValue(value))
}

func stringifyTemplateValue(value any) string {
	text, _ := templateString(value)
	return text
}

func templateString(value any) (string, bool) {
	switch value := value.(type) {
	case templateText:
		return string(value), true
	case string:
		return value, true
	default:
		return "", false
	}
}
