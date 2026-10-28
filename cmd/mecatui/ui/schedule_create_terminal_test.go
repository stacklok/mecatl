package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestScheduleCreateNormalTerminal(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			fs := &fakeScheduleLister{nextFire: time.Date(2027, 1, 4, 17, 0, 0, 0, time.UTC)}
			m, s := openedSchedule(t, fs)
			m = applyAll(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = scheduleRune(t, m, "c")
			s.now = func() time.Time { return time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC) }
			snapshot := func(state string) string {
				t.Helper()
				frame := m.View().Content
				plain := stripANSIstr(frame)
				if s.compact || s.offers[scheduleCreate] != [2]int{m.width, m.vp.Height()} {
					t.Fatalf("%s focus %d: root offered %dx%d but creator is close-only:\n%s", state, s.form.focusIdx, m.width, m.vp.Height(), plain)
				}
				if lipgloss.Height(frame) != size[1] || lipgloss.Width(frame) > size[0] {
					t.Fatalf("%s: root frame escaped %v", state, size)
				}
				lines, footer := strings.Split(frame, "\n"), strings.Split(m.renderFooter(), "\n")
				if !slices.Equal(lines[len(lines)-len(footer):], footer) {
					t.Fatal("creator displaced root footer")
				}
				for _, text := range []string{fmt.Sprintf("(%d/7)", s.form.focusIdx+1), "[Create", "[Cancel]"} {
					if !strings.Contains(plain, text) {
						t.Fatalf("%s focus %d: missing visible control %q:\n%s", state, s.form.focusIdx, text, plain)
					}
				}
				bodyLines := strings.Split(plain, "\n")
				for i, line := range bodyLines {
					if strings.Contains(line, "[Cancel]") && (i+2 >= len(bodyLines) || strings.TrimSpace(bodyLines[i+1]) != "" || strings.TrimSpace(bodyLines[i+2]) == "") {
						t.Fatal("form actions are not separated from the following information")
					}
					info := strings.TrimSpace(line)
					style := s.deps.theme.Style("muted")
					if strings.HasPrefix(info, "Last attempt:") {
						style = s.deps.theme.Style("errorText")
					} else if !strings.HasPrefix(info, "Estimate:") && !strings.HasPrefix(info, "Next:") && !strings.HasPrefix(info, "Create disabled —") {
						continue
					}
					styled := style.Render(info)
					prefix, _, _ := strings.Cut(styled, info)
					if styled == stripANSIstr(styled) || !strings.Contains(frame, prefix+info) {
						t.Fatalf("supporting information lost its semantic style: %q", info)
					}
				}
				focused := []string{"┃ Name:", "┃ Prompt", "┃ When (e.g.", "▶ Timezone", "▶ [x] Read-only", "▶ [Cancel]", "▶ [Create"}[s.form.focusIdx]
				if s.form.focusIdx == scheduleReadOnly && s.form.mutating {
					focused = "▶ [ ] Read-only"
				}
				if !strings.Contains(plain, focused) {
					t.Fatalf("%s hid focused field %q in root frame:\n%s", state, focused, plain)
				}
				if strings.Count(stripANSIstr(s.form.prompt.View()), "\n") < 2 {
					t.Fatal("visible prompt lost its two-row viewport")
				}
				if s.form.focusIdx >= scheduleReadOnly && size[1] >= 30 {
					for _, field := range []string{"Name:", "Prompt", "When (e.g.", "Timezone", "Read-only"} {
						if !strings.Contains(plain, field) {
							t.Fatalf("review viewport lost %s", field)
						}
					}
				}
				if s.pendingAction != "" && (!strings.Contains(plain, "operation pending") || strings.Contains(plain, "tab: next")) {
					t.Fatal("pending form lost feedback or advertised frozen edits")
				}
				if m.prompt.Value() != "" || m.sessionID != "active-chat" || len(m.stagedMedia)+len(m.stagedPastes) != 0 {
					t.Fatal("form input changed the root draft, session or staging")
				}
				t.Logf("receipt %s focus=%d window=%dx%d surface=%dx%d\n%s\nraw frame=%q", state, s.form.focusIdx, size[0], size[1], m.width, m.vp.Height(), plain, frame)
				return plain
			}
			replaceLine := func(value string) {
				t.Helper()
				m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
				m = scheduleKey(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
				m = schedulePaste(m, value)
			}
			checkFocuses := func(state, reason string) {
				t.Helper()
				for focus := scheduleName; focus < scheduleControlCount; focus++ {
					m = scheduleFocus(t, m, s, focus)
					body := snapshot(state)
					if reason != "" && !strings.Contains(body, reason) {
						t.Fatalf("%s focus %d hid validation/server reason", state, focus)
					}
					if state != "invalid" && (!strings.Contains(body, "Next: 2027-01-01 09:00:00 -08:00") || !strings.Contains(body, "America/Los_Angeles")) {
						t.Fatal("normal terminal hid the dated, zoned preview")
					}
					switch focus {
					case schedulePrompt:
						if !strings.Contains(body, "run history") || !strings.Contains(stripANSIstr(s.form.prompt.View()), "Prompt") {
							t.Fatal("prompt lost run policy or internal scrolling")
						}
					case scheduleTiming:
						if !strings.Contains(body, "every day at 9am") || !strings.Contains(body, "cron") {
							t.Fatal("populated timing lost its field guidance")
						}
					case scheduleTimezone:
						if !strings.Contains(body, "Timezone: search") || !strings.Contains(body, "America/Los_Angeles") || !strings.Contains(body, "accept timezone") {
							t.Fatal("timezone field/suggestions/guidance inaccessible")
						}
					case scheduleReadOnly:
						for _, text := range []string{"server default permission mode", "deployment policy", "server default", "plan model"} {
							if !strings.Contains(body, text) {
								t.Fatalf("read-only focus lost policy: %s", text)
							}
						}
					case scheduleCancel:
						if !strings.Contains(body, "Chat can manage schedules") || !strings.Contains(body, "weekday") {
							t.Fatal("chat alternative is undiscoverable")
						}
					}
				}
			}
			snapshot("empty")
			m = schedulePaste(m, "review")
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			prompt := strings.Repeat("Review without edits\n", 12)
			m = schedulePaste(m, prompt)
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			prompt += "\n"
			if s.form.prompt.GetValue().(string) != prompt || s.form.focusIdx != schedulePrompt {
				t.Fatal("prompt cannot be meaningfully edited at normal terminal size")
			}
			for _, hidden := range [][2]int{{38, 24}, {80, 8}, {0, 0}} {
				m = applyAll(m, tea.WindowSizeMsg{Width: hidden[0], Height: hidden[1]})
				m = schedulePaste(m, "hidden edit")
				m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
				if !s.compact || s.form.focusIdx != schedulePrompt || s.form.prompt.GetValue().(string) != prompt || fs.createCalls != 0 {
					t.Fatal("hidden field viewport accepted paste/actions or lost its draft")
				}
			}
			m = applyAll(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			snapshot("resize-restored-prompt")
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			m = schedulePaste(m, "every weekday at 9am")
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			replaceLine("Los Angeles")
			if !strings.Contains(snapshot("timezone-query"), "America/Los_Angeles") || s.form.timezone.Value() != "" {
				t.Fatal("city query did not offer a usable unaccepted suggestion")
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if s.form.timezone.Value() != "America/Los_Angeles" || s.form.timezone.Open() || fs.createCalls != 0 {
				t.Fatal("timezone acceptance was not usable at normal terminal size")
			}
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if !s.form.mutating || fs.createCalls != 0 || s.form.focusIdx != scheduleReadOnly {
				t.Fatal("checkbox cannot toggle independently of submit")
			}
			checkFocuses("valid", "Estimate:")
			// Cancel abandons all values; reopening starts a new independent draft.
			m = scheduleFocus(t, m, s, scheduleCancel)
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if s.view != schedulePanel || fs.createCalls != 0 || (s.form.name != nil || s.form.prompt != nil || s.form.trigger != nil || s.form.styles != nil) {
				t.Fatal("Cancel submitted or retained the abandoned form")
			}
			m = scheduleRune(t, m, "c")
			if s.form.focusIdx != scheduleName || s.form.name.GetValue().(string) != "" || s.form.prompt.GetValue().(string) != "" || s.form.trigger.GetValue().(string) != "" || s.form.timezone.Value() != "UTC" || s.form.mutating {
				t.Fatal("reopening resurrected an abandoned draft/focus")
			}
			m = schedulePaste(m, "review")
			m = scheduleFocus(t, m, s, schedulePrompt)
			m = schedulePaste(m, prompt)
			m = scheduleFocus(t, m, s, scheduleTimezone)
			replaceLine("America/Los_Angeles")
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m = scheduleFocus(t, m, s, scheduleReadOnly)
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			m = scheduleFocus(t, m, s, scheduleTiming)
			replaceLine("daily at 25:99")
			checkFocuses("invalid", "Create disabled — timing")
			m = scheduleFocus(t, m, s, scheduleSubmit)
			m = scheduleKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if fs.createCalls != 0 || s.form.trigger.GetValue().(string) != "daily at 25:99" || s.form.focusIdx != scheduleSubmit {
				t.Fatal("invalid Create escaped validation or lost the editable draft")
			}
			m = scheduleFocus(t, m, s, scheduleTiming)
			replaceLine("every weekday at 9am")
			m = scheduleFocus(t, m, s, scheduleSubmit)
			fs.err = errors.New("Choose another name: review exists. " + strings.Repeat("server detail ", 40))
			mm, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			snapshot("pending")
			for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: tea.KeyTab}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.PasteMsg{Content: "hidden edit"}} {
				var duplicate tea.Cmd
				mm, duplicate = m.Update(msg)
				m = mm.(Model)
				if duplicate != nil || s.form.focusIdx != scheduleSubmit || s.form.prompt.GetValue().(string) != prompt {
					t.Fatal("pending form accepted edits/focus/duplicate operation")
				}
			}
			m = feedCmd(t, m, submit)
			if fs.createCalls != 1 || s.view != scheduleCreate || s.form.focusIdx != scheduleSubmit {
				t.Fatal("RPC rejection lost the creator/focus")
			}
			checkFocuses("server-error", "Choose another name")
			m = scheduleFocus(t, m, s, scheduleName)
			replaceLine("review-2")
			fs.err = nil
			m = scheduleFocus(t, m, s, scheduleSubmit)
			mm, submit = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			snapshot("retry-pending")
			m = feedCmd(t, m, submit)
			if fs.createCalls != 2 || fs.created.Name != "review-2" || fs.created.Prompt != prompt || fs.created.Trigger.Cron != "0 9 * * 1-5" || fs.created.Timezone != "America/Los_Angeles" || !fs.created.Mutating || fs.created.Mode != "" || s.view != schedulePanel || !strings.Contains(s.notice, "next run (server)") {
				t.Fatalf("correction/retry did not send the chosen spec once: %+v", fs.created)
			}
		})
	}
}
