package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// modelOriginRE matches the provenance line the DirDrafter stamps into every
// quarantined candidate (`origin: model`, optionally quoted). Promote requires it
// so the operator gate only ever promotes artifacts that actually went through the
// Drafter — not an arbitrary directory dropped into the quarantine.
var modelOriginRE = regexp.MustCompile(`(?m)^origin:\s*"?model"?\s*$`)

// ReadCandidate returns the raw SKILL.md bytes of a quarantined candidate, for the
// operator to review before promoting. It validates the name (so a traversal name
// cannot read outside the quarantine) and returns a clear error when the candidate
// is absent.
func ReadCandidate(quarantineDir, name string) ([]byte, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	srcFile := filepath.Join(quarantineDir, name, SkillFileName)
	raw, err := os.ReadFile(srcFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no quarantined skill named %q at %s", name, srcFile)
		}
		return nil, fmt.Errorf("read quarantined skill %q: %w", name, err)
	}
	return raw, nil
}

// Promote moves a quarantined candidate skill <quarantineDir>/<name>/ into the
// operator's active <activeDir>/<name>/, re-running structural validation as
// defense in depth. It is the OPERATOR gate: the only path from model-authored
// quarantine to the live, trusted skill catalog. It requires filesystem access
// the model does not have, and is invoked by the `mecated skills promote`
// subcommand, never by a tool.
//
// It fails (and moves nothing) when:
//   - name is not a valid activation name,
//   - the candidate is missing or unreadable,
//   - the candidate lacks `origin: model` provenance (only model-drafted
//     candidates may be promoted),
//   - the candidate fails structural validation (parseSkill) or the injection
//     scan (so an attacker who somehow planted a quarantine file cannot promote
//     instruction-laden content),
//   - a skill with that name already exists under activeDir (no silent overwrite).
//
// Promote performs no human review itself: the `mecated skills promote` CLI shows
// the operator the full candidate (ReadCandidate) and requires confirmation before
// calling this. The injection scan here is a backstop, not a substitute for a human
// reading the body. The move is a directory rename, so the provenance frontmatter
// (origin: model, drafted_at) is carried over for the audit trail.
func Promote(quarantineDir, activeDir, name string) error {
	if err := validateName(name); err != nil {
		return err
	}

	src := filepath.Join(quarantineDir, name)
	srcFile := filepath.Join(src, SkillFileName)
	raw, err := ReadCandidate(quarantineDir, name)
	if err != nil {
		return err
	}

	// Provenance: only promote artifacts the Drafter actually authored (origin:
	// model). Defense in depth — it does not stop a determined writer who can reach
	// the quarantine from forging the line, but it stops accidental promotion of a
	// directory that never went through the draft path.
	if !modelOriginRE.Match(raw) {
		return fmt.Errorf("refusing to promote %q: missing `origin: model` provenance — only model-drafted candidates may be promoted", name)
	}

	// Re-validate structure and re-scan for injection at the gate.
	sk, reason, _ := ParseSkill(raw, srcFile)
	if reason != "" {
		return fmt.Errorf("refusing to promote %q: %s", name, reason)
	}
	if marker, found := ScanForInjection(sk.Description); found {
		return fmt.Errorf("refusing to promote %q: description contains injection marker %q", name, marker)
	}
	if marker, found := ScanForInjection(sk.Body); found {
		return fmt.Errorf("refusing to promote %q: body contains injection marker %q", name, marker)
	}

	dst := filepath.Join(activeDir, name)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("refusing to promote %q: a skill already exists at %s", name, dst)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat destination %q: %w", dst, err)
	}

	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		return fmt.Errorf("create active skills dir %q: %w", activeDir, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("move %q into active skills dir: %w", name, err)
	}
	return nil
}
