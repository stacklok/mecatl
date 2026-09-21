package ui

import (
	"context"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	customization "github.com/stacklok/mecatl/cmd/mecatui/customization"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestADR_0296_StatusContextDiscardsStaleSessionResult(t *testing.T) {
	launch, first, second := t.TempDir(), t.TempDir(), t.TempDir()
	source := customization.NewCommandSource(customization.Command{
		Path: "/bin/sh", Args: []string{"-c", `read input; printf '<status><footer><text>'; pwd -P; printf '</text></footer></status>'`}, LaunchDir: launch,
	})
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	getter := statusContextGetter{roots: map[string]string{"first": first, "second": second}}
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: source, LocalSessionContext: getter})
	m.activePlacement = client.Placement{Kind: "local", Label: "active-workspace"}

	updated, firstCmd := m.Update(client.SessionReadyMsg{SessionID: "first"})
	m = updated.(Model)
	updated, secondCmd := m.Update(client.SessionReadyMsg{SessionID: "second"})
	m = updated.(Model)
	updated, _ = m.Update(statusContextMessage(t, secondCmd))
	m = updated.(Model)
	updated, _ = m.Update(statusContextMessage(t, firstCmd))
	m = updated.(Model)

	source.Submit(m.statusLineSnapshot())
	input := m.statusLineSnapshot()
	if got, want := input.Workspace, (customization.Workspace{Location: "local", Name: "active-workspace", Path: second}); got != want {
		t.Fatalf("status input = %#v, want only current local context path %#v", got, want)
	}
	waitStatusSourceChanged(t, source)
	want, err := filepath.EvalSymlinks(second)
	if err != nil {
		t.Fatalf("canonicalize current session root: %v", err)
	}
	if got := statusContextSurfaceText(source.Latest().Footer); got != want {
		t.Fatalf("status command CWD = %q, want current session root %q", got, want)
	}
}

func TestADR_0296_StatusTemplateReceivesEligibleLocalContext(t *testing.T) {
	root := t.TempDir()
	source := customization.NewTemplateSource(customization.TemplateSet{Footer: customization.SurfaceTemplates{
		Full: `<footer><text>{{.Workspace.Path}}</text></footer>`,
	}}, 0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: source, LocalSessionContext: statusContextGetter{roots: map[string]string{"local": root}}})
	m.activePlacement = client.Placement{Kind: "local", Label: "active-workspace"}
	m.width = 200

	updated, contextCmd := m.Update(client.SessionReadyMsg{SessionID: "local"})
	m = updated.(Model)
	waitStatusSourceChanged(t, source)
	updated, _ = m.Update(statusContextMessage(t, contextCmd))
	m = updated.(Model)

	input := m.statusLineSnapshot()
	if got, want := input.Workspace.Path, root; got != want {
		t.Fatalf("status input workspace path = %q, want active local root %q", got, want)
	}
	source.Submit(input)
	waitStatusContextSurfaceText(t, source, root)
}

func TestADR_0296_StartupResumeReceivesEligibleLocalContext(t *testing.T) {
	root := t.TempDir()
	source := customization.NewTemplateSource(customization.TemplateSet{Footer: customization.SurfaceTemplates{
		Full: `<footer><text>{{.Workspace.Path}}</text></footer>`,
	}}, 0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	m := newTestModelFromDeps(Deps{
		Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()),
		StatusSource: source, LocalSessionContext: statusContextGetter{roots: map[string]string{"existing": root}},
		Resume: startupSelection("existing", "completed"),
	})
	m.width = 200

	updated, contextCmd := m.Update(startupResumeReadyMsg{})
	m = updated.(Model)
	if contextCmd == nil {
		t.Fatal("startup resume did not resolve local status context")
	}
	updated, _ = m.Update(statusContextMessage(t, contextCmd))
	m = updated.(Model)

	if got, want := m.statusLineSnapshot().Workspace.Path, root; got != want {
		t.Fatalf("status input workspace path = %q, want resumed local root %q", got, want)
	}
}

func TestADR_0296_SessionsContinuationReceivesEligibleLocalContext(t *testing.T) {
	root := t.TempDir()
	source := customization.NewTemplateSource(customization.TemplateSet{Footer: customization.SurfaceTemplates{
		Full: `<footer><text>{{.Workspace.Path}}</text></footer>`,
	}}, 0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	m := New(Deps{
		Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()),
		StatusSource: source, LocalSessionContext: statusContextGetter{roots: map[string]string{"continued": root}},
	})
	m.width = 200

	updated, contextCmd, handled := m.adoptAuthoritativeTranscript(client.SessionListItem{
		ID: "continued", Title: "Stored chat", Placement: client.Placement{Kind: "local", Label: "active-workspace"},
	}, conversation{})
	if !handled || contextCmd == nil {
		t.Fatal("sessions continuation did not resolve local status context")
	}
	m = updated.(Model)
	updated, _ = m.Update(statusContextMessage(t, contextCmd))
	m = updated.(Model)

	if got, want := m.statusLineSnapshot().Workspace.Path, root; got != want {
		t.Fatalf("status input workspace path = %q, want continued local root %q", got, want)
	}
}

func waitStatusContextSurfaceText(t *testing.T, source customization.Source, want string) {
	t.Helper()
	for {
		waitStatusSourceChanged(t, source)
		if got := statusContextSurfaceText(source.Latest().Footer); got == want {
			return
		}
	}
}

func TestADR_0296_StatusContextUnavailableUsesHelperParentAndNoPath(t *testing.T) {
	launch := t.TempDir()
	source := customization.NewCommandSource(customization.Command{
		Path: "/bin/sh", Args: []string{"-c", `read input; printf '<status><footer><text>'; pwd -P; printf '</text></footer></status>'`}, LaunchDir: launch,
	})
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: source})
	updated, _ := m.Update(client.SessionReadyMsg{SessionID: "unavailable"})
	m = updated.(Model)
	updated, _ = m.Update(statusContextMsg{sessionID: "unavailable"})
	m = updated.(Model)

	input := m.statusLineSnapshot()
	source.Submit(input)
	waitStatusSourceChanged(t, source)
	want, err := filepath.EvalSymlinks(filepath.Dir("/bin/sh"))
	if err != nil {
		t.Fatalf("resolve helper parent: %v", err)
	}
	if got := statusContextSurfaceText(source.Latest().Footer); got != want {
		t.Fatalf("status command CWD = %q, want helper parent fallback %q", got, want)
	}
	if input.Workspace.Path != "" {
		t.Fatalf("status input disclosed context root: %#v", input.Workspace)
	}
}

type statusContextGetter struct{ roots map[string]string }

func (g statusContextGetter) GetLocalSessionContext(_ context.Context, id string) (string, error) {
	return g.roots[id], nil
}

func statusContextMessage(t *testing.T, cmd tea.Cmd) statusContextMsg {
	t.Helper()
	switch result := cmd().(type) {
	case statusContextMsg:
		return result
	case tea.BatchMsg:
		for _, next := range result {
			if msg, ok := next().(statusContextMsg); ok {
				return msg
			}
		}
	}
	t.Fatal("session-ready command did not resolve local status context")
	return statusContextMsg{}
}

func statusContextSurfaceText(surface customization.Surface) string {
	var text string
	for _, span := range surface.Spans {
		text += span.Text
	}
	return text
}

func waitStatusSourceChanged(t *testing.T, source customization.Source) {
	t.Helper()
	select {
	case <-source.Changed():
	case <-t.Context().Done():
		t.Fatal("status source did not publish")
	}
}
