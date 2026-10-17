package ui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func submitDelivery(t *testing.T, m Model, text string) Model {
	t.Helper()
	m.prompt.Rewrite(text)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runBatchLeaves(cmd)
	return mm.(Model)
}

func TestPromptDeliveryExplicitRouting(t *testing.T) {
	for _, name := range []string{"queue", "steer"} {
		for _, running := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/idle", true: "/running"}[running], func(t *testing.T) {
				m, conv := newSteerModel(t, true)
				if running {
					m = startRunning(t, m, "first")
				}
				m = submitDelivery(t, m, "/"+name+" /help")
				if running && name == "queue" {
					if !reflect.DeepEqual(m.queued, []string{"/help"}) || len(steerTexts(conv.send)) != 0 {
						t.Fatalf("queue=%q steer=%q", m.queued, steerTexts(conv.send))
					}
					mm, cmd := m.Update(client.ResultMsg{Stop: "end_turn"})
					m = mm.(Model)
					runBatchLeaves(cmd)
				}
				if running && name == "steer" {
					if !reflect.DeepEqual(steerTexts(conv.send), []string{"/help"}) || m.steer.Sends[0].Draft != "/help" {
						t.Fatalf("steer=%q state=%#v", steerTexts(conv.send), m.steer)
					}
					if m.steer.Text != "/help" {
						t.Fatalf("pending preview contains prefix: %q", m.steer.Text)
					}
					m = applyAll(m, client.SteerEchoMsg{Text: "/help", MessageID: m.steer.watermarkID()})
				} else {
					got := promptTexts(conv.send)
					if len(got) == 0 || got[len(got)-1] != "/help" {
						t.Fatalf("prompts=%q", got)
					}
					if m.modal != nil {
						t.Fatal("payload opened local help")
					}
				}
				blocks := m.conv.testBlocks()
				if got := testCardText(blocks[len(blocks)-1]); got != "/help" {
					t.Fatalf("transcript payload = %q", got)
				}
			})
		}
	}
}

func TestPromptDeliveryPendingQueueOwnsEnter(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m = startRunning(t, m, "first")
	m = submitDelivery(t, m, "/queue second")
	m = submitDelivery(t, m, "third")
	if !reflect.DeepEqual(m.queued, []string{"second", "third"}) || len(steerTexts(conv.send)) != 0 {
		t.Fatalf("queue=%q steer=%q", m.queued, steerTexts(conv.send))
	}
	m = submitDelivery(t, m, "/steer blocked")
	if m.prompt.Value() != "/steer blocked" || !strings.Contains(stripANSIstr(m.statusMsg), "pending") || len(steerTexts(conv.send)) != 0 {
		t.Fatalf("draft=%q status=%q steer=%q", m.prompt.Value(), m.statusMsg, steerTexts(conv.send))
	}
	m.prompt.Reset()
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if m.prompt.Value() != "/queue second\n\nthird" {
		t.Fatalf("pullback=%q", m.prompt.Value())
	}
	m = submitDelivery(t, m, m.prompt.Value())
	if !reflect.DeepEqual(m.queued, []string{"second\n\nthird"}) {
		t.Fatalf("resend=%q", m.queued)
	}
}

