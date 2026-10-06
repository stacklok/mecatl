package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

const benignSummary = "Guardrail check passed"

func benignReview(id, disposition string) *client.GuardrailReview {
	return &client.GuardrailReview{ReviewID: id, Job: "inbound", Inspection: "complete", Assessment: "acceptable", Disposition: disposition}
}

func TestQuietBenignGuardrailNotices_Scenario1_ExactBenignMatrix(t *testing.T) {
	for _, job := range []string{"action", "inbound"} {
		for _, disposition := range []string{"execute", "release_result"} {
			review := benignReview("review-"+job+"-"+disposition, disposition)
			review.Job = job
			if !routineGuardrail(review) {
				t.Errorf("job %q disposition %q was not benign", job, disposition)
			}
		}
	}
}

func TestQuietBenignGuardrailNotices_Scenario1_FailsUnknownVisible(t *testing.T) {
	if routineGuardrail(nil) {
		t.Fatal("nil review unexpectedly benign")
	}
	cases := []struct {
		name   string
		mutate func(*client.GuardrailReview)
	}{
		{"missing review ID", func(r *client.GuardrailReview) { r.ReviewID = "" }},
		{"missing job", func(r *client.GuardrailReview) { r.Job = "" }},
		{"unknown job", func(r *client.GuardrailReview) { r.Job = "unknown" }},
		{"unknown inspection", func(r *client.GuardrailReview) { r.Inspection = "unknown" }},
		{"operational failure", func(r *client.GuardrailReview) { r.Inspection = "operational_failure" }},
		{"unknown assessment", func(r *client.GuardrailReview) { r.Assessment = "unknown" }},
		{"unresolved", func(r *client.GuardrailReview) { r.Assessment = "unresolved" }},
		{"prohibited", func(r *client.GuardrailReview) { r.Assessment = "prohibited" }},
		{"ask action", func(r *client.GuardrailReview) { r.Disposition = "ask_action" }},
		{"withhold result", func(r *client.GuardrailReview) { r.Disposition = "withhold_result" }},
		{"deny", func(r *client.GuardrailReview) { r.Disposition = "deny" }},
		{"advisory", func(r *client.GuardrailReview) { r.Disposition = "pass_advisory" }},
		{"warning", func(r *client.GuardrailReview) { r.Disposition = "continue_warning" }},
		{"future disposition", func(r *client.GuardrailReview) { r.Disposition = "future_value" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every failure is a one-field mutation of an otherwise valid review with
			// a non-empty identity and a recognized job.
			review := benignReview("valid-review", "execute")
			tc.mutate(review)
			if routineGuardrail(review) {
				t.Fatalf("unexpectedly benign: %+v", review)
			}
			c := conversation{}
			c.addGuardrailHook(client.HookMsg{Tool: "Read", Guardrail: review}, false)
			if c.scrollback.Len() != 1 || benignGuardrailAt(&c, 0) {
				t.Fatalf("attention-worthy review was not retained visible: %+v", c.testBlocks())
			}
		})
	}
}

func TestQuietBenignGuardrailNotices_Scenario1_DoesNotParseProse(t *testing.T) {
	cases := []struct {
		msg    client.HookMsg
		detail client.GuardrailReviewDetail
	}{
		{
			msg:    client.HookMsg{Text: "DENY operational failure prohibited", Tool: "Shell", Guardrail: benignReview("review-a", "execute")},
			detail: client.GuardrailReviewDetail{Concern: "unsafe blocked", SourceDisplay: "unknown checker", NextAction: "deny"},
		},
		{
			msg:    client.HookMsg{Text: "everything is fine", Tool: "FutureTool", Guardrail: benignReview("review-b", "release_result")},
			detail: client.GuardrailReviewDetail{Concern: "acceptable", SourceDisplay: "release_result", NextAction: "execute"},
		},
	}
	for _, tc := range cases {
		tc.msg.Guardrail.ReasonCode = tc.detail.Concern
		tc.msg.Guardrail.RuleID = tc.detail.NextAction
		tc.msg.Guardrail.RuleOrigin = tc.detail.SourceDisplay
		tc.msg.Guardrail.CheckerProviderID = tc.msg.Text
		tc.msg.Guardrail.CheckerModelID = tc.msg.Tool
		tc.msg.Guardrail.ConcernRefs = []string{tc.detail.Concern}
		tc.msg.Guardrail.SourceRefs = []string{tc.detail.SourceDisplay}
		if !routineGuardrail(tc.msg.Guardrail) {
			t.Fatalf("prose changed structured classification: msg=%+v detail=%+v", tc.msg, tc.detail)
		}
	}
}

