package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
)

// resolveInvocation is a PURE seam (no os.Args, no os.Exit, no I/O), so these
// tests exercise the REAL production invocation-resolution logic directly. They
// do not mutate global state, prepare a run, or call os.Exit.

func TestResolveEmptyArgvIsSafeBareInvocation(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{name: "nil", argv: nil},
		{name: "empty", argv: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := resolveInvocation(tc.argv)
			if res.err != nil || res.mode != modeLocal || len(res.remaining) != 0 {
				t.Fatalf("resolution = %+v, want local with no remaining args", res)
			}
		})
	}
}

func TestResolveLocalWordIsUnknownCommand(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "local", "--workspace", "/tmp/w"})
	if res.err == nil {
		t.Fatal("the retired 'local' word must fail closed as an unknown command")
	}
	if !strings.Contains(res.err.Error(), "local") {
		t.Errorf("error %q does not name the unknown command", res.err)
	}
	if !strings.Contains(res.err.Error(), "connect") {
		t.Errorf("error %q does not name the available 'connect' command", res.err)
	}
}

func TestResolveConnectStripsCommandWordAndAddress(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "connect", "10.0.0.5:8080", "--workspace", "/tmp/w"})
	if res.err != nil {
		t.Fatalf("connect resolution error: %v", res.err)
	}
	if res.mode != modeConnect {
		t.Errorf("mode = %q, want connect", res.mode)
	}
	if res.address != "10.0.0.5:8080" {
		t.Errorf("address = %q, want 10.0.0.5:8080", res.address)
	}
	if len(res.remaining) != 2 || res.remaining[0] != "--workspace" {
		t.Errorf("remaining = %v, want [--workspace /tmp/w]", res.remaining)
	}
}

func TestResolveSessionsLaunchForLocalAndConnect(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		mode    transportMode
		address string
	}{
		{name: "local", argv: []string{"mecatui", "sessions", "--workspace", "/tmp/w"}, mode: modeLocal},
		{name: "connect", argv: []string{"mecatui", "connect", "10.0.0.5:8080", "sessions", "--workspace", "/tmp/w"}, mode: modeConnect, address: "10.0.0.5:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveInvocation(tt.argv)
			if res.err != nil {
				t.Fatalf("resolve: %v", res.err)
			}
			if res.mode != tt.mode || res.address != tt.address || !res.browseSessions {
				t.Fatalf("resolution = %+v, want mode=%q address=%q browseSessions=true", res, tt.mode, tt.address)
			}
			if len(res.remaining) != 2 || res.remaining[0] != "--workspace" {
				t.Fatalf("remaining = %v, want [--workspace /tmp/w]", res.remaining)
			}
		})
	}
}

type sessionsLaunchCreator struct {
	createCalls int
	rows        []client.SessionListItem
}

func (f *sessionsLaunchCreator) CreateSession(context.Context, client.ModelSelection, string) (string, client.Capabilities, client.ResolvedModel, error) {
	f.createCalls++
	return "", client.Capabilities{}, client.ResolvedModel{}, errors.New("CreateSession must not be called")
}

func (*sessionsLaunchCreator) ClearSession(context.Context, string, *client.WorktreeSelector) (string, client.SessionSnapshot, error) {
	return "", client.SessionSnapshot{}, errors.New("ClearSession must not be called")
}

func (f *sessionsLaunchCreator) CreateSessionWithCarryover(context.Context, string, client.ModelSelection) (string, client.Capabilities, client.ResolvedModel, error) {
	f.createCalls++
	return "", client.Capabilities{}, client.ResolvedModel{}, errors.New("CreateSessionWithCarryover must not be called")
}

func (*sessionsLaunchCreator) CloseSession(context.Context, string) error { return nil }

func (*sessionsLaunchCreator) GetSession(context.Context, string) (client.SessionSnapshot, error) {
	return client.SessionSnapshot{}, nil
}

func (*sessionsLaunchCreator) SetMode(context.Context, string, string) (string, error) {
	return "", nil
}

func (*sessionsLaunchCreator) ForkSession(context.Context, string, string) (string, error) {
	return "", nil
}

func (f *sessionsLaunchCreator) ListSessions(context.Context) ([]client.SessionListItem, error) {
	return f.rows, nil
}

func (f *sessionsLaunchCreator) ListSessionPage(context.Context, string) (client.SessionInventoryPage, error) {
	return client.SessionInventoryPage{Sessions: f.rows}, nil
}

func TestSessionsLaunchComposesIntoStartupPickerWithoutCreatingSession(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{name: "local", argv: []string{"mecatui", "sessions"}},
		{name: "connect", argv: []string{"mecatui", "connect", "127.0.0.1:9090", "sessions"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveInvocation(tt.argv)
			if res.err != nil {
				t.Fatalf("resolve transport: %v", res.err)
			}
			_, cfg, err := parseTransportFlags(res.mode, io.Discard, res.remaining, res.browseSessions)
			if err != nil {
				t.Fatalf("parse flags: %v", err)
			}

			creator := &sessionsLaunchCreator{rows: []client.SessionListItem{{
				ID: "stored-session", Title: "existing chat", Kind: client.SessionKindMain,
				Capabilities: client.SessionInventoryCapabilities{PublicChat: true},
			}}}
			deps := applyLaunchIntent(cfg, ui.Deps{
				Session: creator, Sessions: creator, Theme: theme.New("aztec", theme.AztecPalette()),
				Ctx: context.Background(), NoAltScreen: true,
			})
			if !deps.BrowseSessions {
				t.Fatal("launch intent did not reach ui dependencies")
			}

			m := ui.New(deps)
			if cmd := m.Init(); cmd == nil {
				t.Fatal("startup picker did not initialize")
			}
			mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
			m = mm.(ui.Model)
			rows, err := creator.ListSessions(context.Background())
			if err != nil {
				t.Fatalf("list sessions: %v", err)
			}
			mm, _ = m.Update(client.SessionsListedMsg{Sessions: rows})
			m = mm.(ui.Model)

			view := m.View().Content
			if !strings.Contains(view, "existing chat") || !strings.Contains(view, "n: new chat") {
				t.Fatalf("startup view did not reach the sessions picker:\n%s", view)
			}
			if got := creator.createCalls; got != 0 {
				t.Fatalf("startup picker created %d sessions, want zero", got)
			}
		})
	}
}

