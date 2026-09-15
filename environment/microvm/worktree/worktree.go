// Package worktree prepares session-owned Git worktrees and guest-local metadata.
package worktree

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/environment/microvm/gitexec"
)

// Fixed guest paths used by the ordered virtio-fs mount plan.
const (
	GuestWorkspace   = "/workspace"
	GuestMetadata    = "/run/mecatl/git-metadata"
	GuestObjectStore = "/run/mecatl/git-objects"

	captureByteLimit   int64 = 256 << 20
	captureEntryLimit        = 100_000
	captureConcurrency       = 2
)

var (
	errCaptureLimit = errors.New("source capture limit exceeded (maximum 256 MiB and 100000 entry records; reduce repository or dirty working-tree size)")
	captureSlots    = make(chan struct{}, captureConcurrency)
)

// Request names the validated source and the new session-owned paths and branch.
type Request struct {
	Source       string
	WorktreePath string
	MetadataPath string
	Branch       string
	// BaseRevision is an optional immutable tree object used for delegated forks.
	// When set, preparation materializes exactly this tree instead of rereading the
	// source checkout's potentially changing dirty state.
	BaseRevision string
}

// Mount describes one host path exposed at a fixed guest path.
type Mount struct {
	HostPath  string
	GuestPath string
	ReadOnly  bool
}

// Prepared is an exact source capture plus the mount plan needed by VM creation.
type Prepared struct {
	SourceRoot           string
	WorktreePath         string
	MetadataPath         string
	Branch               string
	CommonObjectStore    string
	Mounts               []Mount
	beforeCleanupRemoval func()
}

// Preparer creates exact, confined Git-backed session worktrees.
type Preparer struct {
	afterCapture  func() error
	afterWorktree func(string) error
	byteLimit     int64
	entryLimit    int
}

// New returns a worktree preparer using hardened Git subprocesses.
func New() *Preparer {
	return &Preparer{byteLimit: captureByteLimit, entryLimit: captureEntryLimit}
}