func quietTestModel(t *testing.T, guardrails client.GuardrailClient, show bool) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Guardrails: guardrails, ShowBenignHookNotices: show})
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 30})
	m.phase = phaseRunning
	m.sessionID = "session"
	return m
}

func frameText(m *Model) string {
	return stripANSIstr(strings.Join(m.rend.renderConversationLines(&m.conv.scrollback, m.expandConversation), "\n"))
}

func benignGuardrailAt(c *conversation, index int) bool {
	p, ok := c.scrollback.SnapshotAt(index).Payload.(scrollback.NoticeCardSnapshot)
	return ok && p.BenignGuardrail
}

type quietGuardrailClient struct {
	details   map[string]client.GuardrailReviewDetail
	calls     int
	sessions  []string
	reviewIDs []string
}

func (*quietGuardrailClient) ListGuardrailCoverage(context.Context, string) (client.GuardrailCoverage, error) {
	return client.GuardrailCoverage{}, nil
}

func (f *quietGuardrailClient) GetGuardrailReviewDetail(_ context.Context, sessionID, reviewID string) (client.GuardrailReviewDetail, error) {
	f.calls++
	f.sessions = append(f.sessions, sessionID)
	f.reviewIDs = append(f.reviewIDs, reviewID)
	if detail, ok := f.details[reviewID]; ok {
		return detail, nil
	}
	return client.GuardrailReviewDetail{ReviewID: reviewID}, nil
}

func runGuardrailDetailCommand(t *testing.T, cmd tea.Cmd) client.GuardrailReviewDetailMsg {
	t.Helper()
	var found []client.GuardrailReviewDetailMsg
	var run func(tea.Cmd)
	run = func(next tea.Cmd) {
		if next == nil {
			return
		}
		switch msg := next().(type) {
		case tea.BatchMsg:
			for _, child := range msg {
				run(child)
			}
		case client.GuardrailReviewDetailMsg:
			found = append(found, msg)
		}
	}
	run(cmd)
	if len(found) != 1 {
		t.Fatalf("guardrail detail command results = %d, want 1", len(found))
	}
	return found[0]
}

// applyBenignHook applies a live hook and its correlated detail response.
func applyBenignHook(t *testing.T, m Model, msg client.HookMsg) Model {
	t.Helper()
	model, cmd := m.applyHookMsg(msg)
	return applyAll(model.(Model), runGuardrailDetailCommand(t, cmd))
}

func TestQuietBenignGuardrailNotices_Scenario2_DefaultLiveVisibility(t *testing.T) {
	guardrails := &quietGuardrailClient{details: map[string]client.GuardrailReviewDetail{
		"benign": {ReviewID: "benign", Concern: "benign detail"},
	}}
	m := quietTestModel(t, guardrails, false)
	model, cmd := m.applyHookMsg(client.HookMsg{Text: "benign hook prose", Phase: "PostToolUse", Tool: "Read", Guardrail: benignReview("benign", "execute")})
	m = model.(Model)
	detail := runGuardrailDetailCommand(t, cmd)
	if detail.SessionID != "session" || detail.ReviewID != "benign" || detail.RequestID == 0 {
		t.Fatalf("detail correlation = %+v", detail)
	}
	m = applyAll(m, detail)
	m = applyAll(m,
		client.HookMsg{Text: "attention hook prose", Tool: "Read", Guardrail: &client.GuardrailReview{ReviewID: "attention", Job: "inbound", Inspection: "complete", Assessment: "unresolved", Disposition: "pass_advisory"}},
		client.ResultMsg{Stop: stopError, Error: "visible run error"},
	)
	if m.conv.scrollback.Len() != 3 || !benignGuardrailAt(&m.conv, 0) || benignGuardrailAt(&m.conv, 1) {
		t.Fatalf("retained cards = %+v", m.conv.testBlocks())
	}
	if p := m.conv.scrollback.SnapshotAt(0).Payload.(scrollback.NoticeCardSnapshot); !strings.Contains(p.Text, "benign detail") {
		t.Fatalf("correlated live detail was not retained: %q", p.Text)
	}
	got := frameText(&m)
	for _, hidden := range []string{benignSummary, "benign detail"} {
		if strings.Contains(got, hidden) {
			t.Errorf("default frame exposed %q: %q", hidden, got)
		}
	}
	for _, visible := range []string{"Work continued with a warning", "visible run error"} {
		if !strings.Contains(got, visible) {
			t.Errorf("default frame omitted %q: %q", visible, got)
		}
	}

	m = applyAll(m, client.PermissionAskMsg{AskID: "ask", Tool: "Write", Reason: "visible approval reason"})
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "visible approval reason") {
		t.Fatalf("approval was hidden with benign notices: %q", got)
	}
}

