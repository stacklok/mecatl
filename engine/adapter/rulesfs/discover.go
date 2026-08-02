package rulesfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/stacklok/mecatl/engine/prompt"
)

// RuleFileExt is the conventional extension of a rule file. A rule lives at
// <dir>/<name>.md (a FLAT file, not a <name>/RULE.md subdir), matching Claude
// Code's .claude/rules/ layout — the agentfs flat-file twin, not the skills
// subdir shape.
const RuleFileExt = ".md"

// maxRuleBodyBytes is the package-internal alias of the CANONICAL body cap,
// which lives next to the port (prompt.MaxRuleBytes) so every source — this
// frontmatter parser AND a future remote-driver client — enforces the same
// number (the agentfs maxPromptBodyBytes = tool.MaxAgentBodyBytes precedent).
// parseRule truncates (rune-safe, with an ellipsis) and records a non-fatal
// warning when it trims.
const maxRuleBodyBytes = prompt.MaxRuleBytes

// frontmatter is the parsed YAML header of a rule file. Only `paths` is
// meaningful; unknown/extra keys (incl. `name`, which Claude Code's rules do
// not use) are ignored (forward-compat — the FILENAME stem is the name).
// `paths` uses stringOrSlice so a YAML sequence of globs AND a single
// comma/space-separated scalar both parse (Claude-Code tolerance, the agentfs
// `tools` mechanics).
type frontmatter struct {
	Paths stringOrSlice `yaml:"paths"`
}

// stringOrSlice is a YAML field that accepts BOTH a sequence (["a","b"]) AND a
// single comma/space-separated scalar ("a, b" or "a b") for Claude-Code
// compatibility — carried from agentfs (its `tools`/`disallowedTools`
// mechanics) so rulesfs's parser tolerance equals agentfs's. UnmarshalYAML
// normalises either form into a trimmed, empty-free []string.
//
// carries the agentfs stringOrSlice mechanics EXACTLY — the documented #328
// per-package carry pattern (a shared helpers package was rejected).
type stringOrSlice []string

func (s *stringOrSlice) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var arr []string
		if err := node.Decode(&arr); err != nil {
			return err
		}
		*s = splitList(arr...)
		return nil
	case yaml.ScalarNode:
		*s = splitList(node.Value)
		return nil
	default:
		return fmt.Errorf("expected a string or a list, got YAML kind %d", node.Kind)
	}
}

