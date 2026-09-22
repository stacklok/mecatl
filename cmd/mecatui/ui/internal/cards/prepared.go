// Package cards prepares stateless, structured conversation-card snapshots.
//
// It deliberately has no knowledge of the conversation, renderer cache, viewport, or
// Bubble Tea model. The parent ui package attaches document-local identity and frame
// ownership when it adapts a Prepared value into its rendered frame.
package cards

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"strings"

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
