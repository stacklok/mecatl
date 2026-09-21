package statusline

import (
	"html"
	"strings"
	"text/template"
)

// TitleRenderer renders a plain-text title from the display-safe status input.
// It deliberately has no StatusML parsing or command-source integration.
type TitleRenderer struct{ template *template.Template }

// NewTitleRenderer validates and compiles a title template. The startup render
// catches template expressions which parse successfully but cannot execute
// against the status projection.
func NewTitleRenderer(source string) (*TitleRenderer, error) {
	t, err := template.New("terminal_title").Funcs(commonTemplateFuncs()).Option("missingkey=error").Parse(source)
	if err != nil {
		return nil, err
	}
	var output strings.Builder
	if err := t.Execute(&output, newTemplateInput(Input{})); err != nil {
		return nil, err
	}
	return &TitleRenderer{template: t}, nil
}

// Render renders the configured template as plain text. The terminal-title
// controller owns final terminal-control sanitization and bounds.
func (r *TitleRenderer) Render(input Input) (string, error) {
	var output strings.Builder
	if err := r.template.Execute(&output, newTemplateInput(input)); err != nil {
		return "", err
	}
	return html.UnescapeString(output.String()), nil
}
