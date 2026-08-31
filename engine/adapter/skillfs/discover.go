package skillfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"

	"github.com/stacklok/mecatl/engine/adapter/frontmatterdiag"
	"github.com/stacklok/mecatl/engine/tool"
)

// SkillFileName is the conventional file every skill directory contains. A skill
// lives at <dir>/<name>/SKILL.md, mirroring the Agent Skills layout.
const SkillFileName = "SKILL.md"

// DefaultDir is the conventional project-level skills directory, relative to the
// workspace. It is one of the conventional locations the known-path resolver
// (ResolveSources) searches; it is NOT applied automatically by an explicit
// DirSource — skills are opt-in. It is exposed so the composition root can
// surface the convention (e.g. in flag help text).
const DefaultDir = ".mecatl/skills"

// MaxDescriptionBytes caps a skill's one-line description. The description is the
// ALWAYS-IN-CONTEXT metadata (it lives in the Skill tool's Spec().Description, on
// every request), so an unbounded one would inflate every prompt and break the
// byte-stable prompt-prefix caching the OpenAI adapter relies on. A skill
// description is a single line; 800 bytes is generous for that. parseSkill
// truncates (rune-safe, with an ellipsis) and records a non-fatal warning when it
// trims. Exported so the remote-driver skill-source client (grpcdriver) can
// re-truncate defensively to the SAME cap.
const MaxDescriptionBytes = 800

// maxDescriptionBytes is the package-internal alias of MaxDescriptionBytes,
// retained so the parse/draft sites (and their tests) read unchanged.
const maxDescriptionBytes = MaxDescriptionBytes

// frontmatter is the parsed YAML header of a SKILL.md file. Name and
// description are part of the always-in-context metadata; license,
// compatibility, metadata, and allowed-tools are OPTIONAL ADVISORY fields
// carried verbatim (byte-capped defensively by parseSkill); any other keys are
// ignored so the format can grow without breaking discovery.
//
// AllowedTools is the agentskills.io Experimental `allowed-tools` field. The
// spec form is a SPACE-SEPARATED STRING (e.g. `allowed-tools: "Bash Read Grep"`),
// parsed by splitting on whitespace; a YAML LIST form (`[Bash, Read]`) is
// accepted too (the parser unifies into a []string here) but the string form is
// canonical. It is ADVISORY ONLY — surfaced as a note on activation, never a
// permission grant.
type frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  yamlAllowedTools  `yaml:"allowed-tools"`
}

func (f *frontmatter) UnmarshalYAML(node ast.Node) error {
	type decoded frontmatter
	var value decoded
	if err := yaml.NodeToValue(node, &value); err != nil {
		return err
	}
	*f = frontmatter(value)
	if value := frontmatterMappingValue(node, "allowed-tools"); value != nil {
		return f.AllowedTools.UnmarshalYAML(value)
	}
	return nil
}

func frontmatterMappingValue(node ast.Node, name string) ast.Node {
	mapping, ok := node.(*ast.MappingNode)
	if !ok {
		return nil
	}
	for _, value := range mapping.Values {
		if value.Key.GetToken().Value == name {
			return value.Value
		}
	}
	return nil
}

// yamlAllowedTools accepts the `allowed-tools` field as EITHER a
// space-separated string (the spec form) OR a YAML list of strings, normalizing
// both into a []string. The default []string unmarshal would reject the
// scalar string form, so a custom unmarshaler unifies the two.
type yamlAllowedTools []string

func (a *yamlAllowedTools) UnmarshalYAML(node ast.Node) error {
	if text, ok := frontmatterScalarText(node); ok {
		*a = splitAllowedTools(text)
		return nil
	}
	if node.Type() != ast.SequenceType {
		return fmt.Errorf("allowed-tools must be a string or a list of strings")
	}
	sequence, ok := node.(*ast.SequenceNode)
	if !ok {
		return fmt.Errorf("allowed-tools must be a string or a list of strings")
	}
	out := make([]string, 0, len(sequence.Values))
	for _, item := range sequence.Values {
		text, ok := frontmatterScalarText(item)
		if !ok {
			return fmt.Errorf("allowed-tools must be a string or a list of strings")
		}
		out = append(out, splitAllowedTools(text)...)
	}
	*a = out
	return nil
}