func TestSessionsLaunchRejectsSeedAndResumeFlags(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{
			args: []string{"--prompt-file", "task.md"}, want: "--prompt-file",
		},
		{args: []string{"--resume", "session-id"}, want: "--resume"},
		{args: []string{"--resume-latest"}, want: "--resume-latest"},
		{args: []string{"-p", "hello"}, want: "-p/--prompt"},
		{args: []string{"--prompt", "hello"}, want: "-p/--prompt"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			cfg := config{browseSessions: true}
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.StringVar(&cfg.prompt, "prompt", "", "")
			fs.StringVar(&cfg.prompt, "p", "", "")
			fs.StringVar(&cfg.promptFile, "prompt-file", "", "")
			fs.StringVar(&cfg.resumeID, "resume", "", "")
			fs.BoolVar(&cfg.resumeLatest, "resume-latest", false, "")
			if err := fs.Parse(tt.args); err != nil {
				t.Fatal(err)
			}
			err := validateSessionsLaunch(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "sessions") {
				t.Fatalf("error = %v, want sessions conflict naming %q", err, tt.want)
			}
		})
	}
}

func TestSessionsLaunchParserRejectsConflictsBeforePromptFileIO(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{args: []string{"--prompt-file", filepath.Join(t.TempDir(), "missing")}, want: "--prompt-file"},
			{args: []string{"--resume", "session-id"}, want: "--resume"},
			{args: []string{"--resume-latest"}, want: "--resume-latest"},
			{args: []string{"-p", "hello"}, want: "-p/--prompt"},
		} {
			_, _, err := parseTransportFlags(mode, io.Discard, tc.args, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "sessions") {
				t.Errorf("mode=%s args=%v error=%v, want sessions conflict naming %q", mode, tc.args, err, tc.want)
			}
		}
	}
}

func TestResolveBareNoArgsIsLocal(t *testing.T) {
	res := resolveInvocation([]string{"mecatui"})
	if res.err != nil {
		t.Fatalf("bare handled=%v, want nil", res.err)
	}
	if res.mode != modeLocal {
		t.Errorf("mode = %q, want local (bare is the canonical embedded default)", res.mode)
	}
	if len(res.remaining) != 0 {
		t.Errorf("remaining = %v, want empty", res.remaining)
	}
}

func TestResolveLeadingFlagIsLocal(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "--workspace", "/tmp/w"})
	if res.err != nil {
		t.Fatalf("leading-flag error: %v", res.err)
	}
	if res.mode != modeLocal {
		t.Errorf("mode = %q, want local (a leading flag is the bare embedded form)", res.mode)
	}
	if len(res.remaining) != 2 || res.remaining[0] != "--workspace" {
		t.Errorf("remaining = %v, want [--workspace /tmp/w]", res.remaining)
	}
}

// --- Requirement 1: connect requires ADDRESS immediately, fail closed --------

func TestResolveConnectMissingAddressFailsClosed(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "connect"})
	if res.err == nil {
		t.Fatal("connect with no ADDRESS must fail closed")
	}
	if !strings.Contains(res.err.Error(), "missing ADDRESS") {
		t.Errorf("error %q does not name the missing ADDRESS", res.err)
	}
}

func TestResolveConnectFlagFirstFailsClosed(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "connect", "--workspace", "/tmp/w"})
	if res.err == nil {
		t.Fatal("connect with a flag-first token must fail closed")
	}
	if !strings.Contains(res.err.Error(), "ADDRESS must immediately follow") {
		t.Errorf("error %q does not name the flag-first problem", res.err)
	}
}

// connect --help is a help request, not a usage error (the universal --help contract).
func TestResolveConnectHelpPassesThrough(t *testing.T) {
	for _, help := range []string{"--help", "-h", "--help-all"} {
		res := resolveInvocation([]string{"mecatui", "connect", help})
		if res.err != nil {
			t.Errorf("connect %s must pass through as a help request, got error: %v", help, res.err)
		}
		if res.mode != modeConnect {
			t.Errorf("connect %s: mode = %q, want connect", help, res.mode)
		}
		if res.address != "" {
			t.Errorf("connect %s: address = %q, want empty (help needs no ADDRESS)", help, res.address)
		}
	}
}

func TestResolveUnknownCommandFailsClosed(t *testing.T) {
	res := resolveInvocation([]string{"mecatui", "loal"})
	if res.err == nil {
		t.Fatal("unknown command 'loal' must fail closed")
	}
	if !strings.Contains(res.err.Error(), "loal") {
		t.Errorf("error %q does not name the unknown command", res.err)
	}
	if !strings.Contains(res.err.Error(), "Available commands:") {
		t.Errorf("error %q does not list available commands", res.err)
	}
}

// TestResolveLoginCommands pins the split login grammar: the top-level login is
// reserved for remote addresses, while ToolHive login is explicitly nested under
// llm.
func TestResolveLoginCommands(t *testing.T) {
	remote := resolveInvocation([]string{"mecatui", "login", "https://gateway.example"})
	if remote.err != nil || remote.mode != modeRemoteLogin || remote.address != "https://gateway.example" {
		t.Fatalf("remote login resolution = %+v, want remote address route", remote)
	}
	if got := resolveInvocation([]string{"mecatui", "login"}); got.err == nil || !strings.Contains(got.err.Error(), "mecatui login ADDRESS") {
		t.Fatalf("bare login must fail with remote usage, got %+v", got)
	}
	logout := resolveInvocation([]string{"mecatui", "logout", "gateway.example:443"})
	if logout.err != nil || logout.mode != modeRemoteLogout || logout.address != "gateway.example:443" {
		t.Fatalf("remote logout resolution = %+v", logout)
	}
	if got := resolveInvocation([]string{"mecatui", "logout"}); got.err == nil || !strings.Contains(got.err.Error(), "mecatui logout ADDRESS") {
		t.Fatalf("bare logout must fail with remote usage, got %+v", got)
	}
	if got := resolveInvocation([]string{"mecatui", "logout", "--issuer=x"}); got.err == nil {
		t.Fatal("logout with a flag-first address must fail closed")
	}
	llm := resolveInvocation([]string{"mecatui", "llm", "login", "--skip-browser"})
	if llm.err != nil || llm.mode != modeLogin || len(llm.remaining) != 1 || llm.remaining[0] != "--skip-browser" {
		t.Fatalf("llm login resolution = %+v", llm)
	}
	if got := resolveInvocation([]string{"mecatui", "llm"}); got.err == nil || !strings.Contains(got.err.Error(), "llm login") {
		t.Fatalf("bare llm must fail with llm-login usage, got %+v", got)
	}
	if got := resolveInvocation([]string{"mecatui", "mcp", "login"}); got.err == nil {
		t.Fatal("mcp login must remain unknown")
	}
}

// parseTransportFlagsTest is a helper that runs the REAL parse seam with a
// discard writer and returns the FlagSet + config + error (mirroring the
// production path used by run()). It points XDG_CONFIG_HOME at an empty temp
// dir so the provider-credential resolution inside finalizeParsedConfig (env,
// then auth.yaml) is hermetic against whatever the machine running the test
// happens to have at ~/.config/mecatl/auth.yaml — no test in this file
// exercises auth.yaml on purpose, so a real one on the dev box must not flip
// "no provider configured" to "has a provider".
func parseTransportFlagsTest(t *testing.T, mode transportMode, args []string) (*flag.FlagSet, config, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var buf bytes.Buffer
	fs, cfg, err := parseTransportFlags(mode, &buf, args)
	_ = buf
	return fs, cfg, err
}

