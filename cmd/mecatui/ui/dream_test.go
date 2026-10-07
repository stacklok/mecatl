package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

type fakeDream struct {
	generated []string
	decided   [][2]string
	plan      client.DreamPlan
	receipt   client.DreamReceipt
	err       error
}

func (f *fakeDream) GenerateDreamPlan(_ context.Context, target string) (client.DreamPlan, error) {
	f.generated = append(f.generated, target)
	plan := f.plan
	plan.Target = target
	return plan, f.err
}
func (f *fakeDream) DecideDreamPlan(_ context.Context, id, decision string) (client.DreamReceipt, error) {
	f.decided = append(f.decided, [2]string{id, decision})
	return f.receipt, f.err
}

func dreamModel(t *testing.T, f *fakeDream, caps *client.ManualDreamCapabilities) Model {
	t.Helper()
	th := theme.New("aztec", theme.AztecPalette())
	m, _, _ := newTestModel(t, th)
	m.phase = phaseIdle
	m.deps.Dream = f
	m.caps.ManualDream = caps
	return m
}

func pressDream(t *testing.T, m Model, pressed rune) (Model, tea.Cmd) {
	t.Helper()
	msg := tea.KeyPressMsg{Code: pressed, Text: string(pressed)}
	if pressed == '\r' {
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	mm, cmd, handled := m.onDreamKey(msg)
	if !handled {
		t.Fatal("dream key was not handled")
	}
	return mm.(Model), cmd
}

func TestDreamCapabilityGatingAndUnavailableReasons(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	if _, ok := builtinByName(client.Capabilities{}, wiredCollaborators{Dream: true}, "dream"); ok {
		t.Fatal("older server exposed /dream")
	}
	if strings.Contains(stripANSIstr(helpBody(th, client.Capabilities{}, defaultHelpKeys())), "/dream") {
		t.Fatal("older server exposed /dream in help")
	}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{UnavailableReason: "project store disabled"}, UserModel: client.DreamTargetCapability{UnavailableReason: "user model read-only"}}
	if _, ok := builtinByName(client.Capabilities{ManualDream: caps}, wiredCollaborators{Dream: true}, "dream"); !ok {
		t.Fatal("capability object did not expose /dream")
	}
	if !strings.Contains(stripANSIstr(helpBody(th, client.Capabilities{ManualDream: caps}, defaultHelpKeys())), "/dream") {
		t.Fatal("capability object did not expose /dream in help")
	}
	m := dreamModel(t, &fakeDream{}, caps)
	mm, cmd := m.openDream()
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("opening /dream made an RPC")
	}
	out := stripANSIstr(renderDreamOverlay(m.deps.Theme, m.dream, m.caps, defaultHelpKeys(), 100, 30))
	for _, want := range []string{"spends tokens", "does not enable or change scheduled consolidation", "project store disabled", "user model read-only", "generation is disabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay missing %q:\n%s", want, out)
		}
	}
}

func TestDreamGenerateSelectionConfirmAndCorrelation(t *testing.T) {
	f := &fakeDream{plan: client.DreamPlan{ID: "plan-1"}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}, UserModel: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, f, caps)
	mm, _ := m.openDream()
	m = mm.(Model)
	if len(f.generated) != 0 {
		t.Fatal("opening generated")
	}
	m, _ = pressDream(t, m, 'j')
	if m.dream.target != 1 {
		t.Fatal("target selection did not move to user model")
	}
	m, cmd := pressDream(t, m, '\r')
	if cmd == nil || len(f.generated) != 0 {
		t.Fatal("Enter should schedule, not synchronously execute, exactly one RPC")
	}
	msg := cmd().(client.DreamMsg)
	if len(f.generated) != 1 || f.generated[0] != client.DreamTargetUserModel {
		t.Fatalf("generated targets = %v", f.generated)
	}
	stale := msg
	stale.RequestID++
	mm, _ = m.updateDreamMsg(stale)
	if mm.(Model).dream.view != dreamGenerating {
		t.Fatal("wrong-request response was accepted")
	}
	mm, _ = m.updateDreamMsg(msg)
	m = mm.(Model)
	if m.dream.view != dreamReview || m.dream.plan.ID != "plan-1" {
		t.Fatalf("generation result not accepted: %+v", m.dream)
	}
	old := msg
	mm, _ = m.closeDream()
	m = mm.(Model)
	mm, _ = m.updateDreamMsg(old)
	if mm.(Model).dream.view != dreamClosed {
		t.Fatal("late result reopened closed overlay")
	}
}

