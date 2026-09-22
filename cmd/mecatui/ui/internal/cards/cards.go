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

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Region identifies a card-local semantic area. It has no document identity.
type Region uint8

const (
	// RegionChrome identifies frame and header rows without visible semantic text.
	RegionChrome Region = iota
	// RegionBody identifies visible non-tool card text.
	RegionBody
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

// StyledSectionInput is one already width-bounded semantic section of a real
// tool card. Text may contain ANSI styling, but all dynamic text has been
// wrapped before that styling was applied.
type StyledSectionInput struct {
	Region   Region
	Text     string
	Trailing int
}

// StyledToolInput is the caller-owned input for the main-conversation tool card.
type StyledToolInput struct {
	Sections []StyledSectionInput
}

// StyledToolSnapshot owns the strings used by a tool-card preparation.
type StyledToolSnapshot struct {
	sections []StyledSectionInput
}

// SnapshotStyledTool detaches preparation from the mutable conversation block.
func SnapshotStyledTool(input StyledToolInput) StyledToolSnapshot {
	sections := make([]StyledSectionInput, len(input.Sections))
	for i, section := range input.Sections {
		sections[i] = section
		sections[i].Text = strings.Clone(section.Text)
	}
	return StyledToolSnapshot{sections: sections}
}

// StyledLayoutInput contains the concrete card style and every non-content fact
// observed by the real tool-card compiler.
type StyledLayoutInput struct {
	Card         lipgloss.Style
	Width        int
	Expanded     bool
	VisibleHints []string
	Dialect      uint32
}

// StyledLayout is an immutable copy of StyledLayoutInput.
type StyledLayout struct {
	card         lipgloss.Style
	width        int
	expanded     bool
	visibleHints []string
	dialect      uint32
}

// SnapshotStyledLayout owns mutable layout collections. lipgloss.Style is a
// value whose mutators return a new value, so copying it snapshots appearance.
func SnapshotStyledLayout(input StyledLayoutInput) StyledLayout {
	return StyledLayout{
		card:         input.Card,
		width:        input.Width,
		expanded:     input.Expanded,
		visibleHints: cloneStrings(input.VisibleHints),
		dialect:      input.Dialect,
	}
}

// PrepareStyledTool produces the actual main-conversation card lines and their
// structural provenance in one stateless operation.
func PrepareStyledTool(input StyledToolSnapshot, layout StyledLayout) Prepared {
	parts := make([]string, 0, len(input.sections))
	for _, section := range input.sections {
		if section.Text != "" {
			parts = append(parts, section.Text)
		}
	}
	lines := strings.Split(layout.card.Render(strings.Join(parts, "\n")), "\n")
	if layout.card.GetWidth() > 0 && layout.card.GetHorizontalFrameSize() == 0 {
		for i, line := range lines {
			if ansi.StringWidth(line) > layout.card.GetWidth() {
				lines[i] = ansi.Truncate(line, layout.card.GetWidth(), "")
			}
		}
	}
	rows := styledRows(input.sections, layout.card)
	if len(rows) != len(lines) {
		// The semantic sections are width-bounded before decoration, so this is a
		// fail-safe for an unexpected lipgloss layout rule rather than a reflow path.
		rows = fallbackStyledRows(input.sections, lines, layout.card)
	}
	return Prepared{Key: styledToolKey(input, layout), Lines: lines, Rows: rows}
}

func styledRows(sections []StyledSectionInput, card lipgloss.Style) []Row {
	top := card.GetBorderTopSize() + card.GetPaddingTop()
	bottom := card.GetBorderBottomSize() + card.GetPaddingBottom()
	rows := make([]Row, 0, top+bottom+len(sections))
	for range top {
		rows = append(rows, Row{Region: RegionChrome, FallbackRow: len(rows)})
	}
	bodyWidth := card.GetWidth() - card.GetHorizontalFrameSize()
	leading := card.GetBorderLeftSize() + card.GetPaddingLeft()
	offsets := map[Region]int{}
	for _, section := range sections {
		if section.Text == "" {
			continue
		}
		layoutRows := styledLayoutRows(section.Text, bodyWidth)
		semanticRows := styledSemanticRows(section, bodyWidth)
		for i, text := range layoutRows {
			semantic := strings.TrimRight(text, " ")
			if i < len(semanticRows) && strings.TrimRight(semanticRows[i], " ") == semantic {
				semantic = semanticRows[i]
			}
			row := Row{Region: section.Region, FallbackRow: len(rows)}
			if section.Region != RegionChrome {
				row.Text = true
				row.SourceOffset = offsets[section.Region]
				row.LeadingColumn = leading
				row.GraphemeSpan = graphemeCount(semantic)
				offsets[section.Region] += row.GraphemeSpan
			}
			rows = append(rows, row)
		}
	}
	for range bottom {
		rows = append(rows, Row{Region: RegionChrome, FallbackRow: len(rows)})
	}
	return rows
}

func styledLayoutRows(text string, bodyWidth int) []string {
	layout := lipgloss.NewStyle()
	if bodyWidth > 0 {
		layout = layout.Width(bodyWidth)
	}
	rows := strings.Split(layout.Render(text), "\n")
	for i := range rows {
		rows[i] = ansi.Strip(rows[i])
	}
	return rows
}

func styledSemanticRows(section StyledSectionInput, bodyWidth int) []string {
	text := ansi.Strip(section.Text)
	wrapped := text
	if bodyWidth > 0 {
		wrapped = ansi.Hardwrap(text, bodyWidth, true)
	}
	rows := strings.Split(wrapped, "\n")
	if sourceLines := strings.Split(text, "\n"); len(sourceLines) > 0 {
		last := sourceLines[len(sourceLines)-1]
		trailing := last[len(strings.TrimRight(last, " ")):]
		if section.Trailing > len(trailing) {
			trailing = strings.Repeat(" ", section.Trailing)
		}
		if trailing != "" && len(rows) > 0 && !strings.HasSuffix(rows[len(rows)-1], trailing) {
			rows[len(rows)-1] += trailing
		}
	}
	return rows
}

func fallbackStyledRows(sections []StyledSectionInput, lines []string, card lipgloss.Style) []Row {
	rows := make([]Row, len(lines))
	for i := range rows {
		rows[i] = Row{Region: RegionChrome, FallbackRow: i}
	}
	// A mismatch cannot safely invent semantic offsets. The ordinary bounded path
	// above is the contract; chrome fallback keeps line/provenance lockstep.
	_ = sections
	_ = card
	return rows
}

func styledToolKey(input StyledToolSnapshot, layout StyledLayout) [sha256.Size]byte {
	var encoded canonicalEncoder
	encoded.string("mecatui.cards.styled-tool/v1")
	encoded.length(len(input.sections))
	for _, section := range input.sections {
		encoded.uint32(uint32(section.Region))
		encoded.string(section.Text)
		encoded.int(section.Trailing)
	}
	encoded.int(layout.width)
	encoded.bool(layout.expanded)
	encoded.strings(layout.visibleHints)
	encoded.uint32(layout.dialect)
	encoded.int(layout.card.GetWidth())
	encoded.int(layout.card.GetHorizontalFrameSize())
	encoded.string(layout.card.Render("mecatui-card-appearance"))
	return sha256.Sum256(encoded.bytes)
}
