package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/adapter/gitenv"
	"github.com/stacklok/mecatl/internal/app"
)

const workspaceSnapshotTimeout = 30 * time.Second
const workspaceStdoutLimit = 8 << 20
const workspaceStderrLimit = 64 << 10

var errWorkspaceGit = errors.New("git inspection failed")
var errWorkspaceOutput = errors.New("git inspection output exceeded its limit")
var errWorkspaceFormat = errors.New("git inspection returned malformed output")

var errWorkspaceUntrusted = errors.New("git inspection unavailable: local workspace is not trusted")
var errWorkspaceFilters = errors.New("git inspection unavailable: repository uses clean/process filters")

// The transport, not the presence of a local-context client, grants this seam.
func workspaceSnapshotterForConfig(cfg config, getter client.LocalSessionContextGetter, sessions client.SessionGetter) *workspaceSnapshotter {
	if cfg.transportMode != modeLocal {
		return nil
	}
	launch, launchErr := os.Stat(cfg.workspace)
	trusted := func(ctx context.Context, sessionID, dir string) bool {
		actual, err := os.Stat(dir)
		if err != nil {
			return false
		}
		// Flags, trust-once, and legacy posture grants cover only the launch
		// directory. Server capabilities describe launch trust, not selected roots.
		if launchErr == nil && os.SameFile(launch, actual) {
			if cfg.trustProject {
				return true
			}
			if sessions != nil {
				session, err := sessions.GetSession(ctx, sessionID)
				if err == nil && session.Capabilities.ProjectTrust == client.ProjectTrusted {
					return true
				}
			}
		}
		return app.ResolveTrust(app.Config{Workspace: dir}).Trusted
	}
	return &workspaceSnapshotter{getter: getter, trusted: trusted, command: exec.CommandContext}
}

type workspaceSnapshotter struct {
	getter  client.LocalSessionContextGetter
	trusted func(context.Context, string, string) bool
	command func(context.Context, string, ...string) *exec.Cmd
}

func (s workspaceSnapshotter) snapshot(ctx context.Context, sessionID string) (result ui.WorkspaceSnapshot, err error) {
	ctx, cancel := context.WithTimeout(ctx, workspaceSnapshotTimeout)
	defer cancel()
	defer func() { result.CapturedAt = time.Now() }()

	dir, err := s.directory(ctx, sessionID)
	if err != nil {
		return result, err
	}
	result.Directory = dir
	if err := s.checkTrust(ctx, sessionID, dir); err != nil {
		return result, err
	}
	git, err := s.inspectGit(ctx, dir)
	if err != nil {
		return result, err
	}
	result.Git = git
	return result, nil
}

func (s workspaceSnapshotter) directory(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" || s.getter == nil {
		return "", errors.New("local workspace directory unavailable")
	}
	dir, err := s.getter.GetLocalSessionContext(ctx, sessionID)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil || !filepath.IsAbs(dir) {
		return "", errors.New("local workspace directory unavailable")
	}
	return dir, nil
}

