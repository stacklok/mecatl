package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// tui_multi_session_delete_test.go holds the real-git Scenario 3 proofs of
// docs/acceptance/tui-multi-session.md (ADR 0374 Decision 4). The stop/lease
// proofs live in internal/adapter/server/tui_multi_session_delete_test.go.

type tmsRemovalFixture struct {
	svc      *server.Service
	provider *localPlacementProvider
	store    *jsonlstore.Store
	base     string
	sibling  string
}

func newTMSRemovalFixture(t *testing.T) *tmsRemovalFixture {
	t.Helper()
	base, sibling := tmsRepo(t)
	provider := tmsProvider(t, base)
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc, err := newTestServerService(server.Config{
		Engine: noopEngine(), Store: store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return &tmsRemovalFixture{svc: svc, provider: provider, store: store, base: base, sibling: sibling}
}

func (f *tmsRemovalFixture) rootSession(t *testing.T) *session.Session {
	t.Helper()
	sess, err := f.svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func (f *tmsRemovalFixture) worktreeSession(t *testing.T) *session.Session {
	t.Helper()
	sess, err := f.svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithNewWorktree())
	if err != nil {
		t.Fatalf("create worktree session: %v", err)
	}
	return sess
}

// selectorFor returns the source-scoped selector of the worktree at path.
func (f *tmsRemovalFixture) selectorFor(t *testing.T, source session.SessionID, path string) string {
	t.Helper()
	choices, err := f.svc.ListWorktreesForSession(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	want, ok := f.provider.listedWorktree(context.Background(), path)
	if !ok {
		t.Fatalf("%s is not listed", path)
	}
	current, err := f.provider.worktrees.List(context.Background(), f.base)
	if err != nil || len(current) != len(choices) {
		t.Fatalf("listing drift: %v (%d vs %d)", err, len(current), len(choices))
	}
	for i, choice := range current {
		if choice.Path == want.Path {
			return choices[i].Selector
		}
	}
	t.Fatalf("no selector for %s", path)
	return ""
}

func (f *tmsRemovalFixture) delete(t *testing.T, id session.SessionID, stopActive bool) (*mecatlv1.DeleteSessionResponse, error) {
	t.Helper()
	return server.NewHarnessServer(f.svc).DeleteSession(context.Background(), &mecatlv1.DeleteSessionRequest{SessionId: string(id), StopActive: stopActive, RemoveWorktree: true})
}

func (f *tmsRemovalFixture) assertKept(t *testing.T, sess *session.Session) {
	t.Helper()
	got, err := f.store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("session deleted by a refused removal: %v", err)
	}
	if got.State != sess.State {
		t.Fatalf("refused removal changed state %s -> %s", sess.State, got.State)
	}
	if _, err := os.Stat(sess.EnvironmentRef.ID); err != nil {
		t.Fatalf("worktree touched by a refused removal: %v", err)
	}
}

func TestTUIMultiSession_Scenario3_RemoveCleanWorktree(t *testing.T) {
	f := newTMSRemovalFixture(t)
	sess := f.worktreeSession(t)
	path := sess.EnvironmentRef.ID
	name := filepath.Base(path)
	// Ignored files go with the worktree; they do not make it dirty.
	wtWriteFile(t, filepath.Join(path, ".gitignore"), "build/\n")
	wsRunGit(t, path, "add", ".gitignore")
	wsCommit(t, path, "ignore build output")
	wtWriteFile(t, filepath.Join(path, "build", "out.bin"), "artifact\n")

	resp, err := f.delete(t, sess.ID, false)
	if err != nil {
		t.Fatalf("DeleteSession{remove_worktree}: %v", err)
	}
	if !resp.GetWorktreeRemoved() || resp.GetWorktreeRetainedReason() != "" {
		t.Fatalf("response = %+v, want worktree_removed", resp)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree directory still present: %v", err)
	}
	if listed := tmsGitOutput(t, f.base, "worktree", "list", "--porcelain"); strings.Contains(listed, name) {
		t.Fatalf("worktree still registered:\n%s", listed)
	}
	if branches := tmsBranches(t, f.base); !strings.Contains(branches, "mecatl/"+name) {
		t.Fatalf("branch mecatl/%s not kept: %q", name, branches)
	}
	if _, err := f.store.Load(context.Background(), sess.ID); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("session not deleted: %v", err)
	}
}

func TestTUIMultiSession_Scenario3_RemoveWorktreeRefusesUnsafe(t *testing.T) {
	ctx := context.Background()
	const (
		dirty      = "uncommitted or untracked changes"
		notCreated = "not created by the server"
		shared     = "another session or schedule uses the worktree"
	)
	refuse := func(t *testing.T, f *tmsRemovalFixture, sess *session.Session, why string) {
		t.Helper()
		_, err := f.delete(t, sess.ID, true)
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), why) {
			t.Fatalf("unsafe removal = %v, want FAILED_PRECONDITION (%s)", err, why)
		}
		f.assertKept(t, sess)
	}

	t.Run("dirty", func(t *testing.T) {
		f := newTMSRemovalFixture(t)
		sess := f.worktreeSession(t)
		wtWriteFile(t, filepath.Join(sess.EnvironmentRef.ID, "untracked.txt"), "wip\n")
		refuse(t, f, sess, dirty)
	})

	t.Run("configured root and user worktree", func(t *testing.T) {
		f := newTMSRemovalFixture(t)
		root := f.rootSession(t)
		refuse(t, f, root, notCreated)
		moved, err := f.svc.ClearSessionSuccessor(ctx, root.ID, server.SuccessorPlacement{Selector: f.selectorFor(t, root.ID, f.sibling), SelectorPresent: true})
		if err != nil {
			t.Fatal(err)
		}
		sess, err := f.store.Load(ctx, moved)
		if err != nil {
			t.Fatal(err)
		}
		refuse(t, f, sess, notCreated)
	})

	t.Run("symlink under the managed root", func(t *testing.T) {
		f := newTMSRemovalFixture(t)
		_ = f.worktreeSession(t) // materializes the managed root
		link := filepath.Join(f.provider.creator.managedRoot, "mecatl-0000beef")
		if err := os.Symlink(f.sibling, link); err != nil {
			t.Fatal(err)
		}
		sess := f.rootSession(t)
		sess.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: link, Revision: localWorktreePlacementRevision}
		if err := f.store.Save(ctx, sess); err != nil {
			t.Fatal(err)
		}
		refuse(t, f, sess, notCreated)
		if _, err := os.Stat(filepath.Join(f.sibling, "README")); err != nil {
			t.Fatalf("user worktree touched: %v", err)
		}
	})

	t.Run("shared with a successor", func(t *testing.T) {
		f := newTMSRemovalFixture(t)
		cleared := f.worktreeSession(t)
		if _, err := f.svc.ClearSessionSuccessor(ctx, cleared.ID, server.SuccessorPlacement{}); err != nil {
			t.Fatal(err)
		}
		refuse(t, f, cleared, shared)
		forked := f.worktreeSession(t)
		if _, err := f.svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: forked.ID}); err != nil {
			t.Fatal(err)
		}
		refuse(t, f, forked, shared)
	})

	t.Run("shared with a schedule", func(t *testing.T) {
		f := newTMSRemovalFixture(t)
		sess := f.worktreeSession(t)
		if err := f.store.ScheduleStore().Save(ctx, port.Schedule{Spec: port.ScheduleSpec{
			Name: "nightly", Prompt: "run", EnvironmentRef: sess.EnvironmentRef, PlacementScope: "test",
		}}); err != nil {
			t.Fatal(err)
		}
		refuse(t, f, sess, shared)
	})

	t.Run("shared with a persisted child", func(t *testing.T) {
		f := newTMSRemovalFixture(t)
		sess := f.worktreeSession(t)
		child := session.New("subagent-child", session.ModeDefault, sess.EnvironmentRef, session.Limits{}, time.Now())
		child.Kind = session.SessionKindSubagent
		child.Relationship = session.SessionRelationship{ParentSessionID: "someone-else", CallID: "c1"}
		if err := f.store.Save(ctx, child); err != nil {
			t.Fatal(err)
		}
		refuse(t, f, sess, shared)
	})
}

