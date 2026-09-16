package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// fakeStateEnv returns a ResolveEnv whose XDG_STATE_HOME points at stateHome, so
// the store resolves models.yaml under a temp dir — fully offline, no real $HOME.
func fakeStateEnv(stateHome string) xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_STATE_HOME" {
				return stateHome
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", os.ErrNotExist },
		ReadFile:    os.ReadFile,
	}
}

// TestSelectionStoreRoundTrip covers save→load for a per-workspace entry, and that a
// pick is scoped to ITS workspace ONLY — an unseen repo falls back to the server
// default (zero selection), never inheriting another workspace's pick.
func TestSelectionStoreRoundTrip(t *testing.T) {
	stateHome := t.TempDir()
	wsA := t.TempDir()
	wsB := t.TempDir()
	store := newSelectionStore(fakeStateEnv(stateHome))

	selA := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	if err := store.Save(wsA, selA); err != nil {
		t.Fatalf("Save(wsA): %v", err)
	}

	// A fresh store (simulating a relaunch) reads the per-workspace entry back.
	store2 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store2.Load(wsA); got != selA {
		t.Fatalf("Load(wsA) = %+v, want %+v", got, selA)
	}
	// wsB has no entry → falls back to the server default (zero selection), NOT wsA's
	// pick. A pick must not leak across workspaces.
	if got := (store2.Load(wsB)); got != (client.ModelSelection{}) {
		t.Fatalf("Load(wsB) = %+v, want the zero selection (server default), not wsA's pick", got)
	}

	// A second Save for wsB updates wsB ONLY, leaving wsA intact and the global
	// default still unset.
	selB := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if err := store2.Save(wsB, selB); err != nil {
		t.Fatalf("Save(wsB): %v", err)
	}
	store3 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store3.Load(wsA); got != selA {
		t.Fatalf("Load(wsA) after wsB save = %+v, want %+v (per-workspace preserved)", got, selA)
	}
	if got := store3.Load(wsB); got != selB {
		t.Fatalf("Load(wsB) = %+v, want %+v", got, selB)
	}
	// A brand-new repo still falls back to the server default — it does NOT inherit
	// the most-recent pick.
	if got := store3.Load(t.TempDir()); got != (client.ModelSelection{}) {
		t.Fatalf("Load(new repo) = %+v, want the zero selection (server default)", got)
	}
}

// TestSelectionStoreReasoningEffortRoundTrip covers save→load of the reasoningEffort
// field (ADR 0055) for BOTH the per-workspace entry and the global default, plus the
// LoadWorkspace path — and that an effort-only selection (no provider/model) survives.
func TestSelectionStoreReasoningEffortRoundTrip(t *testing.T) {
	stateHome := t.TempDir()
	ws := t.TempDir()
	store := newSelectionStore(fakeStateEnv(stateHome))

	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5", ReasoningEffort: "high"}
	if err := store.Save(ws, sel); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A fresh store (relaunch) reads the effort back via both Load and LoadWorkspace.
	store2 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store2.Load(ws); got != sel {
		t.Fatalf("Load = %+v, want %+v (effort must survive)", got, sel)
	}
	got, ok := store2.LoadWorkspace(ws)
	if !ok || got != sel {
		t.Fatalf("LoadWorkspace = %+v ok=%v, want %+v true", got, ok, sel)
	}

	// The global default carries the effort too.
	def := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude", ReasoningEffort: "low"}
	if err := store2.SaveGlobalDefault(def); err != nil {
		t.Fatalf("SaveGlobalDefault: %v", err)
	}
	store3 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store3.LoadGlobalDefault(); got != def {
		t.Fatalf("LoadGlobalDefault = %+v, want %+v (effort must survive)", got, def)
	}

	// An effort-only selection (no provider/model) round-trips.
	wsB := t.TempDir()
	effortOnly := client.ModelSelection{ReasoningEffort: "max"}
	if err := store3.Save(wsB, effortOnly); err != nil {
		t.Fatalf("Save(effort-only): %v", err)
	}
	if got := newSelectionStore(fakeStateEnv(stateHome)).Load(wsB); got != effortOnly {
		t.Fatalf("Load(effort-only) = %+v, want %+v", got, effortOnly)
	}
}

