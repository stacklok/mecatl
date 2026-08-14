package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/skillvalidation"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// QuarantineSubdir is the conventional name of the quarantine directory the
// DirDrafter writes candidate skills into when the composition root derives the
// quarantine location from a base directory. The quarantine dir is NEVER served
// to the live catalog (see the trust boundary in doc.go); it is only read by the
// novelty check and the promote CLI.
const QuarantineSubdir = "skills-quarantine"

// DefaultSimilarityThreshold is the default 2-gram Jaccard similarity above which
// a candidate's description is flagged as a near-duplicate of an existing skill.
// It WARNS (returned in DraftResult.SimilarSkills); it never blocks — the model
// decides whether to refine or proceed.
const DefaultSimilarityThreshold = 0.5

// maxDescriptionBytes is the package-internal alias of the graduated
// MaxDescriptionBytes (skillfs.MaxDescriptionBytes), retained so the writable
// half's existing lowercase references compile unchanged against the alias.
const maxDescriptionBytes = MaxDescriptionBytes

// maxBodyBytes caps a drafted skill's body. It mirrors
// engine/adapter/skillfs.MaxOutputBytes (== toolkit.MaxOutputBytes) — the draft
// body shares the adapter-layer output cap; a root-local const because skillfs
// does not export a draft-specific cap.
const maxBodyBytes = 25_000

// DraftRequest is one candidate skill the model proposes. It is UNTRUSTED model
// output: every field is sanitized and validated by the Drafter before any byte
// touches disk.
type DraftRequest struct {
	// Name is the proposed activation name (frontmatter `name`).
	Name string
	// Description is the one-line, always-in-context metadata (frontmatter
	// `description`). It is scanned for injection markers because, if the skill
	// were ever promoted, it would join the always-in-context layer.
	Description string
	// Body is the markdown instructions (loaded on activation).
	Body string
}

// DraftResult reports the outcome of a draft attempt for the model to read back.
type DraftResult struct {
	// Path is where the candidate was written (under the quarantine dir), or ""
	// when nothing was written (a validation/sanitization failure).
	Path string
	// SimilarSkills lists existing skill names whose description is a near-duplicate
	// of the candidate's (at/above the similarity threshold). It is a warning, not
	// a block.
	SimilarSkills []string
	// Warnings holds non-fatal notes (e.g. description truncation, similarity over
	// the threshold) the tool surfaces to the model.
	Warnings []string
}

// Drafter is the WRITE seam: it accepts an untrusted candidate skill, validates
// and sanitizes it, checks it for novelty against the existing skills, and on
// success persists it to a QUARANTINE location that is NOT part of any catalog-
// registered Source. It NEVER writes into a directory that DirSource serves to a
// live Skill tool. Construction binds it to (a) the quarantine dir and (b) a
// read-only snapshot of the currently active skills for the novelty check.
//
// LAYERING: this is an adapter-package seam, the write-side mirror of Source (also
// an adapter-package seam, not a domain port). The domain never imports it; the
// SkillDraft tool depends on it by constructor injection, the same shape by which
// the read-only Skill tool depends on Source.
type Drafter interface {
	// Draft validates req, runs the novelty check, and on success writes the
	// candidate SKILL.md under the quarantine dir. A validation/sanitization
	// failure returns a non-nil error whose message is model-addressable (the
	// SkillDraft tool turns it into a NewToolError, never a harness fault) and
	// DraftResult.Path is "" (no file written).
	Draft(ctx context.Context, req DraftRequest) (DraftResult, error)
}

// DraftOption configures a DirDrafter.
type DraftOption func(*DirDrafter)

// WithSimilarityThreshold overrides the 2-gram Jaccard threshold above which the
// novelty check flags a near-duplicate description. Values outside (0,1] are
// ignored (the default stands).
func WithSimilarityThreshold(t float64) DraftOption {
	return func(d *DirDrafter) {
		if t > 0 && t <= 1 {
			d.threshold = t
		}
	}
}

// WithClock injects a deterministic time source for the `drafted_at` provenance
// stamp (tests pass a fixed clock). The default is time.Now.
func WithClock(now func() time.Time) DraftOption {
	return func(d *DirDrafter) {
		if now != nil {
			d.now = now
		}
	}
}