func TestTUIMultiSession_Scenario3_RemovalHoldsPathLock(t *testing.T) {
	for _, op := range []string{"clear", "fork"} {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			f := newTMSRemovalFixture(t)
			source := f.rootSession(t)
			doomed := f.worktreeSession(t)
			selector := f.selectorFor(t, source.ID, doomed.EnvironmentRef.ID)

			removing, release := make(chan struct{}), make(chan struct{})
			git := f.provider.creator.git
			f.provider.creator.git = func(ctx context.Context, dir string, args ...string) (string, error) {
				if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
					close(removing)
					<-release
				}
				return git(ctx, dir, args...)
			}

			deleted := make(chan error, 1)
			go func() {
				resp, err := f.delete(t, doomed.ID, false)
				if err == nil && !resp.GetWorktreeRemoved() {
					err = errors.New("worktree not removed: " + resp.GetWorktreeRetainedReason())
				}
				deleted <- err
			}()
			<-removing

			bound := make(chan error, 1)
			go func() {
				placement := server.SuccessorPlacement{Selector: selector, SelectorPresent: true}
				var err error
				if op == "clear" {
					_, err = f.svc.ClearSessionSuccessor(ctx, source.ID, placement)
				} else {
					_, err = f.svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: source.ID, Placement: placement})
				}
				bound <- err
			}()
			select {
			case err := <-bound:
				t.Fatalf("%s returned while removal held the path lock: %v", op, err)
			case <-time.After(200 * time.Millisecond):
			}
			close(release)
			if err := <-deleted; err != nil {
				t.Fatalf("removal: %v", err)
			}
			if err := <-bound; err == nil {
				t.Fatalf("%s bound a removed worktree", op)
			}
		})
	}
}

