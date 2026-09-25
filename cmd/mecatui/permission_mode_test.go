package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/embed"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/internal/app"
)

// deprecationLines runs the real pre-launch warning path and returns its lines.
func deprecationLines(cfg config) []string {
	var b strings.Builder
	warnDeprecatedPermissionFlags(&b, cfg)
	out := strings.TrimSpace(b.String())
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// TestADR_0365_DeprecatedAliasesStillResolve pins AC6.1 for mecatui: --mode,
// --posture, and --yolo resolve exactly as before and each emits exactly one
// deprecation WARN naming --permission-mode. --trust-project is not deprecated.
func TestADR_0365_DeprecatedAliasesStillResolve(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantPosture app.Posture
		wantMode    string
		wantToken   string
	}{
		{name: "mode", args: []string{"--mode", "plan"}, wantPosture: app.PostureStrict, wantMode: "plan", wantToken: "plan"},
		{name: "posture", args: []string{"--posture", "auto"}, wantPosture: app.PostureAuto, wantMode: "", wantToken: "auto"},
		{name: "yolo", args: []string{"--yolo"}, wantPosture: app.PostureYolo, wantMode: "", wantToken: "yolo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg, err := parseTransportFlagsTest(t, modeLocal, append([]string{"--mock", "--workspace", t.TempDir()}, tc.args...))
			if err != nil {
				t.Fatalf("parse %v: %v", tc.args, err)
			}
			if got := embeddedAuthoritativePosture(cfg); got != tc.wantPosture {
				t.Errorf("posture = %v, want %v", got, tc.wantPosture)
			}
			if mode, _ := cfg.requestedSessionMode(); mode != tc.wantMode {
				t.Errorf("requested session mode = %q, want %q", mode, tc.wantMode)
			}
			ac := embeddedConfig(cfg, nil)
			if ac.PermissionModeFlagSet {
				t.Error("a deprecated alias must not set PermissionModeFlagSet")
			}
			lines := deprecationLines(cfg)
			if len(lines) != 1 {
				t.Fatalf("deprecation WARNs = %d (%q), want exactly 1", len(lines), lines)
			}
			for _, want := range []string{"WARNING", tc.args[0], "--permission-mode " + tc.wantToken} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("WARN %q missing %q", lines[0], want)
				}
			}
		})
	}
	t.Run("each alias warns once", func(t *testing.T) {
		_, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--mock", "--workspace", t.TempDir(), "--mode", "plan", "--posture", "auto", "--yolo"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		lines := deprecationLines(cfg)
		if len(lines) != 3 {
			t.Fatalf("deprecation WARNs = %q, want one per alias", lines)
		}
		for i, flagName := range []string{"--mode", "--posture", "--yolo"} {
			if !strings.Contains(lines[i], flagName+" is deprecated") || !strings.Contains(lines[i], "--permission-mode") {
				t.Errorf("WARN %d = %q, want %s deprecation naming --permission-mode", i, lines[i], flagName)
			}
		}
	})
	t.Run("trust-project and the new flag do not warn", func(t *testing.T) {
		for _, args := range [][]string{{"--trust-project"}, {"--permission-mode", "auto"}, nil} {
			_, cfg, err := parseTransportFlagsTest(t, modeLocal, append([]string{"--mock", "--workspace", t.TempDir()}, args...))
			if err != nil {
				t.Fatalf("parse %v: %v", args, err)
			}
			if lines := deprecationLines(cfg); len(lines) != 0 {
				t.Errorf("%v emitted deprecation WARNs %q", args, lines)
			}
		}
	})
}

// TestADR_0365_PermissionModeConflictsWithAliases pins that --permission-mode
// combined with any deprecated alias is a startup error telling the operator to
// pass one, and that an unknown token fails fast naming the valid set.
func TestADR_0365_PermissionModeConflictsWithAliases(t *testing.T) {
	for _, alias := range [][]string{{"--mode", "plan"}, {"--posture", "auto"}, {"--yolo"}} {
		args := append([]string{"--mock", "--workspace", "/abs", "--permission-mode", "plan"}, alias...)
		_, _, err := parseTransportFlagsTest(t, modeLocal, args)
		if err == nil {
			t.Fatalf("%v: want a startup error", args)
		}
		if !strings.Contains(err.Error(), "--permission-mode cannot be combined with "+alias[0]) || !strings.Contains(err.Error(), "pass one") {
			t.Errorf("%v: error %q must name the conflict and say to pass one", args, err)
		}
	}
	_, _, err := parseTransportFlagsTest(t, modeLocal, []string{"--mock", "--workspace", "/abs", "--permission-mode", "accept-all"})
	if err == nil || !strings.Contains(err.Error(), "trusted-accept-edits") {
		t.Fatalf("unknown token error = %v, want the valid token list", err)
	}
	if _, _, err := parseTransportFlagsTest(t, modeLocal, []string{"--mock", "--workspace", "/abs", "--permission-mode", "plan", "--trust-project"}); err != nil {
		t.Errorf("--trust-project is not deprecated and must combine: %v", err)
	}
}

