package microvm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/stacklok/mecatl/environment/microvm/gitexec"
)

func captureForkBase(ctx context.Context, parent string) (string, error) {
	first, err := captureForkBaseOnce(ctx, parent)
	if err != nil {
		return "", err
	}
	second, err := captureForkBaseOnce(ctx, parent)
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("parent worktree changed while capturing microvm fork base")
	}
	return first, nil
}

func captureForkBaseOnce(ctx context.Context, parent string) (string, error) {
	gitDirOut, err := gitexec.Run(ctx, parent, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(gitDirOut))
	index, err := os.CreateTemp(gitDir, ".mecatl-fork-index-*")
	if err != nil {
		return "", err
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		_ = os.Remove(indexPath)
		return "", err
	}
	if err := os.Remove(indexPath); err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(indexPath) }()
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := gitexec.RunWithEnv(ctx, parent, nil, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := gitexec.RunWithEnv(ctx, parent, nil, env, "add", "-A", "--"); err != nil {
		return "", err
	}
	base, err := gitexec.RunWithEnv(ctx, parent, nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(base))
	if len(value) != 40 && len(value) != 64 {
		return "", errors.New("git returned an invalid fork-base tree id")
	}
	return value, nil
}

func mergeRepositoryWorktrees(ctx context.Context, parentPath, childPath, base string) error {
	return mergeRepositoryWorktreesWithOwnership(ctx, parentPath, childPath, base, prepareRepositoryOwnership)
}

func mergeRepositoryWorktreesWithOwnership(ctx context.Context, parentPath, childPath, base string, prepare repositoryOwnershipPreparer) error {
	if parentPath == "" || childPath == "" || base == "" || parentPath == childPath {
		return ErrInvalidFork
	}
	if _, err := gitexec.Run(ctx, childPath, nil, "add", "-N", "--all"); err != nil {
		return fmt.Errorf("index microvm child additions: %w", err)
	}
	names, err := gitexec.Run(ctx, childPath, nil, "diff", "--name-only", "-z", base, "--")
	if err != nil {
		return fmt.Errorf("enumerate microvm child changes: %w", err)
	}
	paths := splitNUL(names)
	if len(paths) == 0 {
		return nil
	}
	if err := checkParentMergeBase(ctx, parentPath, base, paths); err != nil {
		return err
	}
	patchArgs := append([]string{"diff", "--binary", base, "--"}, paths...)
	patch, err := gitexec.Run(ctx, childPath, nil, patchArgs...)
	if err != nil {
		return fmt.Errorf("build microvm child patch: %w", err)
	}
	if _, err := gitexec.Run(ctx, parentPath, patch, "apply", "--check", "--binary", "-"); err != nil {
		return fmt.Errorf("%w: %v", ErrMergeConflict, err)
	}
	if err := checkParentMergeBase(ctx, parentPath, base, paths); err != nil {
		return err
	}
	if _, err := gitexec.Run(ctx, parentPath, patch, "apply", "--binary", "-"); err != nil {
		return fmt.Errorf("apply microvm child patch: %w", err)
	}
	if err := prepare(ctx, parentPath, "."); err != nil {
		return fmt.Errorf("refresh guest ownership after patch already applied: %w", err)
	}
	return nil
}

func checkParentMergeBase(ctx context.Context, parent, base string, paths []string) error {
	quietArgs := append([]string{"diff", "--quiet", base, "--"}, paths...)
	if _, err := gitexec.Run(ctx, parent, nil, quietArgs...); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return fmt.Errorf("%w: parent changed a child-modified path", ErrMergeConflict)
		}
		return fmt.Errorf("inspect microvm parent merge conflicts: %w", err)
	}
	return nil
}

func splitNUL(data []byte) []string {
	var result []string
	for _, value := range strings.Split(string(data), "\x00") {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}