func TestTUIMultiSession_Scenario3_InventoryReportsRemoveWorktree(t *testing.T) {
	ctx := context.Background()
	f := newTMSRemovalFixture(t)
	root := f.rootSession(t)
	unshared := f.worktreeSession(t)
	shared := f.worktreeSession(t)
	forkID, err := f.svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: shared.ID})
	if err != nil {
		t.Fatal(err)
	}
	userSource := f.rootSession(t)
	userWorktree, err := f.svc.ClearSessionSuccessor(ctx, userSource.ID, server.SuccessorPlacement{Selector: f.selectorFor(t, userSource.ID, f.sibling), SelectorPresent: true})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := server.NewHarnessServer(f.svc).ListSessions(ctx, &mecatlv1.ListSessionsRequest{PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		ok     bool
		reason string
	}{
		string(root.ID):       {false, "not_server_created"},
		string(userSource.ID): {false, "not_server_created"},
		string(userWorktree):  {false, "not_server_created"},
		string(unshared.ID):   {true, ""},
		string(shared.ID):     {false, "shared"},
		string(forkID):        {false, "shared"},
	}
	for _, row := range resp.GetSessions() {
		expected, ok := want[row.GetSessionId()]
		if !ok {
			continue
		}
		delete(want, row.GetSessionId())
		caps := row.GetCapabilities()
		if caps.GetRemoveWorktree() != expected.ok || caps.GetReasons().GetRemoveWorktree() != expected.reason {
			t.Errorf("row %s remove_worktree = (%v, %q), want (%v, %q)", row.GetSessionId(), caps.GetRemoveWorktree(), caps.GetReasons().GetRemoveWorktree(), expected.ok, expected.reason)
		}
	}
	if len(want) != 0 {
		t.Fatalf("rows missing from the inventory: %v", want)
	}

	// A server that cannot remove worktrees reports unavailable.
	plain, err := newTestServerService(server.Config{Engine: noopEngine(), Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	page, err := plain.ListSessionPage(ctx, server.ListSessionsPageRequest{PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Sessions {
		if row.Capabilities.RemoveWorktree || row.Reasons.RemoveWorktree != server.CapabilityReasonUnavailable {
			t.Fatalf("row %s without a remover = (%v, %q)", row.SessionID, row.Capabilities.RemoveWorktree, row.Reasons.RemoveWorktree)
		}
	}
}

type tmsCountingLister struct {
	server.WorktreeLister
	calls atomic.Int64
}

func (l *tmsCountingLister) List(ctx context.Context, root string) ([]server.Worktree, error) {
	l.calls.Add(1)
	return l.WorktreeLister.List(ctx, root)
}

// TestTUIMultiSession_Scenario3_InventoryRunsNoGitPerRow pins that listing a
// page of server-created worktree rows runs no git at all — in particular no
// `git status` cleanliness check — and that cleanliness is checked at delete.
func TestTUIMultiSession_Scenario3_InventoryRunsNoGitPerRow(t *testing.T) {
	ctx := context.Background()
	f := newTMSRemovalFixture(t)
	const rows = 4
	sessions := make([]*session.Session, rows)
	for i := range sessions {
		sessions[i] = f.worktreeSession(t)
	}
	var gitCalls, statusCalls atomic.Int64
	realGit := f.provider.creator.git
	f.provider.creator.git = func(ctx context.Context, dir string, args ...string) (string, error) {
		gitCalls.Add(1)
		if len(args) > 0 && args[0] == "status" {
			statusCalls.Add(1)
		}
		return realGit(ctx, dir, args...)
	}
	lister := &tmsCountingLister{WorktreeLister: f.provider.worktrees}
	f.provider.worktrees = lister

	resp, err := server.NewHarnessServer(f.svc).ListSessions(ctx, &mecatlv1.ListSessionsRequest{PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	removable := 0
	for _, row := range resp.GetSessions() {
		if row.GetCapabilities().GetRemoveWorktree() {
			removable++
		}
	}
	if removable != rows {
		t.Fatalf("removable rows = %d, want %d", removable, rows)
	}
	if gitCalls.Load() != 0 || lister.calls.Load() != 0 {
		t.Fatalf("listing %d rows ran git %d times and worktree list %d times, want 0", rows, gitCalls.Load(), lister.calls.Load())
	}

	if _, err := f.delete(t, sessions[0].ID, false); err != nil {
		t.Fatalf("delete with remove_worktree: %v", err)
	}
	if statusCalls.Load() == 0 {
		t.Fatal("delete removed the worktree without a cleanliness check")
	}
}