// TestADR_0365_ConnectRefusesPostureHalf pins the connect behaviour: the session
// half of a token applies to this client's sessions, while a non-strict posture
// half is refused because the posture belongs to the remote server.
func TestADR_0365_ConnectRefusesPostureHalf(t *testing.T) {
	_, cfg, err := parseTransportFlagsTest(t, modeConnect, []string{"--permission-mode", "accept-edits"})
	if err != nil {
		t.Fatalf("session-only token under connect: %v", err)
	}
	if mode, serverDefault := cfg.requestedSessionMode(); mode != "accept-edits" || serverDefault {
		t.Errorf("connect session half = %q (server default %v), want accept-edits", mode, serverDefault)
	}
	for _, tok := range []string{"trusted", "trusted-accept-edits", "auto", "yolo"} {
		_, _, err := parseTransportFlagsTest(t, modeConnect, []string{"--permission-mode", tok})
		if err == nil {
			t.Fatalf("connect --permission-mode %s: want an error", tok)
		}
		if !strings.Contains(err.Error(), "belongs to the remote server") || !strings.Contains(err.Error(), "mecated") {
			t.Errorf("connect --permission-mode %s error %q must name the server's ownership", tok, err)
		}
	}
}

// TestADR_0365_VocabularyMatchesComposition keeps the TUI's display copy of the
// token table identical to the composition layer's contract.
func TestADR_0365_VocabularyMatchesComposition(t *testing.T) {
	vocab := ui.PermissionModeVocabulary()
	names := app.PermissionModeNames()
	if len(vocab) != len(names) {
		t.Fatalf("TUI lists %d tokens, composition has %d", len(vocab), len(names))
	}
	for i, name := range names {
		tok, err := app.ParsePermissionMode(name)
		if err != nil {
			t.Fatal(err)
		}
		want := ui.PermissionModeEntry{
			Name:        tok.Name,
			Posture:     tok.Posture.String(),
			SessionMode: client.ModeString(client.ModeFromString(string(tok.SessionMode))),
		}
		if vocab[i] != want {
			t.Errorf("row %d = %+v, want %+v", i, vocab[i], want)
		}
	}
}

// TestADR_0365_DefaultsReproduceCurrentBehaviour pins AC6.3 for mecatui end to
// end: the parsed flags build the real embedded server, the real client creates a
// session through the real sessionAdapter, and the server-reported posture and
// session mode match what mecatui produced before ADR 0365.
func TestADR_0365_DefaultsReproduceCurrentBehaviour(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantPosture string
		wantMode    string
	}{
		{name: "unconfigured", args: nil, wantPosture: "strict", wantMode: "default"},
		{name: "mode plan", args: []string{"--mode", "plan"}, wantPosture: "strict", wantMode: "plan"},
		{name: "permission-mode plan", args: []string{"--permission-mode", "plan"}, wantPosture: "strict", wantMode: "plan"},
		{name: "permission-mode trusted-accept-edits", args: []string{"--permission-mode", "trusted-accept-edits"}, wantPosture: "trusted", wantMode: "accept-edits"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			workspace := t.TempDir()
			_, cfg, err := parseTransportFlagsTest(t, modeLocal, append([]string{"--mock", "--workspace", workspace}, tc.args...))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			ac := embeddedConfig(cfg, nil)
			ac.MockProvider = mockllm.New()
			ac.StoreDir = ""
			ac.MemoryDir = t.TempDir()
			ac.UserModelDir = t.TempDir()
			ac.ToolHiveEnabled = false
			srv, err := embed.Start(ctx, ac, embed.PerfConfig{})
			if err != nil {
				t.Fatalf("start embedded server: %v", err)
			}
			defer func() { _ = srv.Close() }()
			cl, err := client.Dial(client.DialConfig{Server: srv.Target()})
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = cl.Close() }()

			requested, _ := cfg.requestedSessionMode()
			adapter := &sessionAdapter{cl: cl, mode: requested}
			id, caps, _, err := adapter.CreateSession(ctx, client.ModelSelection{}, requested)
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			snap, err := adapter.GetSession(ctx, id)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if caps.Posture != tc.wantPosture {
				t.Errorf("posture = %q, want %q", caps.Posture, tc.wantPosture)
			}
			if snap.Mode != tc.wantMode {
				t.Errorf("session mode = %q, want %q", snap.Mode, tc.wantMode)
			}
		})
	}
}