// Prepare captures one source state, creates its branch/worktree, validates linked
// metadata, and reconstructs the guest-local Git directory and mount plan.
func (p *Preparer) Prepare(ctx context.Context, req Request) (_ *Prepared, retErr error) { //nolint:gocyclo // explicit acquisition/rollback transaction
	source, common, err := validateSource(ctx, req.Source)
	if err != nil {
		return nil, err
	}
	worktree, worktreeParent, worktreeName, err := newPath(req.WorktreePath, "worktree")
	if err != nil {
		return nil, err
	}
	defer func() { _ = worktreeParent.Close() }()
	metadata, metadataParent, metadataName, err := newPath(req.MetadataPath, "metadata")
	if err != nil {
		return nil, err
	}
	defer func() { _ = metadataParent.Close() }()
	if req.Branch == "" {
		return nil, errors.New("worktree: branch is required")
	}
	if _, err := git(ctx, source, "check-ref-format", "--branch", req.Branch); err != nil {
		return nil, fmt.Errorf("worktree: invalid branch: %w", err)
	}
	select {
	case captureSlots <- struct{}{}:
		defer func() { <-captureSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	captureDir, err := os.MkdirTemp(filepath.Dir(worktree), ".mecatl-source-capture-")
	if err != nil {
		return nil, fmt.Errorf("worktree: create private source capture: %w", err)
	}
	if err := os.Chmod(captureDir, 0o700); err != nil { // #nosec G302 -- source capture holds repository contents and must remain owner-only.
		_ = os.RemoveAll(captureDir)
		return nil, fmt.Errorf("worktree: make source capture private: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(captureDir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("worktree: remove source capture: %w", err))
		}
	}()
	budget := &captureBudget{bytesLeft: p.byteLimit, entriesLeft: p.entryLimit}

	var capture sourceCapture
	if req.BaseRevision == "" {
		capture, err = p.captureSource(ctx, source, captureDir, budget)
	} else {
		capture, err = captureRevision(ctx, source, req.BaseRevision, captureDir, budget)
	}
	if err != nil {
		return nil, err
	}

	created := false
	var sourceDir, worktreeDir *os.File
	var gitAdminParent *os.Root
	var gitAdminName string
	defer func() {
		if retErr != nil && created {
			retErr = errors.Join(retErr,
				wrapRollbackError("remove worktree", worktreeParent.RemoveAll(worktreeName)),
				wrapRollbackError("remove Git metadata", removeRootEntry(gitAdminParent, gitAdminName)),
				wrapRollbackError("remove branch", removeWorktreeBound(context.Background(), sourceDir, req.Branch)),
				wrapRollbackError("remove guest metadata", metadataParent.RemoveAll(metadataName)),
			)
		}
		if sourceDir != nil {
			_ = sourceDir.Close()
		}
		if worktreeDir != nil {
			_ = worktreeDir.Close()
		}
		if gitAdminParent != nil {
			_ = gitAdminParent.Close()
		}
	}()
	if err := createCapturedWorktree(ctx, source, worktree, req.Branch, capture); err != nil {
		return nil, err
	}
	created = true
	sourceDir, err = os.Open(source)
	if err != nil {
		return nil, fmt.Errorf("worktree: bind source for rollback: %w", err)
	}
	worktreeDir, err = worktreeParent.Open(worktreeName)
	if err != nil {
		return nil, fmt.Errorf("worktree: bind target for rollback: %w", err)
	}
	gitAdminParent, gitAdminName, err = bindWorktreeAdmin(worktreeDir, common)
	if err != nil {
		return nil, fmt.Errorf("worktree: bind Git metadata for rollback: %w", err)
	}
	if p.afterWorktree != nil {
		if err := p.afterWorktree(worktree); err != nil {
			return nil, fmt.Errorf("worktree: validation hook: %w", err)
		}
	}

	gitDir, objectStore, err := validateLinkedMetadata(worktree, common)
	if err != nil {
		return nil, err
	}
	if err := p.verifyPreparedCapture(ctx, source, worktree, req.BaseRevision, captureDir, capture); err != nil {
		return nil, err
	}
	if err := reconstructMetadata(ctx, worktree, gitDir, metadata, req.Branch, objectStore); err != nil {
		return nil, fmt.Errorf("worktree: reconstruct guest metadata: %w", err)
	}
	if err := os.RemoveAll(captureDir); err != nil {
		return nil, fmt.Errorf("worktree: remove source capture: %w", err)
	}

	return &Prepared{
		SourceRoot: source, WorktreePath: worktree, MetadataPath: metadata,
		Branch: req.Branch, CommonObjectStore: objectStore,
		Mounts: []Mount{
			{HostPath: worktree, GuestPath: GuestWorkspace},
			{HostPath: metadata, GuestPath: GuestMetadata},
			{HostPath: objectStore, GuestPath: GuestObjectStore, ReadOnly: true},
		},
	}, nil
}

func wrapRollbackError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("worktree: rollback %s: %w", action, err)
}

func removeRootEntry(root *os.Root, name string) error {
	if root == nil {
		return nil
	}
	return root.RemoveAll(name)
}

func (p *Preparer) verifyPreparedCapture(ctx context.Context, source, prepared, revision, captureDir string, capture sourceCapture) error {
	if revision != "" {
		if _, err := git(ctx, prepared, "diff", "--quiet", revision, "--"); err != nil {
			return fmt.Errorf("worktree: prepared state does not match immutable base: %w", err)
		}
		return nil
	}
	for _, candidate := range []struct {
		name string
		root string
		want []byte
	}{
		{name: "recheck source state", root: source, want: capture.state.digest},
		{name: "verify prepared state", root: prepared, want: capture.state.digest},
	} {
		temp, err := os.MkdirTemp(captureDir, "verify-")
		if err != nil {
			return fmt.Errorf("worktree: %s: %w", candidate.name, err)
		}
		budget := &captureBudget{bytesLeft: p.byteLimit, entriesLeft: p.entryLimit}
		got, err := captureStateBounded(ctx, candidate.root, temp, budget, false)
		_ = os.RemoveAll(temp)
		if err != nil {
			return fmt.Errorf("worktree: %s: %w", candidate.name, err)
		}
		if !bytes.Equal(candidate.want, got.digest) {
			if candidate.root == source {
				return errors.New("worktree: source changed during capture")
			}
			return errors.New("worktree: prepared state does not exactly match captured source")
		}
	}
	return nil
}

type sourceCapture struct {
	state            state
	staged, unstaged string
	archive          string
	baseRevision     string
	extractEntries   int
}

type captureBudget struct {
	bytesLeft   int64
	entriesLeft int
}

func (b *captureBudget) entry() error {
	if b.entriesLeft <= 0 {
		return errCaptureLimit
	}
	b.entriesLeft--
	return nil
}

type captureWriter struct {
	budget   *captureBudget
	writer   io.Writer
	exceeded bool
}

func (w *captureWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.budget.bytesLeft {
		w.exceeded = true
		if w.budget.bytesLeft <= 0 {
			return 0, errCaptureLimit
		}
		n, err := w.writer.Write(p[:w.budget.bytesLeft])
		w.budget.bytesLeft -= int64(n)
		return n, errors.Join(err, errCaptureLimit)
	}
	n, err := w.writer.Write(p)
	w.budget.bytesLeft -= int64(n)
	return n, err
}

func captureRevision(ctx context.Context, source, revision, captureDir string, budget *captureBudget) (sourceCapture, error) {
	if !validObjectID(revision) {
		return sourceCapture{}, errors.New("worktree: immutable base revision must be a full object id")
	}
	if _, err := git(ctx, source, "cat-file", "-e", revision+"^{tree}"); err != nil {
		return sourceCapture{}, fmt.Errorf("worktree: resolve immutable base revision: %w", err)
	}
	archive := filepath.Join(captureDir, "committed.tar")
	if err := captureGitFile(ctx, source, archive, budget, "archive", "--format=tar", revision); err != nil {
		return sourceCapture{}, fmt.Errorf("worktree: capture immutable base revision: %w", err)
	}
	return sourceCapture{archive: archive, baseRevision: revision, extractEntries: budget.entriesLeft}, nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (p *Preparer) captureSource(ctx context.Context, source, captureDir string, budget *captureBudget) (sourceCapture, error) {
	captured, err := captureStateBounded(ctx, source, captureDir, budget, true)
	if err != nil {
		return sourceCapture{}, fmt.Errorf("worktree: capture source state: %w", err)
	}
	staged := filepath.Join(captureDir, "staged.diff")
	if err := captureGitFile(ctx, source, staged, budget, "diff", "--cached", "--binary", "--full-index", "--no-renames", "HEAD", "--"); err != nil {
		return sourceCapture{}, fmt.Errorf("worktree: capture staged changes: %w", err)
	}
	unstaged := filepath.Join(captureDir, "unstaged.diff")
	if err := captureGitFile(ctx, source, unstaged, budget, "diff", "--binary", "--full-index", "--no-renames", "--"); err != nil {
		return sourceCapture{}, fmt.Errorf("worktree: capture unstaged changes: %w", err)
	}
	archive := filepath.Join(captureDir, "committed.tar")
	if err := captureGitFile(ctx, source, archive, budget, "archive", "--format=tar", "HEAD"); err != nil {
		return sourceCapture{}, fmt.Errorf("worktree: capture committed tree: %w", err)
	}
	if p.afterCapture != nil {
		if err := p.afterCapture(); err != nil {
			return sourceCapture{}, fmt.Errorf("worktree: capture hook: %w", err)
		}
	}
	return sourceCapture{state: captured, staged: staged, unstaged: unstaged, archive: archive, extractEntries: budget.entriesLeft}, nil
}

func captureGitFile(ctx context.Context, root, path string, budget *captureBudget, args ...string) (retErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	writer := &captureWriter{budget: budget, writer: file}
	if err := gitexec.RunStream(ctx, root, nil, writer, args...); err != nil {
		if writer.exceeded || errors.Is(err, errCaptureLimit) {
			return errCaptureLimit
		}
		return err
	}
	return nil
}

func createCapturedWorktree(ctx context.Context, source, worktree, branch string, capture sourceCapture) (retErr error) {
	if _, err := git(ctx, source, "worktree", "add", "--no-checkout", "-b", branch, worktree, "HEAD"); err != nil {
		return fmt.Errorf("worktree: create linked worktree: %w", err)
	}
	if err := os.Chmod(worktree, 0o700); err != nil { // #nosec G302 -- the worktree root is deliberately owner-only.
		return fmt.Errorf("worktree: make linked worktree private: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = removeWorktree(context.Background(), source, worktree, branch)
		}
	}()
	if err := extractArchive(capture.archive, worktree, capture.extractEntries); err != nil {
		return fmt.Errorf("worktree: extract committed tree: %w", err)
	}
	revision := capture.baseRevision
	if revision == "" {
		revision = "HEAD"
	}
	if _, err := git(ctx, worktree, "read-tree", revision); err != nil {
		return fmt.Errorf("worktree: initialize index: %w", err)
	}
	if err := applyGitFile(ctx, worktree, capture.staged, "--index"); err != nil {
		return fmt.Errorf("worktree: apply staged changes: %w", err)
	}
	if err := applyGitFile(ctx, worktree, capture.unstaged); err != nil {
		return fmt.Errorf("worktree: apply unstaged changes: %w", err)
	}
	if err := materializeUntracked(worktree, capture.state.untracked); err != nil {
		return fmt.Errorf("worktree: materialize untracked files: %w", err)
	}
	return nil
}

func applyGitFile(ctx context.Context, worktree, path string, extra ...string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	args := append([]string{"apply"}, extra...)
	args = append(args, "--binary", "--whitespace=nowarn", "-")
	return gitexec.RunStream(ctx, worktree, file, io.Discard, args...)
}

// Cleanup removes a successfully prepared worktree through the Preparer seam.
func (*Preparer) Cleanup(ctx context.Context, prepared *Prepared) error {
	if prepared == nil {
		return nil
	}
	return prepared.Cleanup(ctx)
}

// Cleanup removes the linked worktree, its session branch, and guest metadata.
func (p *Prepared) Cleanup(ctx context.Context) error {
	bound, err := bindCleanupTarget(ctx, p)
	if err != nil {
		return err
	}
	defer bound.close()
	if p.beforeCleanupRemoval != nil {
		p.beforeCleanupRemoval()
	}
	if err := bound.worktreeParent.RemoveAll(bound.worktreeName); err != nil {
		return err
	}
	if err := bound.gitAdminParent.RemoveAll(bound.gitAdminName); err != nil {
		return err
	}
	if err := removeWorktreeBound(ctx, bound.source, p.Branch); err != nil {
		return err
	}
	return bound.metadataParent.RemoveAll(bound.metadataName)
}

type cleanupTarget struct {
	source, worktree                               *os.File
	worktreeParent, metadataParent, gitAdminParent *os.Root
	worktreeName, metadataName, gitAdminName       string
}

func (b *cleanupTarget) close() {
	_ = b.source.Close()
	_ = b.worktree.Close()
	_ = b.worktreeParent.Close()
	_ = b.metadataParent.Close()
	_ = b.gitAdminParent.Close()
}

func bindCleanupTarget(ctx context.Context, prepared *Prepared) (*cleanupTarget, error) { //nolint:gocyclo // fail-closed descriptor and identity checks stay explicit
	if prepared == nil {
		return nil, errors.New("worktree: cleanup target is nil")
	}
	common, err := cleanupCommonDirectory(ctx, prepared.SourceRoot)
	if err != nil {
		return nil, errors.New("worktree: cleanup source path was replaced or traverses a symlink")
	}
	worktreeParent, err := os.OpenRoot(filepath.Dir(prepared.WorktreePath))
	if err != nil {
		return nil, err
	}
	metadataParent, err := os.OpenRoot(filepath.Dir(prepared.MetadataPath))
	if err != nil {
		_ = worktreeParent.Close()
		return nil, err
	}
	bound := &cleanupTarget{worktreeParent: worktreeParent, metadataParent: metadataParent, worktreeName: filepath.Base(prepared.WorktreePath), metadataName: filepath.Base(prepared.MetadataPath)}
	fail := func(err error) (*cleanupTarget, error) { bound.closePartial(); return nil, err }
	bound.source, err = os.Open(prepared.SourceRoot)
	if err != nil {
		return fail(err)
	}
	bound.worktree, err = worktreeParent.Open(bound.worktreeName)
	if err != nil {
		return fail(err)
	}
	metadataRoot, err := metadataParent.OpenRoot(bound.metadataName)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = metadataRoot.Close() }()
	if err := sameOpenDirectory(bound.source, prepared.SourceRoot); err != nil {
		return fail(errors.New("worktree: cleanup source path changed while binding"))
	}
	if err := sameOpenDirectory(bound.worktree, prepared.WorktreePath); err != nil {
		return fail(errors.New("worktree: cleanup target changed while binding"))
	}
	if err := validateRemovalTarget(ctx, prepared.SourceRoot, prepared.WorktreePath, prepared.Branch); err != nil {
		return fail(err)
	}
	metadata, err := existingCanonicalDirNoSymlink(prepared.MetadataPath)
	if err != nil || metadata != prepared.MetadataPath {
		return fail(errors.New("worktree: cleanup metadata path was replaced or traverses a symlink"))
	}
	if err := sameOpenRoot(metadataRoot, prepared.MetadataPath); err != nil {
		return fail(errors.New("worktree: cleanup metadata changed while binding"))
	}
	bound.gitAdminParent, bound.gitAdminName, err = bindWorktreeAdmin(bound.worktree, common)
	if err != nil {
		return fail(fmt.Errorf("worktree: bind cleanup Git metadata: %w", err))
	}
	headInfo, err := metadataRoot.Lstat("HEAD")
	if err != nil || !headInfo.Mode().IsRegular() {
		return fail(errors.New("worktree: cleanup metadata HEAD is not a regular file"))
	}
	head, err := metadataRoot.ReadFile("HEAD")
	if err != nil || string(head) != "ref: refs/heads/"+prepared.Branch+"\n" {
		return fail(errors.New("worktree: cleanup metadata does not belong to the prepared branch"))
	}
	branchOut, err := gitexec.RunInDir(ctx, bound.worktree, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return fail(fmt.Errorf("worktree: cleanup target branch changed while binding: %w", err))
	}
	if strings.TrimSpace(string(branchOut)) != prepared.Branch {
		return fail(errors.New("worktree: cleanup target branch changed while binding"))
	}
	return bound, nil
}

func (b *cleanupTarget) closePartial() {
	if b.source != nil {
		_ = b.source.Close()
	}
	if b.worktree != nil {
		_ = b.worktree.Close()
	}
	if b.worktreeParent != nil {
		_ = b.worktreeParent.Close()
	}
	if b.metadataParent != nil {
		_ = b.metadataParent.Close()
	}
	if b.gitAdminParent != nil {
		_ = b.gitAdminParent.Close()
	}
}

func sameOpenDirectory(opened *os.File, path string) error {
	boundInfo, err := opened.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.IsDir() || !os.SameFile(boundInfo, pathInfo) {
		return errors.New("directory identity changed")
	}
	return nil
}

func sameOpenRoot(opened *os.Root, path string) error {
	boundInfo, err := opened.Stat(".")
	if err != nil {
		return err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.IsDir() || !os.SameFile(boundInfo, pathInfo) {
		return errors.New("directory identity changed")
	}
	return nil
}

func bindWorktreeAdmin(worktree *os.File, common string) (*os.Root, string, error) {
	root, err := os.OpenRoot(fmt.Sprintf("/dev/fd/%d", worktree.Fd()))
	if err != nil {
		return nil, "", err
	}
	data, err := root.ReadFile(".git")
	_ = root.Close()
	if err != nil {
		return nil, "", err
	}
	const prefix = "gitdir: "
	gitDir := strings.TrimSpace(strings.TrimPrefix(string(data), prefix))
	adminParent := filepath.Join(common, "worktrees")
	if !strings.HasPrefix(string(data), prefix) || filepath.Dir(gitDir) != adminParent || filepath.Base(gitDir) == "." {
		return nil, "", errors.New("linked-worktree administrative path escaped the common Git directory")
	}
	parent, err := os.OpenRoot(adminParent)
	if err != nil {
		return nil, "", err
	}
	return parent, filepath.Base(gitDir), nil
}

func removeWorktreeBound(ctx context.Context, source *os.File, branch string) error {
	if source == nil {
		return errors.New("worktree: cleanup descriptors are unavailable")
	}
	_, pruneErr := gitexec.RunInDir(ctx, source, nil, "worktree", "prune")
	_, branchErr := gitexec.RunInDir(ctx, source, nil, "branch", "-D", branch)
	return errors.Join(pruneErr, branchErr)
}

func removeWorktree(ctx context.Context, source, worktree, branch string) error {
	if err := validateRemovalTarget(ctx, source, worktree, branch); err != nil {
		return err
	}
	if _, err := git(ctx, source, "worktree", "remove", "--force", worktree); err != nil {
		return err
	}
	_, pruneErr := git(ctx, source, "worktree", "prune")
	_, branchErr := git(ctx, source, "branch", "-D", branch)
	return errors.Join(pruneErr, branchErr)
}

func validateRemovalTarget(ctx context.Context, source, worktree, branch string) error {
	common, err := cleanupCommonDirectory(ctx, source)
	if err != nil {
		return errors.New("worktree: cleanup source path was replaced or traverses a symlink")
	}
	validatedWorktree, err := existingCanonicalDirNoSymlink(worktree)
	if err != nil || validatedWorktree != worktree {
		return errors.New("worktree: cleanup target was replaced or traverses a symlink")
	}
	if _, _, err := validateLinkedMetadata(validatedWorktree, common); err != nil {
		return fmt.Errorf("worktree: cleanup target ownership: %w", err)
	}
	branchOut, err := git(ctx, validatedWorktree, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(string(branchOut)) != branch {
		return errors.New("worktree: cleanup target does not belong to the prepared branch")
	}
	return nil
}

func cleanupCommonDirectory(ctx context.Context, source string) (string, error) {
	validatedSource, err := existingCanonicalDirNoSymlink(source)
	if err != nil || validatedSource != source {
		return "", errors.New("cleanup source is not an unchanged real directory")
	}
	if root, common, err := validateSource(ctx, source); err == nil && root == source {
		return common, nil
	}
	commonOut, err := git(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common, err := existingCanonicalDirNoSymlink(strings.TrimSpace(string(commonOut)))
	if err != nil || common != source {
		return "", errors.New("cleanup source is neither a worktree root nor its common Git directory")
	}
	objects, err := existingCanonicalDirNoSymlink(filepath.Join(common, "objects"))
	if err != nil || !within(common, objects) {
		return "", errors.New("cleanup source has no confined object store")
	}
	if err := rejectExternalObjectAlternates(objects); err != nil {
		return "", err
	}
	return common, nil
}

type capturedFile struct {
	path     string
	mode     fs.FileMode
	data     []byte
	dataPath string
}

type state struct {
	digest    []byte
	untracked []capturedFile
}

func captureState(ctx context.Context, root string) (state, error) {
	temp, err := os.MkdirTemp(filepath.Dir(root), ".mecatl-state-capture-")
	if err != nil {
		return state{}, err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	return captureStateBounded(ctx, root, temp, &captureBudget{bytesLeft: captureByteLimit, entriesLeft: captureEntryLimit}, false)
}

func captureStateBounded(ctx context.Context, root, captureDir string, budget *captureBudget, retainUntracked bool) (state, error) {
	stagedPath := filepath.Join(captureDir, "tracked.list")
	if err := captureGitFile(ctx, root, stagedPath, budget, "ls-files", "--stage", "-z"); err != nil {
		return state{}, err
	}
	untrackedPath := filepath.Join(captureDir, "untracked.list")
	if err := captureGitFile(ctx, root, untrackedPath, budget, "ls-files", "--others", "--exclude-standard", "-z"); err != nil {
		return state{}, err
	}
	h := sha256.New()
	paths := make([]string, 0)
	if err := forEachZeroRecord(stagedPath, func(record []byte) error {
		if err := budget.entry(); err != nil {
			return err
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return errors.New("malformed staged entry")
		}
		fields := strings.Fields(string(record[:tab]))
		if len(fields) != 3 || fields[2] != "0" {
			return fmt.Errorf("unsupported unmerged index entry %q", record)
		}
		if fields[0] == "160000" {
			return errors.New("submodules are unsupported for exact source capture")
		}
		if strings.Trim(fields[1], "0") == "" {
			return errors.New("intent-to-add entries are unsupported for exact source capture")
		}
		path := string(record[tab+1:])
		if err := validRelative(path); err != nil {
			return err
		}
		writeDigest(h, record)
		paths = append(paths, path)
		return nil
	}); err != nil {
		return state{}, err
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, err := readCaptured(root, path, true, "", budget, h); err != nil {
			return state{}, err
		}
	}

	var untrackedPaths []string
	if err := forEachZeroRecord(untrackedPath, func(raw []byte) error {
		if err := budget.entry(); err != nil {
			return err
		}
		path := string(raw)
		if err := validRelative(path); err != nil {
			return err
		}
		untrackedPaths = append(untrackedPaths, path)
		return nil
	}); err != nil {
		return state{}, err
	}
	sort.Strings(untrackedPaths)
	untracked := make([]capturedFile, 0, len(untrackedPaths))
	for _, path := range untrackedPaths {
		payload := ""
		if retainUntracked {
			payload = filepath.Join(captureDir, fmt.Sprintf("untracked-%d", len(untracked)))
		}
		file, err := readCaptured(root, path, false, payload, budget, h)
		if err != nil {
			return state{}, err
		}
		untracked = append(untracked, file)
	}
	return state{digest: h.Sum(nil), untracked: untracked}, nil
}

func forEachZeroRecord(path string, visit func([]byte) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		record, err := reader.ReadSlice(0)
		if errors.Is(err, io.EOF) && len(record) == 0 {
			return nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			return errCaptureLimit
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(record) == 0 || record[len(record)-1] != 0 {
			return errors.New("unterminated Git path record")
		}
		if err := visit(record[:len(record)-1]); err != nil {
			return err
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func readCaptured(root, path string, tracked bool, payload string, budget *captureBudget, digest io.Writer) (capturedFile, error) {
	full := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) && tracked {
		file := capturedFile{path: path}
		writeCapturedMetadata(digest, file)
		writeDigest(digest, nil)
		return file, nil
	}
	if err != nil {
		return capturedFile{}, fmt.Errorf("inspect %q: %w", path, err)
	}
	file := capturedFile{path: path, mode: info.Mode(), dataPath: payload}
	writeCapturedMetadata(digest, file)
	switch {
	case info.Mode().IsRegular():
		if info.Size() < 0 || info.Size() > budget.bytesLeft {
			return capturedFile{}, errCaptureLimit
		}
		input, openErr := os.Open(full)
		if openErr != nil {
			err = openErr
			break
		}
		defer func() { _ = input.Close() }()
		writeDigestSize(digest, info.Size())
		writer := digest
		var output *os.File
		if payload != "" {
			output, err = os.OpenFile(payload, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				break
			}
			defer func() { _ = output.Close() }()
			writer = io.MultiWriter(digest, output)
		}
		written, copyErr := io.CopyN(&captureWriter{budget: budget, writer: writer}, input, info.Size())
		if copyErr != nil || written != info.Size() {
			if errors.Is(copyErr, errCaptureLimit) {
				return capturedFile{}, errCaptureLimit
			}
			err = errors.Join(copyErr, io.ErrUnexpectedEOF)
		}
	case info.Mode()&os.ModeSymlink != 0:
		var target string
		target, err = os.Readlink(full)
		if int64(len(target)) > budget.bytesLeft {
			return capturedFile{}, errCaptureLimit
		}
		budget.bytesLeft -= int64(len(target))
		file.data = []byte(target)
		writeDigest(digest, file.data)
	default:
		return capturedFile{}, fmt.Errorf("unsupported file type at %q", path)
	}
	if err != nil {
		return capturedFile{}, fmt.Errorf("read %q: %w", path, err)
	}
	return file, nil
}

func materializeUntracked(root string, files []capturedFile) error {
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if file.mode&os.ModeSymlink != 0 {
			if err := os.Symlink(string(file.data), path); err != nil {
				return err
			}
			continue
		}
		input, err := os.Open(file.dataPath)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, file.mode.Perm())
		if err == nil {
			_, err = io.Copy(output, input)
		}
		err = errors.Join(err, outputClose(output), input.Close())
		if err != nil {
			return err
		}
	}
	return nil
}

func outputClose(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func validateSource(ctx context.Context, source string) (string, string, error) {
	root, err := existingCanonicalDir(source)
	if err != nil {
		return "", "", fmt.Errorf("worktree: source: %w", err)
	}
	topOut, err := git(ctx, root, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("worktree: source is not a Git worktree: %w", err)
	}
	top, err := existingCanonicalDir(strings.TrimSpace(string(topOut)))
	if err != nil || top != root {
		return "", "", errors.New("worktree: source must be the registered worktree root")
	}
	list, err := git(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil || !listedWorktree(list, root) {
		return "", "", errors.New("worktree: source is not registered in its repository")
	}
	commonOut, err := git(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", fmt.Errorf("worktree: resolve common Git directory: %w", err)
	}
	common, err := existingCanonicalDirNoSymlink(strings.TrimSpace(string(commonOut)))
	if err != nil {
		return "", "", fmt.Errorf("worktree: common Git directory: %w", err)
	}
	objects, err := existingCanonicalDirNoSymlink(filepath.Join(common, "objects"))
	if err != nil || !within(common, objects) {
		return "", "", errors.New("worktree: common object store is not a confined directory")
	}
	if err := rejectExternalObjectAlternates(objects); err != nil {
		return "", "", err
	}
	return root, common, nil
}

func listedWorktree(out []byte, root string) bool {
	for _, field := range splitZero(out) {
		line := string(field)
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		candidate, err := existingCanonicalDir(strings.TrimPrefix(line, "worktree "))
		if err == nil && candidate == root {
			return true
		}
	}
	return false
}

func validateLinkedMetadata(worktree, expectedCommon string) (string, string, error) {
	dotGit := filepath.Join(worktree, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("worktree: linked .git must be a regular file")
	}
	content, err := os.ReadFile(dotGit)
	if err != nil {
		return "", "", fmt.Errorf("worktree: read linked .git: %w", err)
	}
	line := strings.TrimSpace(string(content))
	if !strings.HasPrefix(line, "gitdir: ") || strings.Contains(strings.TrimSpace(strings.TrimPrefix(line, "gitdir: ")), "\n") {
		return "", "", errors.New("worktree: malformed linked .git file")
	}
	gitDirName := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if !filepath.IsAbs(gitDirName) {
		gitDirName = filepath.Join(worktree, gitDirName)
	}
	gitDir, err := existingCanonicalDirNoSymlink(gitDirName)
	if err != nil || filepath.Dir(gitDir) != filepath.Join(expectedCommon, "worktrees") {
		return "", "", errors.New("worktree: linked Git directory escapes the common worktrees directory")
	}
	commondirPath := filepath.Join(gitDir, "commondir")
	// #nosec G703 -- gitDir is canonical and confined to expectedCommon/worktrees above.
	commondirInfo, err := os.Lstat(commondirPath)
	if err != nil || !commondirInfo.Mode().IsRegular() {
		return "", "", errors.New("worktree: commondir must be a regular file")
	}
	// #nosec G703 -- the same confined regular file was checked without following links.
	commondirBytes, err := os.ReadFile(commondirPath)
	if err != nil {
		return "", "", err
	}
	commondirName := strings.TrimSpace(string(commondirBytes))
	if commondirName == "" || filepath.IsAbs(commondirName) {
		return "", "", errors.New("worktree: commondir must be relative")
	}
	resolvedCommon, err := existingCanonicalDirNoSymlink(filepath.Join(gitDir, commondirName))
	if err != nil || resolvedCommon != expectedCommon {
		return "", "", errors.New("worktree: commondir escapes the validated common Git directory")
	}
	objects, err := existingCanonicalDirNoSymlink(filepath.Join(resolvedCommon, "objects"))
	if err != nil || !within(resolvedCommon, objects) {
		return "", "", errors.New("worktree: object store escapes the validated common Git directory")
	}
	if err := rejectExternalObjectAlternates(objects); err != nil {
		return "", "", err
	}
	return gitDir, objects, nil
}

func rejectExternalObjectAlternates(objects string) error {
	for _, name := range []string{"alternates", "http-alternates"} {
		path := filepath.Join(objects, "info", name)
		// #nosec G703 -- objects is canonical and confined by both callers.
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("worktree: object-store %s is not a confined regular file", name)
		}
		// #nosec G703 -- the confined file was Lstat-checked without following links.
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("worktree: read object-store %s: %w", name, err)
		}
		if len(bytes.TrimSpace(content)) != 0 {
			return fmt.Errorf("worktree: external object-store %s is unsupported", name)
		}
	}
	return nil
}

func reconstructMetadata(ctx context.Context, worktree, gitDir, metadata, branch, objectStore string) error {
	if err := os.Mkdir(metadata, 0o700); err != nil {
		return err
	}
	formatOut, err := git(ctx, worktree, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	format := strings.TrimSpace(string(formatOut))
	if format != "sha1" && format != "sha256" {
		return fmt.Errorf("unsupported object format %q", format)
	}
	headOut, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	head := strings.TrimSpace(string(headOut))
	refPath := filepath.Join(metadata, filepath.FromSlash("refs/heads/"+branch))
	if !within(metadata, refPath) {
		return errors.New("branch ref escapes guest metadata")
	}
	if err := os.MkdirAll(filepath.Dir(refPath), 0o700); err != nil {
		return err
	}
	config := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tworktree = " + GuestWorkspace + "\n"
	if format == "sha256" {
		config += "[extensions]\n\tobjectformat = sha256\n"
	}
	if err := os.WriteFile(filepath.Join(metadata, "config"), []byte(config), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(metadata, "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(refPath, []byte(head+"\n"), 0o600); err != nil {
		return err
	}
	// #nosec G703 -- gitDir and metadata are confined above.
	if err := copyRegularFile(filepath.Join(gitDir, "index"), filepath.Join(metadata, "index"), captureByteLimit); err != nil {
		return err
	}
	infoDir := filepath.Join(metadata, "objects", "info")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(infoDir, "alternates"), []byte(GuestObjectStore+"\n"), 0o600); err != nil {
		return err
	}
	if _, err := existingCanonicalDirNoSymlink(objectStore); err != nil {
		return err
	}
	return makeGuestMetadataReadable(metadata)
}

func copyRegularFile(source, destination string, limit int64) (retErr error) {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	if info.Size() < 0 || info.Size() > limit {
		return errCaptureLimit
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, input.Close()) }()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, output.Close()) }()
	written, err := io.CopyN(output, input, info.Size())
	if err != nil || written != info.Size() {
		return errors.Join(err, io.ErrUnexpectedEOF)
	}
	return nil
}

func makeGuestMetadataReadable(metadata string) error {
	// The metadata root is below an owner-only daemon state directory on the host,
	// but it must be readable by the fixed unprivileged guest Git capability.
	metadataRoot, err := os.OpenRoot(metadata)
	if err != nil {
		return err
	}
	defer func() { _ = metadataRoot.Close() }()
	return filepath.WalkDir(metadata, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(metadata, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return metadataRoot.Chmod(rel, 0o755)
		}
		return metadataRoot.Chmod(rel, 0o644)
	})
}

func extractArchive(archivePath, root string, entriesLeft int) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	tr := tar.NewReader(archive)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if entriesLeft <= 0 {
			return errCaptureLimit
		}
		entriesLeft--
		if err := extractArchiveEntry(tr, hdr, root); err != nil {
			return err
		}
	}
}

