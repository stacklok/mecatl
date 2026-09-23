package app

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// escapeclassifier.go is the path-escape-posture Scenario 1 seam
// (docs/acceptance/path-escape-posture.md): a PURE composition-layer
// classification answering "is this FS-tool call an out-of-root escape?" It
// changes NO behaviour — a later wave's root-aware wrapping
// port.PermissionPolicy consumes it; here it only needs to exist and be proven
// to agree with the tool body. The classifier is composition, not domain, per
// the layering rule: the escape *decision* is a posture/policy concern, while
// engine/tool keeps FileSystem/Workspace (the port↔tool cycle gotcha).
//
// It NEVER reimplements the osfs algorithms: the verdict is built from
// osfs.Canonicalize and osfs.LocalizeInRoot, the same canonicalize-then-reject
// primitives the tool body runs. A symlinked absolute path therefore classifies
// identically to the tool body by construction.

// escapeKind is the classification of one FS-tool call's path.
type escapeKind int

const (
	// escapeInRoot — the path resolves inside the workspace root (or the call
	// carries no workspace path at all): never an escape decision.
	escapeInRoot escapeKind = iota
	// escapeEscape — the path resolves outside the workspace root.
	escapeEscape
	// escapePseudoFS — the path is under /proc, /sys, or /dev: a NEVER-RELAXED
	// category, distinct from a regular escape at every posture, because an
	// in-process FS read of e.g. /proc/self/environ returns the SERVER's raw,
	// unscrubbed environment — a secret-exfiltration channel the
	// envscrub-scrubbed Shell parity path does not provide (plan scope cuts).
	escapePseudoFS
)

// String renders the kind for diagnostics and tests.
func (k escapeKind) String() string {
	switch k {
	case escapeInRoot:
		return "in-root"
	case escapeEscape:
		return "escape"
	case escapePseudoFS:
		return "pseudo-fs"
	}
	return "unknown"
}

// escapeClassifier classifies FS-tool calls against one session workspace.
// It holds only canonicalized paths (no *os.Root, no open handles): the only
// I/O classify performs is the Lstat/EvalSymlinks ancestor canonicalization
// resolveInRoot itself performs (via osfs.Canonicalize).
type escapeClassifier struct {
	root string
}

// newEscapeClassifier canonicalizes root exactly as osfs.NewFileSystem does.
func newEscapeClassifier(root string) (*escapeClassifier, error) {
	canonical, err := osfs.ResolveRoot(root)
	if err != nil {
		return nil, err
	}
	return &escapeClassifier{root: canonical}, nil
}

// pseudoFSRoots are the pseudo-filesystem mount points that classify as the
// never-relaxed category (AC1.5). /dev is included: device files are not
// ordinary file content, and /dev/fd/* aliases the proc filesystem.
var pseudoFSRoots = []string{"/proc", "/sys", "/dev"}

// isPseudoFSPath reports whether the ABSOLUTE (or already-canonicalized) path
// lies under a pseudo-fs root. Relative paths are interpreted against the
// workspace root and are never pseudo-fs.
func isPseudoFSPath(abs string) bool {
	if !filepath.IsAbs(abs) {
		return false
	}
	cleaned := filepath.Clean(abs)
	for _, root := range pseudoFSRoots {
		if cleaned == root || strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// underRoot reports whether the canonical absolute path canon lies inside the
// canonical workspace root (equality or containment) — the same comparison
// resolveInRoot performs after EvalSymlinks. No I/O: canon is expected
// already canonicalized by the caller.
func underRoot(canon, root string) bool {
	return canon == root || strings.HasPrefix(canon, root+string(filepath.Separator))
}

// fsPathArg is the arg envelope of the path-carrying FS tools
// (Read/ListDir/Write/Edit) — the classifier reads only the path.
type fsPathArg struct {
	Path string `json:"path"`
}

// classify reports the escapeKind of an FS-tool call. Only Read/ListDir/Write/Edit
// carry a workspace path the escape decision applies to: Shell commands are
// gated by the bash classifiers (SplitCommands/ReadOnlyShell), Glob/Grep route
// patterns (not paths) and stay workspace-confined at every posture (ADR-0047
// point 5), and every other tool has no FS path — all classify in-root so the
// later wrapping policy leaves them to the inner policy untouched. A malformed
// or missing path arg also classifies in-root (the tool body's own arg
// validation rejects it; the escape decision never invents a path).
//
// The verdict order mirrors the tool body's resolution exactly:
//
//  1. pseudo-fs on the VERBATIM path — never-relaxed at every posture;
//  2. a RELATIVE path: lexical-only. A ".." traversal that climbs out of the
//     root is an escape (os.Root's own refusal, mirrored by
//     osfs.LocalizeInRoot). One that stays lexical in-root is canonicalized
//     (deepest-existing-ancestor + EvalSymlinks, the resolveInRoot algorithm)
//     to decide the symlink case the tool body defers to os.Root's
//     containment: an in-root symlink whose target escapes → escape (pseudo-fs
//     if the target is a pseudo-fs mount); everything else → in-root;
//  3. an ABSOLUTE path: resolveInRoot's canonicalize-then-reject — resolves
//     inside the root → in-root; otherwise → escape (pseudo-fs when the
//     canonical target is a pseudo-fs mount).
func (c *escapeClassifier) classify(toolName string, args json.RawMessage) escapeKind {
	switch toolName {
	case "Read", "ListDir", "Write", "Edit":
	default:
		return escapeInRoot
	}
	var a fsPathArg
	if err := json.Unmarshal(args, &a); err != nil || a.Path == "" {
		return escapeInRoot
	}
	path := a.Path
	// (1) pseudo-fs on the verbatim form, before any relax applies.
	if isPseudoFSPath(path) {
		return escapePseudoFS
	}
	// (2) relative path: lexical containment first (os.Root's refusal), then
	// the canonical symlink probe for what the tool body defers to os.Root.
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "/") {
		rel, ok := osfs.LocalizeInRoot(path)
		if !ok {
			return escapeEscape // ".." climbs out: os.Root would refuse
		}
		canon, err := osfs.Canonicalize(c.root, rel)
		if err != nil {
			return escapeEscape // resolveInRoot fails safe on an unverifiable path
		}
		if !underRoot(canon, c.root) {
			if isPseudoFSPath(canon) {
				return escapePseudoFS
			}
			return escapeEscape // in-root symlink whose target escapes
		}
		return escapeInRoot
	}
	// (3) absolute path: resolveInRoot's canonicalize-then-reject.
	canon, err := osfs.Canonicalize("", path)
	if err != nil {
		return escapeEscape // unverifiable ancestor: resolveInRoot fails safe
	}
	if underRoot(canon, c.root) {
		return escapeInRoot
	}
	if isPseudoFSPath(canon) {
		return escapePseudoFS
	}
	return escapeEscape
}
