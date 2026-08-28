package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// modelsResultFromCmd executes the actual command (including a Batch) and returns
// its ListModels result without constructing a ModelsMsg in the test.
func modelsResultFromCmd(t *testing.T, cmd tea.Cmd) client.ModelsMsg {
	t.Helper()
	msg := runCmd(cmd)
	if result, ok := msg.(client.ModelsMsg); ok {
		return result
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("command message = %T, want ModelsMsg or tea.BatchMsg", msg)
	}
	results := make(chan tea.Msg, len(batch))
	for _, command := range batch {
		go func(command tea.Cmd) { results <- command() }(command)
	}
	deadline := time.After(time.Second)
	for range batch {
		select {
		case msg := <-results:
			if result, ok := msg.(client.ModelsMsg); ok {
				return result
			}
		case <-deadline:
			t.Fatal("timed out waiting for ListModelsCmd result")
		}
	}
	t.Fatal("command batch did not contain a ModelsMsg")
	return client.ModelsMsg{}
}

func TestInitListModelsCmdCarriesInitialRequestToken(t *testing.T) {
	fm := sampleModels()
	m := newTestModelFromDeps(Deps{Ctx: context.Background(), Models: fm, Theme: theme.New("aztec", theme.AztecPalette())})
	if m.modelCatalogRequestToken == 0 {
		t.Fatal("New must mint the startup model catalog request token")
	}

	result := modelsResultFromCmd(t, m.Init())
	if result.RequestToken != m.modelCatalogRequestToken {
		t.Fatalf("startup ListModels result token = %d, want minted token %d", result.RequestToken, m.modelCatalogRequestToken)
	}
	if fm.calls != 1 {
		t.Fatalf("startup ListModels calls = %d, want 1", fm.calls)
	}
}

func TestModelsCatalogReopenListModelsCmdCarriesNewRequestToken(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})

	mm, firstCmd := m.runModels()
	m = mm.(Model)
	firstToken := modelsSurface(t, m).requestToken
	if firstToken != m.modelCatalogRequestToken+1 {
		t.Fatalf("first surface request token = %d, want root token %d + 1", firstToken, m.modelCatalogRequestToken)
	}
	first := modelsResultFromCmd(t, firstCmd)
	if first.RequestToken != firstToken {
		t.Fatalf("first ListModels result token = %d, want %d", first.RequestToken, firstToken)
	}

	mm, closeCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = feedCmd(t, mm.(Model), closeCmd)
	if m.modal != nil {
		t.Fatal("escape should close the first models surface")
	}
	if m.modelCatalogRequestToken != firstToken {
		t.Fatalf("closed root token = %d, want surface token %d", m.modelCatalogRequestToken, firstToken)
	}

	mm, reopenedCmd := m.runModels()
	m = mm.(Model)
	reopenedToken := modelsSurface(t, m).requestToken
	reopened := modelsResultFromCmd(t, reopenedCmd)
	if reopened.RequestToken != reopenedToken {
		t.Fatalf("reopened ListModels result token = %d, want %d", reopened.RequestToken, reopenedToken)
	}
	if reopenedToken <= firstToken {
		t.Fatalf("reopened request token = %d, want greater than first token %d", reopenedToken, firstToken)
	}
}

func TestModelsCatalogRejectsStaleResultAfterPickerReopen(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})

	mm, _ := m.runModels()
	m = mm.(Model)
	firstToken := modelsSurface(t, m).requestToken

	mm, closeCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = feedCmd(t, mm.(Model), closeCmd)
	if m.modal != nil {
		t.Fatal("escape should close the first models surface")
	}

	mm, _ = m.runModels()
	m = mm.(Model)
	currentToken := modelsSurface(t, m).requestToken
	if currentToken <= firstToken {
		t.Fatalf("reopened request token = %d, want greater than first token %d", currentToken, firstToken)
	}

	newest := client.ModelsMsg{
		RequestToken: currentToken,
		Models:       []client.ModelInfo{{ProviderID: "new", ID: "model"}},
	}
	mm, _ = m.Update(newest)
	m = mm.(Model)
	if got := m.modelCatalog.models; len(got) != 1 || got[0].ProviderID != "new" {
		t.Fatalf("root catalog after newest result = %+v, want new/model", got)
	}
	if got := modelsSurface(t, m).catalog.models; len(got) != 1 || got[0].ProviderID != "new" {
		t.Fatalf("picker catalog after newest result = %+v, want new/model", got)
	}
	if got := modelsSurface(t, m).requestToken; got != currentToken {
		t.Fatalf("picker token after newest result = %d, want %d", got, currentToken)
	}

	stale := client.ModelsMsg{
		RequestToken: firstToken,
		Models:       []client.ModelInfo{{ProviderID: "stale", ID: "model"}},
	}
	mm, _ = m.Update(stale)
	m = mm.(Model)
	if got := m.modelCatalog.models; len(got) != 1 || got[0].ProviderID != "new" {
		t.Fatalf("root catalog after stale result = %+v, want unchanged new/model", got)
	}
	if got := modelsSurface(t, m).catalog.models; len(got) != 1 || got[0].ProviderID != "new" {
		t.Fatalf("picker catalog after stale result = %+v, want unchanged new/model", got)
	}
}

