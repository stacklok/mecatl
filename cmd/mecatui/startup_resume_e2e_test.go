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

type e2eClipboard struct{ wrote string }

func (*e2eClipboard) Read(context.Context) (string, []byte, error) {
	return "", nil, client.ErrEmptyClipboard
}
func (*e2eClipboard) ReadPrimary(context.Context) (string, error) {
	return "", client.ErrEmptyClipboard
}
func (c *e2eClipboard) Write(_ context.Context, _ string, data []byte) error {
	c.wrote = string(data)
	return nil
}

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

			var lastClipboard *e2eClipboard
			newBrowser := func() ui.Model {
				lastClipboard = &e2eClipboard{}
				return ui.New(ui.Deps{
					Session: &sessionAdapter{cl: cl, workspace: workspace, mode: "default"},
					Conv:    cl, Models: cl, Sessions: cl, SessionManagement: cl, Transcript: cl, Clipboard: lastClipboard,
					BrowseSessions: true, Theme: theme.New("aztec", theme.AztecPalette()),
					Workspace: workspace, Mode: "default", Model: "mock-model", Ctx: ctx, NoAltScreen: true, NoBanner: true,
				})
			}
			prepare := func() ui.Model {
				m := newBrowser()
				m = updateSessionsCommandModel(t, m, tea.WindowSizeMsg{Width: 100, Height: 35}, nil)
				m = updateSessionsCommandModel(t, m, client.ModelsMsg{Models: models, Statuses: statuses, RequestToken: 1}, nil)
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

			t.Run("management-actions", func(t *testing.T) {
				row := requireSessionsCommandRow(ctx, t, cl, chatID)
				if !row.Capabilities.CopyID || !row.Capabilities.ViewTranscript || !row.Capabilities.Fork || !row.Capabilities.Rename || !row.Capabilities.Delete {
					t.Fatalf("server main-chat capabilities = %+v", row.Capabilities)
				}
				scheduled := requireSessionsCommandRow(ctx, t, cl, scheduledID)
				if !scheduled.Capabilities.CopyID || !scheduled.Capabilities.ViewTranscript || scheduled.Capabilities.Fork || scheduled.Capabilities.Rename || scheduled.Capabilities.Delete {
					t.Fatalf("server scheduled-run capabilities = %+v", scheduled.Capabilities)
				}

				prepareRow := func(row client.SessionListItem) ui.Model {
					m := newBrowser()
					m = updateSessionsCommandModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 35}, nil)
					m = updateSessionsCommandModel(t, m, client.ModelsMsg{Models: models, Statuses: statuses, RequestToken: 1}, nil)
					return updateSessionsCommandModel(t, m, client.SessionsListedMsg{Sessions: []client.SessionListItem{row}}, nil)
				}

				m := prepareRow(row)
				content := m.View().Content
				for _, hint := range []string{"y: copy ID", "v: view", "f: fork", "r: rename", "d: delete"} {
					if !strings.Contains(content, hint) {
						t.Fatalf("server-enabled hint %q missing:\n%s", hint, content)
					}
				}
				updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"}, func(msg tea.Msg) tea.Msg { return msg })
				if lastClipboard.wrote != chatID {
					t.Fatalf("copied ID = %q, want exact %q", lastClipboard.wrote, chatID)
				}

				m = prepareRow(row)
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"}, func(msg tea.Msg) tea.Msg { return msg })
				if m.ActiveSessionID() != "" || !strings.Contains(m.View().Content, "Inspecting") {
					t.Fatalf("view rebound the startup browser: id=%q\n%s", m.ActiveSessionID(), m.View().Content)
				}

				m = prepareRow(row)
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"}, nil)
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, nil)
				before, err := cl.GetSession(ctx, chatID)
				if err != nil {
					t.Fatalf("get title after rename cancel: %v", err)
				}
				if before.Title != row.Title {
					t.Fatalf("rename cancel changed title to %q, want %q", before.Title, row.Title)
				}
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"}, nil)
				m = typeSessionsCommandText(t, m, " managed")
				updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, func(msg tea.Msg) tea.Msg { return msg })
				renamed, err := cl.GetSession(ctx, chatID)
				if err != nil || renamed.Title != row.Title+" managed" || renamed.TitleProvenance != "operator" {
					t.Fatalf("rename result = %+v, %v", renamed, err)
				}

				deleteID, _, _, err := cl.CreateSession(ctx, workspace, mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, client.ModelSelection{})
				if err != nil {
					t.Fatalf("create delete target: %v", err)
				}
				deleteRow := requireSessionsCommandRow(ctx, t, cl, deleteID)
				m = prepareRow(deleteRow)
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"}, nil)
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"}, nil)
				if _, err := cl.GetSession(ctx, deleteID); err != nil {
					t.Fatalf("delete cancel removed session: %v", err)
				}
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"}, nil)
				updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"}, func(msg tea.Msg) tea.Msg { return msg })
				if _, err := cl.GetSession(ctx, deleteID); err == nil {
					t.Fatal("confirmed delete left session readable")
				}

				row = requireSessionsCommandRow(ctx, t, cl, chatID)
				m = prepareRow(row)
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"}, func(msg tea.Msg) tea.Msg { return msg })
				forkID := m.ActiveSessionID()
				if forkID == "" || forkID == chatID {
					t.Fatalf("fork did not adopt new session: %q", forkID)
				}

				m = openSessionsCommandInventory(t, m, []client.SessionListItem{requireSessionsCommandRow(ctx, t, cl, forkID)})
				beforeView := m.View().Content
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"}, nil)
				if m.ActiveSessionID() != forkID || !strings.Contains(m.View().Content, "cannot delete the current chat") || strings.Contains(beforeView, "cannot delete the current chat") {
					t.Fatalf("current delete refusal missing or rebound: id=%q\n%s", m.ActiveSessionID(), m.View().Content)
				}
				if _, err := cl.GetSession(ctx, forkID); err != nil {
					t.Fatalf("current delete refusal removed session: %v", err)
				}

				failureID, _, _, err := cl.CreateSession(ctx, workspace, mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, client.ModelSelection{})
				if err != nil {
					t.Fatalf("create stale fork target: %v", err)
				}
				failureRow := requireSessionsCommandRow(ctx, t, cl, failureID)
				m = updateSessionsCommandModel(t, m, client.SessionsListedMsg{Sessions: []client.SessionListItem{failureRow}}, nil)
				if err := cl.DeleteSession(ctx, failureID); err != nil {
					t.Fatalf("delete stale fork target: %v", err)
				}
				m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"}, func(msg tea.Msg) tea.Msg { return msg })
				if m.ActiveSessionID() != forkID || !strings.Contains(m.View().Content, "could not fork session") {
					t.Fatalf("failed fork rebound active session: id=%q\n%s", m.ActiveSessionID(), m.View().Content)
				}
			})
		})
	}
}

func requireSessionsCommandRow(ctx context.Context, t *testing.T, cl *client.Client, id string) client.SessionListItem {
	t.Helper()
	rows, err := cl.ListSessions(ctx)
	if err != nil {
		t.Fatalf("list sessions for %q: %v", id, err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("session %q absent from inventory", id)
	return client.SessionListItem{}
}

func typeSessionsCommandText(t *testing.T, m ui.Model, text string) ui.Model {
	t.Helper()
	for _, r := range text {
		m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: r, Text: string(r)}, nil)
	}
	return m
}

func openSessionsCommandInventory(t *testing.T, m ui.Model, rows []client.SessionListItem) ui.Model {
	t.Helper()
	m = typeSessionsCommandText(t, m, "/sessions")
	m = updateSessionsCommandModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, func(msg tea.Msg) tea.Msg { return msg })
	return updateSessionsCommandModel(t, m, client.SessionsListedMsg{Sessions: rows}, nil)
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

func seedStartupResumeSession(ctx context.Context, t *testing.T, target, _ string) string {
	t.Helper()
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial seed server: %v", err)
	}
	defer func() { _ = conn.Close() }()
	svc := mecatlv1.NewHarnessServiceClient(conn)
	created, err := svc.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
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
