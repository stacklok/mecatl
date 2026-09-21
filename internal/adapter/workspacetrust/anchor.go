package workspacetrust

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/internal/adapter/hashutil"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// syscallNoFollow is syscall.O_NOFOLLOW (Linux/Unix), so a bounded anchor-file read
// refuses to follow a symlinked path (CWE-59 defense-in-depth). mecatl targets
// Linux and depends only on stdlib, so the stdlib syscall constant is the right
// choice (mirroring soulguard's osWriteSidecar).
const syscallNoFollow = syscall.O_NOFOLLOW

// anchor.go computes the workspace IDENTITY-ANCHOR hash (Workspace-Trust feature,
// Phase 2b; MUST-FIX 4 + 5.2). The anchor is the high-signal, rarely-edited subset
// of a project's authority set whose change should re-prompt for trust:
//
//   - the project SOUL          <ws>/.mecatl/soul.md
//   - project AGENT defs        <ws>/.mecatl/agents/**, <ws>/.claude/agents/**
//   - project COMMAND defs      <ws>/.mecatl/commands/**, <ws>/.claude/commands/**
//   - project SKILL defs        <ws>/.mecatl/skills/**, <ws>/.claude/skills/**
//
// It EXPLICITLY EXCLUDES settings.yaml: permissions are edited on nearly every
// commit, so hashing them for drift would nag-fatigue the operator into
// blind-clicking trust (defeating the premise). Permission edits re-resolve live via
// permconfig's own mtime cache without re-prompting; they are admission-gated but
// NOT drift-anchored.
//
// # Determinism (the drift contract)
//
// The hash is a fold of each member file's content hash, taken over the member
// files in a FIXED SORTED ORDER, so the same surface always yields the same hash
// regardless of filesystem walk order. An ABSENT member contributes a stable
// "absent:<relpath>" marker so that ADDING a soul/agent/command/skill later
// registers as drift (a repo that gains a persona after you trusted it must
// re-prompt). The hash CHANGES when any member's content changes and does NOT
// change when a non-anchored file (settings.yaml, source code, README, …) changes.
//
// # Layering / security
//
// anchor.go is adapter-leaf: stdlib + the shared hashutil.SHA256Hex (MUST-FIX 4 —
// it shares ONLY the hash primitive with soulguard; the two anchors stay parallel).
// Reads are bounded (per-file byte cap) so a pathological file cannot allocate
// unbounded (CWE-789). A single coherent read per file (open once, hash the bytes
// read) keeps the anchor TOCTOU-narrow (MUST-FIX 5.2). All paths are workspace-root
// relative and the walk is confined to the listed subdirs.

// anchorMaxFileBytes bounds each member file read so a pathological anchor file
// (a multi-GB agent def, /dev/zero symlinked in) cannot allocate unbounded. Over
// the cap the file is hashed as its first anchorMaxFileBytes only — still a stable,
// deterministic fingerprint (and any edit to the head still drifts); we never
// buffer more than the cap.
const anchorMaxFileBytes = 1 << 20 // 1 MiB per member file

// anchorMaxFiles caps the total number of member files folded into the anchor, so a
// pathological project tree (thousands of generated skill files) cannot make anchor
// computation walk unbounded. Beyond the cap the walk stops; the partial set is
// still deterministic. Far above any realistic project authority set.
const anchorMaxFiles = 4096

// projectSoulRel is the project soul, relative to the workspace root. It mirrors
// internal/app/soulselect.go's projectSoulSubpath (".mecatl/soul.md"); kept as a
// local constant (one cheap string) to avoid coupling this adapter leaf to the
// composition layer.
const projectSoulRel = ".mecatl/soul.md"

