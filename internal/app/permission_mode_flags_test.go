package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

func TestPermissionModeFlagsValidate(t *testing.T) {
	cases := []struct {
		name    string
		flags   PermissionModeFlags
		wantErr string
	}{
		{"unset", PermissionModeFlags{}, ""},
		{"aliases alone", PermissionModeFlags{Posture: "auto", PostureSet: true, YoloSet: true}, ""},
		{"valid token", PermissionModeFlags{PermissionMode: "auto", PermissionModeSet: true}, ""},
		{"unknown token", PermissionModeFlags{PermissionMode: "bogus", PermissionModeSet: true}, "--permission-mode: unknown permission mode"},
		{"with posture", PermissionModeFlags{PermissionMode: "auto", PermissionModeSet: true, Posture: "auto", PostureSet: true}, "--permission-mode and --posture are both set"},
		{"with yolo", PermissionModeFlags{PermissionMode: "yolo", PermissionModeSet: true, YoloSet: true}, "--permission-mode and --yolo are both set"},
		{"unknown token wins over a conflict", PermissionModeFlags{PermissionMode: "bogus", PermissionModeSet: true, PostureSet: true}, "unknown permission mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.flags.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestPermissionModeFlagsWarnDeprecatedOncePerAlias(t *testing.T) {
	cases := []struct {
		name  string
		flags PermissionModeFlags
		want  []string
	}{
		{"none", PermissionModeFlags{PermissionMode: "auto", PermissionModeSet: true}, nil},
		{"posture default is silent", PermissionModeFlags{Posture: "auto"}, nil},
		{"posture", PermissionModeFlags{Posture: "strict", PostureSet: true}, []string{"--posture is DEPRECATED", "--permission-mode default"}},
		{"yolo", PermissionModeFlags{YoloSet: true}, []string{"--yolo is DEPRECATED", "--permission-mode yolo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.flags.WarnDeprecated(slogdiag.NewFromLogger(slog.New(slog.NewTextHandler(&buf, nil))))
			if n, want := strings.Count(buf.String(), "DEPRECATED"), min(len(tc.want), 1); n != want {
				t.Fatalf("logged %d deprecation WARNs, want %d: %s", n, want, buf.String())
			}
			for _, w := range tc.want {
				if !strings.Contains(buf.String(), w) {
					t.Fatalf("WARN lacks %q: %s", w, buf.String())
				}
			}
		})
	}
	var buf bytes.Buffer
	PermissionModeFlags{Posture: "yolo", PostureSet: true, YoloSet: true}.WarnDeprecated(slogdiag.NewFromLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	if n := strings.Count(buf.String(), "DEPRECATED"); n != 2 {
		t.Fatalf("--posture plus --yolo logged %d deprecation WARNs, want one each: %s", n, buf.String())
	}
	PermissionModeFlags{PostureSet: true}.WarnDeprecated(nil)
}

func TestPermissionModeHelpParameters(t *testing.T) {
	base := PermissionModeHelp(PermissionModeHelpOptions{DefaultToken: "default"})
	for _, want := range append(PermissionModeNames(), "process-wide", "new sessions", "ceiling",
		"accept-edits for plan, default, and accept-edits", "Default: default.", "--guardrails off", "--trust-project", "deprecated --posture.") {
		if !strings.Contains(base, want) {
			t.Fatalf("help lacks %q: %s", want, base)
		}
	}
	for _, unwanted := range []string{"MECATL_SANDBOX", "--yolo", "the run's session"} {
		if strings.Contains(base, unwanted) {
			t.Fatalf("help without options names %q: %s", unwanted, base)
		}
	}
	full := PermissionModeHelp(PermissionModeHelpOptions{DefaultToken: "auto", OneShot: true, RootRefusal: true, YoloAlias: true})
	for _, want := range []string{"Default: auto.", "the run's session starts in", "MECATL_SANDBOX=1 or IS_SANDBOX=1", "deprecated --posture and --yolo."} {
		if !strings.Contains(full, want) {
			t.Fatalf("help with every option lacks %q: %s", want, full)
		}
	}
}
