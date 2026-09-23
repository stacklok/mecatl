package customization

import (
	"context"
	"html"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

// SurfaceTemplates contains the three independently selected variants for one
// surface. Empty variants fall back to the corresponding shipped variant.
type SurfaceTemplates struct{ Full, Compact, Minimal string }

// TemplateSet provides independent header and footer variant sets.
type TemplateSet struct{ Header, Footer SurfaceTemplates }

// NewTemplateSource creates a template source whose missing surfaces and
// variants use the shipped defaults.
func NewTemplateSource(templates TemplateSet, interval time.Duration) Source {
	return newTemplateSource(templates, interval)
}

func newTemplateSource(templates TemplateSet, interval time.Duration) *statusLineSource {
	var ticks <-chan time.Time
	var ticker *time.Ticker
	if interval >= time.Second {
		ticker = time.NewTicker(interval)
		ticks = ticker.C
	}
	defaults := defaultTemplateSet()
	header := compileVariants(templates.Header, defaults.Header)
	footer := compileVariants(templates.Footer, defaults.Footer)
	s := newSource(ticks, func(ctx context.Context, input Input) Result {
		return renderTemplates(ctx, header, footer, input)
	})
	if ticker != nil {
		s.stopTicker = ticker.Stop
	}
	return s
}

type templateVariants struct{ full, compact, minimal statusTemplate }
type statusTemplate struct {
	template *template.Template
	fallback *template.Template
}

func compileVariants(given, defaults SurfaceTemplates) templateVariants {
	return templateVariants{
		full:    parseStatusTemplate("full", firstTemplate(given.Full, defaults.Full), defaults.Full),
		compact: parseStatusTemplate("compact", firstTemplate(given.Compact, defaults.Compact), defaults.Compact),
		minimal: parseStatusTemplate("minimal", firstTemplate(given.Minimal, defaults.Minimal), defaults.Minimal),
	}
}
func firstTemplate(given, fallback string) string {
	if given != "" {
		return given
	}
	return fallback
}
func parseStatusTemplate(name, source, fallback string) statusTemplate {
	funcs := statusTemplateFuncs()
	fallbackTemplate, err := template.New(name).Funcs(funcs).Option("missingkey=error").Parse(fallback)
	if err != nil {
		return statusTemplate{}
	}
	parsed, err := template.New(name).Funcs(funcs).Option("missingkey=error").Parse(source)
	if err != nil {
		return statusTemplate{fallback: fallbackTemplate}
	}
	return statusTemplate{template: parsed, fallback: fallbackTemplate}
}

func statusTemplateFuncs() template.FuncMap {
	funcs := commonTemplateFuncs()
	funcs["contextMeter"] = contextMeter
	funcs["contextMeterCompact"] = contextMeterCompact
	funcs["contextMeterMinimal"] = contextMeterMinimal
	return funcs
}

func (t statusTemplate) render(ctx context.Context, input templateInput) Document {
	if ctx.Err() != nil || t.fallback == nil {
		return Document{}
	}
	parsed := t.template
	if parsed == nil {
		parsed = t.fallback
	}
	var output strings.Builder
	if err := parsed.Execute(&output, input); err != nil || ctx.Err() != nil {
		output.Reset()
		if err := t.fallback.Execute(&output, input); err != nil || ctx.Err() != nil {
			return Document{}
		}
	}
	return Render(output.String(), nil)
}
func renderTemplates(ctx context.Context, header, footer templateVariants, input Input) Result {
	projection := newTemplateInput(input)
	headerFull, headerCompact, headerMinimal := header.full.render(ctx, projection), header.compact.render(ctx, projection), header.minimal.render(ctx, projection)
	footerFull, footerCompact, footerMinimal := footer.full.render(ctx, projection), footer.compact.render(ctx, projection), footer.minimal.render(ctx, projection)
	return Result{
		Header: selectTemplateSurface(input.Terminal.HeaderAvailCols, headerFull.Header, headerCompact.Header, headerMinimal.Header),
		Footer: selectTemplateSurface(input.Terminal.FooterAvailCols, footerFull.Footer, footerCompact.Footer, footerMinimal.Footer),
	}
}
func selectTemplateSurface(width int, variants ...Surface) Surface {
	for _, surface := range variants {
		if surface.Present && ansi.StringWidth(statusSurfaceText(surface)) <= width {
			return surface
		}
	}
	return Surface{}
}
func statusSurfaceText(surface Surface) string {
	var b strings.Builder
	for _, span := range surface.Spans {
		b.WriteString(span.Text)
	}
	return b.String()
}

type templateText string

type templateInput struct {
	Version    uint8
	Server     templateServer
	Session    templateSession
	Model      templateModel
	Usage      templateUsage
	Context    templateContext
	Workspace  templateWorkspace
	Terminal   Terminal
	MainAgent  templateMainAgent
	Delegation templateDelegation
	Clock      templateClock
}
type templateDelegation struct {
	Subagents, Parallel templateDelegationSummary
	Team                templateLiveTeam
}
type templateDelegationSummary struct{ Running, Finished int }
type templateLiveTeam struct {
	ID             templateText
	Working, Total int
}
type templateServer struct{ DisplayTarget, ConnectionMode templateText }
type templateSession struct{ Title, Handle, Mode, ReasoningEffort templateText }
type templateModel struct {
	ProviderID, ID, DisplayName, Route templateText
	ContextWindow                      templateContextAtom
}
type templateUsageAtom struct {
	Raw   int64
	Human templateText
}
type templateUsage struct {
	Input, Output, CacheRead, CacheWrite templateUsageAtom
	CacheReadPercent                     int
}
type templateContextAtom struct {
	Raw   int64
	Human templateText
}
type templateContext struct {
	Used, Window templateContextAtom
	Percent      int
}

// contextMeter is the shipped template primitive for the context-pressure bar.
// It emits only trusted StatusML markup; user-derived values remain escaped in
// templateContext. The UI resolves its semantic token through the active theme.
func contextMeter(input templateContext) templateText {
	if input.Window.Raw <= 0 {
		return templateText("<text>ctx " + string(input.Used.Human) + "</text>")
	}
	percent := input.Percent
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	var token, fill string
	switch {
	case percent >= 85:
		token, fill = "warning", "█"
	case percent >= 60:
		token, fill = "warning", "▓"
	default:
		token, fill = "success", "▒"
	}
	filled := (percent*8 + 50) / 100
	if filled > 8 {
		filled = 8
	}
	bar := strings.Repeat(fill, filled) + strings.Repeat("░", 8-filled)
	label := "ctx " + bar + " " + strconv.Itoa(percent) + "%"
	if percent >= 85 {
		label += " ⚠"
	}
	return templateText("<" + token + ">" + label + "</" + token + "><text> · " + string(input.Used.Human) + "/" + string(input.Window.Human) + "</text>")
}

func contextMeterCompact(input templateContext) templateText {
	if input.Window.Raw <= 0 {
		return templateText("<text>ctx " + string(input.Used.Human) + "</text>")
	}
	percent := clampedContextPercent(input.Percent)
	token, fill := contextPressure(percent)
	filled := (percent*8 + 50) / 100
	bar := strings.Repeat(fill, filled) + strings.Repeat("░", 8-filled)
	label := "ctx " + bar + " " + strconv.Itoa(percent) + "%"
	if percent >= 85 {
		label += " ⚠"
	}
	return templateText("<" + token + ">" + label + "</" + token + ">")
}

func contextMeterMinimal(input templateContext) templateText {
	if input.Window.Raw <= 0 {
		return templateText("<text>ctx " + string(input.Used.Human) + "</text>")
	}
	percent := clampedContextPercent(input.Percent)
	token, _ := contextPressure(percent)
	label := "ctx " + strconv.Itoa(percent) + "%"
	if percent >= 85 {
		label += " ⚠"
	}
	return templateText("<" + token + ">" + label + "</" + token + ">")
}

func clampedContextPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func contextPressure(percent int) (token, fill string) {
	switch {
	case percent >= 85:
		return "warning", "█"
	case percent >= 60:
		return "warning", "▓"
	default:
		return "success", "▒"
	}
}

type templateWorkspace struct{ Location, Name, Path templateText }
type templateMainAgent struct{ State, Activity, Approval templateText }
type templateClock struct{ Now templateTime }
type templateTime struct{ now time.Time }

func (t templateTime) String() string { return string(escapeTemplateText(t.now.String())) }
func (t templateTime) Format(layout string) templateText {
	return escapeTemplateText(t.now.Format(layout))
}
func newTemplateInput(input Input) templateInput {
	return templateInput{
		Version:    input.Version,
		Server:     templateServer{escapeTemplateText(input.Server.DisplayTarget), escapeTemplateText(input.Server.ConnectionMode)},
		Session:    templateSession{escapeTemplateText(input.Session.Title), escapeTemplateText(input.Session.Handle), escapeTemplateText(input.Session.Mode), escapeTemplateText(input.Session.ReasoningEffort)},
		Model:      templateModel{escapeTemplateText(input.Model.ProviderID), escapeTemplateText(input.Model.ID), escapeTemplateText(input.Model.DisplayName), escapeTemplateText(input.Model.Route), templateContextAtom{input.Model.ContextWindow.Raw, escapeTemplateText(input.Model.ContextWindow.Human)}},
		Usage:      templateUsage{templateUsageAtom{input.Usage.Input.Raw, escapeTemplateText(input.Usage.Input.Human)}, templateUsageAtom{input.Usage.Output.Raw, escapeTemplateText(input.Usage.Output.Human)}, templateUsageAtom{input.Usage.CacheRead.Raw, escapeTemplateText(input.Usage.CacheRead.Human)}, templateUsageAtom{input.Usage.CacheWrite.Raw, escapeTemplateText(input.Usage.CacheWrite.Human)}, input.Usage.CacheReadPercent},
		Context:    templateContext{templateContextAtom{input.Context.Used.Raw, escapeTemplateText(input.Context.Used.Human)}, templateContextAtom{input.Context.Window.Raw, escapeTemplateText(input.Context.Window.Human)}, input.Context.Percent},
		Workspace:  templateWorkspace{escapeTemplateText(input.Workspace.Location), escapeTemplateText(input.Workspace.Name), escapeTemplateText(input.Workspace.Path)},
		Terminal:   input.Terminal,
		MainAgent:  templateMainAgent{escapeTemplateText(input.MainAgent.State), escapeTemplateText(input.MainAgent.Activity), escapeTemplateText(input.MainAgent.Approval)},
		Delegation: templateDelegation{Subagents: templateDelegationSummary{input.Delegation.Subagents.Running, input.Delegation.Subagents.Finished}, Parallel: templateDelegationSummary{input.Delegation.Parallel.Running, input.Delegation.Parallel.Finished}, Team: templateLiveTeam{ID: escapeTemplateText(input.Delegation.Team.ID), Working: input.Delegation.Team.Working, Total: input.Delegation.Team.Total}},
		Clock:      templateClock{templateTime{input.Clock.Now}},
	}
}
func escapeTemplateText(value string) templateText {
	return templateText(html.EscapeString(terminaltext.SanitizeSingleLine(value)))
}