// anchorDirs is the project authority-set DIRECTORY set the anchor folds, sourced
// DIRECTLY from the OWNING packages (FIX 1 — prevent anchor/gate divergence in the
// unsafe direction). Each project-tier dir admitted by a resolver/gate is folded by
// the anchor because the SAME constant feeds both:
//
//   - agents:   agents.ProjectDirMecatl / agents.ProjectDirClaude (the agents
//     resolver's project tier — agents/resolve.go);
//   - skills:   skills.ProjectDirMecatl / skills.ProjectDirClaude (the skills
//     resolver's project tier — skills/resolve.go);
//   - commands: prompt.DefaultCommandDirs (the canonical project-tier command dir
//     set the expander default + the composition command gate both use).
//
// If a tier dir is ever added to one of those resolvers, it flows into the anchor
// here automatically (or the divergence-guard test, TestAnchorCoversGateSurface,
// fails loudly) — so the gate can never admit a project-tier dir the anchor doesn't
// drift on. This is an adapter→adapter / adapter→domain import (workspacetrust →
// agents/skills/prompt), the normal inward direction; no cycle (none of those import
// workspacetrust). We do NOT extract a shared "tier mechanism" — only the dir-set
// CONSTANTS are shared; the soul/trust drift anchors stay parallel (MUST-FIX 4).
//
// The set is computed once at package init and sorted lexically for a stable
// iteration order; AnchorHash re-sorts members by path anyway, so order here is
// belt-and-suspenders.
var anchorDirs = newAnchorDirs()

// newAnchorDirs assembles the project-tier dir set from the owning packages and
// returns it sorted + de-duplicated. Keeping it a function (not an inline composite)
// lets the divergence-guard test call it / reference the same sources.
func newAnchorDirs() []string {
	dirs := []string{
		agents.ProjectDirMecatl,
		agents.ProjectDirClaude,
		skills.ProjectDirMecatl,
		skills.ProjectDirClaude,
	}
	dirs = append(dirs, prompt.DefaultCommandDirs...)
	sort.Strings(dirs)
	// De-dup (defensive: the sources are distinct today, but a future overlap must
	// not double-fold a dir).
	out := dirs[:0:0]
	for i, d := range dirs {
		if i == 0 || d != dirs[i-1] {
			out = append(out, d)
		}
	}
	return out
}

// anchorIO is the injectable filesystem seam the anchor computation needs: list a
// directory tree and read a (bounded) file. It is a tiny struct of funcs so tests
// run fully offline against an in-memory tree without touching real files. The real
// binding is osAnchorIO.
type anchorIO struct {
	// walk visits every regular file under root (recursively), calling fn with the
	// path RELATIVE to root for each. A missing root is not an error (the dir simply
	// contributes nothing). fn returning an error stops the walk with that error.
	walk func(root string, fn func(rel string) error) error
	// readFile returns up to limit bytes of the file at path.
	readFile func(path string, limit int64) ([]byte, error)
	// stat reports whether path exists as a regular file.
	statFile func(path string) bool
}

// osAnchorIO binds the anchor computation to the real filesystem.
var osAnchorIO = anchorIO{
	walk:     osAnchorWalk,
	readFile: osAnchorRead,
	statFile: osStatFile,
}

// AnchorHash computes the identity-anchor hash for workspace using the real
// filesystem. workspace is realpath'd first (symmetric with the registry keying);
// an unresolvable workspace yields "" (the fold then treats a remembered entry as
// drifted/untrusted — fail-safe). The returned hash is lowercase-hex SHA-256.
//
// It is a method on Reader (not a free function) so the anchor computation is reached
// through the same handle the fold already holds; the receiver carries no state the
// computation needs (the IO seam is the real binding), hence the blank receiver.
func (*Reader) AnchorHash(workspace string) string {
	return anchorHash(workspace, osAnchorIO)
}

