package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/schedparse"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func schedulePaste(m Model, content string) Model {
	mm, _ := m.Update(tea.PasteMsg{Content: content})
	return mm.(Model)
}

func scheduleFocus(t *testing.T, m Model, s *scheduleState, index int) Model {
	t.Helper()
	for i := 0; s.form.focusIdx != index && i < scheduleControlCount; i++ {
		m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	}
	if s.form.focusIdx != index {
		t.Fatalf("could not focus %d: compact=%v", index, s.compact)
	}
	return m
}

func TestScheduleCreateDoesNotOwnRootAsyncMessages(t *testing.T) {
	for _, state := range []string{"editable", "pending", "compact"} {
		t.Run(state, func(t *testing.T) {
			fs := &fakeScheduleLister{schedules: []client.Schedule{sampleSchedule("existing")}}
			m, s := openedSchedule(t, fs)
			m.prompt.Rewrite("root draft")
			m = scheduleRune(t, m, "c")
			s.form.name.SetValue("new")
			s.form.prompt.SetValue("review\nreport")
			s.form.trigger.SetValue("every day at 9am")
			s.form.timezone.SetValue("America/New_York")
			s.form.mutating = true
			m = scheduleFocus(t, m, s, schedulePrompt)
			if state == "pending" {
				m = scheduleFocus(t, m, s, scheduleSubmit)
				mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				m = mm.(Model)
				if s.pendingAction != "create" {
					t.Fatal("fixture did not submit")
				}
			}
			if state == "compact" {
				m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 0})
				_ = m.View()
				if !s.compact {
					t.Fatal("fixture is not compact")
				}
			}
			values := func() []string {
				return []string{s.form.name.Value(), s.form.prompt.Value(), s.form.trigger.Value(), s.form.timezone.Query(), s.form.timezone.Value()}
			}
			before, focus, mutating := values(), s.form.focusIdx, s.form.mutating
			if _, handled, _ := s.HandleMsg(struct{}{}); handled {
				t.Fatal("surface claimed an unrelated message")
			}
			m.modelCatalogRequestToken = 11
			m = applyAll(m, client.ModelsMsg{RequestToken: 11, Models: []client.ModelInfo{{ProviderID: "test", ID: "new-catalog-model"}}})
			if len(m.modelCatalog.models) != 1 || m.modelCatalog.models[0].ID != "new-catalog-model" {
				t.Fatal("creator swallowed the root catalog result")
			}
			m.liveReconGen = 7
			ch := make(chan tea.Msg, 1)
			m.liveReconCh = ch
			m.deps.LiveStream = &fakeLiveStreamer{stream: client.NewFakeEventStream(deliveryEv("audit", "fire-async"))}
			defer func() { m.disarmLiveFeed() }()
			mm, reader := m.Update(reconnectMsg{gen: 7, msg: client.LiveReconnectingMsg{Attempt: 3}})
			m = mm.(Model)
			if !m.liveReconnecting || m.liveReconnectAttempt != 3 || reader == nil {
				t.Fatal("creator swallowed a valid reconnect generation or its next reader")
			}
			ch <- client.LiveReconnectedMsg{}
			mm, rearmed := m.Update(runCmdTimeout(t, reader))
			m = mm.(Model)
			if m.liveReconnecting || m.liveReconCh != nil || m.liveArmed != "active-chat" || m.liveCh == nil || rearmed == nil {
				t.Fatal("reconnect did not rearm the root live reader")
			}
			m = applyAll(m, runCmdTimeout(t, m.waitLiveCmd()))
			blocks := m.conv.testBlocks()
			if len(blocks) != 1 {
				t.Fatal("rearmed reader did not deliver the scheduled result")
			}
			delivery, ok := blocks[0].Payload.(scrollback.DeliveryCardSnapshot)
			if !ok || delivery.ScheduleName != "audit" {
				t.Fatal("scheduled delivery did not reach the root conversation")
			}
			// Surface-owned schedule results must still reduce while the form is open.
			m = feedCmd(t, m, s.listCmd())
			if s.loading || len(s.schedules) != 1 || s.schedules[0].Spec.Name != "existing" || !slices.Equal(values(), before) || s.form.focusIdx != focus || s.form.mutating != mutating || m.modal != s || m.prompt.Value() != "root draft" {
				t.Fatal("async routing lost schedule results, form values/focus, or root draft")
			}
		})
	}
}

