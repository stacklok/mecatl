package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	statusline "github.com/stacklok/mecatl/cmd/mecatui/statusline"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestADR_0296_StatusContextDiscardsStaleSessionResult(t *testing.T) {
	launch, first, second := t.TempDir(), t.TempDir(), t.TempDir()
	source := statusline.NewCommandSource(statusline.Command{
		Path: "/bin/sh", Args: []string{"-c", `read input; printf '<status><footer><text>'; pwd -P; printf '</text></footer></status>'`}, LaunchDir: launch,
	})
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	getter := statusContextGetter{roots: map[string]string{"first": first, "second": second}}
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: source, LocalSessionContext: getter})

	updated, firstCmd := m.Update(client.SessionReadyMsg{SessionID: "first"})
	m = updated.(Model)
	updated, secondCmd := m.Update(client.SessionReadyMsg{SessionID: "second"})
	m = updated.(Model)
	updated, _ = m.Update(statusContextMessage(t, secondCmd))
	m = updated.(Model)
	updated, _ = m.Update(statusContextMessage(t, firstCmd))
	m = updated.(Model)

	source.Submit(m.statusLineSnapshot())
	waitStatusSourceChanged(t, source)
	if got := statusContextSurfaceText(source.Latest().Footer); got != second {
		t.Fatalf("status command CWD = %q, want current session root %q", got, second)
	}
}

func TestADR_0296_StatusContextUnavailableRetainsFallbackAndNoProjection(t *testing.T) {
	launch := t.TempDir()
	source := statusline.NewCommandSource(statusline.Command{
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
	if got := statusContextSurfaceText(source.Latest().Footer); got != launch {
		t.Fatalf("status command CWD = %q, want launch fallback %q", got, launch)
	}
	if input.Workspace.Basename != "" {
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

func statusContextSurfaceText(surface statusline.Surface) string {
	var text string
	for _, span := range surface.Spans {
		text += span.Text
	}
	return text
}

func waitStatusSourceChanged(t *testing.T, source statusline.Source) {
	t.Helper()
	select {
	case <-source.Changed():
	case <-t.Context().Done():
		t.Fatal("status source did not publish")
	}
}
