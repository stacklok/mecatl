// Package blocks prepares stateless, structured conversation-block snapshots.
//
// It deliberately has no knowledge of the conversation, renderer cache, viewport, or
// Bubble Tea model. The parent ui package attaches document-local identity and frame
// ownership when it adapts a Prepared value into its rendered frame.
package blocks

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Theme contains the resolved semantic styles required to prepare conversation blocks.
// The UI maps its stylesheet roles to this value at the package boundary.
type Theme struct {
	UserLabel    lipgloss.Style
	UserBody     lipgloss.Style
	Muted        lipgloss.Style
	Warning      lipgloss.Style
	Error        lipgloss.Style
	HookModified lipgloss.Style
	HookAdvisory lipgloss.Style
	DeliveryHead lipgloss.Style
	ToolCard     lipgloss.Style
}

// Region identifies a block-local semantic area. It has no document identity.
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

// Prepared is the complete output of one stateless preparation.
type Prepared struct {
	Lines []string
	Rows  []Row
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
