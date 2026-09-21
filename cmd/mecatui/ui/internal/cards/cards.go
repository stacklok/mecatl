// Package cards prepares stateless, structured conversation-card snapshots.
//
// It deliberately has no knowledge of the conversation, renderer cache, viewport, or
// Bubble Tea model. The parent ui package attaches document-local identity and frame
// ownership when it adapts a Prepared value into its rendered frame.
package cards

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Region identifies a card-local semantic area. It has no document identity.
type Region uint8

const (
	// RegionChrome identifies frame and header rows without visible semantic text.
	RegionChrome Region = iota
	// RegionArguments identifies visible tool-argument text.
	RegionArguments
	// RegionResult identifies visible tool-result text.
	RegionResult
)

// Frame is concrete card chrome, already resolved from the active theme.
type Frame struct {
	Top    string
	Bottom string
	Left   string
	Right  string
}

// Appearance is the resolved decoration required to make final card-local lines.
type Appearance struct {
	Frame         Frame
	LinePrefix    string
	LineSuffix    string
	LeadingColumn int
}

// LayoutInput is the caller-owned, mutable description of every layout fact this
// foundation observes. SnapshotLayout makes it immutable before preparation.
type LayoutInput struct {
	Width        int
	Expanded     bool
	VisibleHints []string
	Dialect      uint32
	Appearance   Appearance
}

// Layout is an immutable snapshot of LayoutInput.
type Layout struct {
	width        int
	expanded     bool
	visibleHints []string
	dialect      uint32
	appearance   Appearance
}

// SnapshotLayout owns all mutable LayoutInput fields.
func SnapshotLayout(input LayoutInput) Layout {
	return Layout{
		width:        input.Width,
		expanded:     input.Expanded,
		visibleHints: cloneStrings(input.VisibleHints),
		dialect:      input.Dialect,
		appearance: Appearance{
			Frame: Frame{
				Top:    strings.Clone(input.Appearance.Frame.Top),
				Bottom: strings.Clone(input.Appearance.Frame.Bottom),
				Left:   strings.Clone(input.Appearance.Frame.Left),
				Right:  strings.Clone(input.Appearance.Frame.Right),
			},
			LinePrefix:    strings.Clone(input.Appearance.LinePrefix),
			LineSuffix:    strings.Clone(input.Appearance.LineSuffix),
			LeadingColumn: input.Appearance.LeadingColumn,
		},
	}
}

// ToolInput is the tool-card family's caller-owned source data. It is not a
// generic conversation block: future card families receive their own input types.
type ToolInput struct {
	Title     string
	Arguments []string
	Results   []string
	Labels    map[string]string
}

// ToolSnapshot is an immutable tool-card input snapshot.
type ToolSnapshot struct {
	title     string
	arguments []string
	results   []string
	labels    map[string]string
}

// SnapshotTool owns all mutable ToolInput fields.
func SnapshotTool(input ToolInput) ToolSnapshot {
	labels := make(map[string]string, len(input.Labels))
	for key, value := range input.Labels {
		labels[strings.Clone(key)] = strings.Clone(value)
	}
	return ToolSnapshot{
		title:     strings.Clone(input.Title),
		arguments: cloneStrings(input.Arguments),
		results:   cloneStrings(input.Results),
		labels:    labels,
	}
}

// Row is the structural provenance for one final card-local line. It carries no
// semantic source text; the frame remains the owner of rendered strings.
type Row struct {
	Region        Region
	Text          bool
	SourceOffset  int
	FallbackRow   int
	LeadingColumn int
	GraphemeSpan  int
}

// Prepared is the complete output of one stateless preparation. Key is a local
// cache discriminator, not a persistent or adversarial identity.
type Prepared struct {
	Key   [sha256.Size]byte
	Lines []string
	Rows  []Row
}

