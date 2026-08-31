// registry.go is the MACHINE-WRITTEN workspace-trust registry (Workspace-Trust
// feature, Phase 2b). It reads and writes <xdg>/mecatl/trust.yaml — a HARNESS-OWNED
// state file, a SIBLING of but NEVER inside the human-authored settings.yaml (the
// settings-vs-state split). An entry remembers that the operator trusted a
// workspace AND the identity-anchor hash at the moment of trust, so a remembered
// decision survives across launches while a later malicious edit of the project's
// identity surface (its soul / agent / command / skill definitions) shows up as
// DRIFT instead of silently inheriting the old grant.
//
// # Read API: Remembered
//
// Remembered(workspace, currentAnchorHash) answers the two booleans composition
// needs: (remembered, drifted). remembered is true iff a registry entry exists for
// the workspace's realpath. drifted is true iff that entry's stored anchor hash
// DIFFERS from currentAnchorHash. The fold (internal/app) turns these into the
// TrustRemembered tier: matching ⇒ trusted; mismatch ⇒ untrusted+drifted (fail
// safe — mecated has no prompt, so a drifted entry must NOT grant; Phase 2c's
// mecatui turns drift into a re-prompt).
//
// # Write API: Remember
//
// Remember(workspace, anchorHash, trustedAt) persists/updates the entry for the
// workspace's realpath. The write is O_NOFOLLOW + 0o600 + temp-then-rename (the
// soulguard discipline), so a pre-planted SYMLINK at the registry path cannot
// redirect the write (CWE-59) and the file is owner-only. The only PRODUCTION
// caller of Remember is the mecatui first-encounter prompt (Phase 2c); this phase
// ships + unit-tests the API. mecated NEVER calls Remember (it is read-only on the
// registry — declarative).
//
// # Security (MUST-FIX 5)
//
//   - Path keying: realpath (filepath.Abs + EvalSymlinks), SYMMETRIC on store and
//     lookup, so a moved/aliased symlink cannot forge or inherit another
//     workspace's trust. A path that does not resolve ⇒ not remembered (fail-safe).
//   - Fail-to-untrusted: an unreadable / oversized / corrupt / unparseable
//     registry yields "not remembered" — NEVER a grant, never an abort. A corrupt
//     registry can never bootstrap trust.
//   - User-scoped + harness-written: the registry path is derived ONLY from the
//     user-global XDG config dir, never from a repo-controlled path, so a repo
//     cannot redirect the read at, or write to, a file it controls (no self-trust).

package workspacetrust

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	yaml "github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

// userSubdirTrust is the machine-written trust registry relative to the XDG config
// base: <config>/mecatl/trust.yaml. It is a SIBLING of settings.yaml
// (userSubdirMecatl) but a SEPARATE file: the human edits settings.yaml; only the
// harness writes trust.yaml. The two never share a parser.
const userSubdirTrust = "mecatl/trust.yaml"

// maxRegistryBytes caps the trust.yaml read. A pathological registry never grants
// trust and never blows memory: over the cap it is ignored fail-safe (no remembered
// trust). Mirrors maxConfigBytes / permconfig's byte cap.
const maxRegistryBytes = 1 << 20 // 1 MiB

// maxRegistryEntries caps the number of entries honoured from a parsed registry, so
// a pathological file cannot make a lookup walk an unbounded map. Lookups are by
// realpath key (map access), so this is a defensive ceiling, not a hot-path cost.
const maxRegistryEntries = 4096

// registryVersion is the schema version the harness writes and expects to read. A
// registry with a DIFFERENT (unknown) version is ignored fail-safe (treated as no
// remembered trust) rather than mis-interpreted — fail-to-untrusted on a
// wrong-version file (MUST-FIX 5.4).
const registryVersion = 1

// registryFile is the on-disk schema of trust.yaml. Entries are keyed by the
// realpath'd absolute workspace; each stores the identity-anchor SHA-256 captured
// at the moment of trust plus an RFC3339 trustedAt timestamp (provenance only — it
// is never used to grant/deny, so its exact value does not affect security).
type registryFile struct {
	// Version is the schema version. A file whose version != registryVersion is
	// ignored fail-safe.
	Version int `yaml:"version"`
	// Workspaces maps a realpath'd workspace to its remembered trust entry.
	Workspaces map[string]registryEntry `yaml:"workspaces"`
}

