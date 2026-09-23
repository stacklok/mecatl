package governance

// Effect is the outcome of a permission evaluation. The harness resolves a tool
// call across merged scopes with deny → ask → allow precedence: any deny wins,
// otherwise any ask wins, otherwise allow.
type Effect string

const (
	// Deny blocks the tool call; the Reason teaches the model why.
	Deny Effect = "deny"
	// Ask pauses the loop for client approval.
	Ask Effect = "ask"
	// Allow permits the tool call to execute.
	Allow Effect = "allow"
)

// AskProvenance identifies why an Ask survived the deterministic policy fold.
// The zero value is unknown/no-match and grants no additional approval authority;
// existing isolation and optional headless-review fallbacks still apply.
type AskProvenance string

const (
	// AskProvenanceUnknown is a no-match/default Ask. It grants no additional
	// approval authority; existing isolation and optional reviewer fallbacks apply.
	AskProvenanceUnknown AskProvenance = ""
	// AskProvenanceConfigured identifies a configured Ask rule.
	AskProvenanceConfigured AskProvenance = "configured_ask"
	// AskProvenanceConfiguredAllowFloor identifies a configured Allow stopped only by the substitution floor.
	AskProvenanceConfiguredAllowFloor AskProvenance = "configured_allow_floor"
	// AskProvenanceBuiltinSubstitutionFloor identifies the exact built-in worker substitution floor.
	AskProvenanceBuiltinSubstitutionFloor AskProvenance = "builtin_substitution_floor"
)

// PermissionDecision is the immutable result of evaluating a tool call across
// scopes. Reason is surfaced to the model on a deny so it can adapt (and to the
// client on an ask).
type PermissionDecision struct {
	// Effect is the resolved effect.
	Effect Effect
	// Reason explains the decision; especially important on Deny and Ask.
	Reason string
	// AskProvenance is meaningful only when Effect is Ask. It distinguishes a
	// configured Ask, a configured Allow stopped solely by the substitution floor,
	// and the exact built-in child Shell substitution floor eligible for contextual
	// permission review. Unknown/default asks grant no additional approval authority.
	AskProvenance AskProvenance
}

// Scope identifies the configuration layer a permission rule originates from.
// Higher-precedence scopes override lower ones when rules are merged. The
// ordering (highest first) is: Managed > CLI > LocalProject > SharedProject >
// User > BuiltinDefault, so a smaller Scope value has higher precedence.
type Scope int

const (
	// ScopeManaged is enterprise/managed policy; highest precedence.
	ScopeManaged Scope = iota
	// ScopeCLI is policy supplied on the command line / at invocation.
	ScopeCLI
	// ScopeLocalProject is the developer's local, un-shared project settings.
	ScopeLocalProject
	// ScopeSharedProject is checked-in, shared project settings.
	ScopeSharedProject
	// ScopeUser is the user's global settings.
	ScopeUser
	// ScopeBuiltinDefault is the harness's built-in default ruleset (the
	// read-allow / mutate-ask floor). It is the LOWEST precedence (largest iota
	// value), BELOW ScopeUser, so any configured rule of the same effect from a
	// higher scope wins the same-effect tie. Crucially, because it sits below
	// every config scope, a higher-scope Allow can LOOSEN a built-in Ask (the
	// merged deny→ask→allow fold still applies: a deny/ask in ANY scope beats an
	// allow, but among same-effect matches the higher scope is reported). It is
	// added at the TAIL of the iota so the existing scope values stay stable.
	ScopeBuiltinDefault
)

// HasHigherPrecedenceThan reports whether s overrides other when rules conflict.
func (s Scope) HasHigherPrecedenceThan(other Scope) bool {
	return s < other
}

// Audience scopes a Rule to the engine class it binds: the MAIN (interactive)
// engine, SUBAGENT (child/member/branch) engines, or both. The zero value
// (AudienceAll) applies everywhere, so an untagged rule keeps its full reach
// (the back-compatible default).
//
// Matching is symmetric-permissive: a rule binds an Evaluator iff either side is
// AudienceAll or both name the same audience. An Evaluator's audience is set
// with WithAudience (default AudienceAll). The conventional tagging — applied
// by callers that load rules for both engine classes — is: rules that should
// reach only the main engine carry AudienceMain, child-scoped rules carry
// AudienceSubagent, and a deny carries AudienceAll so it binds both (a deny
// only ever tightens).
type Audience int

const (
	// AudienceAll (the zero value) applies to every engine class.
	AudienceAll Audience = iota
	// AudienceMain applies only to the main (interactive) engine's evaluator.
	AudienceMain
	// AudienceSubagent applies only to child engines (Subagent children, team
	// members, parallel branches).
	AudienceSubagent
)

// Rule is a single permission rule: a pattern matched against a tool call,
// the effect it yields, and the scope it came from. Evaluation (WP4) merges
// rules across scopes honouring Scope precedence and deny→ask→allow.
type Rule struct {
	// Scope is the configuration layer this rule originates from.
	Scope Scope
	// Tool is the tool name this rule applies to (empty matches any tool).
	Tool string
	// Pattern is the matcher against the tool's arguments (tool-specific
	// syntax, e.g. a Shell command glob); empty matches any arguments.
	Pattern string
	// Effect is the effect this rule yields on a match.
	Effect Effect
	// Exact, when true, requires Pattern to match the (canonicalized) argument
	// string LITERALLY — never via glob expansion. It is the safety floor for a
	// LEARNED allow (LearnableRule sets it): a learned allow for `git status`
	// must match only `git status`, never let a stray `*`/`?` in the learned text
	// widen into a glob that green-lights commands the user never approved
	// (glob-escalation). Static config rules leave it false and keep glob matching.
	Exact bool
	// Audience scopes the rule to an engine class (main vs subagent). The zero
	// value (AudienceAll) matches every Evaluator, so an untagged rule keeps its
	// full reach. See Audience.
	Audience Audience
}
