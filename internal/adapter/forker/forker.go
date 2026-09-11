// Package forker implements the default tool.EnvironmentForker used by fork-join
// parallelism (harness pattern 8). It isolates a forked child agent loop from
// the shared base working tree so parallel children cannot race on, or mutate,
// the base.
//
// Isolation strategy (chosen per base at Fork time):
//
//   - Git worktree — when the base workspace root is inside a git repository,
//     Fork runs `git worktree add --detach <child> HEAD` so the child gets a
//     real, independent checkout of HEAD that shares the object store but has its
//     own index and working tree. cleanup runs `git worktree remove --force` and
//     deletes the directory. This is the cheapest correct isolation for a repo:
//     it copies no file contents.
//
//   - Recursive copy — when the base root is NOT a git repo (no `.git`), OR when
//     the Forker was constructed WithForceCopy, Fork recursively copies the whole
//     base tree into a fresh temp directory. cleanup removes that directory. This
//     is a full, independent copy: writes in the child never touch the base. When
//     the base IS a git repo, the copy INCLUDES its `.git` directory, so the fork
//     is an independent repository with its OWN object database and refs.
//
// Isolation guarantees: a child Workspace returned by Fork is rooted at an
// isolated directory; Write/Edit/Shell through the child affect ONLY that
// directory. The base tree is never written. cleanup is idempotent-friendly (it
// tolerates an already-removed child) and must be called when the child is done.
//
// Worktree vs. full-copy isolation — the tradeoff:
//
// The git-worktree path isolates the WORKING TREE and INDEX but SHARES the object
// database and refs. Forked children CAN mutate their working tree: Edit/Write land
// in the fork, and Shell is workspace-aware (its CommandRunner runs with the forked
// child's Workspace.Root() as the working directory; see app.buildParallelChildEngine /
// buildMemberEngine and internal/adapter/tools/bash.go), so a child's Shell — and any
// git it runs — defaults to the fork's working tree, not the parent base. But in a
// worktree, a child that runs `git commit` / `git push` / `git update-ref` via Shell
// writes objects and refs into the SHARED `.git`, escaping isolation. That is the
// inherent git-worktree model.
//
// To close that gap for MUTATING forks, construct the Forker WithForceCopy: it
// forces the recursive-copy path even for a git repo and copies the `.git`
// directory along with the tree, so the fork is a SELF-CONTAINED repository. A
// branch's git/Shell writes (commits, refs, objects) then stay inside the fork and
// CANNOT reach the base repo. The composition root wires WithForceCopy for the Fork
// tool's branches and for mutating team members (see internal/app/build.go).
//
// The DEFAULT (no option) — the cheap auto worktree-vs-copy behaviour — is now used
// DELIBERATELY for READ-ONLY callers that nonetheless need a shell, specifically
// read-only team members (see internal/app.buildTeamWiring): a worktree SHARES the
// base repo's `.git`, so the member gets the full commit history for `git log`/`git
// show` inspection at near-zero cost, while its own working tree + index keep its
// (non-mutating) Shell from disturbing the base working tree. Because a worktree shares
// `.git/config` + `.git/hooks`, the composition root runs such a member's Shell through
// a SANDBOXED command runner that neutralises git config-driven code execution
// (core.pager / core.hooksPath / core.fsmonitor / external diff); see
// internal/app.buildSandboxedCommandRunner.
//
// The forker's OWN git invocations are hardened the same way. `git worktree add`
// fires the base repo's post-checkout hook, and even `git rev-parse` honours
// core.pager / external diff — all at FORK time, BEFORE the sandboxed member runner
// exists. So runGit/gitRepoRoot set cmd.Env = gitenv.Scrub(os.Environ()), the SAME
// neutralizing environment the member runner uses (the single shared source in
// internal/adapter/gitenv keeps the two from drifting): inherited GIT_* danger is
// dropped and hooks/pager/fsmonitor/external-diff are force-neutralised.
//
// Cost: a full copy (including `.git`, which for an established repo is often the
// bulk of the bytes) is HEAVIER than a worktree, which copies no file contents.
// That is exactly why worktree is the default. For mutating branches the stronger
// isolation is worth the extra copy.
//
// Dirty-aware overlay (WithDirtyOverlay) — the read-only-child gap:
//
// A plain `git worktree add --detach HEAD` checks out the COMMITTED HEAD, so the
// child sees a CLEAN tree: Read/Grep/Glob AND the child's `git status`/`git diff`
// report no changes even when the operator has uncommitted, staged, or untracked
// work in the parent. A read-only explorer asked to review the operator's
// in-progress changes would then find nothing — it cannot see what the operator
// sees. WithDirtyOverlay is the worktree-only MODE that closes that gap by mirroring
// the parent's uncommitted state into the fresh worktree. It is best-effort: a
// failed overlay resets the worktree to a pristine-HEAD checkout (the safe floor) AND
// returns a degraded-fork advisory from Fork so the agent can tell the child it is
// seeing committed HEAD only — the failure case is SURFACED, never silent. It is
// inert on the force-copy path (copyTree already carries the parent's dirty state
// verbatim). The full step-by-step mechanics live on WithDirtyOverlay's godoc.
//
// The copy path is bounded only by available disk and the size of the base tree; it
// copies regular files and directories and SKIPS symlinks (so a symlink cannot
// smuggle the copy outside the base). Neither path auto-merges results back — see
// the ParallelTool docs (no-auto-merge boundary).
package forker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/envscrub"
	"github.com/stacklok/mecatl/internal/adapter/gitenv"
)

// childRoot is the constructor the forker uses to build a child tool.Workspace
// over an isolated directory. It is injected so this adapter does not import the
// osfs adapter directly (avoiding an adapter→adapter dependency) and so tests can
// substitute a workspace constructor. The composition root passes osfs.NewWorkspace.
type childRoot func(root string) (tool.Workspace, error)

