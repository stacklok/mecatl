package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type fakeLearningSettings struct {
	from, to, restart string
	err               error
	calls             int
}

func (f *fakeLearningSettings) Advance() (string, string, string, error) {
	f.calls++
	return f.from, f.to, f.restart, f.err
}

func TestLearningBuiltinUsesAdapterOwnedSelectionAndLabels(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	settings := &fakeLearningSettings{from: "Off", to: "Review", restart: "saved; restart mecatui"}
	m.deps.Learning = settings
	updated, _ := m.runLearning()
	m = updated.(Model)
	status := stripANSIstr(m.statusMsg)
	if settings.calls != 1 || !strings.Contains(status, "Off → Review") || !strings.Contains(status, "restart mecatui") {
		t.Fatalf("calls=%d status=%q", settings.calls, status)
	}
}

func TestLearningBuiltinSurfacesAdapterErrors(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	settings := &fakeLearningSettings{err: errors.New("change learning.mode on the server host")}
	m.deps.Learning = settings
	updated, _ := m.runLearning()
	m = updated.(Model)
	if settings.calls != 1 || !strings.Contains(stripANSIstr(m.statusMsg), "server host") {
		t.Fatalf("calls=%d status=%q", settings.calls, stripANSIstr(m.statusMsg))
	}
}
