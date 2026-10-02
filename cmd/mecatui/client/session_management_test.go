package client

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type fakeSessionManagementClient struct {
	mecatlv1.HarnessServiceClient
	renameResp *mecatlv1.RenameSessionResponse
	renameErr  error
	deleteErr  error
	deleteResp *mecatlv1.DeleteSessionResponse
	renameReq  *mecatlv1.RenameSessionRequest
	deleteReq  *mecatlv1.DeleteSessionRequest
}

func (f *fakeSessionManagementClient) RenameSession(_ context.Context, in *mecatlv1.RenameSessionRequest, _ ...grpc.CallOption) (*mecatlv1.RenameSessionResponse, error) {
	f.renameReq = in
	return f.renameResp, f.renameErr
}

func (f *fakeSessionManagementClient) DeleteSession(_ context.Context, in *mecatlv1.DeleteSessionRequest, _ ...grpc.CallOption) (*mecatlv1.DeleteSessionResponse, error) {
	f.deleteReq = in
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	if f.deleteResp != nil {
		return f.deleteResp, nil
	}
	return &mecatlv1.DeleteSessionResponse{}, nil
}

func TestRenameSessionWrapperAndCmd(t *testing.T) {
	fake := &fakeSessionManagementClient{renameResp: &mecatlv1.RenameSessionResponse{Session: &mecatlv1.Session{TitleMetadata: &mecatlv1.SessionTitle{Title: "server title", Provenance: "operator", Revision: 7}}}}
	cl := newFakeClient(fake)
	msg := RenameSessionCmdWithToken(context.Background(), cl, "opaque\nID", "new title", 0)()
	got, ok := msg.(SessionRenamedMsg)
	if !ok {
		t.Fatalf("message = %T", msg)
	}
	if got.Err != nil || got.SessionID != "opaque\nID" || got.Title != "server title" || got.TitleProvenance != "operator" || got.TitleRevision != 7 {
		t.Fatalf("message = %+v", got)
	}
	if fake.renameReq.GetSessionId() != "opaque\nID" || fake.renameReq.GetTitle() != "new title" {
		t.Fatalf("request = %+v", fake.renameReq)
	}
}

func TestRenameSessionCmdPreservesError(t *testing.T) {
	fake := &fakeSessionManagementClient{renameErr: errors.New("ownership changed")}
	msg := RenameSessionCmdWithToken(context.Background(), newFakeClient(fake), "s1", "title", 0)().(SessionRenamedMsg)
	if msg.SessionID != "s1" || msg.Err == nil {
		t.Fatalf("message = %+v", msg)
	}
}

func TestDeleteSessionWrapperAndCmd(t *testing.T) {
	fake := &fakeSessionManagementClient{}
	cl := newFakeClient(fake)
	msg := DeleteSessionCmd(context.Background(), cl, "opaque-id")().(SessionDeletedMsg)
	if msg.Err != nil || msg.SessionID != "opaque-id" {
		t.Fatalf("message = %+v", msg)
	}
	if fake.deleteReq.GetSessionId() != "opaque-id" || fake.deleteReq.GetStopActive() || fake.deleteReq.GetRemoveWorktree() {
		t.Fatalf("request = %+v", fake.deleteReq)
	}

	fake.deleteErr = errors.New("active race")
	msg = DeleteSessionCmd(context.Background(), cl, "opaque-id")().(SessionDeletedMsg)
	if msg.Err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteSessionSendsOptionsAndReturnsWorktreeOutcome(t *testing.T) {
	fake := &fakeSessionManagementClient{deleteResp: &mecatlv1.DeleteSessionResponse{WorktreeRetainedReason: "dirty"}}
	cl := newFakeClient(fake)
	got, err := cl.DeleteSession(context.Background(), "s1", DeleteSessionOptions{StopActive: true, RemoveWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !fake.deleteReq.GetStopActive() || !fake.deleteReq.GetRemoveWorktree() || fake.deleteReq.GetSessionId() != "s1" {
		t.Fatalf("request = %+v", fake.deleteReq)
	}
	if got != (DeleteSessionResult{WorktreeRetainedReason: "dirty"}) {
		t.Fatalf("result = %+v", got)
	}

	fake.deleteResp = &mecatlv1.DeleteSessionResponse{WorktreeRemoved: true}
	got, err = cl.DeleteSession(context.Background(), "s1", DeleteSessionOptions{RemoveWorktree: true})
	if err != nil || got != (DeleteSessionResult{WorktreeRemoved: true}) {
		t.Fatalf("result = %+v, err = %v", got, err)
	}
	if fake.deleteReq.GetStopActive() {
		t.Fatalf("request = %+v", fake.deleteReq)
	}
}
