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
	if got := m.prompt.Value(); got != "" {
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
	if !m.prompt.Focused() {
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
	picker := modelsState{filter: textinput.New(), deps: surfaceDeps{theme: th, keys: defaultKeys(), marks: defaultHelpKeys()}}
	got, _ := picker.Render(100, 30)

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

func TestModelsProviderStatusesUseErrorStyleAndSeparateModelRows(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	model := client.ModelInfo{ProviderID: "openai", ID: "gpt-5", DisplayName: "GPT-5"}
	status := client.ProviderStatus{ProviderID: "toolhive", State: "unreachable"}
	picker := modelsState{
		catalog:  modelCatalog{models: []client.ModelInfo{model}, statuses: []client.ProviderStatus{status}},
		filtered: []client.ModelInfo{model},
		filter:   textinput.New(),
	}
	picker.deps = surfaceDeps{theme: th, keys: defaultKeys(), marks: defaultHelpKeys()}
	statusLine := providerStatusLine(status)
	got, _ := picker.Render(100, 30)

	if !strings.Contains(got, th.Style("errorText").Render(statusLine)) {
		t.Fatalf("provider status must use the error style:\n%s", got)
	}
	if strings.Contains(got, th.Style("muted").Render(statusLine)) {
		t.Fatalf("provider status must not use the muted style:\n%s", got)
	}
	plain := stripANSIstr(got)
	if !strings.Contains(plain, "openai · GPT-5") || !strings.Contains(plain, statusLine) {
		t.Fatalf("provider status and model rows must both render through the real surface:\n%s", plain)
	}

	withoutStatus := picker
	withoutStatus.catalog.statuses = nil
	withPrefix, withSuffix := modelsFixedLines(picker, "")
	withoutPrefix, withoutSuffix := modelsFixedLines(withoutStatus, "")
	if got, want := len(withPrefix)+len(withSuffix), len(withoutPrefix)+len(withoutSuffix)+1; got != want {
		t.Fatalf("actual chrome rows with status = %d, want %d", got, want)
	}
	picker.deps = surfaceDeps{keys: defaultKeys(), theme: th}
	prefix, suffix := modelsFixedLines(picker, "")
	_, _ = picker.Render(100, len(prefix)+len(suffix)+4)
	if picker.rowBudget != 4 {
		t.Fatalf("page budget = %d, want 4 after status separation", picker.rowBudget)
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
	prefix, suffix := modelsFixedLines(*s, "")
	_, _ = s.Render(100, len(prefix)+len(suffix)+4)
	if s.rowBudget != 4 {
		t.Fatalf("page budget = %d, want Render-derived 4", s.rowBudget)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if s.cursor != 3 {
		t.Fatalf("cursor after pgdown = %d, want first item after the effective 3-row window", s.cursor)
	}
}
