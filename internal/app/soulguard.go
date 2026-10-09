package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"syscall"

	"github.com/stacklok/mecatl/engine/port"
)

// soulguard is the composition-layer SOUL DRIFT BASELINE (issue #14).
// The soul itself is a USER-scoped, agent-READ-ONLY persona fragment (see
// internal/adapter/soul) — the adapter is write-free by construction. This file is
// the ONLY place that turns the soul's content hash into an on-disk baseline so a
// later run can DETECT that the soul changed.
//
// Mechanics:
//   - The baseline is a HARNESS-OWNED sidecar next to the soul file:
//     <soulPath> + ".sha256". For the conventional <xdg>/mecatl/soul.md that is
//     <xdg>/mecatl/soul.md.sha256; for an explicit --soul-file PATH it is
//     PATH + ".sha256". It stores exactly the lowercase-hex SHA-256 of the clean
//     soul body (the same hash soul.LoadWithMeta computes).
//   - TRUST-ON-FIRST-USE: if no sidecar exists at load, the current hash is written
//     as the baseline and an Info line is logged. No alarm — the first sighting of a
//     soul is implicitly trusted.
//   - DRIFT: if a sidecar exists and the current hash differs, a Warn alarm is
//     logged with both hashes and the soul is reported as Drifted. By default the
//     soul STILL loads (a hand-edit on the operator's own box is expected); with
//     --soul-strict a drifted soul contributes no fragment (decided in build.go).
//   - --approve-soul (re)writes the baseline to the current hash unconditionally,
//     clearing any drift. This is the one-shot "yes, I meant that edit" gesture.
//
// RESTORE-to-baseline is deliberately OUT of MVP: a hash-only baseline gives
// detection + alert without a harness-owned COPY of the approved bytes (which would
// be both a content write surface and disproportionate).
//
// LAYERING: drift is NOT routed through the permission evaluator — the soul is
// fenced DATA, never a permission scope. The hash is computed in the adapter
// (stdlib crypto/sha256); the WRITE lives here, in composition, only.

// sidecarSuffix is appended to the soul path to form its baseline sidecar.
const sidecarSuffix = ".sha256"

// baselineIO is the injectable filesystem seam soulguard needs: read the sidecar,
// write the sidecar. It is a tiny struct of funcs so tests run fully offline against
// an in-memory map without touching the real ~/.config (mirroring the read/write
// seams in permconfig / xdgconfig). The real binding is osBaselineIO.
type baselineIO struct {
	// readFile returns the sidecar bytes, or an error (fs.ErrNotExist when absent).
	readFile func(path string) ([]byte, error)
	// writeFile persists the baseline sidecar (0o600 in the real binding).
	writeFile func(path string, data []byte) error
}

// osBaselineIO binds soulguard to the real filesystem. The sidecar is harness-owned
// metadata, so 0o600 (owner-only) is the right mode.
var osBaselineIO = baselineIO{
	readFile:  os.ReadFile,
	writeFile: osWriteSidecar,
}

// osWriteSidecar writes the baseline sidecar with O_NOFOLLOW so a pre-planted SYMLINK
// at the sidecar path cannot redirect the write elsewhere (CWE-59, defense-in-depth:
// the sidecar lives next to a user-controlled --soul-file path). O_NOFOLLOW is a
// Linux/Unix syscall flag (syscall.O_NOFOLLOW); mecatl targets Linux, and we depend
// only on stdlib (golang.org/x/sys is merely an indirect dep), so the stdlib syscall
// constant is the right choice here. Opening a symlinked path with O_NOFOLLOW fails
// with ELOOP, which callers (establishBaseline) treat as fail-soft (Warn + continue).
func osWriteSidecar(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// sidecarPath returns the baseline sidecar path for a soul file: soulPath + ".sha256".
// It returns "" for an empty soulPath (no soul → no sidecar).
func sidecarPath(soulPath string) string {
	if soulPath == "" {
		return ""
	}
	return soulPath + sidecarSuffix
}

// readBaseline returns the stored baseline hash, ok=true when a sidecar exists and
// is readable. A missing sidecar yields ("", false, nil) — the TOFU signal. Any other
// read error is returned so the caller can log it (and fail soft, not establish a
// bogus baseline). The stored hash is trimmed of surrounding whitespace/newline.
func readBaseline(io baselineIO, sidecar string) (hash string, ok bool, err error) {
	data, rerr := io.readFile(sidecar)
	if rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, rerr
	}
	return trimHash(string(data)), true, nil
}