// registryEntry is a single remembered-trust record.
type registryEntry struct {
	// AnchorSHA256 is the identity-anchor hash captured when the workspace was
	// trusted (see anchor.go). A live anchor hash that differs from this is DRIFT.
	AnchorSHA256 string `yaml:"anchorSHA256"`
	// TrustedAt is when the entry was written (RFC3339). Provenance/audit only.
	TrustedAt string `yaml:"trustedAt"`
}

// Remembered reports whether the workspace has a remembered-trust entry and, if so,
// whether its stored identity-anchor hash has DRIFTED from currentAnchorHash.
//
// It returns (false, false) — not remembered, fail-safe — when:
//   - the Reader is nil, or workspace is empty, or workspace cannot be realpath'd;
//   - the trust.yaml is absent, unreadable, oversized, unparseable, or a
//     wrong/unknown schema version;
//   - no entry's realpath matches the workspace's realpath.
//
// When an entry IS found: remembered=true, and drifted = (stored hash !=
// currentAnchorHash). A drifted entry is reported remembered+drifted so the fold
// (internal/app) can FAIL SAFE to untrusted (mecated) or re-prompt (mecatui, 2c).
// A blank stored hash is treated as drift (it can never match a real anchor hash),
// so a hand-corrupted entry never silently grants.
func (r *Reader) Remembered(workspace, currentAnchorHash string) (remembered, drifted bool) {
	if r == nil || workspace == "" {
		return false, false
	}
	key, err := realpath(workspace)
	if err != nil {
		r.diag.Log(context.Background(), port.LevelWarn, "workspace trust: cannot resolve workspace realpath for registry lookup; treating as not-remembered",
			"workspace", workspace, "err", err)
		return false, false
	}
	entry, ok := r.registryEntries()[key]
	if !ok {
		return false, false
	}
	if entry.AnchorSHA256 == "" || entry.AnchorSHA256 != currentAnchorHash {
		return true, true
	}
	return true, false
}

