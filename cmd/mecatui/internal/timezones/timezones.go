// Package timezones provides the client's pinned selectable timezone names and
// links Go's standard embedded rule fallback. Catalog names do not pin runtime rules.
package timezones

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"time"
	_ "time/tzdata" // Standard fallback when the host has no zoneinfo.
)

//go:embed zones.json
var catalogJSON []byte

var names = func() []string {
	var catalog struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		panic("invalid bundled timezone catalog; run task generate:timezones with the root go.mod toolchain: " + err.Error())
	}
	return catalog.Names
}()

// Names returns an independent sorted list: UTC and loadable slash-qualified archive IDs.
func Names() []string { return slices.Clone(names) }

// Valid reports exact catalog membership; abbreviations, Local and misspellings are rejected.
func Valid(name string) bool {
	_, ok := slices.BinarySearch(names, name)
	return ok
}

// Location validates the exact identifier and loads its runtime rules without a silent fallback.
func Location(name string) (*time.Location, error) {
	if !Valid(name) {
		return nil, fmt.Errorf("choose UTC or a listed IANA timezone (for example America/Los_Angeles)")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("cannot load timezone %q: %w", name, err)
	}
	return loc, nil
}
