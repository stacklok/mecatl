package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func benignReview(id, disposition string) *client.GuardrailReview {
	return &client.GuardrailReview{ReviewID: id, Inspection: "complete", Assessment: "acceptable", Disposition: disposition}
}

func TestQuietBenignGuardrailNotices_Scenario1_ExactBenignMatrix(t *testing.T) {
	for _, disposition := range []string{"execute", "release_result"} {
		if !benignGuardrailReview(benignReview("review", disposition)) {
			t.Errorf("disposition %q was not benign", disposition)
		}
	}
}

func TestQuietBenignGuardrailNotices_Scenario1_FailsUnknownVisible(t *testing.T) {
	cases := []*client.GuardrailReview{
		nil,
		{},
		{Inspection: "unknown", Assessment: "acceptable", Disposition: "execute"},
		{Inspection: "operational_failure", Assessment: "acceptable", Disposition: "execute"},
		{Inspection: "complete", Assessment: "unknown", Disposition: "execute"},
		{Inspection: "complete", Assessment: "unresolved", Disposition: "execute"},
		{Inspection: "complete", Assessment: "prohibited", Disposition: "execute"},
		{Inspection: "complete", Assessment: "acceptable", Disposition: "ask_action"},
		{Inspection: "complete", Assessment: "acceptable", Disposition: "withhold_result"},
		{Inspection: "complete", Assessment: "acceptable", Disposition: "deny"},
		{Inspection: "complete", Assessment: "acceptable", Disposition: "pass_advisory"},
		{Inspection: "complete", Assessment: "acceptable", Disposition: "continue_warning"},
		{Inspection: "complete", Assessment: "acceptable", Disposition: "future_value"},
	}
	for i, review := range cases {
		if benignGuardrailReview(review) {
			t.Errorf("case %d unexpectedly benign: %+v", i, review)
		}
	}
}

func TestQuietBenignGuardrailNotices_Scenario1_DoesNotParseProse(t *testing.T) {
	review := benignReview("review", "execute")
	review.ReasonCode = "DENY operational failure prohibited"
	review.RuleID = "withhold_result"
	review.RuleOrigin = "warning"
	review.CheckerProviderID = "unknown"
	review.CheckerModelID = "ask_action"
	review.ConcernRefs = []string{"unsafe prose"}
	review.SourceRefs = []string{"failed prose"}
	if !benignGuardrailReview(review) {
		t.Fatal("non-classifier fields changed a benign review")
	}
}

func quietTestModel(t *testing.T, show bool) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Guardrails: &quietGuardrailClient{}, ShowBenignHookNotices: show})
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 30})
	m.phase = phaseRunning
	m.sessionID = "session"
	return m
}

func frameText(m *Model) string {
	return stripANSIstr(strings.Join(m.rend.renderConversationLines(&m.conv, m.expandTools), "\n"))
}

func TestQuietBenignGuardrailNotices_Scenario2_DefaultLiveVisibility(t *testing.T) {
	m := quietTestModel(t, false)
	m = applyAll(m, client.HookMsg{Text: "benign hook", Tool: "Read", Guardrail: benignReview("benign", "execute")})
	if !m.guardrailBenign["benign"] {
		t.Fatal("benign hook did not retain detail correlation")
	}
	m = applyAll(m, client.GuardrailReviewDetailMsg{Detail: client.GuardrailReviewDetail{ReviewID: "benign", Concern: "benign detail"}})
	m = applyAll(m, client.HookMsg{Text: "visible hook", Tool: "Read", Guardrail: &client.GuardrailReview{ReviewID: "attention", Inspection: "complete", Assessment: "unresolved", Disposition: "pass_advisory"}})
	m = applyAll(m, client.GuardrailReviewDetailMsg{Detail: client.GuardrailReviewDetail{ReviewID: "attention", Concern: "visible detail"}})

	if len(m.conv.blocks) != 4 {
		t.Fatalf("captured blocks = %d, want 4", len(m.conv.blocks))
	}
	got := frameText(&m)
	for _, hidden := range []string{"benign hook", "benign detail"} {
		if strings.Contains(got, hidden) {
			t.Errorf("default frame exposed %q: %q", hidden, got)
		}
	}
	for _, visible := range []string{"inspection completed unresolved", "visible detail"} {
		if !strings.Contains(got, visible) {
			t.Errorf("default frame omitted %q: %q", visible, got)
		}
	}
}

