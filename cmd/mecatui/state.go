package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

// state.go is the mecatui CLIENT-SIDE model-selection state file: the last-used
// (provider, model) the /models picker writes and the next launch reads. It lives
// in the cmd/mecatui MAIN (the composition root), NOT in cmd/mecatui/client — the
// client stays proto/grpc-only and never touches os/xdg. main already imports
// internal/app + adapters, so reusing internal/adapter/xdgconfig here is the same
// legal composition-side import as trust.go's internal/app use.
//
// # File: settings-vs-state split
//
// The file is machine-written STATE, so it lives under XDG_STATE_HOME (not the
// human config base) — the same split the workspace-trust feature established
// (trust.yaml is state under XDG, settings.yaml is human config). Path:
// <XDG_STATE_HOME or ~/.local/state>/mecatui/models.yaml.
//
// # Format: per-workspace map + a global default
//
//	version: 1
//	default:
//	  providerId: openai
//	  modelId: gpt-5
//	workspaces:
//	  /abs/realpath/repo-a: { providerId: openrouter, modelId: anthropic/claude-... }
//
// Read order on launch: workspaces[key(ws)] if present, else the global default
// (if ever set), else the zero selection (the server default). Write on select:
// update ONLY the per-workspace entry — the global default is left untouched so an
// unseen/new repo falls back to the server default instead of silently inheriting the
// last pick made elsewhere. (A future explicit "set as default" is the only writer of
// default.)
//
// # Keying: per-repo, not per-checkout
//
// A git WORKTREE is just a second working directory of one repository — no more a
// model-relevant boundary than checking out a different branch in a single checkout
// (which never changes the model either). So the map key is the repo's shared
// identity, not the literal workspace path: for a workspace inside a git linked
// worktree, the key resolves to the MAIN checkout's root (derived by reading the
// worktree's ".git" pointer file back to the shared common ".git" directory — no
// `git` subprocess) PLUS the workspace's own offset below the worktree root, so a
// pick made in ANY worktree of a repo lands on the SAME entry as the identical
// nested path under the main checkout — never collapsing every nested workspace
// in a worktree onto one bare root. A worktree of a BARE repository does NOT
// unify (there is no working-tree root to unify onto; unifying anyway would
// collide unrelated bare repos sharing one parent directory) and keeps its own
// realpath instead. A plain checkout (ordinary ".git" directory, no worktree
// layer) keys on its own realpath exactly as before — unchanged, so existing entries
// for a main checkout are not invalidated by this. A non-git directory falls back to
// its own realpath too. Realpath canonicalization (filepath.Abs + EvalSymlinks)
// applies throughout, mirroring the trust registry, so a moved/symlinked repo doesn't
// fork its state. See stateKey/gitCommonWorktreeRoot.
//
// # Safety
//
// Read is fail-soft: a missing/oversized/malformed file ⇒ the zero selection (the
// server default), never an error that aborts launch. Write is atomic (temp-file +
// rename, 0o600, O_NOFOLLOW on the final-path symlink guard), mirroring the trust
// registry's Remember discipline; a write failure is returned to the caller (the
// ui surfaces it as a muted notice) but never crashes the picker.

// stateVersion is the schema version main writes and expects. A file with a
// different (unknown) version is ignored fail-safe (treated as no persisted state).
const stateVersion = 1

// maxStateBytes caps the models.yaml read so a pathological file can't blow memory;
// over the cap it is ignored fail-safe (no persisted selection). Mirrors the trust
// registry's cap.
const maxStateBytes = 1 << 20 // 1 MiB

// stateSubpath is the state file relative to the XDG state base.
var stateSubpath = filepath.Join("mecatui", "models.yaml")

// modelSelectionEntry is one persisted (provider, model, effort) tuple on disk. The
// reasoningEffort field (ADR 0055) is omitempty + backward-compatible: an old file
// with no key loads "" (the auto/unset tier), and an "auto"/unset pick (the picker
// maps auto→"" before persisting) writes no key, keeping the file clean.
type modelSelectionEntry struct {
	ProviderID      string `yaml:"providerId"`
	ModelID         string `yaml:"modelId"`
	ReasoningEffort string `yaml:"reasoningEffort,omitempty"`
}