// TestSelectionStoreBackCompatNoEffortKey asserts a file written WITHOUT a
// reasoningEffort key (an old client) loads "" for the effort — backward-compatible
// and fail-soft. It also asserts an unset effort writes NO key (omitempty keeps the
// file clean).
func TestSelectionStoreBackCompatNoEffortKey(t *testing.T) {
	stateHome := t.TempDir()
	ws := t.TempDir()
	store := newSelectionStore(fakeStateEnv(stateHome))

	// Save with an UNSET effort: the file must carry no reasoningEffort key.
	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if err := store.Save(ws, sel); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(stateHome, "mecatui", "models.yaml"))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if strings.Contains(string(data), "reasoningEffort") {
		t.Fatalf("an unset effort must not write a reasoningEffort key (omitempty), got:\n%s", data)
	}

	// And loading such a file yields the empty effort (the auto/unset tier).
	got := newSelectionStore(fakeStateEnv(stateHome)).Load(ws)
	if got.ReasoningEffort != "" {
		t.Fatalf("Load.ReasoningEffort = %q, want empty (back-compat)", got.ReasoningEffort)
	}
	if got != sel {
		t.Fatalf("Load = %+v, want %+v", got, sel)
	}
}

// TestSelectionStoreRealpathKeying asserts a symlinked workspace resolves to the
// SAME entry as its target (realpath-keyed, mirroring the trust registry).
func TestSelectionStoreRealpathKeying(t *testing.T) {
	stateHome := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	store := newSelectionStore(fakeStateEnv(stateHome))

	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if err := store.Save(target, sel); err != nil {
		t.Fatalf("Save(target): %v", err)
	}
	// Loading via the symlink must hit the SAME realpath-keyed entry as the target —
	// verify it's the target's entry and not some other workspace's by also saving a
	// DIFFERENT entry for an unrelated workspace.
	if err := store.Save(t.TempDir(), client.ModelSelection{ProviderID: "openai", ModelID: "other"}); err != nil {
		t.Fatalf("Save(other): %v", err)
	}
	got := newSelectionStore(fakeStateEnv(stateHome)).Load(link)
	if got != sel {
		t.Fatalf("Load(symlink) = %+v, want the target's entry %+v (realpath-keyed)", got, sel)
	}
}

