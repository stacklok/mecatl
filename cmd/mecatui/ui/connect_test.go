package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type fakeConnect struct {
	targets []ConnectTarget
	err     error
}

func (f fakeConnect) ListConnectTargets(context.Context) ([]ConnectTarget, error) {
	return f.targets, f.err
}

func newConnectModel(targets []ConnectTarget) Model {
	m := New(Deps{Connect: fakeConnect{targets: targets}, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), NoAltScreen: true})
	m.phase = phaseIdle
	return m
}

func TestConnectPaletteIdleAndMetadata(t *testing.T) {
	m := newConnectModel([]ConnectTarget{{Target: "remote.example:443", Issuer: "https://issuer.example", ClientID: "public-client", Audience: "mecatui"}})
	if _, ok := builtinByName(m.caps, m.wiredCollaborators(), "connect"); !ok {
		t.Fatal("/connect missing from palette")
	}
	if !strings.Contains(stripANSIstr(helpBody(m.deps.Theme, m.caps, m.helpKeyMarkings())), "/connect") {
		t.Fatal("help missing /connect")
	}
	m.phase = phaseRunning
	mm, cmd := m.runConnect()
	if mm.(Model).connect.open || cmd != nil {
		t.Fatal("/connect must be idle-only")
	}
	m.phase = phaseIdle
	mm, cmd = m.runConnect()
	m = mm.(Model)
	m = applyAll(m, cmd())
	out := stripANSIstr(m.View().Content)
	for _, want := range []string{"remote.example:443", "https://issuer.example", "public-client", "mecatui"} {
		if !strings.Contains(out, want) {
			t.Fatalf("panel missing public metadata %q: %s", want, out)
		}
	}
	if strings.Contains(strings.ToLower(out), "token") {
		t.Fatalf("panel leaked secret-shaped text: %s", out)
	}
}

func TestConnectRequiresConfirmationAndEmitsPublicIntent(t *testing.T) {
	m := newConnectModel([]ConnectTarget{{Target: "remote.example:443", Issuer: "https://issuer.example", ClientID: "public-client", Audience: "mecatui"}})
	mm, cmd := m.runConnect()
	m = applyAll(mm.(Model), cmd())
	mm, cmd, handled := m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || cmd != nil || mm.(Model).connectIntent != nil {
		t.Fatal("first enter must only request confirmation")
	}
	m = mm.(Model)
	confirm := stripANSIstr(m.View().Content)
	for _, want := range []string{"Connect to this saved target?", "enter: connect", "esc: back"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, confirm)
		}
	}
	mm, cmd, _ = m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("confirmed selection must quit")
	}
	intent, ok := m.ConnectRestartIntent()
	if !ok || intent.Target != "remote.example:443" || intent.Action != ConnectSaved {
		t.Fatalf("intent = %#v, %v", intent, ok)
	}
	if strings.Contains(intent.Target, "token") {
		t.Fatal("intent target contains secret-shaped data")
	}
}