func TestDreamReviewActionsReceiptsAndExplicitRegenerate(t *testing.T) {
	plan := client.DreamPlan{ID: "plan-1", Target: client.DreamTargetProjectMemory, PlannedOperationCount: 2, SourceCount: 3, Operations: []client.DreamOperation{
		{Kind: "exact_duplicate", Survivor: client.DreamParticipant{Key: "keep", Value: "v", Description: "d"}, Sources: []client.DreamParticipant{{Key: "drop", Value: "v", Description: "d"}}, Reason: "same value", ExactDuplicateEligible: true},
		{Kind: "synthesis", Survivor: client.DreamParticipant{Key: "revise", Value: "old", Description: "old desc"}, Sources: []client.DreamParticipant{{Key: "source", Value: "other", Description: "other desc"}}, Replacement: client.DreamReplacement{Value: "new", Description: "new desc"}, Reason: strings.Repeat("reason ", 100)},
	}}
	f := &fakeDream{generated: []string{client.DreamTargetProjectMemory}, plan: plan, receipt: client.DreamReceipt{Disposition: "applied", Planned: 3, Applied: 1, Conflicted: 1, Failed: 1}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, f, caps)
	m.dream = dreamState{view: dreamReview, plan: &plan}
	out := stripANSIstr(renderDreamOverlay(m.deps.Theme, m.dream, m.caps, defaultHelpKeys(), 90, 60))
	for _, want := range []string{"planned operations: 2", "exact-duplicate eligible: true", "survivor key: \"keep\"", "source 1 key: \"drop\"", "replacement value: \"new\"", "replacement description: \"new desc\""} {
		if !strings.Contains(out, want) {
			t.Errorf("review missing %q:\n%s", want, out)
		}
	}
	clipped := stripANSIstr(renderDreamOverlay(m.deps.Theme, m.dream, m.caps, defaultHelpKeys(), 90, 18))
	if !strings.Contains(clipped, "lines 1–") {
		t.Fatalf("bounded review has no scroll indicator:\n%s", clipped)
	}
	m, cmd := pressDream(t, m, 'a')
	if cmd != nil || m.dream.view != dreamConfirmApply {
		t.Fatal("apply did not require confirmation")
	}
	confirm := stripANSIstr(renderDreamOverlay(m.deps.Theme, m.dream, m.caps, defaultHelpKeys(), 100, 30))
	if !strings.Contains(confirm, "3 sources") || !strings.Contains(confirm, "survivor revisions") {
		t.Fatalf("incomplete apply confirmation: %s", confirm)
	}
	m, cmd = pressDream(t, m, '\r')
	msg := cmd().(client.DreamMsg)
	if len(f.decided) != 1 || f.decided[0] != [2]string{"plan-1", client.DreamDecisionApply} {
		t.Fatalf("decision payload = %v", f.decided)
	}
	mm, _ := m.updateDreamMsg(msg)
	m = mm.(Model)
	receipt := strings.Join(renderDreamReceipt(m.dream), "\n")
	for _, want := range []string{"disposition: applied", "planned: 3", "conflicted: 1", "failed: 1", "Memory changed", "Partial result"} {
		if !strings.Contains(receipt, want) {
			t.Errorf("receipt missing %q: %s", want, receipt)
		}
	}
	m, cmd = pressDream(t, m, 'r')
	if cmd != nil || m.dream.view != dreamConfirmRegenerate || len(f.generated) != 1 {
		t.Fatal("regenerate was not explicit-confirmation-only")
	}
	m, cmd = pressDream(t, m, '\r')
	_ = cmd()
	if len(f.generated) != 2 {
		t.Fatalf("confirmed regeneration calls = %d, want second provider call", len(f.generated))
	}
}

