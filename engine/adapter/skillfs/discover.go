package skillfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

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

// frontmatter is the parsed YAML header of a SKILL.md file. Only name and
// description are part of the always-in-context metadata; any other keys are
// ignored so the format can grow without breaking discovery.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

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
// A skill whose frontmatter `name` disagrees with its directory name is accepted
// using the FRONTMATTER name (the frontmatter is the source of truth for the
// activation key); duplicate effective names WITHIN this directory are resolved by
// keeping the first in sorted-path order and skipping the rest (reported as a
// SkipError). Cross-source collisions are resolved one level up, by MultiSource.
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
		return Skill{}, fmt.Sprintf("malformed YAML frontmatter: %v", err), nil
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return Skill{}, "frontmatter is missing a non-empty \"name\"", nil
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

	return Skill{
		Name:        name,
		Description: desc,
		Body:        trimmedBody,
		Path:        path,
	}, "", notes
}