func TestPromptDeliveryMergedMediaPullback(t *testing.T) {
	for _, kind := range []string{"queue", "steer"} {
		t.Run(kind, func(t *testing.T) {
			m, conv := newSteerModel(t, true)
			m.caps.Image = true
			m = startRunning(t, m, "first")
			for i := 1; i <= 2; i++ {
				image, paste := fmt.Sprintf("[Image #%d]", i), fmt.Sprintf("[Paste #%d]", i)
				m.stagedMedia = map[string]stagedAttachment{image: {mime: "image/png", data: []byte{byte(i)}}}
				m.stagedPastes = map[string]string{paste: fmt.Sprintf("body %d", i)}
				m.nextMediaN, m.nextPasteN = i, i
				m = submitDelivery(t, m, "/"+kind+" "+image+" "+paste)
			}
			wantDraft := "/" + kind + " [Image #1] [Paste #1]\n\n[Image #2] [Paste #2]"
			for range 2 {
				m = pressUp(t, m)
				if m.prompt.Value() != wantDraft || len(m.stagedMedia) != 2 || len(m.stagedPastes) != 2 {
					t.Fatalf("pullback: draft=%q staged=%#v pastes=%#v", m.prompt.Value(), m.stagedMedia, m.stagedPastes)
				}
				m = submitDelivery(t, m, m.prompt.Value())
				if kind == "queue" {
					if !reflect.DeepEqual(m.queued, []string{"body 1\n\nbody 2"}) || len(m.queuedMedia.Parts) != 2 {
						t.Fatalf("queue=%q media=%#v", m.queued, m.queuedMedia)
					}
				} else if len(m.steer.Sends) != 1 || m.steer.Text != "body 1\n\nbody 2" || len(m.steer.Sends[0].Media.Parts) != 2 {
					t.Fatalf("steer=%#v", m.steer)
				}
			}
			if kind == "queue" {
				mm, cmd := m.Update(client.ResultMsg{Stop: "end_turn"})
				m = mm.(Model)
				runBatchLeaves(cmd)
			} else {
				if steerCancelCount(conv.send) != 2 {
					t.Fatalf("cancel count=%d", steerCancelCount(conv.send))
				}
			}
			last := conv.send.frames()[len(conv.send.frames())-1]
			if kind == "queue" {
				p := last.GetPrompt()
				if p.GetText() != "body 1\n\nbody 2" || len(p.GetParts()) != 2 || string(p.GetParts()[0].GetData()) != "\x01" || string(p.GetParts()[1].GetData()) != "\x02" {
					t.Fatalf("prompt=%v", p)
				}
			} else {
				s := last.GetSteer()
				if s.GetText() != "body 1\n\nbody 2" || len(s.GetParts()) != 2 || string(s.GetParts()[0].GetData()) != "\x01" || string(s.GetParts()[1].GetData()) != "\x02" {
					t.Fatalf("steer=%v", s)
				}
			}
		})
	}
}

func TestPromptDeliveryPendingSteerExclusionEndsWithPendingWork(t *testing.T) {
	for _, outcome := range []string{"drained", "pulled-back", "retracted", "promoted"} {
		t.Run(outcome, func(t *testing.T) {
			m, conv := newSteerModel(t, true)
			m = startRunning(t, m, "first")
			m = submitDelivery(t, m, "/steer second")
			m = submitDelivery(t, m, "third")
			m = submitDelivery(t, m, "/queue blocked [Image #1]")
			if m.prompt.Value() != "/queue blocked [Image #1]" || len(m.queued) != 0 || len(steerTexts(conv.send)) != 2 || !strings.Contains(stripANSIstr(m.statusMsg), "pending") {
				t.Fatalf("queue=%q draft=%q status=%q", m.queued, m.prompt.Value(), m.statusMsg)
			}
			m.prompt.Reset()
			switch outcome {
			case "drained":
				m = applyAll(m, client.SteerEchoMsg{Text: "second\n\nthird", MessageID: "steer-0002"})
			case "pulled-back":
				m = pressUp(t, m)
			case "retracted":
				m = applyAll(m, client.SteerOutcomeMsg{Outcome: client.SteerRetracted, MessageID: "steer-0002"})
			case "promoted":
				m = applyAll(m, client.SteerOutcomeMsg{Outcome: client.SteerTooLate, Promoted: true, MessageID: "steer-0002"})
			}
			m = submitDelivery(t, m, "/queue allowed")
			if !reflect.DeepEqual(m.queued, []string{"allowed"}) {
				t.Fatalf("queue still excluded: %q", m.queued)
			}
			m = pressUp(t, m)
			if m.prompt.Value() != "/queue allowed" {
				t.Fatalf("pulled terminal steer instead of queue: %q", m.prompt.Value())
			}
			m = submitDelivery(t, m, "/steer fresh")
			if len(m.queued) != 0 || len(m.steer.Sends) != 1 || m.steer.Text != "fresh" {
				t.Fatalf("new steer inherited terminal state: %#v", m.steer)
			}
		})
	}
}