func (s workspaceSnapshotter) checkTrust(ctx context.Context, sessionID, dir string) error {
	if s.trusted == nil || !s.trusted(ctx, sessionID, dir) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errWorkspaceUntrusted
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func (s workspaceSnapshotter) repository(ctx context.Context, sessionID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, workspaceSnapshotTimeout)
	defer cancel()
	dir, err := s.directory(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if err := s.checkTrust(ctx, sessionID, dir); err != nil {
		return false, err
	}
	root, err := s.repositoryRoot(ctx, dir)
	return root != "" && err == nil, err
}

func (s workspaceSnapshotter) repositoryRoot(ctx context.Context, dir string) (string, error) {
	root, stderr, err := s.git(ctx, dir, "--no-optional-locks", "rev-parse", "--show-toplevel")
	if err != nil {
		var exit *exec.ExitError
		// Do not classify permissions, safe.directory, bare repositories, or other
		// failures as non-repositories. LC_ALL=C makes this one diagnosis stable.
		if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 128 && workspaceNotRepository(string(stderr)) {
			return "", nil
		}
		return "", workspaceCommandError(ctx, err)
	}
	// rev-parse emits the raw path followed by ONE newline, not a quoted line.
	// TrimSpace would corrupt valid directory names ending in whitespace.
	if !bytes.HasSuffix(root, []byte{'\n'}) {
		return "", errWorkspaceFormat
	}
	repoRoot := string(root[:len(root)-1])
	if !filepath.IsAbs(repoRoot) || strings.IndexByte(repoRoot, 0) >= 0 {
		return "", errWorkspaceFormat
	}
	return repoRoot, nil
}

func (s workspaceSnapshotter) inspectGit(ctx context.Context, dir string) (*ui.WorkspaceGitSnapshot, error) {
	repoRoot, err := s.repositoryRoot(ctx, dir)
	if err != nil || repoRoot == "" {
		return nil, err
	}
	// Status may run clean/process filters while hashing files. Inspect effective
	// config (including includes/worktree config) without disabling those filters
	// and misreporting status. Trusted concurrent config replacement is not confined.
	filters, stderr, err := s.git(ctx, dir, "--no-optional-locks", "config", "--null", "--get-regexp", `^filter\..*\.(clean|process)$`)
	if err != nil {
		var exit *exec.ExitError
		if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 || len(filters) != 0 || len(stderr) != 0 {
			return nil, workspaceCommandError(ctx, err)
		}
	}
	if err := workspaceCheckFilters(filters); err != nil {
		return nil, err
	}
	out, _, err := s.git(ctx, dir, "--no-optional-locks", "status", "--porcelain=v2", "-z", "--branch", "--ahead-behind", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return nil, workspaceCommandError(ctx, err)
	}
	git, err := parseWorkspaceStatus(out)
	if err != nil {
		return nil, err
	}
	git.Root = repoRoot
	return git, nil
}

func workspaceCheckFilters(out []byte) error {
	if len(out) == 0 {
		return nil
	}
	if out[len(out)-1] != 0 {
		return errWorkspaceFormat
	}
	values := make(map[string]string)
	for _, record := range strings.Split(string(out[:len(out)-1]), "\x00") {
		key, value, ok := strings.Cut(record, "\n")
		if !ok || !strings.HasPrefix(key, "filter.") || (!strings.HasSuffix(key, ".clean") && !strings.HasSuffix(key, ".process")) {
			return errWorkspaceFormat
		}
		values[key] = value // Git's effective value is the last definition.
	}
	for _, value := range values {
		if value != "" {
			return errWorkspaceFilters
		}
	}
	return nil
}

func workspaceNotRepository(stderr string) bool {
	return stderr == "fatal: not a git repository (or any of the parent directories): .git\n" ||
		(strings.HasPrefix(stderr, "fatal: not a git repository (or any parent up to mount point ") &&
			strings.HasSuffix(stderr, ")\nStopping at filesystem boundary (GIT_DISCOVERY_ACROSS_FILESYSTEM not set).\n"))
}

func workspaceCommandError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, errWorkspaceOutput) {
		return errWorkspaceOutput
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("git executable unavailable")
	}
	return errWorkspaceGit // Never expose stderr, configuration, or raw exec errors.
}

func workspaceGitEnv() []string {
	base := []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SystemRoot", "WINDIR"} {
			if value, ok := os.LookupEnv(key); ok {
				base = append(base, key+"="+value)
			}
		}
	}
	return append(gitenv.Scrub(base), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=")
}

type workspaceLimitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *workspaceLimitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		b.overflow = true
		b.cancel()
		return 0, errWorkspaceOutput
	}
	return b.buffer.Write(p)
}

func (s workspaceSnapshotter) git(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := s.command(child, "git", args...)
	cmd.Dir, cmd.Env = dir, workspaceGitEnv()
	// Bound inherited pipes even if a misbehaving process leaves descendants.
	cmd.WaitDelay = time.Second
	out := &workspaceLimitedBuffer{limit: workspaceStdoutLimit, cancel: cancel}
	stderr := &workspaceLimitedBuffer{limit: workspaceStderrLimit, cancel: cancel}
	cmd.Stdout, cmd.Stderr = out, stderr
	err := cmd.Run()
	// Warnings can mean unreadable/uninspected paths even with exit status zero.
	if err == nil && stderr.buffer.Len() != 0 {
		err = errWorkspaceGit
	}
	if out.overflow || stderr.overflow {
		err = errWorkspaceOutput
	}
	return out.buffer.Bytes(), stderr.buffer.Bytes(), err
}

func parseWorkspaceStatus(out []byte) (*ui.WorkspaceGitSnapshot, error) {
	if len(out) == 0 || out[len(out)-1] != 0 {
		return nil, errWorkspaceFormat
	}
	g := &ui.WorkspaceGitSnapshot{}
	headers := make(map[string]bool)
	records := bytes.Split(out[:len(out)-1], []byte{0})
	for i := 0; i < len(records); i++ {
		record := string(records[i])
		if strings.HasPrefix(record, "# ") {
			if err := parseWorkspaceHeader(g, headers, record); err != nil {
				return nil, err
			}
			continue
		}
		file, last, err := parseWorkspaceFile(records, i)
		if err != nil {
			return nil, err
		}
		i = last
		g.Files = append(g.Files, file)
		workspaceCountFile(&g.Counts, file)
	}
	if !headers["branch.oid"] || !headers["branch.head"] || (g.Unborn && g.Detached) || (g.Divergence != nil && g.Upstream == "") {
		return nil, errWorkspaceFormat
	}
	return g, nil
}

