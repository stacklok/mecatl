// Package agents is the in-repo alias shim for the agent-definition discovery
// adapter that graduated into the importable engine module
// (engine/adapter/agentfs, issue #328). The discovery, parsing, registry, and
// resolution bodies live once, in agentfs; this package re-exports them via
// thin type/func/const aliases so every existing caller (internal/app,
// cmd/mecated, internal/adapter/server, internal/adapter/grpcdriver) keeps
// compiling unchanged. See alias.go and the agentfs package doc for the real
// contract.
package agents

import (
	"context"

	"github.com/stacklok/mecatl/engine/adapter/agentfs"
)

// alias.go re-exports the agent-definition discovery adapter that graduated
// into the importable engine module (engine/adapter/agentfs, issue #328) so
// every existing caller — internal/app, cmd/mecated, internal/adapter/server,
// internal/adapter/grpcdriver — keeps compiling against the agents package
// unchanged. The discovery, parsing, registry, and resolution bodies live
// once, in agentfs; these are thin type/func/const aliases, not
// re-implementations, so there is no second copy to drift.

// AgentDef is the pure value object for one agent definition. It is the
// engine/tool.AgentDef value object (agentfs aliases it); re-aliased here so
// every existing literal and signature keeps compiling. See agentfs.AgentDef.
type AgentDef = agentfs.AgentDef

// AgentMCPServer is one entry of a def's mcpServers (reference or inline
// streamable-HTTP server). See agentfs.AgentMCPServer.
type AgentMCPServer = agentfs.AgentMCPServer

// Discovered is one discovered agent definition together with its
// adapter-private locator string. See agentfs.Discovered.
type Discovered = agentfs.Discovered

// AgentSource is the pluggable EXTENSIBILITY POINT for where agent definitions
// come from. See agentfs.AgentSource.
type AgentSource = agentfs.AgentSource

// SkipError records one diagnostic from discovery. See agentfs.SkipError.
type SkipError = agentfs.SkipError

// MultiSource composes an ORDERED list of Sources into one. See
// agentfs.MultiSource.
type MultiSource = agentfs.MultiSource

// DirSource is the local-OS-filesystem implementation of AgentSource. See
// agentfs.DirSource.
type DirSource = agentfs.DirSource

// FSSource is the FILESYSTEM implementation of the tool.AgentDefSource port.
// See agentfs.FSSource.
type FSSource = agentfs.FSSource

// Registry is an immutable, name-indexed view of the discovered agent
// definitions. See agentfs.Registry.
type Registry = agentfs.Registry

// ResolveOptions configures the known-path resolver. See
// agentfs.ResolveOptions.
type ResolveOptions = agentfs.ResolveOptions

// AgentFileExt is the conventional extension of an agent-definition file. See
// agentfs.AgentFileExt.
const AgentFileExt = agentfs.AgentFileExt

// ProjectDirMecatl is the project-level agents dir under the workspace. See
// agentfs.ProjectDirMecatl.
const ProjectDirMecatl = agentfs.ProjectDirMecatl

// ProjectDirClaude is the Claude-Code-compatible project-level agents dir. See
// agentfs.ProjectDirClaude.
const ProjectDirClaude = agentfs.ProjectDirClaude

// NewRegistry builds a Registry from the given defs. See agentfs.NewRegistry.
func NewRegistry(defs []AgentDef) *Registry { return agentfs.NewRegistry(defs) }

// NewRegistryDiscovered builds a Registry from Discovered entries, retaining
// each def's adapter-private Detail. See agentfs.NewRegistryDiscovered.
func NewRegistryDiscovered(discovered []Discovered) *Registry {
	return agentfs.NewRegistryDiscovered(discovered)
}

// ResolveSources builds the ORDERED, highest-precedence-first Source list from
// the conventional locations plus any explicit paths. See agentfs.ResolveSources.
func ResolveSources(opts ResolveOptions) []AgentSource { return agentfs.ResolveSources(opts) }

// NewFSSource resolves the given sources ONCE and returns the snapshot source
// plus the aggregated discovery diagnostics. See agentfs.NewFSSource.
func NewFSSource(ctx context.Context, sources ...AgentSource) (*FSSource, []SkipError, error) {
	return agentfs.NewFSSource(ctx, sources...)
}

// NewMultiSource builds a MultiSource over the given ordered sources. See
// agentfs.NewMultiSource.
func NewMultiSource(sources ...AgentSource) MultiSource { return agentfs.NewMultiSource(sources...) }

// Discover scans dir for agent defs. See agentfs.Discover.
func Discover(dir string) ([]Discovered, []SkipError, error) { return agentfs.Discover(dir) }

// NormalizeHeaders trims keys/values and drops empties. See
// agentfs.NormalizeHeaders.
func NormalizeHeaders(in map[string]string) map[string]string { return agentfs.NormalizeHeaders(in) }

// NormalizeHooks trims each phase key and command value and drops empties.
// See agentfs.NormalizeHooks.
func NormalizeHooks(in map[string]string) map[string]string { return agentfs.NormalizeHooks(in) }
