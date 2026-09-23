package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
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

func TestModelCapSegmentsDescribeImageInput(t *testing.T) {
	got := modelCapSegments(client.ModelInfo{Image: true})
	if len(got) != 1 || got[0] != "image input" {
		t.Fatalf("image capability segment = %v, want [image input]", got)
	}
}

func TestModelsBoundedItemsExposeCurrentAndDefaultStatusCells(t *testing.T) {
	models := []client.ModelInfo{
		{ProviderID: "p", ID: "neither"},
		{ProviderID: "p", ID: "current"},
		{ProviderID: "p", ID: "default"},
		{ProviderID: "p", ID: "both"},
	}
	catalog := modelCatalog{
		active:        client.ModelSelection{ProviderID: "p", ModelID: "current"},
		globalDefault: client.ModelSelection{ProviderID: "p", ModelID: "default"},
	}
	items := modelsBoundedItems(catalog, models)
	catalog.active, catalog.globalDefault = client.ModelSelection{ProviderID: "p", ModelID: "both"}, client.ModelSelection{ProviderID: "p", ModelID: "both"}
	items = append(items, modelsBoundedItems(catalog, models[3:])...)
	want := [][2]string{{}, {"●", ""}, {"", "★"}, {}, {"●", "★"}}
	for i := range items {
		if items[i].StatusCells != want[i] {
			t.Errorf("item %d status cells = %q, want %q", i, items[i].StatusCells, want[i])
		}
		if strings.HasPrefix(items[i].Text, "●") || strings.HasPrefix(items[i].Text, "★") {
			t.Errorf("item %d embeds status marker in text: %q", i, items[i].Text)
		}
	}
}

func TestModelsSurfaceRendersCurrentAndDefaultStatusCellsWithSelection(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, tc := range []struct {
		name             string
		active, default_ client.ModelSelection
		want             string
	}{
		{name: "current", active: client.ModelSelection{ProviderID: "p", ModelID: "model"}, want: "▶●  p · model"},
		{name: "default", default_: client.ModelSelection{ProviderID: "p", ModelID: "model"}, want: "▶ ★ p · model"},
		{name: "both", active: client.ModelSelection{ProviderID: "p", ModelID: "model"}, default_: client.ModelSelection{ProviderID: "p", ModelID: "model"}, want: "▶●★ p · model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := client.ModelInfo{ProviderID: "p", ID: "model"}
			picker := modelsState{
				catalog:  modelCatalog{models: []client.ModelInfo{model}, active: tc.active, globalDefault: tc.default_},
				filtered: []client.ModelInfo{model}, filter: textinput.New(), list: new(bounded.List),
				deps: surfaceDeps{theme: th, keys: defaultKeys(), marks: defaultHelpKeys()},
			}
			out, _ := picker.Render(80, 20)
			if plain := stripANSIstr(out); !strings.Contains(plain, tc.want) {
				t.Fatalf("status cells =\n%s\nwant %q", plain, tc.want)
			}
		})
	}
}

