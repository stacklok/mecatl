package client

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeCommandsClient is a scripted HarnessServiceClient for the ListCommands
// wrapper tests. Like fakeHarnessClient it embeds the interface and overrides
// only the one RPC under test, so the proto→plain mapping runs offline.
type fakeCommandsClient struct {
	mecatlv1.HarnessServiceClient

	resp *mecatlv1.ListCommandsResponse
	err  error

	lastReq *mecatlv1.ListCommandsRequest
}

func (f *fakeCommandsClient) ListCommands(_ context.Context, in *mecatlv1.ListCommandsRequest, _ ...grpc.CallOption) (*mecatlv1.ListCommandsResponse, error) {
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestListCommandsMapping(t *testing.T) {
	fake := &fakeCommandsClient{resp: &mecatlv1.ListCommandsResponse{Commands: []*mecatlv1.Command{
		{Name: "fix", Description: "fix a failing test"},
		{Name: "review", Description: "review a PR"},
	}}}
	cl := newFakeClient(fake)

	cmds, err := cl.ListCommands(context.Background(), "/proj")
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	if fake.lastReq.GetSessionId() != "/proj" {
		t.Fatalf("request session = %q, want /proj", fake.lastReq.GetSessionId())
	}
	if len(cmds) != 2 {
		t.Fatalf("commands = %d, want 2", len(cmds))
	}
	if cmds[0].Name != "fix" || cmds[0].Description != "fix a failing test" {
		t.Fatalf("cmds[0] = %+v", cmds[0])
	}
	if cmds[1].Name != "review" {
		t.Fatalf("cmds[1] = %+v", cmds[1])
	}
}

func TestListCommandsCmdSuccess(t *testing.T) {
	fake := &fakeCommandsClient{resp: &mecatlv1.ListCommandsResponse{Commands: []*mecatlv1.Command{
		{Name: "fix", Description: "fix a failing test"},
	}}}
	cl := newFakeClient(fake)

	msg := ListCommandsCmd(context.Background(), cl, "/proj")()
	cm, ok := msg.(CommandsMsg)
	if !ok {
		t.Fatalf("msg type = %T, want CommandsMsg", msg)
	}
	if cm.Err != nil {
		t.Fatalf("unexpected err: %v", cm.Err)
	}
	if len(cm.Commands) != 1 || cm.Commands[0].Name != "fix" {
		t.Fatalf("commands = %+v", cm.Commands)
	}
}

func TestListCommandsCmdError(t *testing.T) {
	fake := &fakeCommandsClient{err: errors.New("boom")}
	cl := newFakeClient(fake)

	msg := ListCommandsCmd(context.Background(), cl, "/proj")()
	cm, ok := msg.(CommandsMsg)
	if !ok {
		t.Fatalf("msg type = %T, want CommandsMsg", msg)
	}
	if cm.Err == nil {
		t.Fatalf("expected an error in CommandsMsg")
	}
}