func TestQuietBenignGuardrailNotices_Scenario2_ExpandLiveAndReplay(t *testing.T) {
	guardrails := &quietGuardrailClient{details: map[string]client.GuardrailReviewDetail{
		"live": {ReviewID: "live", Concern: "live correlated detail"},
	}}
	m := quietTestModel(t, guardrails, false)
	m = applyBenignHook(t, m, client.HookMsg{Text: "live benign", Tool: "Read", Guardrail: benignReview("live", "execute")})
	if guardrails.calls != 1 || guardrails.sessions[0] != "session" || guardrails.reviewIDs[0] != "live" {
		t.Fatalf("detail calls=%d sessions=%v reviews=%v", guardrails.calls, guardrails.sessions, guardrails.reviewIDs)
	}
	if strings.Contains(frameText(&m), benignSummary) || strings.Contains(frameText(&m), "live correlated detail") {
		t.Fatal("collapsed live benign evidence is visible")
	}
	beforeBlocks := m.conv.scrollback.Len()
	m = applyAll(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if m.expandConversation || toolcallsForTest(t, m) == nil || strings.Contains(frameText(&m), benignSummary) {
		t.Fatal("Toolcalls shortcut must open the inspector without revealing benign notices")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF9})
	for _, want := range []string{benignSummary, "live correlated detail"} {
		if !strings.Contains(frameText(&m), want) {
			t.Fatalf("expanded live evidence omitted %q", want)
		}
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF9})
	if strings.Contains(frameText(&m), benignSummary) || m.conv.scrollback.Len() != beforeBlocks || guardrails.calls != 1 {
		t.Fatal("live recollapse fetched, mutated, or duplicated retained evidence")
	}

	s := sessionsState{deps: surfaceDeps{theme: theme.New("aztec", theme.AztecPalette()), keys: defaultKeys()}}
	s.applyReplayEvent(client.HookMsg{Text: "replay benign", Tool: "Read", Guardrail: benignReview("replay", "release_result")})
	s.view = sessionsTranscript
	s.Render(80, 20)
	if strings.Contains(stripANSIstr(s.transcriptVP.View()), benignSummary) {
		t.Fatal("collapsed replay benign hook is visible")
	}
	_, _, _ = s.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	s.Render(80, 20)
	if s.transcriptExpand || strings.Contains(stripANSIstr(s.transcriptVP.View()), benignSummary) {
		t.Fatal("Toolcalls shortcut must not reveal replayed benign notices")
	}
	_, handled, _ := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyF9})
	s.Render(80, 20)
	if !handled || !strings.Contains(stripANSIstr(s.transcriptVP.View()), benignSummary) || s.transcript.scrollback.Len() != 1 {
		t.Fatal("expand did not reveal exactly one retained replay hook")
	}
	_, _, _ = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyF9})
	s.Render(80, 20)
	if strings.Contains(stripANSIstr(s.transcriptVP.View()), benignSummary) || s.transcript.scrollback.Len() != 1 || guardrails.calls != 1 {
		t.Fatal("replay recollapse fetched, mutated, or duplicated evidence")
	}
}

