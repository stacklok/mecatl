package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/embed"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/app"
)

type workspaceRootGetter func(context.Context, string) (string, error)

func (f workspaceRootGetter) GetLocalSessionContext(ctx context.Context, id string) (string, error) {
	return f(ctx, id)
}

func workspaceTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(workspaceGitEnv(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid", "GIT_AUTHOR_DATE=2025-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2025-01-01T00:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, out)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func workspaceTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	workspaceTestGit(t, dir, "init", "--initial-branch=main", "--template=")
	workspaceTestGit(t, dir, "config", "user.name", "Test")
	workspaceTestGit(t, dir, "config", "user.email", "test@example.invalid")
	return dir
}

func workspaceTestWrite(t *testing.T, dir, name, contents string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func workspaceTestCommit(t *testing.T, dir string) {
	t.Helper()
	workspaceTestGit(t, dir, "add", "--all")
	workspaceTestGit(t, dir, "commit", "--allow-empty", "-m", "fixture")
}

func workspaceTestTrusted(context.Context, string, string) bool { return true }

func workspaceSnapshotForConfig(cfg config, getter client.LocalSessionContextGetter, sessions client.SessionGetter) ui.WorkspaceSnapshotFunc {
	snapshotter := workspaceSnapshotterForConfig(cfg, getter, sessions)
	if snapshotter == nil {
		return nil
	}
	return snapshotter.snapshot
}

func workspaceTestSnapshot(t *testing.T, dir string) ui.WorkspaceSnapshot {
	t.Helper()
	fn := workspaceSnapshotForConfig(config{transportMode: modeLocal, workspace: dir, trustProject: true}, workspaceRootGetter(func(_ context.Context, id string) (string, error) {
		if id != "captured-session" {
			t.Fatalf("session = %q", id)
		}
		return dir, nil
	}), nil)
	start := time.Now()
	snap, err := fn(t.Context(), "captured-session")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Directory != dir || snap.CapturedAt.Before(start) || snap.CapturedAt.After(time.Now()) {
		t.Fatalf("bad snapshot metadata: %+v", snap)
	}
	return snap
}

func TestWorkspaceSnapshotRepositoryStates(t *testing.T) {
	t.Run("nonrepo", func(t *testing.T) {
		if snap := workspaceTestSnapshot(t, t.TempDir()); snap.Git != nil {
			t.Fatalf("nonrepo: %+v", snap.Git)
		}
	})
	t.Run("unborn-clean-detached", func(t *testing.T) {
		dir := workspaceTestRepo(t)
		g := workspaceTestSnapshot(t, dir).Git
		if !g.Unborn || g.Detached || g.Head != "" || g.Branch != "main" || len(g.Files) != 0 || g.Divergence != nil {
			t.Fatalf("unborn: %+v", g)
		}
		workspaceTestCommit(t, dir)
		g = workspaceTestSnapshot(t, dir).Git
		if g.Unborn || g.Head == "" || len(g.Files) != 0 || g.Counts != (ui.WorkspaceGitCounts{}) {
			t.Fatalf("clean: %+v", g)
		}
		workspaceTestGit(t, dir, "checkout", "--detach")
		g = workspaceTestSnapshot(t, dir).Git
		if !g.Detached || g.Branch != "" || g.Head == "" {
			t.Fatalf("detached: %+v", g)
		}
	})
	t.Run("linked-worktree-nested-newline-root", func(t *testing.T) {
		dir := workspaceTestRepo(t)
		workspaceTestCommit(t, dir)
		root := filepath.Join(t.TempDir(), "worktree\n\t")
		workspaceTestGit(t, dir, "worktree", "add", "-b", "linked", root)
		nested := filepath.Join(root, "nested")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		workspaceTestWrite(t, root, "outside-nested", "untracked")
		g := workspaceTestSnapshot(t, nested).Git
		if g.Root != root || g.Branch != "linked" || len(g.Files) != 1 || g.Files[0].Path != "outside-nested" {
			t.Fatalf("worktree: %+v", g)
		}
	})
}

func TestWorkspaceSnapshotFilesAndLocalDivergence(t *testing.T) {
	dir := workspaceTestRepo(t)
	workspaceTestWrite(t, dir, "both", "original\n")
	workspaceTestWrite(t, dir, "source name", "rename me\n")
	workspaceTestWrite(t, dir, "deleted", "delete me\n")
	workspaceTestCommit(t, dir)
	workspaceTestGit(t, dir, "branch", "upstream")
	workspaceTestGit(t, dir, "config", "branch.main.remote", ".")
	workspaceTestGit(t, dir, "config", "branch.main.merge", "refs/heads/upstream")
	workspaceTestGit(t, dir, "config", "status.aheadBehind", "false")
	workspaceTestGit(t, dir, "config", "status.showUntrackedFiles", "no")
	workspaceTestWrite(t, dir, "local-commit", "local\n")
	workspaceTestCommit(t, dir)
	workspaceTestGit(t, dir, "checkout", "upstream")
	workspaceTestWrite(t, dir, "other-commit", "other\n")
	workspaceTestCommit(t, dir)
	workspaceTestGit(t, dir, "checkout", "main")
	workspaceTestWrite(t, dir, "both", "staged\n")
	workspaceTestGit(t, dir, "add", "both")
	workspaceTestWrite(t, dir, "both", "unstaged also\n")
	name := "destination\n\t雪"
	workspaceTestGit(t, dir, "mv", "source name", name)
	if err := os.Remove(filepath.Join(dir, "deleted")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"untracked/a", "untracked/b", "--strange\n\t\x1b[31m\xff"} {
		workspaceTestWrite(t, dir, path, "new")
	}
	g := workspaceTestSnapshot(t, dir).Git
	if g.Upstream != "upstream" || g.Divergence == nil || *g.Divergence != (ui.WorkspaceGitDivergence{Ahead: 1, Behind: 1}) {
		t.Fatalf("divergence: %+v", g)
	}
	if g.Counts != (ui.WorkspaceGitCounts{Staged: 2, Unstaged: 2, Untracked: 3}) {
		t.Fatalf("counts: %+v, files %+v", g.Counts, g.Files)
	}
	files := make(map[string]ui.WorkspaceGitFile)
	for _, f := range g.Files {
		files[f.Path] = f
	}
	if f := files["both"]; f.Index != 'M' || f.Worktree != 'M' {
		t.Fatalf("both: %+v", f)
	}
	if f := files[name]; f.OriginalPath != "source name" || f.Index != 'R' {
		t.Fatalf("rename: %+v", f)
	}
	if f := files["--strange\n\t\x1b[31m\xff"]; !f.Untracked {
		t.Fatalf("raw name: %+v", f)
	}
}

func TestWorkspaceSnapshotConflict(t *testing.T) {
	dir := workspaceTestRepo(t)
	workspaceTestWrite(t, dir, "conflict", "base\n")
	workspaceTestCommit(t, dir)
	workspaceTestGit(t, dir, "checkout", "-b", "other")
	workspaceTestWrite(t, dir, "conflict", "other\n")
	workspaceTestCommit(t, dir)
	workspaceTestGit(t, dir, "checkout", "main")
	workspaceTestWrite(t, dir, "conflict", "main\n")
	workspaceTestCommit(t, dir)
	cmd := exec.CommandContext(t.Context(), "git", "merge", "other")
	cmd.Dir, cmd.Env = dir, workspaceGitEnv()
	if err := cmd.Run(); err == nil {
		t.Fatal("expected merge conflict")
	}
	g := workspaceTestSnapshot(t, dir).Git
	if g.Counts != (ui.WorkspaceGitCounts{Conflicted: 1}) || len(g.Files) != 1 || !g.Files[0].Conflicted || g.Files[0].Index != 'U' || g.Files[0].Worktree != 'U' {
		t.Fatalf("conflict: %+v", g)
	}
}

func TestWorkspaceSnapshotCompositionAndResolution(t *testing.T) {
	dirs := map[string]string{"a": t.TempDir(), "b": t.TempDir()}
	var calls []string
	getter := workspaceRootGetter(func(_ context.Context, id string) (string, error) {
		calls = append(calls, id)
		return dirs[id], nil
	})
	for _, mode := range []transportMode{modeConnect, "external", "debug", ""} {
		if workspaceSnapshotForConfig(config{transportMode: mode}, getter, nil) != nil {
			t.Fatalf("nonlocal %s got collaborator", mode)
		}
	}
	fn := workspaceSnapshotter{getter: getter, trusted: workspaceTestTrusted, command: exec.CommandContext}.snapshot
	for _, id := range []string{"a", "b", "a"} {
		got, err := fn(t.Context(), id)
		if err != nil || got.Directory != dirs[id] {
			t.Fatalf("session %s: %+v %v", id, got, err)
		}
	}
	if !reflect.DeepEqual(calls, []string{"a", "b", "a"}) {
		t.Fatalf("calls: %v", calls)
	}
	for _, dir := range []string{"", "relative", filepath.Join(t.TempDir(), "missing")} {
		fn := workspaceSnapshotter{getter: workspaceRootGetter(func(context.Context, string) (string, error) { return dir, nil }), trusted: workspaceTestTrusted, command: exec.CommandContext}.snapshot
		got, err := fn(t.Context(), "no-fs")
		if err == nil || got.Git != nil || got.CapturedAt.IsZero() {
			t.Fatalf("invalid directory %q: %+v %v", dir, got, err)
		}
	}
	s := workspaceSnapshotter{
		getter: workspaceRootGetter(func(context.Context, string) (string, error) { return "", errors.New("private credential config") }),
		command: func(context.Context, string, ...string) *exec.Cmd {
			t.Fatal("Git invoked after root error")
			return nil
		},
	}
	got, err := s.snapshot(t.Context(), "failed")
	if err == nil || strings.Contains(err.Error(), "private") || got.Directory != "" {
		t.Fatalf("root failure: %+v %v", got, err)
	}
}

// This helper replaces Git without changing the actual execution/environment seam.
// Direct exits avoid the race runtime's exit delay altering subprocess deadlines.
func runWorkspaceSnapshotProcessHelper() {
	args := os.Args
	marker := -1
	for i, arg := range args {
		if arg == "workspace-git-helper" {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	scenario := args[marker+1]
	switch scenario {
	case "stdout-bound":
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", workspaceStdoutLimit+1)))
	case "stderr-bound":
		_, _ = os.Stderr.Write([]byte(strings.Repeat("x", workspaceStderrLimit+1)))
	case "failure":
		_, _ = fmt.Fprint(os.Stderr, "private credential-bearing diagnostic")
		syscall.Exit(128)
	case "warning":
		_, _ = fmt.Fprint(os.Stderr, "warning: incomplete enumeration")
	case "malformed":
		_, _ = fmt.Fprint(os.Stdout, "not porcelain")
	case "canceled":
		ready := os.NewFile(3, "ready")
		if _, err := ready.Write([]byte{1}); err != nil {
			syscall.Exit(48)
		}
		_ = ready.Close()
		time.Sleep(time.Minute)
	case "sleep":
		time.Sleep(time.Minute)
	case "pipes":
		cmd := workspaceHelperCommand("pipe-child")(context.Background(), "git")
		cmd.Env, cmd.Stdout, cmd.Stderr = workspaceGitEnv(), os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			syscall.Exit(46)
		}
		// Exit without waiting: the bounded child holds inherited pipes open.
	case "pipe-child":
		time.Sleep(2 * time.Second)
	case "environment":
		required := map[string]string{
			"LC_ALL": "C", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
			"GIT_PAGER": "cat", "PAGER": "cat", "GIT_CONFIG_COUNT": "4",
			"GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": "/dev/null",
			"GIT_CONFIG_KEY_1": "core.pager", "GIT_CONFIG_VALUE_1": "cat",
			"GIT_CONFIG_KEY_2": "core.fsmonitor", "GIT_CONFIG_VALUE_2": "false",
			"GIT_CONFIG_KEY_3": "diff.external", "GIT_CONFIG_VALUE_3": "",
			"GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0", "GIT_NO_LAZY_FETCH": "1", "GIT_ALLOW_PROTOCOL": "",
		}
		allowed := map[string]bool{"PATH": true, "SystemRoot": runtime.GOOS == "windows", "WINDIR": runtime.GOOS == "windows"}
		for key, value := range required {
			actual, ok := os.LookupEnv(key)
			if !ok || actual != value {
				syscall.Exit(43)
			}
			allowed[key] = true
		}
		for _, kv := range os.Environ() {
			key, _, _ := strings.Cut(kv, "=")
			if !allowed[key] {
				syscall.Exit(42)
			}
		}
		actual := args[marker+2:]
		if len(actual) < 3 || actual[0] != "git" || actual[1] != "--no-optional-locks" {
			syscall.Exit(44)
		}
		switch actual[2] {
		case "rev-parse":
			if !reflect.DeepEqual(actual[2:], []string{"rev-parse", "--show-toplevel"}) {
				syscall.Exit(44)
			}
			dir, err := os.Getwd()
			if err != nil {
				syscall.Exit(47)
			}
			_, _ = fmt.Fprintln(os.Stdout, dir)
		case "config":
			if !reflect.DeepEqual(actual[2:], []string{"config", "--null", "--get-regexp", `^filter\..*\.(clean|process)$`}) {
				syscall.Exit(44)
			}
			syscall.Exit(1) // No matching filters.
		case "status":
			want := []string{"status", "--porcelain=v2", "-z", "--branch", "--ahead-behind", "--untracked-files=all", "--ignore-submodules=all"}
			if !reflect.DeepEqual(actual[2:], want) {
				syscall.Exit(44)
			}
			_, _ = fmt.Fprint(os.Stdout, "# branch.oid (initial)\x00# branch.head main\x00")
		default:
			syscall.Exit(44)
		}
	default:
		syscall.Exit(45)
	}
	syscall.Exit(0)
}

func workspaceHelperCommand(scenario string) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		all := append([]string{"-test.run=^$", "--", "workspace-git-helper", scenario, name}, args...)
		return exec.CommandContext(ctx, os.Args[0], all...)
	}
}