// modelStateFile is the on-disk schema of models.yaml.
type modelStateFile struct {
	Version    int                            `yaml:"version"`
	Default    modelSelectionEntry            `yaml:"default,omitempty"`
	Workspaces map[string]modelSelectionEntry `yaml:"workspaces,omitempty"`
}

// selectionStore is the main-owned concrete SelectionStore (ui.SelectionStore): it
// loads the persisted last-used selection at launch and persists a pick. It is
// bound to a resolved state-file path; tests inject a temp path. A "" path means
// no XDG state dir resolved — persistence is then a no-op (Load returns zero, Save
// reports an error the ui treats fail-soft).
type selectionStore struct {
	path string
}

// newSelectionStore resolves the models.yaml path under XDG_STATE_HOME (via the
// shared xdgconfig leaf) and returns a store bound to it. When no state dir
// resolves (no XDG_STATE_HOME and no home) the path is "" — persistence degrades to
// a no-op rather than anchoring state at a bogus path.
func newSelectionStore(env xdgconfig.ResolveEnv) *selectionStore {
	base := xdgconfig.UserStateDir(env)
	if base == "" {
		return &selectionStore{}
	}
	return &selectionStore{path: filepath.Join(base, stateSubpath)}
}

// Load returns the persisted selection for workspace: the per-workspace entry
// (realpath-keyed) if present, else the global default, else the zero selection.
// Fail-soft: a missing/oversized/malformed/wrong-version file yields the zero
// selection (the server default), never an error.
func (s *selectionStore) Load(workspace string) client.ModelSelection {
	if s == nil || s.path == "" {
		return client.ModelSelection{}
	}
	sf, ok := s.read()
	if !ok {
		return client.ModelSelection{}
	}
	if key, err := stateKey(workspace); err == nil {
		if e, found := sf.Workspaces[key]; found {
			return entrySelection(e)
		}
	}
	return entrySelection(sf.Default)
}

// entrySelection is the single on-disk-entry → client.ModelSelection projection,
// threading every persisted field (provider, model, effort) so a new field is added
// in ONE place rather than at each read site.
func entrySelection(e modelSelectionEntry) client.ModelSelection {
	return client.ModelSelection{
		ProviderID:      e.ProviderID,
		ModelID:         e.ModelID,
		ReasoningEffort: e.ReasoningEffort,
	}
}

// selectionEntry is the inverse: a client.ModelSelection → on-disk entry projection,
// the single write-side mirror of entrySelection.
func selectionEntry(sel client.ModelSelection) modelSelectionEntry {
	return modelSelectionEntry{
		ProviderID:      sel.ProviderID,
		ModelID:         sel.ModelID,
		ReasoningEffort: sel.ReasoningEffort,
	}
}

// Save persists sel as the per-workspace entry (realpath-keyed) ONLY. It deliberately
// does NOT touch the global default: an unseen/new repo falls back to the server
// default rather than silently inheriting whatever was last picked elsewhere (which
// could be an expensive model). A future explicit "set as default" affordance is the
// only thing that should write Default. It is read-modify-write: it preserves the
// existing default and every other workspace's entry. A zero (empty/unresolvable)
// workspace persists nothing. The write is atomic.
func (s *selectionStore) Save(workspace string, sel client.ModelSelection) error {
	if s == nil || s.path == "" {
		return errors.New("mecatui: no XDG state dir to persist the model selection")
	}
	sf, _ := s.read() // a corrupt/absent file ⇒ start fresh (never block a write)
	sf.Version = stateVersion
	if sf.Workspaces == nil {
		sf.Workspaces = make(map[string]modelSelectionEntry)
	}
	entry := selectionEntry(sel)
	if key, err := stateKey(workspace); err == nil {
		sf.Workspaces[key] = entry
	}
	out, err := yaml.Marshal(sf)
	if err != nil {
		return err
	}
	return writeStateFile(s.path, out)
}

// LoadWorkspace returns ONLY the per-workspace (realpath-keyed) entry for
// workspace, with ok=false when there is no such entry (so the caller can tell a
// workspace-set selection apart from a global-default one — the /models picker
// provenance line needs that distinction). Fail-soft like Load.
func (s *selectionStore) LoadWorkspace(workspace string) (client.ModelSelection, bool) {
	if s == nil || s.path == "" {
		return client.ModelSelection{}, false
	}
	sf, ok := s.read()
	if !ok {
		return client.ModelSelection{}, false
	}
	key, err := stateKey(workspace)
	if err != nil {
		return client.ModelSelection{}, false
	}
	e, found := sf.Workspaces[key]
	if !found {
		return client.ModelSelection{}, false
	}
	return entrySelection(e), true
}

