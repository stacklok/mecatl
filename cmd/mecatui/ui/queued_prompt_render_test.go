package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestPendingPromptsSeparateHeadingAndBody(t *testing.T) {
	for _, width := range []int{1, 10, 30, 60, 80, 100, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.width = width
			text := "first\n漢字\t" + strings.Repeat("界", 1000)
			m.queued = []string{text, "second", "third", "fourth"}
			m.steer = &steerState{Text: text, Sends: []steerQueuedSend{{Text: text}}}
			cards := map[string]string{"queue": m.renderQueue()}
			m.queuePaused = stopError
			cards["paused"] = m.renderQueue()
			for _, phase := range []steerPhase{steerPending, steerSent, steerPromoted, steerRetracted} {
				m.steer.Phase = phase
				cards[fmt.Sprintf("steer-%d", phase)] = m.renderSteer()
			}
			for state, card := range cards {
				if lipgloss.Width(card) > width || lipgloss.Height(card) > queuePreviewLimit+2 {
					t.Errorf("%s must stay compact and fit %d cells, got %dx%d: %q", state, width, lipgloss.Width(card), lipgloss.Height(card), card)
				}
				lines := strings.Split(ansi.Strip(card), "\n")
				if len(lines) < 2 || strings.Contains(lines[0], "first") {
					t.Errorf("%s heading and body must be separate: %q", state, card)
				}
				if width >= 30 && (!strings.HasPrefix(lines[0], " ▶ you · ") || lines[1] != " │ first" || !strings.HasPrefix(lines[2], " │ 漢字 ") || !strings.HasSuffix(lines[2], "…")) {
					t.Errorf("%s must look like a user prompt with a bounded body preview: %q", state, card)
				}
			}
			if width >= 80 {
				for state, hints := range map[string]string{
					"queue": "↑ edit", "paused": "enter sends · ↑ edit · esc clears",
					fmt.Sprintf("steer-%d", steerSent): "↑ edit · esc cancel",
				} {
					heading := strings.Split(ansi.Strip(cards[state]), "\n")[0]
					if !strings.HasSuffix(heading, hints) || ansi.StringWidth(heading) != width {
						t.Errorf("%s controls must align to the heading's right edge: %q", state, heading)
					}
				}
				if card := ansi.Strip(cards["queue"]); !strings.Contains(card, "4 queued") || !strings.Contains(card, " │ second\n   +2 more") || strings.Contains(card, "third") || strings.Contains(card, "fourth") {
					t.Errorf("queue must retain count and cap visible previews: %q", card)
				}
			}
		})
	}
}

func TestPendingPromptsPreserveDirectMultilineInput(t *testing.T) {
	for _, kind := range []string{"queue", "steer"} {
		for _, text := range []string{"test 1\ntest 2", "test 1\n\ntest 2"} {
			t.Run(kind+"/"+fmt.Sprintf("%q", text), func(t *testing.T) {
				m, _ := newSteerModel(t, true)
				m = startRunning(t, m, "original")
				m = submitDelivery(t, m, "/"+kind+" "+text)
				want := " │ " + strings.ReplaceAll(text, "\n", "\n │ ")
				if got := ansi.Strip(m.View().Content); !strings.Contains(got, want) {
					t.Fatalf("pending preview must preserve line breaks %q:\n%s", want, got)
				}
			})
		}
	}
}

func TestPendingPromptsKeepSectionsAfterPullbackAndResend(t *testing.T) {
	for _, kind := range []string{"queue", "steer"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := newSteerModel(t, true)
			m = startRunning(t, m, "original")
			m = submitDelivery(t, m, "/"+kind+" test 1")
			m = submitDelivery(t, m, "test 2")
			for range 2 {
				m = pressUp(t, m)
				if got, want := m.prompt.Value(), "/"+kind+" test 1\n\ntest 2"; got != want {
					t.Fatalf("pullback = %q, want %q", got, want)
				}
				m = submitDelivery(t, m, m.prompt.Value())
				if kind == "queue" && (len(m.queued) != 1 || m.queued[0] != "test 1\n\ntest 2") {
					t.Fatalf("resend changed merged queue payload: %q", m.queued)
				}
				if got := ansi.Strip(m.View().Content); !strings.Contains(got, " │ test 1\n │ \n │ test 2") {
					t.Fatalf("resend must keep separate pending preview rows:\n%s", got)
				}
			}
		})
	}
}

