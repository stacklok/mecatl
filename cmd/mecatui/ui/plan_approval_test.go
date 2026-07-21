package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// planAskModel builds a connected, awaiting-approval Model with a PresentPlan ask.
func planAskModel(t *testing.T, offerAlways bool) Model {
	t.Helper()
	m := New(Deps{
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.ask = pendingAsk{
		AskID:       "sess-test-0001:1:presentplan-1",
		Tool:        "PresentPlan",
		Reason:      "Plan mode requires approval to execute.",
		offerAlways: offerAlways,
	}
	return m
}

func TestIsPlanAsk(t *testing.T) {
	if !isPlanAsk("PresentPlan") {
		t.Error("isPlanAsk(PresentPlan) = false, want true")
	}
	if isPlanAsk("Bash") {
		t.Error("isPlanAsk(Bash) = true, want false")
	}
	if isPlanAsk("Write") {
		t.Error("isPlanAsk(Write) = true, want false")
	}
	if isPlanAsk("") {
		t.Error("isPlanAsk('') = true, want false")
	}
}

func TestPlanAskRendersPlanApprovalTitle(t *testing.T) {
	m := planAskModel(t, true)
	got := stripANSIstr(m.View().Content)
	if strings.Contains(got, "Permission required") {
		t.Errorf("plan ask must NOT render 'Permission required', got %q", got)
	}
	if !strings.Contains(got, "Plan ready for review") {
		t.Errorf("plan ask must render 'Plan ready for review', got %q", got)
	}
}

func TestPlanAskButtonCopy(t *testing.T) {
	m := planAskModel(t, true)
	got := stripANSIstr(m.View().Content)
	for _, want := range []string{
		"[A]pprove & run",
		"[W] auto-accept edits",
		"[D] iterate",
		"auto-accept allows every edit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan-approval modal missing %q in: %s", want, got)
		}
	}
}

func TestPlanAskNoAlwaysButtonCopy(t *testing.T) {
	// When not offered always (child ask, which doesn't happen in plan mode
	// but we test the fallback), the button copy omits the auto-accept button.
	m := planAskModel(t, false)
	got := stripANSIstr(m.View().Content)
	if strings.Contains(got, "[W] auto-accept") {
		t.Errorf("no-always plan ask must NOT render the auto-accept button: %s", got)
	}
	if !strings.Contains(got, "[A]pprove & run") {
		t.Error("no-always plan ask must still render 'Approve & run'")
	}
	if !strings.Contains(got, "[D] iterate") {
		t.Error("no-always plan ask must still render 'iterate'")
	}
}

func TestPlanAskFooterLabel(t *testing.T) {
	m := planAskModel(t, true)
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "plan review") {
		t.Errorf("plan ask footer must show 'plan review', got %q", got)
	}
	if strings.Contains(got, "awaiting approval") {
		t.Errorf("plan ask footer must NOT show 'awaiting approval', got %q", got)
	}
}

func TestPlanAskFooterHelpLine(t *testing.T) {
	m := planAskModel(t, true)
	got := stripANSIstr(m.renderFooter())
	for _, want := range []string{
		"A approve & run",
		"W auto-accept",
		"D iterate",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan ask footer help missing %q in: %s", want, got)
		}
	}
}

func TestPlanAskApproveResolvesAllowOnce(t *testing.T) {
	m := planAskModel(t, true)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.phase != phaseRunning {
		t.Fatalf("approve must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission allowed" {
		t.Errorf("allow-once notice = %q, want 'permission allowed'", got)
	}
}

func TestPlanAskAlwaysResolvesAllowAlways(t *testing.T) {
	m := planAskModel(t, true)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if m.phase != phaseRunning {
		t.Fatalf("always must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission allowed (always, this session)" {
		t.Errorf("always-allow notice = %q", got)
	}
}

func TestPlanAskDenyIterates(t *testing.T) {
	m := planAskModel(t, true)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.phase != phaseRunning {
		t.Fatalf("deny must return to phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission denied" {
		t.Errorf("deny notice = %q", got)
	}
}

func TestStopPlanApprovedFooterLabel(t *testing.T) {
	text, slot := stopReasonLabel("plan_approved")
	if text != "plan approved · executing" {
		t.Errorf("stopReasonLabel(plan_approved) text = %q, want 'plan approved · executing'", text)
	}
	if slot != "muted" {
		t.Errorf("stopReasonLabel(plan_approved) slot = %q, want 'muted'", slot)
	}
}

func TestStopPlanApprovedReachesFooter(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	m = applyAll(m, client.ResultMsg{Stop: "plan_approved"})
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "plan approved · executing") {
		t.Errorf("ResultMsg{Stop:plan_approved} → footer = %q, want it to contain 'plan approved · executing'", got)
	}
}

func TestPlanAskQueueBadge(t *testing.T) {
	m := planAskModel(t, true)
	// Enqueue a second ask — the plan ask is the head, the queue has one entry.
	m.askQueue = append(m.askQueue, pendingAsk{AskID: "sess-test-0001:2:c2", Tool: "Bash"})
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "Plan ready for review (1 of 2)") {
		t.Errorf("plan ask with queue must show '(1 of 2)' badge, got %q", got)
	}
	// Footer must also carry the badge.
	footer := stripANSIstr(m.renderFooter())
	if !strings.Contains(footer, "plan review (1 of 2)") {
		t.Errorf("plan ask footer with queue must show 'plan review (1 of 2)', got %q", footer)
	}
}

func TestGenericAskFooterIsUnchanged(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.phase = phaseAwaitingApproval
	m.ask = pendingAsk{AskID: "sess-test-0001:1:c1", Tool: "Bash", Reason: "Bash requires approval"}
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "awaiting approval") {
		t.Errorf("generic ask footer must show 'awaiting approval', got %q", got)
	}
	if strings.Contains(got, "plan review") {
		t.Errorf("generic ask footer must NOT show 'plan review', got %q", got)
	}
	// The header help line must not leak plan-approval hints.
	if strings.Contains(got, "A approve & run") {
		t.Errorf("generic ask footer help must not show plan-approval copy: %q", got)
	}
}

func TestPlanAskRendersModelNames(t *testing.T) {
	m := planAskModel(t, true)
	m.effectiveModel = client.ResolvedModel{ModelID: "gpt-5", ProviderID: "openai"}
	got := stripANSIstr(m.View().Content)
	if !strings.Contains(got, "plan model: gpt-5") {
		t.Errorf("plan modal should show the plan model, got %q", got)
	}
	if !strings.Contains(got, "execute model: session default model") {
		t.Errorf("plan modal should note the execute model runs on the session default, got %q", got)
	}
}

func TestPlanAskRendersModelNamesWhenUnknown(t *testing.T) {
	m := planAskModel(t, true)
	m.effectiveModel = client.ResolvedModel{} // no echo
	got := stripANSIstr(m.View().Content)
	if strings.Contains(got, "plan model:") {
		t.Errorf("plan modal with no model echo must not show 'plan model:', got %q", got)
	}
	if !strings.Contains(got, "execute model: session default model") {
		t.Errorf("plan modal with no model echo must still name the default model, got %q", got)
	}
}
