// Package ui models_catalog.go owns the durable root-owned model catalog and
// selection reconciliation state, which survives /models picker instances.
package ui

import (
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// modelCatalog is Model-owned model discovery state. It survives picker close and
// supplies selection reconciliation, header provenance, and idle gateway status.
type modelCatalog struct {
	models                      []client.ModelInfo
	statuses                    []client.ProviderStatus
	configProvenanceProviderIDs map[string]bool
	active                      client.ModelSelection
	globalDefault               client.ModelSelection
}

// reduceModelCatalog records a successful catalog response and reconciles the
// next session selection against its available providers. It is independent of
// whether the /models picker is open.
func (m Model) reduceModelCatalog(msg client.ModelsMsg) Model {
	m.modelCatalog.models = msg.Models
	m.modelCatalog.statuses = msg.Statuses
	m.modelCatalog.configProvenanceProviderIDs = configProvenanceProviderSet(msg.Statuses)
	m.modelCatalog.active = m.createModelSelection
	return m.reconcileSelection()
}

// updateModelsMsg handles catalog responses that bypass a closed picker and
// selection-persistence receipts. An open picker handles its response first and
// forwards it here through modelsCatalogIntent.
func (m Model) updateModelsMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case client.ModelsMsg:
		return m.applyModelsCatalog(msg)
	case selectionSavedMsg:
		if msg.err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render(
				"model set for this run, but could not persist: " + sanitizeTerminal(msg.err.Error()))
		}
		return m, nil, true
	default:
		return m, nil, false
	}
}

// applyModelsCatalog applies a root-accepted catalog response to durable Model
// state, mirrors it into an open picker, and continues startup connection after
// either a result or a listing failure. An open models surface consumes every
// ModelsMsg before this method; after it closes, dispatchNonInputMsg compares its
// synchronized token and rejects late responses.
func (m Model) applyModelsCatalog(msg client.ModelsMsg) (tea.Model, tea.Cmd, bool) {
	if m.browsingStartupSessions {
		m.modelsReconciled = true
	}
	if msg.Err != nil {
		m.modelCatalog.statuses = nil
		m.modelCatalog.configProvenanceProviderIDs = nil
		if surface, ok := m.modal.(*modelsState); ok {
			surface.loading = false
			surface.err = msg.Err
			surface.catalog.statuses = nil
			surface.catalog.configProvenanceProviderIDs = nil
		}
		if m.phase == phaseConnecting {
			return m, m.createSessionCmd(), true
		}
		return m, nil, true
	}
	m = m.reduceModelCatalog(msg)
	if surface, ok := m.modal.(*modelsState); ok {
		surface.catalog = m.modelCatalog
		surface.provenance = m.modelProvenanceLine()
	}
	if !m.gatewayNoticeShown && !isToolhiveProviderID(m.resolvedSessionModel.ProviderID) {
		if row, ok := availableNotDefaultStatus(msg.Statuses); ok {
			pid := sanitizeTerminal(row.ProviderID)
			m.gatewayNotice = pid + " gateway available (" + strconv.Itoa(int(row.ModelCount)) +
				" models, no API key needed) — /models to use it, or --default-provider " + pid
			m.gatewayNoticeShown = true
		}
	}
	if m.phase == phaseConnecting {
		return m, m.createSessionCmd(), true
	}
	return m, nil, true
}

// reconcileSelection clears the active selection to the server default when its
// provider has no catalog row. A model absent from an available provider is kept:
// the server validates that model string and a live refresh may not have landed.
// This changes only this run's selection; it never rewrites persisted state.
func (m Model) reconcileSelection() Model {
	if m.modelCatalog.active.IsZero() {
		return m
	}
	providerAvailable := false
	for _, mi := range m.modelCatalog.models {
		if m.modelCatalog.active.Matches(mi) {
			return m
		}
		if mi.ProviderID == m.modelCatalog.active.ProviderID {
			providerAvailable = true
		}
	}
	if providerAvailable {
		return m
	}
	gone := m.modelCatalog.active.ModelID
	if gone == "" {
		gone = m.modelCatalog.active.ProviderID
	}
	m.modelCatalog.active = client.ModelSelection{}
	m.createModelSelection = client.ModelSelection{}
	notice := "saved model " + sanitizeTerminal(gone) +
		" is no longer available (provider key removed?) — using the server default"
	if id := m.resolvedSessionModel.ModelID; id != "" {
		notice += " — now running " + sanitizeTerminal(id)
	}
	m.statusMsg = m.deps.Theme.Style("warning").Render(notice)
	return m
}