// frontmatterScalarText preserves yaml.v3's scalar-as-text frontmatter
// compatibility while avoiding parser-rendered error text at this boundary.
func frontmatterScalarText(node ast.Node) (string, bool) {
	switch node.Type() {
	case ast.StringType, ast.LiteralType:
		var text string
		if err := yaml.NodeToValue(node, &text); err != nil {
			return "", false
		}
		return text, true
	case ast.BoolType, ast.IntegerType, ast.FloatType, ast.NullType, ast.InfinityType, ast.NanType:
		if parserToken := node.GetToken(); parserToken != nil {
			return parserToken.Value, true
		}
	}
	return "", false
}

// splitAllowedTools splits a whitespace-separated allowed-tools string into
// trimmed, non-empty tokens.
func splitAllowedTools(s string) []string {
	return strings.Fields(s)
}

// MaxLicenseBytes and MaxCompatibilityBytes cap the advisory license and
// compatibility strings carried on the always-in-context SkillMeta. They are
// ADVISORY (never enforced as a gate), but they ride the in-context metadata,
// so an unbounded one could inflate every prompt and break the byte-stable
// prompt-prefix caching. Exported so the remote-driver skill-source client
// (grpcdriver) can re-truncate defensively to the SAME cap.
const (
	MaxLicenseBytes       = 1024
	MaxCompatibilityBytes = 1024

	// MaxMetadataEntries caps the advisory metadata map's entry count; an
	// over-count drops the WHOLE map to nil (and a warning note) rather than
	// silently truncating it.
	MaxMetadataEntries = 32
	// MaxMetadataValueBytes caps one metadata value; a single over-sized value
	// drops the WHOLE map to nil (and a warning note).
	MaxMetadataValueBytes = 4096

	// MaxAllowedTools caps the advisory `allowed-tools` entry count; an
	// over-count truncates to the first MaxAllowedTools names (and a warning
	// note). It rides the always-in-context SkillMeta, so an unbounded list could
	// inflate every prompt. Exported so the remote-driver skill-source client
	// (grpcdriver) re-clamps defensively to the SAME cap.
	MaxAllowedTools = 64
	// MaxAllowedToolNameBytes caps one advisory `allowed-tools` name; an
	// over-long name is truncated (and a warning note).
	MaxAllowedToolNameBytes = 64
)

// DirSource is the local-OS-filesystem implementation of Source: it produces the
// skills laid out as <Dir>/<name>/SKILL.md under a single directory. It is the
// default, conventional source; other Source implementations (embedded defaults,
// a remote registry) reuse the same parsing (parseSkill/splitFrontmatter) without
// touching the filesystem.
type DirSource struct {
	// Dir is the directory to scan. An empty Dir yields no skills (opt-in), as
	// does a Dir that does not exist.
	Dir string
	// Label is an optional human-readable name for this source (e.g. "project",
	// "user", "explicit"), surfaced in diagnostics and logs. It does not affect
	// discovery or precedence.
	Label string
	// Tier is the admission tier stamped onto every skill this source produces
	// (Skill.Origin → SkillMeta.Origin on the port). ResolveSources sets it per
	// conventional location; a zero Tier defaults to tool.SkillOriginExplicit (a
	// hand-constructed DirSource is an operator-configured location). It is a
	// closed label, never a location, and does not affect discovery or precedence.
	Tier tool.SkillOrigin
}