func TestPromptDeliveryRecoveryKeepsPayloadSemantics(t *testing.T) {
	for _, kind := range []string{"queue", "steer"} {
		t.Run(kind, func(t *testing.T) {
			m, conv := newSteerModel(t, false)
			m.startupAdopted = true
			m = submitDelivery(t, m, "/"+kind+" /queue /help")
			if m.startupRetryPrompt != "/"+kind+" /queue /help" {
				t.Fatalf("startup retry=%q", m.startupRetryPrompt)
			}
			m.startupAdopted = false
			m.startupFirstPromptPending = false
			m = applyAll(m, client.StreamErrMsg{Err: errors.New("connection lost")})
			if m.prompt.Value() != "/"+kind+" /queue /help" {
				t.Fatalf("recovery=%q", m.prompt.Value())
			}
			m = submitDelivery(t, m, m.prompt.Value())
			if got := promptTexts(conv.send); !reflect.DeepEqual(got, []string{"/queue /help", "/queue /help"}) {
				t.Fatalf("recovery parsed payload: %q", got)
			}
		})
	}
}

func TestPromptDeliveryModeGateAndPausedDrainKeepPayload(t *testing.T) {
	for _, stop := range []string{"end_turn", "cancelled", "error"} {
		t.Run(stop, func(t *testing.T) {
			m, conv := newSteerModel(t, true)
			m = startRunning(t, m, "first")
			m = submitDelivery(t, m, "/queue /help")
			m = submitDelivery(t, m, "/queue /queue nested")
			m.pendingMode = "plan"
			mm, cmd := m.Update(client.ResultMsg{Stop: stop})
			m = mm.(Model)
			runBatchLeaves(cmd)
			if len(promptTexts(conv.send)) != 1 {
				t.Fatal("drain bypassed pause/mode gate")
			}
			if stop != "end_turn" {
				if m.queuePaused != stop || len(m.queued) != 2 {
					t.Fatalf("pause=%q queue=%q", m.queuePaused, m.queued)
				}
				m = submitDelivery(t, m, "")
			}
			want := "/queue /help\n\n/queue nested"
			if m.prompt.Value() != want {
				t.Fatalf("mode draft=%q", m.prompt.Value())
			}
			m = submitDelivery(t, m, m.prompt.Value())
			if len(promptTexts(conv.send)) != 1 || m.prompt.Value() != want {
				t.Fatal("Enter bypassed pending mode")
			}
			m = applyAll(m, client.ModeChangedMsg{SessionID: m.sessionID, Requested: "plan", Mode: "plan"})
			m = submitDelivery(t, m, m.prompt.Value())
			if got := promptTexts(conv.send); !reflect.DeepEqual(got, []string{"first", "/help\n\n/queue nested"}) {
				t.Fatalf("mode send=%q", got)
			}
		})
	}
}