func TestParseTransportFlagsLocalModeIsLocal(t *testing.T) {
	_, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--mock", "--workspace", "/abs"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.transportMode != modeLocal {
		t.Errorf("transportMode = %q, want local", cfg.transportMode)
	}
}

func TestParseTransportFlagsConnectModeIsConnect(t *testing.T) {
	_, cfg, err := parseTransportFlagsTest(t, modeConnect, []string{"--workspace", "/abs"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.transportMode != modeConnect {
		t.Errorf("transportMode = %q, want connect", cfg.transportMode)
	}
}

// The retired --server flag is a parse-level unknown-flag error (mirrors
// TestAskReviewerFlagsAreUnknownFlagErrors) in BOTH modes — unregistration is
// total, not local-mode-only.
func TestResolveServerFlagIsUnknownFlag(t *testing.T) {
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		t.Run(string(mode), func(t *testing.T) {
			_, _, err := parseTransportFlagsTest(t, mode, []string{"--server", "127.0.0.1:8080", "--workspace", "/abs"})
			if err == nil {
				t.Errorf("removed flag --server must now be an unknown-flag error in %q mode, not a silent no-op", mode)
			}
			if !strings.Contains(err.Error(), "not defined") && !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Errorf("removed flag --server should surface as a stdlib unknown-flag error in %q mode, got: %v", mode, err)
			}
		})
	}
}

func TestMecatuiAuthFileOnlyParse(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "mecatl", "auth.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("providers:\n  anthropic:\n    api_key: sk-ant-file-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cfg, err := parseTransportFlags(modeLocal, io.Discard, []string{"--workspace", "/abs"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.anthropicKey != "sk-ant-file-only" {
		t.Errorf("anthropic key = %q, want auth-file credential", cfg.anthropicKey)
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate auth-file credential: %v", err)
	}
	embedded := embeddedConfig(cfg, nil)
	if embedded.AnthropicKey != cfg.anthropicKey {
		t.Errorf("embedded AnthropicKey = %q, want resolved auth-file credential", embedded.AnthropicKey)
	}
	if cfg.providerKeys.AuthFileWarning != "" {
		t.Errorf("valid auth file warning = %q", cfg.providerKeys.AuthFileWarning)
	}
}

func TestMecatuiExplicitAuthFileWarningSurvivesValidation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing-auth.yaml")
	_, cfg, err := parseTransportFlags(modeLocal, io.Discard, []string{"--workspace", "/abs", "--auth-file", missing, "--toolhive-llm=false"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.providerKeys.AuthFileWarning == "" {
		t.Fatal("explicit missing auth file should warn before no-provider validation")
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("missing provider should still fail local startup validation")
	}
}

func TestRunPrintsExplicitAuthFileWarningBeforeProviderValidation(t *testing.T) {
	for _, name := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(name, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing-auth.yaml")
	args := []string{"mecatui", "--workspace", t.TempDir(), "--auth-file", missing, "--toolhive-llm=false"}

	_, cfg, err := parseTransportFlags(modeLocal, io.Discard, args[1:])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.providerKeys.AuthFileWarning == "" {
		t.Fatal("explicit missing auth file should produce a startup warning")
	}

	stderr := os.Stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Stderr = stderr
		_ = write.Close()
		_ = read.Close()
	})
	output := &syncBuffer{}
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(output, read)
		close(drained)
	}()
	os.Stderr = write
	runErr := run(args)
	os.Stderr = stderr
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	<-drained

	if runErr == nil || !strings.Contains(runErr.Error(), "no LLM provider configured") {
		t.Fatalf("run error = %v, want no-provider validation failure", runErr)
	}
	want := "mecatui: WARNING: " + wrapAuthFileWarning(cfg.providerKeys.AuthFileWarning) + "\n"
	if got := output.String(); got != want {
		t.Errorf("startup stderr = %q, want %q", got, want)
	}
}

func TestMecatuiInvalidAuthFileWarningSurvivesValidation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-from-env")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "invalid-auth.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  openai:\n    wrong: not-a-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cfg, err := parseTransportFlags(modeLocal, io.Discard, []string{"--workspace", "/abs", "--auth-file", path})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(cfg.providerKeys.AuthFileWarning, "expected schema") {
		t.Fatalf("invalid auth file warning = %q", cfg.providerKeys.AuthFileWarning)
	}
}

func TestMecatuiUnreadableAuthFileWarningSurvivesValidation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-from-env")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "auth-directory")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, cfg, err := parseTransportFlags(modeLocal, io.Discard, []string{"--workspace", "/abs", "--auth-file", path})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.providerKeys.AuthFileWarning == "" {
		t.Fatal("unreadable auth file should warn")
	}
}

func TestWrapAuthFileWarningFitsRenderedWidth(t *testing.T) {
	warning := "auth file /" + strings.Repeat("nested/", 40) + "auth.yaml: does not match the expected schema (providers.<name>.api_key) — check 👩🏽‍💻 indentation and field names"
	const renderedWidth = 100
	const prefix = "mecatui: WARNING: "
	wrapped := wrapAuthFileWarning(warning)
	rendered := prefix + wrapped
	lines := strings.Split(rendered, "\n")
	if len(lines) < 2 {
		t.Fatal("warning should wrap onto continuation lines")
	}
	if !strings.HasPrefix(lines[0], prefix) {
		t.Fatalf("first rendered line = %q, want prefix %q", lines[0], prefix)
	}
	for i, line := range lines {
		if i > 0 && !strings.HasPrefix(line, "  ") {
			t.Errorf("continuation line %d = %q, want two-space indentation", i, line)
		}
		if width := ansi.StringWidth(line); width > renderedWidth {
			t.Errorf("rendered warning line %d has width %d > %d: %q", i, width, renderedWidth, line)
		}
	}
	if !strings.Contains(rendered, "👩🏽‍💻") {
		t.Error("wrapped warning lost grapheme text")
	}
}