func TestDreamUnknownDecisionErrorOffersSameDecisionRetry(t *testing.T) {
	plan := client.DreamPlan{ID: "plan-transport", Target: client.DreamTargetProjectMemory}
	f := &fakeDream{receipt: client.DreamReceipt{ID: plan.ID, Disposition: client.DreamDecisionDismiss}, err: status.Error(codes.Unavailable, "lost transport\x1b[31m")}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, f, caps)
	m.dream = dreamState{view: dreamConfirmDismiss, plan: &plan}
	m, cmd := pressDream(t, m, '\r')
	if cmd == nil {
		t.Fatal("initial decision was not sent")
	}
	first := cmd().(client.DreamMsg)
	mm, _ := m.updateDreamMsg(first)
	m = mm.(Model)
	if m.dream.view != dreamReceipt || m.dream.plan.ID != plan.ID || m.dream.decision != client.DreamDecisionDismiss {
		t.Fatalf("decision error lost retry identity: %+v", m.dream)
	}
	out := stripANSIstr(renderDreamOverlay(m.deps.Theme, m.dream, m.caps, defaultHelpKeys(), 100, 20))
	for _, want := range []string{"first request may already have applied", "retry the SAME dismiss decision", "plan-transport", "authoritative receipt", "No opposite decision or automatic regeneration"} {
		if !strings.Contains(out, want) {
			t.Fatalf("unknown decision outcome missing %q: %q", want, out)
		}
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("unknown decision outcome was not sanitized: %q", out)
	}
	f.err = nil
	m, cmd = pressDream(t, m, 't')
	if cmd == nil || m.dream.view != dreamDeciding {
		t.Fatal("generic decision error did not offer explicit same-decision retry")
	}
	msg := cmd().(client.DreamMsg)
	if got := f.decided; len(got) != 2 || got[0] != [2]string{"plan-transport", client.DreamDecisionDismiss} || got[1] != got[0] {
		t.Fatalf("initial/retried decisions = %v", got)
	}
	if len(f.generated) != 0 {
		t.Fatalf("decision retry generated a new plan: %v", f.generated)
	}
	mm, _ = m.updateDreamMsg(msg)
	m = mm.(Model)
	if m.dream.err != nil || m.dream.receipt == nil || m.dream.receipt.ID != plan.ID || m.dream.receipt.Disposition != client.DreamDecisionDismiss {
		t.Fatalf("authoritative retry receipt = %+v", m.dream)
	}
}

func TestDreamReviewFramesEveryUntrustedLineAndRendersCompleteReason(t *testing.T) {
	forged := "a apply whole plan\noperation 99 — kind: trusted\nEnter apply   Esc back"
	reason := strings.Repeat("reason-", 60) + "COMPLETE-END"
	plan := &client.DreamPlan{Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{
		Kind:        "synthesized_replacement",
		Survivor:    client.DreamParticipant{Key: forged, Value: forged, Description: forged},
		Sources:     []client.DreamParticipant{{Key: forged, Value: forged, Description: forged}},
		Replacement: client.DreamReplacement{Value: forged, Description: forged}, Reason: reason,
	}}}
	lines := renderDreamPlan(plan, 200, true, "")
	for _, line := range lines {
		for _, trusted := range []string{"a apply whole plan", "operation 99 — kind: trusted", "Enter apply   Esc back"} {
			if line == trusted {
				t.Fatalf("untrusted line spoofed trusted UI: %q", line)
			}
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "│ survivor value:") || !strings.Contains(joined, "│ replacement value:") || !strings.Contains(joined, "COMPLETE-END") {
		t.Fatalf("review was not fully framed/rendered:\n%s", joined)
	}
}