func TestWorkspaceSnapshotCommandFailures(t *testing.T) {
	for _, stage := range []string{"rev-parse", "config", "status"} {
		t.Run(stage, func(t *testing.T) {
			scenarios := []string{"stdout-bound", "stderr-bound", "failure", "warning", "malformed", "pipes", "missing-git"}
			// Cancellation/deadline handling is shared by all Git commands. Exercise
			// it on the first subprocess, without timing prerequisite subprocesses.
			if stage == "rev-parse" {
				scenarios = append(scenarios, "sleep", "canceled")
			}
			for _, scenario := range scenarios {
				t.Run(scenario, func(t *testing.T) {
					dir := t.TempDir()
					if scenario == "missing-git" {
						t.Setenv("PATH", dir)
					}
					s := workspaceSnapshotter{getter: workspaceRootGetter(func(context.Context, string) (string, error) { return dir, nil }), trusted: workspaceTestTrusted}
					var process *exec.Cmd
					var invoked []string
					ctx := t.Context()
					var readyWriter *os.File
					var readiness chan error
					switch scenario {
					case "sleep":
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
						defer cancel()
					case "canceled":
						var cancel context.CancelFunc
						ctx, cancel = context.WithCancel(ctx)
						defer cancel()
						readyReader, writer, err := os.Pipe()
						if err != nil {
							t.Fatal(err)
						}
						defer func() { _ = readyReader.Close() }()
						defer func() { _ = writer.Close() }()
						readyWriter, readiness = writer, make(chan error, 1)
						go func() {
							var ready [1]byte
							_, err := readyReader.Read(ready[:])
							readiness <- err
							if err == nil {
								cancel() // The target child has started and signaled readiness.
							}
						}()
					}
					var start time.Time
					s.command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
						invoked = append(invoked, args[1])
						if args[1] != stage {
							return workspaceHelperCommand("environment")(ctx, name, args...)
						}
						// Bound target cleanup, excluding prerequisite helper startup and race-runtime exits.
						start = time.Now()
						if scenario == "missing-git" {
							return exec.CommandContext(ctx, name, args...)
						}
						process = workspaceHelperCommand(scenario)(ctx, name, args...)
						if readyWriter != nil {
							process.ExtraFiles = []*os.File{readyWriter}
						}
						return process
					}
					got, err := s.snapshot(ctx, "captured")
					if readiness != nil {
						_ = readyWriter.Close()
						if err := <-readiness; err != nil {
							t.Fatalf("target subprocess did not signal readiness: %v", err)
						}
					}
					if err == nil || got.Git != nil || got.CapturedAt.IsZero() || strings.Contains(err.Error(), "private") {
						t.Fatalf("failure trusted/leaked: %+v %v", got, err)
					}
					if invoked[len(invoked)-1] != stage {
						t.Fatalf("continued after failed stage: %v", invoked)
					}
					if strings.HasSuffix(scenario, "bound") && !errors.Is(err, errWorkspaceOutput) {
						t.Fatalf("bound: %v", err)
					}
					if scenario == "sleep" && !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("deadline: %v", err)
					}
					if scenario == "canceled" && !errors.Is(err, context.Canceled) {
						t.Fatalf("cancellation: %v", err)
					}
					if time.Since(start) > 3*time.Second || (process != nil && process.ProcessState == nil) {
						t.Fatalf("process/pipes not released: %v after %v", err, time.Since(start))
					}
				})
			}
		})
	}
}