// Skills implements Source for a single local directory. It scans Dir for skills
// laid out as <Dir>/<name>/SKILL.md, parses each one's YAML frontmatter and
// markdown body, and returns the valid skills sorted by name for deterministic
// output.
//
// It is forgiving by design: an empty or missing Dir yields no skills and no
// error (skills are opt-in); a malformed or frontmatter-less SKILL.md is SKIPPED
// and reported via the returned []SkipError rather than aborting the scan. It
// returns a non-nil error only for a genuine I/O fault reading the directory
// itself.
//
// A skill whose frontmatter `name` does not EXACTLY match its parent directory
// name is SKIPPED (fail-soft, reported as a SkipError) — the agentskills.io
// dir-name-match rule. The directory name is the activation key; the frontmatter
// must agree with it. Duplicate effective names WITHIN this directory are
// resolved by keeping the first in sorted-path order and skipping the rest
// (reported as a SkipError). Cross-source collisions are resolved one level up,
// by MultiSource.
func (s DirSource) Skills(_ context.Context) ([]Skill, []SkipError, error) {
	dir := strings.TrimSpace(s.Dir)
	if dir == "" {
		return nil, nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Opt-in: an absent skills dir is simply "no skills", not an error.
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("skills: read dir %q: %w", dir, err)
	}

	// Sort directory entries first so both the keep-first dedup and the final
	// output are deterministic regardless of filesystem ordering.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var (
		out   []Skill
		skips []SkipError
		seen  = map[string]string{} // effective name -> path that claimed it
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), SkillFileName)
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				// A subdirectory without a SKILL.md is not a skill; silently ignore.
				continue
			}
			skips = append(skips, SkipError{Path: path, Reason: fmt.Sprintf("cannot read: %v", rerr)})
			continue
		}
		sk, perr, notes := ParseSkill(raw, path)
		if perr != "" {
			skips = append(skips, SkipError{Path: path, Reason: perr})
			continue
		}
		sk.Origin = s.origin()
		// The agentskills.io dir-name-match rule: the frontmatter name must
		// equal the parent directory name. A mismatch is fail-soft (skip, keep
		// scanning), reported as a SkipError.
		if sk.Name != e.Name() {
			skips = append(skips, SkipError{
				Path:   path,
				Reason: fmt.Sprintf("skill name %q does not match its directory %q", sk.Name, e.Name()),
			})
			continue
		}
		// Non-fatal warnings (e.g. truncation): the skill is kept, but the author
		// gets a signal via the returned diagnostics.
		for _, n := range notes {
			skips = append(skips, SkipError{Path: path, Reason: n})
		}
		if prev, dup := seen[sk.Name]; dup {
			skips = append(skips, SkipError{
				Path:   path,
				Reason: fmt.Sprintf("duplicate skill name %q (already defined at %q)", sk.Name, prev),
			})
			continue
		}
		seen[sk.Name] = path
		out = append(out, sk)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, skips, nil
}

// origin returns the admission tier stamped onto this source's skills: Tier
// when set, else tool.SkillOriginExplicit (the zero-value default — a
// hand-constructed DirSource is an operator-configured location).
func (s DirSource) origin() tool.SkillOrigin {
	if s.Tier != "" {
		return s.Tier
	}
	return tool.SkillOriginExplicit
}

// Discover scans dir for skills and returns them. It is a thin convenience
// wrapper over DirSource preserved for backward compatibility and for callers
// (and tests) that want single-directory discovery without composing a Source.
// New code should construct a DirSource (and compose it with NewMultiSource).
func Discover(dir string) ([]Skill, []SkipError, error) {
	return DirSource{Dir: dir}.Skills(context.Background())
}