// childRunner is the constructor the forker uses to build a BOUND tool.CommandRunner
// for an isolated child directory (issue #462). It is injected so this adapter does
// not import the osfs adapter directly and so the composition root controls the
// hardening (envscrub + gitenv) applied to a child's Shell. The runner is bound to
// the child root — the command's cwd follows the forked workspace, never the parent
// base. nil means "no shell for this child" (the child's Shell surfaces ErrNoShell).
// The composition root passes a closure over osfs.NewCommandRunnerShell that applies
// the same env-scrubbing the parent runner gets.
type childRunner func(root string) tool.CommandRunner

// Forker is the default tool.EnvironmentForker. It picks git-worktree isolation
// when the base root is a git repo and falls back to a recursive directory copy
// otherwise. It is safe for concurrent use: Fork holds no per-call state on the
// Forker, and each call derives a uniquely-named child directory.
type Forker struct {
	// newWorkspace builds a child tool.Workspace over an isolated directory.
	newWorkspace childRoot
	// newRunner builds a child tool.CommandRunner bound to the isolated directory,
	// or nil when the child namespace has no shell. nil (the field) means the
	// forker was constructed without a runner builder, so EVERY forked child is
	// shell-less (the composition root wires the builder only when Shell is on).
	newRunner childRunner
	// tmpBase is the parent directory under which child directories are created.
	// Empty means os.MkdirTemp's default (os.TempDir()).
	tmpBase string
	// runGit executes a git subcommand in dir; injected so tests can avoid git.
	runGit func(ctx context.Context, dir string, args ...string) error
	// forceCopy, when true, makes Fork always take the recursive-copy path (copying
	// .git too) instead of the git-worktree path, even for a git repo — giving the
	// fork its OWN object DB/refs so a child's git/Shell writes stay inside the fork.
	forceCopy bool
	// dirtyOverlay, when true, mirrors the parent's uncommitted state (tracked
	// modifications + staged changes + deletions + untracked non-ignored files) into
	// a freshly-created worktree so a read-only child sees what the operator sees.
	// Best-effort, no-op on a clean tree, inert on the force-copy path.
	dirtyOverlay bool
	// seq disambiguates concurrently-created child directories for the same label.
	seq atomic.Uint64
}

// Option configures a Forker.
type Option func(*Forker)

// WithTempBase sets the parent directory under which isolated child directories
// are created (default: the OS temp dir). Useful to keep forks on the same
// filesystem as the base for cheaper copies, or to scope them to a test dir.
func WithTempBase(dir string) Option {
	return func(f *Forker) { f.tmpBase = dir }
}

// WithForceCopy forces FULL isolation: Fork always takes the recursive-copy path
// (copying the base tree INCLUDING its `.git` when present) instead of a git
// worktree, even when the base is a git repo. The fork is then a self-contained
// repository with its own object database and refs, so a child branch's git/Shell
// writes (commits, refs, objects, working-tree edits) CANNOT reach the base repo.
//
// This is the mode for MUTATING forks (the Parallel tool's branches and mutating team
// members), where isolation matters more than speed: a full copy — `.git` and all
// — is heavier than a worktree (which copies no file contents), which is why the
// default leaves the cheaper auto worktree-vs-copy behaviour in place. Symlinks are
// still skipped, so the copy cannot be smuggled outside the base.
func WithForceCopy() Option {
	return func(f *Forker) { f.forceCopy = true }
}

// WithDirtyOverlay makes Fork mirror the parent's UNCOMMITTED state into a freshly
// created git worktree, closing the "a read-only child sees a clean tree" gap. A
// plain `git worktree add --detach HEAD` checks out the committed HEAD, so a
// read-only explorer (Subagent or read-only team member) cannot see the operator's
// in-progress work — modified tracked files, staged changes, deletions, or new
// untracked files. With this option set, after the worktree is created the forker:
//
//   - applies `git diff --no-ext-diff --binary HEAD` from the parent (tracked
//     modifications + staged changes + deletions; --binary so binary files
//     round-trip) onto the child via `git apply`; and
//   - copies each untracked, non-ignored file (`git ls-files --others
//     --exclude-standard`) into the child, skipping symlinks/irregular files.
//
// It is .gitignore-respecting, symlink-skipping, BEST-EFFORT, and a NO-OP on a clean
// tree (a `git status --porcelain` probe short-circuits before any diff/apply,
// keeping the common cheap path free).
//
// SURFACED degradation: best-effort does NOT mean silent. When the tree was dirty
// but the overlay could not be applied (e.g. `git apply` rejected the patch), the
// forker resets the worktree to a pristine-HEAD checkout (a partially-applied overlay
// is worse than none — the clean worktree is the safe floor) AND Fork returns a
// non-empty degraded-fork advisory describing that the child is seeing committed HEAD
// only. The agent surfaces that advisory to the child so a read-only explorer does
// not silently conclude "nothing changed" while the operator has uncommitted work.
// On a clean tree or a successful overlay the advisory is empty.
//
// WithDirtyOverlay applies ONLY on the worktree branch. On the force-copy path
// (WithForceCopy), the recursive copyTree already carries the parent's dirty state
// verbatim, so combining the two is harmless: force-copy wins by call site and the
// overlay never runs. It is the mode the composition root wires for read-only
// children that need a shell (the Subagent worktree forker and the read-only team
// member forker).
func WithDirtyOverlay() Option {
	return func(f *Forker) { f.dirtyOverlay = true }
}

// WithRunner injects the bound-command-runner builder the forker uses to mint a
// child tool.CommandRunner for each forked directory (issue #462). The runner is
// bound to the child root, so a forked child's Shell observes the SAME child
// namespace its Read/Write do. nil (or unset) means forked children are shell-less
// (the composition root wires the builder only when Shell is on). The builder is
// called once per Fork with the isolated child directory; it returns nil when the
// child namespace should have no shell (e.g. the trust gate withheld it).
func WithRunner(r childRunner) Option {
	return func(f *Forker) { f.newRunner = r }
}