func TestQuietBenignGuardrailNotices_Scenario2_RenderingAndCache(t *testing.T) {
	const width = 36
	guardrails := &quietGuardrailClient{details: map[string]client.GuardrailReviewDetail{
		"one": {ReviewID: "one", Concern: "detail\x1b[2J one", SourceDisplay: "source one"},
		"two": {ReviewID: "two", Concern: "detail two", SourceDisplay: "source\x1b]0;bad\x07 two"},
	}}
	m := quietTestModel(t, guardrails, false)
	m.rend.setWidth(width)
	m.conv.addNotice("before marker")
	m = applyBenignHook(t, m, client.HookMsg{Text: "ignored prose 1", Phase: "PostToolUse", Tool: "Read", Guardrail: benignReview("one", "execute")})
	m = applyBenignHook(t, m, client.HookMsg{Text: "ignored prose 2", Phase: "PostToolUse", Tool: "Write", Guardrail: benignReview("two", "execute")})
	m.conv.addNotice("after marker")

	collapsedRaw := strings.Join(m.rend.renderConversationLines(&m.conv.scrollback, false), "\n")
	baseline := conversation{}
	baseline.addNotice("before marker")
	baseline.addNotice("after marker")
	baselineRaw := strings.Join(newRenderer(m.deps.Theme, keyMarkings(defaultKeys())).renderConversationLines(&baseline.scrollback, false), "\n")
	if collapsedRaw != baselineRaw {
		t.Fatalf("hidden blocks left blank residue\ngot:  %q\nwant: %q", stripANSIstr(collapsedRaw), stripANSIstr(baselineRaw))
	}

	expandedRaw := strings.Join(m.rend.renderConversationLines(&m.conv.scrollback, true), "\n")
	for _, unsafe := range []string{"\x1b[2J", "\x1b]0;bad", "\x07"} {
		if strings.Contains(expandedRaw, unsafe) {
			t.Fatalf("raw rendered output retained unsafe sequence %q: %q", unsafe, expandedRaw)
		}
	}
	expandedOrdered := strings.Join(strings.Fields(stripANSIstr(expandedRaw)), " ")
	expected := []string{"before marker", "passed: Read", "one", "source one", "passed: Write", "detail two", "two", "after marker"}
	last := -1
	for _, want := range expected {
		at := strings.Index(expandedOrdered[last+1:], want)
		if at < 0 {
			t.Fatalf("expanded text %q missing or out of order after byte %d: %q", want, last, expandedOrdered)
		}
		last += 1 + at
	}
	for i, line := range strings.Split(expandedRaw, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("expanded line %d width = %d, want <= %d: %q", i, got, width, stripANSIstr(line))
		}
	}
	cachedRaw := strings.Join(m.rend.renderConversationLines(&m.conv.scrollback, true), "\n")
	freshRenderer := newRenderer(m.deps.Theme, keyMarkings(defaultKeys()))
	freshRenderer.setWidth(width)
	freshRaw := strings.Join(freshRenderer.renderConversationLines(&m.conv.scrollback, true), "\n")
	if cachedRaw != freshRaw {
		t.Fatalf("cache mismatch\ncached: %q\nfresh:  %q", cachedRaw, freshRaw)
	}
}

