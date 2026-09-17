package statusline

import (
	"html"
	"strings"
	"text/template"

	"github.com/charmbracelet/x/ansi"
)

// TitleRenderer renders a plain-text title from the display-safe status input.
// It deliberately has no StatusML parsing or command-source integration.
type TitleRenderer struct{ template *template.Template }

type titleTemplateInput struct {
	templateInput
	Workspace titleTemplateWorkspace
}

type titleTemplateWorkspace struct{ Location, Name templateText }

func newTitleTemplateInput(input Input) titleTemplateInput {
	return titleTemplateInput{
		templateInput: newTemplateInput(input),
		Workspace:     titleTemplateWorkspace{escapeTemplateText(input.Workspace.Location), escapeTemplateText(input.Workspace.Name)},
	}
}

// NewTitleRenderer validates and compiles a title template. The startup render
// catches template expressions which parse successfully but cannot execute
// against the status projection.
func NewTitleRenderer(source string) (*TitleRenderer, error) {
	t, err := template.New("terminal_title").Funcs(templateFuncs()).Option("missingkey=error").Parse(source)
	if err != nil {
		return nil, err
	}
	var output strings.Builder
	if err := t.Execute(&output, newTitleTemplateInput(Input{})); err != nil {
		return nil, err
	}
	return &TitleRenderer{template: t}, nil
}

// Render renders the configured template as plain text. The terminal-title
// controller owns final terminal-control sanitization and bounds.
func (r *TitleRenderer) Render(input Input) (string, error) {
	var output strings.Builder
	if err := r.template.Execute(&output, newTitleTemplateInput(input)); err != nil {
		return "", err
	}
	return html.UnescapeString(output.String()), nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"contextMeter":        contextMeter,
		"contextMeterCompact": contextMeterCompact,
		"contextMeterMinimal": contextMeterMinimal,
		"elide":               elide,
	}
}

func elide(width int, value any) string {
	if width <= 0 {
		return ""
	}
	text := stringifyTemplateValue(value)
	if ansi.StringWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	return ansi.Truncate(text, width, "…")
}

func stringifyTemplateValue(value any) string {
	switch value := value.(type) {
	case templateText:
		return string(value)
	case string:
		return value
	default:
		return ""
	}
}
