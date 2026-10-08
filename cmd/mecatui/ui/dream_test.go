package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
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
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseIdle
	m.deps.Dream = f
	m.caps.ManualDream = caps
	m = applyAll(m, tea.WindowSizeMsg{Width: 90, Height: 36})
	return m
}
func dreamSurface(t *testing.T, m Model) *dreamState {
	t.Helper()
	s, ok := m.modal.(*dreamState)
	if !ok {
		t.Fatalf("modal = %T, want Dream", m.modal)
	}
	return s
}
func dreamAt(t *testing.T, m Model, st dreamState) Model {
	t.Helper()
	st.deps = (&m).surfaceDeps()
	st.client = m.deps.Dream
	m.modal = &st
	return m
}
func pressDream(t *testing.T, m Model, pressed rune) (Model, tea.Cmd) {
	t.Helper()
	msg := tea.KeyPressMsg{Code: pressed, Text: string(pressed)}
	if pressed == '\r' {
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	mm, cmd := m.Update(msg)
	return mm.(Model), cmd
}
func dreamBody(t *testing.T, m Model, width, height int) string {
	t.Helper()
	body, _ := dreamSurface(t, m).Render(width, height)
	return stripANSIstr(body)
}
func TestDreamCapabilityGatingAndUnavailableReasons(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	if strings.Contains(stripANSIstr(helpBody(th, client.Capabilities{}, defaultHelpKeys())), "/dream") {
		t.Fatal("older server exposed /dream in help")
	}
	if _, ok := builtinByName(client.Capabilities{}, wiredCollaborators{Dream: true}, "dream"); ok {
		t.Fatal("older server exposed /dream")
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
		t.Fatal("open made RPC")
	}
	out := dreamBody(t, m, 100, 30)
	for _, want := range []string{"spends tokens", "does not enable or change scheduled consolidation", "project store disabled", "user model read-only", "generation is disabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	if !strings.Contains(out, "generation is disabled") || strings.Contains(out, "to confirm the spend") || dreamSurface(t, m).target != -1 {
		t.Fatal("no actionable target still offered Enter or selected a row")
	}
	for _, code := range []rune{tea.KeyDown, tea.KeyEnter} {
		m = applyAll(m, tea.KeyPressMsg{Code: code})
		if dreamSurface(t, m).target != -1 || dreamSurface(t, m).view != dreamTargets {
			t.Fatal("disabled targets became actionable")
		}
	}
}
func TestDreamGenerateSelectionConfirmAndCorrelation(t *testing.T) {
	f := &fakeDream{plan: client.DreamPlan{ID: "plan-1"}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}, UserModel: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, f, caps)
	mm, _ := m.openDream()
	m = mm.(Model)
	if len(f.generated) != 0 || m.prompt.Focused() {
		t.Fatal("opening made RPC or left prompt focused")
	}
	m, _ = pressDream(t, m, 'j')
	if dreamSurface(t, m).target != 1 {
		t.Fatal("selection did not move")
	}
	m, cmd := pressDream(t, m, '\r')
	if cmd == nil || len(f.generated) != 0 {
		t.Fatal("Enter should schedule one RPC")
	}
	msg := cmd().(dreamResultMsg)
	if len(f.generated) != 1 || f.generated[0] != client.DreamTargetUserModel {
		t.Fatalf("targets = %v", f.generated)
	}
	stale := msg
	stale.requestID++
	m = applyAll(m, stale)
	if dreamSurface(t, m).view != dreamGenerating {
		t.Fatal("wrong request accepted")
	}
	m = applyAll(m, msg)
	if dreamSurface(t, m).view != dreamReview || dreamSurface(t, m).plan.ID != "plan-1" {
		t.Fatal("plan not accepted")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc}, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("Esc did not back then close/refocus")
	}
	m = applyAll(m, msg)
	if m.modal != nil {
		t.Fatal("late result reopened modal")
	}
	mm, _ = m.openDream()
	m = mm.(Model)
	m = applyAll(m, msg)
	if dreamSurface(t, m).view != dreamTargets {
		t.Fatal("old owner mutated reopened modal")
	}
}
func TestDreamReviewActionsReceiptsAndExplicitRegenerate(t *testing.T) {
	plan := client.DreamPlan{ID: "plan-1", Target: client.DreamTargetProjectMemory, PlannedOperationCount: 2, SourceCount: 3, Operations: []client.DreamOperation{{Kind: "exact_duplicate", Survivor: client.DreamParticipant{Key: "keep"}, Sources: []client.DreamParticipant{{Key: "drop"}}, Replacement: client.DreamReplacement{Value: "new", Description: "new desc"}, Reason: strings.Repeat("reason ", 100)}}}
	f := &fakeDream{receipt: client.DreamReceipt{Disposition: "applied", Planned: 3, Applied: 1, Conflicted: 1, Failed: 1}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamAt(t, dreamModel(t, f, caps), dreamState{view: dreamReview, plan: &plan})
	out := dreamBody(t, m, 80, 60)
	for _, want := range []string{"planned operations: 2", "survivor key: \"keep\"", "source 1 key: \"drop\"", "replacement value: \"new\""} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(dreamBody(t, m, 80, 18), "lines 1–") {
		t.Fatal("no bounded indicator")
	}
	m, _ = pressDream(t, m, 'a')
	if dreamSurface(t, m).view != dreamConfirmApply {
		t.Fatal("apply not confirmed")
	}
	if !strings.Contains(dreamBody(t, m, 80, 30), "3 sources") {
		t.Fatal("confirmation missing count")
	}
	m, cmd := pressDream(t, m, '\r')
	msg := cmd().(dreamResultMsg)
	if len(f.decided) != 1 || f.decided[0] != [2]string{"plan-1", client.DreamDecisionApply} {
		t.Fatalf("decision = %v", f.decided)
	}
	m = applyAll(m, msg)
	receipt := strings.Join(renderDreamReceipt(*dreamSurface(t, m)), "\n")
	for _, want := range []string{"disposition: applied", "conflicted: 1", "failed: 1", "Memory changed", "Partial result"} {
		if !strings.Contains(receipt, want) {
			t.Errorf("missing %q", want)
		}
	}
	m, cmd = pressDream(t, m, 'r')
	if cmd != nil || dreamSurface(t, m).view != dreamConfirmRegenerate {
		t.Fatal("regenerate not confirmation-only")
	}
	_, cmd = pressDream(t, m, '\r')
	_ = cmd()
	if len(f.generated) != 1 {
		t.Fatal("confirmed regeneration did not call provider")
	}
}
func TestDreamPlanReaderTraversesEveryField(t *testing.T) {
	plan := &client.DreamPlan{
		ID:                    "plan-sentinels",
		Target:                client.DreamTargetUserModel,
		PlannedOperationCount: 2,
		SourceCount:           3,
		ExpiresAt:             time.Date(2026, time.October, 8, 12, 34, 56, 0, time.UTC),
		Operations: []client.DreamOperation{
			{Kind: "first-kind", ExactDuplicateEligible: true, Survivor: client.DreamParticipant{Key: "K11", Value: "V11", Description: "D11"}, Sources: []client.DreamParticipant{{Key: "K12", Value: "V12", Description: "D12"}, {Key: "K13", Value: "V13", Description: "D13"}}, Replacement: client.DreamReplacement{Value: "R11", Description: "Q11"}, Reason: "Z11"},
			{Kind: "second-kind", Survivor: client.DreamParticipant{Key: "K21", Value: "V21", Description: "D21"}, Sources: []client.DreamParticipant{{Key: "K22", Value: "V22", Description: "D22"}}, Replacement: client.DreamReplacement{Value: "R21", Description: "Q21"}, Reason: "Z21"},
		},
	}
	caps := &client.ManualDreamCapabilities{UserModel: client.DreamTargetCapability{Generate: true, Decide: true}}
	for _, size := range [][2]int{{90, 36}, {48, 26}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := dreamAt(t, dreamModel(t, &fakeDream{}, caps), dreamState{view: dreamReview, target: 1, plan: plan})
			m = applyAll(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			seen := ""
			for range 500 {
				seen += "\n" + ansi.Strip(m.View().Content)
				before := dreamSurface(t, m).viewport.Offset()
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
				if dreamSurface(t, m).viewport.Offset() == before {
					break
				}
			}
			for _, want := range []string{
				"target: user model", "expires: 2026-10-08 12:34:56 UTC", "planned operations: 2", "planned sources: 3",
				"operation 1 — kind: first-kind", "operation 2 — kind: second-kind",
				"exact-duplicate eligible: true", "exact-duplicate eligible: false",
				"K11", "V11", "D11", "K12", "V12", "D12", "K13", "V13", "D13", "R11", "Q11", "Z11",
				"K21", "V21", "D21", "K22", "V22", "D22", "R21", "Q21", "Z21",
				"Exact duplicates keep", "a apply", "dismiss",
			} {
				if !strings.Contains(seen, want) {
					t.Errorf("reader never reached %q", want)
				}
			}
		})
	}
}

