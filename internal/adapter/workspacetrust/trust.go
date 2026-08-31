// Package workspacetrust is the adapter-layer reader/writer for workspace trust
// (Workspace-Trust feature, Phases 1 + 2b). It serves composition from two
// user-global sources under the XDG config dir:
//
//   - Phase 1 (DECLARATIVE, this file): the operator-authored, read-only
//     `trustedWorkspaces:` list in <xdg>/mecatl/settings.yaml. IsDeclared answers
//     "is this workspace declared-trusted?"
//   - Phase 2b (REMEMBERED, registry.go): the machine-written <xdg>/mecatl/trust.yaml
//     registry. Remembered answers "is this workspace remembered-trusted, and has
//     its identity anchor DRIFTED?"; Remember persists an entry; the identity-anchor
//     hash (anchor.go) is what drift is measured against. The registry is a SIBLING
//     of settings.yaml, never inside it (the settings-vs-state split).
//
// # What it is (and is NOT)
//
// `trustedWorkspaces` is config DATA, not a permission Rule. It NEVER produces a
// governance.Rule and NEVER touches the permission evaluator's deny-dominance.
// It only feeds the composition-level trust decision (internal/app), which gates
// whether a project's ALLOW rules and project soul are honoured — exactly the
// same admission gate as the --trust-project flag. Trust here is
// MONOTONIC-POSITIVE: a declared entry can only GRANT trust; it can never
// override a Deny or a configured Ask anywhere (those are honoured regardless).
//
// This is the Phase 1 declarative half of the trust feature. The machine-written
// trust.yaml registry, the interactive prompt, and identity-anchor drift are
// Phase 2 and live elsewhere; this leaf only READS the human-authored
// settings.yaml.
//
// # Path keying (security, MUST-FIX 5.1)
//
// Both declared entries and the workspace under test are keyed by their
// cleaned + realpath'd absolute path (filepath.Abs then filepath.EvalSymlinks),
// so a moved/symlinked path cannot forge or inherit another workspace's trust.
// A path that cannot be resolved (e.g. a broken symlink, or a declared entry
// pointing at a nonexistent dir) is treated as UNTRUSTED / non-matching
// (fail-safe). A malformed entry is ignored; the rest of the list is honoured.
//
// # Layering
//
// workspacetrust is an adapter LEAF: stdlib + the shared xdgconfig env seam + the
// YAML parser. It imports no domain package (session, prompt, governance, tool),
// and no domain package imports it. It mirrors permconfig/soul. The TRUST
// DECISION that folds this together with the --trust-project flag is composition
// (internal/app), not here.
package workspacetrust

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	yaml "github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

// userSubdirMecatl is the user-level YAML config under the XDG config dir,
// i.e. <config>/mecatl/settings.yaml. It mirrors permconfig.userSubdirMecatl: the
// declarative trust list lives in the SAME human-authored, read-only file as the
// user-scoped permission config, so an operator declares both in one place.
const userSubdirMecatl = "mecatl/settings.yaml"

// maxConfigBytes caps the settings.yaml read (mirrors permconfig's byte cap): a
// pathological file never grants trust and never blows memory. An oversized file
// is ignored fail-safe (no declared trust).
const maxConfigBytes = 1 << 20 // 1 MiB

// schema is the SUBSET of <xdg>/mecatl/settings.yaml this adapter cares about:
// the top-level `trustedWorkspaces:` list. The permission keys are parsed
// separately by permconfig against the SAME file; YAML ignores keys a struct
// does not declare, so the two readers coexist without interfering.
type schema struct {
	// TrustedWorkspaces is the operator-authored list of absolute workspace
	// paths to trust without the --trust-project flag. Entries are
	// realpath-keyed at lookup time (see Reader.IsDeclared).
	TrustedWorkspaces []string `yaml:"trustedWorkspaces"`
}

// Reader resolves workspace trust from the user-global config dir: the DECLARATIVE
// `trustedWorkspaces:` list in settings.yaml (Phase 1, IsDeclared) AND the
// MACHINE-WRITTEN trust.yaml registry (Phase 2b, Remembered/Remember).
//
// Construct it with New (real env + real registry write seam) or NewWithEnv (faked
// env + the real write seam) or NewWithEnvIO (faked env + an injected write seam, so
// the registry write runs fully offline in tests). A Reader built without a write
// seam still serves every READ path; only Remember requires the seam.
type Reader struct {
	env       xdgconfig.ResolveEnv
	writeFile registryWriteFunc
	// diag is the operational-logging sink for the fail-safe Warn lines (unresolvable
	// realpath, oversized/unparseable config, unknown schema version). Never nil after
	// a constructor (defaulted to port.NopDiagnostics), so a read path never
	// nil-panics. The composition layer injects the shared sink via WithDiagnostics.
	diag port.Diagnostics
}

// New binds a Reader to the real process environment + filesystem, with the real
// O_NOFOLLOW/0o600/temp-rename registry write seam.
func New() *Reader {
	return &Reader{env: xdgconfig.OSEnv, writeFile: osRegistryWrite, diag: port.NopDiagnostics{}}
}

// WithDiagnostics injects the operational-logging sink the Reader writes its
// fail-safe Warn lines through and returns the receiver for fluent wiring. A nil
// sink is ignored (the NopDiagnostics default stands), so the Reader never
// nil-panics. The composition layer calls this immediately after a constructor.
func (r *Reader) WithDiagnostics(d port.Diagnostics) *Reader {
	if r != nil && d != nil {
		r.diag = d
	}
	return r
}