func parseWorkspaceHeader(g *ui.WorkspaceGitSnapshot, headers map[string]bool, record string) error {
	key, value, ok := strings.Cut(record[2:], " ")
	if !ok || value == "" || headers[key] {
		return errWorkspaceFormat
	}
	headers[key] = true
	switch key {
	case "branch.oid":
		g.Unborn = value == "(initial)"
		if !g.Unborn {
			if !workspaceObjectID(value) {
				return errWorkspaceFormat
			}
			g.Head = value
		}
	case "branch.head":
		g.Detached = value == "(detached)"
		if !g.Detached {
			g.Branch = value
		}
	case "branch.upstream":
		g.Upstream = value
	case "branch.ab":
		parts := strings.Split(value, " ")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "+") || !strings.HasPrefix(parts[1], "-") {
			return errWorkspaceFormat
		}
		ahead, e1 := workspaceNumber(parts[0][1:])
		behind, e2 := workspaceNumber(parts[1][1:])
		if e1 != nil || e2 != nil {
			return errWorkspaceFormat
		}
		g.Divergence = &ui.WorkspaceGitDivergence{Ahead: ahead, Behind: behind}
	}
	return nil
}

func parseWorkspaceFile(records [][]byte, i int) (ui.WorkspaceGitFile, int, error) {
	record := string(records[i])
	if len(record) < 3 || record[1] != ' ' {
		return ui.WorkspaceGitFile{}, i, errWorkspaceFormat
	}
	var file ui.WorkspaceGitFile
	switch record[0] {
	case '?':
		file.Path, file.Untracked, file.Index, file.Worktree = record[2:], true, '?', '?'
	case '1', '2', 'u':
		var err error
		file, i, err = parseWorkspaceTrackedFile(records, i)
		if err != nil {
			return ui.WorkspaceGitFile{}, i, err
		}
	default:
		return ui.WorkspaceGitFile{}, i, errWorkspaceFormat
	}
	if file.Path == "" {
		return ui.WorkspaceGitFile{}, i, errWorkspaceFormat
	}
	return file, i, nil
}

func parseWorkspaceTrackedFile(records [][]byte, i int) (ui.WorkspaceGitFile, int, error) {
	record := string(records[i])
	kind := record[0]
	n, modeEnd, oidEnd := 0, 0, 0
	switch kind {
	case '1':
		n, modeEnd, oidEnd = 9, 6, 8
	case '2':
		n, modeEnd, oidEnd = 10, 6, 8
	case 'u':
		n, modeEnd, oidEnd = 11, 7, 10
	}
	fields := strings.SplitN(record, " ", n)
	if len(fields) != n || len(fields[1]) != 2 || !workspaceXY(fields[1]) || !workspaceSubmodule(fields[2]) {
		return ui.WorkspaceGitFile{}, i, errWorkspaceFormat
	}
	for _, mode := range fields[3:modeEnd] {
		if len(mode) != 6 || strings.Trim(mode, "01234567") != "" {
			return ui.WorkspaceGitFile{}, i, errWorkspaceFormat
		}
	}
	for _, oid := range fields[modeEnd:oidEnd] {
		if !workspaceObjectID(oid) {
			return ui.WorkspaceGitFile{}, i, errWorkspaceFormat
		}
	}
	file := ui.WorkspaceGitFile{Path: fields[n-1], Index: fields[1][0], Worktree: fields[1][1], Conflicted: kind == 'u'}
	if kind != '2' {
		return file, i, nil
	}
	score := fields[n-2]
	if len(score) < 2 || !strings.ContainsRune("RC", rune(score[0])) {
		return ui.WorkspaceGitFile{}, i, errWorkspaceFormat
	}
	value, err := workspaceNumber(score[1:])
	if err != nil || value > 100 || i+1 >= len(records) {
		return ui.WorkspaceGitFile{}, i, errWorkspaceFormat
	}
	i++
	file.OriginalPath = string(records[i])
	if file.OriginalPath == "" {
		return ui.WorkspaceGitFile{}, i, errWorkspaceFormat
	}
	return file, i, nil
}

func workspaceCountFile(counts *ui.WorkspaceGitCounts, file ui.WorkspaceGitFile) {
	switch {
	case file.Untracked:
		counts.Untracked++
	case file.Conflicted:
		counts.Conflicted++
	default:
		if file.Index != '.' {
			counts.Staged++
		}
		if file.Worktree != '.' {
			counts.Unstaged++
		}
	}
}

func workspaceNumber(s string) (int, error) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, errWorkspaceFormat
	}
	return strconv.Atoi(s)
}

func workspaceObjectID(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0123456789abcdef") == ""
}

func workspaceXY(s string) bool {
	return strings.Trim(s, ".MTADRCU") == ""
}

func workspaceSubmodule(s string) bool {
	return s == "N..." || (len(s) == 4 && s[0] == 'S' && strings.ContainsRune(".C", rune(s[1])) && strings.ContainsRune(".M", rune(s[2])) && strings.ContainsRune(".U", rune(s[3])))
}
