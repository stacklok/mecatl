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
	s, ok := m.modal.(*connectState)
	if m.phase != phaseIdle || !ok {
		t.Fatal("missing recovery surface")
	}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("recovery Init must list saved targets")
	}
	result := cmd().(connectTargetsMsg)
	if result.owner != s || result.request != 1 {
		t.Fatal("Init did not issue exactly one owned load")
	}
	m = applyAll(m, result)
	if s.loading || len(s.targets) != 1 || s.list.CursorID() != "target:remote.example:443" {
		t.Fatalf("load not applied: %+v", s)
	}
}

func TestConnectAuthReasonsRenderHonestAffordances(t *testing.T) {
	for _, tc := range []struct {
		reason client.AuthReason
		want   string
	}{
		{client.AuthNotEnrolled, "server requires caller authentication"}, {client.AuthNotEnrolled, "OIDC"},
		{client.AuthAnonymousRejected, "rejected --anonymous"}, {client.AuthSessionExpired, "session expired"},
		{client.AuthCredentialUnusable, "corrupt"}, {client.AuthStorageUnavailable, "unavailable"},
		{client.AuthStorageUnavailable, "will not fix"}, {client.AuthCredentialCleanup, "Retry the connection"},
		{client.AuthRejected, "Re-login is disabled"},
	} {
		t.Run(string(tc.reason)+tc.want, func(t *testing.T) {
			m := openTestConnect(newConnectModel([]ConnectTarget{{Target: "remote.example:443"}}), tc.reason, "remote.example:443", "")
			if got := stripANSIstr(m.View().Content); !strings.Contains(got, tc.want) {
				t.Fatalf("missing %q: %s", tc.want, got)
			}
		})
	}
}

func TestConnectChooserLabelsEnrollmentAsOIDCSignIn(t *testing.T) {
	m := openTestConnect(newConnectModel(nil), "", "", "")
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "OIDC sign-in for a new target") {
		t.Fatal(got)
	}
}

func TestRejectedTargetDoesNotOfferRelogin(t *testing.T) {
	for _, targets := range [][]ConnectTarget{
		{{Target: "failed:443"}}, {{Target: "failed:443"}, {Target: "other:443"}},
	} {
		m := openTestConnect(newConnectModel(targets), client.AuthRejected, "failed:443", "session-1")
		s := m.modal.(*connectState)
		for _, item := range s.items {
			if item.ID == "add" || item.ID == "target:failed:443" {
				t.Fatalf("unavailable choice selectable: %+v", item)
			}
		}
		if out := stripANSIstr(m.View().Content); !strings.Contains(out, "failed:443") || !strings.Contains(out, "credential rejected") {
			t.Fatal(out)
		}
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter})
		intent, ok := m.ConnectRestartIntent()
		if len(targets) == 1 && ok {
			t.Fatal("rejected target activated")
		}
		if len(targets) == 2 && (!ok || intent.Target != "other:443" || intent.Action != ConnectSaved || intent.ResumeSessionID != "") {
			t.Fatalf("other target blocked/changed: %+v", intent)
		}
	}
}

func TestConnectActionTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason client.AuthReason
		cursor int
		want   ConnectAction
		resume bool
	}{
		{"ordinary saved", "", 0, ConnectSaved, false},
		{"expired same", client.AuthSessionExpired, 0, Reauthenticate, true},
		{"unusable same", client.AuthCredentialUnusable, 0, Reauthenticate, true},
		{"changed same", client.AuthTargetChanged, 0, Reauthenticate, true},
		{"storage same", client.AuthStorageUnavailable, 0, RetryAfterCleanup, true},
		{"not enrolled same", client.AuthNotEnrolled, 0, Reauthenticate, true},
		{"anonymous rejected same", client.AuthAnonymousRejected, 0, ConnectSaved, false},
		{"cleanup same", client.AuthCredentialCleanup, 0, RetryAfterCleanup, true},
		{"different target", client.AuthSessionExpired, 1, ConnectSaved, false},
		{"add target", client.AuthSessionExpired, 2, AddTarget, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := openTestConnect(newConnectModel([]ConnectTarget{{Target: "canonical.example:443"}, {Target: "other.example:443"}}), tc.reason, "canonical.example:443", "session-1")
			m.modal.(*connectState).list.SetCursor(tc.cursor)
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter})
			intent, ok := m.ConnectRestartIntent()
			if !ok || intent.Action != tc.want || (intent.ResumeSessionID != "") != tc.resume {
				t.Fatalf("intent=%+v ok=%v", intent, ok)
			}
		})
	}
}

func TestConnectLoadsIgnoreStaleGenerations(t *testing.T) {
	m := newConnectModel(nil)
	mm, load := m.openConnect()
	m = mm.(Model)
	first := m.modal.(*connectState)
	stale := load().(connectTargetsMsg)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	mm, load = m.openConnect()
	m = mm.(Model)
	current := m.modal.(*connectState)
	m = applyAll(m, stale)
	if !current.loading {
		t.Fatal("old instance load applied")
	}
	fresh := load().(connectTargetsMsg)
	fresh.targets = []ConnectTarget{{Target: "fresh"}}
	m = applyAll(m, fresh)
	fresh.targets = []ConnectTarget{{Target: "duplicate"}}
	m = applyAll(m, fresh)
	if current.loading || current.targets[0].Target != "fresh" {
		t.Fatal("duplicate response applied")
	}
	if first.request == stale.request {
		t.Fatal("close did not invalidate request")
	}
}