// New constructs the default Forker. newWorkspace builds a child tool.Workspace
// over an isolated directory (the composition root passes osfs.NewWorkspace);
// it must be non-nil.
func New(newWorkspace func(root string) (tool.Workspace, error), opts ...Option) *Forker {
	if newWorkspace == nil {
		panic("forker: New requires a non-nil newWorkspace constructor")
	}
	f := &Forker{
		newWorkspace: newWorkspace,
		runGit:       runGit,
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Compile-time assertion that Forker satisfies the seam.
var _ tool.EnvironmentForker = (*Forker)(nil)

// Fork creates an isolated child workspace derived from base. It uses a git
// worktree when base's root is a git repo, else a recursive copy. The returned
// cleanup removes the child's backing storage (worktree or copy).
//
// The advisory is non-empty ONLY on the dirty-overlay worktree path when the
// overlay DEGRADED (the base was dirty but its uncommitted state could not be
// mirrored into the child, so the child sees committed HEAD only). Every other path
// — force-copy, plain worktree, copy fallback, clean tree, no overlay — returns "".
func (f *Forker) Fork(ctx context.Context, base tool.Environment, label string) (tool.Environment, func() error, string, error) {
	if base.Workspace() == nil {
		return tool.Environment{}, nil, "", errors.New("forker: Fork requires a base with a non-nil workspace")
	}
	baseRoot := base.Workspace().Root()
	if baseRoot == "" {
		return tool.Environment{}, nil, "", errors.New("forker: base workspace has no root")
	}

	childDir, err := f.childDir(label)
	if err != nil {
		return tool.Environment{}, nil, "", err
	}

	// Force-copy mode: always take the full recursive copy (including .git), giving
	// the fork its own object DB/refs. This is the MUTATING-fork isolation mode — a
	// child's git/Shell writes can never reach the base repo. copyTree already carries
	// the parent's dirty state verbatim, so there is never a degraded-fork advisory.
	if f.forceCopy {
		ws, cleanup, ferr := f.forkCopyInto(baseRoot, childDir)
		if ferr != nil {
			return tool.Environment{}, nil, "", ferr
		}
		env, werr := f.childEnv(base, ws)
		if werr != nil {
			_ = cleanup()
			return tool.Environment{}, nil, "", werr
		}
		return env, cleanup, "", nil
	}

	repoRoot, isRepo := gitRepoRoot(ctx, baseRoot)
	if isRepo {
		ws, cleanup, advisory, ferr := f.forkWorktree(ctx, repoRoot, childDir)
		if ferr != nil {
			// Worktree creation failed (e.g. dirty/odd repo state): fall back to a
			// copy so a fork never hard-fails just because git refused. forkCopy
			// mints a FRESH child directory (the reservation above was discarded),
			// so Workspace/Ref/runner all derive from the copy root via childEnv —
			// never from the discarded childDir.
			_ = os.RemoveAll(childDir)
			cws, ccleanup, cerr := f.forkCopy(baseRoot, label)
			if cerr != nil {
				return tool.Environment{}, nil, "", cerr
			}
			cenv, cwerr := f.childEnv(base, cws)
			if cwerr != nil {
				_ = ccleanup()
				return tool.Environment{}, nil, "", cwerr
			}
			return cenv, ccleanup, "", nil
		}
		env, werr := f.childEnv(base, ws)
		if werr != nil {
			_ = cleanup()
			return tool.Environment{}, nil, "", werr
		}
		return env, cleanup, advisory, nil
	}
	ws, cleanup, ferr := f.forkCopyInto(baseRoot, childDir)
	if ferr != nil {
		return tool.Environment{}, nil, "", ferr
	}
	env, werr := f.childEnv(base, ws)
	if werr != nil {
		_ = cleanup()
		return tool.Environment{}, nil, "", werr
	}
	return env, cleanup, "", nil
}

// childEnv wraps a forked child Workspace into a complete tool.Environment,
// binding a CommandRunner to the child namespace when the forker was wired with a
// runner builder (issue #462). The child Environment carries the SAME backend
// Kind as the base (a local base forks into a local child) and a ref ID naming the
// child namespace so the parent can identify the fork.
//
// AFFINITY (structural): the ref ID and the bound runner root are derived from
// ws.Root() — NEVER from a separately threaded childDir. A Workspace opened over
// an isolated directory is the single source of truth for the child namespace: its
// root IS where the runner must be bound and what the ref ID names. Threading a
// second childDir risks divergence on the worktree-failure→copy fallback path,
// where forkCopy mints a FRESH directory distinct from the one Fork reserved
// (git refused the worktree, so the reservation was discarded) — binding the
// runner and ref to the discarded reservation would point Shell and identity at a
// directory the child never executes in. Deriving both from ws.Root() makes that
// impossible: Workspace, Ref, and runner always share the actual copy root.
//
// When no runner builder is wired (or it returns nil for this root) the child is
// shell-less and its Shell surfaces ErrNoShell honestly.
func (f *Forker) childEnv(base tool.Environment, ws tool.Workspace) (tool.Environment, error) {
	root := ws.Root()
	var runner tool.CommandRunner
	if f.newRunner != nil {
		runner = f.newRunner(root)
	}
	ref := session.EnvironmentRef{Kind: base.Ref().Kind, ID: root, Revision: base.Ref().Revision}
	return tool.NewEnvironment(ref, ws, memledger.New(), runner)
}

// childDir reserves (creates) a uniquely-named, empty directory for a child fork,
// folding a sanitized label in for observability.
func (f *Forker) childDir(label string) (string, error) {
	n := f.seq.Add(1)
	pattern := fmt.Sprintf("mecatlfork-%s-%d-*", sanitizeLabel(label), n)
	dir, err := os.MkdirTemp(f.tmpBase, pattern)
	if err != nil {
		return "", fmt.Errorf("forker: create child dir: %w", err)
	}
	return dir, nil
}

// forkWorktree adds a detached git worktree at childDir pointing at repoRoot's
// HEAD. git refuses to create a worktree at an existing non-empty directory, so
// childDir (created empty by childDir) is removed first and recreated by git.
func (f *Forker) forkWorktree(ctx context.Context, repoRoot, childDir string) (tool.Workspace, func() error, string, error) {
	// git worktree add wants to create the directory itself.
	if err := os.RemoveAll(childDir); err != nil {
		return nil, nil, "", fmt.Errorf("forker: prepare worktree dir: %w", err)
	}
	if err := f.runGit(ctx, repoRoot, "worktree", "add", "--detach", childDir, "HEAD"); err != nil {
		return nil, nil, "", fmt.Errorf("forker: git worktree add: %w", err)
	}
	// Dirty-aware overlay: mirror the parent's uncommitted state into the fresh
	// worktree so a read-only child sees what the operator sees. Best-effort — a
	// failure leaves a pristine-HEAD worktree (the safe floor), so the error is
	// deliberately swallowed and Fork never hard-fails because of the overlay. When
	// the overlay DEGRADED (the tree was dirty but the overlay failed and the child
	// fell back to committed HEAD), overlayDirty returns a non-empty advisory the
	// caller surfaces to the child so it knows it is NOT seeing the operator's work.
	var advisory string
	if f.dirtyOverlay {
		advisory = f.overlayDirty(ctx, repoRoot, childDir)
	}
	ws, err := f.newWorkspace(childDir)
	if err != nil {
		_ = f.runGit(ctx, repoRoot, "worktree", "remove", "--force", childDir)
		return nil, nil, "", fmt.Errorf("forker: open child workspace: %w", err)
	}
	cleanup := func() error {
		// Detached context: cleanup must run even if the fork's ctx was cancelled.
		rmErr := f.runGit(context.Background(), repoRoot, "worktree", "remove", "--force", childDir)
		// Best-effort directory removal in case git left anything (or already gone).
		_ = os.RemoveAll(childDir)
		// Prune any dangling administrative entry.
		_ = f.runGit(context.Background(), repoRoot, "worktree", "prune")
		return rmErr
	}
	return ws, cleanup, advisory, nil
}

// forkCopy creates a fresh child directory and recursively copies base into it.
func (f *Forker) forkCopy(baseRoot, label string) (tool.Workspace, func() error, error) {
	childDir, err := f.childDir(label)
	if err != nil {
		return nil, nil, err
	}
	return f.forkCopyInto(baseRoot, childDir)
}

// forkCopyInto recursively copies baseRoot's tree into the (already-created,
// empty) childDir and opens a child workspace there.
func (f *Forker) forkCopyInto(baseRoot, childDir string) (tool.Workspace, func() error, error) {
	if err := copyTree(baseRoot, childDir); err != nil {
		_ = os.RemoveAll(childDir)
		return nil, nil, fmt.Errorf("forker: copy base tree: %w", err)
	}
	ws, err := f.newWorkspace(childDir)
	if err != nil {
		_ = os.RemoveAll(childDir)
		return nil, nil, fmt.Errorf("forker: open child workspace: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(childDir) }
	return ws, cleanup, nil
}

// gitRepoRoot reports whether dir is inside a git work tree and, if so, the
// absolute top-level directory. A git failure (git absent, not a repo) reports
// (",", false) and the caller falls back to a copy.
func gitRepoRoot(ctx context.Context, dir string) (string, bool) {
	// Use a capturing exec directly (the injected runner is fire-and-forget); a
	// failure simply means "not a repo / no git" and triggers the copy fallback.
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	// Even this read-only probe runs git config-aware; scrub the env so a shared
	// `.git/config` (core.pager/external diff/etc.) can never drive code here.
	// envscrub.Scrub first removes the harness credentials (one secret-scrub policy
	// across every agent-adjacent shell), then gitenv neutralises git.
	cmd.Env = gitenv.Scrub(envscrub.Scrub(os.Environ()))
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", false
	}
	return root, true
}

// runGit executes a git subcommand in dir, returning a descriptive error on a
// non-zero exit (folding in stderr).
func runGit(ctx context.Context, dir string, args ...string) error {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	// CRITICAL: scrub the env BEFORE any git invocation. `git worktree add` fires
	// the base repo's post-checkout hook, and other subcommands honour core.pager /
	// external diff — all at FORK time, before the sandboxed member runner exists.
	// gitenv.Scrub (shared with buildSandboxedCommandRunner) neutralises hooks,
	// pager, fsmonitor and external diff and drops inherited GIT_* danger; the
	// envscrub.Scrub base also removes the harness credentials.
	cmd.Env = gitenv.Scrub(envscrub.Scrub(os.Environ()))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// degradedOverlayAdvisory is the human-readable note overlayDirty returns when the
// parent tree was dirty but its uncommitted state could not be mirrored into the
// child — so the child is seeing committed HEAD only. The agent prepends it to the
// child's view so a read-only explorer reasons honestly ("the diff looks clean but
// the operator has uncommitted work I cannot see") instead of concluding there is
// nothing to review. It is informational, never load-bearing for safety.
const degradedOverlayAdvisory = "[harness note: the workspace had uncommitted changes, but they could not be overlaid into your isolated checkout — you are seeing the committed HEAD only. `git status`/`git diff` will look clean even though the operator has un-committed work.]"

// overlayDirty mirrors the parent repo's UNCOMMITTED state (tracked modifications,
// staged changes, deletions, and untracked non-ignored files) from parentRoot into
// the freshly-created worktree at childDir, so a read-only child sees what the
// operator sees rather than a clean HEAD checkout. It is best-effort: on ANY error
// it resets the worktree to pristine HEAD (the pre-fix clean-worktree behaviour —
// the safe floor, since a partially-applied overlay is worse than none).
//
// It returns a DEGRADED-fork advisory: a non-empty string (degradedOverlayAdvisory)
// ONLY when the dirty probe saw changes AND the overlay failed — i.e. the tree was
// dirty but the child fell back to committed HEAD. It returns "" on a clean tree
// (nothing to overlay) or a successful overlay (the child genuinely sees the dirty
// state). The error itself is intentionally NOT propagated — the worktree is usable
// either way; only the human-facing advisory crosses back to the caller.
//
// Sequence:
//
//  1. Dirty probe — `git status --porcelain`. Empty ⇒ clean ⇒ no-op + "" (keeps the
//     common cheap path free; no diff/apply runs).
//  2. Tracked + staged + deletions — pipe `git diff --no-ext-diff --binary HEAD`
//     from the parent into `git apply --whitespace=nowarn -` in the child. `diff
//     HEAD` captures the net working-tree-vs-HEAD delta (exactly what `git status`
//     reports); --binary round-trips binary files; deletions and renames-as-delete
//     +add are reproduced by apply.
//  3. Untracked non-ignored — `git ls-files --others --exclude-standard -z`, then
//     copy each `<parentRoot>/<path>` to `<childDir>/<path>` via copyFile, SKIPPING
//     symlinks/irregular files (os.Lstat check, mirroring copyTree's discipline).
//
// Every git invocation carries the SAME scrubbed/neutralizing env as runGit (via
// runGitCapture), and the diff carries --no-ext-diff, so a shared `.git/config` or
// attacker-named external diff driver cannot drive code here.
func (*Forker) overlayDirty(ctx context.Context, parentRoot, childDir string) string {
	// (1) Cheap dirty probe. A clean tree short-circuits before any diff/apply.
	status, err := runGitCapture(ctx, parentRoot, nil, "status", "--porcelain")
	if err != nil {
		// The probe itself failed: we cannot tell whether the tree is dirty. Treat it
		// as a possible-degradation and advise — the child may be missing uncommitted
		// work, and a false-positive advisory on a genuinely-clean tree is harmless.
		return degradedOverlayAdvisory
	}
	if len(bytes.TrimSpace(status)) == 0 {
		return "" // clean tree — nothing to overlay, no degradation
	}

	if oerr := overlayDirtyInner(ctx, parentRoot, childDir); oerr != nil {
		// Reset the worktree to pristine HEAD: a partial overlay is worse than none.
		// The tree WAS dirty (probe saw changes) but the child now sees committed HEAD
		// only — surface the degradation so the child does not report "nothing changed".
		_, _ = runGitCapture(ctx, childDir, nil, "checkout", "--", ".")
		_, _ = runGitCapture(ctx, childDir, nil, "clean", "-fd")
		return degradedOverlayAdvisory
	}
	return "" // overlay succeeded — the child sees the dirty state
}

// overlayDirtyInner performs steps (2) and (3) of the overlay; split out so a
// failure in either has ONE recovery path (reset-to-pristine) in overlayDirty.
func overlayDirtyInner(ctx context.Context, parentRoot, childDir string) error {
	// (2) Tracked + staged + deletions: apply `git diff HEAD` into the child.
	// --no-textconv is defence-in-depth: the overlay's source `.git` is the trusted
	// PARENT today, so textconv here is the operator's own config — but the flag is
	// cheap and closes the attacker-named-driver class if the overlay's source ever
	// becomes untrusted. Consistent with the merge path (mergeForkInner), which IS
	// load-bearing (the fork's .git is attacker-authored).
	patch, err := runGitCapture(ctx, parentRoot, nil, "diff", "--no-ext-diff", "--no-textconv", "--binary", "HEAD")
	if err != nil {
		return fmt.Errorf("forker: diff HEAD: %w", err)
	}
	if len(bytes.TrimSpace(patch)) > 0 {
		// NB: `git apply` does NOT accept --no-ext-diff (a diff-family flag); the
		// scrubbed env already neutralises any external diff driver. apply reads the
		// patch from stdin ("-"). --whitespace=nowarn keeps a noisy-but-valid patch
		// (trailing whitespace in the operator's edits) from being rejected.
		if _, aerr := runGitCapture(ctx, childDir, patch,
			"apply", "--whitespace=nowarn", "-"); aerr != nil {
			return fmt.Errorf("forker: apply dirty patch: %w", aerr)
		}
	}

	// (3) Untracked, non-ignored files: copy each into the child. --exclude-standard
	// honours .gitignore (+ .git/info/exclude); -z is NUL-separated, robust to spaces.
	others, err := runGitCapture(ctx, parentRoot, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("forker: ls-files --others: %w", err)
	}
	for _, rel := range splitNUL(others) {
		if rel == "" {
			continue
		}
		srcPath := filepath.Join(parentRoot, rel)
		// SKIP symlinks/irregular files: a symlink could point outside the base, and
		// copying its target would break isolation (mirrors copyTree's discipline).
		info, lerr := os.Lstat(srcPath)
		if lerr != nil {
			continue // raced away mid-fork; honour best-effort
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if cerr := copyFile(srcPath, filepath.Join(childDir, rel)); cerr != nil {
			return fmt.Errorf("forker: copy untracked %q: %w", rel, cerr)
		}
	}
	return nil
}

// runGitCapture executes a git subcommand in dir, returning its stdout. It is the
// capturing sibling of runGit (fire-and-forget) for the dirty-overlay path, which
// must read diff/status/ls-files output and pipe a patch on stdin. It carries the
// IDENTICAL scrubbed/neutralizing env line as runGit/gitRepoRoot (the single shared
// git-hardening policy), sets cmd.Stdin from stdin when non-nil, and folds stderr
// into the returned error on a non-zero exit.
//
// It is a package var (delegating to runGitCaptureImpl) so a test can WRAP it to
// count the overlay's git invocations — the seam that lets TestForkDirtyOverlay-
// CleanTreeNoOp prove the `git status --porcelain` short-circuit (a regression that
// removed it would run diff/apply on a clean tree and the call count would jump).
var runGitCapture = runGitCaptureImpl

func runGitCaptureImpl(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	// SAME env as runGit/gitRepoRoot: envscrub removes the harness credentials, then
	// gitenv neutralises hooks/pager/fsmonitor/external-diff and drops GIT_* danger.
	cmd.Env = gitenv.Scrub(envscrub.Scrub(os.Environ()))
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// splitNUL splits a NUL-separated, NUL-terminated byte slice (git -z output) into
// its non-empty elements. It drops EVERY empty element (not just the trailing one
// from the final separator), keeping it byte-for-byte equivalent to the sibling impl
// in cmd/mecatequi/run.go (the two are deliberately kept identical until a third
// `-z` parser appears and earns a shared helper — Rule of Three).
func splitNUL(b []byte) []string {
	parts := strings.Split(string(b), "\x00")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// copyTree recursively copies the directory tree rooted at src into dst (which
// must already exist). It copies regular files and directories, preserves
// permission bits, and deliberately SKIPS symlinks. The repo's `.git` admin dir is
// copied like any other directory: on the auto path it is only reached for a
// non-repo (where there is no `.git`), but on the WithForceCopy path the base IS a
// repo and copying `.git` is the POINT — it makes the fork a self-contained repo
// with its own object DB/refs, so a child's git writes stay inside the fork.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		return copyTreeEntry(src, dst, p, d, err)
	})
}

// copyTreeEntry copies one WalkDir entry. It is separate from the walk so the
// transient maintenance-lock race is deterministic to test.
func copyTreeEntry(src, dst, p string, d fs.DirEntry, err error) error {
	rel, rerr := filepath.Rel(src, p)
	if rerr != nil {
		return rerr
	}
	if err != nil {
		if isTransientGitMaintenanceLock(rel) && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if isTransientGitMaintenanceLock(rel) {
		return nil
	}
	target := filepath.Join(dst, rel)
	switch {
	case d.Type()&fs.ModeSymlink != 0:
		// Skip symlinks: they could point outside the base, and copying their
		// target would break isolation.
		return nil
	case d.IsDir():
		if rel == "." {
			return nil // dst already exists
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		return os.MkdirAll(target, info.Mode().Perm()|0o700)
	case d.Type().IsRegular():
		return copyFile(p, target)
	default:
		// Skip irregular files (devices, sockets, pipes).
		return nil
	}
}

// isTransientGitMaintenanceLock identifies Git's short-lived maintenance lock in
// the object database. copyTree ignores it, including when WalkDir reports that it
// vanished; unrelated paths and errors remain copy failures.
func isTransientGitMaintenanceLock(rel string) bool {
	return filepath.ToSlash(rel) == ".git/objects/maintenance.lock"
}

// copyFile copies a single regular file from src to dst, preserving its mode.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src) //nolint:gosec // src is a tree entry under the base root
	if err != nil {
		return err
	}
	defer func() {
		if cerr := in.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(dst), 0o700); mkErr != nil {
		return mkErr
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()) //nolint:gosec // dst is under the freshly-created child dir
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// sanitizeLabel reduces an arbitrary label to a short, filesystem-safe token for
// use in a child directory name. Empty or all-stripped labels become "fork".
func sanitizeLabel(label string) string {
	const maxLen = 24
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= maxLen {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "fork"
	}
	return s
}

// Merger is the tool.EnvironmentMerger implementation: it merges a preserved winning
// fork's working-tree changes BACK into the parent workspace. It is the
// composition-owned merge half of BOTH merge-back paths — a Parallel
// single-branch winner (ADR 0039) AND a writable Subagent (mode:"read-write",
// ADR 0040) — DEFAULT-ON with no flag, the capability that lets a delegated
// implementer's edits actually land without a manual copy/merge step. The
// composition root wraps it in a SerializingMerger (one process-wide mutex) so
// concurrent merges from different runs/sessions cannot interleave their writes.
//
// Mechanism (mirrors overlayDirty, in the reverse direction — fork → parent):
//   - (1) Cheap clean probe on the fork: a clean fork short-circuits (nothing
//     to merge), returning nil without touching the parent.
//   - (2) Tracked + staged + deletions: pipe `git diff --no-ext-diff --binary
//     HEAD` from the fork into `git apply --whitespace=nowarn -` in the parent.
//     --binary round-trips binary files; --no-ext-diff + the scrubbed env keeps
//     an attacker-named external diff driver from firing.
//   - (3) Untracked, non-ignored files in the fork: `git ls-files --others
//     --exclude-standard -z`, then copy each into the parent, skipping
//     symlinks/irregular files (same discipline as copyTree/overlayDirtyInner —
//     a symlink could point outside the fork).
//
// On ANY failure (diff/apply rejected, copy failed) it returns a non-nil error
// naming the fork path so the operator can resolve manually. It NEVER forces:
// a partial merge is worse than none. The fork is left intact (the caller still
// owns its cleanup / reaper slot) so a failed merge is recoverable. The merge
// runs in the PARENT workspace under the parent's trust posture (the same trust
// the parent's own Edit/Write carries) — applying a diff is a parent-side
// operation, not a fork-side one.
//
// Every git invocation carries the SAME scrubbed/neutralizing env as runGit /
// overlayDirty (gitenv.Scrub(envscrub.Scrub(os.Environ()))), so a shared or
// copied `.git/config` and attacker-named drivers cannot drive code here.
type Merger struct{}

// NewMerger constructs the default EnvironmentMerger. It is stateless; the constructor
// exists so composition can inject it as a tool.EnvironmentMerger without the forker
// package needing to know about the tool port (the adapter meets the port at
// construction).
func NewMerger() *Merger { return &Merger{} }

// Merge implements tool.EnvironmentMerger. See the Merger type doc for the contract.
func (*Merger) Merge(ctx context.Context, child, parent tool.Environment) error {
	// A zero-value Environment{} (nil Workspace) must surface a normal error,
	// never a nil-pointer panic from dereferencing Workspace().Root(). This is
	// the defense at the merger boundary: NewEnvironment/MustEnvironment enforce
	// non-nil at construction, but a caller can still hand a zero value here.
	if child.Workspace() == nil {
		return errors.New("forker: merge requires a child Environment with a non-nil workspace")
	}
	if parent.Workspace() == nil {
		return errors.New("forker: merge requires a parent Environment with a non-nil workspace")
	}
	parentRoot := parent.Workspace().Root()
	if parentRoot == "" {
		return fmt.Errorf("forker: merge requires a non-empty parent workspace root")
	}
	forkRoot := child.Workspace().Root()
	if forkRoot == "" {
		return fmt.Errorf("forker: merge requires a non-empty fork root")
	}

	// (1) Cheap clean probe on the fork. A clean fork (no working-tree changes)
	// has nothing to merge — return nil without touching the parent. If the fork
	// is NOT a git repo (a non-git workspace — the forker's copy fallback, or a
	// memfs test workspace), the merge is a no-op: there is no git diff to apply
	// and no git ls-files to enumerate untracked files, so the git-based merge
	// concept does not apply. Degrade gracefully (return nil) rather than erroring
	// — the parent's own Edit/Write is the user's tool for a non-git workspace.
	status, err := runGitCapture(ctx, forkRoot, nil, "status", "--porcelain")
	if err != nil {
		// not a git repo / git absent — nothing git-based to merge.
		return nil
	}
	if len(bytes.TrimSpace(status)) == 0 {
		return nil // clean fork — nothing to merge
	}

	if merr := mergeForkInner(ctx, forkRoot, parentRoot); merr != nil {
		// Do NOT force / clean / reset the parent on a partial merge — a partial
		// apply may have already written some files, and the operator needs to
		// see the exact failure state to resolve. Surface the fork path so the
		// operator can inspect/resolve manually.
		return fmt.Errorf("forker: auto-merge of fork %q into parent %q failed: %w "+
			"(the fork is preserved at %q for manual resolution)", forkRoot, parentRoot, merr, forkRoot)
	}
	return nil
}

// mergeForkInner performs steps (2) and (3) of the merge; split out so a failure
// in either has ONE error-reporting path in Merge (the caller surfaces the fork
// path for manual resolution).
func mergeForkInner(ctx context.Context, forkRoot, parentRoot string) error {
	// (2) Tracked + staged + deletions: apply the fork's `git diff HEAD` into the
	// parent. --no-textconv is SECURITY-LOAD-BEARING: the fork's `.git/config` is
	// attacker-authored (a force-copy branch has write to its own .git), and an
	// attacker-installed `diff.<drv>.textconv = /bin/sh -c "..."` would fire during
	// `git diff` — executing an attacker-chosen command on the PARENT host at merge
	// time and letting its stdout masquerade as the merged content. `--no-ext-diff`
	// suppresses only `diff.external`; it does NOT suppress `diff.*.textconv`
	// (verified). `--no-textconv` is the flag that closes it (verified). The scrubbed
	// env (gitenv.Scrub) is defence-in-depth for the fixed keys; --no-textconv closes
	// the attacker-named-driver class the env structurally cannot.
	patch, err := runGitCapture(ctx, forkRoot, nil, "diff", "--no-ext-diff", "--no-textconv", "--binary", "HEAD")
	if err != nil {
		return fmt.Errorf("diff HEAD: %w", err)
	}
	if len(bytes.TrimSpace(patch)) > 0 {
		// SECURITY: reject any change to `.gitattributes` in the patch. An untrusted
		// branch must not silently land attribute changes that repoint the parent's
		// filter/diff drivers — a staged `.gitattributes` adding `filter=<name>` to an
		// existing tracked file would cause the parent's `filter.<name>.smudge` (e.g.
		// git-lfs/git-crypt) to fire on attacker-controlled blob content at merge time.
		// `git apply` does NOT accept --no-ext-diff/--no-textconv (diff-family flags);
		// the scrubbed env neutralises the fixed keys. apply reads the patch from stdin
		// ("-"). --whitespace=nowarn keeps a noisy-but-valid patch from being rejected.
		if patchTouchesGitattributes(patch) {
			return fmt.Errorf("refusing to apply a patch that touches .gitattributes — an untrusted branch may not repoint the parent's git filter/diff drivers (the fork is preserved for manual resolution)")
		}
		// ATOMIC-OR-NOTHING (defense-in-depth): dry-run the apply FIRST (`git apply
		// --check`). `git apply` is ITSELF atomic for the common case — it validates
		// every hunk across every file before writing any, so a multi-file patch whose
		// later file conflicts applies NOTHING (verified: a 2-file patch with one
		// conflicting file leaves the clean file untouched in the parent). So the parent
		// is already protected from partial writes by git apply's own behaviour; the
		// --check pre-pass is belt-and-suspenders — an explicit, intention-revealing
		// pre-validation that keeps the failure path obviously side-effect-free should a
		// future git edge case ever be less strict. It costs one extra git invocation on
		// the (non-hot) merge path. Only once the check passes do we run the real apply.
		if _, cerr := runGitCapture(ctx, parentRoot, patch,
			"apply", "--check", "--whitespace=nowarn", "-"); cerr != nil {
			return fmt.Errorf("apply fork patch: %w", cerr)
		}
		if _, aerr := runGitCapture(ctx, parentRoot, patch,
			"apply", "--whitespace=nowarn", "-"); aerr != nil {
			return fmt.Errorf("apply fork patch: %w", aerr)
		}
	}

	// (3) Untracked, non-ignored files in the fork: copy each into the parent.
	// --exclude-standard honours .gitignore; -z is NUL-separated, robust to
	// spaces. These files are NOT in `git diff HEAD` (they're untracked), so the
	// patch in step (2) does not carry them — they must be copied explicitly.
	others, err := runGitCapture(ctx, forkRoot, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("ls-files --others: %w", err)
	}
	for _, rel := range splitNUL(others) {
		if rel == "" {
			continue
		}
		// SECURITY (FIX B): refuse an UNTRACKED .gitattributes too. The step-(2)
		// patch screen (patchTouchesGitattributes) only covers TRACKED changes; a
		// child that CREATES a new .gitattributes (at the repo root or in any
		// subdir) is untracked and would otherwise bypass the filter/diff-driver
		// repointing defense and land verbatim in the parent. Mirror the patch-side
		// refusal wording and preserve the fork for manual resolution.
		if rel == ".gitattributes" || strings.HasSuffix(rel, "/.gitattributes") {
			return fmt.Errorf("refusing to merge an untracked .gitattributes (%q) — an untrusted branch may not repoint the parent's git filter/diff drivers (the fork is preserved for manual resolution)", rel)
		}
		srcPath := filepath.Join(forkRoot, rel)
		// SKIP symlinks/irregular files: a symlink could point outside the fork,
		// and copying its target would land arbitrary content in the parent
		// (mirrors copyTree/overlayDirtyInner discipline).
		info, lerr := os.Lstat(srcPath)
		if lerr != nil {
			continue // raced away; honour best-effort
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if cerr := copyFile(srcPath, filepath.Join(parentRoot, rel)); cerr != nil {
			return fmt.Errorf("copy untracked %q: %w", rel, cerr)
		}
	}
	return nil
}

// Compile-time assertion that Merger satisfies the seam.
var _ tool.EnvironmentMerger = (*Merger)(nil)

// SerializingMerger wraps an inner tool.EnvironmentMerger with a single mutex so that
// concurrent Merge calls are SERIALIZED — at most one fork's working-tree diff is
// applied to a parent workspace at a time, process-wide.
//
// Why it exists: a merge applies a fork's `git diff HEAD` into a PARENT workspace
// (a write of arbitrary files). Parallel's single-branch auto-merge drives merges,
// and a process may run many sessions concurrently. Without serialization, two
// merges targeting the SAME parent workspace (or two merges sharing any on-disk
// state the inner merger touches) could interleave their `git apply` / file-copy
// writes and corrupt the parent tree. The mutex makes merge-back a process-wide
// critical section: correctness over throughput, which is the right call for a write
// that is already a post-run, off-the-hot-path step.
//
// The decorator is composition-owned: ONE instance is built in Phase A (like the
// fork reaper / shared MCP manager) and injected — as a tool.EnvironmentMerger — into the
// Parallel path (WithAutoMerge), so the SAME mutex serializes across every merge in
// the process. A per-session instance would NOT serialize across sessions, defeating
// the point. It owns its own sync.Mutex (zero-value-ready) and forwards the inner
// result/error verbatim.
type SerializingMerger struct {
	mu    sync.Mutex
	inner tool.EnvironmentMerger
}

// NewSerializingMerger wraps inner so its Merge calls are serialized process-wide
// by a single mutex the returned decorator owns. Construct ONE instance in
// composition and share it across every merge-driving tool.
func NewSerializingMerger(inner tool.EnvironmentMerger) *SerializingMerger {
	return &SerializingMerger{inner: inner}
}

// Merge implements tool.EnvironmentMerger: it takes the decorator's mutex for the whole
// duration of the inner Merge, so at most one merge runs at a time, then returns
// the inner result verbatim.
func (m *SerializingMerger) Merge(ctx context.Context, child, parent tool.Environment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inner.Merge(ctx, child, parent)
}

// Compile-time assertion that SerializingMerger satisfies the seam.
var _ tool.EnvironmentMerger = (*SerializingMerger)(nil)

// patchTouchesGitattributes reports whether the given unified-diff patch touches
// any `.gitattributes` file (at the repo root or any subdirectory). It scans the
// `diff --git a/<path> b/<path>` header lines — a path ending in `.gitattributes`
// (case-sensitive, the git convention) means the patch would add/modify/delete an
// attributes file. The merge refuses such patches: an untrusted branch must not
// silently land attribute changes that repoint the parent's git filter/diff
// drivers (a security-relevant file). The check is on the patch HEADER only
// (cheap, no parse of the hunks) and is conservative: it flags any `.gitattributes`
// anywhere in the tree, not just the root, because a subdirectory attributes file
// also applies to its subtree.
func patchTouchesGitattributes(patch []byte) bool {
	for _, line := range bytes.Split(patch, []byte("\n")) {
		// A diff header looks like: "diff --git a/foo/.gitattributes b/foo/.gitattributes"
		if !bytes.HasPrefix(line, []byte("diff --git ")) {
			continue
		}
		// The trailing path (after "b/") is the canonical destination. Check both
		// the a/ and b/ paths in case of a rename, but the b/ path is the one that
		// would land. A simple suffix check on the whole header line is sufficient
		// and robust to renames.
		if bytes.HasSuffix(line, []byte("/.gitattributes")) ||
			bytes.HasSuffix(line, []byte(" .gitattributes")) ||
			bytes.Contains(line, []byte("/.gitattributes\t")) {
			return true
		}
	}
	return false
}
