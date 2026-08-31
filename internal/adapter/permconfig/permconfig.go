package permconfig

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

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
	if len(data) > maxConfigBytes {
		return Config{}, errConfigTooLarge(len(data))
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return Config{}, nil
	}

	file, err := parser.ParseBytes(data, 0)
	if err != nil {
		return Config{}, safePermconfigParseError(err)
	}
	if len(file.Docs) == 0 || (len(file.Docs) == 1 && file.Docs[0] != nil && file.Docs[0].Body == nil) {
		return Config{}, nil
	}
	if len(file.Docs) != 1 || file.Docs[0] == nil || file.Docs[0].Body == nil {
		return Config{}, fmt.Errorf("invalid permission config document")
	}
	root, ok := permconfigMapping(file.Docs[0].Body)
	if !ok {
		return Config{}, fmt.Errorf("permission config must be a mapping")
	}
	if hasTopLevelMappingKey(root, "output-economy") {
		return Config{}, fmt.Errorf("output-economy: unknown key (the output-economy setting was removed; delete it from your settings.yaml)")
	}

	var cfg Config
	if err := cfg.UnmarshalYAML(root); err != nil {
		return Config{}, safePermconfigSchemaError(err)
	}
	return cfg, nil
}

// permconfigSchemaError carries the fixed section identity and a source location
// independently of decoder-rendered text. Both fields originate from the mapping
// entry or typed decoder token, never from YAML scalar content.
type permconfigSchemaError struct {
	section  string
	location permconfigLocation
	err      error
}

func (*permconfigSchemaError) Error() string { return "invalid permission config schema" }

func (e *permconfigSchemaError) Unwrap() error { return e.err }

// safePermconfigSchemaError drops every decoder-rendered detail before a schema
// failure can reach resolver diagnostics. Section identity and location arrive as
// structured context from UnmarshalYAML and the decoder token.
func safePermconfigSchemaError(err error) error {
	section := "permission config"
	location := permconfigLocation{}
	var schemaErr *permconfigSchemaError
	if errors.As(err, &schemaErr) {
		section = schemaErr.section
		location = permconfigErrorLocation(schemaErr.err)
		if !location.HasLocation {
			location = schemaErr.location
		}
	}
	if location.HasLocation {
		return fmt.Errorf("invalid permission config schema at %s (line %d)", section, location.Line)
	}
	return fmt.Errorf("invalid permission config schema at %s", section)
}

// safePermconfigParseError keeps parser output out of returned errors and
// diagnostics. goccy's typed token is the only input-derived detail allowed.
func safePermconfigParseError(err error) error {
	location := permconfigErrorLocation(err)
	if location.HasLocation {
		return fmt.Errorf("invalid permission config YAML at line %d, column %d", location.Line, location.Column)
	}
	return fmt.Errorf("invalid permission config YAML")
}

func rejectRemovedTopLevelKeys(data []byte) error {
	file, err := parser.ParseBytes(data, 0)
	if err != nil || len(file.Docs) != 1 || file.Docs[0] == nil {
		return nil // Malformed YAML is reported by the primary parser path.
	}
	root, ok := permconfigMapping(file.Docs[0].Body)
	if ok && hasTopLevelMappingKey(root, "output-economy") {
		return fmt.Errorf("output-economy: unknown key (the output-economy setting was removed; delete it from your settings.yaml)")
	}
	return nil
}

func hasTopLevelMappingKey(root *ast.MappingNode, key string) bool {
	for _, entry := range root.Values {
		entryKey, ok := permconfigMappingKey(entry.Key)
		if ok && entryKey == key {
			return true
		}
	}
	return false
}

// UnmarshalYAML decodes known top-level sections while preserving the lenient
// top-level compatibility contract.
func (c *Config) UnmarshalYAML(node ast.Node) error {
	mapping, ok := permconfigMapping(node)
	if !ok {
		return fmt.Errorf("permission config must be a mapping")
	}
	known := map[string]any{
		"providers":              &c.Providers,
		"provider_overrides":     &c.ProviderOverrides,
		"permissions":            &c.Permissions,
		"guardrails":             newPermconfigNodePointer(&c.Guardrails),
		"posture":                &c.Posture,
		"models":                 newPermconfigNodePointer(&c.Models),
		"reasoning-effort":       &c.ReasoningEffort,
		"plan-mode-auto-approve": &c.PlanModeAutoApprove,
		"learning":               newPermconfigNodePointer(&c.Learning),
		"steer":                  newPermconfigNodePointer(&c.Steer),
		"openrouter":             newPermconfigNodePointer(&c.OpenRouter),
		"mcp":                    newPermconfigNodePointer(&c.MCP),
		"retention":              newPermconfigNodePointer(&c.Retention),
		"storage_management":     newPermconfigNodePointer(&c.StorageManagement),
	}
	for _, entry := range mapping.Values {
		key, ok := permconfigMappingKey(entry.Key)
		if !ok {
			continue // Top-level settings remain intentionally lenient.
		}
		target, known := known[key]
		if !known {
			continue
		}
		if err := yaml.NodeToValue(entry.Value, target); err != nil {
			return &permconfigSchemaError{
				section:  key,
				location: permconfigMappingEntryLocation(entry),
				err:      err,
			}
		}
	}
	return nil
}

func hasTopLevelKey(data []byte, key string) bool {
	file, err := parser.ParseBytes(data, 0)
	if err != nil || len(file.Docs) != 1 || file.Docs[0] == nil {
		return false
	}
	root, ok := permconfigMapping(file.Docs[0].Body)
	return ok && hasTopLevelMappingKey(root, key)
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
	file, err := parser.ParseBytes(data, 0)
	if err != nil || len(file.Docs) != 1 || file.Docs[0] == nil {
		return 0, 0, 0, false
	}
	root, mapping := permconfigMapping(file.Docs[0].Body)
	if !mapping {
		return 0, 0, 0, false
	}
	for _, entry := range root.Values {
		key, stringKey := permconfigMappingKey(entry.Key)
		if !stringKey || key != "permissions" {
			continue
		}
		var lc lenientCounts
		if err := yaml.NewDecoder(bytes.NewReader(nil)).DecodeFromNode(entry.Value, &lc.Permissions); err != nil {
			return 0, 0, 0, false
		}
		p := lc.Permissions
		return len(p.Deny) + len(p.Subagent.Deny),
			len(p.Ask) + len(p.Subagent.Ask),
			len(p.Allow) + len(p.Subagent.Allow),
			true
	}
	return 0, 0, 0, true
}
