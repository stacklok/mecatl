package modelhook

import "strings"

// Mode is a guardrail rule's enforcement posture.
type Mode string

const (
	// ModeAdvisory observes only: a checker "unsafe" verdict emits an operator
	// diagnostic but never alters the tool call/result (the model never sees it).
	ModeAdvisory Mode = "advisory"
	// ModeBlock enforces: an "unsafe" verdict vetoes a PreToolUse call (HookOutcome.Block)
	// or, because a PostToolUse Block is INERT (the tool already ran), rewrites the
	// result to a model-visible error via HookOutcome.Mutated.
	ModeBlock Mode = "block"
)

// Phase selects which tool-use phase(s) a rule inspects. A rule with no explicit
// phases inspects BOTH (the conservative default — a guardrail an operator forgot
// to scope still covers both directions).
type Phase string

const (
	// PhasePre inspects OUTBOUND tool-call arguments (exfiltration / secret leak).
	PhasePre Phase = "pre"
	// PhasePost inspects INBOUND tool results (injection / instruction-like content).
	PhasePost Phase = "post"
)

// RuleSpec is the operator-tier guardrail rule as configured (string match + string
// mode + string phase list), the input to CompileRule. The composition layer reads
// these from the operator-tier YAML / flags and compiles them once.
type RuleSpec struct {
	// Match is the tool-NAME matcher: an exact name, a "prefix*" glob, or "*".
	Match string
	// Phases is the directions this rule inspects ("pre"/"post"); empty = BOTH.
	Phases []string
	// Mode is the enforcement posture ("advisory"/"block").
	Mode string
	// Prompt overrides the built-in inspection prompt for the rule's direction.
	Prompt string
	// FailClosed flips the fail-open default for enforcing modes.
	FailClosed bool
	// FailClosedSet reports whether the operator explicitly set failClosed on
	// this rule (vs the YAML default false). When false, the global onCheckerDown
	// posture fills in; when true, the per-rule value wins over the global.
	FailClosedSet bool
	// SkipReadOnlyShell, when set, makes a Pre-phase Shell call whose command is
	// CONFIDENTLY read-only skip the checker entirely — a zero-LLM-call cost guard
	// so a configured guardrail can cover the local-shell blast radius (a mutating /
	// outward command like `gh pr merge`) without inspecting every `ls`. It is
	// FAIL-SAFE: an ambiguous / substitution-bearing command that cannot be proven
	// read-only is still inspected. Honored ONLY for tool=="Shell" && phase==pre; it
	// is inert on any other tool or on the post phase.
	SkipReadOnlyShell bool
	// Order is the rule's index in the configured list (the equal-specificity tiebreak).
	Order int
}

// CompileRule validates a RuleSpec into a CompiledRule. ok=false when the match is
// empty, the mode is unrecognised, or a listed phase is unrecognised — the caller
// (composition) logs and skips it. An empty Phases list inspects BOTH directions
// (the conservative default).
func CompileRule(spec RuleSpec) (CompiledRule, bool) {
	match := strings.TrimSpace(spec.Match)
	if match == "" {
		return CompiledRule{}, false
	}
	mode, ok := compileMode(spec.Mode)
	if !ok {
		return CompiledRule{}, false
	}
	pre, post, ok := compilePhases(spec.Phases)
	if !ok {
		return CompiledRule{}, false
	}
	return CompiledRule{
		match:             match,
		pre:               pre,
		post:              post,
		mode:              mode,
		prompt:            spec.Prompt,
		failClosed:        spec.FailClosed,
		failClosedSet:     spec.FailClosedSet,
		skipReadOnlyShell: spec.SkipReadOnlyShell,
		order:             spec.Order,
	}, true
}

// compileMode maps a config mode string to a Mode. An empty mode defaults to
// ModeBlock (the safe enforcing default for a configured guardrail). ok=false for an
// unrecognised non-empty value.
func compileMode(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ModeBlock):
		return ModeBlock, true
	case string(ModeAdvisory):
		return ModeAdvisory, true
	}
	return "", false
}

// compilePhases maps a config phase list to (pre, post). An empty list inspects
// BOTH. ok=false on an unrecognised phase token.
func compilePhases(phases []string) (pre, post, ok bool) {
	if len(phases) == 0 {
		return true, true, true
	}
	for _, p := range phases {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case string(PhasePre):
			pre = true
		case string(PhasePost):
			post = true
		default:
			return false, false, false
		}
	}
	return pre, post, true
}

