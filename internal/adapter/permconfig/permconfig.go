package permconfig

import (
	"fmt"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/stacklok/mecatl/engine/governance"
)

// Defense-in-depth caps (CWE-770), mirroring permstore's per-session rule cap. A
// permission config is operator/project-authored, but the file is still untrusted
// input read on the hot path; an unbounded file or rule list would let a single
// pathological config balloon memory or per-evaluate work.
const (
	// maxConfigBytes rejects an over-large config file before parsing. A real
	// permission config is a short allow/ask/deny list — a few hundred KB is far
	// beyond any legitimate use.
	maxConfigBytes = 256 * 1024
	// maxRulesPerConfig caps how many rules one config file contributes; specs
	// beyond the cap are dropped (and reported), so the file keeps asking for the
	// uncovered calls rather than silently growing the merged rule set.
	maxRulesPerConfig = 1024
)

// errConfigTooLarge is returned by parse paths when the input exceeds
// maxConfigBytes. Callers log-and-skip it (fail-soft), like any other parse error.
func errConfigTooLarge(n int) error {
	return fmt.Errorf("permission config too large: %d bytes exceeds the %d-byte cap", n, maxConfigBytes)
}

// ValidateYAML validates a complete settings document with the same bounded parser
// used when loading operator and project configuration. It does not expose parsed
// values or mutate configuration state.
func ValidateYAML(data []byte) error {
	_, err := parseYAML(data)
	return err
}

// parseYAML unmarshals the `.mecatl/settings.yaml` bytes into a Config. A nil/
// empty input yields a zero Config (no rules). A malformed document is a hard
// error the caller surfaces (a config file that cannot be parsed must not be
// silently ignored — that would hide a typo that disables a deny rule).
func parseYAML(data []byte) (Config, error) {
	var cfg Config
	if len(data) > maxConfigBytes {
		return Config{}, errConfigTooLarge(len(data))
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return Config{}, nil
	}
	// TARGETED unknown-key rejection (the top-level decode is deliberately
	// lenient — plain yaml.Unmarshal — so removed keys must be named
	// individually): `output-economy:` was REMOVED (ADR 0089, the clean break
	// superseding ADR 0086's parse-compat shim; ADR 0041 INTRODUCED the
	// setting). Error precisely so the invalid-file WARN names the key to
	// delete.
	if err := rejectRemovedTopLevelKeys(data); err != nil {
		return Config{}, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse permission config: %w", err)
	}
	return cfg, nil
}

func hasTopLevelKey(data []byte, key string) bool {
	var node yaml.Node
	if yaml.Unmarshal(data, &node) != nil || len(node.Content) == 0 {
		return false
	}
	root := node.Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return true
		}
	}
	return false
}