func TestDreamRegenerateOriginAndTerminalDecisionConflict(t *testing.T) {
	plan := client.DreamPlan{ID: "plan-1", Target: client.DreamTargetProjectMemory}
	f := &fakeDream{plan: plan, err: status.Error(codes.AlreadyExists, "decided")}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, f, caps)
	m.dream = dreamState{view: dreamReceipt, plan: &plan, decision: client.DreamDecisionApply, receipt: &client.DreamReceipt{Failed: 1}}
	m, _ = pressDream(t, m, 'r')
	if m.dream.view != dreamConfirmRegenerate || m.dream.regenerateFrom != dreamReceipt {
		t.Fatalf("regeneration origin = %+v", m.dream)
	}
	mm, _, handled := m.onDreamKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !handled || mm.(Model).dream.view != dreamReceipt {
		t.Fatal("Esc from receipt regeneration returned to actionable review")
	}
	m = mm.(Model)
	m.dream.err = status.Error(codes.AlreadyExists, "decided")
	m.dream.receipt = nil
	out := strings.Join(renderDreamReceipt(m.dream), "\n")
	if !strings.Contains(out, "different terminal decision") || !strings.Contains(out, "fresh plan") || strings.Contains(out, "retry the SAME") {
		t.Fatalf("terminal conflict actions = %q", out)
	}
	m, cmd := pressDream(t, m, 't')
	if cmd != nil || m.dream.view != dreamReceipt || len(f.decided) != 0 {
		t.Fatal("terminal conflict offered a decision retry")
	}
	m, cmd = pressDream(t, m, 'r')
	if cmd != nil || m.dream.view != dreamConfirmRegenerate {
		t.Fatal("terminal conflict did not offer explicit regeneration confirmation")
	}
}

func TestDreamDecisionErrorActionsAreStateHonest(t *testing.T) {
	plan := client.DreamPlan{ID: "plan-state", Target: client.DreamTargetProjectMemory}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	tests := []struct {
		name      string
		code      codes.Code
		wantText  string
		wantRetry bool
		wantRegen bool
	}{
		{name: "pending same decision", code: codes.Aborted, wantText: "still in progress", wantRetry: true},
		{name: "opposite decision in progress", code: codes.FailedPrecondition, wantText: "conflicting decision is in progress"},
		{name: "plan not found", code: codes.NotFound, wantText: "expiry, restart, or routing", wantRegen: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDream{}
			m := dreamModel(t, f, caps)
			m.dream = dreamState{view: dreamReceipt, plan: &plan, decision: client.DreamDecisionApply, err: status.Error(tc.code, "private backend detail")}
			out := strings.Join(renderDreamReceipt(m.dream), "\n")
			if !strings.Contains(out, tc.wantText) || strings.Contains(out, "private backend detail") {
				t.Fatalf("rendered state = %q", out)
			}
			afterRetry, retryCmd := pressDream(t, m, 't')
			if (retryCmd != nil) != tc.wantRetry {
				t.Fatalf("retry command present = %t, want %t", retryCmd != nil, tc.wantRetry)
			}
			if retryCmd != nil {
				_ = retryCmd()
				if len(f.decided) != 1 || f.decided[0] != [2]string{plan.ID, client.DreamDecisionApply} {
					t.Fatalf("retry changed decision: %v", f.decided)
				}
			} else if afterRetry.dream.view != dreamReceipt || len(f.decided) != 0 {
				t.Fatalf("non-retryable state changed: %+v decisions=%v", afterRetry.dream, f.decided)
			}
			afterRegen, regenCmd := pressDream(t, m, 'r')
			if regenCmd != nil || (afterRegen.dream.view == dreamConfirmRegenerate) != tc.wantRegen {
				t.Fatalf("regenerate confirmation = %v, want %t", afterRegen.dream.view, tc.wantRegen)
			}
		})
	}
}