func TestPromptDeliveryEmptyAndInvalidMediaPreserved(t *testing.T) {
	for _, kind := range []string{"queue", "steer"} {
		for _, running := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/running=%t", kind, running), func(t *testing.T) {
				m, conv := newSteerModel(t, true)
				if running {
					m = startRunning(t, m, "first")
				}
				frames := len(conv.send.frames())
				m = submitDelivery(t, m, "/"+kind+" ")
				if m.prompt.Value() != "/"+kind+" " || !strings.Contains(stripANSIstr(m.statusMsg), "empty") {
					t.Fatalf("draft=%q status=%q", m.prompt.Value(), m.statusMsg)
				}
				m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("pixels")}}
				m.stagedPastes = map[string]string{"[Paste #1]": "kept"}
				draft := "/" + kind + " [Image #1] [Paste #1]"
				m = submitDelivery(t, m, draft)
				if m.prompt.Value() != draft || len(m.stagedMedia) != 1 || len(m.stagedPastes) != 1 || len(conv.send.frames()) != frames {
					t.Fatalf("invalid media consumed draft: %q", m.prompt.Value())
				}
				m.caps.Image = true
				m.stagedPastes = nil
				m = submitDelivery(t, m, "/"+kind+" [Image #1]")
				if running && kind == "queue" {
					if len(m.queued) != 1 || m.queued[0] != "" || len(m.queuedMedia.Parts) != 1 {
						t.Fatalf("media-only queue=%q media=%#v", m.queued, m.queuedMedia)
					}
				} else {
					last := conv.send.frames()[len(conv.send.frames())-1]
					if running {
						if s := last.GetSteer(); s.GetText() != "" || len(s.GetParts()) != 1 {
							t.Fatalf("media-only steer=%v", s)
						}
					} else {
						if p := last.GetPrompt(); p.GetText() != "" || len(p.GetParts()) != 1 {
							t.Fatalf("media-only prompt=%v", p)
						}
					}
				}
			})
		}
	}
}

func TestPromptDeliveryPaletteCompletesArguments(t *testing.T) {
	for _, kind := range []string{"queue", "steer"} {
		t.Run(kind, func(t *testing.T) {
			m := newPaletteModel(t, &fakeCommander{cmds: []client.Command{{Name: kind, Description: "workspace collision"}}})
			m = typeText(t, m, "/"+kind)
			if len(m.palette.filtered) != 1 || !m.palette.filtered[0].Builtin {
				t.Fatalf("builtin not reserved: %#v", m.palette.filtered)
			}
			m0, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = m0.(Model)
			runBatchLeaves(cmd)
			if m.prompt.Value() != "/"+kind+" " || m.palette.open || m.phase != phaseIdle {
				t.Fatalf("completion executed instead: draft=%q phase=%v", m.prompt.Value(), m.phase)
			}
		})
	}
}

func TestPromptDeliveryPulledDraftOutlivesRun(t *testing.T) {
	for _, kind := range []string{"queue", "steer"} {
		t.Run(kind, func(t *testing.T) {
			m, conv := newSteerModel(t, true)
			m = startRunning(t, m, "first")
			m = submitDelivery(t, m, "/"+kind+" /help")
			m = pressUp(t, m)
			m = applyAll(m, client.ResultMsg{Stop: "end_turn"})
			m = submitDelivery(t, m, m.prompt.Value())
			if got := promptTexts(conv.send); !reflect.DeepEqual(got, []string{"first", "/help"}) {
				t.Fatalf("pulled draft was not an ordinary payload: %q", got)
			}
			m = submitDelivery(t, m, "ordinary default")
			if got := steerTexts(conv.send); got[len(got)-1] != "ordinary default" {
				t.Fatalf("old delivery kind survived its pending work: %q", got)
			}
		})
	}
}

func TestPromptDeliveryConflictRetainsMedia(t *testing.T) {
	for _, kind := range []string{"queue", "steer"} {
		t.Run(kind, func(t *testing.T) {
			m, conv := newSteerModel(t, true)
			m = startRunning(t, m, "first")
			opposite := "queue"
			if kind == "queue" {
				opposite = "steer"
			}
			m = submitDelivery(t, m, "/"+opposite+" pending")
			m.caps.Image = true
			m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("pixels")}}
			m.stagedPastes = map[string]string{"[Paste #1]": "kept"}
			draft := "/" + kind + " [Image #1] [Paste #1]"
			frames := len(conv.send.frames())
			m = submitDelivery(t, m, draft)
			if m.prompt.Value() != draft || string(m.stagedMedia["[Image #1]"].data) != "pixels" || m.stagedPastes["[Paste #1]"] != "kept" || len(conv.send.frames()) != frames || !strings.Contains(stripANSIstr(m.statusMsg), "pending") {
				t.Fatalf("conflict mutated draft: %q media=%#v pastes=%#v status=%q", m.prompt.Value(), m.stagedMedia, m.stagedPastes, m.statusMsg)
			}
		})
	}
}

