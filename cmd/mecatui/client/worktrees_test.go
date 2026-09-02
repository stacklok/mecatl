package client

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeWorktreesClient is a scripted HarnessServiceClient for the ListWorktrees
// wrapper tests (issue #102). Like fakeCommandsClient it embeds the interface
// and overrides only the one RPC under test, so the proto→plain mapping runs
// offline.
type fakeWorktreesClient struct {
	mecatlv1.HarnessServiceClient

	resp *mecatlv1.ListWorktreesResponse
	err  error

	lastReq *mecatlv1.ListWorktreesRequest
}

func (f *fakeWorktreesClient) ListWorktrees(_ context.Context, in *mecatlv1.ListWorktreesRequest, _ ...grpc.CallOption) (*mecatlv1.ListWorktreesResponse, error) {
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestListWorktreesMapping(t *testing.T) {
	fake := &fakeWorktreesClient{resp: &mecatlv1.ListWorktreesResponse{Worktrees: []*mecatlv1.Worktree{
		{Selector: "s1", Label: "repo", Branch: "refs/heads/main", Revision: "abcdef1"},
		{Selector: "s2", Label: "repo-wt", Branch: "refs/heads/feature", Revision: "1234567"},
	}}}
	cl := newFakeClient(fake)

	wts, err := cl.ListWorktrees(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if fake.lastReq.GetSessionId() != "/repo" {
		t.Fatalf("request session = %q, want /repo", fake.lastReq.GetSessionId())
	}
	if len(wts) != 2 {
		t.Fatalf("worktrees = %d, want 2", len(wts))
	}
	if wts[0].Selector != "s1" || wts[0].Label != "repo" || wts[0].Branch != "refs/heads/main" || wts[0].Revision != "abcdef1" {
		t.Fatalf("wts[0] = %+v", wts[0])
	}
	if wts[1].Selector != "s2" || wts[1].Label != "repo-wt" || wts[1].Revision != "1234567" {
		t.Fatalf("wts[1] = %+v", wts[1])
	}
}

func TestListWorktreesCmdSuccess(t *testing.T) {
	fake := &fakeWorktreesClient{resp: &mecatlv1.ListWorktreesResponse{Worktrees: []*mecatlv1.Worktree{
		{Selector: "s1", Label: "repo", Branch: "refs/heads/main"},
	}}}
	cl := newFakeClient(fake)

	msg := ListWorktreesCmd(context.Background(), cl, "/repo")()
	wm, ok := msg.(WorktreesMsg)
	if !ok {
		t.Fatalf("msg type = %T, want WorktreesMsg", msg)
	}
	if wm.Err != nil {
		t.Fatalf("unexpected err: %v", wm.Err)
	}
	if len(wm.Worktrees) != 1 || wm.Worktrees[0].Selector != "s1" || wm.Worktrees[0].Label != "repo" {
		t.Fatalf("worktrees = %+v", wm.Worktrees)
	}
}

func TestListWorktreesCmdError(t *testing.T) {
	fake := &fakeWorktreesClient{err: errors.New("boom")}
	cl := newFakeClient(fake)

	msg := ListWorktreesCmd(context.Background(), cl, "/repo")()
	wm, ok := msg.(WorktreesMsg)
	if !ok {
		t.Fatalf("msg type = %T, want WorktreesMsg", msg)
	}
	if wm.Err == nil {
		t.Fatal("expected err, got nil")
	}
	if wm.Worktrees != nil {
		t.Fatalf("expected nil worktrees on err, got %+v", wm.Worktrees)
	}
}