func TestDreamDismissAndSanitization(t *testing.T) {
	plan := client.DreamPlan{ID: "plan", SourceCount: 1, Operations: []client.DreamOperation{{Kind: "synth\x1b[31m", Survivor: client.DreamParticipant{Key: "bad\x1b]0;owned\a", Value: "\xffvalue"}}}}
	f := &fakeDream{plan: plan, receipt: client.DreamReceipt{Disposition: "dismissed"}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, f, caps)
	m.dream = dreamState{view: dreamReview, plan: &plan}
	out := stripANSIstr(renderDreamOverlay(m.deps.Theme, m.dream, m.caps, defaultHelpKeys(), 90, 30))
	if strings.ContainsAny(out, "\x1b\a") || !strings.Contains(out, `\xff`) {
		t.Fatalf("unsafe/invalid content was not quoted unambiguously: %q", out)
	}
	m, cmd := pressDream(t, m, 'x')
	if cmd != nil || m.dream.view != dreamConfirmDismiss {
		t.Fatal("dismiss did not require confirmation")
	}
	m, cmd = pressDream(t, m, '\r')
	_ = cmd()
	if f.decided[0] != [2]string{"plan", client.DreamDecisionDismiss} {
		t.Fatalf("dismiss payload = %v", f.decided)
	}
}

func TestDreamReaderRetainsFrameWhenRewrappingUntrustedRows(t *testing.T) {
	rows := dreamPhysicalRows([]string{"│ reason: " + strings.Repeat("padding ", 5) + "a apply whole plan"}, 20)
	if len(rows) < 2 {
		t.Fatal("precondition: value must rewrap")
	}
	for _, row := range rows {
		if !strings.HasPrefix(row, "│ ") {
			t.Fatalf("untrusted continuation lost its frame: %q", row)
		}
		if ansi.StringWidth(row) > 20 {
			t.Fatalf("framed row exceeds width: %q", row)
		}
	}
	receipt := dreamState{view: dreamReceipt, receipt: &client.DreamReceipt{Disposition: strings.Repeat("padding ", 5) + "a apply whole plan"}}
	for _, row := range dreamPhysicalRows(renderDreamReceipt(receipt), 20) {
		if strings.Contains(row, "a apply whole plan") && !strings.HasPrefix(row, "│ ") {
			t.Fatalf("receipt continuation impersonates an action: %q", row)
		}
	}
}

func TestDreamReaderLongIndicatorFitsCard(t *testing.T) {
	plan := &client.DreamPlan{Target: client.DreamTargetProjectMemory, Operations: make([]client.DreamOperation, 130)}
	st := dreamState{view: dreamReview, plan: plan, viewport: new(bounded.Viewport)}
	th := theme.New("aztec", theme.AztecPalette())
	_, _, rows, _, bodyHeight := dreamReaderLayout(th, st, client.Capabilities{}, defaultHelpKeys(), 30, 25)
	if bodyHeight < 1 || len(rows) < 1000 {
		t.Fatalf("precondition: reader height=%d rows=%d", bodyHeight, len(rows))
	}
	_ = renderDreamOverlay(th, st, client.Capabilities{}, defaultHelpKeys(), 30, 25)
	st.viewport.Move(bounded.End, len(rows))
	out := renderDreamOverlay(th, st, client.Capabilities{}, defaultHelpKeys(), 30, 25)
	for _, row := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(row); got > 30 {
			t.Fatalf("overflow indicator/card row exceeds 30 columns (%d): %q", got, row)
		}
	}
}

