package prompt

import "context"

// Rule is one project/user rule: a markdown body plus the OPTIONAL path globs that
// scope it. Pure value object — no behaviour, NO file/path/dir/root concept (the
// source's private business, matching SoulSource/CommandSource). A rule with an
// empty Paths slice is UNCONDITIONAL (always applies); a non-empty Paths slice
// scopes it to the listed globs, stated to the model as a condition the model
// applies itself (eager v1 — see ADR 0081). Paths rides the port from day one so
// a future lazy, path-triggered activation is additive, not a breaking change.
type Rule struct {
	Name   string     // logical id from the filename stem (e.g. "testing"); non-empty
	Body   string     // the markdown body, frontmatter stripped, byte-capped by the source
	Paths  []string   // glob patterns from `paths:` frontmatter; nil/empty = always applies
	Origin RuleOrigin // admission tier (observability only; a tier label, never a location)
}

// RuleOrigin classifies the ADMISSION TIER a rule entered through (project / user /
// driver). A tier label, NEVER a location — the SkillOrigin/AgentOrigin discipline.
// It is the THIRD parallel closed label set: extraction of a shared Origin type was
// evaluated here and DEFERRED — the sets are not identical (this set has no
// "explicit" tier; rules carry no operator-flag lane), so a shared type would force
// a superset one seam must never mint. Recorded in agentsource.go's NOTE.
type RuleOrigin string

// The CLOSED admission-tier label set — implementations must never mint a new
// label (a consumer that does not recognise one normalizes to Driver, mirroring
// the SkillOrigin/AgentOrigin contract).
const (
	RuleOriginProject RuleOrigin = "project" // workspace-tier (trust-gated at construction)
	RuleOriginUser    RuleOrigin = "user"    // user-tier (never trust-gated)
	RuleOriginDriver  RuleOrigin = "driver"  // operator-configured remote driver
)

// MaxRuleBytes caps a single rule body. The body is the EXPENSIVE field: it is
// always-in-context turn-0 context (every run prepends it), summed across ALL
// injected rules, so an unbounded one would inflate every prompt. It therefore
// stays CONSERVATIVE — 20 KiB mirrors the soul body cap
// (internal/adapter/soul/store.go). It is the ONE canonical cap every source
// shares: the filesystem frontmatter parser truncates to it on discovery, and a
// remote driver re-truncates wire data to it defensively (the conformance suite
// asserts every listed rule respects it). Changing this value is a ONE-TIME
// prompt-cache-prefix invalidation (the truncation point moves).
const MaxRuleBytes = 20 * 1024

// RulesSource is a CONSUMER-DEFINED port: prompt declares the tiny seam it needs
// (the ordered set of rules) and the rules adapter satisfies it structurally, so
// prompt never imports the rules adapter. It is satisfied by *rulesfs.FSSource
// (or a future remote driver), bound at the composition root. Mirrors
// SoulSource/CommandSource.
//
// Lifecycle: SNAPSHOT-semantics — ListRules is stable for the life of the source
// (the harness resolves once at build; there is no watch seam, matching the
// build-once trust-gate invariant).
type RulesSource interface {
	// ListRules returns the discovered rules, name-sorted and de-duplicated. A
	// nil/empty slice means "no rules"; an error is returned only for a genuine
	// read fault (not a missing source), and the assembler fails soft on it
	// (never aborts a run).
	ListRules(ctx context.Context) ([]Rule, error)
}

// RulesHeader returns the shared header string the RulesAssembler prepends to the
// rendered rules fragment. It is exported so tests can assert against the exact
// header the assembler emits, without carrying a private verbatim copy that could
// silently diverge if the header is reworded.
func RulesHeader() string { return rulesHeader }
