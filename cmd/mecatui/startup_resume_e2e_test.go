package main

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/embed"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
)

func TestSessionContinuityUX_Scenario6_EmbeddedAndConnectE2E(t *testing.T) {
	starters := []struct {
		name  string
		start func(*testing.T, app.Config) (string, func())
	}{
		{name: "embedded", start: startStartupResumeEmbedded},
		{name: "connect", start: startStartupResumeConnect},
	}
	for _, tc := range starters {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			workspace, storeDir := t.TempDir(), t.TempDir()
			cfg := app.Config{Workspace: workspace, StoreDir: storeDir, Model: "mock-model", UseMock: true, Shell: "/bin/sh", Compaction: "heuristic", Tokenizer: "heuristic"}

			target, stop := tc.start(t, cfg)
			id := seedStartupResumeSession(ctx, t, target, workspace)
			stop()

			target, stop = tc.start(t, cfg)
			defer stop()
			cl, err := client.Dial(client.DialConfig{Server: target})
			if err != nil {
				t.Fatalf("dial restarted server: %v", err)
			}
			defer func() { _ = cl.Close() }()

			for _, selector := range []struct {
				name   string
				id     string
				latest bool
			}{{name: "exact", id: id}, {name: "latest", latest: true}} {
				t.Run(selector.name, func(t *testing.T) {
					selection, err := resolveStartupResume(ctx, cl, selector.id, selector.latest)
					if err != nil {
						t.Fatalf("resolve startup resume: %v", err)
					}
					if selection.Row.ID != id || selection.Transcript.SessionID != id || !selection.Transcript.Complete || len(selection.Transcript.Messages) == 0 {
						t.Fatalf("selection = %#v, want complete transcript for %q", selection, id)
					}
					rows, err := cl.ListSessions(ctx)
					if err != nil {
						t.Fatalf("list after adoption: %v", err)
					}
					if len(rows) != 1 || rows[0].ID != id {
						t.Fatalf("rows after adoption = %#v, want only %q", rows, id)
					}
				})
			}
		})
	}
}

func TestSessionsCommand_EmbeddedAndConnectE2E(t *testing.T) {
	starters := []struct {
		name  string
		start func(*testing.T, app.Config) (string, func())
	}{
		{name: "embedded", start: startStartupResumeEmbedded},
		{name: "connect", start: startStartupResumeConnect},
	}
	for _, tc := range starters {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			workspace, storeDir := t.TempDir(), t.TempDir()
			cfg := app.Config{Workspace: workspace, StoreDir: storeDir, Model: "mock-model", UseMock: true, Shell: "/bin/sh", Compaction: "heuristic", Tokenizer: "heuristic", SchedulerEnabled: true}
			target, stop := tc.start(t, cfg)
			defer stop()
			cl, err := client.Dial(client.DialConfig{Server: target})
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = cl.Close() }()

			chatID := seedStartupResumeSession(ctx, t, target, workspace)
			if _, err := cl.CreateSchedule(ctx, client.ScheduleSpec{
				Name: "sessions-command-inspect", Prompt: "scheduled inspection transcript",
				Trigger: client.ScheduleTrigger{OneShot: time.Now().Add(time.Hour)}, Workspace: workspace, Mode: "plan",
			}); err != nil {
				t.Fatalf("create schedule: %v", err)
			}
			_, scheduledID, err := cl.FireNow(ctx, "sessions-command-inspect")
			if err != nil {
				t.Fatalf("fire schedule: %v", err)
			}
			rows := waitForSessionsCommandRows(ctx, t, cl, chatID, scheduledID)
			baseline := len(rows)
			models, statuses, err := cl.ListModels(ctx)
			if err != nil {
				t.Fatalf("list models: %v", err)
			}

			newBrowser := func() ui.Model {
				return ui.New(ui.Deps{
					Session: &sessionAdapter{cl: cl, workspace: workspace, mode: "default"},
					Conv:    cl, Models: cl, Sessions: cl, Transcript: cl,
					BrowseSessions: true, Theme: theme.New("aztec", theme.AztecPalette()),
					Workspace: workspace, Mode: "default", Model: "mock-model", Ctx: ctx, NoAltScreen: true, NoBanner: true,
				})
			}
			prepare := func() ui.Model {
				m := newBrowser()
				m = updateSessionsCommandModel(t, m, tea.WindowSizeMsg{Width: 100, Height: 35}, nil)
				m = updateSessionsCommandModel(t, m, client.ModelsMsg{Models: models, Statuses: statuses}, nil)
				m = updateSessionsCommandModel(t, m, client.SessionsListedMsg{Sessions: rows}, nil)
				got, err := cl.ListSessions(ctx)
				if err != nil || len(got) != baseline {
					t.Fatalf("startup browser created a throwaway session: rows=%d baseline=%d err=%v", len(got), baseline, err)
				}
				return m
			}

			t.Run("continue", func(t *testing.T) {
				m := prepare()
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, func(msg tea.Msg) tea.Msg { return msg })
				if got := m.ActiveSessionID(); got != chatID {
					t.Fatalf("continued session = %q, want %q", got, chatID)
				}
				gotRows, _ := cl.ListSessions(ctx)
				if len(gotRows) != baseline {
					t.Fatalf("continue created a session: rows=%d baseline=%d", len(gotRows), baseline)
				}
			})

			t.Run("inspect-back", func(t *testing.T) {
				m := prepare()
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: tea.KeyTab}, nil)
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, func(msg tea.Msg) tea.Msg { return msg })
				if m.ActiveSessionID() != "" || !strings.Contains(m.View().Content, "Inspecting") {
					t.Fatalf("scheduled inspection did not stay read-only:\n%s", m.View().Content)
				}
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, nil)
				if m.ActiveSessionID() != "" || !strings.Contains(m.View().Content, "Scheduled runs") {
					t.Fatalf("Back did not return to startup inventory:\n%s", m.View().Content)
				}
			})

			t.Run("cancel", func(t *testing.T) {
				m := prepare()
				_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				if cmd == nil {
					t.Fatal("esc did not return a quit command")
				}
				if _, ok := cmd().(tea.QuitMsg); !ok {
					t.Fatalf("esc command = %T, want tea.QuitMsg", cmd())
				}
				gotRows, _ := cl.ListSessions(ctx)
				if m.ActiveSessionID() != "" || len(gotRows) != baseline {
					t.Fatalf("cancel established a session: id=%q rows=%d baseline=%d", m.ActiveSessionID(), len(gotRows), baseline)
				}
			})

			t.Run("new-chat", func(t *testing.T) {
				m := prepare()
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"}, func(msg tea.Msg) tea.Msg { return msg })
				if m.ActiveSessionID() == "" || m.ActiveSessionID() == chatID || m.ActiveSessionID() == scheduledID {
					t.Fatalf("new chat id = %q", m.ActiveSessionID())
				}
				gotRows, err := cl.ListSessions(ctx)
				if err != nil || len(gotRows) != baseline+1 {
					t.Fatalf("new chat rows=%d, want %d: %v", len(gotRows), baseline+1, err)
				}
			})
		})
	}
}