// liveModelLabel returns the live session model's catalog display name, falling
// back to its raw ID and then a neutral placeholder while it is unknown.
func (m Model) liveModelLabel() string {
	rm := m.resolvedSessionModel
	if rm.ModelID == "" {
		return "the current model"
	}
	for _, mi := range m.modelCatalog.models {
		if mi.ProviderID == rm.ProviderID && mi.ID == rm.ModelID && mi.DisplayName != "" {
			return mi.DisplayName
		}
	}
	return rm.ModelID
}

// modelProvenanceLine builds the picker header's best-effort current-model
// provenance from client-held state. The server remains authoritative.
func (m Model) modelProvenanceLine() string {
	eff := client.ModelSelection{ProviderID: m.resolvedSessionModel.ProviderID, ModelID: m.resolvedSessionModel.ModelID}
	if eff.ModelID == "" {
		return ""
	}
	label := m.liveModelLabel()
	line := "current: " + sanitizeTerminal(label) + " (" + m.modelProvenance(eff) + ")"
	if row, ok := availableNotDefaultStatus(m.modelCatalog.statuses); ok {
		if row.ProviderID != eff.ProviderID && !m.modelCatalog.configProvenanceProviderIDs[eff.ProviderID] {
			line += " · " + sanitizeTerminal(row.ProviderID) +
				" gateway also available — outranked by your " + sanitizeTerminal(eff.ProviderID) + " key"
		}
	}
	return line
}

// modelProvenance returns the best-effort provenance word for an effective model.
func (m Model) modelProvenance(eff client.ModelSelection) string {
	switch {
	case !m.pickedThisSession.IsZero() && m.pickedThisSession == eff:
		return "picked this session"
	case m.deps.Model != "" && m.deps.Model == eff.ModelID:
		return "--model flag"
	case m.deps.WorkspaceDefaultSet && m.deps.WorkspaceDefault == eff:
		return "workspace default"
	case !m.deps.GlobalDefault.IsZero() && m.deps.GlobalDefault == eff:
		return "global default"
	case statusAutoSelected(m.modelCatalog.statuses, eff.ProviderID):
		return "auto-selected"
	default:
		return "server default"
	}
}

// statusAutoSelected reports whether the provider's status says the server chose
// its default model automatically rather than from an operator configuration.
func statusAutoSelected(statuses []client.ProviderStatus, providerID string) bool {
	for _, s := range statuses {
		if s.ProviderID == providerID {
			return s.DefaultModelAutoSelected
		}
	}
	return false
}

// configProvenanceProviderSet projects config-detected provider IDs from the broader
// operator-status list. This local catalog-provenance classification is ToolHive-only.
func configProvenanceProviderSet(statuses []client.ProviderStatus) map[string]bool {
	var out map[string]bool
	for _, s := range statuses {
		if isToolhiveProviderID(s.ProviderID) {
			if out == nil {
				out = make(map[string]bool, 1)
			}
			out[s.ProviderID] = true
		}
	}
	return out
}

func isToolhiveProviderID(providerID string) bool {
	return providerID == "toolhive" || providerID == "toolhive-anthropic"
}

// availableNotDefaultStatus returns the first reachable intent-driven provider
// that is available but not the active default.
func availableNotDefaultStatus(statuses []client.ProviderStatus) (client.ProviderStatus, bool) {
	for _, s := range statuses {
		if s.AvailableNotDefault {
			return s, true
		}
	}
	return client.ProviderStatus{}, false
}