// LoadGlobalDefault returns the global `default:` block (the zero selection when
// none is set). Fail-soft like Load. The /models picker reads it to mark the ★
// global-default row and to derive the "global default" provenance label.
func (s *selectionStore) LoadGlobalDefault() client.ModelSelection {
	if s == nil || s.path == "" {
		return client.ModelSelection{}
	}
	sf, ok := s.read()
	if !ok {
		return client.ModelSelection{}
	}
	return entrySelection(sf.Default)
}

// SaveGlobalDefault persists sel as the global `default:` block (the model used by
// a NEW/unseen workspace that has no per-workspace entry). It is the explicit "set
// as global default" writer the package header anticipated — the ONLY writer of
// Default — and is invoked by the picker's ctrl+g. It is read-modify-write: it
// preserves the existing per-workspace map and the schema version, touching only the
// default block. The write is atomic (same temp+rename / O_NOFOLLOW discipline as
// Save). A nil store / unresolved state path returns an error the ui surfaces
// fail-soft.
func (s *selectionStore) SaveGlobalDefault(sel client.ModelSelection) error {
	if s == nil || s.path == "" {
		return errors.New("mecatui: no XDG state dir to persist the global default model")
	}
	sf, _ := s.read() // a corrupt/absent file ⇒ start fresh (never block a write)
	sf.Version = stateVersion
	sf.Default = selectionEntry(sel)
	out, err := yaml.Marshal(sf)
	if err != nil {
		return err
	}
	return writeStateFile(s.path, out)
}

// read loads + parses the state file. ok is false (and the file is ignored
// fail-soft) on any read/parse failure or an unknown schema version. The open is
// O_NOFOLLOW (symmetric with the WRITE path's symlink guard, CWE-59): a symlinked
// state path fails the open ⇒ treated as no-state, the same fail-soft as a
// malformed file — a planted symlink can never redirect the read.
func (s *selectionStore) read() (modelStateFile, bool) {
	data, err := readStateFile(s.path)
	if err != nil {
		return modelStateFile{}, false
	}
	if len(data) > maxStateBytes {
		return modelStateFile{}, false
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return modelStateFile{}, false
	}
	document, err := yamldiag.ParseSettingsDocument(data)
	if err != nil {
		return modelStateFile{}, false
	}
	var sf modelStateFile
	if err := document.Decode(document.Mapping(), &sf); err != nil {
		return modelStateFile{}, false
	}
	if sf.Version != stateVersion {
		return modelStateFile{}, false
	}
	return sf, true
}

// realpathState canonicalizes a workspace to its realpath (filepath.Abs +
// EvalSymlinks), symmetric on Load and Save, so a moved/aliased symlink can't fork
// the state. An empty or unresolvable workspace returns an error (the caller then
// skips the per-workspace entry and uses default).
func realpathState(workspace string) (string, error) {
	if workspace == "" {
		return "", errors.New("empty workspace")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// stateKey resolves the models.yaml map key for workspace: the realpath of the
// git repository's MAIN checkout root when workspace is inside a linked git
// worktree (unifying every worktree of one repo onto the single entry the main
// checkout already uses), else the plain realpath (unchanged fallback for an
// ordinary checkout or a non-git directory). An empty workspace errors, same as
// realpathState.
func stateKey(workspace string) (string, error) {
	if root, ok := gitCommonWorktreeRoot(workspace); ok {
		return realpathState(root)
	}
	return realpathState(workspace)
}

// gitCommonWorktreeRoot returns the MAIN checkout's identity for workspace when
// workspace sits inside a git LINKED worktree, and ok=false otherwise (ordinary
// checkout, bare repo, or no git layout found at all — every such case falls
// through to stateKey's plain-realpath behavior, unchanged from before this
// unification existed). Resolved purely by reading git's on-disk worktree
// layout (no `git` subprocess): walk upward from workspace to the nearest
// ".git" entry; a directory ".git" is an ordinary checkout (not a linked
// worktree, so ok=false); a FILE ".git" is a worktree pointer
// ("gitdir: <main>/.git/worktrees/<name>") whose "commondir" sibling names the
// shared ".git" relative to it — its parent directory is the main checkout
// root. The result REJOINS workspace's own offset below the discovered
// worktree root onto that main root, so a nested workspace keeps the SAME
// identity it would have under the main checkout directly (e.g.
// "<worktree>/service-a" and "<main>/service-a" resolve to the same key,
// rather than every nested path collapsing onto the bare main root). Any
// unreadable/malformed layout fails closed (ok=false) so a pick still persists
// per-checkout rather than being silently dropped.
func gitCommonWorktreeRoot(workspace string) (string, bool) {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", false
	}
	dir := abs
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Lstat(gitPath)
		if err == nil {
			if info.IsDir() {
				return "", false // ordinary checkout: keep today's own-realpath keying
			}
			if !info.Mode().IsRegular() {
				return "", false
			}
			mainRoot, ok := mainRootFromWorktreeGitFile(gitPath)
			if !ok {
				return "", false
			}
			rel, err := filepath.Rel(dir, abs)
			if err != nil {
				return "", false
			}
			return filepath.Join(mainRoot, rel), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false // reached the filesystem root: not a git working tree
		}
		dir = parent
	}
}

