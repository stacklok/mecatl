package ui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func effortHandoffPick(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	mm, _ := m.runEffort()
	m = mm.(Model)
	m = pressEffortKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	mm, cmd, _ := m.onEffortKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	return mm.(Model), cmd
}

func assertEffortSourceLiveRearmed(t *testing.T, m Model, live *fakeLiveStreamer) {
	t.Helper()
	if m.liveArmed != "sess-test-0001" || m.liveCh == nil || live.calls != 1 || live.lastID != "sess-test-0001" {
		t.Fatalf("source live feed not rearmed: liveArmed=%q liveCh=%v calls=%d lastID=%q", m.liveArmed, m.liveCh, live.calls, live.lastID)
	}
}

// driveEffortHandoffFailure runs the fork/hydrate leaf of an effort-pick command
// and applies its resulting effortHandoffFailedMsg, without draining the
// reducer's own follow-up commands. armLiveFeed re-arms the source synchronously
// before returning its tea.Cmd, so the assertion needs no further draining — and
// must not get any, since a wired fakeLiveStreamer's wait command never
// terminates (mirrors the sibling modelSwitchFailedMsg test's feedModelSwitchBusiness).
func driveEffortHandoffFailure(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	batch, ok := runCmd(cmd).(tea.BatchMsg)
	if !ok || len(batch) != 3 {
		t.Fatalf("effort handoff command = %#v, want the 3-leaf switchEffort/save/spinner batch", batch)
	}
	msg := runCmd(batch[0])
	if _, ok := msg.(effortHandoffFailedMsg); !ok {
		t.Fatalf("effort handoff leaf message = %T, want effortHandoffFailedMsg", msg)
	}
	mm, followup := m.Update(msg)
	m = mm.(Model)
	if followup == nil {
		t.Fatal("effort handoff failure returned no focus/rearm follow-up")
	}
	return m
}

func TestMecatuiEffortHandoffRecovery_Scenario1_FailuresRetainUsableSource(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	conv.forkErr = context.DeadlineExceeded
	live := wireModelSwitchLiveFeed(&m)
	beforeBlocks := len(m.conv.blocks)
	m, cmd := effortHandoffPick(t, m)
	m = driveEffortHandoffFailure(t, m, cmd)
	if m.sessionID != "sess-test-0001" || m.phase != phaseIdle || len(m.conv.blocks) != beforeBlocks || !m.prompt.Focused() {
		t.Fatalf("fork failure lost source usability: id=%q phase=%v blocks=%d focused=%v", m.sessionID, m.phase, len(m.conv.blocks), m.prompt.Focused())
	}
	if m.restartFailed || len(conv.closed()) != 0 {
		t.Fatalf("fork failure armed obsolete retry or closed source: restartFailed=%v closed=%v", m.restartFailed, conv.closed())
	}
	assertEffortSourceLiveRearmed(t, m, live)
}

func TestMecatuiEffortHandoffRecovery_Scenario1_HydrationFailureCleansExactTarget(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	conv.getSessionErr = errors.New("hydrate")
	live := wireModelSwitchLiveFeed(&m)
	m, cmd := effortHandoffPick(t, m)
	m = driveEffortHandoffFailure(t, m, cmd)
	if m.sessionID != "sess-test-0001" || m.phase != phaseIdle || !m.prompt.Focused() {
		t.Fatalf("hydration failure did not restore source: id=%q phase=%v focused=%v", m.sessionID, m.phase, m.prompt.Focused())
	}
	if got := conv.closed(); len(got) != 1 || got[0] != "sess-fork-1" {
		t.Fatalf("closed=%v, want only exact target", got)
	}
	assertEffortSourceLiveRearmed(t, m, live)
}