func TestDreamReaderUpdateViewAndResultLifecycle(t *testing.T) {
	plan := &client.DreamPlan{Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{
		Kind: "synthesis", Reason: strings.Repeat("long reason ", 100),
	}}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, &fakeDream{}, caps)
	m = applyAll(m, tea.WindowSizeMsg{Width: 42, Height: 28})
	m.dreamGen, m.dream.requestID = 3, 8
	m.dream.view, m.dream.viewport = dreamGenerating, new(bounded.Viewport)
	m.dream.viewport.SetGeometry(20, 2, 0, bounded.Clip)
	m.dream.viewport.Move(bounded.End, 100)
	staleOffset := m.dream.viewport.Offset()
	m = applyAll(m, client.DreamMsg{Generation: 3, RequestID: 7, Plan: plan})
	if m.dream.viewport.Offset() != staleOffset || m.dream.view != dreamGenerating {
		t.Fatal("stale plan response changed the reader")
	}
	m = applyAll(m, client.DreamMsg{Generation: 3, RequestID: 8, Plan: plan})
	if m.dream.view != dreamReview || m.dream.viewport.Offset() != 0 {
		t.Fatal("new plan did not reset reader to top")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown}) // no intervening View
	if m.dream.viewport.Offset() != 1 {
		t.Fatal("Model.Update did not route reader navigation through the copied model")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 38, Height: 27}, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.dream.viewport.Offset() <= 1 {
		t.Fatal("Model.Update lost page navigation after resize")
	}
	if got := len(strings.Split(m.renderBody(), "\n")); got > m.vp.Height() {
		t.Fatalf("reader body uses %d rows, offered %d", got, m.vp.Height())
	}
	if !strings.Contains(m.View().Content, "lines ") {
		t.Fatal("Model.View did not project the bounded reader")
	}
	m.dream.view, m.dream.requestID, m.dream.decision = dreamDeciding, 9, client.DreamDecisionApply
	scrolled := m.dream.viewport.Offset()
	m = applyAll(m, client.DreamMsg{Generation: 3, RequestID: 8, Err: status.Error(codes.Unavailable, "stale")})
	if m.dream.viewport.Offset() != scrolled || m.dream.view != dreamDeciding {
		t.Fatal("stale decision response changed the reader")
	}
	m = applyAll(m, client.DreamMsg{Generation: 3, RequestID: 9, Err: status.Error(codes.Unavailable, "retry")})
	if m.dream.view != dreamReceipt || m.dream.viewport.Offset() != 0 {
		t.Fatal("decision receipt did not reset reader to top")
	}
	out := m.renderBody()
	if !strings.Contains(stripANSIstr(out), "Decision result unknown") {
		t.Fatal("receipt did not render through the reader")
	}
	if !strings.Contains(stripANSIstr(out), "lines ") {
		t.Fatal("clipped receipt lost its overflow indicator")
	}
	if got := len(strings.Split(out, "\n")); got > m.vp.Height() {
		t.Fatalf("receipt body uses %d rows, offered %d", got, m.vp.Height())
	}
	for _, row := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(row); got > m.width {
			t.Fatalf("receipt row exceeds terminal width (%d > %d): %q", got, m.width, row)
		}
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.dream.viewport.Offset() != 1 {
		t.Fatal("receipt navigation did not advance")
	}
	if got := len(strings.Split(m.View().Content, "\n")); got > m.height {
		t.Fatalf("full frame uses %d rows, terminal height %d", got, m.height)
	}
}

func TestDreamReaderNavigationBeforeRenderAndAfterResize(t *testing.T) {
	plan := client.DreamPlan{Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{
		Kind: "synthesis", Reason: strings.Repeat("a long reason ", 60),
	}}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, &fakeDream{}, caps)
	m = applyAll(m, tea.WindowSizeMsg{Width: 48, Height: 36})
	m.dream = dreamState{view: dreamReview, plan: &plan, viewport: new(bounded.Viewport)}
	m, _ = pressDream(t, m, 'j') // no intervening View call
	if got := m.dream.viewport.Offset(); got != 1 {
		t.Fatalf("first navigation before rendering = %d, want 1", got)
	}

	m = applyAll(m, tea.WindowSizeMsg{Width: 36, Height: 32})
	_, _, rows, _, bodyHeight := dreamReaderLayout(m.deps.Theme, m.dream, m.caps, m.helpKeyMarkings(), m.width, m.vp.Height())
	if bodyHeight < 1 {
		t.Fatal("resize precondition: reader must fit")
	}
	mm, _, handled := m.onDreamKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !handled {
		t.Fatal("PageDown was not handled")
	}
	m = mm.(Model)
	if got, want := m.dream.viewport.Offset(), min(1+bodyHeight, max(0, len(rows)-bodyHeight)); got != want {
		t.Fatalf("PageDown after resize before render = %d, want %d", got, want)
	}
}

