package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestStartupPlacementFailureShowsStableRemediation(t *testing.T) {
	m := New(Deps{
		Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(),
		StartupFailureHint: func() string {
			return "next: run 'mecated microvm doctor'; inspect the mecatui diagnostics log for detailed cause"
		},
	})
	updated, _ := m.Update(client.ConnectErrMsg{Err: errors.New("server: placement unavailable: microvm readiness failed; stage=preflight; category=host_prerequisite; cause=KVM is unavailable to the current user, run microvm doctor for host remediation; remediation=run 'mecated microvm doctor' and inspect the mecatui/server diagnostics log, then retry")})
	got := updated.(Model)
	for _, want := range []string{"placement_unavailable", "stage=preflight", "category=host_prerequisite", "cause=KVM is unavailable", "mecated microvm doctor", "diagnostics log"} {
		if !strings.Contains(got.fatalErr, want) {
			t.Fatalf("fatal error %q omitted %q", got.fatalErr, want)
		}
	}
	if strings.Contains(got.fatalErr, "/private/root") {
		t.Fatalf("fatal error leaked server detail: %q", got.fatalErr)
	}
}

func TestStartupProgressIsVisibleAndTerminalSafeWhileConnecting(t *testing.T) {
	progress := make(chan string, 1)
	progress <- "Downloading microVM components\x1b[31m /private/path"
	m := New(Deps{
		Theme:           theme.New("aztec", theme.AztecPalette()),
		Ctx:             context.Background(),
		StartupProgress: progress,
	})
	msg := m.startupProgressCmd()()
	updated, next := m.Update(msg)
	got := updated.(Model)
	status := stripANSIstr(got.statusMsg)
	if !strings.Contains(status, "Downloading microVM components") || strings.Contains(status, "\x1b") {
		t.Fatalf("startup status = %q", status)
	}
	if view := stripANSIstr(got.View().Content); !strings.Contains(view, "Downloading microVM components") {
		t.Fatalf("startup progress is not visible while connecting:\n%s", view)
	}
	if next == nil {
		t.Fatal("startup progress listener was not re-armed")
	}
}