func TestConnectCopyNamesTheSelectedAction(t *testing.T) {
	target := ConnectTarget{Target: "remote.example:443"}
	for _, tc := range []struct {
		name     string
		reason   client.AuthReason
		cursor   int
		action   string
		question string
		rejected bool
	}{
		{name: "saved target", action: "connect", question: "Connect to this saved target?"},
		{name: "new target", cursor: 1, action: "sign in", question: "Sign in to a new target?"},
		{name: "new target after rejection", reason: client.AuthRejected, cursor: 1, action: "sign in", question: "Sign in to a new target?"},
		{name: "expired credential", reason: client.AuthSessionExpired, action: "sign in again", question: "Sign in again and connect?"},
		{name: "storage recovery", reason: client.AuthStorageUnavailable, action: "retry connection", question: "Retry connection without opening a browser?"},
		{name: "rejected credential", reason: client.AuthRejected, rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newConnectModel([]ConnectTarget{target})
			m.connect.reason = tc.reason
			m.connect.failedTarget = target.Target
			mm, cmd := m.openConnect()
			m = applyAll(mm.(Model), cmd())
			m.connect.cursor = tc.cursor
			out := stripANSIstr(m.View().Content)
			if tc.rejected {
				if strings.Contains(out, "enter:") || !strings.Contains(out, "esc: close") {
					t.Fatalf("rejected credential advertised an unavailable action:\n%s", out)
				}
				mm, cmd, handled := m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				m = mm.(Model)
				if !handled || cmd != nil || m.connect.confirm || !strings.Contains(m.connect.err, "rejected") {
					t.Fatalf("rejected credential Enter = handled=%v cmd=%v state=%+v", handled, cmd, m.connect)
				}
				return
			}
			if !strings.Contains(out, "enter: "+tc.action) || !strings.Contains(out, "esc: close") {
				t.Fatalf("connect hint missing selected action %q:\n%s", tc.action, out)
			}
			if strings.Contains(out, "enter continues") {
				t.Fatalf("connect hint still uses vague continuation copy:\n%s", out)
			}
			mm, cmd, handled := m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			if !handled || cmd != nil || !m.connect.confirm {
				t.Fatalf("first Enter did not open confirmation: handled=%v cmd=%v state=%+v", handled, cmd, m.connect)
			}
			confirm := stripANSIstr(m.View().Content)
			for _, want := range []string{tc.question, "enter: " + tc.action, "esc: back"} {
				if !strings.Contains(confirm, want) {
					t.Fatalf("confirmation missing %q:\n%s", want, confirm)
				}
			}
		})
	}
}

func TestConnectListFailureIsSanitized(t *testing.T) {
	m := New(Deps{Connect: fakeConnect{err: errors.New("refresh token=secret")}, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), NoAltScreen: true})
	m.phase = phaseIdle
	mm, cmd := m.runConnect()
	m = applyAll(mm.(Model), cmd())
	if m.connect.err != "saved targets are unavailable" {
		t.Fatalf("error = %q", m.connect.err)
	}
	out := stripANSIstr(m.View().Content)
	if strings.Contains(out, "secret") {
		t.Fatal("raw list error leaked")
	}
	if strings.Contains(out, "OIDC sign-in for a new target") {
		t.Fatalf("storage-unavailable panel offered enrollment: %s", out)
	}
	for range 2 {
		mm, cmd, handled := m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = mm.(Model)
		if !handled || cmd != nil || m.connect.confirm || m.connectIntent != nil {
			t.Fatalf("enter must not confirm unavailable targets: connect=%#v intent=%#v cmd=%v", m.connect, m.connectIntent, cmd)
		}
	}
}

// TestReauthenticateIntentCarriesNoBrowserState pins that ConnectRestartIntent
// itself carries no NoBrowser field: ADR 0271's contract that the recovery
// overlay never opens a browser is now enforced unconditionally at the
// cmd/mecatui composition boundary (restartFromConnectIntentWith), not by a
// value threaded through the intent.
func TestReauthenticateIntentCarriesNoBrowserState(t *testing.T) {
	m := New(Deps{
		Connect:       fakeConnect{targets: []ConnectTarget{{Target: "remote.example:443"}}},
		Theme:         theme.New("aztec", theme.AztecPalette()),
		Ctx:           context.Background(),
		NoAltScreen:   true,
		ConnectOpen:   true,
		ConnectReason: client.AuthSessionExpired,
		ConnectTarget: "remote.example:443",
	})
	model, _ := m.Update(m.Init()())
	m = model.(Model)
	for range 2 {
		mm, _, handled := m.onConnectKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !handled {
			t.Fatal("enter not handled")
		}
		m = mm.(Model)
	}
	intent, ok := m.ConnectRestartIntent()
	if !ok || intent.Action != Reauthenticate {
		t.Fatalf("intent = %#v, ok=%v, want Reauthenticate", intent, ok)
	}
}
