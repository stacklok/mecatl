package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestRecoveryOnlyModelInitListsTargetsWithoutSessionCreator(t *testing.T) {
	m := New(Deps{Connect: fakeConnect{targets: []ConnectTarget{{Target: "remote.example:443"}}}, ConnectOpen: true, ConnectError: "Authentication needs attention.", ConnectReason: client.AuthNotEnrolled, ConnectTarget: "remote.example:443", ConnectResumeSessionID: "session-1", Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), NoAltScreen: true})
	if m.phase != phaseIdle || !m.connect.open || !m.connect.loading {
		t.Fatalf("recovery model state = %#v", m.connect)
	}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("recovery Init must list saved targets")
	}
	mm, _ := m.Update(cmd())
	m = mm.(Model)
	if m.phase != phaseIdle || !m.connect.open || m.connect.loading || len(m.connect.targets) != 1 || m.connect.cursor != 0 {
		t.Fatalf("after target list: phase=%v connect=%#v", m.phase, m.connect)
	}
}
func TestConnectAuthReasonsRenderHonestAffordances(t *testing.T) {
	for _, tc := range []struct {
		reason client.AuthReason
		want   string
	}{
		{client.AuthNeverEnrolled, "Run mecatui login remote.example:443 first"},
		{client.AuthNotEnrolled, "No saved login"},
		{client.AuthSessionExpired, "session expired"},
		{client.AuthCredentialUnusable, "corrupt"},
		{client.AuthStorageUnavailable, "unavailable"},
		{client.AuthStorageUnavailable, "will not fix"},
		{client.AuthCredentialCleanup, "Retry the connection"},
		{client.AuthRejected, "Re-login is disabled"},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			m := newConnectModel([]ConnectTarget{{Target: "remote.example:443"}})
			m.connect = connectState{open: true, reason: tc.reason, failedTarget: "remote.example:443"}
			if got := stripANSIstr(m.View().Content); !strings.Contains(got, tc.want) {
				t.Fatalf("overlay missing %q: %s", tc.want, got)
			}
		})
	}
}

func TestConnectAuthRecoveryDropsCandidateOnTargetChange(t *testing.T) {
	m := newConnectModel([]ConnectTarget{{Target: "failed:443"}, {Target: "other:443"}})
	m.connect = connectState{open: true, failedTarget: "failed:443", resumeSessionID: "session-1", targets: []ConnectTarget{{Target: "failed:443"}, {Target: "other:443"}}, cursor: 1}
	mm, _, _ := m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm, cmd, _ := mm.(Model).onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	intent, ok := mm.(Model).ConnectRestartIntent()
	if !ok || cmd == nil || intent.Target != "other:443" || intent.ResumeSessionID != "" {
		t.Fatalf("cross-target intent = %#v, ok=%v cmd=%v", intent, ok, cmd)
	}
}
func TestRejectedTargetDoesNotOfferRelogin(t *testing.T) {
	m := newConnectModel([]ConnectTarget{{Target: "remote.example:443"}})
	m.connect = connectState{open: true, reason: client.AuthRejected, failedTarget: "remote.example:443", targets: []ConnectTarget{{Target: "remote.example:443"}}}
	mm, _, _ := m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	mm, cmd, _ := m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd != nil || m.connectIntent != nil || !strings.Contains(m.connect.err, "rejected") {
		t.Fatalf("rejected target must not restart: intent=%#v err=%q", m.connectIntent, m.connect.err)
	}
}

func TestConnectActionTable(t *testing.T) {
	tests := []struct {
		name       string
		reason     client.AuthReason
		cursor     int
		want       ConnectAction
		wantResume bool
	}{
		{"ordinary saved", "", 0, ConnectSaved, false},
		{"expired same", client.AuthSessionExpired, 0, Reauthenticate, true},
		{"unusable same", client.AuthCredentialUnusable, 0, Reauthenticate, true},
		{"storage same", client.AuthStorageUnavailable, 0, RetryAfterCleanup, true},
		{"never enrolled same", client.AuthNeverEnrolled, 0, ConnectSaved, false},
		{"not enrolled same", client.AuthNotEnrolled, 0, Reauthenticate, true},
		{"cleanup same", client.AuthCredentialCleanup, 0, RetryAfterCleanup, true},
		{"different target", client.AuthSessionExpired, 1, ConnectSaved, false},
		{"add target", client.AuthSessionExpired, 2, AddTarget, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			targets := []ConnectTarget{{Target: "canonical.example:443"}, {Target: "other.example:443"}}
			m := newConnectModel(targets)
			m.connect = connectState{open: true, reason: tc.reason, failedTarget: "canonical.example:443", resumeSessionID: "session-1", targets: targets, cursor: tc.cursor, confirm: true}
			mm, cmd, _ := m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			intent, ok := mm.(Model).ConnectRestartIntent()
			if !ok || cmd == nil || intent.Action != tc.want {
				t.Fatalf("intent = %#v, ok=%v", intent, ok)
			}
			if (intent.ResumeSessionID != "") != tc.wantResume {
				t.Fatalf("resume = %q, want present=%v", intent.ResumeSessionID, tc.wantResume)
			}
		})
	}
}

func TestConnectLoadsIgnoreStaleGenerations(t *testing.T) {
	m := newConnectModel(nil)
	mm, _ := m.openConnect()
	m = mm.(Model)
	first := m.connectLoadGeneration
	m.connect = connectState{} // closing must not reset the model-owned generation
	mm, _ = m.openConnect()
	m = mm.(Model)
	second := m.connectLoadGeneration
	if first == 0 || second != first+1 {
		t.Fatalf("generations = %d, %d", first, second)
	}
	mm, handled := m.updateConnectMsg(connectTargetsMsg{generation: first, targets: []ConnectTarget{{Target: "stale"}}})
	m = mm.(Model)
	if !handled || len(m.connect.targets) != 0 || !m.connect.loading {
		t.Fatalf("stale load changed state: %#v", m.connect)
	}
	mm, _ = m.updateConnectMsg(connectTargetsMsg{generation: second, targets: []ConnectTarget{{Target: "fresh"}}})
	m = mm.(Model)
	if m.connect.loading || len(m.connect.targets) != 1 || m.connect.targets[0].Target != "fresh" {
		t.Fatalf("fresh load not applied: %#v", m.connect)
	}
}

func TestRecoveryInitialConnectLoadHasGeneration(t *testing.T) {
	m := New(Deps{Connect: fakeConnect{}, ConnectOpen: true, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), NoAltScreen: true})
	if m.connectLoadGeneration == 0 {
		t.Fatal("initial recovery load generation is zero")
	}
	msg := m.Init()().(connectTargetsMsg)
	if msg.generation != m.connectLoadGeneration {
		t.Fatalf("message generation = %d, want %d", msg.generation, m.connectLoadGeneration)
	}
}
