// Package rules is the in-repo alias shim for the project/user rule discovery
// adapter that lives in the importable engine module
// (engine/adapter/rulesfs, issue #329). The discovery, parsing, and resolution
// bodies live once, in rulesfs; this package re-exports them via thin
// type/func/const aliases so the composition layer (internal/app) and the cmd
// mains reference a stable import path. There is no second copy to drift. See
// the rulesfs package for the real contract.
package rules

import (
	"context"

	"github.com/stacklok/mecatl/engine/adapter/rulesfs"
	"github.com/stacklok/mecatl/engine/prompt"
)

// Rule is the pure value object for one rule (the engine/prompt.Rule value
// object, re-aliased through rulesfs). See prompt.Rule.
type Rule = prompt.Rule

// Discovered is one discovered rule together with its adapter-private locator
// string. See rulesfs.Discovered.
type Discovered = rulesfs.Discovered

// RuleSource is the pluggable EXTENSIBILITY POINT for where rules come from.
// See rulesfs.RuleSource.
type RuleSource = rulesfs.RuleSource

// SkipError records one diagnostic from discovery. See rulesfs.SkipError.
type SkipError = rulesfs.SkipError

// MultiSource composes an ORDERED list of Sources into one. See
// rulesfs.MultiSource.
type MultiSource = rulesfs.MultiSource

// DirSource is the local-OS-filesystem implementation of RuleSource. See
// rulesfs.DirSource.
type DirSource = rulesfs.DirSource

// FSSource is the FILESYSTEM implementation of the prompt.RulesSource port.
// See rulesfs.FSSource.
type FSSource = rulesfs.FSSource

// ResolveOptions configures the known-path resolver. See
// rulesfs.ResolveOptions.
type ResolveOptions = rulesfs.ResolveOptions

// RuleFileExt is the conventional extension of a rule file. See
// rulesfs.RuleFileExt.
const RuleFileExt = rulesfs.RuleFileExt

// ProjectDirMecatl is the project-level rules dir under the workspace. See
// rulesfs.ProjectDirMecatl.
const ProjectDirMecatl = rulesfs.ProjectDirMecatl

// ProjectDirClaude is the Claude-Code-compatible project-level rules dir. See
// rulesfs.ProjectDirClaude.
const ProjectDirClaude = rulesfs.ProjectDirClaude

// ResolveSources builds the ORDERED, highest-precedence-first Source list from
// the conventional locations. See rulesfs.ResolveSources.
func ResolveSources(opts ResolveOptions) []RuleSource { return rulesfs.ResolveSources(opts) }

// NewFSSource resolves the given sources ONCE and returns the snapshot source
// plus the aggregated discovery diagnostics. See rulesfs.NewFSSource.
func NewFSSource(ctx context.Context, sources ...RuleSource) (*FSSource, []SkipError, error) {
	return rulesfs.NewFSSource(ctx, sources...)
}

// NewMultiSource builds a MultiSource over the given ordered sources. See
// rulesfs.NewMultiSource.
func NewMultiSource(sources ...RuleSource) MultiSource { return rulesfs.NewMultiSource(sources...) }

// Discover scans dir for rules. See rulesfs.Discover.
func Discover(dir string) ([]Discovered, []SkipError, error) { return rulesfs.Discover(dir) }
