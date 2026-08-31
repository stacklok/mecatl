package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
	if strings.Contains(out, "Sign in to a new target") {
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