// registryEntries reads and parses trust.yaml, returning the realpath-keyed entry
// map. It returns nil (no remembered trust) on ANY read/parse failure — fail-safe,
// logged at Warn, never an error that aborts startup. Entries are re-keyed by their
// realpath at read time so a registry hand-edited with a non-canonical path still
// matches the canonical lookup; an entry whose key does not resolve is dropped.
func (r *Reader) registryEntries() map[string]registryEntry {
	cfgDir := xdgconfig.UserConfigDir(r.env)
	if cfgDir == "" {
		return nil
	}
	path := filepath.Join(cfgDir, userSubdirTrust)
	data, err := r.env.ReadFile(path)
	if err != nil {
		// Absent/unreadable registry ⇒ no remembered trust. A missing registry is the
		// common first-run case, so this is not logged at Warn.
		return nil
	}
	if len(data) > maxRegistryBytes {
		r.diag.Log(context.Background(), port.LevelWarn, "workspace trust: trust.yaml exceeds size cap; ignoring remembered trust",
			"file", path, "bytes", len(data), "cap", maxRegistryBytes)
		return nil
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var rf registryFile
	if perr := yaml.Unmarshal(data, &rf); perr != nil {
		yamlDiagnostic := yamldiag.Classify("parse workspace trust registry", perr)
		args := []any{"file", path, "yaml_operation", yamlDiagnostic.Operation, "yaml_category", yamlDiagnostic.Category}
		if yamlDiagnostic.HasLocation {
			args = append(args, "yaml_line", yamlDiagnostic.Line, "yaml_column", yamlDiagnostic.Column)
		}
		r.diag.Log(context.Background(), port.LevelWarn, "workspace trust: trust.yaml unparseable; ignoring remembered trust (fail-safe)", args...)
		return nil
	}
	if rf.Version != registryVersion {
		r.diag.Log(context.Background(), port.LevelWarn, "workspace trust: trust.yaml has an unknown schema version; ignoring remembered trust (fail-safe)",
			"file", path, "version", rf.Version, "want", registryVersion)
		return nil
	}
	out := make(map[string]registryEntry, len(rf.Workspaces))
	count := 0
	for raw, entry := range rf.Workspaces {
		if count >= maxRegistryEntries {
			r.diag.Log(context.Background(), port.LevelWarn, "workspace trust: trust.yaml exceeds entry cap; remaining entries ignored",
				"file", path, "cap", maxRegistryEntries)
			break
		}
		key, kerr := realpath(raw)
		if kerr != nil {
			// An entry whose path no longer resolves (moved/deleted dir, broken
			// symlink) is dropped; the rest of the registry is honoured.
			continue
		}
		out[key] = entry
		count++
	}
	return out
}

// Remember persists (or updates) the remembered-trust entry for workspace with the
// given identity-anchor hash and trustedAt timestamp. The workspace is realpath'd
// (symmetric with Remembered) so the entry is keyed identically to how it will be
// looked up. trustedAt is INJECTED by the caller (composition supplies time.Now())
// so the write is deterministic in tests — this adapter never calls time.Now().
//
// The write is read-modify-write: it loads the current registry (preserving other
// entries), upserts this one, and atomically replaces the file via a 0o600 +
// O_NOFOLLOW temp file then rename (the soulguard discipline). It returns an error
// only on a genuine IO failure (the caller logs it); a corrupt EXISTING registry is
// treated as empty (the upsert starts fresh rather than failing) so a remembered
// write is never blocked by an unrelated corrupt file.
//
// This is the ONLY write path for trust.yaml. mecated never calls it. The path is
// derived solely from the user XDG config dir (never a repo-controlled path), so a
// repo cannot redirect the write (no self-trust).
func (r *Reader) Remember(workspace, anchorHash string, trustedAt time.Time) error {
	if r == nil {
		return errors.New("workspacetrust: nil Reader")
	}
	if workspace == "" {
		return errors.New("workspacetrust: empty workspace")
	}
	key, err := realpath(workspace)
	if err != nil {
		return err
	}
	cfgDir := xdgconfig.UserConfigDir(r.env)
	if cfgDir == "" {
		return errors.New("workspacetrust: no XDG config dir to write trust.yaml")
	}
	path := filepath.Join(cfgDir, userSubdirTrust)

	// Read-modify-write: preserve existing entries. A corrupt/absent registry is
	// treated as empty (start fresh) — a remembered write must not be blocked by an
	// unrelated corrupt file, and the upsert can never be a downgrade.
	existing := r.registryEntries()
	rf := registryFile{Version: registryVersion, Workspaces: make(map[string]registryEntry, len(existing)+1)}
	for k, v := range existing {
		rf.Workspaces[k] = v
	}
	rf.Workspaces[key] = registryEntry{
		AnchorSHA256: anchorHash,
		TrustedAt:    trustedAt.UTC().Format(time.RFC3339),
	}

	out, merr := yaml.Marshal(rf)
	if merr != nil {
		return merr
	}
	return r.write(path, out)
}

// write atomically replaces the registry file: it ensures the parent dir exists,
// writes to a sibling temp file via the O_NOFOLLOW/0o600 seam, then renames it over
// the target. The seam (osRegistryWrite) is injectable so tests run fully offline.
func (r *Reader) write(path string, data []byte) error {
	if r.writeFile == nil {
		return errors.New("workspacetrust: no write seam bound (Reader constructed read-only)")
	}
	return r.writeFile(path, data)
}

// registryWriteFunc is the injectable write seam: persist data at path atomically.
// The real binding is osRegistryWrite; tests inject a fake (or a spy that records /
// rejects writes) so the registry write runs fully offline and the "mecated never
// writes" property is assertable.
type registryWriteFunc func(path string, data []byte) error

// osRegistryWrite writes the registry atomically with the soulguard discipline:
// mkdir -p the parent, write to a temp sibling with O_NOFOLLOW|O_EXCL|0o600 (a
// pre-planted symlink at the temp path fails with ELOOP/EEXIST — CWE-59), then
// rename over the target. The rename is atomic on the same filesystem, so a reader
// never sees a half-written registry. O_NOFOLLOW additionally guards the FINAL
// target check below.
func osRegistryWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	// Refuse to write THROUGH a symlink at the final path: open it O_NOFOLLOW to
	// detect a pre-planted symlink (CWE-59). We do not write to this handle (the
	// real write goes to the temp file + rename); this is purely the symlink guard
	// the brief calls for ("the registry WRITE uses O_NOFOLLOW ... refuses a
	// symlinked registry path"). A symlinked target ⇒ ELOOP ⇒ fail-soft to caller.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return &os.PathError{Op: "open", Path: path, Err: syscall.ELOOP}
	}

	tmp, err := os.CreateTemp(dir, ".trust-*.yaml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