func TestDreamPlanUsesPrimaryValuesAndStandardTargetSelection(t *testing.T) {
	for _, th := range []theme.Theme{theme.New("aztec", theme.AztecPalette()), theme.Solar()} {
		t.Run(th.Name, func(t *testing.T) {
			plan := &client.DreamPlan{PlannedOperationCount: 2, SourceCount: 3, Operations: []client.DreamOperation{{Survivor: client.DreamParticipant{Key: "sentinel-key"}, Replacement: client.DreamReplacement{Value: "sentinel-value", Description: "sentinel-description"}, Reason: "sentinel-reason"}}}
			lines := strings.Join(renderDreamPlan(th, plan, 80, true, false, ""), "\n")
			for _, want := range []string{th.Style("viewport").Render(` "sentinel-key"`), th.Style("viewport").Render(` "sentinel-value"`), th.Style("viewport").Render(` "sentinel-description"`), th.Style("viewport").Render(` "sentinel-reason"`)} {
				if !strings.Contains(lines, want) {
					t.Errorf("Dream value did not use viewport text style: %q", ansi.Strip(want))
				}
			}
			if strings.Contains(lines, th.Style("toolArgs").Render(" sentinel-value")) {
				t.Fatal("Dream value retained muted toolArgs styling")
			}

			s := &dreamState{view: dreamTargets, target: 0, deps: surfaceDeps{theme: th, caps: client.Capabilities{ManualDream: &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}, UserModel: client.DreamTargetCapability{Generate: true, Decide: true}}}, marks: defaultHelpKeys()}}
			out, _ := s.Render(64, 30)
			spinnerSample := th.Style("spinner").Render("x")
			spinnerPrefix := spinnerSample[:strings.Index(spinnerSample, "x")]
			mutedSample := th.Style("muted").Render("x")
			mutedPrefix := mutedSample[:strings.Index(mutedSample, "x")]
			if !strings.Contains(out, spinnerPrefix+"▶ project memory — available") {
				t.Fatalf("selected Dream target does not use the standard spinner selection style: %q", out)
			}
			if !strings.Contains(out, mutedPrefix+"  user model — available") {
				t.Fatal("unselected Dream target does not use the standard muted style")
			}

			wrapped := &dreamState{view: dreamTargets, target: 0, deps: surfaceDeps{theme: th, caps: client.Capabilities{ManualDream: &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, UnavailableReason: strings.Repeat("decision unavailable ", 8)}, UserModel: client.DreamTargetCapability{Generate: true, Decide: true}}}, marks: defaultHelpKeys()}}
			out, _ = wrapped.Render(48, 40)
			if count := strings.Count(ansi.Strip(out), "▶"); count != 1 {
				t.Fatalf("cursor marker appeared on %d wrapped rows", count)
			}
			if count := strings.Count(out, spinnerPrefix); count < 2 {
				t.Fatalf("selected styling did not cover wrapped target rows: %q", out)
			}
		})
	}
}
func TestDreamUnknownDecisionErrorOffersSameDecisionRetry(t *testing.T) {
	plan := client.DreamPlan{ID: "plan-transport", Target: client.DreamTargetProjectMemory}
	f := &fakeDream{receipt: client.DreamReceipt{ID: plan.ID, Disposition: client.DreamDecisionDismiss}, err: status.Error(codes.Unavailable, "lost transport\x1b[31m")}
	m := dreamAt(t, dreamModel(t, f, &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}), dreamState{view: dreamConfirmDismiss, plan: &plan})
	m, cmd := pressDream(t, m, '\r')
	first := cmd().(dreamResultMsg)
	m = applyAll(m, first)
	s := dreamSurface(t, m)
	if s.view != dreamReceipt || s.plan.ID != plan.ID || s.decision != client.DreamDecisionDismiss {
		t.Fatal("retry identity lost")
	}
	out := dreamBody(t, m, 100, 20)
	for _, want := range []string{"first request may already have applied", "retry the SAME dismiss decision", "plan-transport", "No opposite decision"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	f.err = nil
	m, cmd = pressDream(t, m, 't')
	if cmd == nil || dreamSurface(t, m).view != dreamDeciding {
		t.Fatal("retry unavailable")
	}
	second := cmd().(dreamResultMsg)
	if len(f.decided) != 2 || f.decided[0] != f.decided[1] {
		t.Fatalf("changed decision: %v", f.decided)
	}
	m = applyAll(m, first, second)
	if dreamSurface(t, m).view != dreamReceipt || dreamSurface(t, m).err != nil || dreamSurface(t, m).receipt.ID != plan.ID {
		t.Fatal("retry receipt not authoritative")
	}
}
func TestDreamDecisionErrorActionsAreStateHonest(t *testing.T) {
	plan := client.DreamPlan{ID: "plan-state", Target: client.DreamTargetProjectMemory}
	for _, tc := range []struct {
		name         string
		code         codes.Code
		retry, regen bool
	}{{"pending", codes.Aborted, true, false}, {"conflict", codes.FailedPrecondition, false, false}, {"gone", codes.NotFound, false, true}, {"terminal", codes.AlreadyExists, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDream{}
			m := dreamAt(t, dreamModel(t, f, &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}), dreamState{view: dreamReceipt, plan: &plan, decision: client.DreamDecisionApply, err: status.Error(tc.code, "private backend detail")})
			if strings.Contains(dreamBody(t, m, 100, 20), "private backend detail") {
				t.Fatal("leaked transport detail")
			}
			retry, cmd := pressDream(t, m, 't')
			if (cmd != nil) != tc.retry {
				t.Fatal("retry mismatch")
			}
			if cmd != nil {
				_ = cmd()
				if f.decided[0] != [2]string{plan.ID, client.DreamDecisionApply} {
					t.Fatal("wrong retry")
				}
			} else if dreamSurface(t, retry).view != dreamReceipt {
				t.Fatal("non-retryable changed")
			}
			regen, cmd := pressDream(t, m, 'r')
			if cmd != nil || (dreamSurface(t, regen).view == dreamConfirmRegenerate) != tc.regen {
				t.Fatal("regenerate mismatch")
			}
		})
	}
}
func TestDreamSanitizedDismissAndRegenerationBack(t *testing.T) {
	plan := client.DreamPlan{ID: "plan", Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{Kind: "synth\x1b[31m", Survivor: client.DreamParticipant{Key: "bad\x1b]0;owned\a", Value: "\xffvalue"}}}}
	f := &fakeDream{}
	m := dreamAt(t, dreamModel(t, f, &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}), dreamState{view: dreamReview, plan: &plan})
	out := dreamBody(t, m, 90, 30)
	if strings.ContainsAny(out, "\x1b\a") || !strings.Contains(out, `\xff`) {
		t.Fatalf("untrusted plan was not quoted: %q", out)
	}
	m, _ = pressDream(t, m, 'x')
	if dreamSurface(t, m).view != dreamConfirmDismiss {
		t.Fatal("dismiss did not require confirmation")
	}
	m, cmd := pressDream(t, m, '\r')
	_ = cmd()
	if f.decided[0] != [2]string{"plan", client.DreamDecisionDismiss} {
		t.Fatalf("dismiss sent %v", f.decided)
	}
	s := dreamSurface(t, m)
	s.view, s.receipt = dreamReceipt, &client.DreamReceipt{Failed: 1}
	m, _ = pressDream(t, m, 'r')
	if s.view != dreamConfirmRegenerate {
		t.Fatal("receipt regeneration not confirmed")
	}
	applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.view != dreamReceipt {
		t.Fatal("regeneration back returned to actionable review")
	}
}