func TestWorkspaceSnapshotActualEnvironmentAndArgv(t *testing.T) {
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "HTTPS_PROXY", "GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_COUNT", "GIT_SSH_COMMAND", "GIT_EXTERNAL_DIFF", "GIT_TRACE", "AWS_SECRET_ACCESS_KEY", "LD_PRELOAD"} {
		t.Setenv(key, "hostile-sentinel")
	}
	dir := t.TempDir()
	s := workspaceSnapshotter{
		getter: workspaceRootGetter(func(context.Context, string) (string, error) { return dir, nil }), trusted: workspaceTestTrusted,
		command: workspaceHelperCommand("environment"),
	}
	got, err := s.snapshot(t.Context(), "captured")
	if err != nil || got.Git == nil || got.Git.Root != dir || !got.Git.Unborn {
		t.Fatalf("actual child rejected production environment/argv: %+v %v", got, err)
	}
}

func TestWorkspaceSnapshotHostileHelpers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable fixture uses POSIX shebang")
	}
	for _, filter := range []string{"", "clean", "process", "empty-override", "included", "worktree"} {
		t.Run(filter, func(t *testing.T) {
			dir := workspaceTestRepo(t)
			workspaceTestWrite(t, dir, "tracked", "before\n")
			workspaceTestCommit(t, dir)
			helper := filepath.Join(t.TempDir(), "helper")
			if err := os.WriteFile(helper, []byte("#!/bin/sh\n: > \"$0.ran\"\ncat\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"core.fsmonitor", "core.pager", "diff.external", "diff.evil.textconv"} {
				workspaceTestGit(t, dir, "config", key, helper)
			}
			switch filter {
			case "clean", "process":
				workspaceTestGit(t, dir, "config", "filter.evil."+filter, helper)
			case "empty-override":
				workspaceTestGit(t, dir, "config", "--add", "filter.evil.clean", helper)
				workspaceTestGit(t, dir, "config", "--add", "filter.evil.clean", "")
			case "included":
				included := filepath.Join(t.TempDir(), "config")
				workspaceTestGit(t, dir, "config", "--file", included, "filter.evil.clean", helper)
				workspaceTestGit(t, dir, "config", "include.path", included)
			case "worktree":
				workspaceTestGit(t, dir, "config", "extensions.worktreeConfig", "true")
				workspaceTestGit(t, dir, "config", "--worktree", "filter.evil.process", helper)
			}
			workspaceTestWrite(t, dir, ".gitattributes", "tracked diff=evil filter=evil\n")
			workspaceTestWrite(t, dir, "tracked", "after!\n") // Same size forces content hashing.
			if err := os.Chtimes(filepath.Join(dir, "tracked"), time.Unix(100, 0), time.Unix(100, 0)); err != nil {
				t.Fatal(err)
			}
			if filter == "clean" || filter == "" {
				// Ordinary Git must activate the same fixture. Unlike workspaceGitEnv,
				// this environment does not inject helper-disabling config overrides.
				args := []string{"--no-optional-locks"}
				if filter == "clean" {
					// Attribute the marker to the clean filter, not fsmonitor.
					args = append(args, "-c", "core.fsmonitor=false")
				}
				args = append(args, "status", "--porcelain=v2", "-z", "--branch", "--ahead-behind", "--untracked-files=all", "--ignore-submodules=all")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = dir
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL="}
				if err := cmd.Run(); err != nil {
					t.Fatalf("ordinary Git activation control: %v", err)
				}
				if _, err := os.Stat(helper + ".ran"); err != nil {
					t.Fatalf("ordinary Git did not activate %q helper: %v", filter, err)
				}
				if err := os.Remove(helper + ".ran"); err != nil {
					t.Fatal(err)
				}
			}
			fn := workspaceSnapshotForConfig(config{transportMode: modeLocal, workspace: dir, trustProject: true}, workspaceRootGetter(func(context.Context, string) (string, error) { return dir, nil }), nil)
			got, err := fn(t.Context(), "captured")
			blocked := filter != "" && filter != "empty-override"
			if blocked {
				if !errors.Is(err, errWorkspaceFilters) || got.Git != nil || got.Directory != dir {
					t.Fatalf("filter was not rejected: %+v %v", got, err)
				}
			} else if err != nil || got.Git == nil || got.Git.Counts.Unstaged != 1 {
				t.Fatalf("helper-free status failed: %+v %v", got, err)
			}
			if _, err := os.Stat(helper + ".ran"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("inspection executed a repository helper: %v", err)
			}
		})
	}
}