func TestQuietBenignGuardrailNotices_Scenario2_ExpandLiveAndReplay(t *testing.T) {
	m := quietTestModel(t, false)
	msg := client.HookMsg{Text: "benign", Tool: "Read", Guardrail: benignReview("live", "execute")}
	m = applyAll(m, msg)
	if strings.Contains(frameText(&m), "acceptable") {
		t.Fatal("collapsed live benign hook is visible")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !strings.Contains(frameText(&m), "acceptable") {
		t.Fatal("expanded live benign hook is hidden")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if strings.Contains(frameText(&m), "acceptable") || len(m.conv.blocks) != 1 {
		t.Fatal("collapse mutated or duplicated retained live hook")
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
		t.Fatal("ExpandTools did not reveal the retained replay hook")
	}
}

func TestQuietBenignGuardrailNotices_Scenario2_RenderingAndCache(t *testing.T) {
	m := quietTestModel(t, false)
	m.conv.addNotice("before")
	m.conv.addHook("benign\x1b[2J one", "PostToolUse", "Read", "info")
	m.conv.blocks[len(m.conv.blocks)-1].benignGuardrail = true
	m.conv.addNotice("detail\x1b]0;bad\x07 one")
	m.conv.blocks[len(m.conv.blocks)-1].benignGuardrail = true
	m.conv.addHook("benign two", "PostToolUse", "Read", "info")
	m.conv.blocks[len(m.conv.blocks)-1].benignGuardrail = true
	m.conv.addNotice("after")
	collapsed := frameText(&m)
	if strings.Contains(collapsed, "benign") || strings.Contains(collapsed, "detail") || strings.Contains(collapsed, "\n\n\n") {
		t.Fatalf("hidden blocks left content or blank residue: %q", collapsed)
	}
	m.expandTools = true
	expanded := frameText(&m)
	if strings.Index(expanded, "benign one") > strings.Index(expanded, "detail one") || strings.Index(expanded, "detail one") > strings.Index(expanded, "benign two") {
		t.Fatalf("expanded block order changed: %q", expanded)
	}
	if strings.ContainsRune(expanded, '\x1b') {
		t.Fatalf("expanded notice was not sanitized: %q", expanded)
	}
	cached := frameText(&m)
	fresh := newRenderer(m.deps.Theme, keyMarkings(defaultKeys())).renderConversationLines(&m.conv, true)
	if cached != stripANSIstr(strings.Join(fresh, "\n")) {
		t.Fatalf("cache mismatch\ncached: %q\nfresh: %q", cached, stripANSIstr(strings.Join(fresh, "\n")))
	}
}

type quietGuardrailClient struct {
	calls int
}

func (*quietGuardrailClient) ListGuardrailCoverage(context.Context, string) (client.GuardrailCoverage, error) {
	return client.GuardrailCoverage{}, nil
}
func (f *quietGuardrailClient) GetGuardrailReviewDetail(context.Context, string, string) (client.GuardrailReviewDetail, error) {
	f.calls++
	return client.GuardrailReviewDetail{ReviewID: "approval", Concern: "approval detail"}, nil
}

func TestQuietBenignGuardrailNotices_Scenario2_ApprovalDetailAlwaysVisible(t *testing.T) {
	guardrails := &quietGuardrailClient{}
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Guardrails: guardrails})
	m.sessionID = "session"
	model, cmd := m.applyPermissionAsk(client.PermissionAskMsg{AskID: "ask", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "approval"}})
	if cmd == nil {
		t.Fatal("guardrail approval did not request detail")
	}
	_ = model
	msg := client.GetGuardrailReviewDetailCmd(context.Background(), guardrails, "session", "approval")()
	if guardrails.calls != 1 {
		t.Fatalf("detail calls = %d, want 1", guardrails.calls)
	}
	s := openApprovalSurface(&m)
	s.applyPermissionAsk(client.PermissionAskMsg{AskID: "ask", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "approval"}}, false, phaseRunning)
	s.HandleMsg(msg)
	if s.ask.reviewDetail.Concern != "approval detail" {
		t.Fatalf("approval detail not displayed: %+v", s.ask.reviewDetail)
	}
}