// CompiledRule is one validated guardrail rule: a tool-name matcher, the phases it
// covers, its enforcement mode, the (optional) per-rule inspection prompt override,
// and the fail-closed opt-in. It is constructed only via CompileRule; its fields are
// unexported so the adapter owns the (validated) invariants.
type CompiledRule struct {
	// match is the tool-NAME matcher (matcher.go keys ONLY on the harness-controlled
	// tool name, never on attacker content): an exact name, a "prefix*" glob (e.g.
	// "mcp__github__*"), or "*" (catch-all).
	match string
	// pre/post report whether this rule inspects the outbound/inbound direction.
	pre  bool
	post bool
	// mode is the enforcement posture (advisory/block).
	mode Mode
	// prompt overrides the built-in inspection prompt for the rule's direction.
	// Empty keeps the built-in default (defaultPrePrompt / defaultPostPrompt).
	prompt string
	// failClosed flips the fail-OPEN default: when true, a checker error/timeout in
	// an enforcing mode treats the content as UNSAFE (Block on Pre, Mutated-to-error
	// on Post) instead of degrading to "no checker". A checker SAYING safe always
	// passes regardless.
	failClosed bool
	// failClosedSet reports whether the operator explicitly set failClosed on this
	// rule. When false, the global onCheckerDown posture fills in; when true, the
	// per-rule value wins over the global.
	failClosedSet bool
	// skipReadOnlyShell, when set, lets a Pre-phase Shell call whose command is
	// confidently read-only bypass the checker entirely (a zero-cost pre-filter for
	// the default Shell rule). FAIL-SAFE: an ambiguous/substitution command is still
	// inspected. Honored only for tool=="Shell" && phase==pre.
	skipReadOnlyShell bool
	// order is the rule's index in the configured list, the deterministic tiebreak
	// when two rules match with equal specificity.
	order int
}

// covers reports whether this rule inspects the given phase.
func (r CompiledRule) covers(p Phase) bool {
	switch p {
	case PhasePre:
		return r.pre
	case PhasePost:
		return r.post
	}
	return false
}

// specificity ranks a matcher for most-specific-wins resolution: an exact name
// (highest), then a prefix glob ranked by its literal-prefix length (longer = more
// specific), then "*" (lowest). It is the single ordering resolve() folds.
func specificity(match string) int {
	switch {
	case match == "*":
		return 0
	case strings.HasSuffix(match, "*"):
		// A prefix glob: longer literal prefix is more specific. +1 so any glob
		// out-ranks the bare "*" catch-all (whose prefix length is 0).
		return 1 + len(strings.TrimSuffix(match, "*"))
	default:
		// An exact name is the most specific; place it above every glob by a wide
		// margin (no realistic prefix glob reaches this band).
		return 1 << 20
	}
}

// matches reports whether a rule's tool-name matcher matches the given tool name.
func ruleMatches(match, tool string) bool {
	switch {
	case match == "*":
		return true
	case strings.HasSuffix(match, "*"):
		return strings.HasPrefix(tool, strings.TrimSuffix(match, "*"))
	default:
		return match == tool
	}
}

// resolve returns the single most-specific rule covering (tool, phase), or ok=false
// when no configured rule matches (the call is then unchecked — guardrails are
// opt-in per tool). Specificity is exact > longest-prefix glob > "*"; a tie (two
// equally-specific matchers, which a load-time WARN already flagged) breaks toward
// the EARLIER configured rule (lower order), so resolution is deterministic
// regardless of map iteration.
func resolve(rules []CompiledRule, tool string, phase Phase) (CompiledRule, bool) {
	best := CompiledRule{}
	found := false
	for _, r := range rules {
		if !r.covers(phase) || !ruleMatches(r.match, tool) {
			continue
		}
		if !found {
			best, found = r, true
			continue
		}
		rs, bs := specificity(r.match), specificity(best.match)
		switch {
		case rs > bs:
			best = r
		case rs == bs && r.order < best.order:
			best = r
		}
	}
	return best, found
}

// ResolveRule returns the effective compiled rule for a tool and phase. It is
// used by composition to place action review after trusted mutation while keeping
// matcher precedence owned by this adapter.
func ResolveRule(rules []CompiledRule, tool string, phase Phase) (CompiledRule, bool) {
	return resolve(rules, tool, phase)
}

// Advisory reports whether this rule observes without enforcing.
func (r CompiledRule) Advisory() bool { return r.mode == ModeAdvisory }

// Prompt returns the additive operator task-risk prompt for this rule.
func (r CompiledRule) Prompt() string { return r.prompt }

// FailClosed reports the effective checker-down posture for this rule.
func (r CompiledRule) FailClosed(global bool) bool {
	if r.failClosedSet {
		return r.failClosed
	}
	return global
}

// SkipAction reports whether this rule's conservative Shell pre-filter proves
// the exact action read-only. Ambiguous input never skips.
func (r CompiledRule) SkipAction(tool, input string) bool {
	if !r.skipReadOnlyShell || tool != "Shell" {
		return false
	}
	command, ok := shellCmdFromArgs(input)
	return ok && shellFullyReadOnly(command)
}
