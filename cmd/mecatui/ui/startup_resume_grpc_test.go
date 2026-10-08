package ui

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type resumeBufServer struct {
	mecatlv1.UnimplementedHarnessServiceServer

	mu        sync.Mutex
	order     []string
	prompts   []string
	failFirst bool
	calls     int
}

func (s *resumeBufServer) Converse(stream grpc.BidiStreamingServer[mecatlv1.ConverseRequest, mecatlv1.ConverseResponse]) error {
	s.record("recv-before-send")
	request, err := stream.Recv()
	if err != nil {
		return err
	}
	prompt := request.GetPrompt()
	s.mu.Lock()
	s.order = append(s.order, "prompt-received")
	s.prompts = append(s.prompts, prompt.GetText())
	s.calls++
	fail := s.failFirst && s.calls == 1
	s.mu.Unlock()
	if fail {
		return status.Error(codes.FailedPrecondition, "private lease owner and /private/path")
	}
	for _, response := range simpleRunScript("resume") {
		if err := stream.Send(response); err != nil {
			return err
		}
	}
	return nil
}

func (s *resumeBufServer) record(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, event)
}

func (s *resumeBufServer) snapshot() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...), append([]string(nil), s.prompts...)
}

type resumeGRPCConverser struct{ svc mecatlv1.HarnessServiceClient }

func (c resumeGRPCConverser) OpenConverse(ctx context.Context) (*client.Stream, error) {
	stream, err := c.svc.Converse(ctx)
	if err != nil {
		return nil, err
	}
	return client.NewStream(stream, stream), nil
}

func (c resumeGRPCConverser) OpenConverseForSession(ctx context.Context, _ string) (*client.Stream, error) {
	return c.OpenConverse(ctx)
}

func newResumeBufConverser(t *testing.T) (resumeGRPCConverser, *resumeBufServer) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	service := &resumeBufServer{}
	mecatlv1.RegisterHarnessServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	conn, err := grpc.NewClient("passthrough:///resume-bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatalf("new bufconn client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return resumeGRPCConverser{svc: mecatlv1.NewHarnessServiceClient(conn)}, service
}

func TestCompletedResumeFirstStatusFailureThenManualSubmissionWorks(t *testing.T) {
	const prompt = "continue after one-shot conflict"
	converser, service := newResumeBufConverser(t)
	service.failFirst = true
	progress := newProgress()
	model := newTestModelFromDeps(Deps{
		Conv: converser, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
		Workspace: "/workspace", Resume: startupSelection("completed-session", "completed"), InitialPrompt: prompt,
		NoAltScreen: true, emojiCapable: func() bool { return false }, kittyCapable: func() bool { return false },
		scrollKeysMarking: func() string { return "pgup/pgdn" }, onPhase: progress.record,
	})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 30))
	progress.wait(t, phaseReplay, 5*time.Second)

	// Back returns to the same adopted chat. Re-enter the prompt as an ordinary
	// operator submission; the second stream succeeds without any transport reorder.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEsc})
	tm.Send(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	tm.Type(prompt)
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	progress.waitRunComplete(t, 2, 5*time.Second)

	_, prompts := service.snapshot()
	if len(prompts) != 2 || prompts[0] != prompt || prompts[1] != prompt {
		t.Fatalf("automatic/manual prompts = %q, want two identical submissions", prompts)
	}

	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))
	final := tm.FinalModel(t).(Model)
	if final.phase != phaseIdle || final.sessionID != "completed-session" || final.startupRunEntryFailed {
		t.Fatalf("final retry state: phase=%v session=%q failed=%t", final.phase, final.sessionID, final.startupRunEntryFailed)
	}
}

func TestCompletedResumeAutomaticAndManualUseSameGRPCPath(t *testing.T) {
	const prompt = "continue the completed chat"
	for _, automatic := range []bool{true, false} {
		name := "manual"
		if automatic {
			name = "automatic"
		}
		t.Run(name, func(t *testing.T) {
			converser, service := newResumeBufConverser(t)
			progress := newProgress()
			initial := ""
			if automatic {
				initial = prompt
			}
			model := newTestModelFromDeps(Deps{
				Conv: converser, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
				Workspace: "/workspace", Resume: startupSelection("completed-session", "completed"), InitialPrompt: initial,
				NoAltScreen: true, emojiCapable: func() bool { return false }, kittyCapable: func() bool { return false },
				scrollKeysMarking: func() string { return "pgup/pgdn" }, onPhase: progress.record,
			})
			tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 30))
			if !automatic {
				progress.wait(t, phaseIdle, 3*time.Second)
				tm.Type(prompt)
				tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			progress.waitRunComplete(t, 1, 5*time.Second)

			order, prompts := service.snapshot()
			if len(prompts) != 1 || prompts[0] != prompt {
				t.Fatalf("server prompts = %q, want [%q]", prompts, prompt)
			}
			if len(order) < 2 || order[0] != "recv-before-send" || order[1] != "prompt-received" {
				t.Fatalf("first-frame order = %q, want Recv waiting before prompt Send", order)
			}

			tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))
			final := tm.FinalModel(t).(Model)
			if final.phase != phaseIdle || final.sessionID != "completed-session" || final.startupRunEntryFailed {
				t.Fatalf("final resume state: phase=%v session=%q failed=%t", final.phase, final.sessionID, final.startupRunEntryFailed)
			}
		})
	}
}
