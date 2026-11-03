package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/app"
)

func workspaceCompositionStart(t *testing.T, m ui.Model, embedded bool) ui.Model {
	t.Helper()
	batch := m.Init()().(tea.BatchMsg)
	if embedded {
		batch = batch[0]().(tea.BatchMsg)
	}
	updated, create := m.Update(batch[1]()) // Catalog, then real session creation.
	if create == nil {
		t.Fatal("catalog did not continue to session creation")
	}
	ready, ok := create().(client.SessionReadyMsg)
	if !ok || ready.SessionID == "" {
		t.Fatalf("session creation = %#v", ready)
	}
	updated, _ = updated.Update(ready)
	return updated.(ui.Model)
}

func workspaceCompositionInspect(t *testing.T, m ui.Model, local bool, wants ...string) ui.Model {
	t.Helper()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 70})
	updated, load := updated.Update(tea.KeyPressMsg{Code: tea.KeyF10})
	if local {
		if load == nil {
			t.Fatal("workspace key did not schedule inspection")
		}
		result := load()
		batch, ok := result.(tea.BatchMsg)
		if !ok || len(batch) != 2 {
			t.Fatalf("workspace open did not schedule local inspection: %T", result)
		}
		updated, _ = updated.Update(batch[0]())
	}
	view := ansi.Strip(updated.View().Content)
	for _, want := range append([]string{"workspace snapshot"}, wants...) {
		if !strings.Contains(view, want) {
			t.Fatalf("workspace missing %q:\n%s", want, view)
		}
	}
	for _, forbidden := range []string{"client-cwd-decoy", "Git: client-cwd", "Inspecting local workspace…"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("workspace used client cwd or did not finish: %q\n%s", forbidden, view)
		}
	}
	if strings.Contains(view, "Local inspection unavailable") {
		for _, forbidden := range []string{"Git: composition-local", "local-only-file", "Directory:"} {
			if strings.Contains(view, forbidden) {
				t.Fatalf("unavailable inspection substituted local Git: %q\n%s", forbidden, view)
			}
		}
	}
	updated, _ = updated.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	return updated.(ui.Model)
}

func TestWorkspaceProductionComposition(t *testing.T) {
	for _, transport := range []string{"embedded", "connect"} {
		t.Run(transport, func(t *testing.T) {
			storage := isolatedEmbeddedTestStorage(t)
			shortEmbeddedRuntimeDir(t)
			decoy := workspaceTestRepo(t)
			workspaceTestGit(t, decoy, "branch", "-m", "client-cwd")
			workspaceTestWrite(t, decoy, "client-cwd-decoy", "never inspect this")
			t.Chdir(decoy)
			workspace := workspaceTestRepo(t)
			workspaceTestGit(t, workspace, "branch", "-m", "composition-local")
			workspaceTestWrite(t, workspace, "local-only-file", "untracked")
			argv := []string{"mecatui", "connect", "", "--anonymous", "--theme=aztec"}
			if transport == "embedded" {
				workspaceTestWrite(t, os.Getenv("XDG_CONFIG_HOME"), "mecatl/settings.yaml", "execution: {default_placement: "+app.PlacementHostLocal+"}\n")
				argv = []string{"mecatui", "--mock", "--quiet", "--theme=aztec", "--trust-project", "--toolhive-llm=false", "--product-metrics=false", "--no-store", "--no-memory", "--no-user-model", "--no-soul", "--no-skills", "--no-commands", "--user-model-dir=" + storage.userModelDir, "--workspace=" + workspace}
			} else {
				argv[2] = startStartupCompatibilityServer(t, &startupCompatibilityServer{major: 1})
			}
			ran := false
			err := runWithOptions(argv, runOptions{runProgram: func(ctx context.Context, m ui.Model) (tea.Model, error) {
				ran = true
				m = workspaceCompositionStart(t, m, transport == "embedded")
				if transport == "connect" {
					return workspaceCompositionInspect(t, m, false, "Connection: remote", "Local inspection unavailable"), nil
				}
				m = workspaceCompositionInspect(t, m, true, "Directory: ", filepath.Base(workspace)+`"`, "Git: composition-local", `?? "local-only-file"`)
				targetID := m.ActiveSessionID()
				sockets, err := filepath.Glob(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "mecatui-*", "mecated.sock"))
				if err != nil || len(sockets) != 1 {
					t.Fatalf("embedded socket: %v %v", sockets, err)
				}
				cl, err := client.Dial(client.DialConfig{Server: "unix://" + sockets[0]})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = cl.Close() }()
				// Keep the production collaborator, but resolve a missing session.
				updated, _ := m.Update(client.SessionReadyMsg{SessionID: "missing-session"})
				m = workspaceCompositionInspect(t, updated.(ui.Model), true, "Local inspection unavailable: local workspace directory unavailable")
				id, caps, resolved, err := cl.CreateSessionWithExecution(ctx, client.ModeFromString("default"), client.ModelSelection{}, client.ExecutionChoice{None: true})
				if err != nil {
					t.Fatal(err)
				}
				updated, _ = m.Update(client.SessionReadyMsg{SessionID: id, Capabilities: caps, ResolvedModel: resolved})
				m = workspaceCompositionInspect(t, updated.(ui.Model), true, "Local inspection unavailable: local workspace directory unavailable")
				id, _, caps, resolved, err = cl.CreateDebugSession(ctx, targetID, client.ModeFromString("default"), client.ModelSelection{})
				if err != nil {
					t.Fatal(err)
				}
				updated, _ = m.Update(client.SessionReadyMsg{SessionID: id, Capabilities: caps, ResolvedModel: resolved})
				return workspaceCompositionInspect(t, updated.(ui.Model), true, "Local inspection unavailable: local workspace directory unavailable"), nil
			}})
			if err != nil || !ran {
				t.Fatalf("production startup: %v; program ran=%v", err, ran)
			}
		})
	}
}