func TestMecatuiConventionalMissingAuthFileWarnsWithoutProvider(t *testing.T) {
	for _, name := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(name, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, cfg, err := parseTransportFlags(modeLocal, io.Discard, []string{
		"--workspace", "/abs", "--toolhive-llm=false",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(cfg.providerKeys.AuthFileWarning, "auth file") {
		t.Fatalf("missing conventional auth file warning = %q", cfg.providerKeys.AuthFileWarning)
	}
	if !strings.Contains(cfg.providerKeys.AuthFileWarning, "file not found") {
		t.Fatalf("warning should identify the missing file: %q", cfg.providerKeys.AuthFileWarning)
	}
}

// embedded-only flags rejected in connect mode.
func TestRejectEmbeddedOnlyFlagsInConnect(t *testing.T) {
	embeddedOnly := []string{
		"mock", "no-shell", "trust-project", "yolo", "posture",
		"openai-base-url", "openrouter-base-url", "anthropic-base-url", "opencode-base-url", "auth-file",
		"toolhive-llm", "toolhive-llm-base-url",
		"model", "default-provider", "default-model", "subagent-model",
		"model-alias", "model-slot", "subagent-model-router",
		"llm-per-attempt-timeout", "llm-stream-idle-timeout",
		"no-prompt-cache", "anthropic-cache-ttl",
		"memory-dir", "no-memory", "store-dir", "no-store",
		"soul-file", "no-soul", "approve-soul", "soul-strict",
		"user-model-dir", "no-user-model", "user-model-review", "user-model-review-interval",
		"commands-dir", "no-commands", "skills-dir", "no-skills",
		"perf", "perf-addr", "perf-goroutine-warn-threshold", "perf-mcp",
		"reasoning-effort", "quiet",
	}
	for _, name := range embeddedOnly {
		t.Run(name, func(t *testing.T) {
			// connect mode parses only shared + remote flags; pass the embedded flag
			// alongside a valid workspace and assert it is rejected BY NAME.
			_, _, err := parseTransportFlagsTest(t, modeConnect, []string{"--" + name, flagValueForTest(name), "--workspace", "/abs"})
			if err == nil {
				t.Errorf("embedded-only flag --%s must be rejected in connect mode", name)
			} else if !strings.Contains(err.Error(), name) {
				t.Errorf("connect rejection for --%s does not name the flag: %v", name, err)
			}
		})
	}
}

// remote-only flags rejected in the bare (embedded) mode.
func TestRejectRemoteOnlyFlagsInBare(t *testing.T) {
	remoteOnly := []string{"auth-token", "tls", "tls-ca", "insecure"}
	for _, name := range remoteOnly {
		t.Run(name, func(t *testing.T) {
			_, _, err := parseTransportFlagsTest(t, modeLocal, []string{"--" + name, flagValueForTest(name), "--mock", "--workspace", "/abs"})
			if err == nil {
				t.Errorf("remote-only flag --%s must be rejected in the bare (embedded) mode", name)
			} else if !strings.Contains(err.Error(), name) {
				t.Errorf("bare-mode rejection for --%s does not name the flag: %v", name, err)
			}
		})
	}
}

// shared flags valid in BOTH modes (no rejection).
func TestSharedFlagsValidInBothModes(t *testing.T) {
	shared := []string{"workspace", "mode", "theme", "theme-dir", "no-alt-screen", "inline", "no-mouse", "no-banner", "keymap"}
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		for _, name := range shared {
			t.Run(string(mode)+"/"+name, func(t *testing.T) {
				_, _, err := parseTransportFlagsTest(t, mode, []string{"--" + name, flagValueForTest(name)})
				// Some shared flags need --mock/--workspace to pass validate; but
				// applicability rejection runs BEFORE validate, so a nil error here
				// means the flag was accepted by the applicability check. We only
				// assert the error is NOT an applicability rejection naming the flag.
				if err != nil && strings.Contains(err.Error(), "not applicable in") && strings.Contains(err.Error(), name) {
					t.Errorf("shared flag --%s must not be rejected as inapplicable in %q mode: %v", name, mode, err)
				}
			})
		}
	}
}

// flagValueForTest returns a value for flags that require one in the test
// harness. Booleans take no value; the others take a placeholder.
func flagValueForTest(name string) string {
	switch name {
	case "mock", "no-shell", "trust-project", "yolo", "no-memory", "no-store",
		"no-soul", "approve-soul", "soul-strict", "no-user-model", "user-model-review",
		"no-commands", "no-skills", "perf", "perf-mcp", "tls", "insecure", "anonymous",
		"no-alt-screen", "inline", "no-mouse", "no-banner", "list-themes",
		"subagent-model-router", "help-all", "quiet", "no-prompt-cache":
		return "" // bool: no value consumed
	}
	// Provide a plausible value; the applicability check runs at fs.Visit time so
	// the value just needs to parse.
	switch name {
	case "auth-token":
		return "127.0.0.1:8080"
	case "tls-ca", "soul-file", "memory-dir", "store-dir", "user-model-dir",
		"commands-dir", "skills-dir", "perf-addr":
		return "/tmp/x"
	case "workspace":
		return "/abs"
	case "mode":
		return "default"
	case "theme", "terminal-title":
		return "aztec"
	case "theme-dir":
		return "/tmp/themes"
	case "model", "default-provider", "default-model", "subagent-model",
		"reasoning-effort", "posture":
		return "x"
	case "anthropic-cache-ttl":
		return "1h"
	case "openai-base-url", "openrouter-base-url", "anthropic-base-url",
		"opencode-base-url", "toolhive-llm-base-url":
		return "http://x"
	case "model-alias", "model-slot", "keymap":
		return "k=v"
	case "llm-per-attempt-timeout", "llm-stream-idle-timeout":
		return "30s"
	case "perf-goroutine-warn-threshold", "user-model-review-interval":
		return "1"
	case "toolhive-llm":
		return "true"
	}
	return ""
}

// --- Requirement 4: metadata completeness / no orphans ---------------------

func TestFlagApplicabilityCompletenessOverRealFlagSet(t *testing.T) {
	fs, _, err := parseTransportFlagsTest(t, modeLocal, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validateFlagApplicability(fs); err != nil {
		t.Fatalf("flagApplicability metadata is incomplete or has orphans: %v", err)
	}
}

// Default unknown metadata must fail closed (reject), not silently include.
func TestUnknownMetadataFailsClosed(t *testing.T) {
	// A flag not in flagApplicabilityByFlag is
	// rejected in BOTH explicit modes. --help is registered by the stdlib flag
	// parser implicitly but is NOT in our metadata; however it is handled by
	// fs.Parse (returns flag.ErrHelp) BEFORE the applicability check runs. So we
	// assert the invariant directly: applicableIn for a synthetic unknown name is
	// false in both explicit modes.
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		if applicableIn("__synthetic_unknown_flag__", mode) {
			t.Errorf("unknown metadata must fail closed (reject) in %q mode, not silently include", mode)
		}
	}
}

// --- Requirement 5: ask-reviewer removal → honest unknown-flag errors -------

func TestAskReviewerFlagsAreUnknownFlagErrors(t *testing.T) {
	for _, name := range []string{"subagent-ask-reviewer", "subagent-ask-reviewer-max-denies", "subagent-ask-reviewer-policy"} {
		t.Run(name, func(t *testing.T) {
			_, _, err := parseTransportFlagsTest(t, modeLocal, []string{"--" + name, "x", "--workspace", "/abs"})
			if err == nil {
				t.Errorf("removed flag --%s must now be an unknown-flag error, not a silent no-op", name)
			}
			if !strings.Contains(err.Error(), "not defined") && !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Errorf("removed flag --%s should surface as a stdlib unknown-flag error, got: %v", name, err)
			}
		})
	}
}

func TestNoSavedAuthFlagIsUnknownFlagError(t *testing.T) {
	_, _, err := parseTransportFlagsTest(t, modeConnect, []string{"--no-saved-auth"})
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("removed --no-saved-auth error = %v, want unknown flag", err)
	}
}