// WithNeutralValidation controls the shared logical skill validator. Validation
// is enabled by default; false is the explicit compatibility escape hatch for a
// host that has not yet migrated its legacy quarantine policy.
func WithNeutralValidation(enabled bool) DraftOption {
	return func(d *DirDrafter) { d.validate = enabled }
}

// DirDrafter is the local-filesystem Drafter. It writes candidates as
// <QuarantineDir>/<name>/SKILL.md with a provenance-stamped frontmatter, reusing
// parseSkill/validateName for validation and a snapshot of the active skill
// descriptions (taken at construction) for the offline novelty check.
type DirDrafter struct {
	quarantineDir string
	existing      []Skill // snapshot of active skills, for the novelty check (read-only)
	threshold     float64
	now           func() time.Time
	validate      bool
}

// Compile-time assertion that *DirDrafter implements Drafter.
var _ Drafter = (*DirDrafter)(nil)

// NewDirDrafter builds a DirDrafter that writes quarantined candidates under
// quarantineDir and runs the novelty check against existing (a snapshot of the
// currently active skills). quarantineDir must be a directory the model has NO
// other write path into (the composition root enforces this with a governance
// deny rule on Write/Edit and asserts the dir is disjoint from every active
// skills dir).
func NewDirDrafter(quarantineDir string, existing []Skill, opts ...DraftOption) *DirDrafter {
	d := &DirDrafter{
		quarantineDir: quarantineDir,
		existing:      append([]Skill(nil), existing...),
		threshold:     DefaultSimilarityThreshold,
		now:           time.Now,
		validate:      true,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Draft validates, sanitizes, novelty-checks, and atomically writes the candidate.
// On any validation/sanitization failure it returns ("", err) with NO file
// written. The novelty check never blocks: a near-duplicate is reported in
// DraftResult.SimilarSkills/Warnings and the candidate is still written.
func (d *DirDrafter) Draft(ctx context.Context, req DraftRequest) (DraftResult, error) {
	// Honor cancellation before doing any work; the local write itself is fast and
	// atomic (temp+rename), so there is no mid-write state to unwind past this point.
	if err := ctx.Err(); err != nil {
		return DraftResult{}, err
	}
	name := strings.TrimSpace(req.Name)
	if err := validateName(name); err != nil {
		return DraftResult{}, err
	}

	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		return DraftResult{}, fmt.Errorf("description is required (the one-line, always-in-context summary of when to use this skill)")
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return DraftResult{}, fmt.Errorf("body is required (the numbered procedure steps and a \"Done when:\" line)")
	}

	// Injection scan runs on BOTH the description (always-in-context surface if
	// ever promoted) and the body. A hit is fatal and model-addressable.
	if marker, found := ScanForInjection(desc); found {
		return DraftResult{}, fmt.Errorf("description contains a disallowed instruction-injection marker %q; a skill must be procedural how-to, not instructions that override the agent", marker)
	}
	if marker, found := ScanForInjection(body); found {
		return DraftResult{}, fmt.Errorf("body contains a disallowed instruction-injection marker %q; a skill must be procedural how-to, not instructions that override the agent", marker)
	}

	var warnings []string

	// Size: cap the description at the always-in-context cap (rune-safe), and flag
	// an oversized body (it is truncated on activation by the Skill tool).
	if len(desc) > maxDescriptionBytes {
		warnings = append(warnings, fmt.Sprintf(
			"description is %d bytes; it was truncated to the always-in-context cap of %d bytes",
			len(desc), maxDescriptionBytes))
		desc = toolkit.TruncateRunes(desc, maxDescriptionBytes)
	}
	if len(body) > maxBodyBytes {
		warnings = append(warnings, fmt.Sprintf(
			"body is %d bytes; it exceeds %d bytes and will be truncated when the skill is activated — keep it concise and reference detail with a \"For detail, Read: <skill-dir>/REFERENCE.md\" line",
			len(body), maxBodyBytes))
	}

	if d.validate {
		inventory := make([]learning.SkillInventoryItem, 0, len(d.existing))
		for _, existing := range d.existing {
			inventory = append(inventory, learning.SkillInventoryItem{
				Name: existing.Name,
				Bundle: learning.SkillBundle{
					Name: existing.Name, Description: existing.Description, Body: existing.Body,
				},
			})
		}
		_, err := (skillvalidation.Validator{}).Validate(ctx, learning.SkillValidationRequest{
			Partition:  learning.SkillPartition{Principal: "legacy-skilldraft"},
			OwnerAgent: "legacy-skilldraft",
			Bundle:     learning.SkillBundle{Name: name, Description: desc, Body: body},
			Provenance: learning.SkillProvenance{Origin: learning.SkillProvenanceLegacyModel},
			Inventory:  inventory,
		})
		if err != nil {
			return DraftResult{}, fmt.Errorf("skill validation failed: %w", err)
		}
	}

	// Novelty check (warn-only).
	similar := d.similarNames(desc)
	if len(similar) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"similar skills already exist: %s — consider refining the description or skipping this draft",
			strings.Join(similar, ", ")))
	}

	// Path containment: defense in depth even though name is regex-restricted.
	skillDir := filepath.Join(d.quarantineDir, name)
	cleanQuar := filepath.Clean(d.quarantineDir)
	if rel, err := filepath.Rel(cleanQuar, filepath.Clean(skillDir)); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return DraftResult{}, fmt.Errorf("invalid skill name %q: resolved path escapes the quarantine directory", name)
	}

	path := filepath.Join(skillDir, SkillFileName)
	content := d.render(name, desc, body)
	if err := writeSkillFile(skillDir, path, content); err != nil {
		// A write fault is model-addressable (the model can retry) and leaves no
		// partial file (atomic temp+rename).
		return DraftResult{}, fmt.Errorf("could not write quarantined skill %q: %v", name, err)
	}

	return DraftResult{Path: path, SimilarSkills: similar, Warnings: warnings}, nil
}