func TestModelsCatalogRejectsStaleErrorWithoutMutatingPickerOrClosedRoot(t *testing.T) {
	statuses := []client.ProviderStatus{{ProviderID: "toolhive", State: "ok"}}
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.modelCatalog.statuses = statuses
	m.modelCatalog.configProvenanceProviderIDs = map[string]bool{"toolhive": true}

	mm, _ := m.runModels()
	m = mm.(Model)
	currentToken := modelsSurface(t, m).requestToken
	surface := modelsSurface(t, m)
	if !surface.loading || surface.err != nil {
		t.Fatalf("picker before stale error = loading %t, err %v", surface.loading, surface.err)
	}

	mm, _ = m.Update(client.ModelsMsg{RequestToken: currentToken - 1, Err: errors.New("stale")})
	m = mm.(Model)
	surface = modelsSurface(t, m)
	if !surface.loading || surface.err != nil {
		t.Fatalf("stale error changed picker to loading %t, err %v", surface.loading, surface.err)
	}
	if got := m.modelCatalog.statuses; len(got) != 1 || got[0] != statuses[0] {
		t.Fatalf("stale error cleared root provider statuses: %+v", got)
	}
	if !m.modelCatalog.configProvenanceProviderIDs["toolhive"] {
		t.Fatal("stale error cleared root provider provenance")
	}

	mm, closeCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = feedCmd(t, mm.(Model), closeCmd)
	if m.modal != nil {
		t.Fatal("escape should close the models surface")
	}

	mm, _ = m.Update(client.ModelsMsg{RequestToken: currentToken - 1, Err: errors.New("stale")})
	m = mm.(Model)
	if m.modal != nil {
		t.Fatal("stale error must not resurrect the closed picker")
	}
	if got := m.modelCatalog.statuses; len(got) != 1 || got[0] != statuses[0] {
		t.Fatalf("stale closed-picker error cleared root provider statuses: %+v", got)
	}
	if !m.modelCatalog.configProvenanceProviderIDs["toolhive"] {
		t.Fatal("stale error cleared root provider provenance")
	}

	mm, _ = m.Update(client.ModelsMsg{RequestToken: currentToken, Err: errors.New("current")})
	m = mm.(Model)
	if m.modal != nil {
		t.Fatal("current error after close must not reopen the picker")
	}
	if m.modelCatalog.statuses != nil || m.modelCatalog.configProvenanceProviderIDs != nil {
		t.Fatalf("current closed-picker error must clear root provider metadata, got statuses=%+v provenance=%v", m.modelCatalog.statuses, m.modelCatalog.configProvenanceProviderIDs)
	}
}

func TestModelsCatalogRejectsStaleResultAfterPickerClose(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	mm, _ := m.runModels()
	m = mm.(Model)
	currentToken := modelsSurface(t, m).requestToken

	mm, closeCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = feedCmd(t, mm.(Model), closeCmd)
	if m.modal != nil {
		t.Fatal("escape should close the models surface")
	}
	if m.modelCatalogRequestToken != currentToken {
		t.Fatalf("closed root token = %d, want surface token %d", m.modelCatalogRequestToken, currentToken)
	}
	staleToken := currentToken - 1

	mm, _ = m.Update(client.ModelsMsg{
		RequestToken: currentToken,
		Models:       []client.ModelInfo{{ProviderID: "current", ID: "model"}},
	})
	m = mm.(Model)
	mm, _ = m.Update(client.ModelsMsg{
		RequestToken: staleToken,
		Models:       []client.ModelInfo{{ProviderID: "stale", ID: "model"}},
	})
	m = mm.(Model)

	if m.modal != nil {
		t.Fatal("closed picker must not reopen for a stale catalog result")
	}
	if got := m.modelCatalog.models; len(got) != 1 || got[0].ProviderID != "current" {
		t.Fatalf("root catalog after stale closed-picker result = %+v, want unchanged current/model", got)
	}
}

func TestModelsCatalogCurrentRootResultAppliesWithoutModelsSurface(t *testing.T) {
	for _, tc := range []struct {
		name      string
		openOther bool
	}{
		{name: "no modal"},
		{name: "different modal", openOther: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
			if tc.openOther {
				m.modal = &sessionsState{}
			}
			current := m.modelCatalogRequestToken
			mm, _ := m.Update(client.ModelsMsg{
				RequestToken: current,
				Models:       []client.ModelInfo{{ProviderID: "current", ID: "model"}},
			})
			m = mm.(Model)
			if got := m.modelCatalog.models; len(got) != 1 || got[0].ProviderID != "current" {
				t.Fatalf("root catalog = %+v, want current/model", got)
			}
		})
	}
}