func extractArchiveEntry(tr *tar.Reader, hdr *tar.Header, root string) error {
	name := filepath.ToSlash(hdr.Name)
	if hdr.Typeflag == tar.TypeDir {
		name = strings.TrimSuffix(name, "/")
	}
	if err := validRelative(name); err != nil {
		return err
	}
	mode, err := safeArchiveMode(hdr.Mode)
	if err != nil {
		return fmt.Errorf("archive entry %q: %w", name, err)
	}
	path := filepath.Join(root, filepath.FromSlash(name))
	switch hdr.Typeflag {
	case tar.TypeDir:
		return extractArchiveDirectory(hdr, path, mode, name)
	case tar.TypeReg:
		return extractArchiveRegular(tr, hdr, path, mode)
	case tar.TypeSymlink:
		return extractArchiveSymlink(hdr, path, name)
	case tar.TypeXGlobalHeader, tar.TypeXHeader:
		return nil
	default:
		return fmt.Errorf("unsupported archive entry type for %q", name)
	}
}

func extractArchiveDirectory(hdr *tar.Header, path string, mode fs.FileMode, name string) error {
	if hdr.Size != 0 {
		return fmt.Errorf("archive directory %q has content", name)
	}
	return os.MkdirAll(path, mode)
}

func extractArchiveRegular(tr *tar.Reader, hdr *tar.Header, path string, mode fs.FileMode) error {
	if hdr.Size < 0 || hdr.Size > captureByteLimit {
		return errCaptureLimit
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	written, copyErr := io.CopyN(output, tr, hdr.Size)
	closeErr := output.Close()
	if copyErr != nil || written != hdr.Size {
		return errors.Join(copyErr, closeErr, io.ErrUnexpectedEOF)
	}
	return closeErr
}

func extractArchiveSymlink(hdr *tar.Header, path, name string) error {
	if hdr.Size != 0 {
		return fmt.Errorf("archive symlink %q has content", name)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.Symlink(hdr.Linkname, path)
}

func safeArchiveMode(raw int64) (fs.FileMode, error) {
	if raw < 0 || raw > 0o777 {
		return 0, errors.New("invalid permission mode")
	}
	// #nosec G115 -- the explicit bound above makes this conversion lossless.
	return fs.FileMode(uint32(raw)), nil
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitexec.Run(ctx, dir, nil, args...)
}

func newPath(path, kind string) (string, *os.Root, string, error) {
	if path == "" {
		return "", nil, "", fmt.Errorf("worktree: %s path is required", kind)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nil, "", err
	}
	if _, err := os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", nil, "", fmt.Errorf("worktree: %s path already exists", kind)
		}
		return "", nil, "", err
	}
	parent, err := existingCanonicalDir(filepath.Dir(abs))
	if err != nil {
		return "", nil, "", fmt.Errorf("worktree: %s parent: %w", kind, err)
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return "", nil, "", err
	}
	name := filepath.Base(abs)
	return filepath.Join(parent, name), root, name, nil
}

func existingCanonicalDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("not a directory")
	}
	return filepath.Clean(canonical), nil
}

func existingCanonicalDirNoSymlink(path string) (string, error) {
	// #nosec G703 -- Lstat deliberately inspects the untrusted final component
	// without following it; EvalSymlinks below then rejects any parent symlink.
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("not a real directory")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if filepath.Clean(path) != canonical {
		return "", errors.New("directory path traverses a symlink")
	}
	return canonical, nil
}

func validRelative(path string) error {
	if path == "" || filepath.IsAbs(path) || path == "." || path == ".git" || strings.HasPrefix(path, ".git/") {
		return fmt.Errorf("unsafe repository path %q", path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("unsafe repository path %q", path)
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func splitZero(data []byte) [][]byte {
	parts := bytes.Split(data, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func writeDigest(w io.Writer, data []byte) {
	writeDigestSize(w, int64(len(data)))
	_, _ = w.Write(data)
}

func writeDigestSize(w io.Writer, length int64) {
	var size [8]byte
	// #nosec G115 -- callers reject negative lengths.
	binary.BigEndian.PutUint64(size[:], uint64(length))
	_, _ = w.Write(size[:])
}

func writeCapturedMetadata(w io.Writer, file capturedFile) {
	writeDigest(w, []byte(file.path))
	writeDigest(w, []byte(file.mode.String()))
}
