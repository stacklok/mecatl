package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func debugUIModel(target string, width int) Model {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), DebugTarget: target})
	m.width = width
	m.height = 20
	return m
}

func TestDebugIdentityUsesNormalHeaderAcrossPhases(t *testing.T) {
	m := debugUIModel("target\x1b[31m\nopaque", 80)
	want := "DEBUG target " + client.SessionHandle(m.deps.DebugTarget)
	for _, p := range []phase{phaseConnecting, phaseIdle, phaseRunning, phaseAwaitingApproval, phaseFatal} {
		m.phase = p
		rendered := m.renderHeader()
		if p == phaseFatal {
			rendered = m.View().Content
		}
		plain := stripANSIstr(rendered)
		if !strings.Contains(plain, "mecatui  ·  "+want+"  ·  session ") || strings.Contains(plain, "target[31m opaque") || strings.Contains(rendered, "\x1b[31m") {
			t.Fatalf("phase %v debug header identity unsafe/missing: %q", p, plain)
		}
		if !strings.Contains(rendered, m.deps.Theme.Style("warning").Bold(true).Render(want)) {
			t.Fatalf("phase %v debug target lacks amber/bold treatment: %q", p, rendered)
		}
	}
}

func TestDebugPrivacyDisclosureIsVisibleInRenderedHeader(t *testing.T) {
	m := debugUIModel("target-session", 80)
	m.deps.DebugMCP = []string{"github"}
	plain := stripANSIstr(m.renderHeader())
	for _, want := range []string{
		"PRIVACY: target evidence sent to the configured model may include prompts",
		"assistant output, tool arguments/results, file paths, and secrets",
		"reporting servers available: github",
		"availability does not authorize",
		"publication or sending.",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("debug privacy disclosure missing %q: %q", want, plain)
		}
	}
}

func TestDebugHeaderKeepsWholeIdentityAndShedsOptionalSegments(t *testing.T) {
	m := debugUIModel("target-session", 40)
	m.sessionID = "debug-session"
	m.resolvedSessionModel = client.ResolvedModel{ModelID: "large-model", ProviderID: "provider"}
	m.activeMode = "accept-edits"
	m.deps.Server = "remote-server"
	plain := stripANSIstr(m.renderHeader())
	want := "DEBUG target " + client.SessionHandle("target-session")
	if !strings.Contains(plain, want) || strings.Contains(plain, "large-model") || strings.Contains(plain, "accept-edits") || strings.Contains(plain, "remote-server") {
		t.Fatalf("narrow debug header did not preserve target before optional segments: %q", plain)
	}
	m.width = 7
	plain = strings.Join(strings.Fields(stripANSIstr(m.renderHeader())), "")
	if !strings.Contains(plain, "DEBUGtarget"+client.SessionHandle("target-session")) || strings.Contains(plain, "…") {
		t.Fatalf("very narrow header clipped debug identity: %q", plain)
	}
}

func TestDebugFatalHeaderHeightIsAccountedFor(t *testing.T) {
	m := debugUIModel("target-session", 40)
	m.phase = phaseFatal
	if got := strings.Count(m.View().Content, "\n") + 1; got != m.height {
		t.Fatalf("fatal frame height = %d, want %d", got, m.height)
	}
}

type resolvedDebugTargetSession struct {
	*fakeConv
	target string
}

func (s *resolvedDebugTargetSession) DebugTargetID() string { return s.target }

func TestDebugReadyAdoptsResolvedExactTarget(t *testing.T) {
	m := debugUIModel("123456789012", 80)
	m.deps.Session = &resolvedDebugTargetSession{fakeConv: &fakeConv{}, target: "123456789012-full-target"}
	m0, _, _ := m.applySessionReady(client.SessionReadyMsg{SessionID: "debug-session"})
	m = m0.(Model)
	if m.deps.DebugTarget != "123456789012-full-target" || m.sessionDetails().DebugTargetID != "123456789012-full-target" {
		t.Fatalf("resolved debug target was not adopted: %q", m.deps.DebugTarget)
	}
}