// mainRootFromWorktreeGitFile reads a linked worktree's ".git" pointer file and
// its "commondir" sibling to recover the main checkout's root directory.
// ok=false when the shared ".git" resolves into a BARE repository: a bare
// repo's directory has no working tree of its own, so its parent is just an
// arbitrary containing folder (e.g. "/repos" for "/repos/foo.git") — treating
// that as the "main root" would collide every worktree of every bare repo
// sharing that parent onto one entry.
func mainRootFromWorktreeGitFile(gitFile string) (string, bool) {
	data, err := os.ReadFile(gitFile) //nolint:gosec // path derived from a filesystem walk, not user/network input
	if err != nil {
		return "", false
	}
	const prefix = "gitdir:"
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	worktreeGitDir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if worktreeGitDir == "" {
		return "", false
	}
	if !filepath.IsAbs(worktreeGitDir) {
		worktreeGitDir = filepath.Join(filepath.Dir(gitFile), worktreeGitDir)
	}
	commonData, err := os.ReadFile(filepath.Join(worktreeGitDir, "commondir")) //nolint:gosec // same trust boundary as above
	if err != nil {
		return "", false
	}
	commonDir := strings.TrimSpace(string(commonData))
	if commonDir == "" {
		return "", false
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreeGitDir, commonDir)
	}
	resolvedCommonDir, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		return "", false
	}
	if isBareGitDir(resolvedCommonDir) {
		return "", false
	}
	return filepath.Dir(resolvedCommonDir), true
}

// bareTrueRE matches git's own "bare = true" marker (case/space-tolerant) in a
// repository's `config` file — the same field `git rev-parse --is-bare-repository`
// reads, so this agrees with git's own notion of bareness rather than guessing
// from directory naming (a bare repo need not be named "*.git").
var bareTrueRE = regexp.MustCompile(`(?im)^\s*bare\s*=\s*true\s*$`)

// isBareGitDir reports whether dir (a resolved shared ".git" directory) belongs
// to a bare repository, by reading its "config" file. Fails closed (true, i.e.
// "treat as bare, don't unify") on any read error — an unreadable config is
// exactly the situation where guessing "it's a normal checkout" risks the
// collision this check exists to prevent.
func isBareGitDir(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "config")) //nolint:gosec // path derived from a filesystem walk, not user/network input
	if err != nil {
		return true
	}
	return bareTrueRE.MatchString(string(data))
}

// readStateFile reads the state file with O_NOFOLLOW, so a symlink at the final
// path fails the open (CWE-59) — symmetric with writeStateFile's symlink guard.
// The caller treats any error fail-soft (no persisted state).
func readStateFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // path derived solely from XDG state base, not repo-controlled
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// writeStateFile atomically replaces the state file with the trust-registry
// discipline: mkdir -p the parent, refuse to write THROUGH a pre-planted symlink at
// the final path (O_NOFOLLOW spirit via an Lstat guard, CWE-59), write to a sibling
// temp file (O_EXCL|0o600), then rename over the target.
func writeStateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return &os.PathError{Op: "open", Path: path, Err: syscall.ELOOP}
	}
	tmp, err := os.CreateTemp(dir, ".models-*.yaml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
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
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename model state: %w", err)
	}
	return nil
}
