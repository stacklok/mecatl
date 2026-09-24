package ui

import (
	"slices"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// admissionSubmission is volatile and bounded by the ordinary preparation limits.
// Prepared content and editable staging have separate detached ownership.
type admissionSubmission struct {
	sessionID              string
	streamGen              uint64
	draft, text            string
	media, pendingMedia    client.MediaResult
	staged                 map[string]stagedAttachment
	pastes                 map[string]string
	nextMediaN, nextPasteN int
	blockID                uint64
	rejected               bool
}

func (m *Model) retainAdmission(text string, media client.MediaResult) {
	draft := m.prompt.Value()
	pastes := filterPastes(draft, m.stagedPastes)
	// Deleted markers are already excluded by preparation; do not retain their bytes.
	staged := filterStaged(expandPastePlaceholders(draft, pastes), m.stagedMedia)
	for marker, attachment := range staged {
		attachment.data = slices.Clone(attachment.data)
		staged[marker] = attachment
	}
	m.admissionSubmission = &admissionSubmission{
		sessionID: m.sessionID, streamGen: m.streamGen + 1,
		draft: draft, text: text, media: media.Clone(), pendingMedia: m.pendingPromptMedia.Clone(),
		staged: staged, pastes: pastes, nextMediaN: m.nextMediaN, nextPasteN: m.nextPasteN,
		blockID: m.conv.nextBlockID + 1,
	}
}

func (m Model) ownsAdmission() bool {
	r := m.admissionSubmission
	return r != nil && r.sessionID == m.sessionID && r.streamGen == m.streamGen
}

func (m Model) rejectAdmission() Model {
	r := m.admissionSubmission
	m = m.endRun("")
	m.conv.blocks = slices.DeleteFunc(m.conv.blocks, func(b block) bool { return b.id == r.blockID })
	m = m.resetDocumentProjection()
	r.streamGen, r.rejected = m.streamGen, true
	m.admissionSubmission = r
	m.promptRecovery = nil
	m.startupFirstPromptPending = false
	m.startupRetryPrompt = ""
	m.statusMsg = "execution not started"
	m.modal = &admissionRecoveryState{deps: (&m).surfaceDeps()}
	m.prompt.Blur()
	m.refreshView()
	return m
}

const admissionUnavailableCopy = "Model context metadata is unavailable. Execution has not started.\n\nRestore provider discovery or configure the exact model context window on the server, then Retry. Back restores your editable submission."

type admissionRecoveryState struct {
	deps           surfaceDeps
	confirmReplace bool
}

func (s *admissionRecoveryState) Render(width, _ int) (string, []ClickableRegion) {
	text := admissionUnavailableCopy + "\n\nr: Retry  " + firstKey(s.deps.keys.Close, "esc") + ": Back  d: Discard submission"
	if s.confirmReplace {
		text = "Replace the newer draft with the rejected submission?\n\ny: Replace draft  " + firstKey(s.deps.keys.Close, "esc") + ": Cancel"
	}
	return s.deps.theme.Style("warning").Width(max(1, min(width, 72))).Render(text), nil
}

// Model owns the retained payload and applies these controls synchronously.
func (*admissionRecoveryState) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	return nil, true, false
}
func (*admissionRecoveryState) HandleMsg(tea.Msg) (tea.Cmd, bool, bool)       { return nil, false, false }
func (*admissionRecoveryState) HandleWheel(tea.MouseWheelMsg) (tea.Cmd, bool) { return nil, true }
func (*admissionRecoveryState) Close()                                        {}

func (m Model) onAdmissionKey(msg tea.KeyPressMsg, s *admissionRecoveryState) (tea.Model, tea.Cmd) {
	if !m.ownsAdmission() || !m.admissionSubmission.rejected {
		m.admissionSubmission = nil
		m.closeModal()
		return m, nil
	}
	if s.confirmReplace {
		switch {
		case msg.String() == "y":
			return m.restoreAdmission()
		case key.Matches(msg, m.keys.Close), msg.String() == "n":
			s.confirmReplace = false
		}
		return m, nil
	}
	switch {
	case msg.String() == "r":
		r := m.admissionSubmission
		m.closeModal()
		r.streamGen, r.rejected = m.streamGen+1, false
		m.conv.addUserWithMedia(r.text, r.media.Descriptors)
		r.blockID = m.conv.nextBlockID
		m.phase = phaseRunning
		_ = m.prompt.Focus()
		m.statusMsg = "running…"
		m.startupFirstPromptPending = m.startupAdopted
		m.startupRetryPrompt = r.draft
		m.refreshView()
		media := r.media.Clone()
		return m.openRun(false, func(stream *client.Stream) error { return stream.SendPrompt(r.sessionID, r.text, media.Parts) })
	case key.Matches(msg, m.keys.Close):
		if m.prompt.Value() != "" || len(m.pendingPromptMedia.Parts) > 0 || len(m.stagedMedia) > 0 || len(m.stagedPastes) > 0 {
			s.confirmReplace = true
			return m, nil
		}
		return m.restoreAdmission()
	case msg.String() == "d":
		m.admissionSubmission = nil
		m.closeModal()
		m.statusMsg = "submission discarded"
		return m, m.prompt.Focus()
	}
	return m, nil
}

func (m Model) restoreAdmission() (tea.Model, tea.Cmd) {
	r := m.admissionSubmission
	m.prompt.Rewrite(r.draft)
	m.pendingPromptMedia = r.pendingMedia
	m.stagedMedia, m.stagedPastes = r.staged, r.pastes
	m.nextMediaN, m.nextPasteN = r.nextMediaN, r.nextPasteN
	m.admissionSubmission = nil
	m.closeModal()
	m.statusMsg = "ready"
	m.refreshView()
	return m, m.prompt.Focus()
}