// writeGitWorktreeLayout lays out a minimal git linked-worktree filesystem
// structure under a temp dir: mainRoot/.git (a plain directory, the "ordinary
// checkout") and worktreeRoot/.git (a FILE pointing at
// mainRoot/.git/worktrees/<name>, whose "commondir" sibling points back at
// mainRoot/.git) — exactly what `git worktree add` produces, built by hand so
// the test stays offline (no real git invocation).
func writeGitWorktreeLayout(t *testing.T, mainRoot, worktreeRoot, name string) {
	t.Helper()
	mainGitDir := filepath.Join(mainRoot, ".git")
	if err := os.MkdirAll(mainGitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A real git checkout always has a "config" file; a NON-bare one carries
	// "bare = false" (or omits the key). Without this, isBareGitDir fails closed
	// on the missing file and treats the checkout as bare.
	if err := os.WriteFile(filepath.Join(mainGitDir, "config"), []byte("[core]\n\tbare = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adminDir := filepath.Join(mainGitDir, "worktrees", name)
	if err := os.MkdirAll(adminDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adminDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	pointer := "gitdir: " + adminDir + "\n"
	if err := os.WriteFile(filepath.Join(worktreeRoot, ".git"), []byte(pointer), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSelectionStoreWorktreesShareRepoIdentity asserts that a pick made in a git
// linked worktree lands on the SAME entry as the main checkout — a worktree switch
// is no more a model-relevant event than a branch switch in one checkout — while an
// ordinary (non-worktree) checkout keeps its own realpath keying unchanged.
func TestSelectionStoreWorktreesShareRepoIdentity(t *testing.T) {
	stateHome := t.TempDir()
	repoRoot := t.TempDir()
	worktreeRoot := filepath.Join(t.TempDir(), "wt")
	writeGitWorktreeLayout(t, repoRoot, worktreeRoot, "wt")
	store := newSelectionStore(fakeStateEnv(stateHome))

	// A pick made from inside the WORKTREE...
	sel := client.ModelSelection{ProviderID: "toolhive", ModelID: "gpt-5.6-luna"}
	if err := store.Save(worktreeRoot, sel); err != nil {
		t.Fatalf("Save(worktreeRoot): %v", err)
	}

	// ...is visible from the MAIN checkout root, and vice versa.
	store2 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store2.Load(repoRoot); got != sel {
		t.Fatalf("Load(repoRoot) = %+v, want the worktree's pick %+v (shared repo identity)", got, sel)
	}
	if got := store2.Load(worktreeRoot); got != sel {
		t.Fatalf("Load(worktreeRoot) = %+v, want %+v", got, sel)
	}

	// A pick made from the MAIN checkout updates the SAME shared entry.
	sel2 := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if err := store2.Save(repoRoot, sel2); err != nil {
		t.Fatalf("Save(repoRoot): %v", err)
	}
	store3 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store3.Load(worktreeRoot); got != sel2 {
		t.Fatalf("Load(worktreeRoot) after main-checkout save = %+v, want %+v (one shared identity)", got, sel2)
	}

	// The state file has exactly ONE workspace entry, not two.
	data, err := os.ReadFile(filepath.Join(stateHome, "mecatui", "models.yaml"))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if strings.Count(string(data), "providerId:") != 1 {
		t.Fatalf("expected exactly one persisted entry (shared identity), got:\n%s", data)
	}
}

// writeBareGitWorktreeLayout lays out a linked worktree of a BARE repository:
// bareRepoDir is the repo's own directory (no working tree, "config" marked
// bare=true), containing worktrees/<name>/commondir pointing back to itself.
func writeBareGitWorktreeLayout(t *testing.T, bareRepoDir, worktreeRoot, name string) {
	t.Helper()
	if err := os.MkdirAll(bareRepoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bareRepoDir, "config"), []byte("[core]\n\tbare = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adminDir := filepath.Join(bareRepoDir, "worktrees", name)
	if err := os.MkdirAll(adminDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adminDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	pointer := "gitdir: " + adminDir + "\n"
	if err := os.WriteFile(filepath.Join(worktreeRoot, ".git"), []byte(pointer), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSelectionStoreBareRepoWorktreeDoesNotUnify asserts a worktree of a BARE
// repository does NOT unify onto its common-dir's parent directory — a naive
// "parent of the shared .git" identity would collide every worktree of every
// bare repo sharing that parent (e.g. two bare repos both cloned under
// /repos), since a bare repo has no working-tree root of its own.
func TestSelectionStoreBareRepoWorktreeDoesNotUnify(t *testing.T) {
	stateHome := t.TempDir()
	reposParent := t.TempDir()
	bareA := filepath.Join(reposParent, "a.git")
	bareB := filepath.Join(reposParent, "b.git")
	wtA := filepath.Join(t.TempDir(), "wt-a")
	wtB := filepath.Join(t.TempDir(), "wt-b")
	writeBareGitWorktreeLayout(t, bareA, wtA, "wt-a")
	writeBareGitWorktreeLayout(t, bareB, wtB, "wt-b")
	store := newSelectionStore(fakeStateEnv(stateHome))

	selA := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if err := store.Save(wtA, selA); err != nil {
		t.Fatalf("Save(wtA): %v", err)
	}

	store2 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store2.Load(wtB); got != (client.ModelSelection{}) {
		t.Fatalf("Load(wtB) = %+v, want the zero selection — worktrees of DIFFERENT bare repos under the same parent must not collide", got)
	}
	if got := store2.Load(wtA); got != selA {
		t.Fatalf("Load(wtA) = %+v, want %+v (its own pick, unaffected)", got, selA)
	}
}

// TestSelectionStoreWorktreeNestedSubdirPreservesOffset asserts that a
// workspace nested below a linked worktree's root (e.g. mecatui launched from
// a subdirectory) resolves to the SAME entry as the identical subdirectory
// under the main checkout — not the bare repo root, which would collapse
// every nested subdirectory in a worktree onto one entry.
func TestSelectionStoreWorktreeNestedSubdirPreservesOffset(t *testing.T) {
	stateHome := t.TempDir()
	mainRoot := t.TempDir()
	worktreeRoot := filepath.Join(t.TempDir(), "wt")
	writeGitWorktreeLayout(t, mainRoot, worktreeRoot, "wt")
	for _, dir := range []string{
		filepath.Join(mainRoot, "service-a"),
		filepath.Join(worktreeRoot, "service-a"),
		filepath.Join(worktreeRoot, "service-b"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store := newSelectionStore(fakeStateEnv(stateHome))

	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if err := store.Save(filepath.Join(worktreeRoot, "service-a"), sel); err != nil {
		t.Fatalf("Save(worktree/service-a): %v", err)
	}

	store2 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store2.Load(filepath.Join(mainRoot, "service-a")); got != sel {
		t.Fatalf("Load(main/service-a) = %+v, want %+v (offset preserved across the worktree boundary)", got, sel)
	}
	if got := store2.Load(filepath.Join(worktreeRoot, "service-b")); got != (client.ModelSelection{}) {
		t.Fatalf("Load(worktree/service-b) = %+v, want the zero selection — a DIFFERENT nested subdirectory must not collapse onto the same entry", got)
	}
}

// TestSelectionStoreOrdinaryCheckoutUnaffected asserts an ordinary git checkout
// (a plain ".git" directory, no worktree layer) still keys on its own realpath,
// exactly as before this feature — so existing entries for a main checkout are
// never invalidated by the worktree-unification logic.
func TestSelectionStoreOrdinaryCheckoutUnaffected(t *testing.T) {
	stateHome := t.TempDir()
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := newSelectionStore(fakeStateEnv(stateHome))

	sel := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if err := store.Save(repoRoot, sel); err != nil {
		t.Fatalf("Save(repoRoot): %v", err)
	}
	store2 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store2.Load(other); got != (client.ModelSelection{}) {
		t.Fatalf("Load(other) = %+v, want the zero selection — unrelated repos still don't share an entry", got)
	}
	if got := store2.Load(repoRoot); got != sel {
		t.Fatalf("Load(repoRoot) = %+v, want %+v", got, sel)
	}
}

// TestSelectionStoreSaveGlobalDefault covers the global-default writer: it
// round-trips, is read by LoadGlobalDefault, and PRESERVES the per-workspace map +
// version (read-modify-write touching only the default block).
func TestSelectionStoreSaveGlobalDefault(t *testing.T) {
	stateHome := t.TempDir()
	ws := t.TempDir()
	store := newSelectionStore(fakeStateEnv(stateHome))

	// Seed a per-workspace entry first, so the global save must preserve it.
	wsSel := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	if err := store.Save(ws, wsSel); err != nil {
		t.Fatalf("Save(ws): %v", err)
	}
	def := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if err := store.SaveGlobalDefault(def); err != nil {
		t.Fatalf("SaveGlobalDefault: %v", err)
	}

	// A fresh store (relaunch) reads the global default back AND still has the
	// per-workspace entry — the global write did not clobber it.
	store2 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store2.LoadGlobalDefault(); got != def {
		t.Fatalf("LoadGlobalDefault = %+v, want %+v", got, def)
	}
	if got, ok := store2.LoadWorkspace(ws); !ok || got != wsSel {
		t.Fatalf("LoadWorkspace(ws) = %+v ok=%v, want %+v (global save must preserve workspaces)", got, ok, wsSel)
	}

	// Updating the global default again leaves the per-workspace entry intact.
	def2 := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5-mini"}
	if err := store2.SaveGlobalDefault(def2); err != nil {
		t.Fatalf("SaveGlobalDefault(2): %v", err)
	}
	store3 := newSelectionStore(fakeStateEnv(stateHome))
	if got := store3.LoadGlobalDefault(); got != def2 {
		t.Fatalf("LoadGlobalDefault(2) = %+v, want %+v", got, def2)
	}
	if got, ok := store3.LoadWorkspace(ws); !ok || got != wsSel {
		t.Fatalf("LoadWorkspace(ws) after global update = %+v ok=%v, want %+v", got, ok, wsSel)
	}
}

// TestSelectionStoreLoadPrefersWorkspaceOverDefault asserts Load() precedence: a
// workspace WITH a per-workspace entry resolves to THAT entry (not the global
// default), while an unseen workspace falls back to the global default. LoadWorkspace
// reports the distinction (ok=false for the unseen one) the picker provenance needs.
func TestSelectionStoreLoadPrefersWorkspaceOverDefault(t *testing.T) {
	stateHome := t.TempDir()
	wsWith := t.TempDir()
	wsWithout := t.TempDir()
	store := newSelectionStore(fakeStateEnv(stateHome))

	def := client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if err := store.SaveGlobalDefault(def); err != nil {
		t.Fatalf("SaveGlobalDefault: %v", err)
	}
	wsSel := client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}
	if err := store.Save(wsWith, wsSel); err != nil {
		t.Fatalf("Save(wsWith): %v", err)
	}

	store2 := newSelectionStore(fakeStateEnv(stateHome))
	// The workspace WITH an entry resolves to its entry, NOT the global default.
	if got := store2.Load(wsWith); got != wsSel {
		t.Fatalf("Load(wsWith) = %+v, want the workspace entry %+v (workspace wins over default)", got, wsSel)
	}
	if got, ok := store2.LoadWorkspace(wsWith); !ok || got != wsSel {
		t.Fatalf("LoadWorkspace(wsWith) = %+v ok=%v, want %+v / true", got, ok, wsSel)
	}
	// The unseen workspace falls back to the global default; LoadWorkspace says "no
	// per-workspace entry" (ok=false) so provenance can distinguish the two cases.
	if got := store2.Load(wsWithout); got != def {
		t.Fatalf("Load(wsWithout) = %+v, want the global default %+v", got, def)
	}
	if _, ok := store2.LoadWorkspace(wsWithout); ok {
		t.Fatalf("LoadWorkspace(wsWithout) ok=true, want false (no per-workspace entry)")
	}
}

// TestSelectionStoreFailSoftRead covers the fail-soft read paths: a missing file
// and a malformed file both yield the zero selection, never a crash.
func TestSelectionStoreFailSoftRead(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		store := newSelectionStore(fakeStateEnv(t.TempDir()))
		if got := store.Load(t.TempDir()); !got.IsZero() {
			t.Fatalf("Load on missing file = %+v, want zero", got)
		}
	})
	t.Run("malformed yaml", func(t *testing.T) {
		stateHome := t.TempDir()
		path := filepath.Join(stateHome, stateSubpath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(":\n  not: [valid"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := newSelectionStore(fakeStateEnv(stateHome))
		if got := store.Load(t.TempDir()); !got.IsZero() {
			t.Fatalf("Load on malformed file = %+v, want zero (fail-soft)", got)
		}
	})
	t.Run("wrong version", func(t *testing.T) {
		stateHome := t.TempDir()
		path := filepath.Join(stateHome, stateSubpath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("version: 999\ndefault: {providerId: x, modelId: y}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := newSelectionStore(fakeStateEnv(stateHome))
		if got := store.Load(t.TempDir()); !got.IsZero() {
			t.Fatalf("Load on wrong-version file = %+v, want zero (fail-safe)", got)
		}
	})
}

// TestSelectionStoreAtomicWritePerms asserts the persisted file is owner-only
// (0o600), matching the trust registry discipline.
func TestSelectionStoreAtomicWritePerms(t *testing.T) {
	stateHome := t.TempDir()
	store := newSelectionStore(fakeStateEnv(stateHome))
	if err := store.Save(t.TempDir(), client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(stateHome, stateSubpath))
	if err != nil {
		t.Fatalf("stat models.yaml: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("models.yaml perms = %o, want 0600", perm)
	}
}

// TestSelectionStoreNoStateDir asserts that with no XDG state dir resolvable,
// persistence degrades to a no-op (Load zero, Save errors) rather than crashing or
// writing somewhere bogus.
func TestSelectionStoreNoStateDir(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", os.ErrNotExist },
		ReadFile:    os.ReadFile,
	}
	store := newSelectionStore(env)
	if got := store.Load(t.TempDir()); !got.IsZero() {
		t.Fatalf("Load with no state dir = %+v, want zero", got)
	}
	if err := store.Save(t.TempDir(), client.ModelSelection{ProviderID: "openai"}); err == nil {
		t.Fatal("Save with no state dir should report an error (the ui treats it fail-soft)")
	}
}