// ParseSkill splits raw into YAML frontmatter and a markdown body and validates
// the required header fields. It returns:
//   - a fatal reason string (with a zero Skill) on any structural problem, so the
//     caller records a SkipError and EXCLUDES the skill; reason is "" on success.
//   - a slice of non-fatal warning notes for a skill that IS kept (e.g. its
//     description was truncated to the always-in-context cap, or its body exceeds
//     the activation output cap), so the author gets a signal.
//
// The description is capped HERE (at parse time) to maxDescriptionBytes because it
// lives in the always-in-context tool spec; the body is NOT trimmed here (the
// Skill tool truncates it on activation against the shared output cap), but an
// oversized body is flagged so the author knows it will be truncated. ParseSkill
// is filesystem-free so every Source implementation can reuse it.
//
// Exported so the root writable half (internal/adapter/skills/promote.go) can
// re-run the promotion-gate structural validation through the SAME parser the
// read-only core uses, without importing this adapter (the root package aliases
// it). Behaviour is byte-identical to the pre-graduation parseSkill.
func ParseSkill(raw []byte, path string) (Skill, string, []string) {
	fmText, body, ok := SplitFrontmatter(string(raw))
	if !ok {
		return Skill{}, "missing YAML frontmatter (expected a leading '---' delimited block)", nil
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return Skill{}, frontmatterdiag.FrontmatterParseError(err), nil
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return Skill{}, "frontmatter is missing a non-empty \"name\"", nil
	}
	if !ValidSkillName(name) {
		return Skill{}, fmt.Sprintf("skill name %q is invalid: must be 1-64 chars, lowercase letters/digits/'-'/'_', starting with a letter or digit (no spaces, no uppercase, no path separators)", name), nil
	}
	desc := strings.TrimSpace(fm.Description)
	if desc == "" {
		return Skill{}, "frontmatter is missing a non-empty \"description\"", nil
	}

	var notes []string
	if len(desc) > maxDescriptionBytes {
		notes = append(notes, fmt.Sprintf(
			"description is %d bytes; truncated to the always-in-context cap of %d bytes (a skill description should be a single line)",
			len(desc), maxDescriptionBytes))
		desc = TruncateRunes(desc, maxDescriptionBytes)
	}

	trimmedBody := strings.TrimSpace(body)
	if len(trimmedBody) > MaxOutputBytes {
		notes = append(notes, fmt.Sprintf(
			"body is %d bytes; it will be truncated to %d bytes when the skill is activated",
			len(trimmedBody), MaxOutputBytes))
	}

	license := strings.TrimSpace(fm.License)
	if len(license) > MaxLicenseBytes {
		notes = append(notes, fmt.Sprintf(
			"license is %d bytes; truncated to the advisory cap of %d bytes",
			len(license), MaxLicenseBytes))
		license = TruncateRunes(license, MaxLicenseBytes)
	}
	compat := strings.TrimSpace(fm.Compatibility)
	if len(compat) > MaxCompatibilityBytes {
		notes = append(notes, fmt.Sprintf(
			"compatibility is %d bytes; truncated to the advisory cap of %d bytes",
			len(compat), MaxCompatibilityBytes))
		compat = TruncateRunes(compat, MaxCompatibilityBytes)
	}
	meta := clampMetadata(fm.Metadata, &notes)
	allowed := clampAllowedTools(fm.AllowedTools, &notes)

	return Skill{
		Name:          name,
		Description:   desc,
		Body:          trimmedBody,
		Path:          path,
		License:       license,
		Compatibility: compat,
		Metadata:      meta,
		AllowedTools:  allowed,
	}, "", notes
}

// clampMetadata defensively bounds the optional advisory metadata map to
// MaxMetadataEntries with each value ≤ MaxMetadataValueBytes. On ANY overflow
// (too many entries, or a single over-sized value) the WHOLE map drops to nil
// and a non-fatal warning note is recorded — the map is advisory, so dropping
// it is honest rather than silently truncating. A nil/empty map stays nil.
// Keys/values are trimmed of surrounding whitespace; an empty key is dropped.
func clampMetadata(in map[string]string, notes *[]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	if len(in) > MaxMetadataEntries {
		*notes = append(*notes, fmt.Sprintf(
			"metadata has %d entries; dropped (the advisory cap is %d entries)",
			len(in), MaxMetadataEntries))
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) > MaxMetadataValueBytes {
			*notes = append(*notes, fmt.Sprintf(
				"metadata entry %q is %d bytes; the whole metadata map was dropped (the advisory per-value cap is %d bytes)",
				k, len(v), MaxMetadataValueBytes))
			return nil
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// clampAllowedTools defensively bounds the advisory `allowed-tools` list to
// MaxAllowedTools entries with each name ≤ MaxAllowedToolNameBytes. The list
// is ADVISORY (never enforced as a gate), but it rides the always-in-context
// SkillMeta, so an unbounded list could inflate every prompt. On count
// overflow the list is truncated to the parsed PREFIX (not dropped — the
// field is a list of names, so the prefix is the honest partial signal) and a
// non-fatal warning note is recorded. An over-long single NAME is truncated
// rune-safe. An empty list stays nil.
func clampAllowedTools(in []string, notes *[]string) []string {
	if len(in) == 0 {
		return nil
	}
	if len(in) > MaxAllowedTools {
		*notes = append(*notes, fmt.Sprintf(
			"allowed-tools has %d entries; truncated to the advisory cap of %d entries",
			len(in), MaxAllowedTools))
		in = in[:MaxAllowedTools]
	}
	out := make([]string, 0, len(in))
	for _, name := range in {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if len(name) > MaxAllowedToolNameBytes {
			*notes = append(*notes, fmt.Sprintf(
				"allowed-tools entry %q is %d bytes; truncated to the advisory cap of %d bytes",
				name, len(name), MaxAllowedToolNameBytes))
			name = TruncateRunes(name, MaxAllowedToolNameBytes)
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