func TestScheduleEditorReceiptsAreScopedToFocusAndForm(t *testing.T) {
	m, s := openedSchedule(t, &fakeScheduleLister{})
	m = scheduleRune(t, m, "c")
	m = scheduleFocus(t, m, s, schedulePrompt)
	widgetCmd := func() tea.Msg {
		return tea.BatchMsg{func() tea.Msg { return tea.PasteMsg{Content: "queued paste"} }}
	}
	m = applyAll(m, flattenBatch(s.editorCmd(widgetCmd))...)
	if s.form.prompt.Value() != "queued paste" {
		t.Fatal("tagged widget batch did not reach its editor")
	}
	staleFocus := s.editorCmd(widgetCmd)
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = applyAll(m, flattenBatch(staleFocus)...)
	if s.form.trigger.Value() != "" || s.form.prompt.Value() != "queued paste" || s.form.focusIdx != scheduleTiming {
		t.Fatal("queued paste changed fields after focus traversal")
	}
	m = scheduleFocus(t, m, s, schedulePrompt)
	hidden := s.editorCmd(widgetCmd)
	m = applyAll(m, tea.WindowSizeMsg{Width: 0, Height: 0})
	// No View between resize and the asynchronous editor receipt.
	m = applyAll(m, flattenBatch(hidden)...)
	if !s.compact || s.form.prompt.Value() != "queued paste" {
		t.Fatal("queued widget paste bypassed fresh compact geometry")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	_ = m.View()
	late := s.editorCmd(widgetCmd)
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = scheduleRune(t, m, "c")
	m = scheduleFocus(t, m, s, schedulePrompt)
	m = applyAll(m, flattenBatch(late)...)
	if s.form.prompt.Value() != "" || s.form.focusIdx != schedulePrompt || m.modal != s {
		t.Fatal("abandoned editor command changed a new independent form")
	}
}

func TestScheduleEditorReceiptDoesNotCrossSurfaceLifetimes(t *testing.T) {
	m, old := openedSchedule(t, &fakeScheduleLister{})
	m.prompt.Rewrite("root draft")
	m = scheduleRune(t, m, "c")
	m = scheduleFocus(t, m, old, schedulePrompt)
	paste := func() tea.Msg { return tea.PasteMsg{Content: "queued widget paste"} }
	receipt := old.editorCmd(paste)().(scheduleEditorMsg)
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modal != nil {
		t.Fatal("fixture did not close the old surface")
	}
	mm, cmd := m.openSchedule()
	m = feedCmd(t, mm.(Model), cmd)
	current := m.modal.(*scheduleState)
	m = scheduleRune(t, m, "c")
	m = scheduleFocus(t, m, current, schedulePrompt)
	if current == old || current.editorGeneration != receipt.generation || current.view != receipt.view {
		t.Fatal("fixture must isolate owner identity from generation and view checks")
	}
	m = applyAll(m, receipt)
	if current.form.prompt.Value() != "" || m.prompt.Value() != "root draft" || m.modal != current {
		t.Fatal("old widget receipt crossed surface lifetimes")
	}
	m = applyAll(m, current.editorCmd(paste)())
	if current.form.prompt.Value() != "queued widget paste" || m.prompt.Value() != "root draft" {
		t.Fatal("current widget receipt was not delivered to the new form")
	}
}

func TestSchedulePasteOnlyReachesFocusedSurfaceInput(t *testing.T) {
	fs := &fakeScheduleLister{schedules: scheduleRows(2)}
	m, s := openedSchedule(t, fs)
	m.prompt.Rewrite("root draft")
	m = schedulePaste(m, strings.Repeat("/workspace/image.png\n", 50))
	if s.filter.Value() != "" || m.prompt.Value() != "root draft" || len(m.stagedPastes)+len(m.stagedMedia) != 0 {
		t.Fatal("unhandled modal paste reached hidden chat/staging or unfocused filter")
	}
	m = scheduleRune(t, m, "/")
	m = schedulePaste(m, "schedule-01")
	if s.filter.Value() != "schedule-01" || len(s.filtered) != 1 || s.filtered[0].Spec.Name != "schedule-01" || m.prompt.Value() != "root draft" {
		t.Fatal("focused inventory paste did not refresh its own filter")
	}
}

func TestScheduleCreatePasteSelectionAndCursorDispatch(t *testing.T) {
	fs := &fakeScheduleLister{}
	m, s := openedSchedule(t, fs)
	m.prompt.Rewrite("unsent chat draft")
	m = scheduleRune(t, m, "c")
	m = schedulePaste(m, "name\nnot a shortcut")
	if strings.ContainsAny(s.form.name.Value(), "\r\n") || s.form.name.Value() == "" {
		t.Fatal("single-line paste did not use textinput policy")
	}
	m = scheduleFocus(t, m, s, schedulePrompt)
	m = schedulePaste(m, "first"+xtermModifyOtherKeysCtrlJ+"second")
	if s.form.prompt.Value() != "first\nsecond" {
		t.Fatalf("terminal newline encoding: %q", s.form.prompt.Value())
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	m = schedulePaste(m, "!")
	if s.form.focusIdx != schedulePrompt || s.form.prompt.Value() != "first!\nsecond" {
		t.Fatal("paste did not insert at multiline editor cursor")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !s.form.prompt.HasSelection() {
		t.Fatal("upstream select-all binding did not reach textarea")
	}
	payload := "/schedule\nc\tp\tr\tf\td\ny\nn\n/workspace/image.png\n" + strings.Repeat("long task\n", 80)
	m = schedulePaste(m, payload)
	payload = strings.ReplaceAll(payload, "\t", "    ") // Upstream textarea's normal tab policy.
	if s.form.prompt.Value() != payload || s.form.prompt.HasSelection() {
		t.Fatalf("paste did not replace selected text literally: got %q", s.form.prompt.Value())
	}
	// Use the real field's blink message, not an invented generic event.
	blink := s.form.prompt.Focus()
	if blink == nil {
		t.Fatal("textarea did not arm cursor")
	}
	mm, nextBlink := m.Update(blink())
	m = mm.(Model)
	if nextBlink == nil || s.form.focusIdx != schedulePrompt || s.form.prompt.Value() != payload {
		t.Fatal("active textarea cursor message was lost or edited text")
	}
	body := scheduleBody(t, s, 100, 30)
	if s.compact || !strings.Contains(body, "run history") || !strings.Contains(body, "enter: newline") || s.form.prompt.Height() > 6 || s.form.prompt.ScrollYOffset() == 0 {
		t.Fatal("long task did not scroll internally while retaining guidance")
	}
	if m.prompt.Value() != "unsent chat draft" || m.sessionID != "active-chat" || len(m.stagedMedia) != 0 || len(m.stagedPastes) != 0 || fs.createCalls+fs.pauseCalls+fs.resumeCalls+fs.fireCalls+fs.deleteCalls != 0 {
		t.Fatal("pasted shortcuts reached root state/actions/media")
	}
	// Replacement of only the previous character uses upstream shift-selection.
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = schedulePaste(m, "abc")
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
	m = schedulePaste(m, "Z")
	if s.form.prompt.Value() != "abZ" {
		t.Fatalf("partial selection replacement = %q", s.form.prompt.Value())
	}
	m = scheduleFocus(t, m, s, scheduleTiming)
	m = schedulePaste(m, "every day at 9am")
	if body := scheduleBody(t, s, 100, 30); !strings.Contains(body, "Timing:") || !strings.Contains(body, "every 30 minutes") || !strings.Contains(body, "five-field cron") {
		t.Fatal("populated timing field lost persistent guidance")
	}
}

func TestScheduleCreateTimezoneQueryAcceptanceDismissAndGeometry(t *testing.T) {
	fs := &fakeScheduleLister{}
	m, s := openedSchedule(t, fs)
	m.prompt.Rewrite("chat draft")
	m = scheduleRune(t, m, "c")
	s.form.name.SetValue("review")
	s.form.prompt.SetValue("review without edits")
	s.form.trigger.SetValue("every weekday at 9am")
	m = scheduleFocus(t, m, s, scheduleTimezone)
	if !s.form.timezone.Open() || s.form.timezone.Value() != "UTC" {
		t.Fatal("UTC was not an explicit default")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	m = schedulePaste(m, "Los Angeles")
	if s.form.timezone.Value() != "" || !s.form.timezone.Open() {
		t.Fatal("city query silently retained old timezone")
	}
	if _, err := s.validateCreate(); err == nil {
		t.Fatal("unaccepted city query validated")
	}
	body := scheduleBody(t, s, 100, 30)
	if !strings.Contains(body, "America/Los_Angeles") || !strings.Contains(body, "Create disabled") || !strings.Contains(body, "accept timezone") {
		t.Fatal("query suggestions/error/accept hint missing: " + body)
	}
	m = scheduleFocus(t, m, s, scheduleSubmit)
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if fs.createCalls != 0 || s.form.timezone.Value() != "" || s.form.timezone.Query() != "Los Angeles" || s.form.focusIdx != scheduleSubmit {
		t.Fatal("Tab accepted a highlight, Create used old UTC, or local failure lost draft/focus")
	}
	m = scheduleFocus(t, m, s, scheduleTimezone)
	// No View before paste: root must refresh geometry, not reuse the last offer.
	for _, size := range [][2]int{{38, 30}, {100, 8}, {0, 0}} {
		mm, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = schedulePaste(mm.(Model), "hidden")
		m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if s.form.timezone.Query() != "Los Angeles" || s.form.timezone.Value() != "" || !s.compact || fs.createCalls != 0 {
			t.Fatal("hidden resize allowed query edit/activation")
		}
	}
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = scheduleKey(t, mm.(Model), tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.form.timezone.Open() || s.form.timezone.Value() != "America/Los_Angeles" || fs.createCalls != 0 {
		t.Fatal("Enter acceptance reopened suggestions or submitted")
	}
	// Geometry must not reopen accepted/dismissed suggestions.
	scheduleBody(t, s, 80, 35)
	if s.form.timezone.Open() {
		t.Fatal("resize reopened accepted suggestions")
	}
	m = scheduleFocus(t, m, s, scheduleSubmit)
	m = scheduleFocus(t, m, s, scheduleTimezone)
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.view != scheduleCreate || s.form.timezone.Open() {
		t.Fatal("first Esc did not just dismiss suggestions")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.view != schedulePanel || m.prompt.Value() != "chat draft" || s.form.initialized {
		t.Fatal("second Esc did not abandon the form without chat leakage")
	}
	m = scheduleRune(t, m, "c")
	if m.modal != s || s.form.focusIdx != scheduleName || !s.form.name.Focused() || s.form.name.Value() != "" || s.form.prompt.Value() != "" || s.form.trigger.Value() != "" || s.form.timezone.Value() != "UTC" || s.form.mutating || s.actionErr != "" {
		t.Fatal("reopening resurrected an abandoned draft, timezone, focus or error")
	}
}

func TestScheduleCreateLocalValidationAndFreshZonedSubmission(t *testing.T) {
	serverNext := time.Date(2030, 5, 9, 19, 12, 0, 0, time.UTC)
	fs := &fakeScheduleLister{nextFire: serverNext}
	m, s := openedSchedule(t, fs)
	m.createModelSelection = client.ModelSelection{ProviderID: "create-provider", ModelID: "create-model"}
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "chat-provider", ModelID: "chat-model"}
	m.prompt.Rewrite("chat draft")
	m = scheduleRune(t, m, "c")
	s.form.name.SetValue("review")
	s.form.prompt.SetValue("first\nsecond")
	s.form.timezone.SetValue("America/Los_Angeles")
	clock := time.Date(2027, 1, 1, 23, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	m = scheduleFocus(t, m, s, scheduleSubmit)
	for _, bad := range []string{"daily at 25:99", " CRON_TZ=Europe/Paris 0 9 * * *", " tz=Europe/Paris 0 9 * * *"} {
		s.form.trigger.SetValue(bad)
		m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		body := scheduleBody(t, s, 100, 30)
		if fs.createCalls != 0 || s.pendingAction != "" || !strings.Contains(body, "Create disabled") || !strings.Contains(body, "timing") || s.form.trigger.Value() != bad || s.form.focusIdx != scheduleSubmit {
			t.Fatalf("bad timing escaped validation or lost draft: %q", bad)
		}
	}
	s.form.trigger.SetValue("tomorrow at 9am")
	preview, err := s.validateCreate()
	if err != nil {
		t.Fatal(err)
	}
	body := scheduleBody(t, s, 100, 30)
	if !strings.Contains(body, "Estimate:") || !strings.Contains(body, "America/Los_Angeles") || !strings.Contains(body, "-08:00") || !strings.Contains(body, "2027-01-02") {
		t.Fatal("preview omitted actual zoned instant/date/offset: " + body)
	}
	clock = clock.Add(24 * time.Hour)
	want, err := schedparse.Validate("tomorrow at 9am", "America/Los_Angeles", clock)
	if err != nil || preview.OneShot.Equal(want.OneShot) {
		t.Fatal("clock fixture did not move relative calendar")
	}
	mm, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	clock = clock.Add(24 * time.Hour)
	if !strings.Contains(scheduleBody(t, s, 100, 30), want.Next.Format("2006-01-02")) || !s.form.submitted.OneShot.Equal(want.OneShot) {
		t.Fatal("pending preview drifted from the frozen submitted instant")
	}
	m = feedCmd(t, m, submit)
	if fs.createCalls != 1 || !fs.created.Trigger.OneShot.Equal(want.OneShot) || fs.created.Trigger.Cron != "" || fs.created.Timezone != "America/Los_Angeles" || fs.created.Mode != "plan" || fs.created.Mutating || fs.created.Selector != (client.ScheduleSelector{}) || fs.created.Profile != "" {
		t.Fatalf("fresh validated spec / default policy lost: %+v", fs.created)
	}
	if !strings.Contains(s.notice, "next run (server): "+formatScheduleTime(serverNext)) || s.view != schedulePanel || m.prompt.Value() != "chat draft" || m.sessionID != "active-chat" {
		t.Fatal("success did not present authoritative server time and preserve chat")
	}
}

func TestScheduleCreatePopulatedGeometryAndErrorBounds(t *testing.T) {
	m, s := openedSchedule(t, &fakeScheduleLister{})
	m.prompt.Rewrite("root draft")
	m = scheduleRune(t, m, "c")
	s.form.name.SetValue(strings.Repeat("界name", 50))
	s.form.prompt.SetValue(strings.Repeat("界task\n", 100))
	s.form.trigger.SetValue("every weekday at 9am")
	s.actionErr = "Try another name: " + strings.Repeat("界server error ", 100)
	m = scheduleFocus(t, m, s, scheduleTimezone)
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	m = schedulePaste(m, "America")
	for _, size := range [][2]int{{100, 35}, {60, 40}, {38, 20}, {100, 8}, {0, 0}, {100, 35}} {
		body := scheduleBody(t, s, size[0], size[1])
		if size[0] >= 60 && size[1] >= 35 && (s.compact || !strings.Contains(body, "Try another name") || !strings.Contains(body, "…") || !strings.Contains(body, "Timezone:") || !strings.Contains(body, "suggestions") || strings.Count(body, "America/") > 5) {
			t.Fatalf("populated geometry hid bounded controls/help: %v\n%s", size, body)
		}
		if s.form.name.Value() != strings.Repeat("界name", 50) || s.form.prompt.Value() != strings.Repeat("界task\n", 100) || s.form.timezone.Query() != "America" || s.form.timezone.Value() != "" || s.form.focusIdx != scheduleTimezone {
			t.Fatal("resize changed draft/focus/query identity")
		}
	}
	_ = m.View()
	if m.prompt.Value() != "root draft" || s.compact {
		t.Fatal("resize restore changed root draft or failed to reveal form")
	}
}

func TestScheduleCreateActionBindingsAndCancel(t *testing.T) {
	fs := &fakeScheduleLister{}
	m, s := openedSchedule(t, fs)
	s.deps.keys.Choose = key.NewBinding(key.WithKeys("space"))
	s.deps.keys.Close = key.NewBinding(key.WithKeys("tab"))
	s.deps.marks.choose, s.deps.marks.closeOnly = "space", "tab"
	m = scheduleRune(t, m, "c")
	s.form.focusIdx = scheduleReadOnly
	s.focusField()
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	body := scheduleBody(t, s, 100, 30)
	if s.form.mutating || fs.createCalls != 0 || strings.Contains(body, "space: toggle") || strings.Contains(body, "tab: next") || !strings.Contains(body, "tab: back") {
		t.Fatal("captured actions did not outrank checkbox/traversal or hints lied")
	}
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if s.view != schedulePanel {
		t.Fatal("remapped Back traversed instead")
	}
	m = scheduleRune(t, m, "c")
	s.form.focusIdx = scheduleCancel
	s.focusField()
	m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if s.view != schedulePanel || fs.createCalls != 0 || m.modal != s || s.form.initialized {
		t.Fatal("independent Cancel submitted or retained its abandoned form")
	}
}