func TestDreamPlanPresentationSeparatesBlocksAndStylesFields(t *testing.T) {
	plan := &client.DreamPlan{
		Target: client.DreamTargetProjectMemory, PlannedOperationCount: 1, SourceCount: 2,
		Operations: []client.DreamOperation{{
			Kind: "synthesis", ExactDuplicateEligible: true,
			Survivor: client.DreamParticipant{Key: "keep", Value: "survivor value", Description: "survivor description"},
			Sources: []client.DreamParticipant{
				{Key: "drop-1", Value: "source value", Description: "source description"},
				{Key: "drop-2", Value: "second value", Description: "second description"},
			},
			Replacement: client.DreamReplacement{Value: "replacement", Description: "replacement description"}, Reason: "because",
		}},
	}
	for _, th := range []theme.Theme{theme.New("aztec", theme.AztecPalette()), theme.Solar()} {
		t.Run(th.Name, func(t *testing.T) {
			rows := renderDreamPlan(th, plan, 120, true, true, "")
			styled := strings.Join(rows, "\n")
			if !strings.Contains(styled, th.Style("overlayTitle").Render("operation 1 — kind: synthesis")) {
				t.Fatal("operation title did not use the heading style")
			}
			for _, want := range []string{
				th.Style("muted").Render("target:") + th.Style("viewport").Render(" project memory"),
				"│ " + th.Style("muted").Render("survivor key:") + th.Style("viewport").Render(" \"keep\""),
			} {
				if !strings.Contains(styled, want) {
					t.Errorf("missing typed label/value styles %q", want)
				}
			}
			plain := strings.Split(ansi.Strip(styled), "\n")
			index := func(prefix string) int {
				for i, line := range plain {
					if strings.HasPrefix(line, prefix) {
						return i
					}
				}
				t.Fatalf("missing %q in %q", prefix, plain)
				return 0
			}
			for _, prefix := range []string{"Exact duplicates", "operation 1", "│ source 1 key", "│ source 2 key", "│ replacement value", "│ reason"} {
				i := index(prefix)
				if i == 0 || plain[i-1] != "" {
					t.Errorf("%q is not separated by a blank row: %q", prefix, plain)
				}
			}
			survivor := index("│ survivor key")
			if survivor == 0 || strings.HasPrefix(plain[survivor-1], "operation") || plain[survivor-1] == "" {
				t.Errorf("survivor should directly follow operation metadata: %q", plain)
			}
			if count := strings.Count(ansi.Strip(styled), "planned operations:"); count != 1 || !strings.Contains(plain[index("planned operations:")], "planned sources: 2") {
				t.Errorf("plan counts are not combined on one row: %q", plain)
			}
		})
	}
}