// NewWithEnv binds a Reader to an injected env so the XDG/home resolution and the
// file read run fully offline against a fake — never the developer's real
// ~/.config. It uses the REAL registry write seam (so a test that writes a registry
// against a faked XDG dir exercises the actual O_NOFOLLOW/temp-rename path). Tests
// that need to SPY on or REJECT the write inject their own seam via NewWithEnvIO.
func NewWithEnv(env xdgconfig.ResolveEnv) *Reader {
	return &Reader{env: env, writeFile: osRegistryWrite, diag: port.NopDiagnostics{}}
}

// NewWithEnvIO binds a Reader to an injected env AND an injected registry write
// seam, so the write is fully observable/controllable offline (e.g. a spy that
// records writes to assert "mecated never writes", or a stub that always errors). A
// nil writeFile leaves the Reader read-only (Remember then errors).
func NewWithEnvIO(env xdgconfig.ResolveEnv, writeFile registryWriteFunc) *Reader {
	return &Reader{env: env, writeFile: writeFile, diag: port.NopDiagnostics{}}
}

// IsDeclared reports whether workspace is in the operator-authored
// `trustedWorkspaces:` list, comparing on the cleaned + realpath'd absolute path
// of BOTH sides (MUST-FIX 5.1). It returns false (fail-safe) when:
//   - workspace is empty, or cannot be resolved to a realpath (broken symlink);
//   - the settings.yaml is absent, unreadable, oversized, or unparseable;
//   - no declared entry's realpath matches the workspace's realpath.
//
// A single malformed/unresolvable entry is skipped; the rest of the list is
// still honoured. This NEVER errors out of band: a corrupt config simply yields
// "not declared" — trust is monotonic-positive, so the absence of a grant is the
// safe default.
func (r *Reader) IsDeclared(workspace string) bool {
	if r == nil || workspace == "" {
		return false
	}
	want, err := realpath(workspace)
	if err != nil {
		// The workspace under test cannot be resolved (e.g. broken symlink): it
		// cannot match a declared realpath. Fail-safe: untrusted.
		r.diag.Log(context.Background(), port.LevelWarn, "workspace trust: cannot resolve workspace realpath; treating as not-declared",
			"workspace", workspace, "err", err)
		return false
	}

	for _, entry := range r.declaredEntries() {
		got, derr := realpath(entry)
		if derr != nil {
			// A declared entry that does not resolve (typo, moved dir, broken
			// symlink) is ignored; the rest of the list is honoured.
			r.diag.Log(context.Background(), port.LevelWarn, "workspace trust: declared trustedWorkspaces entry does not resolve; ignoring it",
				"entry", entry, "err", derr)
			continue
		}
		if got == want {
			return true
		}
	}
	return false
}

// declaredEntries reads and parses the `trustedWorkspaces:` list from the
// user-global settings.yaml. It returns nil (no declared trust) on any read or
// parse failure — fail-safe, logged at Warn, never an error that aborts startup.
func (r *Reader) declaredEntries() []string {
	cfgDir := xdgconfig.UserConfigDir(r.env)
	if cfgDir == "" {
		return nil
	}
	path := filepath.Join(cfgDir, userSubdirMecatl)
	data, err := r.env.ReadFile(path)
	if err != nil {
		// Absent/unreadable settings.yaml ⇒ no declared trust. Not logged at Warn
		// (a missing user settings file is the common, expected case).
		return nil
	}
	if len(data) > maxConfigBytes {
		r.diag.Log(context.Background(), port.LevelWarn, "workspace trust: user settings.yaml exceeds size cap; ignoring trustedWorkspaces",
			"file", path, "bytes", len(data), "cap", maxConfigBytes)
		return nil
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var s schema
	if perr := yaml.Unmarshal(data, &s); perr != nil {
		yamlDiagnostic := yamldiag.Classify("parse workspace trust settings", perr)
		args := []any{"file", path, "yaml_operation", yamlDiagnostic.Operation, "yaml_category", yamlDiagnostic.Category}
		if yamlDiagnostic.HasLocation {
			args = append(args, "yaml_line", yamlDiagnostic.Line, "yaml_column", yamlDiagnostic.Column)
		}
		r.diag.Log(context.Background(), port.LevelWarn, "workspace trust: user settings.yaml unparseable; ignoring trustedWorkspaces", args...)
		return nil
	}
	// Drop empty/blank entries (a malformed list item is ignored, the rest kept).
	out := make([]string, 0, len(s.TrustedWorkspaces))
	for _, e := range s.TrustedWorkspaces {
		if strings.TrimSpace(e) != "" {
			out = append(out, e)
		}
	}
	return out
}

// realpath returns the cleaned, absolute, symlink-resolved form of p — the trust
// key. filepath.Abs cleans + absolutizes against the cwd; filepath.EvalSymlinks
// then resolves every symlink component to its real target. The composition of
// the two is what defeats a moved-symlink swap (MUST-FIX 5.1): two distinct
// aliases of the same real directory key identically, and a declared path whose
// symlink is repointed no longer matches its old target. An unresolvable path
// (broken link, nonexistent) is an error the caller treats as untrusted.
func realpath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("absolutize %q: %w", p, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks for %q: %w", abs, err)
	}
	return resolved, nil
}