func TestDreamReaderBoundedViewportScenario(t *testing.T) {
	plan := client.DreamPlan{ID: "plan-reader", Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{
		Kind: "synthesis", Survivor: client.DreamParticipant{Key: "survivor", Value: strings.Repeat("wide value ", 20)},
		Sources:     []client.DreamParticipant{{Key: "source", Value: strings.Repeat("source value ", 20)}},
		Replacement: client.DreamReplacement{Value: strings.Repeat("replacement ", 20)}, Reason: strings.Repeat("reason ", 80),
	}}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, &fakeDream{}, caps)
	m = applyAll(m, tea.WindowSizeMsg{Width: 48, Height: 36})
	m.dream = dreamState{view: dreamReview, plan: &plan, viewport: new(bounded.Viewport)}

	_ = renderDreamOverlay(m.deps.Theme, m.dream, m.caps, defaultHelpKeys(), 48, m.vp.Height())
	if m.dream.viewport.Height() < 1 {
		t.Fatal("reader did not receive a usable viewport")
	}
	for range 1000 {
		m, _ = pressDream(t, m, 'j')
	}
	atEnd := m.dream.viewport.Offset()
	if atEnd == 0 {
		t.Fatal("long plan did not scroll")
	}
	m, _ = pressDream(t, m, 'k')
	if got := m.dream.viewport.Offset(); got != atEnd-1 {
		t.Fatalf("Up after Down past End offset = %d, want %d", got, atEnd-1)
	}
	m, _ = pressDream(t, m, 'a')
	if m.dream.view != dreamConfirmApply {
		t.Fatalf("review action was lost to reader navigation: %v", m.dream.view)
	}

	m.dream = dreamState{view: dreamReview, plan: &plan, viewport: new(bounded.Viewport)}
	for i, size := range [][2]int{{48, 20}, {28, 12}, {12, 5}, {60, 30}} {
		m.width = size[0]
		out := renderDreamOverlay(m.deps.Theme, m.dream, m.caps, defaultHelpKeys(), size[0], size[1])
		if i == 0 {
			m.dream.viewport.Move(bounded.End, len(dreamPhysicalRows(dreamReaderLines(m.dream, m.caps, m.width), cardTextWidth(m.width))))
		} else if m.dream.viewport.Valid() {
			total := len(dreamPhysicalRows(dreamReaderLines(m.dream, m.caps, m.width), cardTextWidth(m.width)))
			if got, maxOffset := m.dream.viewport.Offset(), max(0, total-m.dream.viewport.Height()); got > maxOffset {
				t.Fatalf("%dx%d resize offset = %d, want <= %d", size[0], size[1], got, maxOffset)
			}
		}
		for _, line := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(ansi.Strip(line)); got > size[0] {
				t.Fatalf("%dx%d row width = %d: %q", size[0], size[1], got, ansi.Strip(line))
			}
		}
		if got := len(strings.Split(out, "\n")); got > size[1] {
			t.Fatalf("%dx%d rendered %d rows", size[0], size[1], got)
		}
	}
	m.dream = dreamState{view: dreamReceipt, plan: &plan, receipt: &client.DreamReceipt{Failed: 1}, viewport: m.dream.viewport}
	m, _ = pressDream(t, m, 'r')
	if m.dream.view != dreamConfirmRegenerate {
		t.Fatalf("receipt action was lost to reader navigation: %v", m.dream.view)
	}
}
