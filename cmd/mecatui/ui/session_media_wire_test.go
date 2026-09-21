package ui

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type sessionMediaFixture struct {
	mecatlv1.UnimplementedHarnessServiceServer
	createdImage bool
	refetchImage bool
}

func (f *sessionMediaFixture) CreateSession(_ context.Context, _ *mecatlv1.CreateSessionRequest) (*mecatlv1.CreateSessionResponse, error) {
	return &mecatlv1.CreateSessionResponse{
		SessionId:           "sess-test-0001",
		SessionCapabilities: &mecatlv1.SessionCapabilities{Image: f.createdImage},
	}, nil
}

func (f *sessionMediaFixture) GetCompatibilityInfo(context.Context, *mecatlv1.GetCompatibilityInfoRequest) (*mecatlv1.GetCompatibilityInfoResponse, error) {
	return &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: 1, Capabilities: &mecatlv1.ServerCapabilities{Teams: true}}, nil
}

func (f *sessionMediaFixture) GetSession(_ context.Context, _ *mecatlv1.GetSessionRequest) (*mecatlv1.GetSessionResponse, error) {
	return &mecatlv1.GetSessionResponse{Session: &mecatlv1.Session{
		SessionId:           "sess-test-0001",
		SessionCapabilities: &mecatlv1.SessionCapabilities{Image: f.refetchImage},
	}}, nil
}

func TestSessionMediaWireRefreshControlsClipboardImages(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		selection                  client.ModelSelection
		createdImage, refetchImage bool
	}{
		{name: "default vision is revoked by text-only snapshot", createdImage: true, refetchImage: false},
		{name: "selected text-only is raised by vision snapshot", selection: client.ModelSelection{ProviderID: "gateway", ModelID: "text-only"}, createdImage: false, refetchImage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			grpcServer := grpc.NewServer()
			mecatlv1.RegisterHarnessServiceServer(grpcServer, &sessionMediaFixture{createdImage: tc.createdImage, refetchImage: tc.refetchImage})
			go func() { _ = grpcServer.Serve(listener) }()
			t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })

			cl, err := client.Dial(client.DialConfig{Server: listener.Addr().String()})
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			t.Cleanup(func() { _ = cl.Close() })
			id, createdCaps, _, err := cl.CreateSession(t.Context(), mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, tc.selection)
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if createdCaps.Image != tc.createdImage || !createdCaps.Teams {
				t.Fatalf("created capabilities = %+v", createdCaps)
			}

			cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
			m, _ := newClipboardModel(t, createdCaps, cb)
			msg, ok := client.RefreshResolvedModelCmd(t.Context(), cl, id)().(client.ResolvedModelMsg)
			if !ok || msg.Err != nil {
				t.Fatalf("GetSession message = %#v", msg)
			}
			m = applyAll(m, msg)
			if m.caps.Image != tc.refetchImage || !m.caps.Teams {
				t.Fatalf("refetched capabilities = %+v", m.caps)
			}
			m = pressCtrlV(t, m)
			if got := len(m.stagedMedia) > 0; got != tc.refetchImage {
				t.Fatalf("image staged = %t, want %t (caps %+v)", got, tc.refetchImage, m.caps)
			}
		})
	}
}

func TestResolvedModelMediaOverlay(t *testing.T) {
	prior := client.Capabilities{Teams: true, Memory: true}
	for _, tc := range []struct {
		name string
		msg  client.ResolvedModelMsg
		want client.Capabilities
	}{
		{
			name: "explicit text-only media preserves global capabilities",
			msg:  client.ResolvedModelMsg{SessionID: "session", Capabilities: client.Capabilities{SessionMediaPresent: true}},
			want: client.Capabilities{Teams: true, Memory: true, SessionMediaPresent: true},
		},
		{
			name: "image media overlay preserves global capabilities",
			msg:  client.ResolvedModelMsg{SessionID: "session", Capabilities: client.Capabilities{Image: true, SessionMediaPresent: true}},
			want: client.Capabilities{Teams: true, Memory: true, Image: true, SessionMediaPresent: true},
		},
		{
			name: "audio media overlay preserves global capabilities",
			msg:  client.ResolvedModelMsg{SessionID: "session", Capabilities: client.Capabilities{Audio: true, SessionMediaPresent: true}},
			want: client.Capabilities{Teams: true, Memory: true, Audio: true, SessionMediaPresent: true},
		},
		{
			name: "global snapshot replaces capabilities",
			msg:  client.ResolvedModelMsg{SessionID: "session", Capabilities: client.Capabilities{MCP: true}},
			want: client.Capabilities{MCP: true},
		},
		{
			name: "stale snapshot is ignored",
			msg:  client.ResolvedModelMsg{SessionID: "other", Capabilities: client.Capabilities{MCP: true}},
			want: prior,
		},
		{
			name: "failed snapshot is ignored",
			msg:  client.ResolvedModelMsg{SessionID: "session", Capabilities: client.Capabilities{MCP: true}, Err: context.Canceled},
			want: prior,
		},
		{
			name: "missing legacy capability fields leave current capabilities unchanged",
			msg:  client.ResolvedModelMsg{SessionID: "session"},
			want: prior,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{sessionID: "session", caps: prior}
			got, _, _ := m.onResolvedModelMsg(tc.msg)
			if got.caps != tc.want {
				t.Fatalf("capabilities = %+v, want %+v", got.caps, tc.want)
			}
		})
	}
}
