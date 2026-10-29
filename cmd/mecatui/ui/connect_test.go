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
	return applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 40})
}

func openTestConnect(m Model, reason client.AuthReason, failed, resume string) Model {
	mm, cmd := m.openConnectRecovery("", reason, failed, resume)
	return applyAll(mm.(Model), cmd())
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
	if mm.(Model).modal != nil || cmd != nil {
		t.Fatal("/connect must be idle-only")
	}
	m.phase = phaseIdle
	m = openTestConnect(m, "", "", "")
	out := stripANSIstr(m.View().Content)
	for _, want := range []string{"remote.example:443", "https://issuer.example", "public-client", "mecatui"} {
		if !strings.Contains(out, want) {
			t.Fatalf("panel missing public metadata %q: %s", want, out)
		}
	}
}

func TestConnectRequiresConfirmationAndEmitsPublicIntent(t *testing.T) {
	m := openTestConnect(newConnectModel([]ConnectTarget{{Target: "remote.example:443"}}), "", "", "")
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m.ConnectRestartIntent(); ok {
		t.Fatal("first Enter restarted")
	}
	for _, want := range []string{"Connect to this saved target?", "enter: connect", "esc: back"} {
		if out := stripANSIstr(m.View().Content); !strings.Contains(out, want) {
			t.Fatalf("confirmation missing %q: %s", want, out)
		}
	}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	intent, ok := mm.(Model).ConnectRestartIntent()
	if cmd == nil || !ok || intent.Target != "remote.example:443" || intent.Action != ConnectSaved {
		t.Fatalf("intent = %+v, %v", intent, ok)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("restart did not quit")
	}
}

func TestConnectCopyNamesTheSelectedAction(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason client.AuthReason
		cursor int
		action ConnectAction
	}{
		{"saved", "", 0, ConnectSaved}, {"new", "", 1, AddTarget},
		{"expired", client.AuthSessionExpired, 0, Reauthenticate}, {"storage", client.AuthStorageUnavailable, 0, RetryAfterCleanup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := openTestConnect(newConnectModel([]ConnectTarget{{Target: "remote.example:443"}}), tc.reason, "remote.example:443", "")
			m.modal.(*connectState).list.SetCursor(tc.cursor)
			if out := stripANSIstr(m.View().Content); !strings.Contains(out, "enter: "+connectActionLabel(tc.action)) {
				t.Fatal(out)
			}
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if out := stripANSIstr(m.View().Content); !strings.Contains(out, connectConfirmationLabel(tc.action)) {
				t.Fatal(out)
			}
		})
	}
}

func TestConnectListFailureIsSanitized(t *testing.T) {
	m := newConnectModel(nil)
	m.deps.Connect = fakeConnect{err: errors.New("refresh token=secret")}
	m = openTestConnect(m, "", "", "")
	s := m.modal.(*connectState)
	if s.err != "saved targets are unavailable" || len(s.items) != 0 {
		t.Fatalf("error=%q items=%+v", s.err, s.items)
	}
	out := stripANSIstr(m.View().Content)
	if strings.Contains(out, "secret") || strings.Contains(out, "OIDC sign-in for a new target") {
		t.Fatal(out)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.connectIntent != nil || s.confirm != nil {
		t.Fatal("failed list allowed activation")
	}
}

func TestReauthenticateIntentCarriesNoBrowserState(t *testing.T) {
	m := New(Deps{Connect: fakeConnect{targets: []ConnectTarget{{Target: "remote.example:443"}}}, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), NoAltScreen: true, ConnectOpen: true, ConnectReason: client.AuthSessionExpired, ConnectTarget: "remote.example:443"})
	m = applyAll(m, m.Init()(), tea.WindowSizeMsg{Width: 120, Height: 40}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.KeyPressMsg{Code: tea.KeyEnter})
	intent, ok := m.ConnectRestartIntent()
	if !ok || intent.Action != Reauthenticate {
		t.Fatalf("intent=%+v ok=%v", intent, ok)
	}
}