func TestDreamPlanFieldsRetainTrustMarkersAndGeometry(t *testing.T) {
	payload := "hostile: value\n" + strings.Repeat("e\u0301👩‍👩‍👧‍👦界 ", 8)
	plan := &client.DreamPlan{Operations: []client.DreamOperation{{
		Kind: "synthesis", Survivor: client.DreamParticipant{Key: payload, Value: payload, Description: payload},
		Sources:     []client.DreamParticipant{{Key: payload, Value: payload, Description: payload}},
		Replacement: client.DreamReplacement{Value: payload, Description: payload}, Reason: payload,
	}}}
	th := theme.New("aztec", theme.AztecPalette())
	for _, width := range []int{16, 24, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			rows := dreamPhysicalRows(renderDreamPlan(th, plan, width, true, true, ""), width)
			for _, row := range rows {
				if ansi.StringWidth(row) > width {
					t.Fatalf("row exceeds width %d: %q", width, row)
				}
				plain := ansi.Strip(row)
				if strings.Contains(plain, "hostile:") || strings.Contains(plain, "👩") || strings.Contains(plain, "e\u0301") {
					if !strings.HasPrefix(plain, "│ ") {
						t.Fatalf("untrusted value lost its frame: %q", plain)
					}
				}
			}
			plain := ansi.Strip(strings.Join(rows, "\n"))
			compact := strings.NewReplacer(" ", "", "\n", "", "│", "").Replace(plain)
			for _, want := range []string{"hostile:value", "👩\\u200d👩\\u200d👧\\u200d👦", "replacementvalue:", "reason:"} {
				if !strings.Contains(compact, want) {
					t.Errorf("missing complete field content %q", want)
				}
			}
		})
	}
}

func TestDreamReaderLongIndicatorFitsCard(t *testing.T) {
	plan := &client.DreamPlan{Operations: make([]client.DreamOperation, 130)}
	s := &dreamState{view: dreamReview, plan: plan, viewport: new(bounded.Viewport), deps: surfaceDeps{theme: theme.New("aztec", theme.AztecPalette()), marks: defaultHelpKeys()}}
	_, _ = s.Render(24, 23)
	if s.viewport.Height() < 1 {
		t.Fatal("no reader")
	}
	s.viewport.Move(bounded.End, len(dreamPhysicalRows(dreamReaderLines(*s, client.Capabilities{}, 24), 24)))
	body, _ := s.Render(24, 23)
	if !strings.Contains(body, "lines ") {
		t.Fatal("no indicator")
	}
	for _, row := range strings.Split(body, "\n") {
		if ansi.StringWidth(row) > 24 {
			t.Fatalf("row exceeds content offer: %q", row)
		}
	}
}

func TestDreamReaderRetainsFrameWhenRewrappingUntrustedRows(t *testing.T) {
	payload := strings.Repeat("e\u0301👩‍👩‍👧‍👦界 ", 8) + "\n" + strings.Repeat("a apply whole plan ", 6)
	plan := &client.DreamPlan{Operations: []client.DreamOperation{{
		Survivor:    client.DreamParticipant{Key: payload, Value: payload, Description: payload},
		Sources:     []client.DreamParticipant{{Key: payload, Value: payload, Description: payload}},
		Replacement: client.DreamReplacement{Value: payload, Description: payload}, Reason: payload,
	}}}
	for _, view := range []dreamView{dreamReview, dreamReceipt} {
		for _, width := range []int{16, 24, 40, 65} {
			t.Run(fmt.Sprintf("%d/%d", view, width), func(t *testing.T) {
				m := dreamModel(t, &fakeDream{}, &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}})
				s := &dreamState{view: view, plan: plan, receipt: &client.DreamReceipt{Disposition: payload}, deps: (&m).surfaceDeps()}
				body, _ := s.Render(width, 90)
				if s.compact {
					t.Fatalf("unexpected compact reader at width %d", width)
				}
				framed := 0
				for _, row := range strings.Split(body, "\n") {
					if got := ansi.StringWidth(row); got > width {
						t.Fatalf("row width %d exceeds offer %d: %q", got, width, row)
					}
					plain := ansi.Strip(row)
					if strings.HasPrefix(plain, "│ ") {
						framed++
					} else if strings.Contains(plain, "👩") || strings.Contains(plain, "e\u0301") || strings.Contains(plain, "apply whole plan") && !strings.Contains(plain, "a apply whole plan   x dismiss") {
						t.Fatalf("unframed untrusted continuation: %q", plain)
					}
				}
				if framed < 2 {
					t.Fatalf("no framed continuation: %q", ansi.Strip(body))
				}
			})
		}
	}
}
func TestDreamReaderUpdateViewAndResultLifecycle(t *testing.T) {
	plan := client.DreamPlan{ID: "p", Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{Reason: strings.Repeat("long reason ", 100)}}}
	f := &fakeDream{plan: plan}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, f, caps)
	m = applyAll(m, tea.WindowSizeMsg{Width: 60, Height: 36})
	mm, _ := m.openDream()
	m = mm.(Model)
	m, cmd := pressDream(t, m, '\r')
	result := cmd().(dreamResultMsg)
	m = applyAll(m, result)
	s := dreamSurface(t, m)
	if s.view != dreamReview || s.viewport.Offset() != 0 {
		t.Fatal("plan did not reset reader")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if s.viewport.Offset() != 1 {
		t.Fatal("navigation before View failed")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 38, Height: 27}, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if s.viewport.Offset() <= 1 {
		t.Fatal("page navigation failed")
	}
	if !strings.Contains(m.View().Content, "lines ") {
		t.Fatal("bounded reader not rendered")
	}
	m, _ = pressDream(t, m, 'a')
	m, cmd = pressDream(t, m, '\r')
	decision := cmd().(dreamResultMsg)
	wrong := decision
	wrong.requestID++
	m = applyAll(m, result, wrong)
	if s.view != dreamDeciding {
		t.Fatal("stale decision changed state")
	}
	applyAll(m, decision)
	if s.view != dreamReceipt || s.viewport.Offset() != 0 {
		t.Fatal("receipt not reset")
	}
}
func TestDreamSurfaceLifecycleIsolationAndWheel(t *testing.T) {
	f := &fakeDream{plan: client.DreamPlan{ID: "p"}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamModel(t, f, caps)
	mm, _ := m.openDream()
	m = mm.(Model)
	m, cmd := pressDream(t, m, '\r')
	old := cmd().(dreamResultMsg)
	// Replacing or resetting the owner cannot let its late command affect root or a new modal.
	m.closeModal()
	m.modal = &bodyOwnerTestSurface{}
	m = applyAll(m, old)
	if _, ok := m.modal.(*bodyOwnerTestSurface); !ok {
		t.Fatal("old Dream result disturbed replacement modal")
	}
	m.closeModal()
	mm, _ = m.openDream()
	m = mm.(Model)
	m = applyAll(m, old)
	if dreamSurface(t, m).view != dreamTargets {
		t.Fatal("replaced owner accepted result")
	}
	m = m.resetSessionDerived()
	m = applyAll(m, old)
	if m.modal != nil {
		t.Fatal("reset admitted late response")
	}
	for _, view := range []dreamView{dreamTargets, dreamGenerating, dreamReview, dreamConfirmApply, dreamConfirmDismiss, dreamConfirmRegenerate, dreamDeciding, dreamReceipt} {
		m = dreamAt(t, m, dreamState{view: view, plan: &client.DreamPlan{Target: client.DreamTargetProjectMemory}, viewport: new(bounded.Viewport)})
		before := m.vp.YOffset()
		mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m = mm.(Model)
		if m.vp.YOffset() != before {
			t.Fatalf("wheel leaked in %v", view)
		}
		m.closeModal()
	}
}
func TestDreamSurfaceViewThemeAndBackOwnership(t *testing.T) {
	plan := client.DreamPlan{ID: "p", Target: client.DreamTargetProjectMemory, SourceCount: 1}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamAt(t, dreamModel(t, &fakeDream{}, caps), dreamState{view: dreamReview, plan: &plan})
	if !strings.Contains(m.View().Content, "Dream — manual memory maintenance") {
		t.Fatal("Model.View did not show Dream in the modal")
	}
	m, _ = pressDream(t, m, 'a')
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if dreamSurface(t, m).view != dreamReview {
		t.Fatal("Esc did not back out of confirmation")
	}
	m = m.switchTheme(theme.Solar())
	_ = m.View()
	if dreamSurface(t, m).deps.theme.Name != m.deps.Theme.Name {
		t.Fatal("open surface retained prior theme")
	}
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Close": {"ctrl+f33"}})
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF33, Mod: tea.ModCtrl})
	if dreamSurface(t, m).view != dreamTargets {
		t.Fatal("Dream retained old keymap")
	}
}