type workspaceSessionGetter func(context.Context, string) (client.SessionSnapshot, error)

func (f workspaceSessionGetter) GetSession(ctx context.Context, id string) (client.SessionSnapshot, error) {
	return f(ctx, id)
}

func TestWorkspaceSnapshotProductionTrust(t *testing.T) {
	// ResolveTrust uses live HOME/XDG lookups; never consult operator files.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	launch := workspaceTestRepo(t)
	other := workspaceTestRepo(t)
	for _, address := range []string{"127.0.0.1:7777", "unix:///looks-local.sock"} {
		cfg := config{transportMode: modeConnect, connectAddress: address, workspace: launch, trustProject: true}
		if workspaceSnapshotForConfig(cfg, workspaceRootGetter(func(context.Context, string) (string, error) {
			t.Fatal("connect transport resolved a local root")
			return "", nil
		}), nil) != nil {
			t.Fatalf("connect %s got local inspection", address)
		}
	}
	for _, tc := range []struct {
		name, root, confirmed string
		flag                  bool
		want                  bool
	}{
		{name: "flag-launch", root: launch, flag: true, want: true},
		{name: "flag-not-other", root: other, flag: true},
		{name: "once-or-legacy-confirmed", root: launch, confirmed: client.ProjectTrusted, want: true},
		{name: "confirmed-not-other", root: other, confirmed: client.ProjectTrusted},
		{name: "untrusted", root: launch, confirmed: client.ProjectUntrusted},
		{name: "unknown", root: launch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getter := workspaceRootGetter(func(context.Context, string) (string, error) { return tc.root, nil })
			sessions := workspaceSessionGetter(func(_ context.Context, id string) (client.SessionSnapshot, error) {
				if id != "captured" {
					t.Fatalf("wrong trust session: %s", id)
				}
				return client.SessionSnapshot{Capabilities: client.Capabilities{ProjectTrust: tc.confirmed}}, nil
			})
			fn := workspaceSnapshotForConfig(config{transportMode: modeLocal, workspace: launch, trustProject: tc.flag}, getter, sessions)
			got, err := fn(t.Context(), "captured")
			if tc.want {
				if err != nil || got.Git == nil {
					t.Fatalf("trusted: %+v %v", got, err)
				}
			} else if !errors.Is(err, errWorkspaceUntrusted) || got.Git != nil || got.Directory != tc.root {
				t.Fatalf("untrusted: %+v %v", got, err)
			}
		})
	}
	// A separately trusted linked worktree is eligible without inheriting the
	// launch grant; independent resolution is performed again on every call.
	workspaceTestCommit(t, launch)
	linked := filepath.Join(t.TempDir(), "linked")
	workspaceTestGit(t, launch, "worktree", "add", "-b", "linked", linked)
	getter := workspaceRootGetter(func(context.Context, string) (string, error) { return linked, nil })
	fn := workspaceSnapshotForConfig(config{transportMode: modeLocal, workspace: launch, trustProject: true}, getter, nil)
	if _, err := fn(t.Context(), "captured"); !errors.Is(err, errWorkspaceUntrusted) {
		t.Fatalf("linked root inherited launch grant: %v", err)
	}
	if err := app.RememberTrust(app.Config{Workspace: linked}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err := fn(t.Context(), "captured"); err != nil || got.Git == nil || got.Git.Root != linked {
		t.Fatalf("remembered linked root: %+v %v", got, err)
	}
	declared := workspaceTestRepo(t)
	workspaceTestWrite(t, os.Getenv("XDG_CONFIG_HOME"), "mecatl/settings.yaml", fmt.Sprintf("trustedWorkspaces:\n  - %q\n", declared))
	declaredFn := workspaceSnapshotForConfig(config{transportMode: modeLocal, workspace: launch}, workspaceRootGetter(func(context.Context, string) (string, error) { return declared, nil }), nil)
	if got, err := declaredFn(t.Context(), "captured"); err != nil || got.Git == nil {
		t.Fatalf("declared alternate root: %+v %v", got, err)
	}
}

