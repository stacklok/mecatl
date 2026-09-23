package client

import (
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/contracts/sessionaffinity"
)

type affinityCall struct {
	method string
	md     metadata.MD
}

type affinityRecordingConn struct {
	mu      sync.Mutex
	calls   []affinityCall
	streams []*affinityClientStream
}

func (c *affinityRecordingConn) record(ctx context.Context, method string) {
	md, _ := metadata.FromOutgoingContext(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, affinityCall{method: method, md: md.Copy()})
}

func (c *affinityRecordingConn) Invoke(ctx context.Context, method string, _, reply any, _ ...grpc.CallOption) error {
	c.record(ctx, method)
	if response, ok := reply.(*mecatlv1.CreateSessionResponse); ok {
		response.SessionId = "created"
	}
	if response, ok := reply.(*mecatlv1.GetCompatibilityInfoResponse); ok {
		response.ApiMajor = 1
		response.Capabilities = &mecatlv1.ServerCapabilities{SessionDebug: true}
	}
	return nil
}

func (c *affinityRecordingConn) NewStream(ctx context.Context, _ *grpc.StreamDesc, method string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	c.record(ctx, method)
	stream := &affinityClientStream{ctx: ctx}
	c.mu.Lock()
	c.streams = append(c.streams, stream)
	c.mu.Unlock()
	return stream, nil
}

type affinityClientStream struct {
	ctx  context.Context
	sent []any
}

func (*affinityClientStream) Header() (metadata.MD, error) { return nil, nil }
func (*affinityClientStream) Trailer() metadata.MD         { return nil }
func (*affinityClientStream) CloseSend() error             { return nil }
func (s *affinityClientStream) Context() context.Context   { return s.ctx }
func (s *affinityClientStream) SendMsg(m any) error {
	s.sent = append(s.sent, m)
	return nil
}
func (*affinityClientStream) RecvMsg(any) error { return io.EOF }

func TestMecatuiIllegalExternalSessionIDOmitAffinity(t *testing.T) {
	ctx := withSessionAffinity(context.Background(), "session-α")
	md, _ := metadata.FromOutgoingContext(ctx)
	if got := md.Get(sessionaffinity.HeaderName); len(got) != 0 {
		t.Fatalf("illegal external session affinity = %#v, want omitted", got)
	}
}

func TestSessionAffinityAndHandoff_Scenario4_MecatuiUnaryAndStreamPropagation(t *testing.T) {
	const sessionID = "session-%2Fexact"
	conn := &affinityRecordingConn{}
	client := &Client{svc: mecatlv1.NewHarnessServiceClient(conn)}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer existing",
		sessionaffinity.HeaderName, "stale-binding",
	))

	calls := []struct {
		name string
		call func() error
	}{
		{"debug", func() error {
			_, _, _, _, err := client.CreateDebugSession(ctx, sessionID, mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, ModelSelection{})
			return err
		}},
		{"fork", func() error { _, err := client.ForkSession(ctx, sessionID, "", ""); return err }},
		{"close", func() error { return client.CloseSession(ctx, sessionID) }},
		{"get", func() error { _, err := client.GetSession(ctx, sessionID); return err }},
		{"transcript", func() error { _, err := client.GetSessionTranscript(ctx, sessionID); return err }},
		{"mode", func() error { _, err := client.SetMode(ctx, sessionID, ModeDefaultString); return err }},
		{"rename", func() error { _, err := client.RenameSession(ctx, sessionID, "title"); return err }},
		{"delete", func() error { return client.DeleteSession(ctx, sessionID) }},
		{"compact", func() error { _, err := client.CompactSession(ctx, sessionID); return err }},
		{"reflect", func() error { _, err := client.ReflectSession(ctx, sessionID); return err }},
		{"commands", func() error { _, err := client.ListCommands(ctx, sessionID); return err }},
		{"worktrees", func() error { _, err := client.ListWorktrees(ctx, sessionID); return err }},
		{"event replay", func() error { _, err := client.StreamSessionEvents(ctx, sessionID); return err }},
		{"live events", func() error { _, err := client.StreamSessionLive(ctx, sessionID); return err }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err != nil {
				t.Fatalf("call failed: %v", err)
			}
		})
	}

	if len(conn.calls) != len(calls)+1 {
		t.Fatalf("recorded calls = %d, want %d", len(conn.calls), len(calls)+1)
	}
	for _, call := range conn.calls {
		if strings.HasSuffix(call.method, "/GetCompatibilityInfo") {
			if got := call.md.Get(sessionaffinity.HeaderName); len(got) != 0 {
				t.Errorf("GetCompatibilityInfo session metadata = %#v, want none", got)
			}
			continue
		}
		if got := call.md.Get(sessionaffinity.HeaderName); !reflect.DeepEqual(got, []string{sessionID}) {
			t.Errorf("%s session metadata = %#v, want exact %q", call.method, got, sessionID)
		}
		if got := call.md.Get("authorization"); !reflect.DeepEqual(got, []string{"Bearer existing"}) {
			t.Errorf("%s authorization metadata = %#v", call.method, got)
		}
	}
}

func TestADR_0294_MecatuiOpenConverseCompatibility(t *testing.T) {
	const sessionID = "session-bound"
	conn := &affinityRecordingConn{}
	client := &Client{svc: mecatlv1.NewHarnessServiceClient(conn)}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer existing"))

	legacy := client.OpenConverse
	legacyStream, err := legacy(ctx)
	if err != nil {
		t.Fatalf("OpenConverse: %v", err)
	}
	if err := legacyStream.SendPrompt(sessionID, "legacy", nil); err != nil {
		t.Fatalf("legacy SendPrompt: %v", err)
	}

	bound, err := client.OpenConverseForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("OpenConverseForSession: %v", err)
	}
	if _, err := client.OpenConverseForSession(ctx, "session-α"); err == nil || !strings.Contains(err.Error(), "session affinity") {
		t.Fatalf("illegal bound affinity error = %v, want useful session affinity error", err)
	}
	if got := len(conn.calls); got != 2 {
		t.Fatalf("illegal bound affinity opened a stream: calls = %d, want 2", got)
	}
	if err := bound.SendRetryStart(sessionID); err != nil {
		t.Fatalf("bound SendRetryStart: %v", err)
	}
	if err := bound.SendCancel(); err != nil {
		t.Fatalf("bound SendCancel: %v", err)
	}

	if got := conn.calls[0].md.Get(sessionaffinity.HeaderName); len(got) != 0 {
		t.Fatalf("legacy OpenConverse metadata = %#v, want absent", got)
	}
	if got := conn.calls[1].md.Get(sessionaffinity.HeaderName); !reflect.DeepEqual(got, []string{sessionID}) {
		t.Fatalf("bound OpenConverse metadata = %#v, want exact %q", got, sessionID)
	}
	if got := conn.calls[1].md.Get("authorization"); !reflect.DeepEqual(got, []string{"Bearer existing"}) {
		t.Fatalf("bound authorization metadata = %#v", got)
	}
	if got := len(conn.streams[1].sent); got != 2 {
		t.Fatalf("bound stream frames = %d, want retry plus control on the same bound stream", got)
	}
}
