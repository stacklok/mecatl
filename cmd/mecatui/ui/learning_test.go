package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type fakeLearningSettings struct {
	from, to, restart              string
	err                            error
	advanceCalls, sensitivityCalls int
}

func (f *fakeLearningSettings) Advance() (string, string, string, error) {
	f.advanceCalls++
	return f.from, f.to, f.restart, f.err
}

func (f *fakeLearningSettings) AdvanceSensitivity() (string, string, string, error) {
	f.sensitivityCalls++
	return f.from, f.to, f.restart, f.err
}

func TestLearningBuiltinUsesAdapterOwnedSelectionAndLabels(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	settings := &fakeLearningSettings{from: "Off", to: "Review", restart: "saved; restart mecatui"}
	m.deps.Learning = settings
	updated, _ := m.runLearning()
	m = updated.(Model)
	status := stripANSIstr(m.statusMsg)
	if settings.advanceCalls != 1 || settings.sensitivityCalls != 0 || !strings.Contains(status, "Off → Review") || !strings.Contains(status, "restart mecatui") {
		t.Fatalf("advance=%d sensitivity=%d status=%q", settings.advanceCalls, settings.sensitivityCalls, status)
	}
}

func TestLearningBuiltinSurfacesAdapterErrors(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	settings := &fakeLearningSettings{err: errors.New("change learning.mode on the server host")}
	m.deps.Learning = settings
	updated, _ := m.runLearning()
	m = updated.(Model)
	if settings.advanceCalls != 1 || settings.sensitivityCalls != 0 || !strings.Contains(stripANSIstr(m.statusMsg), "server host") {
		t.Fatalf("advance=%d sensitivity=%d status=%q", settings.advanceCalls, settings.sensitivityCalls, stripANSIstr(m.statusMsg))
	}
}

func TestLearningSensitivityBuiltinDispatchesSensitivityAdapter(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	settings := &fakeLearningSettings{from: "Balanced", to: "Eager", restart: "saved; restart mecatui"}
	m.deps.Learning = settings
	updated, _ := m.runLearningSensitivity()
	m = updated.(Model)
	status := stripANSIstr(m.statusMsg)
	if settings.advanceCalls != 0 || settings.sensitivityCalls != 1 || !strings.Contains(status, "Balanced → Eager") || !strings.Contains(status, "restart mecatui") {
		t.Fatalf("advance=%d sensitivity=%d status=%q", settings.advanceCalls, settings.sensitivityCalls, status)
	}
}

func TestLearningSensitivityBuiltinSurfacesAdapterErrors(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	settings := &fakeLearningSettings{err: errors.New("change learning.sensitivity on the server host")}
	m.deps.Learning = settings
	updated, _ := m.runLearningSensitivity()
	m = updated.(Model)
	status := stripANSIstr(m.statusMsg)
	if settings.advanceCalls != 0 || settings.sensitivityCalls != 1 || !strings.Contains(status, "server host") {
		t.Fatalf("advance=%d sensitivity=%d status=%q", settings.advanceCalls, settings.sensitivityCalls, status)
	}
}