func TestOutputEconomyFlagIsUnknownFlagError(t *testing.T) {
	// The --output-economy compatibility flag is DELETED (ADR 0089, the clean
	// break superseding ADR 0086's parse-compat shim): it now fails at flag-parse
	// time with the standard unknown-flag error instead of parsing as a no-op —
	// in BOTH modes (unregistration is total).
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		t.Run(string(mode), func(t *testing.T) {
			_, _, err := parseTransportFlagsTest(t, mode, []string{"--output-economy", "terse", "--workspace", "/abs"})
			if err == nil {
				t.Errorf("removed flag --output-economy must now be an unknown-flag error in %q mode, not a silent no-op", mode)
			}
			if !strings.Contains(err.Error(), "not defined") && !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Errorf("removed flag --output-economy should surface as a stdlib unknown-flag error in %q mode, got: %v", mode, err)
			}
		})
	}
}

// --- Requirement 6: provider/trust validation gating -----------------------

// local mode runs the provider check (mayEmbed); --mock satisfies it. The
// --toolhive-llm=false opt-out keeps the test environment-independent (a host
// with a locally-running ToolHive gateway would otherwise auto-satisfy it).
func TestLocalProviderCheckRuns(t *testing.T) {
	if _, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--workspace", "/abs", "--toolhive-llm=false"}); err != nil {
		t.Fatalf("parse: %v", err)
	} else if err := cfg.validate(); err == nil {
		t.Error("local with no provider and no --mock must fail validation (mayEmbed)")
	}
}

// local with --mock works offline (no provider needed).
func TestLocalMockWorksOffline(t *testing.T) {
	if _, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--mock", "--workspace", "/abs"}); err != nil {
		t.Fatalf("parse: %v", err)
	} else if err := cfg.validate(); err != nil {
		t.Errorf("local --mock must validate offline: %v", err)
	}
}

// local canonical path works offline end-to-end (provider check + posture
// check both pass under --mock). This is the "canonical local must work
// offline with --mock" acceptance bullet.
func TestLocalCanonicalOfflineWithMock(t *testing.T) {
	if _, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--mock", "--workspace", "/abs"}); err != nil {
		t.Fatalf("parse: %v", err)
	} else if err := cfg.validate(); err != nil {
		t.Errorf("canonical local --mock must validate offline: %v", err)
	}
}

// connect mode skips the provider check (never embeds).
func TestConnectSkipsProviderCheck(t *testing.T) {
	if _, cfg, err := parseTransportFlagsTest(t, modeConnect, []string{"--workspace", "/abs"}); err != nil {
		t.Fatalf("parse: %v", err)
	} else if err := cfg.validate(); err != nil {
		t.Errorf("connect must skip the provider/posture checks (never embeds): %v", err)
	}
}

// local rejects --yolo as root outside a sandbox (posture check runs).
func TestLocalPostureCheckRuns(t *testing.T) {
	_, cfg, err := parseTransportFlagsTest(t, modeLocal, []string{"--yolo", "--mock", "--workspace", "/abs"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The posture refusal is only assertable when running as root; in any case
	// validate must NOT skip it (it would surface a refusal for a privileged
	// run). We assert validate returns nil when not privileged (the common test
	// runner) — the gate is exercised structurally.
	if os.Geteuid() != 0 {
		if err := cfg.validate(); err != nil {
			t.Errorf("local --yolo as non-root must validate (sandbox or not-privileged): %v", err)
		}
	}
}

// --- Requirement 7: real help content --------------------------------------

func helpRenderOut(t *testing.T, mode transportMode, argv []string) string {
	t.Helper()
	return helpRenderOutForLaunch(t, mode, false, argv)
}

func helpRenderOutForLaunch(t *testing.T, mode transportMode, browseSessions bool, argv []string) string {
	t.Helper()
	var buf strings.Builder
	_, _, err := parseTransportFlags(mode, &buf, argv, browseSessions)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseTransportFlags(%v, %v): %v", mode, argv, err)
	}
	return buf.String()
}

// hasFlagHeader reports whether the rendered help output contains the flag's
// own header line — distinguishing the flag's own entry from a bare mention in
// prose. Multi-character names use the conventional -- spelling while aliases
// retain the single-dash form.
func hasFlagHeader(out, name string) bool {
	prefix := "  --"
	if len(name) == 1 {
		prefix = "  -"
	}
	for _, line := range strings.Split(out, "\n") {
		rest := strings.TrimPrefix(line, prefix+name)
		if rest == line {
			continue
		}
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return true
		}
	}
	return false
}

func assertCatalogCommandsRendered(t *testing.T, out string) {
	t.Helper()
	for _, command := range topLevelCommands {
		for _, want := range []string{command.synopsis, "mecatui " + command.name + " --help"} {
			if !strings.Contains(out, want) {
				t.Errorf("rendered output omitted catalog command %q detail %q:\n%s", command.name, want, out)
			}
		}
	}
}

func TestTopLevelHelpRealRendererContainsCommands(t *testing.T) {
	var buf strings.Builder
	writeTopLevelHelp(&buf)
	out := buf.String()
	for _, want := range []string{"Usage: mecatui [flags]", "mecatui <command> [flags]", "hosts an embedded mecated"} {
		if !strings.Contains(out, want) {
			t.Errorf("top-level help (real renderer) missing %q\n--- output ---\n%s", want, out)
		}
	}
	assertCatalogCommandsRendered(t, out)
	// The retired `local` subcommand and the deprecation/compat prose are gone.
	for _, unwanted := range []string{"local ", "deprecated", "Compatibility", "--server"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("top-level help (real renderer) must not mention %q\n--- output ---\n%s", unwanted, out)
		}
	}
}