func TestDreamReaderWheelMovesOnlyReader(t *testing.T) {
	plan := client.DreamPlan{Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{Reason: strings.Repeat("long reason ", 120)}}}
	m := dreamAt(t, dreamModel(t, &fakeDream{}, &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}), dreamState{view: dreamReview, plan: &plan})
	s := dreamSurface(t, m)
	_ = m.View()
	before := m.vp.YOffset()
	mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if s.viewport == nil || s.viewport.Offset() != 1 || m.vp.YOffset() != before {
		t.Fatal("wheel did not scroll only the reader")
	}
	s.view = dreamReceipt
	s.receipt = nil
	s.err = status.Error(codes.Unavailable, "lost response")
	s.plan.ID = strings.Repeat("p", 60)
	s.decision = client.DreamDecisionApply
	s.viewport.Reset()
	m = applyAll(m, tea.WindowSizeMsg{Width: 24, Height: 30})
	_ = m.View()
	mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if s.viewport.Offset() != 1 || m.vp.YOffset() != before {
		t.Fatalf("receipt wheel reader offset=%d height=%d width=%d compact=%t vp=%d before=%d", s.viewport.Offset(), s.viewport.Height(), s.width, s.compact, m.vp.YOffset(), before)
	}
}

func TestDreamAllStateContentOffers(t *testing.T) {
	plan := &client.DreamPlan{ID: "p", Target: client.DreamTargetProjectMemory, SourceCount: 3, Operations: []client.DreamOperation{{Reason: strings.Repeat("界 reason ", 24)}}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}, UserModel: client.DreamTargetCapability{UnavailableReason: strings.Repeat("界 unavailable ", 8)}}
	views := []dreamView{dreamTargets, dreamGenerating, dreamReview, dreamConfirmApply, dreamConfirmDismiss, dreamConfirmRegenerate, dreamDeciding, dreamReceipt}
	for _, view := range views {
		for _, size := range [][2]int{{0, 0}, {1, 1}, {2, 8}, {12, 5}, {24, 14}, {60, 34}, {128, 50}, {220, 50}} {
			t.Run(fmt.Sprintf("%d/%dx%d", view, size[0], size[1]), func(t *testing.T) {
				m := dreamModel(t, &fakeDream{}, caps)
				s := &dreamState{view: view, plan: plan, receipt: &client.DreamReceipt{Disposition: strings.Repeat("界 result ", 12), Conflicted: 1}, err: nil, deps: (&m).surfaceDeps()}
				if view == dreamTargets {
					s.err = errors.New(strings.Repeat("界 error ", 12))
				}
				body, _ := s.Render(size[0], size[1])
				if size[0] <= 0 || size[1] <= 0 {
					if body != "" || !s.compact {
						t.Fatalf("nonpositive offer produced %q", body)
					}
					return
				}
				lines := strings.Split(body, "\n")
				if len(lines) > size[1] {
					t.Fatalf("body height %d > %d", len(lines), size[1])
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("row width %d > %d: %q", ansi.StringWidth(line), size[0], line)
					}
				}
				if s.compact {
					if len(lines) != 1 || strings.Contains(ansi.Strip(body), "back") {
						t.Fatalf("compact not close-only: %q", body)
					}
				} else if !strings.Contains(ansi.Strip(body), "back/close") {
					t.Fatalf("footer displaced: %q", body)
				}
			})
		}
	}
}