// updateSessionsCommandModel drives one reducer input and, when runCmd is non-nil,
// executes the returned transport command and feeds its result back through the
// same reducer. The server and client are real; only Bubble Tea's scheduler is
// collapsed synchronously for deterministic assertions.
func updateSessionsCommandModel(t *testing.T, m ui.Model, msg tea.Msg, runCmd func(tea.Msg) tea.Msg) ui.Model {
	t.Helper()
	next, cmd := m.Update(msg)
	var ok bool
	m, ok = next.(ui.Model)
	if !ok {
		t.Fatalf("model = %T, want ui.Model", next)
	}
	if runCmd == nil {
		return m
	}
	if cmd == nil {
		t.Fatalf("message %T returned no command", msg)
	}
	next, _ = m.Update(runCmd(cmd()))
	m, ok = next.(ui.Model)
	if !ok {
		t.Fatalf("command result model = %T, want ui.Model", next)
	}
	return m
}

func waitForSessionsCommandRows(ctx context.Context, t *testing.T, cl *client.Client, chatID, scheduledID string) []client.SessionListItem {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := cl.ListSessions(ctx)
		if err == nil {
			haveChat, haveScheduled := false, false
			for _, row := range rows {
				haveChat = haveChat || row.ID == chatID
				haveScheduled = haveScheduled || row.ID == scheduledID
			}
			if haveChat && haveScheduled {
				transcript, transcriptErr := cl.GetSessionTranscript(ctx, scheduledID)
				if transcriptErr == nil && transcript.Complete {
					return rows
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("sessions inventory never exposed chat %q and scheduled run %q", chatID, scheduledID)
	return nil
}

func startStartupResumeEmbedded(t *testing.T, cfg app.Config) (string, func()) {
	t.Helper()
	srv, err := embed.Start(t.Context(), cfg, embed.PerfConfig{})
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	return srv.Target(), func() { _ = srv.Close() }
}

func startStartupResumeConnect(t *testing.T, cfg app.Config) (string, func()) {
	t.Helper()
	built, err := app.Build(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build connect server: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		built.Close()
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(built.Service))
	mecatlv1.RegisterScheduleServiceServer(grpcServer, server.NewScheduleServer(built.Service))
	go func() { _ = grpcServer.Serve(lis) }()
	return lis.Addr().String(), func() {
		grpcServer.GracefulStop()
		_ = lis.Close()
		built.Close()
	}
}

func seedStartupResumeSession(ctx context.Context, t *testing.T, target, workspace string) string {
	t.Helper()
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial seed server: %v", err)
	}
	defer func() { _ = conn.Close() }()
	svc := mecatlv1.NewHarnessServiceClient(conn)
	created, err := svc.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: workspace})
	if err != nil {
		t.Fatalf("create seed session: %v", err)
	}
	stream, err := svc.Converse(ctx)
	if err != nil {
		t.Fatalf("open seed stream: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: created.GetSessionId(), Text: "resume this transcript"}}}); err != nil {
		t.Fatalf("send seed prompt: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close seed stream: %v", err)
	}
	for {
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			return created.GetSessionId()
		}
		if err != nil {
			t.Fatalf("receive seed turn: %v", err)
		}
	}
}
