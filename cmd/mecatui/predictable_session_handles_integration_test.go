package main

import (
	"context"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type renderedHeaderDebugServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
	ids []string

	mu     sync.Mutex
	target string
}

func (s *renderedHeaderDebugServer) ListSessions(context.Context, *mecatlv1.ListSessionsRequest) (*mecatlv1.ListSessionsResponse, error) {
	items := make([]*mecatlv1.SessionSummary, 0, len(s.ids))
	for _, id := range s.ids {
		items = append(items, &mecatlv1.SessionSummary{SessionId: id})
	}
	return &mecatlv1.ListSessionsResponse{Sessions: items}, nil
}

func (s *renderedHeaderDebugServer) CreateSession(_ context.Context, req *mecatlv1.CreateSessionRequest) (*mecatlv1.CreateSessionResponse, error) {
	s.mu.Lock()
	s.target = req.GetDebugTargetSessionId()
	s.mu.Unlock()
	return &mecatlv1.CreateSessionResponse{
		SessionId:    "debug-created",
		Capabilities: &mecatlv1.ServerCapabilities{SessionDebug: true},
	}, nil
}

func (s *renderedHeaderDebugServer) createdTarget() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func TestPredictableSessionHandles_Scenario2_RenderedHeaderCreatesBoundDebugger(t *testing.T) {
	ids := []string{
		"0123456789abcdef0123456789abcdef",
		"legacy-session-é",
	}
	service := &renderedHeaderDebugServer{ids: ids}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, service)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	cl, err := client.Dial(client.DialConfig{Server: listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	for _, fullID := range ids {
		t.Run(fullID, func(t *testing.T) {
			m := ui.New(ui.Deps{
				Theme:       theme.New("aztec", theme.AztecPalette()),
				Ctx:         t.Context(),
				NoAltScreen: true,
			})
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
			m = updated.(ui.Model)
			updated, _ = m.Update(client.SessionReadyMsg{SessionID: fullID})
			m = updated.(ui.Model)

			rendered := ansiEscape.ReplaceAllString(m.View().Content, "")
			_, sessionHeader, ok := strings.Cut(rendered, "session ")
			if !ok {
				t.Fatalf("public View has no normal session header: %q", rendered)
			}
			headerLiteral := strings.Fields(sessionHeader)[0]
			if headerLiteral == "" {
				t.Fatalf("public View has no session handle: %q", rendered)
			}

			_, resolved, _, _, err := cl.CreateDebugSession(context.Background(), headerLiteral, 0, client.ModelSelection{})
			if err != nil {
				t.Fatal(err)
			}
			if target := service.createdTarget(); resolved != fullID || target != fullID {
				t.Fatalf("rendered header literal %q resolved=%q request target=%q, want exact ID %q", headerLiteral, resolved, target, fullID)
			}

			// The final handoff prints fullID verbatim. Feeding that exact exit ID
			// through the same API path must preserve the server identity.
			_, resolved, _, _, err = cl.CreateDebugSession(context.Background(), fullID, 0, client.ModelSelection{})
			if err != nil {
				t.Fatal(err)
			}
			if target := service.createdTarget(); resolved != fullID || target != fullID {
				t.Fatalf("exact exit ID resolved=%q request target=%q, want %q", resolved, target, fullID)
			}
		})
	}
}
