package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
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
	bounded := stripANSIstr(renderDreamOverlay(m.deps.Theme, m.dream, m.caps, defaultHelpKeys(), 90, 18))
	if !strings.Contains(bounded, "lines 1–") {
		t.Fatalf("bounded review has no scroll indicator:\n%s", bounded)
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
