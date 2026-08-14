package ui

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestSessionContinuityUX_Scenario5_DetailsSurface(t *testing.T) {
	m := newScenario5Model(t, &fakeClipboard{})
	m.sessionID = "opaque\nfull-id"
	m.sessionTitle = "Current chat"
	m.sessionState = "idle"
	m.sessionCreatedAt = 1_700_000_000
	m.sessionModifiedAt = 1_700_000_100
	m.activeWorkspace = "/work/repo"
	m.effectiveModel = client.ResolvedModel{ProviderID: "openrouter", ModelID: "openai/gpt-5"}

	got := stripANSIstr(renderSessionDetails(m.deps.Theme, m.sessionDetails(), helpKeys{closeOnly: "esc"}, 100, 30))
	for _, want := range []string{
		strconv.QuoteToASCII(m.sessionID), "Current chat", "idle", "/work/repo",
		"2023-11-14", "openrouter", "openai/gpt-5", "c: copy exact ID",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("details missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "opaque\nfull-id") {
		t.Fatalf("details rendered a control-bearing ID literally:\n%s", got)
	}
}

func TestSessionContinuityUX_Scenario5_CopyExactID(t *testing.T) {
	id := "opaque\n\u2603\x00id"
	cb := &fakeClipboard{}
	m := newScenario5Model(t, cb)
	m.sessionID = id
	m.sessionDetailsOpen = true

	mm, cmd, handled := m.onSessionDetailsKey(tea.KeyPressMsg{Code: 'c'})
	if !handled || cmd == nil {
		t.Fatal("c must start the explicit exact-ID clipboard action")
	}
	m = mm.(Model)
	m = applyAll(m, cmd())
	if len(cb.wrote) != 1 || !bytes.Equal(cb.wrote[0], []byte(id)) {
		t.Fatalf("clipboard payloads = %q, want byte-exact %q", cb.wrote, []byte(id))
	}
	if got := stripANSIstr(m.statusMsg); !strings.Contains(got, "copied session ID") {
		t.Fatalf("success status = %q", got)
	}

	cb.writeErr = errors.New("clipboard unavailable")
	mm, cmd, _ = m.onSessionDetailsKey(tea.KeyPressMsg{Code: 'c'})
	m = mm.(Model)
	m = applyAll(m, cmd())
	if got := stripANSIstr(m.statusMsg); !strings.Contains(got, "could not copy session ID") || strings.Contains(got, "copied session ID") {
		t.Fatalf("failure status was dishonest: %q", got)
	}

	m.sessionID = ""
	mm, cmd, _ = m.onSessionDetailsKey(tea.KeyPressMsg{Code: 'c'})
	m = mm.(Model)
	if cmd != nil || strings.Contains(stripANSIstr(m.statusMsg), "copied session ID") {
		t.Fatalf("empty ID must not be copied or claimed: cmd=%v status=%q", cmd != nil, stripANSIstr(m.statusMsg))
	}
}

func TestSessionContinuityUX_Scenario5_RebindMatrix(t *testing.T) {
	for _, journey := range []string{"stored continuation", "model carryover", "effort fork", "worktree switch"} {
		t.Run(journey, func(t *testing.T) {
			cb := &fakeClipboard{}
			m, wantID := driveSessionRebindJourney(t, journey, cb)
			if got := m.sessionDetails().ID; got != wantID {
				t.Fatalf("details ID = %q, want final adopted %q", got, wantID)
			}
			m.sessionDetailsOpen = true
			mm, cmd, handled := m.onSessionDetailsKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
			if !handled || cmd == nil {
				t.Fatal("/session copy action did not issue a clipboard command")
			}
			m = mm.(Model)
			m = applyAll(m, cmd())
			if len(cb.wrote) != 1 || !bytes.Equal(cb.wrote[0], []byte(wantID)) {
				t.Fatalf("clipboard payloads = %q, want final adopted ID %q", cb.wrote, wantID)
			}
		})
	}
}

func driveSessionRebindJourney(t *testing.T, journey string, cb client.Clipboard) (Model, string) {
	t.Helper()
	m := newScenario5Model(t, cb)
	conv := m.deps.Session.(*fakeConv)
	applyCmd := func(cmd tea.Cmd) {
		t.Helper()
		for _, msg := range flattenBatch(cmd) {
			m = applyAll(m, msg)
		}
	}

	switch journey {
	case "stored continuation":
		const id = "continued-id"
		loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{SessionID: id, Complete: true, Kind: client.SessionKindMain}}
		m.deps.Transcript = loader
		m.sessions.filtered = []client.SessionListItem{{ID: id, Title: "Stored chat", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true}}}
		mm, cmd, handled := m.chooseSession()
		if !handled || cmd == nil {
			t.Fatal("stored continuation did not request its authoritative transcript")
		}
		m = mm.(Model)
		applyCmd(cmd)
		return m, id
	case "model carryover":
		conv.createCount = 1
		mm, cmd, handled := m.restartOnModelWithCarryover(client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5-mini"})
		if !handled || cmd == nil {
			t.Fatal("model carryover did not issue its create command")
		}
		m = mm.(Model)
		applyCmd(cmd)
		return m, "sess-test-0002"
	case "effort fork":
		conv.forkedID = "effort-fork-id"
		mm, cmd, handled := m.switchEffort(client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5", ReasoningEffort: "high"})
		if !handled || cmd == nil {
			t.Fatal("effort switch did not issue its fork command")
		}
		m = mm.(Model)
		applyCmd(cmd)
		return m, conv.forkedID
	case "worktree switch":
		conv.createCount = 1
		mm, cmd, handled := m.switchToWorktree(client.Worktree{Path: "/workspace/feature", Branch: "feature"})
		if !handled || cmd == nil {
			t.Fatal("worktree switch did not issue its create command")
		}
		m = mm.(Model)
		applyCmd(cmd)
		return m, "sess-test-0002"
	default:
		t.Fatalf("unknown rebind journey %q", journey)
		return Model{}, ""
	}
}

func TestSessionContinuityUX_Scenario5_HeaderAndHelp(t *testing.T) {
	m := newScenario5Model(t, &fakeClipboard{})
	m.sessionID = strings.Repeat("very-long-opaque-id", 20)
	m.width = 44
	header := stripANSIstr(m.renderHeader())
	wantHandle := sessionDigest(m.sessionID)[:8]
	if !strings.Contains(header, wantHandle) || strings.Contains(header, m.sessionID) {
		t.Fatalf("header must use the shared display digest, not the full id: %q", header)
	}
	for _, line := range strings.Split(header, "\n") {
		if len([]rune(line)) > m.width {
			t.Fatalf("header line exceeds width %d: %q", m.width, line)
		}
	}

	builtins := builtinCommands(client.Capabilities{}, wiredCollaborators{})
	found := false
	for _, b := range builtins {
		found = found || b.name == "session"
	}
	if !found {
		t.Fatal("/session missing from slash builtins")
	}
	if got := stripANSIstr(helpBody(m.deps.Theme, client.Capabilities{}, m.helpKeyMarkings())); !strings.Contains(got, "/session") {
		t.Fatalf("? help does not discover /session:\n%s", got)
	}
}

func TestInvariant_session_details_render_safe_copy_exact(t *testing.T) {
	ids := []string{"", "line1\nline2\x00\x1b[31m", strings.Repeat("\u754c", 2048)}
	for _, id := range ids {
		t.Run(strconv.Itoa(len(id)), func(t *testing.T) {
			quoted := safeSessionID(id)
			decoded, err := strconv.Unquote(quoted)
			if err != nil || decoded != id {
				t.Fatalf("safe ID is not reversible: quoted=%q decoded=%q err=%v", quoted, decoded, err)
			}
			if strings.Contains(quoted, "\n") || strings.Contains(quoted, "\x1b") {
				t.Fatalf("safe ID contains literal terminal controls: %q", quoted)
			}
			m := newScenario5Model(t, &fakeClipboard{})
			m.sessionID = id
			if got := m.sessionCopyTarget(); got != id {
				t.Fatalf("copy target changed bytes: got %q want %q", got, id)
			}
		})
	}
}

func newScenario5Model(t *testing.T, cb client.Clipboard) Model {
	t.Helper()
	m, _ := newClipboardModel(t, client.Capabilities{}, cb)
	return m
}
