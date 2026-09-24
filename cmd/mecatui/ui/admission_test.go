package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func admissionError(t *testing.T) error {
	t.Helper()
	st, err := status.New(codes.Unavailable, "context_window_unavailable https://untrusted.invalid/token /tenant/private credential=secret --untrusted-override=1").WithDetails(&errdetails.ErrorInfo{Domain: "mecatl.stacklok.com", Reason: "context_window_unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func admissionModel(t *testing.T, resumed bool) (Model, *fakeSender) {
	t.Helper()
	sender := &fakeSender{}
	conv := &fakeConv{recv: &fakeRecver{}, send: sender}
	var m Model
	if resumed {
		m = startupResumeUI(t, conv, "", "completed")
	} else {
		m = newTestModelFromDeps(Deps{Session: conv, Conv: conv, Theme: testTheme(), Ctx: t.Context()})
		m = applyAll(m, client.SessionReadyMsg{SessionID: "new-session"})
	}
	m.deps.Workspace = t.TempDir()
	home := m.deps.Workspace
	m.deps.homeDir = func() (string, error) { return home, nil }
	m.deps.Clipboard = &fakeClipboard{err: errors.New("clipboard must not be reread")}
	m.caps = client.Capabilities{Image: true}
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 35})
	return m, sender
}