func TestPromptDeliveryPreparedMediaSurvivesKindChanges(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m.caps.Image = true
	m = startRunning(t, m, "first")
	part, desc, err := client.StageClipboardImage("image/png", []byte("snapshot"), m.caps)
	if err != nil {
		t.Fatal(err)
	}
	m.pendingPromptMedia.Parts = append(m.pendingPromptMedia.Parts, part)
	m.pendingPromptMedia.Descriptors = append(m.pendingPromptMedia.Descriptors, desc)
	for _, kind := range []string{"queue", "steer", "queue"} {
		m = submitDelivery(t, m, "/"+kind+" /help")
		m = pressUp(t, m)
		if len(m.pendingPromptMedia.Parts) != 1 || string(m.pendingPromptMedia.Parts[0].GetData()) != "snapshot" {
			t.Fatalf("%s pullback media=%#v", kind, m.pendingPromptMedia)
		}
	}
	m = submitDelivery(t, m, m.prompt.Value())
	mm, cmd := m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(cmd)
	last := conv.send.frames()[len(conv.send.frames())-1].GetPrompt()
	if last.GetText() != "/help" || len(last.GetParts()) != 1 || string(last.GetParts()[0].GetData()) != "snapshot" {
		t.Fatalf("prepared media lost or duplicated: %v", last)
	}
}

func TestPromptDeliveryFailedSteerAckKeepsPayloadSemantics(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m = startRunning(t, m, "first")
	m = submitDelivery(t, m, "/steer /help")
	m = applyAll(m, client.SteerOutcomeMsg{Outcome: client.SteerTooLate, MessageID: "steer-0001"})
	if m.prompt.Value() != "/steer /help" {
		t.Fatalf("failed ack restored command instead of payload: %q", m.prompt.Value())
	}
	m = submitDelivery(t, m, m.prompt.Value())
	if got := steerTexts(conv.send); !reflect.DeepEqual(got, []string{"/help", "/help"}) {
		t.Fatalf("retry=%q", got)
	}
}

func TestPromptDeliveryFailedSteerAckMergesCurrentDraft(t *testing.T) {
	for _, tc := range []struct {
		name, current, payload string
	}{
		{"plain", "new draft", "new draft"},
		{"local command", "/help", "/help"},
		{"queue prefix", "/queue new draft", "new draft"},
		{"steer prefix with nested payload", "/steer /queue /help", "/queue /help"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, conv := newSteerModel(t, true)
			m.caps.Image = true
			m = startRunning(t, m, "first")
			m = typeText(t, m, "/steer /queue /help ")
			m = applyAll(m, clipboardResultMsg{mime: "image/png", data: []byte("failed-image")})
			m = submitDelivery(t, m, m.prompt.Value())
			m = typeText(t, m, tc.current+" ")
			m = applyAll(m, clipboardResultMsg{mime: "image/png", data: []byte("current-image")})
			m = applyAll(m, client.SteerOutcomeMsg{Outcome: client.SteerTooLate, MessageID: "steer-0001"})
			wantDraft := "/steer " + tc.payload + " [Image #2]\n\n/queue /help [Image #1]"
			if m.prompt.Value() != wantDraft {
				t.Errorf("recovered draft = %q, want %q", m.prompt.Value(), wantDraft)
			}
			if string(m.stagedMedia["[Image #1]"].data) != "failed-image" || string(m.stagedMedia["[Image #2]"].data) != "current-image" {
				t.Fatalf("recovery lost media: %#v", m.stagedMedia)
			}
			m = submitDelivery(t, m, m.prompt.Value())
			want := []string{"/queue /help", tc.payload + "\n\n/queue /help"}
			if got := steerTexts(conv.send); !reflect.DeepEqual(got, want) {
				t.Fatalf("recovered wire payload = %q, want %q", got, want)
			}
			if m.modal != nil || len(m.queued) != 0 {
				t.Fatal("recovery dispatched a local command or queued instead of steering")
			}
			frames := conv.send.frames()
			parts := frames[len(frames)-1].GetSteer().GetParts()
			if len(parts) != 2 || string(parts[0].GetData()) != "failed-image" || string(parts[1].GetData()) != "current-image" {
				t.Fatalf("recovered wire media = %v", parts)
			}
		})
	}
}