func TestQuietBenignGuardrailNotices_Scenario2_ApprovalDetailAlwaysVisible(t *testing.T) {
	guardrails := &quietGuardrailClient{details: map[string]client.GuardrailReviewDetail{
		"unrelated": {ReviewID: "unrelated", Concern: "unrelated benign detail"},
		"approval":  {ReviewID: "approval", Concern: "matching approval detail"},
	}}
	m := quietTestModel(t, guardrails, false)
	model, unrelatedCmd := m.applyHookMsg(client.HookMsg{Tool: "Read", Guardrail: benignReview("unrelated", "execute")})
	m = model.(Model)
	unrelated := runGuardrailDetailCommand(t, unrelatedCmd)

	model, approvalCmd := m.applyPermissionAsk(client.PermissionAskMsg{AskID: "ask", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "approval", Kind: "action"}})
	m = model.(Model)
	before := m.conv.scrollback.Len()
	m = applyAll(m, unrelated)
	s := approvalSurfaceOf(t, m)
	if s.ask.detail.Concern != "" || m.conv.scrollback.Len() != before || !benignGuardrailAt(&m.conv, 0) {
		t.Fatalf("unrelated response was not ignored by approval and retained on its benign notice: ask=%+v cards=%+v", s.ask.detail, m.conv.testBlocks())
	}
	if strings.Contains(frameText(&m), "unrelated benign detail") {
		t.Fatal("unrelated benign conversation detail became visible")
	}

	m = applyAll(m, runGuardrailDetailCommand(t, approvalCmd))
	if guardrails.calls != 2 {
		t.Fatalf("detail calls = %d, want 2", guardrails.calls)
	}
	if s = approvalSurfaceOf(t, m); s.ask.detail.Concern != "matching approval detail" {
		t.Fatalf("matching approval detail not consumed: %+v", s.ask.detail)
	}
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "matching approval detail") {
		t.Fatalf("matching approval detail not visible: %q", got)
	}
}

func TestQuietBenignGuardrailNotices_Scenario2_DetailResponseIdentity(t *testing.T) {
	guardrails := &quietGuardrailClient{details: map[string]client.GuardrailReviewDetail{
		"mismatch": {ReviewID: "different-review", Concern: "must not be accepted"},
	}}
	m := quietTestModel(t, guardrails, false)
	m = applyBenignHook(t, m, client.HookMsg{Tool: "Read", Guardrail: benignReview("mismatch", "execute")})
	got := strings.Join(strings.Fields(frameText(&m)), " ")
	if !strings.Contains(got, "response identity mismatch") || strings.Contains(got, "must not be accepted") {
		t.Fatalf("mismatched response did not fail visibly: %q", got)
	}

	model, cmd := m.applyHookMsg(client.HookMsg{Tool: "Read", Guardrail: benignReview("stale", "execute")})
	m = model.(Model)
	stale := runGuardrailDetailCommand(t, cmd)
	stale.Detail.Concern = "session A detail"
	before := m.conv.testBlocks()
	m.sessionID = "session-b"
	m = applyAll(m, stale)
	if after := m.conv.testBlocks(); len(after) != len(before) || after[len(after)-1].Revision != before[len(before)-1].Revision ||
		strings.Contains(stripANSIstr(strings.Join(m.rend.renderConversationLines(&m.conv.scrollback, true), "\n")), "session A detail") {
		t.Fatal("session A conversation detail entered session B")
	}
}

func TestQuietBenignGuardrailNotices_Scenario3_ShowSettingSurvivesThemeReconstruction(t *testing.T) {
	guardrails := &quietGuardrailClient{details: map[string]client.GuardrailReviewDetail{
		"live": {ReviewID: "live", Concern: "live benign detail"},
	}}
	m := quietTestModel(t, guardrails, true)
	m = applyBenignHook(t, m, client.HookMsg{Tool: "Read", Guardrail: benignReview("live", "execute")})
	m = m.switchTheme(theme.Solar())
	for _, want := range []string{benignSummary, "live benign detail"} {
		if !strings.Contains(frameText(&m), want) {
			t.Fatalf("show=true lost live %q after theme reconstruction", want)
		}
	}

	s := sessionsState{
		deps:                  surfaceDeps{theme: theme.New("aztec", theme.AztecPalette()), keys: defaultKeys()},
		showBenignHookNotices: true,
		view:                  sessionsTranscript,
	}
	s.applyReplayEvent(client.HookMsg{Tool: "Read", Guardrail: benignReview("replay", "release_result")})
	s.Render(80, 20)
	if got := stripANSIstr(s.transcriptVP.View()); !strings.Contains(got, benignSummary) {
		t.Fatalf("show=true hid replay hook: %q", got)
	}
	s.transcriptRend = nil
	s.deps.theme = theme.Solar()
	s.Render(80, 20)
	if got := stripANSIstr(s.transcriptVP.View()); !strings.Contains(got, benignSummary) {
		t.Fatalf("show=true lost replay hook after renderer reconstruction: %q", got)
	}
}