func TestWorkspaceSnapshotEmbeddedConfirmedTrust(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := workspaceTestRepo(t)
	// This is the post-prompt embedded config; the caller's original config below
	// intentionally lacks trustProject, as with a trust-once answer in main.go.
	srv, err := embed.Start(t.Context(), app.Config{Workspace: dir, TrustProject: true, UserModelDir: t.TempDir(), UseMock: true, Model: "mock-model", Shell: "/bin/sh", Compaction: "heuristic", Tokenizer: "heuristic", NoSoul: true}, embed.PerfConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	cl, err := client.Dial(client.DialConfig{Server: srv.Target()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	id, _, _, err := cl.CreateSession(t.Context(), client.ModeFromString("default"), client.ModelSelection{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{transportMode: modeLocal, workspace: dir}
	got, err := workspaceSnapshotForConfig(cfg, cl, cl)(t.Context(), id)
	if err != nil || got.Git == nil || got.Git.Root != dir {
		t.Fatalf("embedded confirmed trust: %+v %v", got, err)
	}
	other := workspaceTestRepo(t)
	otherRoot := workspaceRootGetter(func(context.Context, string) (string, error) { return other, nil })
	got, err = workspaceSnapshotForConfig(cfg, otherRoot, cl)(t.Context(), id)
	if !errors.Is(err, errWorkspaceUntrusted) || got.Git != nil || got.Directory != other {
		t.Fatalf("embedded launch grant escaped to alternate root: %+v %v", got, err)
	}
}

func TestWorkspaceSnapshotTrustOrderingAndDeadline(t *testing.T) {
	dir := t.TempDir()
	var order []string
	s := workspaceSnapshotter{
		getter: workspaceRootGetter(func(ctx context.Context, id string) (string, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > workspaceSnapshotTimeout || id != "captured" {
				t.Fatal("missing overall deadline or exact captured session")
			}
			order = append(order, "resolve")
			return dir, nil
		}),
		trusted: func(_ context.Context, id, actual string) bool {
			if id != "captured" || actual != dir {
				t.Fatal("trust did not receive exact root/session")
			}
			order = append(order, "trust")
			return false
		},
		command: func(context.Context, string, ...string) *exec.Cmd { t.Fatal("Git before trust"); return nil },
	}
	got, err := s.snapshot(t.Context(), "captured")
	if !errors.Is(err, errWorkspaceUntrusted) || got.Directory != dir || !reflect.DeepEqual(order, []string{"resolve", "trust"}) {
		t.Fatalf("trust ordering: %+v %v %v", got, err, order)
	}
	ctx, cancel := context.WithCancel(t.Context())
	s.getter = workspaceRootGetter(func(ctx context.Context, _ string) (string, error) { cancel(); <-ctx.Done(); return "", ctx.Err() })
	if _, err := s.snapshot(ctx, "captured"); !errors.Is(err, context.Canceled) {
		t.Fatalf("root cancellation: %v", err)
	}
}

func TestWorkspaceSnapshotSubmoduleScope(t *testing.T) {
	dir := workspaceTestRepo(t)
	workspaceTestCommit(t, dir)
	module := filepath.Join(dir, "module")
	if err := os.Mkdir(module, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceTestGit(t, module, "init", "--initial-branch=main", "--template=")
	workspaceTestWrite(t, module, "tracked", "before\n")
	workspaceTestCommit(t, module)
	head := workspaceTestGit(t, module, "rev-parse", "HEAD")
	workspaceTestWrite(t, dir, ".gitmodules", "[submodule \"module\"]\n\tpath = module\n\turl = disabled://not-contacted\n")
	workspaceTestGit(t, dir, "add", ".gitmodules")
	workspaceTestGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
	workspaceTestGit(t, dir, "commit", "-m", "gitlink")
	workspaceTestGit(t, dir, "config", "submodule.module.ignore", "none")
	workspaceTestWrite(t, module, "tracked", "second\n")
	workspaceTestCommit(t, module)
	next := workspaceTestGit(t, module, "rev-parse", "HEAD")
	workspaceTestWrite(t, module, "tracked", "third!\n")
	workspaceTestWrite(t, module, "untracked", "nested untracked\n")
	// Nested config must never become an inspection/helper invocation. A required
	// unavailable filter would fail any content inspection inside the submodule.
	workspaceTestGit(t, module, "config", "filter.nested.clean", "must-not-execute-nested-helper")
	workspaceTestGit(t, module, "config", "filter.nested.required", "true")
	workspaceTestWrite(t, module, ".gitattributes", "* filter=nested\n")
	indexPath := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	g := workspaceTestSnapshot(t, dir).Git
	if len(g.Files) != 0 || g.Counts != (ui.WorkspaceGitCounts{}) {
		t.Fatalf("inspected nested working tree/HEAD: %+v", g)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("read-only status changed index: %v", err)
	}
	workspaceTestGit(t, dir, "update-index", "--cacheinfo", "160000,"+next+",module")
	if raw := workspaceTestGit(t, dir, "diff", "--cached", "--raw", "--ignore-submodules=none"); !strings.Contains(raw, "M\tmodule") {
		t.Fatalf("fixture did not stage a gitlink change: %q", raw)
	}
	g = workspaceTestSnapshot(t, dir).Git
	// With the fixed --ignore-submodules=all, this Git version also omits staged
	// gitlinks. Do not add a second status pass to manufacture recursive totals.
	if len(g.Files) != 0 || g.Counts != (ui.WorkspaceGitCounts{}) {
		t.Fatalf("fixed submodule scope: %+v", g)
	}
}

func TestWorkspaceSnapshotRepositoryFailures(t *testing.T) {
	for _, scenario := range []string{"bare", "malformed-config", "unreadable-root"} {
		t.Run(scenario, func(t *testing.T) {
			dir := workspaceTestRepo(t)
			switch scenario {
			case "bare":
				workspaceTestGit(t, dir, "config", "core.bare", "true")
			case "malformed-config":
				workspaceTestWrite(t, dir, ".git/config", "[credential-bearing-sentinel\n")
			case "unreadable-root":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses directory permissions")
				}
				if err := os.Chmod(dir, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			fn := workspaceSnapshotForConfig(config{transportMode: modeLocal, workspace: dir, trustProject: true}, workspaceRootGetter(func(context.Context, string) (string, error) { return dir, nil }), nil)
			got, err := fn(t.Context(), "captured")
			if err == nil || got.Git != nil || got.Directory != dir || got.CapturedAt.IsZero() || strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("failure classified as non-repository or leaked config: %+v %v", got, err)
			}
		})
	}
}

func TestWorkspaceSnapshotFilterParsing(t *testing.T) {
	for _, tc := range []struct {
		data string
		err  error
	}{
		{data: ""},
		{data: "filter.evil.clean\n\x00"},
		{data: "filter.evil.clean\ncommand\x00filter.evil.clean\n\x00"},
		{data: "filter.evil.clean\ncommand\x00", err: errWorkspaceFilters},
		{data: "filter.evil.process\ncommand\nsecond line\x00", err: errWorkspaceFilters},
		{data: "filter.evil.clean\ncommand", err: errWorkspaceFormat},
		{data: "filter.evil.clean\x00", err: errWorkspaceFormat},
		{data: "not-a-filter\nvalue\x00", err: errWorkspaceFormat},
	} {
		if err := workspaceCheckFilters([]byte(tc.data)); !errors.Is(err, tc.err) {
			t.Fatalf("filter output %q: %v, want %v", tc.data, err, tc.err)
		}
	}
}

func TestWorkspaceSnapshotMalformedStatus(t *testing.T) {
	header := "# branch.oid (initial)\x00# branch.head main\x00"
	for _, data := range []string{"", "\x00", header[:len(header)-1], "? file\x00", header + "? \x00", header + "garbage\x00", header + "# branch.head duplicate\x00", header + "# branch.ab +1 -2\x00", header + "# branch.oid invalid\x00", header + "1 MM N... 100644 100644 100644 invalid invalid file\x00", header + "2 R. N... 100644 100644 100644 " + strings.Repeat("0", 40) + " " + strings.Repeat("0", 40) + " R100 destination\x00"} {
		if got, err := parseWorkspaceStatus([]byte(data)); err == nil || got != nil {
			t.Fatalf("accepted malformed status %q: %+v %v", data, got, err)
		}
	}
}