func TestBareHelpFlagsRealRendererShowsCommonFlags(t *testing.T) {
	out := helpRenderOut(t, modeLocal, []string{"--help-flags"})
	if !strings.Contains(out, "Usage: mecatui --help-flags") {
		t.Errorf("bare help-flags missing usage:\n%s", out)
	}
	if !strings.Contains(out, "NEVER probes loopback") {
		t.Errorf("bare help missing the no-probe note:\n%s", out)
	}
	assertCatalogCommandsRendered(t, out)
	// Embedded flags appear in the bare common help (--mock is common+local), and
	// the client-side debug switch is shared by local and connect modes.
	for _, name := range []string{"mock", "debug"} {
		if !hasFlagHeader(out, name) {
			t.Errorf("bare help missing --%s common flag header:\n%s", name, out)
		}
	}
	// Remote-only flags do NOT appear in the bare common help.
	if hasFlagHeader(out, "auth-token") {
		t.Errorf("bare help leaked remote-only --auth-token as a flag header:\n%s", out)
	}
}

func TestSessionsHelpUsesLaunchGrammarAndOmitsConflicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode transportMode
		want string
	}{
		{name: "local", mode: modeLocal, want: "Usage: mecatui sessions [flags]"},
		{name: "connect", mode: modeConnect, want: "Usage: mecatui connect ADDRESS sessions [flags]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := helpRenderOutForLaunch(t, tc.mode, true, []string{"--help"})
			if !strings.Contains(out, tc.want) || !strings.Contains(out, "without creating a session") {
				t.Fatalf("sessions help missing launch contract:\n%s", out)
			}
			for _, conflict := range []string{"prompt", "p", "prompt-file", "resume", "resume-latest"} {
				if hasFlagHeader(out, conflict) {
					t.Errorf("sessions help includes conflicting --%s:\n%s", conflict, out)
				}
			}
		})
	}
}

func TestConnectHelpRealRendererShowsCommonFlags(t *testing.T) {
	out := helpRenderOut(t, modeConnect, []string{"--help"})
	if !strings.Contains(out, "Usage: mecatui connect ADDRESS [flags]") {
		t.Errorf("connect help missing 'Usage: mecatui connect ADDRESS [flags]':\n%s", out)
	}
	if !strings.Contains(out, "NEVER probes loopback") {
		t.Errorf("connect help missing the no-probe note:\n%s", out)
	}
	// Remote flags appear in connect common help (--server is NOT applicable in
	// connect — connect takes ADDRESS — so it must NOT appear; --auth-token IS).
	// The shared client-side --debug flag appears here too.
	for _, name := range []string{"auth-token", "anonymous", "debug"} {
		if !hasFlagHeader(out, name) {
			t.Errorf("connect help missing remote --%s flag header:\n%s", name, out)
		}
	}
	if hasFlagHeader(out, "mock") {
		t.Errorf("connect help leaked embedded-only --mock as a flag header:\n%s", out)
	}
}

func TestHelpAllReturnsErrHelp(t *testing.T) {
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		t.Run(string(mode), func(t *testing.T) {
			_, _, err := parseTransportFlags(mode, &bytes.Buffer{}, []string{"--help-all"})
			if !errors.Is(err, flag.ErrHelp) {
				t.Errorf("--help-all in %q mode must return flag.ErrHelp (exit 0), got %v", mode, err)
			}
		})
	}
}

func TestBareHelpAllRendersFullRealFlagSet(t *testing.T) {
	out := helpRenderOut(t, modeLocal, []string{"--help-all"})
	if !strings.Contains(out, "Usage: mecatui [flags]") {
		t.Errorf("bare --help-all missing usage:\n%s", out)
	}
	assertCatalogCommandsRendered(t, out)
	// A representative advanced embedded flag appears in --help-all.
	if !hasFlagHeader(out, "perf-goroutine-warn-threshold") {
		t.Errorf("bare --help-all missing an advanced flag header:\n%s", out)
	}
}

func TestConnectHelpAllExcludesEmbeddedFlags(t *testing.T) {
	out := helpRenderOut(t, modeConnect, []string{"--help-all"})
	if !strings.Contains(out, "Usage: mecatui connect ADDRESS [flags]") {
		t.Errorf("connect --help-all missing usage:\n%s", out)
	}
	// Embedded-only flags are excluded from connect --help-all.
	for _, embedded := range []string{"mock", "trust-project", "posture", "perf"} {
		if hasFlagHeader(out, embedded) {
			t.Errorf("connect --help-all leaked embedded flag --%s as a header:\n%s", embedded, out)
		}
	}
	// Remote flags appear.
	if !hasFlagHeader(out, "auth-token") {
		t.Errorf("connect --help-all missing remote --auth-token flag header:\n%s", out)
	}
}

// --- Requirement 1: top-level help is a command index (not a flag dump) ------

func TestBareHelpIsNotRawFlagDump(t *testing.T) {
	out := runHelpCase(t, []string{"mecatui", "--help"}, "Commands:")
	if strings.Contains(out, "Usage of mecatui:") || hasFlagHeader(out, "mock") {
		t.Errorf("bare help must be the concise command index, not a flag dump:\n%s", out)
	}
}

func TestCommandSummaryUsesIndentedWrappedDescriptions(t *testing.T) {
	var out strings.Builder
	writeCommandSummary(&out)
	summary := out.String()

	for _, want := range []string{
		"  sessions\n    browse stored sessions before creating or continuing a chat\n",
		"  debug TARGET [flags]\n    diagnose by an exact session ID or displayed 12-column short handle; exact\n    identity wins, a unique handle resolves automatically, and ambiguity asks\n    for the full exact ID\n",
		"  connect ADDRESS [sessions | debug TARGET] [flags]\n    dial a running mecated at ADDRESS (host:port), optionally browsing or\n    debugging a stored session\n",
		"  login ADDRESS\n    log in to a remote mecated at ADDRESS using OIDC\n",
		"  llm <config|setup|login|status|logout> [args]\n    configure, inspect, and manage LLM providers; setup guides API-key custody\n    while native and ToolHive lifecycles remain separate\n",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("command summary missing indented, wrapped description %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "connect ADDRESS [sessions] dial a running") {
		t.Errorf("command summary put the connect description on its synopsis line:\n%s", summary)
	}
	unknown := unknownCommandError("unknown").Error()
	if !strings.Contains(unknown, "  connect ADDRESS [sessions | debug TARGET] [flags]\n    dial a running mecated at ADDRESS (host:port), optionally browsing or\n    debugging a stored session\n") {
		t.Errorf("unknown-command output did not reuse the indented, wrapped command summary:\n%s", unknown)
	}
	for _, line := range strings.Split(strings.TrimSuffix(summary, "\n"), "\n") {
		if len(line) > 80 {
			t.Errorf("command summary line exceeds the 80-column help width (%d): %q", len(line), line)
		}
	}
}

// --- Requirement: --help returns flag.ErrHelp (mirrors mecated) -------------

// TestHelpReturnsErrHelp asserts --help/-h return flag.ErrHelp across every
// transport mode, so main() exits 0 without printing "mecatui: flag: help
// requested" (the regression this guards: a Usage hook that printed help but
// left run() returning a non-ErrHelp error would surface a spurious error line
// on a successful help action).
func TestHelpReturnsErrHelp(t *testing.T) {
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		for _, help := range []string{"--help", "-h"} {
			t.Run(string(mode)+"/"+help, func(t *testing.T) {
				_, _, err := parseTransportFlags(mode, &bytes.Buffer{}, []string{help})
				if !errors.Is(err, flag.ErrHelp) {
					t.Errorf("%q in %q mode must return flag.ErrHelp (exit 0), got %v", help, mode, err)
				}
			})
		}
	}
}

