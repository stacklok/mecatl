package server

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
	// FeatureHTTPSteer is the unary HTTP steer and cancel-steer control pair
	// (issue #873, ADR 0252). The engine-level steer capability remains a
	// separate runtime fact; this identifier reports that the HTTP transport
	// implements the routes the TypeScript SDK can drive.
	FeatureHTTPSteer = "http_steer"

	// FeatureServerInfo is this RPC itself. It is degenerate over the wire — a
	// client that received a response already knows the server implements it —
	// but it is load-bearing as the registry's self-test: the feature set is
	// never empty, so "empty means something went wrong" stays a usable
	// assertion for every other consumer.
	FeatureServerInfo = "server_info"

	// FeatureWatchSessionEvents is the durable replay-then-follow watch — the
	// WatchSessionEvents RPC and its SSE peer (issue #821, ADR 0250).
	//
	// It answers "does this BUILD implement the watch?", which is the question a
	// client needs before it decides between one watch and the older
	// replay-then-subscribe dance. It deliberately does NOT answer "will a watch
	// succeed here": that additionally depends on the wired event log implementing
	// the cursor seam, which is a DEPLOYMENT fact and is reported by the
	// watch_unsupported error instead.
	//
	// The DISAMBIGUATOR is the stable `code` string, not the gRPC status.
	// watch_unsupported is registered as Unimplemented — exactly what grpc-go
	// returns for a method the server does not have — so a client switching on the
	// status alone still cannot separate a cursor-less store from version skew; it
	// has to read the code. That is the ADR 0248 design rather than a compromise:
	// the sibling no_event_log refusal ships the same status for the same class of
	// fact one level up, and moving this one to FailedPrecondition would make two
	// sibling refusals disagree while breaking clients whose Unimplemented handling
	// already covers it. What the feature flag buys is that a client never has to
	// reach the error at all to know whether the build has the RPC.
	FeatureWatchSessionEvents = "watch_session_events"

	// FeatureMCPServersOnCreate is client-provided MCP servers on session
	// creation — CreateSessionRequest.mcp_servers and its HTTP peer (issue #821,
	// ADR 0237).
	FeatureMCPServersOnCreate = "mcp_servers_on_create"

	// FeaturePromptFreeControls is the run-ID-addressed unary control family:
	// resolve-ask, cancel, steer, and cancel-steer (ADR 0347).
	FeaturePromptFreeControls = "prompt_free_controls"

	// FeatureSessionActivityInventory reports that ListSessions pages carry the
	// atomically persisted activity projection.
	FeatureSessionActivityInventory = "session_activity_inventory"
)

// FeatureScope is what the DEPLOYMENT permits, as distinct from what the build
// implements. It is the "listener argument" serverFeatures' doc comment
// anticipated, in the shape ADR 0237 requires: a composition policy value, not
// an inference the server package makes from its own socket state.
//
// One *Service backs both the gRPC and the HTTP listener, so this is decided
// ONCE at startup from the deployment's listener topology (mecated's
// clientMCPOnCreateForListeners) and handed in. A per-connection answer would
// be a different design needing its own ADR.
type FeatureScope struct {
	// ClientMCPOnCreate reports whether this deployment accepts
	// CreateSessionRequest.mcp_servers.
	ClientMCPOnCreate bool
	// SessionActivityInventory reports whether ListSessions reads an atomic,
	// activity-projecting metadata pager.
	SessionActivityInventory bool
}

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
	FeatureHTTPSteer,
	FeatureMCPServersOnCreate,
	FeaturePromptFreeControls,
	FeatureServerInfo,
	FeatureSessionActivityInventory,
	FeatureWatchSessionEvents,
}

// permittedBy reports whether scope permits the named feature.
//
// Only listener-scoped identifiers appear here; everything else is a pure build
// fact and is always permitted. Keeping the filter as one switch — rather than
// each transport testing its own conditions — is what stops the two surfaces
// from advertising different sets, which is the failure the scope note in
// serverFeatures warns about.
func permittedBy(scope FeatureScope, feature string) bool {
	switch feature {
	case FeatureMCPServersOnCreate:
		return scope.ClientMCPOnCreate
	case FeatureSessionActivityInventory:
		return scope.SessionActivityInventory
	default:
		return true
	}
}

// serverFeatures returns the feature identifiers this build implements, as a
// fresh sorted copy.
//
// The copy is not defensive politeness: the slice reaches a proto message that
// the gRPC layer may retain, and a caller mutating the shared backing array
// would corrupt every subsequent response from the process.
//
// SCOPE (ADR 0248 / ADR 0237): a feature that is reachable only on some
// deployments is advertised only where it is permitted, so this set reads as
// "what this build implements AND this deployment permits". The scope argument
// is how that filtering stays in ONE place: the callers hand in the composition
// policy and never do their own filtering, so the two transports cannot drift.
//
// The advertisement and the enforcement therefore read the SAME value —
// Config.ClientMCPOnCreate — which is what makes the advertisement honest: a
// deployment cannot advertise mcp_servers_on_create and then refuse the request,
// nor refuse it while staying silent about the refusal.
func serverFeatures(scope FeatureScope) []string {
	out := make([]string, 0, len(allFeatures))
	for _, f := range allFeatures {
		if permittedBy(scope, f) {
			out = append(out, f)
		}
	}
	return out
}
