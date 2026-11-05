package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWorkspaceRepositoryExactRootAndMinimalQuery(t *testing.T) {
	repo := workspaceTestRepo(t)
	workspaceTestCommit(t, repo)
	worktree := filepath.Join(t.TempDir(), "selected-worktree")
	workspaceTestGit(t, repo, "worktree", "add", "-b", "selected", worktree)
	workspaceTestWrite(t, worktree, "nested/file", "untracked")
	dirs := map[string]string{"main": repo, "selected": filepath.Join(worktree, "nested"), "nonrepo": t.TempDir()}
	var ids []string
	var commands int
	s := workspaceSnapshotter{
		getter: workspaceRootGetter(func(ctx context.Context, id string) (string, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > workspaceSnapshotTimeout {
				t.Fatal("root resolution lacks overall deadline")
			}
			ids = append(ids, id)
			return dirs[id], nil
		}),
		trusted: func(_ context.Context, id, dir string) bool {
			if dir != dirs[id] {
				t.Fatalf("trust checked wrong root: %q", dir)
			}
			return true
		},
		command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			commands++
			if name != "git" || !reflect.DeepEqual(args, []string{"--no-optional-locks", "rev-parse", "--show-toplevel"}) {
				t.Fatalf("discovery ran more than a root query: %s %v", name, args)
			}
			return exec.CommandContext(ctx, name, args...)
		},
	}
	for _, id := range []string{"main", "selected", "nonrepo", "main"} {
		got, err := s.repository(t.Context(), id)
		if err != nil || got != (id != "nonrepo") {
			t.Fatalf("session %s: membership=%v err=%v", id, got, err)
		}
	}
	if commands != 4 || !reflect.DeepEqual(ids, []string{"main", "selected", "nonrepo", "main"}) {
		t.Fatalf("queries=%d sessions=%v", commands, ids)
	}
}

func TestWorkspaceRepositoryTrustAndUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	launch := workspaceTestRepo(t)
	selected := workspaceTestRepo(t)
	for _, test := range []struct {
		name, dir string
		rootErr   error
		want      bool
	}{
		{name: "trusted-launch", dir: launch, want: true},
		{name: "untrusted-selected", dir: selected},
		{name: "missing-root", dir: filepath.Join(t.TempDir(), "missing")},
		{name: "no-fs"},
		{name: "debug", rootErr: errors.New("local context unavailable")},
		{name: "relative", dir: "relative"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := workspaceSnapshotterForConfig(config{transportMode: modeLocal, workspace: launch, trustProject: true}, workspaceRootGetter(func(context.Context, string) (string, error) { return test.dir, test.rootErr }), nil)
			if !test.want {
				s.command = func(context.Context, string, ...string) *exec.Cmd {
					t.Fatal("discovery invoked Git without a trusted local root")
					return nil
				}
			}
			got, err := s.repository(t.Context(), "exact-session")
			if got != test.want || (err == nil) != test.want {
				t.Fatalf("membership=%v err=%v", got, err)
			}
		})
	}
	for _, mode := range []transportMode{modeConnect, "external", "debug", ""} {
		if workspaceSnapshotterForConfig(config{transportMode: mode}, nil, nil) != nil {
			t.Fatalf("nonlocal transport %s gained local discovery", mode)
		}
	}
}

func TestWorkspaceRepositoryFailuresAndHardening(t *testing.T) {
	for _, scenario := range []string{"environment", "malformed", "failure", "warning", "stdout-bound", "stderr-bound", "sleep"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("GIT_DIR", "/never-use-ambient-root")
			t.Setenv("HTTPS_PROXY", "https://sentinel.invalid")
			dir := t.TempDir()
			queries := 0
			s := workspaceSnapshotter{getter: workspaceRootGetter(func(context.Context, string) (string, error) { return dir, nil }), trusted: workspaceTestTrusted,
				command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
					queries++
					return workspaceHelperCommand(scenario)(ctx, name, args...)
				}}
			ctx := t.Context()
			if scenario == "sleep" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			got, err := s.repository(ctx, "captured")
			if queries != 1 || got != (scenario == "environment") || (err == nil) != (scenario == "environment") {
				t.Fatalf("queries=%d membership=%v err=%v", queries, got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := workspaceSnapshotter{getter: workspaceRootGetter(func(context.Context, string) (string, error) { return t.TempDir(), nil }), trusted: workspaceTestTrusted,
		command: func(context.Context, string, ...string) *exec.Cmd {
			t.Fatal("canceled discovery invoked Git")
			return nil
		}}
	if got, err := s.repository(ctx, "canceled"); got || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery=%v %v", got, err)
	}
}