func TestDebugSessionDetailsShowAndCopyExactTargetID(t *testing.T) {
	const target = "target with \"quotes\"\nline"
	m := debugUIModel(target, 100)
	m.sessionID = "debug-session"
	m.phase = phaseIdle
	clip := &fakeClipboard{}
	m.deps.Clipboard = clip
	m.sessionDetailsOpen = true

	details := stripANSIstr(renderSessionDetails(m.deps.Theme, m.sessionDetails(), helpKeys{closeOnly: "esc"}, 100, 30))
	if !strings.Contains(details, safeSessionID(target)) || !strings.Contains(details, "Debug target ID:") || !strings.Contains(details, "t: copy exact target ID") {
		t.Fatalf("debug session details omit safely quoted target/copy key: %q", details)
	}
	m0, cmd, handled := m.onSessionDetailsKey(tea.KeyPressMsg{Code: 't', Text: "t"})
	if !handled || cmd == nil {
		t.Fatal("target copy key was not handled")
	}
	msg := cmd().(sessionIDCopyResultMsg)
	m = m0.(Model).onSessionIDCopyResult(msg)
	if len(clip.wrote) != 1 || string(clip.wrote[0]) != target || stripANSIstr(m.statusMsg) != "copied debug target ID" {
		t.Fatalf("target copy = payloads:%q status:%q", clip.wrote, stripANSIstr(m.statusMsg))
	}
}

func TestDebugSessionDisablesWorkspaceEnrollment(t *testing.T) {
	control := &workspaceEnrollmentControlFake{}
	m, send := builtinDispatchModel(t, client.Capabilities{WorkspaceEnrollment: true}, false)
	m.deps.DebugTarget = "target"
	m.deps.WorkspaceEnrollment = control
	m.pendingInitialPrompt = "diagnose"
	m.workspaceEnrollmentNotice = ""

	if m.workspaceEnrollmentActive() || m.wiredCollaborators().Workspace {
		t.Fatal("debug session must not activate workspace enrollment")
	}
	if _, ok := builtinByName(m.caps, m.wiredCollaborators(), "tools-connect"); ok {
		t.Fatal("debug session must not register /tools-connect")
	}
	if m.brokerMCPSetupState().eligible {
		t.Fatal("debug session must not be eligible to connect workspace tools")
	}

	m0, cmd := m.finishStartupResume()
	m = m0.(Model)
	runBatchLeaves(cmd)
	if got := promptTexts(send); len(got) != 1 || got[0] == "" {
		t.Fatalf("startup debug diagnosis prompts = %v, want one", got)
	}
	if m.workspaceEnrollmentNotice != "" {
		t.Fatalf("debug startup showed workspace enrollment notice: %q", m.workspaceEnrollmentNotice)
	}

	m, send = builtinDispatchModel(t, client.Capabilities{WorkspaceEnrollment: true}, false)
	m.deps.DebugTarget = "target"
	m.deps.WorkspaceEnrollment = control
	m.pendingInitialPrompt = "diagnose after rebind"
	m.workspaceEnrollmentNotice = ""
	m0, cmd, _ = m.applySessionReady(client.SessionReadyMsg{
		SessionID: "debug-session", Capabilities: client.Capabilities{WorkspaceEnrollment: true},
	})
	m = m0.(Model)
	runBatchLeaves(cmd)
	if got := promptTexts(send); len(got) != 1 || got[0] == "" {
		t.Fatalf("rebound debug diagnosis prompts = %v, want one", got)
	}
	if m.workspaceEnrollmentNotice != "" {
		t.Fatalf("rebound debug session showed workspace enrollment notice: %q", m.workspaceEnrollmentNotice)
	}
}

func TestDebugBuiltinFilterHidesOnlyBindingBreakingControls(t *testing.T) {
	caps := client.Capabilities{MCP: true, Agents: true, Teams: true, Skills: true, Soul: true, UserModel: true, ModelSelection: true, Worktrees: true, Scheduling: true, Posture: "auto"}
	wired := wiredCollaborators{MCP: true, Agents: true, Skills: true, Soul: true, UserModel: true, Models: true, Worktrees: true, Scheduling: true, Sessions: true, Learning: true, DebugSession: true}
	var names []string
	for _, b := range builtinCommands(caps, wired) {
		names = append(names, b.name)
	}
	joined := "," + strings.Join(names, ",") + ","
	for _, forbidden := range []string{"clear", "models", "effort", "worktrees", "sessions"} {
		if strings.Contains(joined, ","+forbidden+",") {
			t.Fatalf("debug commands expose binding-breaking /%s: %v", forbidden, names)
		}
	}
	for _, harmless := range []string{"help", "session", "retry", "mcp", "agents", "schedule", "learning", "learning-sensitivity", "posture"} {
		if !strings.Contains(joined, ","+harmless+",") {
			t.Fatalf("debug commands hid harmless /%s: %v", harmless, names)
		}
	}
	m := debugUIModel("target", 80)
	m.phase = phaseIdle
	before := m.desiredMode()
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt})
	if got := mm.(Model).desiredMode(); got != before {
		t.Fatalf("mode shortcut changed dedicated debugger mode: %q -> %q", before, got)
	}
}
