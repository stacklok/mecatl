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
	if fake.deleteReq.GetSessionId() != "opaque-id" {
		t.Fatalf("request = %+v", fake.deleteReq)
	}

	fake.deleteErr = errors.New("active race")
	msg = DeleteSessionCmd(context.Background(), cl, "opaque-id")().(SessionDeletedMsg)
	if msg.Err == nil {
		t.Fatal("expected error")
	}
}