// trimHash strips surrounding whitespace/newlines from a stored sidecar value, so a
// trailing newline written by an editor does not register as drift.
func trimHash(s string) string {
	// Cheap, allocation-light trim of ASCII whitespace (space, tab, CR, LF) on both
	// ends — avoids a strings import for one use.
	start, end := 0, len(s)
	for start < end && isASCIISpace(s[start]) {
		start++
	}
	for end > start && isASCIISpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// establishBaseline writes currentHash as the baseline sidecar (TOFU or
// --approve-soul). It logs at Info on success and Warn on failure (fail-soft: a
// write failure never aborts a run — the soul still loads, drift detection is just
// degraded for this run).
func establishBaseline(d port.Diagnostics, io baselineIO, sidecar, currentHash, reason string) {
	if err := io.writeFile(sidecar, []byte(currentHash+"\n")); err != nil {
		d.Log(context.Background(), port.LevelWarn, "soul: could not write drift baseline; drift detection degraded for this run",
			"sidecar", sidecar, "reason", reason, "err", err)
		return
	}
	d.Log(context.Background(), port.LevelInfo, "soul: baseline established", "sidecar", sidecar, "reason", reason, "hash", currentHash)
}

// checkSoulDrift compares the current soul hash against the on-disk baseline and
// returns whether the soul has DRIFTED. It encapsulates the full Item-1 policy:
//
//   - currentHash == "" (no usable soul): no baseline machinery at all — no sidecar
//     is created for an absent soul, never drift. (R1.5.)
//   - approve == true: (re)write the baseline to currentHash, clearing drift; never
//     drifted. (--approve-soul.)
//   - no sidecar exists: TOFU — write currentHash as baseline + Info; not drifted.
//     (R1.2.)
//   - sidecar exists, hash matches: not drifted, nothing logged at Warn. (R1.4.)
//   - sidecar exists, hash differs: Warn alarm with both hashes; drifted = true.
//     (R1.3.) The caller decides whether a drifted soul still contributes a fragment
//     (default yes; --soul-strict no).
//
// It is fail-soft: an unreadable sidecar is logged at Warn and treated as "no
// reliable baseline" (not drifted, no spurious alarm).
func checkSoulDrift(d port.Diagnostics, io baselineIO, soulPath, currentHash string, approve bool) (drifted bool) {
	if currentHash == "" {
		return false // no usable soul → no baseline, no drift (R1.5)
	}
	sidecar := sidecarPath(soulPath)
	if sidecar == "" {
		return false // no resolvable path → nowhere to anchor a baseline
	}

	if approve {
		establishBaseline(d, io, sidecar, currentHash, "approve")
		return false
	}

	baseline, ok, err := readBaseline(io, sidecar)
	if err != nil {
		d.Log(context.Background(), port.LevelWarn, "soul: could not read drift baseline; drift detection skipped for this run",
			"sidecar", sidecar, "err", err)
		return false
	}
	if !ok {
		establishBaseline(d, io, sidecar, currentHash, "trust-on-first-use") // R1.2
		return false
	}
	if baseline == currentHash {
		return false // R1.4: matching hash, nothing at Warn
	}

	// R1.3: drift. Alarm with both hashes; the soul still loads unless --soul-strict.
	d.Log(context.Background(), port.LevelWarn, "soul: drift detected — the persona file changed since the approved baseline; run with --approve-soul to accept it (or --soul-strict to refuse a drifted soul)",
		"sidecar", sidecar, "baseline", baseline, "current", currentHash)
	return true
}
