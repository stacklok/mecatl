package server

import "slices"

// APIMajor is the wire-contract major version this build speaks (ADR 0248).
//
// It starts at 1 and bumps ONLY on a genuine break. Every additive change —
// a new RPC, a new field, a new capability — is announced through the feature
// vocabulary below instead, which is the entire reason that vocabulary exists.
// A client gates on APIMajor plus feature identifiers and NEVER on a
// server/package semver comparison: mecatl is deployed from main as often as
// from a tag, and a version string cannot tell a client whether a given RPC
// exists.
const APIMajor int32 = 1

// A feature identifier is an OPEN STRING, not an enum value.
//
// This is the discipline the event taxonomy already settled on the same axis
// (AGENTS.md: EvNoProgress/StopNoProgress are "STRING passthroughs on the wire
// … no `task generate` needed"). A closed proto enum would make every added
// feature a wire-compat event requiring codegen and a proto review, and would
// leave an older client decoding new values as UNKNOWN while its exhaustive
// switch silently grew a dead branch. Adding a server feature should be a minor
// SDK release, not a schema change.
//
// Identifiers are STABLE ONCE PUBLISHED. Renaming one is a break dressed up as
// a refactor: a deployed client gates on the exact string.
const (
	// FeatureServerInfo is this RPC itself. It is degenerate over the wire — a
	// client that received a response already knows the server implements it —
	// but it is load-bearing as the registry's self-test: the feature set is
	// never empty, so "empty means something went wrong" stays a usable
	// assertion for every other consumer.
	FeatureServerInfo = "server_info"
)

// allFeatures is the registry: the single source of truth both transports read.
//
// It is deliberately a plain sorted slice rather than a set keyed off scattered
// call sites. Each PR in the #821 server-enabler stack appends its own
// identifier as it lands, so a partially-deployed stack — which WILL exist,
// because these land in main incrementally — describes itself honestly instead
// of advertising capabilities its build does not have.
//
// ORDERING: sorted, and kept sorted by the registry test. The response is a
// repeated string and a client must treat it as a set, but a stable order keeps
// diffs and golden fixtures readable.
var allFeatures = []string{
	FeatureServerInfo,
}

// serverFeatures returns the feature identifiers this build implements, as a
// fresh sorted copy.
//
// The copy is not defensive politeness: the slice reaches a proto message that
// the gRPC layer may retain, and a caller mutating the shared backing array
// would corrupt every subsequent response from the process.
//
// SCOPE NOTE (ADR 0248 / ADR 0237): a feature that is reachable only on some
// listeners must be advertised only on a listener that permits it, so this set
// is properly read as "what this build implements AND this listener permits".
// No such feature exists yet — the listener-scoped mcp_servers work lands later
// in the stack — so today the two coincide. When the first one arrives, this
// function grows a listener argument rather than the callers growing their own
// filtering, or the two transports will drift.
func serverFeatures() []string {
	return slices.Clone(allFeatures)
}
