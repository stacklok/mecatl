package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestModelsSurfaceCapturesInputAndLateCatalogDoesNotReopen(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)

	m = pressModelsKey(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	if got := modelsSurface(t, m).filter.Value(); got != "c" {
		t.Fatalf("surface filter = %q, want input captured by the modal", got)
	}
	if got := m.ta.Value(); got != "" {
		t.Fatalf("textarea = %q, want modal input capture", got)
	}

	mm, closeCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	// Filter clearing is synchronous; no command is relevant to this assertion.
	_ = closeCmd
	if m.modal == nil { // first esc clears the non-empty filter
		t.Fatal("first esc should clear the surface filter, not close")
	}
	mm, closeCmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	// Closing synchronously restores focus; closeCmd is only the widget blink.
	_ = closeCmd
	if m.modal != nil {
		t.Fatal("second esc should close the models surface")
	}
	if !m.ta.Focused() {
		t.Fatal("closing the models surface should refocus the textarea")
	}

	late := client.ModelsMsg{
		RequestToken: m.modelCatalogRequestToken,
		Models:       []client.ModelInfo{{ProviderID: "late", ID: "model"}},
	}
	mm, _ = m.Update(late)
	m = mm.(Model)
	if m.modal != nil {
		t.Fatal("late ModelsMsg reopened the closed surface")
	}
	if len(m.modelCatalog.models) != 1 || m.modelCatalog.models[0].ProviderID != "late" {
		t.Fatalf("root catalog = %+v, want late result", m.modelCatalog.models)
	}
}

func TestModelsSurfaceConsumesWheelBeforeViewport(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m.vp.SetContent(strings.Repeat("line\n", 40))
	m.vp.SetHeight(3)
	m.vp.SetYOffset(5)
	if got := m.vp.YOffset(); got != 5 {
		t.Fatalf("viewport setup offset = %d, want 5", got)
	}

	mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if got := m.vp.YOffset(); got != 5 {
		t.Fatalf("viewport offset = %d, want 5; open models surface must consume the wheel", got)
	}
}

func TestModelsSwitchDisclosureIsOneWarningLine(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	picker := modelsState{filter: textinput.New()}
	got := renderModelsPanel(th, modelCatalog{}, picker, client.Capabilities{}, "", defaultHelpKeys(), modelsMinRows)

	if strings.Count(modelSwitchDisclosure, "\n") != 0 {
		t.Fatalf("disclosure must be one line, got %q", modelSwitchDisclosure)
	}
	if strings.Count(stripANSIstr(got), modelSwitchDisclosure) != 1 {
		t.Fatalf("rendered disclosure count = %d, want 1:\n%s", strings.Count(stripANSIstr(got), modelSwitchDisclosure), got)
	}
	if !strings.Contains(got, th.Style("warning").Render(modelSwitchDisclosure)) {
		t.Fatalf("disclosure must use warning style:\n%s", got)
	}
}

func TestModelsSurfaceRenderOwnsCurrentPageBudget(t *testing.T) {
	models := make([]client.ModelInfo, 10)
	for i := range models {
		models[i] = client.ModelInfo{ProviderID: "p", ID: itoa3(i)}
	}
	s := &modelsState{
		view:     modelsPanel,
		catalog:  modelCatalog{models: models},
		filtered: models,
		deps:     surfaceDeps{keys: defaultKeys(), theme: theme.New("aztec", theme.AztecPalette())},
	}
	_, _ = s.Render(100, modelsPanelFixedRows(*s, "", defaultHelpKeys())+4)
	if s.rowBudget != 4 {
		t.Fatalf("page budget = %d, want Render-derived 4", s.rowBudget)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if s.cursor != 4 {
		t.Fatalf("cursor after pgdown = %d, want current Render page budget 4", s.cursor)
	}
}