func TestPendingPromptsHideUnavailableEditHints(t *testing.T) {
	m, _ := newQueueModel(t)
	m.queued = []string{"follow-up"}
	m.queuePaused = stopError
	m.steer = &steerState{Phase: steerSent, Sends: []steerQueuedSend{{Text: "follow-up"}}}
	m = typeText(t, m, "draft")
	for name, card := range map[string]string{"queue": m.renderQueue(), "steer": m.renderSteer()} {
		card = ansi.Strip(card)
		if strings.Contains(card, "↑ edit") || strings.Contains(card, "enter sends") || !strings.Contains(card, "esc") {
			t.Errorf("%s must show only actionable controls with a draft present: %q", name, card)
		}
	}
}

func TestPendingPromptPreviewBoundedAndSafe(t *testing.T) {
	m, _ := newQueueModel(t)
	head := strings.Repeat("h", queuePreviewBound)
	for _, steer := range []bool{false, true} {
		render := func(text string) string {
			if steer {
				m.steer = &steerState{Phase: steerSent, Text: text, Sends: []steerQueuedSend{{Text: text}}}
				return m.renderSteer()
			}
			m.queued = []string{text}
			return m.renderQueue()
		}
		if a, b := render(head+strings.Repeat("tail-a", 4000)), render(head+strings.Repeat("tail-b", 4000)); a != b {
			t.Errorf("steer=%v: preview depends on content past the bound", steer)
		}
		if a, b := render(head+strings.Repeat("\ntail", 4000)), render(head); a != b {
			t.Errorf("steer=%v: preview depends on line breaks past the bound", steer)
		}
		body := strings.Split(ansi.Strip(render("hello\x1b[2J\x07\u009b\u2028\u2029\nworld")), "\n")[1]
		if strings.ContainsAny(body, "\x1b\x07\u009b\u2028\u2029\n") || !strings.Contains(body, "hello[2J") {
			t.Errorf("steer=%v: preview must be terminal-safe single-line text: %q", steer, body)
		}
	}
}

func TestPendingPromptUsesTranscriptTheme(t *testing.T) {
	for _, th := range []theme.Theme{theme.NewRegistry().Default(), theme.Solar(), theme.New("custom", theme.AztecPalette())} {
		t.Run(th.Name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.deps.Theme = th
			m.queued = []string{"follow-up"}
			m.steer = &steerState{Phase: steerSent, Sends: []steerQueuedSend{{Text: "follow-up"}}}
			for _, card := range []string{m.renderQueue(), m.renderSteer()} {
				for _, want := range []string{
					th.Style("transcriptUserMarker").Render(theme.TranscriptUserGlyph),
					th.Style("transcriptUserHeading").Render(" you"),
					th.Style("transcriptUserRail").Render("│"),
					th.Style("transcriptUserText").Render("follow-up"),
				} {
					if !strings.Contains(card, want) {
						t.Errorf("pending prompt lacks transcript styling %q: %q", want, card)
					}
				}
			}
		})
	}
}

func TestPendingPromptLayout(t *testing.T) {
	for _, steer := range []bool{false, true} {
		m, _ := newSteerModel(t, steer)
		m = startRunning(t, m, "original")
		baseline := m.vp.Height()
		if steer {
			m = enqueueSteer(t, m, "follow-up\nwith more text")
			mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: m.steer.Text, MessageID: m.steer.watermarkID()})
			m = mm.(Model)
		} else {
			m = enqueue(t, m, "follow-up\nwith more text")
		}
		if got := m.vp.Height(); got != baseline-3 {
			t.Errorf("steer=%v: pending prompt must reserve a heading and two body rows, viewport went from %d to %d", steer, baseline, got)
		}
		m = applyAll(m, tea.WindowSizeMsg{Width: 60, Height: 30})
		if got := lipgloss.Height(m.View().Content); got > m.height {
			t.Errorf("steer=%v: resized frame height %d exceeds terminal height %d", steer, got, m.height)
		}
	}
}