// splitList flattens its inputs, splitting any entry on commas and whitespace,
// trimming each token and dropping empties. It is the single normaliser for
// both the array and scalar `paths` forms.
//
// carries the agentfs splitList mechanics EXACTLY (same pinning as
// stringOrSlice above).
func splitList(in ...string) []string {
	var out []string
	for _, raw := range in {
		for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		}) {
			if t := strings.TrimSpace(tok); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// DirSource is the local-OS-filesystem implementation of RuleSource: it
// produces the rules laid out as <Dir>/<name>.md (flat files) under a single
// directory.
type DirSource struct {
	// Dir is the directory to scan. An empty or absent Dir yields no rules.
	Dir string
	// Label is an optional human-readable name for this source (e.g.
	// "project(.mecatl)", "user(xdg)"), surfaced in diagnostics (it prefixes
	// each Discovered entry's Detail). It does not affect discovery.
	Label string
	// Tier is the admission tier stamped onto every rule this source produces
	// (prompt.Rule.Origin on the port). ResolveSources sets it per conventional
	// location; a zero Tier defaults to prompt.RuleOriginUser (a
	// hand-constructed source is a personal location).
	Tier prompt.RuleOrigin
}

// origin returns the admission tier stamped onto this source's rules: Tier
// when set, else prompt.RuleOriginUser (the zero-value default).
func (s DirSource) origin() prompt.RuleOrigin {
	if s.Tier != "" {
		return s.Tier
	}
	return prompt.RuleOriginUser
}

// detail renders the adapter-private locator string for a rule discovered at
// path: "<label>: <path>", or the bare path when the source carries no label.
// It is the NON-PORT diagnostics channel (Discovered.Detail / FSSource.Detail).
func (s DirSource) detail(path string) string {
	if l := strings.TrimSpace(s.Label); l != "" {
		return l + ": " + path
	}
	return path
}

// Rules implements RuleSource for a single local directory. It scans Dir for
// flat <name>.md files, parses each one's optional YAML frontmatter and
// markdown body, and returns the valid rules sorted by name.
//
// It is forgiving by design: an empty or missing Dir yields no rules and no
// error; a malformed-frontmatter file is SKIPPED and reported via the returned
// []SkipError rather than aborting the scan. It returns a non-nil error only
// for a genuine I/O fault reading the directory itself.
//
// A rule's name is the FILENAME stem (NOT a frontmatter `name`, which Claude
// Code's rules do not use — one is ignored if present). Duplicate stems WITHIN
// this directory are resolved keep-first in sorted-path order. Cross-source
// collisions are resolved one level up by MultiSource.
//
// Every kept rule is stamped with this source's admission tier (Origin) and
// carried with its adapter-private locator (Detail = "<label>: <path>") —
// SkipError diagnostics keep the verbatim path.
func (s DirSource) Rules(_ context.Context) ([]Discovered, []SkipError, error) {
	dir := strings.TrimSpace(s.Dir)
	if dir == "" {
		return nil, nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("rules: read dir %q: %w", dir, err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var (
		out   []Discovered
		skips []SkipError
		seen  = map[string]string{} // effective name -> path that claimed it
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), RuleFileExt) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			skips = append(skips, SkipError{Path: path, Reason: fmt.Sprintf("cannot read: %v", rerr), Fatal: true})
			continue
		}
		name := strings.TrimSuffix(e.Name(), RuleFileExt)
		rule, perr, notes := parseRule(raw, name)
		if perr != "" {
			skips = append(skips, SkipError{Path: path, Reason: perr, Fatal: true})
			continue
		}
		// Notes are non-fatal: the rule IS kept, just adjusted (truncated).
		for _, n := range notes {
			skips = append(skips, SkipError{Path: path, Reason: n})
		}
		if prev, dup := seen[rule.Name]; dup {
			skips = append(skips, SkipError{
				Path:   path,
				Reason: fmt.Sprintf("duplicate rule name %q (already defined at %q)", rule.Name, prev),
				Fatal:  true,
			})
			continue
		}
		seen[rule.Name] = path
		rule.Origin = s.origin()
		out = append(out, Discovered{Rule: rule, Detail: s.detail(path)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Rule.Name < out[j].Rule.Name })
	return out, skips, nil
}

// Discover scans dir for rules and returns them. It is a thin convenience
// wrapper over DirSource for callers (and tests) that want single-directory
// discovery without composing a Source.
func Discover(dir string) ([]Discovered, []SkipError, error) {
	return DirSource{Dir: dir}.Rules(context.Background())
}

// parseRule splits raw into an OPTIONAL YAML frontmatter and a markdown body
// and builds the Rule. It returns a fatal reason string (with a zero Rule) on
// malformed frontmatter so the caller records a SkipError and EXCLUDES the
// rule (reason is "" on success), plus a slice of non-fatal warning notes for
// a rule that IS kept (truncation). A file with NO frontmatter parses to a
// rule with empty Paths (unconditional) — the frontmatter is optional.
//
// The name is the FILENAME stem (passed in by the caller); a frontmatter
// `name:` is ignored if present (Claude Code's rules carry none). The parser
// is filesystem-free so every Source implementation can reuse it.
func parseRule(raw []byte, name string) (Rule, string, []string) {
	fmText, body, ok := SplitFrontmatter(string(raw))
	if !ok {
		// No frontmatter: the WHOLE file is the body (an unconditional rule).
		body = string(raw)
	}

	var paths []string
	if fmText != "" {
		var fm frontmatter
		if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
			return Rule{}, fmt.Sprintf("malformed YAML frontmatter: %v", err), nil
		}
		paths = []string(fm.Paths)
	}

	var notes []string
	trimmedBody := strings.TrimSpace(body)
	if len(trimmedBody) > maxRuleBodyBytes {
		notes = append(notes, fmt.Sprintf(
			"body is %d bytes; truncated to the always-in-context rule cap of %d bytes",
			len(trimmedBody), maxRuleBodyBytes))
		trimmedBody = TruncateRunes(trimmedBody, maxRuleBodyBytes)
	}

	return Rule{
		Name:  name,
		Body:  trimmedBody,
		Paths: paths,
	}, "", notes
}