func TestPromptDeliveryQueueRealMediaEventsPreserveMarkerOwnership(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m.caps.Image = true
	m = startRunning(t, m, "first")
	pastes := []string{strings.Repeat("first paste", pasteCharThreshold), strings.Repeat("second paste", pasteCharThreshold)}
	images := []string{"first-image", "second-image"}
	var drafts []string
	for i := range pastes {
		m = typeText(t, m, "/queue ")
		m = applyAll(m, clipboardResultMsg{mime: "image/png", data: []byte(images[i])}, pasteMsg(pastes[i]))
		want := fmt.Sprintf("/queue [Image #%d] [Pasted text #%d]", i+1, i+1)
		if got := strings.TrimSpace(m.prompt.Value()); got != want {
			t.Fatalf("real media events reused a marker: draft = %q, want %q", got, want)
		}
		drafts = append(drafts, strings.TrimPrefix(want, "/queue "))
		mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = mm.(Model)
		runBatchLeaves(cmd)
	}
	if len(m.queued) != 2 || len(steerTexts(conv.send)) != 0 {
		t.Fatalf("explicit queue used wrong delivery: queued=%q steer=%q", m.queued, steerTexts(conv.send))
	}
	m = pressUp(t, m)
	if got, want := m.prompt.Value(), "/queue "+strings.Join(drafts, queueMergeSep); got != want {
		t.Fatalf("pullback = %q, want %q", got, want)
	}
	if len(m.stagedMedia) != 2 || len(m.stagedPastes) != 2 {
		t.Fatalf("pullback lost distinct marker ownership: media=%#v pastes=%#v", m.stagedMedia, m.stagedPastes)
	}
	for i := range pastes {
		if got := string(m.stagedMedia[fmt.Sprintf("[Image #%d]", i+1)].data); got != images[i] {
			t.Fatalf("image #%d = %q, want %q", i+1, got, images[i])
		}
		if got := m.stagedPastes[fmt.Sprintf("[Pasted text #%d]", i+1)]; got != pastes[i] {
			t.Fatalf("paste #%d does not own its original bytes", i+1)
		}
	}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	wantText := strings.Join(pastes, queueMergeSep)
	if !reflect.DeepEqual(m.queued, []string{wantText}) || len(m.queuedMedia.Parts) != 2 {
		t.Fatalf("resend did not merge into one payload: queue count=%d media count=%d", len(m.queued), len(m.queuedMedia.Parts))
	}
	mm, cmd = m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if got := promptTexts(conv.send); !reflect.DeepEqual(got, []string{"first", wantText}) {
		t.Fatal("merged prompt wire text did not preserve both paste payloads exactly")
	}
	frames := conv.send.frames()
	parts := frames[len(frames)-1].GetPrompt().GetParts()
	if len(parts) != 2 || string(parts[0].GetData()) != images[0] || string(parts[1].GetData()) != images[1] {
		t.Fatalf("merged prompt wire media = %v", parts)
	}
}

