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
	Version    uint8
	Server     templateServer
	Session    templateSession
	Model      templateModel
	Usage      templateUsage
	Context    templateContext
	Workspace  titleTemplateWorkspace
	Terminal   Terminal
	MainAgent  templateMainAgent
	Delegation templateDelegation
	Clock      templateClock
}

type titleTemplateWorkspace struct{ Location, Name templateText }

func newTitleTemplateInput(input Input) titleTemplateInput {
	projection := newTemplateInput(input)
	return titleTemplateInput{
		Version:    projection.Version,
		Server:     projection.Server,
		Session:    projection.Session,
		Model:      projection.Model,
		Usage:      projection.Usage,
		Context:    projection.Context,
		Workspace:  titleTemplateWorkspace{projection.Workspace.Location, projection.Workspace.Name},
		Terminal:   projection.Terminal,
		MainAgent:  projection.MainAgent,
		Delegation: projection.Delegation,
		Clock:      projection.Clock,
	}
}

// NewTitleRenderer validates and compiles a title template. The startup render
// catches template expressions which parse successfully but cannot execute
// against the status projection.
func NewTitleRenderer(source string) (*TitleRenderer, error) {
	t, err := template.New("terminal_title").Funcs(titleTemplateFuncs()).Option("missingkey=error").Parse(source)
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

func titleTemplateFuncs() template.FuncMap {
	return template.FuncMap{"elide": elide}
}

func statusTemplateFuncs() template.FuncMap {
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
	text := html.UnescapeString(stringifyTemplateValue(value))
	if ansi.StringWidth(text) <= width {
		return html.EscapeString(text)
	}
	if width == 1 {
		return "…"
	}
	return html.EscapeString(ansi.Truncate(text, width, "…"))
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