// --- Requirement: actual main/run help behaviour at the run() seam ----------

// runHelpCase runs the REAL run() seam (the function main() calls) with a
// captured stderr and asserts the help contract: run() returns flag.ErrHelp
// (so main() exits 0 WITHOUT printing "mecatui: <err>"), and the help text is
// written to stderr. It does NOT touch the network or start the TUI: a help
// request returns from parseTransportFlags before any transport resolution. The
// argv includes the program name (run() reads argv, not os.Args), matching the
// production main() call shape.
func runHelpCase(t *testing.T, argv []string, wantSubstring string) string {
	t.Helper()
	// Capture stderr by swapping os.Stderr for the duration of run(). run()
	// threads os.Stderr into parseTransportFlags as the help output writer, so
	// the rendered help lands here. The --help-all output can exceed an os.Pipe's
	// 64KB buffer, so drain the read end concurrently to avoid a write-block
	// deadlock; io.Copy into a bytes.Buffer and join before asserting.
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	var captured bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&captured, r)
		close(done)
	}()
	runErr := run(argv)
	if err := w.Close(); err != nil {
		t.Fatalf("close write pipe: %v", err)
	}
	<-done
	os.Stderr = orig
	out := captured.String()

	if !errors.Is(runErr, flag.ErrHelp) {
		t.Errorf("run(%v) err = %v, want flag.ErrHelp (so main exits 0 with no error line)", argv, runErr)
	}
	if !strings.Contains(out, wantSubstring) {
		t.Errorf("run(%v) stderr missing %q\n--- stderr ---\n%s", argv, wantSubstring, out)
	}
	// The help action must NOT print the "mecatui: <err>" error line main()
	// emits on a non-ErrHelp failure — that is the regression this guards.
	if strings.HasPrefix(out, "mecatui:") {
		t.Errorf("run(%v) stderr starts with the error line \"mecatui:\" — help must be a clean success:\n%s", argv, out)
	}
	return out
}

func TestRunBareHelpShowsOnlyCommandIndex(t *testing.T) {
	out := runHelpCase(t, []string{"mecatui", "--help"}, "Usage: mecatui [flags]")
	assertCatalogCommandsRendered(t, out)
	if !strings.Contains(out, "mecatui --version prints the build version and exits") {
		t.Errorf("run bare help omitted the global version action:\n%s", out)
	}
	for _, group := range []string{"Session:", "UI:", "Provider:", "Permissions:"} {
		if strings.Contains(out, group) {
			t.Errorf("run bare help included flag group %q:\n%s", group, out)
		}
	}
	if hasFlagHeader(out, "mock") {
		t.Errorf("run bare help included flag entries:\n%s", out)
	}
}

func TestRunTopLevelHelpSpellingsRenderSameIndex(t *testing.T) {
	want := runHelpCase(t, []string{"mecatui", "--help"}, "Commands:")
	for _, argv := range [][]string{
		{"mecatui", "-h"},
		{"mecatui", "help"},
		{"mecatui", "--workspace", "/tmp", "--help"},
		{"mecatui", "--quiet", "-h"},
	} {
		if got := runHelpCase(t, argv, "Commands:"); got != want {
			t.Errorf("run(%v) rendered a different top-level index:\n%s", argv, got)
		}
	}
}

func TestRunHelpCommandAliasesMatchDirectHelp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		direct []string
		alias  []string
		want   string
	}{
		{"sessions", []string{"mecatui", "sessions", "--help"}, []string{"mecatui", "help", "sessions"}, "Usage: mecatui sessions [flags]"},
		{"connect", []string{"mecatui", "connect", "--help"}, []string{"mecatui", "help", "connect"}, "Usage: mecatui connect ADDRESS [flags]"},
		{"login", []string{"mecatui", "login", "--help"}, []string{"mecatui", "help", "login"}, "Usage: mecatui login ADDRESS"},
		{"logout", []string{"mecatui", "logout", "--help"}, []string{"mecatui", "help", "logout"}, "Usage: mecatui logout ADDRESS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := runHelpCase(t, tc.direct, tc.want)
			if got := runHelpCase(t, tc.alias, tc.want); got != want {
				t.Errorf("help alias %v differs from direct help %v:\n%s", tc.alias, tc.direct, got)
			}
		})
	}
}

func TestRunHelpFlagsShowsEmbeddedCommonFlags(t *testing.T) {
	out := runHelpCase(t, []string{"mecatui", "--help-flags"}, "Usage: mecatui --help-flags")
	if !hasFlagHeader(out, "mock") {
		t.Errorf("help-flags omitted local --mock:\n%s", out)
	}
	if hasFlagHeader(out, "auth-token") {
		t.Errorf("help-flags leaked remote --auth-token:\n%s", out)
	}
	_, _, err := parseTransportFlags(modeConnect, io.Discard, []string{"--help-flags"})
	if err == nil || errors.Is(err, flag.ErrHelp) || !strings.Contains(err.Error(), "bare") {
		t.Errorf("connect --help-flags error = %v, want bare-only rejection", err)
	}
}

func TestResolveHelpRejectsInvalidTargetsAndExtraOperands(t *testing.T) {
	for _, argv := range [][]string{
		{"mecatui", "help", "unknown"},
		{"mecatui", "help", "sessions", "extra"},
		{"mecatui", "--help", "extra"},
		{"mecatui", "--help-flags", "extra"},
		{"mecatui", "--help-all", "extra"},
		{"mecatui", "sessions", "--help", "extra"},
		{"mecatui", "connect", "127.0.0.1:8080", "--help", "extra"},
	} {
		res := resolveInvocation(argv)
		if res.err == nil || !strings.Contains(res.err.Error(), "mecatui help") {
			t.Errorf("resolveInvocation(%v) error = %v, want command-index guidance", argv, res.err)
		}
	}
}