func TestPromptDeliveryAdoptedStartupRetryKeepsPayloadSemantics(t *testing.T) {
	for _, kind := range []string{"queue", "steer"} {
		t.Run(kind, func(t *testing.T) {
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
			draft := "/" + kind + " /queue /help"
			m := startupResumeUI(t, conv, draft, "completed")
			mm, cmd := m.Update(startupResumeReadyMsg{})
			m = mm.(Model)
			runStartupCommands(cmd)
			if !m.startupAdopted || !m.startupFirstPromptPending || m.phase != phaseRunning {
				t.Fatalf("initial adopted run not armed: adopted=%t pending=%t phase=%v", m.startupAdopted, m.startupFirstPromptPending, m.phase)
			}
			m = applyAll(m, client.StreamErrMsg{Err: errors.New("startup transport failure")})
			if m.phase != phaseReplay || !m.startupRunEntryFailed || m.modal == nil || m.prompt.Focused() || m.prompt.Value() != draft {
				t.Fatalf("did not enter startup retry UI: phase=%v failed=%t modal=%T draft=%q", m.phase, m.startupRunEntryFailed, m.modal, m.prompt.Value())
			}
			mm, cmd = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
			m = mm.(Model)
			runStartupCommands(cmd)
			if m.phase != phaseRunning || !m.startupFirstPromptPending || m.startupRunEntryFailed || m.modal != nil || len(m.queued) != 0 {
				t.Fatalf("retry did not start ordinary run: phase=%v pending=%t failed=%t modal=%T queue=%q", m.phase, m.startupFirstPromptPending, m.startupRunEntryFailed, m.modal, m.queued)
			}
			if got := promptTexts(conv.send); !reflect.DeepEqual(got, []string{"/queue /help", "/queue /help"}) {
				t.Fatalf("startup retry interpreted nested payload: %q", got)
			}
			frames := conv.send.frames()
			if len(frames) != 2 || frames[1].GetPrompt().GetSessionId() != "existing" || conv.createCount != 0 {
				t.Fatalf("retry did not reuse adopted session: frames=%v creates=%d", frames, conv.createCount)
			}
			blocks := m.conv.testBlocks()
			if got := testCardText(blocks[len(blocks)-1]); got != "/queue /help" {
				t.Fatalf("retry transcript payload = %q", got)
			}
		})
	}
}

func TestPromptDeliveryErrorsVisibleDuringRun(t *testing.T) {
	for _, tc := range []struct {
		name, pending, rejected, feedback string
		steerCap                          bool
	}{
		{"queue conflicts with steer", "/steer pending", "/queue rejected", "pending steers must drain or be pulled back before queueing", true},
		{"steer conflicts with queue", "/queue pending", "/steer rejected", "pending queued prompts must drain or be pulled back before steering", true},
		{"steer unavailable", "", "/steer rejected", "steering is unavailable for this active run", false},
		{"empty instruction", "", "/queue ", "prompt is empty; add text or attached content", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, conv := newSteerModel(t, tc.steerCap)
			m = startRunning(t, m, "first")
			if tc.pending != "" {
				m = submitDelivery(t, m, tc.pending)
			}
			m.activeTool = "Subagent"
			before := len(conv.send.frames())
			m.prompt.Rewrite(tc.rejected)
			mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			runBatchLeaves(cmd)
			if m.phase != phaseRunning || m.prompt.Value() != tc.rejected || len(conv.send.frames()) != before {
				t.Fatalf("rejection changed run, draft or sent frames: phase=%v draft=%q frames=%d", m.phase, m.prompt.Value(), len(conv.send.frames()))
			}
			if got := stripANSIstr(m.View().Content); !strings.Contains(got, tc.feedback) {
				t.Fatalf("rejection must be visible while the footer shows tool activity; missing %q:\n%s", tc.feedback, got)
			}
		})
	}
}

func TestPromptDeliverySteerUnavailableRetainsDraft(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "missing-stream"}[missing], func(t *testing.T) {
			m, conv := newSteerModel(t, missing)
			m = startRunning(t, m, "first")
			if missing {
				m.stream = nil
			}
			m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("pixels")}}
			m.stagedPastes = map[string]string{"[Paste #1]": "kept"}
			draft := "/steer inspect [Image #1] [Paste #1]"
			m = submitDelivery(t, m, draft)
			if m.prompt.Value() != draft || len(m.stagedMedia) != 1 || len(m.stagedPastes) != 1 || len(m.queued) != 0 || len(steerTexts(conv.send)) != 0 || !strings.Contains(stripANSIstr(m.statusMsg), "unavailable") {
				t.Fatalf("draft=%q queue=%q status=%q", m.prompt.Value(), m.queued, m.statusMsg)
			}
		})
	}
}