func TestMecatuiEffortHandoffRecovery_Scenario1_AdoptsTargetBeforeClosingSource(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	conv.resolvedModel = client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5", ReasoningEffort: "low"}
	conv.getSessionCaps = client.Capabilities{ModelSelection: true, ManualCompaction: true}
	conv.getSessionTitle = "successor"
	m, cmd := effortHandoffPick(t, m)
	beforeBlocks := len(m.conv.blocks)
	m = feedCmd(t, m, cmd)
	if m.sessionID != "sess-fork-1" || len(m.conv.blocks) != beforeBlocks || m.sessionTitle != "successor" || m.resolvedSessionModel.ReasoningEffort != "low" {
		t.Fatalf("target was not adopted with local projection preserved: id=%q blocks=%d title=%q effort=%q", m.sessionID, len(m.conv.blocks), m.sessionTitle, m.resolvedSessionModel.ReasoningEffort)
	}
	if m.caps.ManualCompaction != true || len(conv.closed()) != 1 || conv.closed()[0] != "sess-test-0001" {
		t.Fatalf("target metadata/source retirement mismatch: caps=%+v closed=%v", m.caps, conv.closed())
	}
	conv.mu.Lock()
	gotIDs := append([]string(nil), conv.getSessionIDs...)
	conv.mu.Unlock()
	if len(gotIDs) == 0 {
		t.Fatalf("GetSession IDs = %v, want the returned fork target", gotIDs)
	}
	for _, id := range gotIDs {
		if id != "sess-fork-1" {
			t.Fatalf("GetSession IDs = %v, want every hydration to use returned fork target", gotIDs)
		}
	}
}

func TestMecatuiEffortHandoffOverlaysSessionMediaAndResetsDerivedState(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	m.usage = client.Usage{InputTokens: 11, OutputTokens: 7}
	m.contextTokens = 42
	m.providerRoute = "old-downstream"
	m.caps = client.Capabilities{ModelSelection: true, Teams: true, Image: true, Audio: true}
	oldResolved := m.resolvedSessionModel
	conv.getSessionCaps = client.Capabilities{SessionMediaPresent: true}
	conv.resolvedModel = client.ResolvedModel{}

	m, cmd := effortHandoffPick(t, m)
	m = feedCmd(t, m, cmd)

	if m.caps.ModelSelection != true || m.caps.Teams != true || m.caps.Image || m.caps.Audio || !m.caps.SessionMediaPresent {
		t.Fatalf("session media-only target did not preserve global caps and overlay text-only media: %+v", m.caps)
	}
	if m.resolvedSessionModel != oldResolved {
		t.Fatalf("empty target resolved model = %+v, want source fallback %+v", m.resolvedSessionModel, oldResolved)
	}
	if m.usage != (client.Usage{}) || m.contextTokens != 0 || m.providerRoute != "" {
		t.Fatalf("successor-derived state was not reset: usage=%+v context=%d route=%q", m.usage, m.contextTokens, m.providerRoute)
	}
	conv.mu.Lock()
	gotIDs := append([]string(nil), conv.getSessionIDs...)
	conv.mu.Unlock()
	if len(gotIDs) == 0 {
		t.Fatalf("GetSession IDs = %v, want returned target ID", gotIDs)
	}
	for _, id := range gotIDs {
		if id != "sess-fork-1" {
			t.Fatalf("GetSession IDs = %v, want every hydration to use returned target ID", gotIDs)
		}
	}
}

func TestMecatuiEffortHandoffRecovery_Scenario1_StaleReadyCleansTargetAndStaleFailureIsInert(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	conv := m.deps.Session.(*fakeConv)
	m.phase = phaseConnecting
	m.modelSwitchRequestToken = 2
	mm, cmd, _ := m.updateLifecycle(effortHandoffReadyMsg{token: 1, sourceID: m.sessionID, targetID: "stale-target"})
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	if got := conv.closed(); len(got) != 1 || got[0] != "stale-target" {
		t.Fatalf("stale ready cleanup=%v, want stale target only", got)
	}
	beforeID, beforePhase := m.sessionID, m.phase
	mm, cmd, _ = m.updateLifecycle(effortHandoffFailedMsg{token: 1, sourceID: m.sessionID, targetID: "ignored", err: errors.New("stale")})
	m = mm.(Model)
	m = feedCmd(t, m, cmd)
	if m.sessionID != beforeID || m.phase != beforePhase || len(conv.closed()) != 1 {
		t.Fatalf("stale failure changed handoff state: id=%q phase=%v closed=%v", m.sessionID, m.phase, conv.closed())
	}
}