func TestDreamCompactOnlyClosesAndParentGeometry(t *testing.T) {
	plan := &client.DreamPlan{ID: "p", Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{Reason: strings.Repeat("reason ", 100)}}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	for _, view := range []dreamView{dreamTargets, dreamGenerating, dreamReview, dreamConfirmApply, dreamConfirmDismiss, dreamConfirmRegenerate, dreamDeciding, dreamReceipt} {
		t.Run(fmt.Sprint(view), func(t *testing.T) {
			m := dreamAt(t, dreamModel(t, &fakeDream{}, caps), dreamState{view: view, plan: plan, receipt: &client.DreamReceipt{Failed: 1}})
			m = applyAll(m, tea.WindowSizeMsg{Width: 12, Height: 10})
			s := dreamSurface(t, m)
			_ = m.View()
			if !s.compact || s.modalFrame() {
				t.Fatal("tiny parent must show unframed compact")
			}
			for _, code := range []rune{'a', 'x', 'r', 't', tea.KeyEnter, tea.KeyDown, tea.KeyPgDown} {
				mm, cmd := m.Update(tea.KeyPressMsg{Code: code})
				m = mm.(Model)
				if cmd != nil || s.view != view || m.modal != s || s.viewport != nil {
					t.Fatalf("compact accepted %q: cmd=%v view=%v viewport=%v", code, cmd, s.view, s.viewport)
				}
			}
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
			if m.modal != nil {
				t.Fatal("compact Esc did not close final modal")
			}
		})
	}
	for _, view := range []dreamView{dreamTargets, dreamGenerating, dreamReview, dreamConfirmApply, dreamConfirmDismiss, dreamConfirmRegenerate, dreamDeciding, dreamReceipt} {
		for _, size := range [][2]int{{1, 14}, {12, 12}, {60, 40}, {220, 55}} {
			m := dreamAt(t, dreamModel(t, &fakeDream{}, caps), dreamState{view: view, plan: plan, receipt: &client.DreamReceipt{Failed: 1}})
			m = applyAll(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			_ = m.View()
			bounds := m.metrics.outerBounds
			if bounds.x1 > size[0] || bounds.x1-bounds.x0 > 128 || bounds.y1 > convTopRow(m)+m.vp.Height() {
				t.Fatalf("view %d %v: card %+v outside parent offer height %d", view, size, bounds, m.vp.Height())
			}
			body := m.renderBody()
			if len(strings.Split(body, "\n")) > m.vp.Height() {
				t.Fatalf("view %d %v: body exceeds conversation height", view, size)
			}
			for _, row := range strings.Split(body, "\n") {
				if ansi.StringWidth(row) > size[0] {
					t.Fatalf("view %d %v: body row too wide: %q", view, size, row)
				}
			}
		}
	}
}

func TestDreamCompactUpdateBeforeView(t *testing.T) {
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	for _, size := range [][2]int{{0, 0}, {1, 1}, {12, 10}} {
		for _, view := range []dreamView{dreamTargets, dreamReview, dreamReceipt, dreamConfirmApply} {
			t.Run(fmt.Sprintf("%v/%v", size, view), func(t *testing.T) {
				f := &fakeDream{}
				m := dreamAt(t, dreamModel(t, f, caps), dreamState{view: view, plan: &client.DreamPlan{ID: "p"}, receipt: &client.DreamReceipt{Failed: 1}})
				m = applyAll(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				s := dreamSurface(t, m)
				for _, code := range []rune{'a', 'x', 'r', 't', tea.KeyEnter, tea.KeyDown, tea.KeyPgDown} {
					mm, cmd := m.Update(tea.KeyPressMsg{Code: code})
					m = mm.(Model)
					if cmd != nil || s.view != view || s.viewport != nil || s.list != nil || !s.compact || len(f.generated) != 0 || len(f.decided) != 0 {
						t.Fatalf("hidden action %q accepted before View: view=%v compact=%t cmd=%v", code, s.view, s.compact, cmd)
					}
				}
			})
		}
	}
}

func TestDreamReaderResizeClampsWithoutFollowingTail(t *testing.T) {
	plan := &client.DreamPlan{Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{Reason: strings.Repeat("long reason ", 80)}}}
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	m := dreamAt(t, dreamModel(t, &fakeDream{}, caps), dreamState{view: dreamReview, plan: plan, viewport: new(bounded.Viewport)})
	m = applyAll(m, tea.WindowSizeMsg{Width: 60, Height: 36})
	s := dreamSurface(t, m)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	position := s.viewport.Offset()
	if position == 0 {
		t.Fatal("reader did not move before View")
	}
	m, _ = pressDream(t, m, 'a')
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.view != dreamReview || s.viewport.Offset() != position {
		t.Fatal("confirmation back lost physical reader position")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 44})
	_ = m.View() // resize does not fan out geometry; Render clamps the reader
	_, _, rows, _, readerHeight := dreamReaderLayout(s.deps.theme, *s, s.deps.caps, s.deps.marks, s.width, s.height)
	if want := min(position, max(0, len(rows)-readerHeight)); s.viewport.Offset() != want {
		t.Fatalf("resize offset = %d, want clamped %d (previous %d)", s.viewport.Offset(), want, position)
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	_ = m.View()
	if !s.compact || s.viewport.Offset() != 0 {
		t.Fatal("compact resize retained inaccessible scroll position")
	}
	_ = m.View()
}

func TestDreamReaderNavigationBeforeRenderAndAfterResize(t *testing.T) {
	plan := client.DreamPlan{Operations: []client.DreamOperation{{Reason: strings.Repeat("a long reason ", 60)}}}
	m := dreamModel(t, &fakeDream{}, &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}})
	m = dreamAt(t, m, dreamState{view: dreamReview, plan: &plan, viewport: new(bounded.Viewport)})
	m, _ = pressDream(t, m, 'j')
	s := dreamSurface(t, m)
	if s.viewport.Offset() != 1 {
		t.Fatalf("first navigation = %d", s.viewport.Offset())
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 36, Height: 32})
	applyAll(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if s.viewport.Offset() <= 1 {
		t.Fatal("resize lost page navigation")
	}
	for _, size := range [][2]int{{48, 20}, {28, 12}, {12, 5}, {60, 30}} {
		body, _ := s.Render(size[0], size[1])
		if len(strings.Split(body, "\n")) > size[1] {
			t.Fatalf("%v reader too tall", size)
		}
		for _, row := range strings.Split(body, "\n") {
			if ansi.StringWidth(row) > size[0] {
				t.Fatalf("%v reader too wide: %q", size, row)
			}
		}
	}
}