// PrepareTool compiles a tool snapshot and layout into decorated local lines and
// lockstep structural provenance.
func PrepareTool(input ToolSnapshot, layout Layout) Prepared {
	key := toolKey(input, layout)
	lines := make([]string, 0, 2+len(input.arguments)+len(input.results)+1)
	rows := make([]Row, 0, cap(lines))
	appendLine := func(line string, row Row) {
		lines = append(lines, line)
		row.FallbackRow = len(rows)
		rows = append(rows, row)
	}

	appendLine(layout.appearance.Frame.Top, Row{Region: RegionChrome})
	header := input.title
	if labels := renderLabels(input.labels); labels != "" {
		header += " " + labels
	}
	if hints := strings.Join(layout.visibleHints, " "); hints != "" {
		header += " " + hints
	}
	appendLine(decorate(header, layout.appearance), Row{Region: RegionChrome})
	if layout.expanded {
		bodyWidth := layout.width - ansi.StringWidth(layout.appearance.Frame.Left+layout.appearance.LinePrefix+layout.appearance.LineSuffix+layout.appearance.Frame.Right)
		appendSemanticLines(input.arguments, RegionArguments, layout.appearance, bodyWidth, &lines, &rows)
		appendSemanticLines(input.results, RegionResult, layout.appearance, bodyWidth, &lines, &rows)
	}
	appendLine(layout.appearance.Frame.Bottom, Row{Region: RegionChrome})
	return Prepared{Key: key, Lines: lines, Rows: rows}
}

func appendSemanticLines(source []string, region Region, appearance Appearance, width int, lines *[]string, rows *[]Row) {
	offset := 0
	for _, text := range source {
		for _, line := range strings.Split(text, "\n") {
			wrapped := line
			if width > 0 {
				wrapped = ansi.Wrap(line, width, "")
			}
			for _, rowText := range strings.Split(wrapped, "\n") {
				*lines = append(*lines, decorate(rowText, appearance))
				span := graphemeCount(rowText)
				*rows = append(*rows, Row{
					Region:        region,
					Text:          true,
					SourceOffset:  offset,
					FallbackRow:   len(*rows),
					LeadingColumn: appearance.LeadingColumn,
					GraphemeSpan:  span,
				})
				offset += span
			}
		}
	}
}

func decorate(line string, appearance Appearance) string {
	return appearance.Frame.Left + appearance.LinePrefix + line + appearance.LineSuffix + appearance.Frame.Right
}

func renderLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, " ")
}

func toolKey(input ToolSnapshot, layout Layout) [sha256.Size]byte {
	var encoded canonicalEncoder
	encoded.string("mecatui.cards.tool/v1")
	encoded.string(input.title)
	encoded.strings(input.arguments)
	encoded.strings(input.results)
	encoded.mapStrings(input.labels)
	encoded.int(layout.width)
	encoded.bool(layout.expanded)
	encoded.strings(layout.visibleHints)
	encoded.uint32(layout.dialect)
	encoded.string(layout.appearance.Frame.Top)
	encoded.string(layout.appearance.Frame.Bottom)
	encoded.string(layout.appearance.Frame.Left)
	encoded.string(layout.appearance.Frame.Right)
	encoded.string(layout.appearance.LinePrefix)
	encoded.string(layout.appearance.LineSuffix)
	encoded.int(layout.appearance.LeadingColumn)
	return sha256.Sum256(encoded.bytes)
}

type canonicalEncoder struct {
	bytes []byte
}

func (e *canonicalEncoder) uint32(value uint32) {
	var data [4]byte
	binary.BigEndian.PutUint32(data[:], value)
	e.bytes = append(e.bytes, data[:]...)
}

func (e *canonicalEncoder) int(value int) {
	e.string(strconv.Itoa(value))
}

func (e *canonicalEncoder) length(value int) {
	e.bytes = strconv.AppendInt(e.bytes, int64(value), 10)
	e.bytes = append(e.bytes, ':')
}

func (e *canonicalEncoder) bool(value bool) {
	if value {
		e.bytes = append(e.bytes, 1)
		return
	}
	e.bytes = append(e.bytes, 0)
}

func (e *canonicalEncoder) string(value string) {
	e.length(len(value))
	e.bytes = append(e.bytes, value...)
}

func (e *canonicalEncoder) strings(values []string) {
	e.length(len(values))
	for _, value := range values {
		e.string(value)
	}
}

func (e *canonicalEncoder) mapStrings(values map[string]string) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	e.length(len(keys))
	for _, key := range keys {
		e.string(key)
		e.string(values[key])
	}
}

func cloneStrings(values []string) []string {
	cloned := make([]string, len(values))
	for i, value := range values {
		cloned[i] = strings.Clone(value)
	}
	return cloned
}

func graphemeCount(value string) int {
	_, count := ansi.ByteToGraphemeRange(value, 0, len(value))
	return count
}
