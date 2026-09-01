package ui

import (
	"context"
	"fmt"
	"image/color"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func themeAutoDetectModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Theme:           aztec(),
		ThemeAutoDetect: true,
		Ctx:             t.Context(),
	})
	return applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 24})
}

func TestInitRequestsBackgroundColorOnlyWhenAutoDetectArmed(t *testing.T) {
	armed := newTestModelFromDeps(Deps{Theme: aztec(), ThemeAutoDetect: true, Ctx: t.Context()})
	batch, ok := armed.Init()().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("Init() = %#v, want a non-empty batch", batch)
	}
	if got := fmt.Sprintf("%T", batch[0]()); got != "tea.backgroundColorMsg" {
		t.Fatalf("first Init command produced %s, want tea.backgroundColorMsg", got)
	}

	unarmed := newTestModelFromDeps(Deps{Theme: aztec(), Ctx: t.Context()})
	if unarmed.themeAutoDetectArmed {
		t.Fatal("theme auto-detect armed when disabled")
	}
}

func TestOnBackgroundColorSwitchesEveryThemeConsumer(t *testing.T) {
	m := themeAutoDetectModel(t)
	mm, _ := m.Update(tea.BackgroundColorMsg{Color: color.White})
	m = mm.(Model)

	if m.deps.Theme.Name != "solar" || m.rend.th.Name != "solar" {
		t.Fatalf("themes after light response: model=%q renderer=%q", m.deps.Theme.Name, m.rend.th.Name)
	}
	wantSpinner := theme.Solar().Style("spinner").GetForeground()
	if got := m.sp.Style.GetForeground(); got != wantSpinner {
		t.Errorf("spinner foreground = %v, want %v", got, wantSpinner)
	}
	if m.themeAutoDetectArmed {
		t.Error("theme auto-detect remained armed after response")
	}
}

func TestOnBackgroundColorNoOps(t *testing.T) {
	t.Run("dark response", func(t *testing.T) {
		m := themeAutoDetectModel(t)
		mm, _ := m.Update(tea.BackgroundColorMsg{Color: color.Black})
		m = mm.(Model)
		if m.deps.Theme.Name != "aztec" || m.rend.th.Name != "aztec" {
			t.Fatalf("dark response changed theme: model=%q renderer=%q", m.deps.Theme.Name, m.rend.th.Name)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		m := newTestModelFromDeps(Deps{Theme: aztec(), Ctx: t.Context()})
		mm, _ := m.Update(tea.BackgroundColorMsg{Color: color.White})
		if got := mm.(Model).deps.Theme.Name; got != "aztec" {
			t.Fatalf("disabled auto-detect changed theme to %q", got)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		m := themeAutoDetectModel(t)
		mm, _ := m.Update(tea.BackgroundColorMsg{Color: color.White})
		m = mm.(Model)
		mm, _ = m.Update(tea.BackgroundColorMsg{Color: color.Black})
		if got := mm.(Model).deps.Theme.Name; got != "solar" {
			t.Fatalf("duplicate response changed theme to %q", got)
		}
	})
}

func TestThemeAutoDetectE2E(t *testing.T) {
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{
		recv: recv, send: &fakeSender{}, caps: embeddedCaps(),
		sessionReady: make(chan struct{}), created: make(chan struct{}),
	}
	prog := newProgress()
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: aztec(), ThemeAutoDetect: true,
		Server: "127.0.0.1:8080", Workspace: "/workspace", Mode: "default",
		Ctx: context.Background(), NoAltScreen: true, onPhase: prog.record,
	})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	waitClosed(t, "startup CreateSession", conv.created, 5*time.Second)
	prog.wait(t, phaseIdle, 5*time.Second)
	tm.Send(tea.BackgroundColorMsg{Color: color.White})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scaleWait(3*time.Second)))

	fm := tm.FinalModel(t).(Model)
	if fm.deps.Theme.Name != "solar" || fm.rend.th.Name != "solar" {
		t.Fatalf("final themes: model=%q renderer=%q, want solar", fm.deps.Theme.Name, fm.rend.th.Name)
	}
}
