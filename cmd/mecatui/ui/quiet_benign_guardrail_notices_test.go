package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func benignReview(id, disposition string) *client.GuardrailReview {
	return &client.GuardrailReview{ReviewID: id, Job: "inbound", Inspection: "complete", Assessment: "acceptable", Disposition: disposition}
}

func TestQuietBenignGuardrailNotices_Scenario1_ExactBenignMatrix(t *testing.T) {
	for _, job := range []string{"action", "inbound"} {
		for _, disposition := range []string{"execute", "release_result"} {
			review := benignReview("review-"+job+"-"+disposition, disposition)
			review.Job = job
			if !benignGuardrailReview(review) {
				t.Errorf("job %q disposition %q was not benign", job, disposition)
			}
		}
	}
}

func TestQuietBenignGuardrailNotices_Scenario1_FailsUnknownVisible(t *testing.T) {
	if benignGuardrailReview(nil) {
		t.Fatal("nil review unexpectedly benign")
	}
	cases := []struct {
		name   string
		mutate func(*client.GuardrailReview)
	}{
		{"missing review ID", func(r *client.GuardrailReview) { r.ReviewID = "" }},
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
			if benignGuardrailReview(review) {
				t.Fatalf("unexpectedly benign: %+v", review)
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
		if !benignGuardrailReview(tc.msg.Guardrail) {
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
	return stripANSIstr(strings.Join(m.rend.renderConversationLines(&m.conv, m.expandTools), "\n"))
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

func TestQuietBenignGuardrailNotices_Scenario2_DefaultLiveVisibility(t *testing.T) {
	guardrails := &quietGuardrailClient{details: map[string]client.GuardrailReviewDetail{
		"benign": {ReviewID: "benign", Concern: "benign detail"},
	}}
	m := quietTestModel(t, guardrails, false)
	model, cmd := m.applyHookMsg(client.HookMsg{Text: "benign hook prose", Phase: "PostToolUse", Tool: "Read", Guardrail: benignReview("benign", "execute")})
	m = model.(Model)
	detail := runGuardrailDetailCommand(t, cmd)
	if detail.SessionID != "session" || detail.ReviewID != "benign" || !detail.Conversation || !detail.Benign {
		t.Fatalf("detail correlation = %+v", detail)
	}
	m = applyAll(m, detail)
	m = applyAll(m,
		client.HookMsg{Text: "attention hook prose", Tool: "Read", Guardrail: &client.GuardrailReview{ReviewID: "attention", Job: "inbound", Inspection: "complete", Assessment: "unresolved", Disposition: "pass_advisory"}},
		client.ResultMsg{Stop: stopError, Error: "visible run error"},
	)
	if len(m.conv.blocks) != 4 || !m.conv.blocks[0].benignGuardrail || !m.conv.blocks[1].benignGuardrail {
		t.Fatalf("retained blocks = %+v", m.conv.blocks)
	}
	got := frameText(&m)
	for _, hidden := range []string{"completed acceptable", "benign detail"} {
		if strings.Contains(got, hidden) {
			t.Errorf("default frame exposed %q: %q", hidden, got)
		}
	}
	for _, visible := range []string{"inspection completed unresolved", "visible run error"} {
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
	model, cmd := m.applyHookMsg(client.HookMsg{Text: "live benign", Tool: "Read", Guardrail: benignReview("live", "execute")})
	m = model.(Model)
	m = applyAll(m, runGuardrailDetailCommand(t, cmd))
	if guardrails.calls != 1 || guardrails.sessions[0] != "session" || guardrails.reviewIDs[0] != "live" {
		t.Fatalf("detail calls=%d sessions=%v reviews=%v", guardrails.calls, guardrails.sessions, guardrails.reviewIDs)
	}
	if strings.Contains(frameText(&m), "acceptable") || strings.Contains(frameText(&m), "live correlated detail") {
		t.Fatal("collapsed live benign evidence is visible")
	}
	beforeBlocks := len(m.conv.blocks)
	m = applyAll(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	for _, want := range []string{"acceptable", "live correlated detail"} {
		if !strings.Contains(frameText(&m), want) {
			t.Fatalf("expanded live evidence omitted %q", want)
		}
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if strings.Contains(frameText(&m), "acceptable") || len(m.conv.blocks) != beforeBlocks || guardrails.calls != 1 {
		t.Fatal("live recollapse fetched, mutated, or duplicated retained evidence")
	}

	s := sessionsState{deps: surfaceDeps{theme: theme.New("aztec", theme.AztecPalette()), keys: defaultKeys()}}
	s.applyReplayEvent(client.HookMsg{Text: "replay benign", Tool: "Read", Guardrail: benignReview("replay", "release_result")})
	s.view = sessionsTranscript
	s.Render(80, 20)
	if strings.Contains(stripANSIstr(s.transcriptVP.View()), "acceptable") {
		t.Fatal("collapsed replay benign hook is visible")
	}
	_, handled, _ := s.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	s.Render(80, 20)
	if !handled || !strings.Contains(stripANSIstr(s.transcriptVP.View()), "acceptable") || len(s.transcript.blocks) != 1 {
		t.Fatal("expand did not reveal exactly one retained replay hook")
	}
	_, _, _ = s.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	s.Render(80, 20)
	if strings.Contains(stripANSIstr(s.transcriptVP.View()), "acceptable") || len(s.transcript.blocks) != 1 || guardrails.calls != 1 {
		t.Fatal("replay recollapse fetched, mutated, or duplicated evidence")
	}
}

func TestQuietBenignGuardrailNotices_Scenario2_RenderingAndCache(t *testing.T) {
	const width = 36
	m := quietTestModel(t, &quietGuardrailClient{}, false)
	m.rend.setWidth(width)
	m.conv.addNotice("before marker")
	for i, fixture := range []struct {
		id, tool, concern, source, next string
	}{
		{"one", "Read", "detail\x1b[2J one", "source one", "next one"},
		{"two", "Write", "detail two", "source\x1b]0;bad\x07 two", "next two"},
	} {
		model, _ := m.applyHookMsg(client.HookMsg{Text: fmt.Sprintf("ignored prose %d", i), Phase: "PostToolUse", Tool: fixture.tool, Guardrail: benignReview(fixture.id, "execute")})
		m = model.(Model)
		m = applyAll(m, client.GuardrailReviewDetailMsg{
			SessionID: "session", ReviewID: fixture.id, Conversation: true, Benign: true,
			Detail: client.GuardrailReviewDetail{ReviewID: fixture.id, Concern: fixture.concern, SourceDisplay: fixture.source, NextAction: fixture.next},
		})
	}
	m.conv.addNotice("after marker")

	collapsedRaw := strings.Join(m.rend.renderConversationLines(&m.conv, false), "\n")
	baseline := conversation{}
	baseline.addNotice("before marker")
	baseline.addNotice("after marker")
	baselineRaw := strings.Join(newRenderer(m.deps.Theme, keyMarkings(defaultKeys())).renderConversationLines(&baseline, false), "\n")
	if collapsedRaw != baselineRaw {
		t.Fatalf("hidden blocks left blank residue\ngot:  %q\nwant: %q", stripANSIstr(collapsedRaw), stripANSIstr(baselineRaw))
	}

	expandedRaw := strings.Join(m.rend.renderConversationLines(&m.conv, true), "\n")
	for _, unsafe := range []string{"\x1b[2J", "\x1b]0;bad", "\x07"} {
		if strings.Contains(expandedRaw, unsafe) {
			t.Fatalf("raw rendered output retained unsafe sequence %q: %q", unsafe, expandedRaw)
		}
	}
	expanded := stripANSIstr(expandedRaw)
	expandedOrdered := strings.Join(strings.Fields(expanded), " ")
	expected := []string{"before marker", "inbound Read", "detail[2J one", "source one", "next one", "inbound Write", "detail two", "source]0;bad two", "next two", "after marker"}
	last := -1
	for _, want := range expected {
		at := strings.Index(expandedOrdered, want)
		if at < 0 || at <= last {
			t.Fatalf("expanded text %q missing or out of order after byte %d: %q", want, last, expandedOrdered)
		}
		last = at
	}
	for i, line := range strings.Split(expandedRaw, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("expanded line %d width = %d, want <= %d: %q", i, got, width, stripANSIstr(line))
		}
	}
	cachedRaw := strings.Join(m.rend.renderConversationLines(&m.conv, true), "\n")
	freshRenderer := newRenderer(m.deps.Theme, keyMarkings(defaultKeys()))
	freshRenderer.setWidth(width)
	freshRaw := strings.Join(freshRenderer.renderConversationLines(&m.conv, true), "\n")
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
	before := len(m.conv.blocks)
	m = applyAll(m, unrelated)
	s := openApprovalSurface(&m)
	if s.ask.reviewDetail.Concern != "" || len(m.conv.blocks) != before+1 || !m.conv.blocks[len(m.conv.blocks)-1].benignGuardrail {
		t.Fatalf("unrelated response was not ignored by approval and retained by generic reducer: ask=%+v blocks=%+v", s.ask.reviewDetail, m.conv.blocks)
	}
	if strings.Contains(frameText(&m), "unrelated benign detail") {
		t.Fatal("unrelated benign conversation detail became visible")
	}

	matching := runGuardrailDetailCommand(t, approvalCmd)
	m = applyAll(m, matching)
	if guardrails.calls != 2 {
		t.Fatalf("detail calls = %d, want 2", guardrails.calls)
	}
	s = openApprovalSurface(&m)
	if s.ask.reviewDetail.Concern != "matching approval detail" {
		t.Fatalf("matching approval detail not consumed: %+v", s.ask.reviewDetail)
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
	model, cmd := m.applyHookMsg(client.HookMsg{Tool: "Read", Guardrail: benignReview("mismatch", "execute")})
	m = model.(Model)
	m = applyAll(m, runGuardrailDetailCommand(t, cmd))
	got := frameText(&m)
	if !strings.Contains(got, "response identity mismatch") || strings.Contains(got, "must not be accepted") {
		t.Fatalf("mismatched response did not fail visibly: %q", got)
	}

	before := len(m.conv.blocks)
	m.sessionID = "session-b"
	m = applyAll(m, client.GuardrailReviewDetailMsg{
		SessionID: "session", ReviewID: "stale", Conversation: true,
		Detail: client.GuardrailReviewDetail{ReviewID: "stale", Concern: "session A detail"},
	})
	if len(m.conv.blocks) != before || strings.Contains(frameText(&m), "session A detail") {
		t.Fatal("session A conversation detail entered session B")
	}
}

func TestQuietBenignGuardrailNotices_Scenario3_ShowSettingSurvivesThemeReconstruction(t *testing.T) {
	m := quietTestModel(t, &quietGuardrailClient{}, true)
	model, _ := m.applyHookMsg(client.HookMsg{Tool: "Read", Guardrail: benignReview("live", "execute")})
	m = model.(Model)
	m = applyAll(m, client.GuardrailReviewDetailMsg{
		SessionID: "session", ReviewID: "live", Conversation: true, Benign: true,
		Detail: client.GuardrailReviewDetail{ReviewID: "live", Concern: "live benign detail"},
	})
	m = m.switchTheme(theme.Solar())
	for _, want := range []string{"inspection completed acceptable", "live benign detail"} {
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
	if got := stripANSIstr(s.transcriptVP.View()); !strings.Contains(got, "inspection completed acceptable") {
		t.Fatalf("show=true hid replay hook: %q", got)
	}
	s.transcriptRend = nil
	s.deps.theme = theme.Solar()
	s.Render(80, 20)
	if got := stripANSIstr(s.transcriptVP.View()); !strings.Contains(got, "inspection completed acceptable") {
		t.Fatalf("show=true lost replay hook after renderer reconstruction: %q", got)
	}
}