func submitAdmission(t *testing.T, m Model) Model {
	t.Helper()
	mm, cmd := m.submitPrompt()
	runStartupCommands(cmd)
	return mm.(Model)
}
func rejectAdmission(t *testing.T, m Model) Model {
	t.Helper()
	mm, cmd := m.Update(streamMsg{gen: m.streamGen, msg: client.StreamErrMsg{Err: admissionError(t)}})
	if cmd != nil {
		t.Fatal("admission rejection scheduled work (must not replay)")
	}
	return mm.(Model)
}
func admissionKey(m Model, k tea.KeyPressMsg) Model {
	mm, cmd := m.Update(k)
	runStartupCommands(cmd)
	return mm.(Model)
}
func retainedAdmission(m Model) bool { return m.admissionSubmission != nil }
func assertAdmissionRetained(t *testing.T, m Model, want bool) {
	t.Helper()
	if got := retainedAdmission(m); got != want {
		t.Fatalf("retained submission = %v, want %v", got, want)
	}
}
func promptBytes(t *testing.T, sender *fakeSender, n int) string {
	t.Helper()
	frames := sender.frames()
	if len(frames) <= n {
		t.Fatalf("only %d prompt frames, need index %d", len(frames), n)
	}
	b, err := json.Marshal(frames[n].GetPrompt())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProviderModelDiscovery_Scenario5_TruthfulSafeStartupError(t *testing.T) {
	m, _ := admissionModel(t, true)
	m.prompt.Rewrite("continue")
	m = rejectAdmission(t, submitAdmission(t, m))
	view := stripANSIstr(m.View().Content)
	for _, want := range []string{"context metadata", "not started", "discovery", "exact", "Retry", "Back"} {
		if !strings.Contains(view, want) {
			t.Errorf("guidance missing %q:\n%s", want, view)
		}
	}
	for _, bad := range []string{"transcript unavailable", "conversation unavailable", "tenant", "untrusted.invalid", "credential=", "--untrusted", "could not be attached"} {
		if strings.Contains(view, bad) {
			t.Errorf("unsafe/untruthful copy %q in:\n%s", bad, view)
		}
	}
	if m.sessionID != "existing" || len(m.conv.blocks) != 2 {
		t.Fatalf("lost transcript/binding: %q, %d blocks", m.sessionID, len(m.conv.blocks))
	}
}

func TestProviderModelDiscovery_Scenario5_TranscriptDraftAndExplicitRetry(t *testing.T) {
	m, send := admissionModel(t, true)
	prior := append([]block(nil), m.conv.blocks...)
	m.prompt.Rewrite("  exact prepared request  ")
	m = submitAdmission(t, m)
	want := promptBytes(t, send, 0)
	m = rejectAdmission(t, m)
	assertAdmissionRetained(t, m, true)
	if !reflect.DeepEqual(m.conv.blocks, prior) {
		t.Fatal("rejection changed adopted transcript")
	}
	if len(send.frames()) != 1 || len(m.conv.blocks) != 2 {
		t.Fatal("automatic replay or optimistic row survived rejection")
	}
	m.prompt.Rewrite("newer follow-up")
	m = admissionKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.prompt.Value() != "newer follow-up" || !m.prompt.Focused() {
		t.Fatal("explicit retry lost or disabled newer compose draft")
	}
	if got := promptBytes(t, send, 1); got != want {
		t.Fatalf("retry changed content: %s != %s", got, want)
	}
	m = applyAll(m, streamMsg{gen: m.streamGen, msg: client.SessionInitMsg{}})
	assertAdmissionRetained(t, m, false)
	if len(m.conv.blocks) != 3 || len(send.frames()) != 2 {
		t.Fatal("successful retry duplicated prompt row/send")
	}
	m = m.endRun("")
}

func TestProviderModelDiscovery_Scenario5_PasteImageAndMixedRecovery(t *testing.T) {
	for _, kind := range []string{"paste", "image-only", "mixed"} {
		for _, action := range []string{"retry", "back", "newer-cancel-confirm"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				m, send := admissionModel(t, true)
				draft := ""
				if kind != "image-only" {
					draft = "[Pasted text #3]"
					m.stagedPastes = map[string]string{"[Pasted text #3]": "pasted bytes\nunchanged"}
					m.nextPasteN = 3
				}
				if kind != "paste" {
					draft += " [Image #4]"
					m.stagedMedia = map[string]stagedAttachment{"[Image #4]": {mime: "image/png", data: []byte("pixels")}}
					m.nextMediaN = 4
				}
				if kind == "mixed" {
					p := filepath.Join(m.deps.Workspace, "fixture.txt")
					if err := os.WriteFile(p, []byte("original file"), 0600); err != nil {
						t.Fatal(err)
					}
					draft += " @fixture.txt"
					part, desc, err := client.StageClipboardImage("image/png", []byte("pending"), m.caps)
					if err != nil {
						t.Fatal(err)
					}
					m.pendingPromptMedia.Parts = append(m.pendingPromptMedia.Parts, part)
					m.pendingPromptMedia.Descriptors = append(m.pendingPromptMedia.Descriptors, desc)
				}
				m.prompt.Rewrite(draft)
				pastes, staged, pending := m.stagedPastes, m.stagedMedia, m.pendingPromptMedia
				m = submitAdmission(t, m)
				want := promptBytes(t, send, 0)
				if pastes != nil {
					pastes["[Pasted text #3]"] = "MUTATED"
				}
				if staged != nil {
					staged["[Image #4]"].data[0] = 'X'
				}
				if len(pending.Parts) > 0 {
					pending.Parts[0].Data[0] = 'X'
					pending.Descriptors[0] = "MUTATED"
				}
				if kind == "mixed" {
					if err := os.WriteFile(filepath.Join(m.deps.Workspace, "fixture.txt"), []byte("changed file"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				m = rejectAdmission(t, m)
				if action == "retry" {
					m = admissionKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
					if got := promptBytes(t, send, 1); got != want {
						t.Fatalf("retry reexpanded/reread/aliased: %s != %s", got, want)
					}
					if m.deps.Clipboard.(*fakeClipboard).calls != 0 {
						t.Fatal("retry read clipboard")
					}
					// The transport owns its sent frame, not the retained payload.
					frames := send.frames()
					if len(frames[1].GetPrompt().GetParts()) > 0 {
						frames[1].GetPrompt().Parts[0].Data[0] = 'Z'
					}
					m = rejectAdmission(t, m)
					m = admissionKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
					if got := promptBytes(t, send, 2); got != want {
						t.Fatalf("transport aliased retained payload: %s != %s", got, want)
					}
					m = m.endRun("")
					return
				}
				if action == "newer-cancel-confirm" {
					m.prompt.Rewrite("newer draft")
					newerStaged := map[string]stagedAttachment{"[Image #9]": {mime: "image/png", data: []byte("newer pixels")}}
					newerPastes := map[string]string{"[Pasted text #9]": "newer paste"}
					m.stagedMedia, m.stagedPastes = newerStaged, newerPastes
					m.nextMediaN, m.nextPasteN = 9, 9
					m = admissionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
					if m.prompt.Value() != "newer draft" {
						t.Fatal("Back destroyed newer draft without confirmation")
					}
					m = admissionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
					assertAdmissionRetained(t, m, true)
					if m.prompt.Value() != "newer draft" || !reflect.DeepEqual(m.stagedMedia, newerStaged) || !reflect.DeepEqual(m.stagedPastes, newerPastes) || m.nextMediaN != 9 || m.nextPasteN != 9 {
						t.Fatal("cancel destroyed newer draft/staging")
					}
					m = admissionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
					m = admissionKey(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
				} else {
					m = admissionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
				}
				if m.prompt.Value() != draft {
					t.Fatalf("draft = %q, want %q", m.prompt.Value(), draft)
				}
				if kind != "image-only" && (m.stagedPastes["[Pasted text #3]"] != "pasted bytes\nunchanged" || m.nextPasteN != 3) {
					t.Fatal("paste not detached/restored")
				}
				if kind != "paste" && (string(m.stagedMedia["[Image #4]"].data) != "pixels" || m.nextMediaN != 4) {
					t.Fatal("staged media not detached/restored")
				}
				if kind == "mixed" && (len(m.pendingPromptMedia.Parts) != 1 || string(m.pendingPromptMedia.Parts[0].GetData()) != "pending" || m.pendingPromptMedia.Descriptors[0] == "MUTATED") {
					t.Fatal("pending media not detached/restored")
				}
				assertAdmissionRetained(t, m, false)
				if len(send.frames()) != 1 || m.phase != phaseIdle || !m.prompt.Focused() {
					t.Fatal("Back sent content or failed to restore editing")
				}
			})
		}
	}
}

type admissionErrorReceiver struct{ err error }

func (r admissionErrorReceiver) Recv() (*mecatlv1.ConverseResponse, error) { return nil, r.err }

type admissionConverser struct {
	err      error
	sender   *fakeSender
	failOpen bool
}

func (c admissionConverser) OpenConverse(context.Context) (*client.Stream, error) {
	if c.failOpen {
		return nil, c.err
	}
	return client.NewStream(admissionErrorReceiver{c.err}, c.sender), nil
}

func TestProviderModelDiscovery_Scenario5_NewSessionAndUnrelatedErrors(t *testing.T) {
	for _, failOpen := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream boundary/open=%v", failOpen), func(t *testing.T) {
			m, send := admissionModel(t, true)
			m.deps.Conv = admissionConverser{err: admissionError(t), sender: send, failOpen: failOpen}
			m.prompt.Rewrite("stream rejection")
			mm, cmd := m.submitPrompt()
			m = mm.(Model)
			if !failOpen {
				// Execute the send separately, then deliver the actual ReadLoop output.
				batch := cmd().(tea.BatchMsg)
				if result := batch[0](); result != nil {
					t.Fatalf("send: %v", result)
				}
				event := m.waitCmd()()
				tagged, ok := event.(streamMsg)
				if !ok {
					t.Fatalf("reader message %T", event)
				}
				streamErr, ok := tagged.msg.(client.StreamErrMsg)
				if !ok || !client.IsContextWindowUnavailable(streamErr.Err) {
					t.Fatalf("typed error lost at stream: %v", tagged.msg)
				}
				mm, cmd = m.Update(event)
				m = mm.(Model)
			}
			assertAdmissionRetained(t, m, true)
			if _, ok := m.modal.(*admissionRecoveryState); !ok || cmd != nil {
				t.Fatal("typed transport rejection did not open safe explicit recovery")
			}
			m = m.endRun("")
		})
	}

	t.Run("new session", func(t *testing.T) {
		m, send := admissionModel(t, false)
		m.prompt.Rewrite("first prompt [Image #1]")
		m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("new session image")}}
		m = rejectAdmission(t, submitAdmission(t, m))
		assertAdmissionRetained(t, m, true)
		if len(m.conv.blocks) != 0 {
			t.Fatal("rejected optimistic row remains")
		}
		m = admissionKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
		if promptBytes(t, send, 0) != promptBytes(t, send, 1) {
			t.Fatal("new-session retry changed prompt")
		}
		m = m.endRun("")
	})
	t.Run("after SessionInit is not recoverable", func(t *testing.T) {
		m, _ := admissionModel(t, true)
		m.prompt.Rewrite("already admitted")
		m = submitAdmission(t, m)
		m = applyAll(m, streamMsg{gen: m.streamGen, msg: client.SessionInitMsg{}})
		m = applyAll(m, streamMsg{gen: m.streamGen, msg: client.StreamErrMsg{Err: admissionError(t)}})
		assertAdmissionRetained(t, m, false)
		if _, ok := m.modal.(*admissionRecoveryState); ok {
			t.Fatal("post-init error offered replay")
		}
	})
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary transport", true: "startup transport"}[resumed], func(t *testing.T) {
			m, _ := admissionModel(t, resumed)
			m.prompt.Rewrite("plain transport recovery")
			m = submitAdmission(t, m)
			m = applyAll(m, streamMsg{gen: m.streamGen, msg: client.StreamErrMsg{Err: errors.New("context_window_unavailable tenant path")}})
			assertAdmissionRetained(t, m, false)
			if strings.Contains(stripANSIstr(m.View().Content), "context metadata") {
				t.Fatal("raw text entered typed recovery")
			}
			if m.prompt.Value() != "plain transport recovery" {
				t.Fatal("generic recovery changed")
			}
		})
	}
}

