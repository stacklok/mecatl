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
	m.activePlacement = client.Placement{Kind: "git", Label: "repo"}
	m.deps.ConnectionMode = "connect"
	m.deps.Server = "server.example:8080"
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "openrouter", ModelID: "openai/gpt-5"}

	got := stripANSIstr(renderSessionDetails(m.deps.Theme, m.sessionDetails(), helpKeys{closeOnly: "esc"}, 100, 30))
	for _, want := range []string{
		strconv.QuoteToASCII(m.sessionID), "Current chat", "idle", "remote (server.example:8080)", "repo",
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

func TestSessionConnectionLabel(t *testing.T) {
	for _, tc := range []struct {
		mode, target, want string
	}{
		{mode: "embedded", target: "unix:///private/mecatui.sock", want: "embedded"},
		{mode: "connect", target: "server.example:8080", want: "remote (server.example:8080)"},
		{mode: "connect", want: "remote"},
		{mode: "unknown", target: "server.example", want: ""},
	} {
		if got := sessionConnectionLabel(tc.mode, tc.target); got != tc.want {
			t.Errorf("sessionConnectionLabel(%q, %q) = %q, want %q", tc.mode, tc.target, got, tc.want)
		}
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

func TestADR_0344_Scenario4_SessionDetailsOpenWhileRunning(t *testing.T) {
	cb := &fakeClipboard{}
	m := newScenario5Model(t, cb)
	m.sessionID = "full-running-session-id"
	m.sessionState = "running"
	m.phase = phaseRunning

	mm, cmd := m.openSessionDetails()
	got := mm.(Model)
	if cmd == nil {
		t.Fatal("running /session should refresh the bound session details")
	}
	if !got.sessionDetailsOpen {
		t.Fatal("running /session did not open the details overlay")
	}
	if got.sessionDetails().ID != m.sessionID {
		t.Fatalf("details ID = %q, want full bound ID %q", got.sessionDetails().ID, m.sessionID)
	}
	mm, copyCmd, handled := got.onSessionDetailsKey(tea.KeyPressMsg{Code: 'c'})
	if !handled || copyCmd == nil {
		t.Fatal("running /session should offer copying the full ID")
	}
	applyAll(mm.(Model), copyCmd())
	if len(cb.wrote) != 1 || string(cb.wrote[0]) != m.sessionID {
		t.Fatalf("copied ID = %q, want full bound ID %q", cb.wrote, m.sessionID)
	}
}

func TestADR_0344_Scenario4_SessionDetailsDoNotInterruptRun(t *testing.T) {
	m := newScenario5Model(t, &fakeClipboard{})
	m.sessionID = "live-session"
	m.phase = phaseRunning
	m.prompt.Focus()

	mm, _ := m.openSessionDetails()
	m = mm.(Model)
	if m.phase != phaseRunning {
		t.Fatalf("opening /session changed phase to %v, want running", m.phase)
	}
	if m.prompt.Focused() {
		t.Fatal("overlay should temporarily capture input focus")
	}

	mm, _, handled := m.onSessionDetailsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if !handled || m.sessionDetailsOpen || m.phase != phaseRunning {
		t.Fatalf("closing /session changed live run state: handled=%t open=%t phase=%v", handled, m.sessionDetailsOpen, m.phase)
	}
	if !m.prompt.Focused() {
		t.Fatal("closing /session did not return focus to the live conversation")
	}
}

func TestADR_0344_Scenario4_NoSessionGuardRemains(t *testing.T) {
	m := newScenario5Model(t, &fakeClipboard{})
	m.sessionID = ""
	m.phase = phaseRunning

	mm, cmd := m.openSessionDetails()
	m = mm.(Model)
	if cmd != nil || m.sessionDetailsOpen {
		t.Fatal("/session without a bound session must not open or refresh details")
	}
	if got := stripANSIstr(m.statusMsg); got != "no active session" {
		t.Fatalf("no-session response = %q, want %q", got, "no active session")
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
		setSessionsInventoryRows(ensureActiveSessions(&m), []client.SessionListItem{{ID: id, Title: "Stored chat", Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true}}})
		mm, cmd, handled := m.chooseSession()
		if !handled || cmd == nil {
			t.Fatal("stored continuation did not request its authoritative transcript")
		}
		m = mm.(Model)
		applyCmd(cmd)
		return m, id
	case "model carryover":
		conv.createCount = 1
		m.deps.Transcript = modelSwitchTranscriptLoader{}
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
		mm, cmd, handled := m.switchToWorktree(client.Worktree{Selector: testWorktreeSelector("opaque-feature"), Label: "feature", Branch: "feature"})
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
	wantHandle := client.SessionHandle(m.sessionID)
	if !strings.Contains(header, wantHandle) || strings.Contains(header, m.sessionID) {
		t.Fatalf("header must use the shared fixed handle, not the full id: %q", header)
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
