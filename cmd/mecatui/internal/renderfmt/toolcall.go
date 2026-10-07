package renderfmt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

const (
	toolLineComponentWidth = 120
	toolPathArg            = "path"
	toolURLArg             = "url"
	toolSourceArg          = "source"
)

type toolcallPresentation struct {
	intentKeys   []string
	argumentKeys []string
	stringIntent bool
	quoteFirst   bool
	intentJoin   string
}

var toolcallPresentations = map[string]toolcallPresentation{
	"Read":             {intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg, "offset", "limit"}},
	"ListDir":          {intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg, "depth"}},
	"Glob":             {intentKeys: []string{"pattern"}, argumentKeys: []string{"pattern", toolPathArg}},
	"Grep":             {intentKeys: []string{"pattern", toolPathArg}, argumentKeys: []string{"pattern", toolPathArg}, stringIntent: true, quoteFirst: true, intentJoin: " in "},
	"Edit":             {intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg, "old_string", "new_string"}},
	"Write":            {intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg, "content"}},
	"Copy":             {intentKeys: []string{toolSourceArg, "destination"}, argumentKeys: []string{toolSourceArg, "destination"}},
	"Move":             {intentKeys: []string{toolSourceArg, "destination"}, argumentKeys: []string{toolSourceArg, "destination"}},
	"Remove":           {intentKeys: []string{toolPathArg}, argumentKeys: []string{toolPathArg}},
	"Shell":            {intentKeys: []string{"command"}, argumentKeys: []string{"command"}},
	"WebFetch":         {intentKeys: []string{toolURLArg}, argumentKeys: []string{toolURLArg}},
	"FetchMcpResource": {intentKeys: []string{"uri"}, argumentKeys: []string{"uri"}},
	"Skill":            {intentKeys: []string{"name"}, stringIntent: true},
}

// ArgumentOrder returns a caller-owned ordered copy of the known argument names.
// Unknown tools have no preferred order.
func ArgumentOrder(name string) []string {
	return append([]string(nil), toolcallPresentations[name].argumentKeys...)
}

// ToolIntent derives a terminal-safe single-line description from a tool call's
// JSON arguments. Malformed arguments fall back to the tool name.
func ToolIntent(name, arguments string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &fields) != nil || fields == nil {
		return terminaltext.SanitizeSingleLine(name)
	}
	return toolIntent(name, fields)
}

func toolIntent(name string, fields map[string]json.RawMessage) string {
	if presentation, ok := toolcallPresentations[name]; ok {
		values := make([]string, 0, len(presentation.intentKeys))
		for _, key := range presentation.intentKeys {
			var value string
			if presentation.stringIntent {
				if json.Unmarshal(fields[key], &value) != nil {
					continue
				}
			} else {
				value = argumentSummary(fields[key])
			}
			if value = terminaltext.SanitizeSingleLine(value); strings.TrimSpace(value) != "" {
				if presentation.quoteFirst && key == presentation.intentKeys[0] {
					value = fmt.Sprintf("%q", value)
				}
				values = append(values, value)
			}
		}
		if len(values) > 0 {
			separator := " → "
			if presentation.intentJoin != "" {
				separator = presentation.intentJoin
			}
			return strings.Join(values, separator)
		}
		return terminaltext.SanitizeSingleLine(name)
	}
	for _, key := range []string{"target", toolPathArg, "uri", toolURLArg, "command", "query", "prompt", "task", "goal"} {
		if raw, ok := fields[key]; ok {
			if value := argumentSummary(raw); value != "" {
				return terminaltext.SanitizeSingleLine(value)
			}
		}
	}
	return terminaltext.SanitizeSingleLine(name)
}

func argumentSummary(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if strings.HasPrefix(value, "{") {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil {
			return fmt.Sprintf("%d fields", len(fields))
		}
	}
	if strings.HasPrefix(value, "[") {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			return fmt.Sprintf("%d items", len(items))
		}
	}
	return argumentValue(raw)
}

func argumentValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return "null"
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return terminaltext.Sanitize(value)
	}
	return terminaltext.Sanitize(string(raw))
}

// ToolState is the observed lifecycle phase of a tool call.
type ToolState uint8

// ToolState values distinguish top-level lifecycle phases from a delegated
// call whose result has not yet been observed. Only an observed result is final.
const (
	ToolRunning ToolState = iota
	ToolAwaitingResult
	ToolFinalizing
	ToolFinalizingFailed
	ToolDelegatedPending
	ToolSucceeded
	ToolFailed
)

type toolStatus struct {
	glyph, label, style string
}

func (state ToolState) status() toolStatus {
	switch state {
	case ToolAwaitingResult:
		return toolStatus{"…", "awaiting result", "toolName"}
	case ToolFinalizing:
		return toolStatus{"✓", "result received · finalizing", "toolOk"}
	case ToolFinalizingFailed:
		return toolStatus{"✗", "failed · finalizing", "toolErr"}
	case ToolDelegatedPending:
		return toolStatus{"…", "pending", "toolName"}
	case ToolSucceeded:
		return toolStatus{"✓", "done", "toolOk"}
	case ToolFailed:
		return toolStatus{"✗", "failed", "toolErr"}
	default:
		return toolStatus{"…", "running", "toolName"}
	}
}

// ToolLine is a terminal-safe single-line tool-call presentation.
type ToolLine struct {
	glyph, name, intent, status string
	statusStyle                 string
}

// PresentToolLine derives the complete single-line status vocabulary from the
// observed lifecycle state. Surface renderers own theme lookup and layout.
func PresentToolLine(displayName, intent string, state ToolState) ToolLine {
	status := state.status()
	label := status.label
	if state == ToolSucceeded {
		label = ""
	}
	return ToolLine{
		glyph: boundedSingleLine(status.glyph), name: boundedSingleLine(displayName),
		intent: boundedSingleLine(intent), status: boundedSingleLine(label),
		statusStyle: status.style,
	}
}

// Glyph returns the status symbol.
func (line ToolLine) Glyph() string { return line.glyph }

// Name returns the bounded display name.
func (line ToolLine) Name() string { return line.name }

// Intent returns the bounded call-side intent.
func (line ToolLine) Intent() string { return line.intent }

// Status returns a textual lifecycle cue, empty for a successful settled call.
func (line ToolLine) Status() string { return line.status }

// StatusStyle returns the semantic theme slot for the status glyph and label.
func (line ToolLine) StatusStyle() string { return line.statusStyle }

// Content returns the text after the optional glyph, for surfaces that style
// the glyph separately.
func (line ToolLine) Content() string {
	parts := make([]string, 0, 3)
	if line.name != "" {
		parts = append(parts, line.name)
	}
	if line.intent != "" && line.intent != line.name {
		parts = append(parts, line.intent)
	}
	if line.status != "" {
		parts = append(parts, line.status)
	}
	return strings.Join(parts, " · ")
}

// Text returns the plain tool-call summary with its optional glyph.
func (line ToolLine) Text() string {
	content := line.Content()
	if line.glyph == "" {
		return content
	}
	if content == "" {
		return line.glyph
	}
	return line.glyph + " " + content
}

func boundedSingleLine(value string) string {
	return ansi.Truncate(terminaltext.SanitizeSingleLine(value), toolLineComponentWidth, "…")
}