// anchorHash is AnchorHash with an injectable IO seam, for offline tests.
func anchorHash(workspace string, aio anchorIO) string {
	if workspace == "" {
		return ""
	}
	root, err := realpath(workspace)
	if err != nil {
		// Unresolvable workspace ⇒ no stable anchor. The caller (the fold) treats a
		// remembered entry against an empty anchor as drift (fail-safe untrusted).
		return ""
	}

	// Collect (relpath, contentHash) for every member, then sort by relpath for a
	// deterministic fold independent of walk order.
	type member struct {
		rel  string
		hash string // SHA256Hex of content, or "" sentinel handled below
	}
	var members []member

	// 1. The project soul (a single file).
	soulPath := filepath.Join(root, projectSoulRel)
	if aio.statFile(soulPath) {
		data, rerr := aio.readFile(soulPath, anchorMaxFileBytes)
		if rerr == nil {
			members = append(members, member{rel: projectSoulRel, hash: hashutil.SHA256Hex(data)})
		} else {
			members = append(members, member{rel: projectSoulRel, hash: "absent"})
		}
	} else {
		members = append(members, member{rel: projectSoulRel, hash: "absent"})
	}

	// 2. The project authority-set directories (recursive). Each found file is a
	//    member keyed by its dir-relative path under the workspace root.
	for _, dir := range anchorDirs {
		dirAbs := filepath.Join(root, dir)
		_ = aio.walk(dirAbs, func(rel string) error {
			if len(members) >= anchorMaxFiles {
				return errAnchorTooMany
			}
			full := filepath.Join(dirAbs, rel)
			data, rerr := aio.readFile(full, anchorMaxFileBytes)
			// Key by the workspace-root-relative path so the marker is stable and
			// portable across machines (filepath.ToSlash normalises separators).
			key := filepath.ToSlash(filepath.Join(dir, rel))
			if rerr != nil {
				members = append(members, member{rel: key, hash: "absent"})
				return nil
			}
			members = append(members, member{rel: key, hash: hashutil.SHA256Hex(data)})
			return nil
		})
	}

	sort.Slice(members, func(i, j int) bool { return members[i].rel < members[j].rel })

	// Fold: hash a deterministic transcript of "relpath\x00contenthash\n" lines. The
	// NUL separator cannot appear in a path or a hex hash, so the transcript is
	// unambiguous (no two distinct member sets collide on it).
	var b strings.Builder
	for _, m := range members {
		b.WriteString(m.rel)
		b.WriteByte(0)
		if m.hash == "absent" {
			b.WriteString("absent:")
			b.WriteString(m.rel)
		} else {
			b.WriteString(m.hash)
		}
		b.WriteByte('\n')
	}
	return hashutil.SHA256Hex([]byte(b.String()))
}

// errAnchorTooMany stops the walk once the member cap is hit. It is internal; the
// caller ignores it (the partial set is still deterministic).
var errAnchorTooMany = fs.SkipAll

// osAnchorWalk walks every regular file under root (recursively), calling fn with
// the root-relative path. A missing root contributes nothing (not an error). It
// does NOT follow symlinked directories out of the tree: WalkDir does not descend
// into symlinked dirs by default (it reports them as entries, not dirs), so a
// symlink under the anchor dir is treated as a leaf, not a traversal vector.
func osAnchorWalk(root string, fn func(rel string) error) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil // absent or not a dir ⇒ contributes nothing
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // unreadable entry ⇒ skip it, keep walking
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks/devices/etc. — only regular files are anchored
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		return fn(rel)
	})
}

// osAnchorRead reads up to limit bytes of path via a bounded LimitReader so a
// pathological file cannot allocate unbounded (CWE-789). The file is opened
// O_NOFOLLOW so a symlinked anchor file is not followed (it would have been skipped
// by the walk's IsRegular check, but reading the soul path directly also refuses a
// symlink). A symlinked path ⇒ ELOOP ⇒ read error ⇒ "absent" marker (fail-safe).
func osAnchorRead(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscallNoFollow, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

// osStatFile reports whether path exists as a regular file (not a dir/symlink).
func osStatFile(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}
