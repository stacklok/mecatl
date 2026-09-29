// Package soul implements issue #14, Phase 1: a user-scoped, agent-READ-ONLY
// persona/"soul" fragment loaded into the turn-0 conversation as data.
//
// SCOPING: the soul is USER-scoped, not project-scoped. It lives at
// $XDG_CONFIG_HOME/mecatl/soul.md (fallback ~/.config/mecatl/soul.md) — OUTSIDE
// any session workspace root. It is therefore resolved against the process
// environment (the shared xdgconfig.ResolveEnv for path resolution, plus a local
// bounded-read seam), NOT through the per-session WorkspaceReader (which is rooted
// at the session workspace).
//
// SECURITY / "no write path" invariant: the agent loop can NEVER mutate the soul.
// Every method on Store is READ-ONLY (Load / LoadWithMeta read+validate the body;
// ResolvedPath only computes a path string), and this package contains NO
// os.WriteFile/Create/MkdirAll and NO Catalog/tool registration. The drift-baseline
// fingerprint (issue #14, Phase 3) is COMPUTED here (LoadWithMeta) but PERSISTED only
// by the composition layer (internal/app/soulguard) — the adapter never writes.
// A writable identity anchor is
// the central trap the spike (docs/adr/0011-soul-and-user-model.md §4) warns against: a
// prompt injection that rewrites "who the agent is" would persist across every
// future session. So identity is read-only-if-present and bootstrapped by hand
// (a text editor), never by a tool. The soul is additionally injection-scanned
// (reusing skills.ScanForInjection) and byte-capped at load; any hit, an empty/
// whitespace-only/oversized/unreadable file all degrade to "" (fail-soft, no
// fragment) — never an error that aborts a run.
//
// BOUNDED READ (CWE-789): the file read is bounded to MaxBytes+1 bytes so a
// --soul-file pointed at /dev/zero or a multi-GB file cannot allocate unbounded at
// load time, and the cap is measured on RAW bytes read (before TrimSpace), so the
// "20 KiB byte cap" actually bounds the file, not just the trimmed string.
//
// Unlike memory.Store there is NO flock here: this is a read-only loader (no RMW,
// no temp-file rename), so there is no cross-process write to serialise. A
// concurrent hand-edit is harmless — the next Load re-reads the file.
package soul

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/hashutil"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adaptersupport/soulbody"
)

// DefaultMaxBytes is the load-time byte ceiling on the soul body (matching the
// Hermes 20 KiB cap the spike cites). Over the cap the soul is REJECTED (no
// fragment), not truncated: a half-truncated persona is worse than none.
const DefaultMaxBytes = soulbody.DefaultMaxBytes

// soulSubpath is the conventional soul file relative to the XDG config base, i.e.
// <config>/mecatl/soul.md (fallback ~/.config/mecatl/soul.md). Mirrors
// permconfig.userSubdirMecatl / skills.userSubdirMecatl path conventions.
const soulSubpath = "mecatl/soul.md"

// readFunc opens a soul file for a bounded read and returns its content limited to
// at most limit bytes (the caller passes MaxBytes+1 to detect an over-cap file
// without reading the whole thing). The real binding (osRead) streams through an
// io.LimitReader so a pathological file cannot allocate unbounded; tests inject a
// fake that honours the same limit. A missing/unreadable file returns an error.
type readFunc func(path string, limit int64) ([]byte, error)

// osRead is the real bounded read: open the file and io.ReadAll a LimitReader so at
// most limit bytes are ever buffered.
func osRead(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is operator-supplied (--soul-file) or the conventional user-config location; this is a read-only loader.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only handle; close error is irrelevant
	return io.ReadAll(io.LimitReader(f, limit))
}

// Options configures a Store.
type Options struct {
	// Path is an explicit soul file path (e.g. from --soul-file). When empty the
	// store resolves the conventional <xdg>/mecatl/soul.md (fallback
	// ~/.config/mecatl/soul.md). An explicit path overrides the conventional one.
	Path string
	// MaxBytes caps the loaded body; 0 uses DefaultMaxBytes. A body over the cap is
	// rejected (no fragment), not truncated. The cap bounds the RAW file read.
	MaxBytes int
	// Diagnostics is the operational-logging sink for the loader's fail-soft Debug
	// lines (no path / unreadable / over-cap / empty / injection-marker / fence
	// breakout). nil is tolerated: newWith defaults it to port.NopDiagnostics so the
	// loader stays silent rather than nil-panicking. The composition layer injects
	// the shared sink; tests that don't care leave it unset.
	Diagnostics port.Diagnostics
}

// Store is a user-scoped, agent-read-only persona loader. It satisfies
// prompt.SoulSource. The zero value is not usable; construct with New.
type Store struct {
	path     string // explicit path; "" means resolve the conventional location
	maxBytes int
	env      xdgconfig.ResolveEnv // path resolution (Getenv/UserHomeDir) only
	read     readFunc             // bounded file read seam
	diag     port.Diagnostics     // operational-logging sink; never nil after newWith
}

// New constructs a Store from opts, binding the real process environment.
func New(opts Options) *Store {
	return newWith(opts, xdgconfig.OSEnv, osRead)
}

// NewWithEnv is New with an injectable PATH-RESOLUTION environment (Getenv/
// UserHomeDir) but the REAL bounded file read. The composition layer uses it so
// the user-scoped soul's conventional <xdg>/mecatl/soul.md resolves against a
// faked env in tests (no real ~/.config dependency) while still reading an actual
// on-disk file. It adds NO write path: env is used only to compute a path string.
func NewWithEnv(opts Options, env xdgconfig.ResolveEnv) *Store {
	return newWith(opts, env, osRead)
}