// similarNames returns the names of existing skills whose description is at or
// above the similarity threshold against desc, sorted for deterministic output.
func (d *DirDrafter) similarNames(desc string) []string {
	var out []string
	for _, s := range d.existing {
		if Jaccard2Gram(desc, s.Description) >= d.threshold {
			out = append(out, s.Name)
		}
	}
	sortStrings(out)
	return out
}

// render builds the provenance-stamped SKILL.md content. The provenance keys
// (origin/drafted_at) are forward-compatible: parseSkill's frontmatter struct
// ignores unknown keys, so they never reach the always-in-context layer (only
// name/description do).
func (d *DirDrafter) render(name, desc, body string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", yamlScalar(name))
	fmt.Fprintf(&b, "description: %s\n", yamlScalar(desc))
	// Provenance: origin is ALWAYS "model" for a drafted skill (never "operator").
	// The operator promote gate requires this line. drafted_at gives the reviewer a
	// timestamp; parseSkill ignores both keys, so they never reach the always-in-
	// context layer.
	b.WriteString("origin: model\n")
	fmt.Fprintf(&b, "drafted_at: %s\n", d.now().UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// validateName enforces the activation-name policy: lowercase Agent-Skills style,
// blocking whitespace, control chars, uppercase, and path-traversal name attacks.
// It routes through the ONE shared validator (skillfs.ValidSkillName, re-exported
// via the alias) so the draft write path and the discovery read path enforce the
// exact same grammar — a single source of truth, not two regexes to drift.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required (a short, lowercase activation name, e.g. \"deploy-to-staging\")")
	}
	if !ValidSkillName(name) {
		return fmt.Errorf("invalid name %q: must be 1-64 chars, lowercase letters/digits/'-'/'_', starting with a letter or digit (no spaces, no uppercase, no path separators)", name)
	}
	return nil
}

// writeSkillFile atomically writes content to path under skillDir, creating
// skillDir. It writes to a temp file in the same directory and renames it into
// place so a crash never leaves a half-written SKILL.md (mirroring memory.save).
// The DirDrafter is a shared singleton and sessions run concurrently, but the
// random temp suffix (os.CreateTemp) + idempotent MkdirAll + atomic os.Rename make
// concurrent same-name drafts last-writer-wins with no corruption or partial files;
// do not replace this with a non-atomic write.
func writeSkillFile(skillDir, path, content string) error {
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp, err := os.CreateTemp(skillDir, ".skill-*.md.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp into place: %w", err)
	}
	return nil
}

// yamlScalar renders s as a safe single-line YAML scalar by double-quoting and
// escaping. The injection scan already rejected role-override markers; quoting
// guarantees the value stays one scalar even if it contains YAML-significant
// characters (colons, '#', leading '-').
func yamlScalar(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}