func TestDreamTargetPointerAndBoundedWindow(t *testing.T) {
	f := &fakeDream{}
	caps := &client.ManualDreamCapabilities{
		ProjectMemory: client.DreamTargetCapability{Generate: true},
		UserModel:     client.DreamTargetCapability{Generate: true},
	}
	m := dreamModel(t, f, caps)
	m.deps.NoAltScreen = false
	mm, _ := m.openDream()
	m = mm.(Model)
	_ = m.View()
	s := dreamSurface(t, m)
	if len(m.hits.frame) < 2 {
		t.Fatal("both targets need row hits")
	}
	first := m.hits.frame[0]
	second := m.hits.frame[len(m.hits.frame)-1]
	if s.hitTargets[second.id] != 1 {
		t.Fatal("last row is not user model")
	}
	for _, point := range [][2]int{{0, m.metrics.contentOrigin.y}, {m.width - 1, m.metrics.contentOrigin.y + 1}} {
		mm, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: point[0], Y: point[1]})
		m = mm.(Model)
		if cmd != nil || s.target != 0 || len(f.generated) != 0 {
			t.Fatalf("miss %v affected Dream", point)
		}
	}
	x, y := m.metrics.localToGlobal(second.rect.x0, second.rect.y0)
	mm, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = mm.(Model)
	if cmd != nil || s.target != 1 || len(f.generated) != 0 {
		t.Fatal("click should select user model without generation")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 0})
	s.target = 0
	mm, cmd = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = mm.(Model)
	if cmd != nil || s.target != 0 || len(f.generated) != 0 {
		t.Fatal("old screen point selected target after zero resize without View")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 90, Height: 36})
	s.target = 1
	_ = m.View()
	caps.UserModel.Generate = false
	m.caps.ManualDream.UserModel.Generate = false
	_ = m.View()
	if s.target != 1 { // a disabled row cannot activate even if capabilities change mid-frame
		// No selection side effects from render.
		t.Fatal("render changed selection")
	}
	s.target = 0 // replay must not select the formerly enabled user row
	mm, cmd = m.Update(surfaceHitMsg{ID: second.id})
	m = mm.(Model)
	if cmd != nil || s.target != 0 || len(f.generated) != 0 {
		t.Fatal("stale hit selected disabled target or activated RPC")
	}
	s.target = 0
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if s.target != 0 {
		t.Fatal("navigation did not skip disabled user model")
	}
	_ = first
	m = applyAll(m, tea.WindowSizeMsg{Width: 48, Height: 26})
	_ = m.View()
	if s.compact || s.list == nil {
		beforeRows, afterRows := s.targetChrome(s.width)
		t.Fatalf("short picker should keep a bounded list (offer %dx%d chrome %d+%d)", s.width, s.height, len(beforeRows), len(afterRows))
	}
	before := m.vp.YOffset()
	selected := s.target
	start := s.list.Offset()
	for range 12 {
		mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m = mm.(Model)
	}
	if s.target != selected || m.vp.YOffset() != before || s.list.Offset() <= start {
		t.Fatalf("wheel did not scroll list independently: offset %d→%d selected %d conversation %d→%d", start, s.list.Offset(), s.target, before, m.vp.YOffset())
	}
	_ = m.View()
	for _, hit := range m.hits.frame {
		if s.hitTargets[hit.id] != 0 {
			t.Fatal("disabled or indicator row got a hit")
		}
	}
	s.view = dreamReview
	s.target = 1 // a stale hit must not reselect project memory in another view
	mm, cmd = m.Update(surfaceHitMsg{ID: first.id})
	m = mm.(Model)
	if cmd != nil || s.target != 1 || len(f.generated) != 0 {
		t.Fatal("non-target state accepted stale click")
	}
	m.closeModal()
	m = dreamAt(t, m, dreamState{view: dreamTargets, target: 1})
	_ = m.View()
	mm, cmd = m.Update(surfaceHitMsg{ID: first.id})
	m = mm.(Model)
	if cmd != nil || dreamSurface(t, m).target != 1 || len(f.generated) != 0 {
		t.Fatal("replacement modal accepted old frame hit")
	}
}

func TestDreamDisabledTargetClickAndWheelIsolation(t *testing.T) {
	f := &fakeDream{}
	caps := &client.ManualDreamCapabilities{
		ProjectMemory: client.DreamTargetCapability{Generate: true},
		UserModel:     client.DreamTargetCapability{UnavailableReason: strings.Repeat("disabled reason ", 10)},
	}
	m := dreamModel(t, f, caps)
	m.deps.NoAltScreen = false
	mm, _ := m.openDream()
	m = mm.(Model)
	_ = m.View()
	s := dreamSurface(t, m)
	if len(m.hits.frame) != 1 || s.target != 0 {
		t.Fatal("disabled target has a hit or was selected")
	}
	body := ansi.Strip(m.View().Content)
	if !strings.Contains(body, "disabled reason") {
		t.Fatal("disabled reason not visible")
	}
	// A narrow, tall offer wraps the disabled row; its continuations must not be clickable.
	m = applyAll(m, tea.WindowSizeMsg{Width: 48, Height: 40})
	_ = m.View()
	body, _ = s.Render(s.width, s.height)
	clicked := false
	for y, line := range strings.Split(body, "\n") {
		if !strings.Contains(ansi.Strip(line), "user model") {
			continue
		}
		x, globalY := m.metrics.localToGlobal(0, y)
		mm, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: globalY})
		m = mm.(Model)
		if cmd != nil || s.target != 0 || len(f.generated) != 0 {
			t.Fatal("disabled row click activated")
		}
		clicked = true
	}
	if !clicked {
		t.Fatalf("disabled row not rendered in narrow picker: %q (offer %dx%d)", ansi.Strip(body), s.width, s.height)
	}
	for range 4 {
		mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m = mm.(Model)
	}
	_ = m.View()
	body, _ = s.Render(s.width, s.height)
	continuation := false
	for y, line := range strings.Split(body, "\n") {
		if !strings.Contains(ansi.Strip(line), "disabled reason") {
			continue
		}
		x, globalY := m.metrics.localToGlobal(0, y)
		mm, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: globalY})
		m = mm.(Model)
		if cmd != nil || s.target != 0 || len(f.generated) != 0 {
			t.Fatal("wrapped disabled continuation activated")
		}
		continuation = true
	}
	if !continuation {
		t.Fatal("wrapped disabled reason was not reachable by wheel")
	}
	for _, view := range []dreamView{dreamTargets, dreamGenerating, dreamReview, dreamConfirmApply, dreamConfirmDismiss, dreamConfirmRegenerate, dreamDeciding, dreamReceipt} {
		s.view = view
		s.plan = &client.DreamPlan{Target: client.DreamTargetProjectMemory, Operations: []client.DreamOperation{{Reason: strings.Repeat("long reason ", 100)}}}
		s.receipt = &client.DreamReceipt{Conflicted: 1, Disposition: strings.Repeat("long disposition ", 60)}
		_ = m.View()
		before := m.vp.YOffset()
		for range 100 {
			mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			m = mm.(Model)
		}
		for range 100 {
			mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
			m = mm.(Model)
		}
		if m.vp.YOffset() != before || len(f.generated) != 0 {
			t.Fatalf("wheel leaked or made RPC in view %d", view)
		}
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 12, Height: 10})
	_ = m.View()
	before := m.vp.YOffset()
	mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if !s.compact || m.vp.YOffset() != before {
		t.Fatal("compact wheel leaked")
	}
}