// newWith is New with an injectable environment + bounded-read seam, for tests.
func newWith(opts Options, env xdgconfig.ResolveEnv, read readFunc) *Store {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	diag := opts.Diagnostics
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	return &Store{path: opts.Path, maxBytes: maxBytes, env: env, read: read, diag: diag}
}

// ResolvedPath returns the soul file path this Store will read — the explicit Path
// when set, else the conventional <xdg>/mecatl/soul.md (fallback
// ~/.config/mecatl/soul.md) — or "" when none can be resolved. It is a READ-ONLY
// accessor (it computes a path string; it touches no file and writes nothing), used
// by the composition layer to locate the drift-baseline sidecar (issue #14, Phase 3,
// Item 1) as a sibling of this path. Exposing it does NOT add a write path.
func (s *Store) ResolvedPath() string {
	return s.resolvePath()
}

// resolvePath returns the soul file path: the explicit Path when set, else the
// conventional <xdg>/mecatl/soul.md (fallback ~/.config/mecatl/soul.md). It
// returns "" when no path can be resolved (no explicit path and neither
// XDG_CONFIG_HOME nor a home dir is available) — the caller then loads nothing.
func (s *Store) resolvePath() string {
	if s.path != "" {
		return s.path
	}
	if base := xdgconfig.UserConfigDir(s.env); base != "" {
		return filepath.Join(base, soulSubpath)
	}
	return ""
}

// Result is the outcome of LoadWithMeta: the clean soul body plus its content
// fingerprint, for the composition-layer drift baseline (issue #14, Phase 3,
// Item 1). The SHA256 is computed over the SAME clean body Load returns (after
// trim/scan/fence checks) — so it fingerprints the bytes that actually reach the
// prompt, not the raw file. When there is no usable soul every field is its zero
// value (empty Body, empty SHA256, zero Size), so an absent/rejected soul yields
// no fingerprint and therefore no baseline.
//
// Note: this is metadata ABOUT a read; it adds no write path. The composition
// layer (internal/app/soulguard) is the only place that turns this hash into an
// on-disk baseline sidecar — the soul adapter itself stays write-free (the
// agent-read-only invariant).
type Result struct {
	// Body is the clean, validated soul body (== Load's return), or "" for none.
	Body string
	// SHA256 is the lowercase-hex SHA-256 of Body, or "" when Body is empty.
	SHA256 string
	// Size is len(Body) in bytes, or 0 when Body is empty.
	Size int
}

// Load reads, validates, and returns the soul body, or ("", nil) when there is no
// usable soul. It FAILS SOFT at every branch: an unresolvable path, a missing/
// unreadable file, an over-cap file, an empty/whitespace-only body, a fence-breakout
// body, or an injection-scan hit all yield ("", nil) — never an error that aborts a
// run. Each branch logs at Debug.
//
// The read is BOUNDED to maxBytes+1 bytes and the cap is checked on RAW bytes
// (before TrimSpace), so an over-cap or pathological file is rejected without
// allocating its full contents.
//
// Load is a thin wrapper over LoadWithMeta so existing prompt.SoulSource consumers
// (which only need the body) are unchanged. It satisfies prompt.SoulSource.
func (s *Store) Load(ctx context.Context) (string, error) {
	res, err := s.LoadWithMeta(ctx)
	return res.Body, err
}

// LoadWithMeta is Load plus the content fingerprint (SHA-256 + size) of the clean
// body, computed in the same pass WITHOUT re-reading the file. It shares every
// fail-soft branch with Load: an absent/empty/over-cap/flagged/fence-breakout soul
// yields the zero Result (empty body, empty hash, zero size) and a nil error.
//
// This adds NO write path: it only computes a hash over bytes already read. The
// hash's sole consumer is the composition-layer drift baseline (internal/app),
// which is the only place allowed to persist it.
func (s *Store) LoadWithMeta(ctx context.Context) (Result, error) {
	path := s.resolvePath()
	if path == "" {
		s.diag.Log(ctx, port.LevelDebug, "soul: no path could be resolved (no --soul-file and no XDG/home); no soul loaded")
		return Result{}, nil
	}

	// Read at most maxBytes+1 RAW bytes: enough to DETECT an over-cap file without
	// buffering more than one byte past the ceiling (ValidateBody re-checks the
	// cap on the raw string, so the rejection is identical).
	raw, err := s.read(path, int64(s.maxBytes)+1)
	if err != nil {
		// Missing file is the common case (soul is opt-in-by-presence); a genuine
		// read error is equally best-effort. Either way: no fragment, no abort.
		s.diag.Log(ctx, port.LevelDebug, "soul: file not read; no soul loaded", "path", path, "err", err)
		return Result{}, nil
	}

	body, reject := ValidateBody(string(raw), s.maxBytes)
	if reject != "" {
		s.diag.Log(ctx, port.LevelDebug, "soul: body rejected; no soul loaded", "path", path, "reason", reject)
		return Result{}, nil
	}

	return Result{Body: body, SHA256: hashutil.SHA256Hex([]byte(body)), Size: len(body)}, nil
}

// ValidateBody preserves the local soul API while delegating the single
// validation discipline to soulbody, shared with the gRPC driver client.
func ValidateBody(body string, maxBytes int) (string, string) {
	return soulbody.ValidateBody(body, maxBytes)
}
