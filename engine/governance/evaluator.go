package governance

import (
	"encoding/json"
	"strings"
)

// readOnlyTools are tools whose use never mutates the workspace and are therefore
// always permitted under plan mode. Bash is classified per-command via
// ReadOnlyBash rather than appearing here.
var readOnlyTools = map[string]bool{
	"Read":    true,
	"ListDir": true,
	"Grep":    true,
	"Glob":    true,
}

// mutatingTools are tools that always mutate and are unconditionally denied by
// plan mode. Bash is not listed: a Bash call is mutating only when its command
// is not read-only (see ReadOnlyBash).
var mutatingTools = map[string]bool{
	"Edit":   true,
	"Write":  true,
	"Copy":   true,
	"Move":   true,
	"Remove": true,
}

// Evaluator resolves a tool call against a merged set of permission Rules using
// deny → ask → allow precedence across Scopes. It is session-free by design so
// that package governance can stay free of a session import (doc.go): callers in
// the adapter/agent layer translate session types into the primitive arguments
// Evaluate accepts.
//
// Resolution rules (doc 08 §10):
//   - A Deny in ANY scope beats an Allow or Ask in ANY scope.
//   - Otherwise an Ask in any scope beats an Allow.
//   - Among rules of the SAME effect, the highest-precedence Scope wins (its
//     Reason is reported).
//   - With no matching rule the result is Ask (the safe default: pause for the
//     client) — the harness never silently allows an unconfigured call.
type Evaluator struct {
	rules []Rule
	// looseSubstitution, when true, DISABLES the built-in substitution Ask floor in
	// resolveBash: a substitution/subshell segment that is NOT SubstitutionReadOnly is
	// resolved by resolveSimple's own decision (so a higher-scope allow-all loosens it)
	// instead of being floored at Ask. It is the substitution analogue of the
	// mutate-ask floor loosening the --yolo allow-all already grants. Deny-dominance is
	// unaffected: a configured Deny (or Ask) on any segment still wins through the fold,
	// because the floor was only ever an ADDITIONAL escalation. DEFAULT false (the floor
	// stands). Set via WithLooseSubstitution in composition (Config.AllowAllTools).
	looseSubstitution bool
	// audience is the engine class this evaluator resolves for (issue #32). The
	// default AudienceAll matches every rule (back-compat); composition tags the
	// main engine's evaluator AudienceMain and child engines AudienceSubagent so
	// audience-scoped config rules (the permconfig `subagent:` block, top-level
	// allow/ask) bind only the engine class they were written for. Matching is
	// symmetric-permissive: a rule applies iff either the rule's or the
	// evaluator's audience is AudienceAll, or they are equal.
	audience Audience
}

// EvaluatorOption configures an Evaluator at construction.
type EvaluatorOption func(*Evaluator)

// WithLooseSubstitution loosens the built-in substitution Ask floor (the --yolo
// posture): a substitution segment that is not classifiable as read-only is resolved
// by the ordinary rule fold (so an allow-all rule allows it) rather than floored at
// Ask. A configured Deny/Ask in any scope still wins. DEFAULT (no option) keeps the
// floor — the substitution still prompts.
func WithLooseSubstitution(loose bool) EvaluatorOption {
	return func(e *Evaluator) { e.looseSubstitution = loose }
}

// WithAudience pins the engine class this Evaluator resolves for: AudienceMain
// for a main-engine policy, AudienceSubagent for a child/member policy. The
// DEFAULT (no option) is AudienceAll, which matches every rule — the
// back-compatible behaviour. An audience-tagged rule binds iff the rule's
// audience is AudienceAll, the Evaluator's is AudienceAll, or they match.
func WithAudience(a Audience) EvaluatorOption {
	return func(e *Evaluator) { e.audience = a }
}