func TestDreamFailedGenerateReturnsToTargets(t *testing.T) {
	const canary = "Bearer secret https://example.invalid/?token=secret"
	for _, tc := range []struct {
		name, reason, want string
		code               codes.Code
	}{
		{"server", "dream_generate_failed", "check server diagnostics", codes.Internal},
		{"deadline", "dream_deadline", "timed out", codes.DeadlineExceeded},
		{"capacity", "dream_capacity", "Dismiss an existing plan", codes.ResourceExhausted},
		{"unavailable", "dream_unavailable", "Check its configuration", codes.Unimplemented},
		{"transport", "", "Check the connection", codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := status.New(tc.code, canary)
			if tc.reason != "" {
				var err error
				st, err = st.WithDetails(&errdetails.ErrorInfo{Domain: "mecatl.stacklok.com", Reason: tc.reason})
				if err != nil {
					t.Fatal(err)
				}
			}
			f := &fakeDream{err: st.Err()}
			caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true}}
			m := dreamModel(t, f, caps)
			mm, _ := m.openDream()
			m = mm.(Model)
			m, cmd := pressDream(t, m, '\r')
			m = applyAll(m, cmd())
			if dreamSurface(t, m).view != dreamTargets {
				t.Fatal("failed generation did not return to targets")
			}
			out := dreamBody(t, m, 80, 30)
			if !strings.Contains(out, tc.want) || strings.Contains(out, canary) || strings.Contains(out, "token=secret") {
				t.Fatalf("unsafe or missing generation message: %q", out)
			}
			_, cmd = pressDream(t, m, '\r')
			if cmd == nil {
				t.Fatal("failed generation disabled retry")
			}
		})
	}
}

func TestDreamReceiptAffordancesFollowRecovery(t *testing.T) {
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: true}}
	for _, tc := range []struct {
		name         string
		err          error
		receipt      *client.DreamReceipt
		retry, regen bool
	}{
		{"success", nil, &client.DreamReceipt{Disposition: "applied"}, false, false},
		{"partial", nil, &client.DreamReceipt{Failed: 1}, false, true},
		{"pending", status.Error(codes.Aborted, "pending"), nil, true, false},
		{"unknown", status.Error(codes.Unavailable, "lost"), nil, true, false},
		{"conflict", status.Error(codes.FailedPrecondition, "conflict"), nil, false, false},
		{"gone", status.Error(codes.NotFound, "gone"), nil, false, true},
		{"terminal", status.Error(codes.AlreadyExists, "terminal"), nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := dreamModel(t, &fakeDream{}, caps)
			m = dreamAt(t, m, dreamState{view: dreamReceipt, plan: &client.DreamPlan{Target: client.DreamTargetProjectMemory}, decision: client.DreamDecisionApply, err: tc.err, receipt: tc.receipt})
			out := ansi.Strip(m.View().Content)
			if strings.Contains(out, "t retry") != tc.retry || strings.Contains(out, "r regenerate") != tc.regen {
				t.Fatalf("receipt recovery mismatch: %q", out)
			}
		})
	}
}

func TestDreamLiveCapabilityHintsAndKeymap(t *testing.T) {
	caps := &client.ManualDreamCapabilities{ProjectMemory: client.DreamTargetCapability{Generate: true, Decide: false, UnavailableReason: "read only"}}
	m := dreamModel(t, &fakeDream{}, caps)
	mm, _ := m.openDream()
	m = mm.(Model)
	_ = m.View()
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Choose": {"ctrl+f33"}, "Close": {"ctrl+f34"}})
	m = m.switchTheme(theme.Solar())
	out := m.View().Content
	if !strings.Contains(out, "read only") || !strings.Contains(out, m.helpKeyMarkings().choose) || dreamSurface(t, m).deps.theme.Name != m.deps.Theme.Name {
		t.Fatal("live theme/keymap did not reach target hints")
	}
	plan := &client.DreamPlan{Target: client.DreamTargetProjectMemory}
	s := dreamSurface(t, m)
	s.view, s.plan = dreamReview, plan
	out = m.View().Content
	if strings.Contains(out, "a apply") || strings.Contains(out, "x dismiss") || !strings.Contains(out, "r regenerate") {
		t.Fatal("review actions disagree with capabilities")
	}
	s.view, s.err, s.decision = dreamReceipt, status.Error(codes.FailedPrecondition, "conflict"), client.DreamDecisionApply
	out = m.View().Content
	if strings.Contains(out, "t retry") || strings.Contains(out, "r regenerate") {
		t.Fatal("conflicting receipt advertised recovery")
	}
	s.err = status.Error(codes.Unavailable, "unknown")
	out = m.View().Content
	if !strings.Contains(out, "t retry") || strings.Contains(out, "r regenerate") {
		t.Fatal("unknown receipt recovery mismatch")
	}
}