func TestProviderModelDiscovery_Scenario5_RetentionBoundsAndCleanup(t *testing.T) {
	t.Run("only surviving staging is retained", func(t *testing.T) {
		m, _ := admissionModel(t, false)
		m.prompt.Rewrite("[Pasted text #1]")
		m.stagedPastes = map[string]string{"[Pasted text #1]": "image within paste [Image #1]", "[Pasted text #2]": "deleted paste"}
		m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("kept")}, "[Image #2]": {mime: "image/png", data: make([]byte, (20<<20)+1)}}
		m.nextMediaN, m.nextPasteN = 2, 2
		m = submitAdmission(t, m)
		assertAdmissionRetained(t, m, true)
		if len(m.admissionSubmission.staged) != 1 || len(m.admissionSubmission.pastes) != 1 {
			t.Fatal("deleted markers retained bytes outside validated submission limits")
		}
		m = rejectAdmission(t, m)
		m = admissionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if string(m.stagedMedia["[Image #1]"].data) != "kept" || m.nextMediaN != 2 || m.nextPasteN != 2 {
			t.Fatal("lost paste-embedded image or original counters")
		}
	})

	for _, action := range []string{"init", "discard", "replace", "exit", "unrelated", "auth", "closed", "stale", "stale retry", "wrong session retry", "reset", "retries"} {
		t.Run(action, func(t *testing.T) {
			m, send := admissionModel(t, false)
			m.prompt.Rewrite("bounded")
			m = submitAdmission(t, m)
			assertAdmissionRetained(t, m, true)
			switch action {
			case "init":
				m = applyAll(m, streamMsg{gen: m.streamGen, msg: client.SessionInitMsg{}})
			case "unrelated":
				m = applyAll(m, streamMsg{gen: m.streamGen, msg: client.StreamErrMsg{Err: errors.New("transport")}})
			case "auth":
				m = applyAll(m, streamMsg{gen: m.streamGen, msg: client.StreamErrMsg{Err: status.Error(codes.Unauthenticated, "expired"), AuthReason: client.AuthSessionExpired}})
				if _, ok := m.modal.(*admissionRecoveryState); ok {
					t.Fatal("auth entered admission recovery")
				}
			case "closed":
				m = applyAll(m, streamMsg{gen: m.streamGen, msg: client.StreamClosedMsg{}})
			case "stale":
				m = applyAll(m, streamMsg{gen: m.streamGen - 1, msg: client.StreamErrMsg{Err: admissionError(t)}})
				if m.phase != phaseRunning || len(send.frames()) != 1 {
					t.Fatal("stale rejection consumed current submission")
				}
				assertAdmissionRetained(t, m, true)
				m = m.endRun("")
				return
			default:
				m = rejectAdmission(t, m)
				switch action {
				case "discard":
					m = admissionKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
				case "replace":
					m = m.bindSessionID("successor")
				case "exit":
					mm, _ := m.quitNow()
					m = mm.(Model)
				case "reset":
					m = m.resetSession()
				case "stale retry", "wrong session retry":
					if action == "stale retry" {
						m.streamGen++
					} else {
						m.sessionID = "other"
					}
					m = admissionKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
					if len(send.frames()) != 1 {
						t.Fatal("stale retry sent rejected content")
					}
				case "retries":
					retained := m.admissionSubmission
					for range 3 {
						m = admissionKey(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
						m = rejectAdmission(t, m)
						assertAdmissionRetained(t, m, true)
						if m.admissionSubmission != retained || !m.ownsAdmission() {
							t.Fatal("retry copied or misbound retained record")
						}
					}
					if len(send.frames()) != 4 || len(m.conv.blocks) != 0 {
						t.Fatal("retry accumulated rows or replayed automatically")
					}
					m = admissionKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
				}
			}
			assertAdmissionRetained(t, m, false)
			m = m.endRun("")
		})
	}
	for _, tc := range []struct {
		name           string
		count, size    int
		pending, valid bool
	}{
		{"part", 1, (10 << 20) + 1, false, false},
		{"aggregate", 3, 8 << 20, false, false},
		{"count", 17, 1, false, false},
		{"pending aggregate", 2, 8 << 20, true, false},
		{"pending count", 16, 1, true, false},
		{"exact part and aggregate cap", 2, 10 << 20, false, true},
		{"exact count cap", 16, 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, send := admissionModel(t, false)
			for i := range tc.count {
				marker := fmt.Sprintf("[Image #%d]", i+1)
				if m.stagedMedia == nil {
					m.stagedMedia = map[string]stagedAttachment{}
				}
				m.stagedMedia[marker] = stagedAttachment{mime: "image/png", data: make([]byte, tc.size)}
				m.prompt.Rewrite(m.prompt.Value() + " " + marker)
			}
			if tc.pending {
				part, desc, err := client.StageClipboardImage("image/png", make([]byte, tc.size), m.caps)
				if err != nil {
					t.Fatal(err)
				}
				m.pendingPromptMedia.Parts = append(m.pendingPromptMedia.Parts, part)
				m.pendingPromptMedia.Descriptors = append(m.pendingPromptMedia.Descriptors, desc)
			}
			draft := m.prompt.Value()
			m = submitAdmission(t, m)
			if tc.valid {
				assertAdmissionRetained(t, m, true)
				if len(send.frames()) != 1 {
					t.Fatal("valid boundary submission not sent")
				}
				m.endRun("")
				return
			}
			assertAdmissionRetained(t, m, false)
			if len(send.frames()) != 0 || m.prompt.Value() != draft || len(m.stagedMedia) != tc.count {
				t.Fatal("validation captured/sent/cleared invalid payload")
			}
		})
	}
}