// NewEvaluator constructs an Evaluator over the given merged rules. The rules may
// come from any mix of Scopes; precedence is resolved at evaluation time. The
// slice is copied so later mutation by the caller cannot affect the policy.
func NewEvaluator(rules []Rule, opts ...EvaluatorOption) *Evaluator {
	cp := make([]Rule, len(rules))
	copy(cp, rules)
	e := &Evaluator{rules: cp}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Evaluate resolves the decision for a tool call. tool is the tool name, args is
// its raw JSON argument payload (used for Bash compound-command splitting and
// pattern matching), and planMode forces deny for mutating actions (plan mode).
// It evaluates against the Evaluator's static rules only (no learned extras).
func (e *Evaluator) Evaluate(tool string, args json.RawMessage, planMode bool) PermissionDecision {
	return e.EvaluateWith(tool, args, planMode, nil)
}

// EvaluateWith resolves the decision for a tool call against the Evaluator's
// static rules PLUS the caller-supplied extra rules (e.g. per-session LEARNED
// allows). The ordering is security-load-bearing:
//
//  1. The plan-mode gate runs FIRST, BEFORE any learned rule is consulted — so a
//     learned allow can NEVER bypass plan mode's hard-deny of a mutating action.
//  2. Only then does the deny → ask → allow rule engine run over the static rules
//     with extra APPENDED. Because resolution is deny-dominant across the WHOLE
//     merged set, a static (or any) Deny still beats a learned Allow, and a static
//     Ask still beats a learned Allow — the extras only ever ADD allows at the
//     lowest scope; they can never weaken a deny/ask that already matches.
//
// The static rules are never mutated: a fresh slice (static followed by extra) is
// resolved per call, preserving the Evaluator's immutability.
func (e *Evaluator) EvaluateWith(tool string, args json.RawMessage, planMode bool, extra []Rule) PermissionDecision {
	if planMode {
		if d, blocked := planModeDecision(tool, args); blocked {
			return d
		}
	}
	if len(extra) == 0 {
		return e.resolve(e.rules, tool, args)
	}
	merged := make([]Rule, 0, len(e.rules)+len(extra))
	merged = append(merged, e.rules...)
	merged = append(merged, extra...)
	return e.resolve(merged, tool, args)
}

// LearnableRule derives the per-session allow rule the Evaluator WOULD learn for
// a (tool, args) pair the user chose to "allow always", reusing the SAME pattern
// derivation the evaluator matches against — so a learned rule matches exactly the
// call it was learned from, no broader.
//
// It returns (rule, false) — DO NOT learn — in any case where learning would be
// unsafe or untargetable:
//
//   - Bash whose command splits into != 1 segment (a compound `a && b` / `a; b`),
//     OR contains command/process substitution or subshell grouping ($(...),
//     backticks, (...)), OR is empty. Learning a single literal from a compound or
//     substituted line could green-light a hidden destructive command, so we refuse.
//   - Any tool whose derived pattern is EMPTY (no targetable path/command field):
//     an empty pattern would match the tool TOOL-WIDE, which is broader than the
//     conservative tool+exact-pattern grant this feature promises. We refuse rather
//     than learn a tool-wide allow.
//
// When learnable, the returned Rule is {Tool, Pattern, Effect: Allow, Exact: true,
// Scope: <lowest precedence>} — Exact so it matches literally (never via glob),
// and lowest scope so it can never out-rank a configured rule of any effect.
func (*Evaluator) LearnableRule(tool string, args json.RawMessage) (Rule, bool) {
	pattern, ok := learnablePattern(tool, args)
	if !ok || pattern == "" {
		return Rule{}, false
	}
	return Rule{
		// ScopeUser is the LOWEST precedence (largest Scope value); a learned allow
		// can never out-rank a configured rule. Precedence only breaks SAME-effect
		// ties anyway, and deny/ask always beat allow regardless of scope.
		Scope:   ScopeUser,
		Tool:    tool,
		Pattern: pattern,
		Effect:  Allow,
		Exact:   true,
	}, true
}

// learnablePattern derives the exact canonical pattern a learned rule would carry
// for (tool, args), or ok=false when the call must not be learned. For Bash it
// requires EXACTLY one canonicalized segment with no substitution/grouping; for
// every other tool it reuses nonBashPattern (the path/target field). An empty
// derived pattern is returned as-is so the caller refuses a tool-wide grant.
func learnablePattern(tool string, args json.RawMessage) (string, bool) {
	if tool == "Bash" {
		cmd, _ := BashCommandFromArgs(args)
		if cmd == "" {
			return "", false
		}
		subs := SplitCommands(cmd)
		if len(subs) != 1 {
			// Compound (a && b, a; b, pipelines, ...): refuse — a single learned
			// literal could green-light a hidden command in another segment.
			return "", false
		}
		seg := subs[0]
		if HasSubstitutionOrGrouping(seg) {
			// $(...), backticks, (...): the inner program can't be soundly extracted,
			// so we never learn it.
			return "", false
		}
		return Canonicalize(seg), true
	}
	return nonBashPattern(tool, args), true
}

// planModeDecision applies the plan-mode read-only gate. It returns a Deny
// decision and true when the call is a mutating action that plan mode forbids;
// otherwise it returns false and the rule engine proceeds as normal.
func planModeDecision(tool string, args json.RawMessage) (PermissionDecision, bool) {
	if mutatingTools[tool] {
		return PermissionDecision{
			Effect: Deny,
			Reason: "plan mode is active: " + tool + " mutates the workspace and is not permitted; present a plan and exit plan mode first",
		}, true
	}
	if tool == "Bash" {
		cmd, ok := BashCommandFromArgs(args)
		if ok && !ReadOnlyBash(cmd) {
			return PermissionDecision{
				Effect: Deny,
				Reason: "plan mode is active: this Bash command is not read-only and is not permitted; present a plan and exit plan mode first",
			}, true
		}
	}
	// Read-only tools (Read/Grep/Glob) and read-only Bash fall through.
	return PermissionDecision{}, false
}

// resolve runs the deny → ask → allow rule engine for a single tool call. For
// Bash it splits compound commands and requires EVERY sub-command to be allowed:
// a deny on any sub-command (e.g. `rm` in `git status && rm -rf /`) denies the
// whole compound.
func (e *Evaluator) resolve(rules []Rule, tool string, args json.RawMessage) PermissionDecision {
	if tool == "Bash" {
		return e.resolveBash(rules, args)
	}
	pattern := nonBashPattern(tool, args)
	return e.resolveSimple(rules, tool, pattern)
}

// resolveBash evaluates each canonicalized sub-command of a (possibly compound)
// Bash line and folds them with deny → ask → allow: the worst outcome wins. It
// also folds the two child-ask decision bits (issue #32): ConfiguredAsk (any
// segment's winning Ask came from a configured rule — conservative, so a
// configured Ask anywhere gates the whole compound) and FlooredConfiguredAllow
// (the ONLY reason the fold is Ask is the substitution floor, every floored
// segment had a configured Allow match AND passes flooredAllowSafe — every
// extracted inner positively read-only, blanked outer escape-rejection-free —
// and the floor-free fold is Allow). The two are mutually exclusive by
// construction: a configured Ask on any segment makes the floor-free fold
// not-Allow.
func (e *Evaluator) resolveBash(rules []Rule, args json.RawMessage) PermissionDecision {
	cmd, _ := BashCommandFromArgs(args)
	subs := SplitCommands(cmd)
	if len(subs) == 0 {
		// No parseable command: evaluate against the raw (empty) pattern.
		return e.resolveSimple(rules, "Bash", "")
	}
	worst := PermissionDecision{Effect: Allow, Reason: ""}
	haveDecision := false
	var (
		anyConfiguredAsk bool // some segment's (un-floored) Ask is configured
		flooredOK        int  // floored segments covered by a configured Allow + escape-free
		flooredBad       bool // some floored segment NOT covered (or escape-capable)
		unflooredNotAll  bool // some segment's FLOOR-FREE decision is not Allow
	)
	for _, sub := range subs {
		var d PermissionDecision
		if HasSubstitutionOrGrouping(sub) {
			// Command/process substitution or subshell grouping can smuggle an
			// arbitrary inner command past the operator splitter. Three cases, in
			// order:
			//   (A1) the substitution is fully READ-ONLY (every extracted inner is
			//        read-only AND the blanked outer is read-only) — do NOT floor:
			//        let resolveSimple's decision stand, so `cat $(ls)` resolves as
			//        the read-only Bash it is (Allow under allow-all / the read floor).
			//   (yolo) looseSubstitution disables the floor entirely — resolveSimple's
			//        decision stands so an allow-all rule loosens it.
			//   (default) we cannot soundly extract the inner program, so fail safe:
			//        evaluate the segment AND floor the result at Ask so an allow rule
			//        for the outer literal can never silently approve a hidden command.
			seg, segRule := e.resolveSimpleRule(rules, "Bash", Canonicalize(sub))
			switch {
			case SubstitutionReadOnly(sub):
				d = seg // read-only substitution: no floor, the ordinary decision stands.
			case e.looseSubstitution:
				d = seg // yolo: the floor is loosened, the ordinary decision stands.
			case effectRank(seg.Effect) >= effectRank(Ask):
				d = seg // already Ask or Deny: keep its (more specific) reason
			default:
				// The substitution floor escalates a would-be Allow to Ask (seg.Effect
				// is necessarily Allow here — Ask/Deny were kept above). Record whether
				// a CONFIGURED (above-floor) Allow covered the segment AND the segment
				// passes flooredAllowSafe: every extracted INNER must be positively
				// read-only (the configured Allow vouches only for the OUTER literal —
				// never for what a substitution hides), and the blanked outer must pass
				// the worktree-escape rejections — the precondition for
				// FlooredConfiguredAllow on the folded decision. The segment's
				// FLOOR-FREE effect is Allow either way, so unflooredNotAll stays unset.
				if ruleIsConfigured(segRule) && flooredAllowSafe(sub) {
					flooredOK++
				} else {
					flooredBad = true
				}
				d = PermissionDecision{
					Effect: Ask,
					Reason: "Bash command contains command/process substitution or subshell grouping that may hide an inner command; client approval required",
				}
				if !haveDecision || effectRank(d.Effect) > effectRank(worst.Effect) {
					worst = d
					haveDecision = true
				}
				continue
			}
		} else {
			d = e.resolveSimple(rules, "Bash", Canonicalize(sub))
		}
		// Un-floored segment (plain, A1, yolo, or kept Ask/Deny): its own effect IS
		// its floor-free effect.
		if d.Effect != Allow {
			unflooredNotAll = true
		}
		if d.Effect == Ask && d.ConfiguredAsk {
			anyConfiguredAsk = true
		}
		if !haveDecision || effectRank(d.Effect) > effectRank(worst.Effect) {
			worst = d
			haveDecision = true
		}
	}
	// Fold the decision bits fresh — the worst segment's own bits may not describe
	// the COMPOUND (a configured Ask elsewhere must still gate; the floored-allow
	// bit requires EVERY segment to cooperate). Both are meaningful only on Ask.
	worst.ConfiguredAsk, worst.FlooredConfiguredAllow = false, false
	if worst.Effect == Ask {
		worst.ConfiguredAsk = anyConfiguredAsk
		worst.FlooredConfiguredAllow = !anyConfiguredAsk && flooredOK > 0 && !flooredBad && !unflooredNotAll
	}
	return worst
}

// ruleIsConfigured reports whether r is a CONFIGURED rule: non-nil and scoped
// ABOVE the built-in floor (operator/CLI/project/user intent, never the harness's
// own defaults). The no-matching-rule default carries a nil rule.
func ruleIsConfigured(r *Rule) bool {
	return r != nil && r.Scope != ScopeBuiltinDefault
}

// resolveSimple finds the winning decision for a single tool+pattern against the
// rule set. The resolution is:
//
//  1. A Deny in ANY scope wins ABSOLUTELY (deny-dominant) — a deny can never be
//     out-ranked or loosened by an allow/ask of any scope.
//  2. Otherwise an Ask beats an Allow (the classic deny → ask → allow order), with
//     ONE narrow exception (issue #13): a higher-precedence Allow may loosen ONLY
//     a built-in-DEFAULT Ask (the ScopeBuiltinDefault floor). This lets a project/
//     user/CLI config Allow relax the harness's own read-allow/mutate-ask floor,
//     WITHOUT letting an Allow suppress any CONFIGURED Ask (e.g. a ScopeManaged
//     Allow must NOT silently override a ScopeSharedProject Ask — both are author
//     intent, and an Ask there must still gate). On an exact scope tie Ask wins.
//  3. No matching rule → Ask (the safe default: pause for the client).
//
// Within each effect the highest-precedence matching rule is the candidate (Scope
// breaks same-effect ties), then step 2 compares across the two non-deny effects.
//
// A winning Ask additionally reports its configured-ness (ConfiguredAsk): true
// when the ask rule's Scope sits above the ScopeBuiltinDefault floor (a real
// operator/project/user rule), false for the floor itself and for the no-match
// default Ask — the bit the child-ask model keys "never suppress a configured
// Ask" on (issue #32).
func (e *Evaluator) resolveSimple(rules []Rule, tool, pattern string) PermissionDecision {
	d, _ := e.resolveSimpleRule(rules, tool, pattern)
	return d
}

// resolveSimpleRule is resolveSimple plus the WINNING rule (nil for the
// no-matching-rule default Ask), so resolveBash can read the winner's
// configured-ness when folding the substitution-floor decision bits.
func (e *Evaluator) resolveSimpleRule(rules []Rule, tool, pattern string) (PermissionDecision, *Rule) {
	// Collect the highest-precedence matching rule per effect.
	best := map[Effect]*Rule{}
	for i := range rules {
		r := &rules[i]
		if !ruleMatches(r, tool, pattern, e.audience) {
			continue
		}
		cur := best[r.Effect]
		if cur == nil || r.Scope.HasHigherPrecedenceThan(cur.Scope) {
			best[r.Effect] = r
		}
	}
	// (1) Deny is absolute.
	if r := best[Deny]; r != nil {
		return PermissionDecision{Effect: Deny, Reason: ruleReason(r, tool, pattern)}, r
	}
	// (2) Ask normally beats Allow; the ONLY loosening is a higher-precedence Allow
	// over a built-in-DEFAULT Ask floor. A configured Ask (any scope above the
	// floor) is never suppressed by an Allow.
	ask, allow := best[Ask], best[Allow]
	switch {
	case ask != nil && allow != nil:
		if ask.Scope == ScopeBuiltinDefault && allow.Scope.HasHigherPrecedenceThan(ask.Scope) {
			return PermissionDecision{Effect: Allow, Reason: ruleReason(allow, tool, pattern)}, allow
		}
		return PermissionDecision{Effect: Ask, Reason: ruleReason(ask, tool, pattern), ConfiguredAsk: ruleIsConfigured(ask)}, ask
	case ask != nil:
		return PermissionDecision{Effect: Ask, Reason: ruleReason(ask, tool, pattern), ConfiguredAsk: ruleIsConfigured(ask)}, ask
	case allow != nil:
		return PermissionDecision{Effect: Allow, Reason: ruleReason(allow, tool, pattern)}, allow
	}
	// (3) No matching rule: default to Ask so an unconfigured call pauses for the
	// client rather than being silently allowed. Not a configured Ask — there is
	// no rule behind it.
	return PermissionDecision{
		Effect: Ask,
		Reason: "no permission rule matched " + tool + "; client approval required",
	}, nil
}

// effectRank orders effects by severity for compound folding: deny is worst.
func effectRank(e Effect) int {
	switch e {
	case Deny:
		return 3
	case Ask:
		return 2
	case Allow:
		return 1
	default:
		return 0
	}
}

// ruleMatches reports whether rule r applies to the given tool and pattern for
// an evaluator of the given audience. An empty Rule.Tool matches any tool; an
// empty Rule.Pattern matches any arguments. A non-empty pattern is matched as a
// shell-style glob against the (already canonicalized) command/argument string,
// with an exact-match fast path. The audience check is symmetric-permissive
// (issue #32): a rule binds iff the rule's audience is AudienceAll, the
// evaluator's is AudienceAll, or they are equal — so untagged rules and untagged
// evaluators keep their full historical reach.
func ruleMatches(r *Rule, tool, pattern string, audience Audience) bool {
	if r.Audience != AudienceAll && audience != AudienceAll && r.Audience != audience {
		return false
	}
	if r.Tool != "" && r.Tool != tool {
		return false
	}
	if r.Pattern == "" {
		return true
	}
	if r.Pattern == pattern {
		return true
	}
	// Exact rules (e.g. a LEARNED allow) match LITERALLY only — never via glob.
	// This is the glob-escalation floor: a learned pattern that happens to contain
	// a `*`/`?` must not widen into a glob that approves un-approved commands. We
	// already returned true above on the literal equality fast-path, so an Exact
	// rule that did not equal `pattern` simply does not match.
	if r.Exact {
		return false
	}
	return globMatch(r.Pattern, pattern)
}

// globMatch reports whether the glob pattern matches s. Unlike path.Match it does
// NOT treat "/" as a path separator (command strings contain slashes that "*"
// must be able to span, e.g. `rm *` matching `rm -rf /`). It supports "*" (any
// run, including empty) and "?" (any single rune). It is intentionally simple;
// rules needing richer matching should use exact patterns.
func globMatch(pattern, s string) bool {
	p := []rune(pattern)
	str := []rune(s)
	// Iterative backtracking matcher (linear in practice).
	var pi, si, star, mark int
	star = -1
	for si < len(str) {
		switch {
		case pi < len(p) && (p[pi] == str[si] || p[pi] == '?'):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star = pi
			mark = si
			pi++
		case star != -1:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// ruleReason returns a human-readable reason for the winning rule, falling back
// to a synthesized message when the rule carries none implicitly.
func ruleReason(r *Rule, tool, pattern string) string {
	switch r.Effect {
	case Deny:
		return "denied by rule for " + describeTarget(tool, r.Pattern, pattern)
	case Ask:
		return "approval required by rule for " + describeTarget(tool, r.Pattern, pattern)
	default:
		return "allowed by rule for " + describeTarget(tool, r.Pattern, pattern)
	}
}

func describeTarget(tool, rulePattern, pattern string) string {
	if rulePattern != "" {
		return tool + " (" + rulePattern + ")"
	}
	if pattern != "" {
		return tool + " (" + pattern + ")"
	}
	return tool
}

// nonBashPattern derives the pattern string to match a non-Bash tool call
// against. It uses the most common path/target field of the built-in tools so
// rules can target specific files; unknown shapes fall back to the empty string
// (which only matches tool-wide rules).
func nonBashPattern(_ string, args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "pattern", "url", "query"} {
		if raw, ok := m[key]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return s
			}
		}
	}
	return ""
}

// BashCommandFromArgs extracts the command string from a Bash tool call's raw
// args JSON. It reads the "command" field, then "cmd" as a fallback, returning
// the first non-empty (after TrimSpace) string. It is FAIL-SAFE: a JSON parse
// error OR neither field carrying a non-empty string returns ("", false), so a
// caller that gates a safety decision on the result INSPECTS rather than skips
// (an unreadable args object must never be presumed read-only). This is the
// single shared extraction for the Bash tool-call args schema — the governance
// evaluator, the Subagent isolation gate, and the guardrail Bash pre-filter all
// call it, so a schema change lands in one place.
func BashCommandFromArgs(args json.RawMessage) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return "", false
	}
	for _, key := range []string{"command", "cmd"} {
		rawVal, present := m[key]
		if !present {
			continue
		}
		var s string
		if err := json.Unmarshal(rawVal, &s); err != nil {
			continue
		}
		if strings.TrimSpace(s) != "" {
			return s, true
		}
	}
	return "", false
}

// IsReadOnlyTool reports whether a non-Bash tool is unconditionally read-only.
// Exposed for callers (dispatch, plan-mode) that need the same classification.
func IsReadOnlyTool(tool string) bool {
	return readOnlyTools[tool]
}