func TestModelsSurfaceRefreshPreservesStableCursorAndTopAnchor(t *testing.T) {
	models := make([]client.ModelInfo, 40)
	for i := range models {
		models[i] = client.ModelInfo{ProviderID: "provider", ID: fmt.Sprintf("model-%d", i), DisplayName: fmt.Sprintf("Model %d", i)}
	}
	fm := &fakeModels{models: models}
	m := newModelsModel(t, fm, &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m = resize(m, 40, 15)
	s := modelsSurface(t, m)
	_, _ = s.Render(40, 15)

	s.list.SetCursor(5)
	s.list.Scroll(bounded.LineDown)
	beforeTop := s.list.View().Rows[0]
	if s.list.CursorID() != aggregateScopedID("provider", "model-5") || beforeTop.ID == "" {
		t.Fatalf("refresh setup cursor/top = %q/%q", s.list.CursorID(), beforeTop.ID)
	}

	reordered := append([]client.ModelInfo{models[5], models[0], models[1]}, models[2:5]...)
	reordered = append(reordered, models[6:]...)
	// The picker receives the newly reconciled catalog before its next frame. Render
	// owns the geometry/list refresh that must retain the stable selection.
	s.catalog.models, s.filtered = reordered, reordered
	_, _ = s.Render(40, 15)
	s = modelsSurface(t, m)
	if got := s.list.CursorID(); got != aggregateScopedID("provider", "model-5") {
		t.Fatalf("reordered refresh selected %q, want provider/model-5", got)
	}
	if afterTop := s.list.View().Rows[0]; afterTop.ID != beforeTop.ID || afterTop.ItemLine != beforeTop.ItemLine {
		t.Fatalf("reordered refresh top anchor = {%q,%d}, want {%q,%d}", afterTop.ID, afterTop.ItemLine, beforeTop.ID, beforeTop.ItemLine)
	}
}

func TestModelsSurfaceDelimiterIDsSurviveRefreshReorder(t *testing.T) {
	first := client.ModelInfo{ProviderID: "a:b", ID: "c", DisplayName: "first"}
	selected := client.ModelInfo{ProviderID: "a", ID: "b:c", DisplayName: "selected"}
	s := &modelsState{
		catalog:  modelCatalog{models: []client.ModelInfo{first, selected}},
		filtered: []client.ModelInfo{first, selected},
		filter:   textinput.New(),
		deps:     surfaceDeps{keys: defaultKeys(), theme: aztec()},
	}
	_, _ = s.Render(40, 15)
	s.list.SetCursor(1)
	want := aggregateScopedID("a", "b:c")
	if got, other := s.list.CursorID(), aggregateScopedID("a:b", "c"); got != want || got == other {
		t.Fatalf("delimiter-bearing setup cursor=%q other=%q want=%q", got, other, want)
	}

	s.catalog.models, s.filtered = []client.ModelInfo{selected, first}, []client.ModelInfo{selected, first}
	_, _ = s.Render(40, 15)
	if got := s.list.CursorID(); got != want || s.list.Cursor() != 0 {
		t.Fatalf("delimiter-bearing refresh cursor=(%d,%q), want (0,%q)", s.list.Cursor(), got, want)
	}
	chosen, ok := s.chosen()
	if !ok || chosen.ProviderID != selected.ProviderID || chosen.ID != selected.ID {
		t.Fatalf("delimiter-bearing refresh chose %+v, want %+v", chosen, selected)
	}
}

func TestModelsStaleHitResolvesIdentityAfterReorderAndFilterRefresh(t *testing.T) {
	first := client.ModelInfo{ProviderID: "provider", ID: "first", DisplayName: "First"}
	second := client.ModelInfo{ProviderID: "provider", ID: "second", DisplayName: "Second"}
	third := client.ModelInfo{ProviderID: "provider", ID: "third", DisplayName: "Third"}
	hits := &hitRegions{}
	s := &modelsState{
		view: modelsPanel, catalog: modelCatalog{models: []client.ModelInfo{first, second, third}},
		filtered: []client.ModelInfo{first, second, third}, filter: textinput.New(),
		deps: surfaceDeps{keys: defaultKeys(), marks: defaultHelpKeys(), theme: aztec(), hits: hits},
	}
	_, _ = s.Render(80, 20)
	var stale HitID
	for hit, identity := range s.hitItems {
		if identity == modelIdentity(first) {
			stale = hit
			break
		}
	}
	if stale == 0 {
		t.Fatal("render produced no hit for the first model")
	}

	// A refresh both filters out the old second row and moves the clicked model.
	// The old numeric index now names third and must not become the click action.
	s.catalog.models = []client.ModelInfo{third, first}
	s.filtered = []client.ModelInfo{third, first}
	_, handled, _ := s.HandleMsg(surfaceHitMsg{ID: stale})
	if !handled || s.list.CursorID() != modelIdentity(first) {
		t.Fatalf("stale click selected (%d,%q), want refreshed first-model identity", s.list.Cursor(), s.list.CursorID())
	}
	_, handled, shouldClose := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	intent, ok := s.intent.(modelsSelectIntent)
	if !handled || !shouldClose || !ok || intent.selection.ProviderID != first.ProviderID || intent.selection.ModelID != first.ID {
		t.Fatalf("Enter after stale click intent=%#v handled=%v close=%v, want provider/first", s.intent, handled, shouldClose)
	}
}

func TestModelsWheelCancelsPendingRevealBeforeRender(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reveal func(*modelsState)
	}{
		{"key", func(s *modelsState) { s.moveCursor(bounded.End) }},
		{"click", func(s *modelsState) {
			s.hitItems = map[HitID]string{1: modelIdentity(s.filtered[len(s.filtered)-1])}
			_, _, _ = s.HandleMsg(surfaceHitMsg{ID: 1})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := boundedScenarioModelsState(t, 20)
			_, _ = s.Render(40, 12)
			tc.reveal(s)
			if !s.list.RevealPending() {
				t.Fatal("selection did not request reveal")
			}
			_, _ = s.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
			want := s.list.Offset()
			if s.list.RevealPending() {
				t.Fatal("wheel did not cancel pending reveal")
			}
			_, _ = s.Render(40, 12)
			if got := s.list.Offset(); got != want {
				t.Fatalf("render reapplied cancelled reveal: offset=%d want=%d", got, want)
			}
		})
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
	if got, want := len(withPrefix)+len(withSuffix), len(withoutPrefix)+len(withoutSuffix)+2; got != want {
		t.Fatalf("actual chrome rows with status and separator = %d, want %d", got, want)
	}
	if len(withSuffix) < 2 || withSuffix[0] != "" || !strings.Contains(stripANSIstr(withSuffix[1]), statusLine) {
		t.Fatalf("provider status suffix does not begin with separator then status: %q", withSuffix)
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
	if s.list.Cursor() != 3 {
		t.Fatalf("cursor after pgdown = %d, want first item after the effective 3-row window", s.list.Cursor())
	}
}

func TestModelsNoCacheLegendConsumesFixedChromeBudget(t *testing.T) {
	cached := client.ModelInfo{ProviderID: "anthropic", ID: "claude-opus", PromptCached: true}
	picker := modelsState{
		catalog:  modelCatalog{models: []client.ModelInfo{cached}},
		filtered: []client.ModelInfo{cached},
		filter:   textinput.New(),
		deps:     surfaceDeps{keys: defaultKeys(), marks: defaultHelpKeys(), theme: aztec()},
	}
	prefix, cachedSuffix := modelsFixedLines(picker, "")

	uncached := cached
	uncached.PromptCached = false
	picker.catalog.models, picker.filtered = []client.ModelInfo{uncached}, []client.ModelInfo{uncached}
	_, uncachedSuffix := modelsFixedLines(picker, "")
	if got, want := len(uncachedSuffix), len(cachedSuffix)+1; got != want {
		t.Fatalf("uncached fixed chrome rows = %d, want %d", got, want)
	}

	const cachedListBudget = 4
	out, _ := picker.Render(100, len(prefix)+len(cachedSuffix)+cachedListBudget)
	if picker.rowBudget != cachedListBudget-1 {
		t.Fatalf("uncached page budget = %d, want %d after legend chrome", picker.rowBudget, cachedListBudget-1)
	}
	if !strings.Contains(stripANSIstr(out), "no-cache = no prompt-cache breakpoint sent") {
		t.Fatalf("uncached fixed legend did not render:\n%s", stripANSIstr(out))
	}
}
