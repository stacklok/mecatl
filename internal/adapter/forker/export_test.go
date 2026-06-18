package forker

import "context"

// SetRunGitForTest overrides the Forker's injected runGit seam (fire-and-forget git
// runner) so a test can wrap or sabotage specific git invocations — e.g. let
// `git worktree add` succeed, then drop a conflicting file into the child dir so the
// dirty-overlay's `git apply` fails, exercising the best-effort fail-soft path.
// Test-only; never called from production code.
func (f *Forker) SetRunGitForTest(fn func(ctx context.Context, dir string, args ...string) error) {
	f.runGit = fn
}

// RunGitForTest returns the Forker's current runGit seam so a wrapper can delegate
// to the real implementation before/after its own side effects.
func (f *Forker) RunGitForTest() func(ctx context.Context, dir string, args ...string) error {
	return f.runGit
}

// CountOverlayGitForTest wraps the package-level runGitCapture (the overlay's git
// runner) with a counting + arg-recording wrapper for the duration of fn, restoring
// the original afterwards. It returns the captured invocations (each as its args
// slice). This lets a test PROVE the cheap-path short-circuit: a clean tree must run
// exactly the one `status --porcelain` probe and NO diff/apply. Test-only.
func CountOverlayGitForTest(fn func()) [][]string {
	orig := runGitCapture
	var calls [][]string
	runGitCapture = func(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return orig(ctx, dir, stdin, args...)
	}
	defer func() { runGitCapture = orig }()
	fn()
	return calls
}