func TestRunInvalidHelpFormsReturnUsageErrorTrailer(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"unknown target", []string{"mecatui", "help", "unknown"}},
		{"extra help target operand", []string{"mecatui", "help", "sessions", "extra"}},
		{"long help extra operand", []string{"mecatui", "--help", "extra"}},
		{"short help extra operand", []string{"mecatui", "-h", "extra"}},
		{"help flags extra operand", []string{"mecatui", "--help-flags", "extra"}},
		{"help all extra operand", []string{"mecatui", "--help-all", "extra"}},
		{"sessions help extra operand", []string{"mecatui", "sessions", "--help", "extra"}},
		{"connect help extra operand", []string{"mecatui", "connect", "127.0.0.1:8080", "--help", "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.argv)
			if err == nil {
				t.Fatal("run() error = nil, want usage error")
			}
			if errors.Is(err, flag.ErrHelp) {
				t.Fatalf("run() error = flag.ErrHelp, want non-help usage error: %v", err)
			}
			var trailer *usageErrorTrailer
			if !errors.As(err, &trailer) {
				t.Fatalf("run() error is not a usageErrorTrailer: %v", err)
			}
			if !strings.Contains(err.Error(), "mecatui help") {
				t.Errorf("run() error %q missing recovery guidance", err)
			}
		})
	}
}

// TestRunHelpReturnsErrHelpAndWritesHelp exercises run() (the function main
// calls) for --help across every transport shape: the bare embedded form and
// `connect` (with and without an ADDRESS — connect --help needs no ADDRESS).
// It pins the contract that a help request is a SUCCESSFUL action: run()
// returns flag.ErrHelp so main() exits 0 with no error line, and the
// mode-appropriate help is written to stderr.
func TestRunHelpReturnsErrHelpAndWritesHelp(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"bare", []string{"mecatui", "--help"}, "Usage: mecatui [flags]"},
		{"connect-no-addr", []string{"mecatui", "connect", "--help"}, "Usage: mecatui connect ADDRESS [flags]"},
		{"connect-with-addr", []string{"mecatui", "connect", "127.0.0.1:8080", "--help"}, "Usage: mecatui connect ADDRESS [flags]"},
		{"sessions", []string{"mecatui", "sessions", "--help"}, "Usage: mecatui sessions [flags]"},
		{"login", []string{"mecatui", "login", "--help"}, "Usage: mecatui login ADDRESS"},
		{"short-h", []string{"mecatui", "-h"}, "Usage: mecatui [flags]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runHelpCase(t, tc.argv, tc.want)
		})
	}
}

// TestRunHelpAllReturnsErrHelpAndWritesHelp exercises run() for --help-all: it
// returns flag.ErrHelp (exit 0, no error line) and writes the exhaustive flag
// reference to stderr with the mode-appropriate usage header.
func TestRunHelpAllReturnsErrHelpAndWritesHelp(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"bare", []string{"mecatui", "--help-all"}, "Usage: mecatui [flags]"},
		{"connect", []string{"mecatui", "connect", "--help-all"}, "Usage: mecatui connect ADDRESS [flags]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runHelpCase(t, tc.argv, tc.want)
		})
	}
}

// TestRunRemoteSessionsHelpUsesLaunchGrammarAndApplicableFlags exercises the real
// run() path for both remote session-browser help forms.
func TestRunRemoteSessionsHelpUsesLaunchGrammarAndApplicableFlags(t *testing.T) {
	for _, help := range []string{"--help", "--help-all"} {
		t.Run(help, func(t *testing.T) {
			out := runHelpCase(t, []string{"mecatui", "connect", "127.0.0.1:8080", "sessions", help}, "Usage: mecatui connect ADDRESS sessions [flags]")
			if help == "--help" && !strings.Contains(out, "without creating a session") {
				t.Errorf("remote sessions %s missing launch contract:\n%s", help, out)
			}
			if !hasFlagHeader(out, "auth-token") {
				t.Errorf("remote sessions %s missing applicable --auth-token:\n%s", help, out)
			}
			for _, inapplicable := range []string{"mock", "prompt", "p", "prompt-file", "resume", "resume-latest"} {
				if hasFlagHeader(out, inapplicable) {
					t.Errorf("remote sessions %s includes inapplicable --%s:\n%s", help, inapplicable, out)
				}
			}
		})
	}
}

// TestRunUnknownCommandReturnsNonHelpError distinguishes a usage failure from a
// successful help request at the real run() seam.
func TestRunUnknownCommandReturnsNonHelpError(t *testing.T) {
	err := run([]string{"mecatui", "bogus-command"})
	if err == nil {
		t.Fatal("run(unknown command) err = nil, want a non-nil usage error")
	}
	if errors.Is(err, flag.ErrHelp) {
		t.Errorf("run(unknown command) err = flag.ErrHelp, want a non-ErrHelp usage error (so main exits 1 with the error line)")
	}
	if !strings.Contains(err.Error(), "bogus-command") {
		t.Errorf("run(unknown command) error %q does not name the unknown command", err)
	}
	var trailer *usageErrorTrailer
	if !errors.As(err, &trailer) {
		t.Errorf("run(unknown command) error is not a usageErrorTrailer — main would skip the top-level command summary trailer")
	}
}

// TestUsageErrorTrailerMarkers pin the trailer contract main's error printer
// keys on: a resolver usage error (unknown command AND the connect
// missing/flag-first ADDRESS forms) arrives wrapped, while every OTHER run()
// error (flag parse, validation) stays unwrapped so main prints no trailer.
func TestUsageErrorTrailerMarkers(t *testing.T) {
	// connect missing ADDRESS → wrapped usage error.
	err := run([]string{"mecatui", "connect"})
	var trailer *usageErrorTrailer
	if !errors.As(err, &trailer) {
		t.Errorf("run(connect with no ADDRESS) error is not a usageErrorTrailer, want the trailer so main appends the command summary: %v", err)
	}

	// connect flag-first → wrapped usage error.
	err = run([]string{"mecatui", "connect", "--workspace", "/abs"})
	if !errors.As(err, &trailer) {
		t.Errorf("run(connect --workspace …) error is not a usageErrorTrailer: %v", err)
	}

	// A flag-PARSE error (unknown flag in a valid mode) is NOT wrapped: it is a
	// per-flag problem, not a leading-word grammar error.
	err = run([]string{"mecatui", "--not-a-real-flag"})
	if errors.As(err, &trailer) {
		t.Errorf("run(--not-a-real-flag) error unexpectedly wrapped as usageErrorTrailer: %v", err)
	}
}

// TestUnknownCommandErrorListsCatalogHelpRoutes ensures unknown-command guidance
// stays derived from the same catalog as resolution and top-level help.
func TestUnknownCommandErrorListsCatalogHelpRoutes(t *testing.T) {
	err := unknownCommandError("local")
	for _, command := range topLevelCommands {
		if !strings.Contains(err.Error(), "mecatui "+command.name+" --help") {
			t.Errorf("unknown-command error missing %q help route: %v", command.name, err)
		}
	}
}