// rejectRemovedTopLevelKeys probes the document for a top-level mapping key
// that was REMOVED from the schema, with an error naming the key precisely. The
// probe is a one-field flat struct decode — lenient yaml.Unmarshal ignores
// unknown keys, so only the top-level `output-economy:` binds (a NESTED
// `output-economy:` under a section can never trip it: the flat struct has no
// path to it).
func rejectRemovedTopLevelKeys(data []byte) error {
	var probe struct {
		OutputEconomy *string `yaml:"output-economy"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		// Malformed YAML: fall through to the real decode, which reports it.
		return nil
	}
	if probe.OutputEconomy != nil {
		return fmt.Errorf("output-economy: unknown key (the output-economy setting was removed; delete it from your settings.yaml)")
	}
	return nil
}

// rulesFromConfig converts a parsed Config into governance.Rule values tagged
// with the given scope and the per-bucket Audience (issue #32 D1):
//
//   - top-level deny  → AudienceAll (binds main AND children — a deny only tightens)
//   - top-level allow/ask → AudienceMain (the operator configured the
//     interactive engine; a child does not inherit a main allow/ask)
//   - the subagent: block → AudienceSubagent (allow/ask/deny), at the SAME
//     tier scope as the file it came from
//
// The deny → ask → allow ordering in the returned slice is irrelevant for
// precedence (the Evaluator folds deny-dominant regardless), but it IS the cap
// order: at maxRulesPerConfig the SAFER effects survive — deny(all) →
// subagent-deny → ask → subagent-ask → allow → subagent-allow — so an allow is
// always the first to be dropped. Unparseable specs are collected into report
// so the caller can surface them.
func rulesFromConfig(cfg Config, scope governance.Scope, report *Report) []governance.Rule {
	var rules []governance.Rule
	add := func(specs []string, effect governance.Effect, audience governance.Audience) {
		for _, spec := range specs {
			if len(rules) >= maxRulesPerConfig {
				report.addDropped(spec, "rule-count cap reached; rule dropped")
				continue
			}
			rule, ok := parseSpec(spec, scope, effect)
			if !ok {
				report.addDropped(spec, "unparseable rule spec")
				continue
			}
			rule.Audience = audience
			rules = append(rules, rule)
		}
	}
	add(cfg.Permissions.Deny, governance.Deny, governance.AudienceAll)
	add(cfg.Permissions.Subagent.Deny, governance.Deny, governance.AudienceSubagent)
	add(cfg.Permissions.Ask, governance.Ask, governance.AudienceMain)
	add(cfg.Permissions.Subagent.Ask, governance.Ask, governance.AudienceSubagent)
	add(cfg.Permissions.Allow, governance.Allow, governance.AudienceMain)
	add(cfg.Permissions.Subagent.Allow, governance.Allow, governance.AudienceSubagent)
	return rules
}

// parseSpec parses a single rule spec of the form "Tool(pattern)" or bare "Tool"
// into a governance.Rule with the given scope and effect. It returns ok=false
// when the spec is empty or malformed (e.g. an unmatched parenthesis).
//
// The pattern is normalised via normalizeGlob so the Claude-style ":" prefix-glob
// ("go test:*") and the mecatl glob ("go test*") both resolve to the evaluator's
// glob grammar. Exact stays FALSE: config rules use glob semantics (only LEARNED
// rules are Exact — that invariant is preserved here).
func parseSpec(spec string, scope governance.Scope, effect governance.Effect) (governance.Rule, bool) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return governance.Rule{}, false
	}
	open := strings.IndexByte(s, '(')
	if open < 0 {
		// Bare tool name, tool-wide rule (empty pattern matches any args).
		return governance.Rule{Scope: scope, Tool: s, Effect: effect}, true
	}
	if !strings.HasSuffix(s, ")") {
		return governance.Rule{}, false
	}
	toolName := strings.TrimSpace(s[:open])
	if toolName == "" {
		return governance.Rule{}, false
	}
	inner := s[open+1 : len(s)-1]
	return governance.Rule{
		Scope:   scope,
		Tool:    toolName,
		Pattern: normalizeGlob(inner),
		Effect:  effect,
	}, true
}

// normalizeGlob normalises a rule-spec pattern into the evaluator's glob grammar.
//
//   - The Claude-Code convention "<prefix>:*" (and the bare "<prefix>:") means a
//     prefix match: "go test:*" → "go test*", "git push:" → "git push*". This is
//     applied to ANY tool (Claude uses it for Bash; harmless elsewhere since the
//     ":" form is Claude-specific).
//   - An empty pattern stays empty (tool-wide).
//   - Everything else passes through unchanged (it is already a mecatl glob, e.g.
//     "go test*" or a file glob "src/**").
func normalizeGlob(pattern string) string {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return ""
	}
	if i := strings.LastIndexByte(p, ':'); i >= 0 {
		head := p[:i]
		tail := p[i+1:]
		switch tail {
		case "*", "":
			// Prefix glob: match the head followed by anything.
			return head + "*"
		}
	}
	return p
}

// lenientCounts is the permissive twin of the strict permissions schema, used
// ONLY to estimate what a skipped (strict-parse-failed) file WOULD have
// contributed. Unknown keys are ignored here by design — the point is to count
// the recognisable buckets a typo'd sibling key is about to cost the operator.
type lenientCounts struct {
	Permissions struct {
		Allow    []string `yaml:"allow"`
		Ask      []string `yaml:"ask"`
		Deny     []string `yaml:"deny"`
		Subagent struct {
			Allow []string `yaml:"allow"`
			Ask   []string `yaml:"ask"`
			Deny  []string `yaml:"deny"`
		} `yaml:"subagent"`
	} `yaml:"permissions"`
}

// lostRuleCounts best-effort counts the per-effect rules a SKIPPED config file
// loses (issue #32 panel finding 8a): a strict-parse failure (e.g. a typo'd
// key) drops the WHOLE file — including its DENY rules, so strictness would
// otherwise silently LOOSEN policy. The counts ride the existing skip WARN so
// the operator sees exactly how much tightening was lost. Lenient decode; on a
// true YAML syntax error the counts are simply unavailable (all zero,
// ok=false). Top-level and subagent buckets fold per effect.
func lostRuleCounts(data []byte) (deny, ask, allow int, ok bool) {
	var lc lenientCounts
	if err := yaml.Unmarshal(data, &lc); err != nil {
		return 0, 0, 0, false
	}
	p := lc.Permissions
	return len(p.Deny) + len(p.Subagent.Deny),
		len(p.Ask) + len(p.Subagent.Ask),
		len(p.Allow) + len(p.Subagent.Allow),
		true
}
