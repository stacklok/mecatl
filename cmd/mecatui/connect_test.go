package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
)

type connectRestartModel struct{ intent ui.ConnectRestartIntent }

type staticConnectController struct{ target string }

func (c staticConnectController) ListConnectTargets(context.Context) ([]ui.ConnectTarget, error) {
	return []ui.ConnectTarget{{Target: c.target}}, nil
}

func (connectRestartModel) Init() tea.Cmd                         { return nil }
func (m connectRestartModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (connectRestartModel) View() tea.View                        { return tea.View{} }

func (m connectRestartModel) ConnectRestartIntent() (ui.ConnectRestartIntent, bool) {
	return m.intent, true
}

func TestConnectRestartSuppressesFinalSessionHandoff(t *testing.T) {
	model := connectRestartModel{intent: ui.ConnectRestartIntent{Target: "remote.example:443", Action: ui.ConnectSaved}}
	if shouldWriteFinalSessionHandoff(model, nil, false) {
		t.Fatal("internal connect restart must not emit final-session handoff")
	}
	if shouldWriteFinalSessionHandoff(model, errors.New("exit"), false) {
		t.Fatal("internal connect restart must suppress handoff even when the prior UI had an error")
	}
}

func TestCanonicalSavedTargetFlowsThroughRecoveryRestart(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })

	root := filepath.Join(xdg.ConfigHome, "mecatl")
	registry, err := clientauth.OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	conn := clientauth.Connection{
		Identity: clientauth.Identity{
			Target:      "MiXeD.Example:0443",
			Issuer:      "https://issuer.example",
			ClientID:    "client",
			Audience:    "audience",
			RedirectURI: "http://127.0.0.1:19876/oauth/callback",
			Scopes:      []string{"openid"},
		},
		IssuerCAFile: filepath.Join(xdg.ConfigHome, "issuer-ca-a.pem"),
	}
	if _, err := registry.Upsert(conn); err != nil {
		t.Fatal(err)
	}

	serverCA := filepath.Join(xdg.ConfigHome, "server-ca-b.pem")
	target, _, _, resolveErr := resolveTransport(t.Context(), config{
		transportMode:  modeConnect,
		connectAddress: "MIXED.EXAMPLE:00443",
		useTLS:         true,
		tlsCA:          serverCA,
	})
	if target != "mixed.example:443" {
		t.Fatalf("resolved target = %q, want canonical target", target)
	}
	reason, ok := client.AuthFailure(resolveErr, false)
	if !ok || reason != client.AuthStorageUnavailable {
		t.Fatalf("resolve error = %v, reason=%q ok=%v", resolveErr, reason, ok)
	}
	options := authRecoveryOptions(reason, target, "Authentication needs attention.", "session-1", restartTransport{Target: target, TLSCAFile: serverCA})
	m := ui.New(ui.Deps{
		Connect:                savedConnectController{},
		ConnectOpen:            true,
		ConnectReason:          options.connectReason,
		ConnectTarget:          options.connectTarget,
		ConnectResumeSessionID: options.connectResumeSessionID,
		Theme:                  theme.New("aztec", theme.AztecPalette()),
		Ctx:                    t.Context(),
		NoAltScreen:            true,
	})
	model, _ := m.Update(m.Init()())
	m = model.(ui.Model)
	model, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = model.(ui.Model)
	model, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	intent, ok := model.(ui.Model).ConnectRestartIntent()
	if !ok || intent.Target != target || intent.Action != ui.RetryAfterCleanup || intent.ResumeSessionID != "session-1" {
		t.Fatalf("canonical recovery intent = %#v, ok=%v", intent, ok)
	}

	var gotArgv []string
	var gotIssuerCA string
	err = restartFromConnectIntentWith([]string{"mecatui"}, intent, options.connectTransport, connectRestartOps{
		run: func(argv []string, _ runOptions) error { gotArgv = append([]string(nil), argv...); return nil },
		connection: func(gotTarget string) (clientauth.Connection, error) {
			return savedConnection(gotTarget)
		},
		login: func(_ context.Context, got clientauth.Connection, _ bool) error {
			gotIssuerCA = got.IssuerCAFile
			return nil
		},
		loginContext: func(time.Duration) (context.Context, context.CancelFunc) {
			return context.WithCancel(t.Context())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{"mecatui", "connect", "mixed.example:443", "--tls", "--tls-ca", serverCA}
	if !slices.Equal(gotArgv, wantArgv) || gotIssuerCA != "" {
		t.Fatalf("restart argv=%q issuerCA=%q, want browser-free retry", gotArgv, gotIssuerCA)
	}
}

// TestDefaultConnectRestartOpsUsesExistingOnlyReauthLogin pins that
// restartFromConnectIntent's production wiring uses the existing-only
// reauthentication login (runExistingSavedRemoteLogin), never the creating one
// (runSavedRemoteLogin) -- a missing local keyring/store must not look like
// corruption and get silently replaced during a Reauthenticate restart.
// runExistingSavedRemoteLogin's own behavior (existing-only, no state
// creation) is pinned separately by
// TestExistingSavedRemoteLoginMissingStoreDoesNotLaunchBrowserOrCreateState in
// login_test.go; this test closes the loop by proving that function is the
// one actually wired in.
func TestDefaultConnectRestartOpsUsesExistingOnlyReauthLogin(t *testing.T) {
	got := reflect.ValueOf(defaultConnectRestartOps().login).Pointer()
	want := reflect.ValueOf(runExistingSavedRemoteLogin).Pointer()
	creating := reflect.ValueOf(runSavedRemoteLogin).Pointer()
	if got == creating {
		t.Fatal("restartFromConnectIntent still wires the creating login (runSavedRemoteLogin)")
	}
	if got != want {
		t.Fatal("restartFromConnectIntent does not wire runExistingSavedRemoteLogin")
	}
}

func TestRestartTargetSwitchDropsPriorTransportAndResume(t *testing.T) {
	var gotArgv []string
	var gotOptions runOptions
	err := restartFromConnectIntentWith([]string{"mecatui"}, ui.ConnectRestartIntent{Target: "other.example:443", Action: ui.ConnectSaved, ResumeSessionID: "must-drop"}, restartTransport{Target: "old.example:443", TLSCAFile: "/old-server-ca.pem"}, connectRestartOps{
		run: func(argv []string, options runOptions) error {
			gotArgv = append([]string(nil), argv...)
			gotOptions = options
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{"mecatui", "connect", "other.example:443", "--tls"}
	if !slices.Equal(gotArgv, wantArgv) || gotOptions.connectResumeSessionID != "" {
		t.Fatalf("target-switch argv=%q resume=%q", gotArgv, gotOptions.connectResumeSessionID)
	}
}

func TestCrossTargetRecoveryActionsDowngradeToFreshSavedConnect(t *testing.T) {
	for _, action := range []ui.ConnectAction{ui.RetryAfterCleanup, ui.Reauthenticate} {
		t.Run(string(rune('0'+action)), func(t *testing.T) {
			var gotArgv []string
			var gotOptions runOptions
			lookups, logins := 0, 0
			err := restartFromConnectIntentWith([]string{"mecatui"}, ui.ConnectRestartIntent{Target: "other.example:443", Action: action, ResumeSessionID: "must-drop"}, restartTransport{Target: "original.example:443", TLSCAFile: "/original-ca.pem"}, connectRestartOps{
				run: func(argv []string, options runOptions) error {
					gotArgv, gotOptions = append([]string(nil), argv...), options
					return nil
				},
				connection: func(string) (clientauth.Connection, error) { lookups++; return clientauth.Connection{}, nil },
				login:      func(context.Context, clientauth.Connection, bool) error { logins++; return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"mecatui", "connect", "other.example:443", "--tls"}
			if !slices.Equal(gotArgv, want) || gotOptions.connectResumeSessionID != "" || lookups != 0 || logins != 0 {
				t.Fatalf("action %d: argv=%q options=%#v lookups=%d logins=%d", action, gotArgv, gotOptions, lookups, logins)
			}
		})
	}
}

func TestNeverEnrolledTargetFailsBeforeDial(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })

	target, dial, cleanup, err := resolveTransport(t.Context(), config{
		transportMode:  modeConnect,
		connectAddress: "new.example:443",
		useTLS:         true,
	})
	defer cleanup()
	if target != "new.example:443" {
		t.Fatalf("target = %q, want requested target", target)
	}
	if dial.Server != "" || dial.TokenSource != nil {
		t.Fatalf("dial config = %#v, want no dial target or token source", dial)
	}
	reason, ok := client.AuthFailure(err, false)
	if !ok || reason != client.AuthNeverEnrolled {
		t.Fatalf("resolve error = %v, reason=%q ok=%v", err, reason, ok)
	}
}

func TestRestartConnectActionsAndBrowserBoundary(t *testing.T) {
	for _, action := range []ui.ConnectAction{ui.ConnectSaved, ui.RetryAfterCleanup, ui.Reauthenticate, ui.AddTarget} {
		t.Run(string(rune('0'+action)), func(t *testing.T) {
			var runs, logins, lookups int
			var lastArgv []string
			var loginIssuerCA string
			var lastOptions runOptions
			ops := connectRestartOps{
				run: func(argv []string, options runOptions) error {
					runs++
					lastArgv = append([]string(nil), argv...)
					lastOptions = options
					return nil
				},
				connection: func(target string) (clientauth.Connection, error) {
					lookups++
					return clientauth.Connection{Identity: clientauth.Identity{Target: target}, IssuerCAFile: "/issuer-ca-a.pem"}, nil
				},
				login: func(_ context.Context, conn clientauth.Connection, _ bool) error {
					logins++
					loginIssuerCA = conn.IssuerCAFile
					return nil
				},
				loginContext: func(time.Duration) (context.Context, context.CancelFunc) {
					return context.WithCancel(context.Background())
				},
			}
			err := restartFromConnectIntentWith([]string{"mecatui"}, ui.ConnectRestartIntent{Target: "canonical.example:443", Action: action, ResumeSessionID: "session-1"}, restartTransport{Target: "canonical.example:443", TLSCAFile: "/server-ca-b.pem"}, ops)
			if err != nil {
				t.Fatal(err)
			}
			wantLogin := 0
			if action == ui.Reauthenticate {
				wantLogin = 1
			}
			if logins != wantLogin || lookups != wantLogin {
				t.Fatalf("action %d: lookups=%d logins=%d, want %d", action, lookups, logins, wantLogin)
			}
			if action == ui.Reauthenticate && loginIssuerCA != "/issuer-ca-a.pem" {
				t.Fatalf("reauth issuer CA path = %q, want CA A", loginIssuerCA)
			}
			if runs != 1 {
				t.Fatalf("action %d: runs=%d, want 1", action, runs)
			}
			if action != ui.AddTarget {
				wantArgv := []string{"mecatui", "connect", "canonical.example:443", "--tls", "--tls-ca", "/server-ca-b.pem"}
				if !slices.Equal(lastArgv, wantArgv) {
					t.Fatalf("action %d: argv=%q, want %q", action, lastArgv, wantArgv)
				}
			}
			wantResume := action == ui.Reauthenticate || action == ui.RetryAfterCleanup
			if (lastOptions.connectResumeSessionID != "") != wantResume {
				t.Fatalf("action %d: resume=%q, want present=%v", action, lastOptions.connectResumeSessionID, wantResume)
			}
		})
	}
}

// TestReauthenticateRestartAlwaysPassesNoBrowser pins that
// restartFromConnectIntentWith's Reauthenticate branch passes true to the
// login call unconditionally (ADR 0271: the recovery overlay never opens a
// browser). ConnectRestartIntent carries no NoBrowser field, so there is no
// producer-supplied value that could reopen the browser path here.
func TestReauthenticateRestartAlwaysPassesNoBrowser(t *testing.T) {
	var gotNoBrowser bool
	var logins int
	ops := connectRestartOps{
		run: func([]string, runOptions) error { return nil },
		connection: func(target string) (clientauth.Connection, error) {
			return clientauth.Connection{Identity: clientauth.Identity{Target: target}}, nil
		},
		login: func(_ context.Context, _ clientauth.Connection, headless bool) error {
			logins++
			gotNoBrowser = headless
			return nil
		},
		loginContext: func(time.Duration) (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
	}
	err := restartFromConnectIntentWith([]string{"mecatui"}, ui.ConnectRestartIntent{Target: "canonical.example:443", Action: ui.Reauthenticate}, restartTransport{Target: "canonical.example:443"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if logins != 1 {
		t.Fatalf("logins = %d, want 1", logins)
	}
	if !gotNoBrowser {
		t.Fatal("login noBrowser = false, want true (ADR 0271)")
	}
}

func TestRecoveryRestartFailurePreservesSecondAttemptIntent(t *testing.T) {
	const target = "canonical.example:443"
	for _, action := range []ui.ConnectAction{ui.RetryAfterCleanup, ui.Reauthenticate} {
		t.Run(string(rune('0'+action)), func(t *testing.T) {
			var reopened runOptions
			runs := 0
			err := restartFromConnectIntentWith([]string{"mecatui"}, ui.ConnectRestartIntent{Target: target, Action: action, ResumeSessionID: "session-1"}, restartTransport{Target: target, TLSCAFile: "/server-ca.pem"}, connectRestartOps{
				run: func(argv []string, options runOptions) error {
					runs++
					if len(argv) > 1 {
						return errors.New("injected connect failure")
					}
					reopened = options
					return nil
				},
				connection: func(string) (clientauth.Connection, error) {
					return clientauth.Connection{Identity: clientauth.Identity{Target: target}}, nil
				},
				login: func(context.Context, clientauth.Connection, bool) error { return nil },
				loginContext: func(time.Duration) (context.Context, context.CancelFunc) {
					return context.WithCancel(t.Context())
				},
			})
			if err != nil || runs != 2 {
				t.Fatalf("restart = %v, runs=%d", err, runs)
			}
			if reopened.connectReason != connectRecoveryReason(action) || reopened.connectResumeSessionID != "session-1" || reopened.connectTransport.Target != target || reopened.connectTransport.TLSCAFile != "/server-ca.pem" {
				t.Fatalf("reopened recovery options = %#v", reopened)
			}

			m := ui.New(ui.Deps{Connect: staticConnectController{target: target}, ConnectOpen: true, ConnectReason: reopened.connectReason, ConnectTarget: reopened.connectTarget, ConnectResumeSessionID: reopened.connectResumeSessionID, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), NoAltScreen: true})
			model, _ := m.Update(m.Init()())
			m = model.(ui.Model)
			model, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = model.(ui.Model)
			model, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			intent, ok := model.(ui.Model).ConnectRestartIntent()
			if !ok || intent.Action != action || intent.Target != target || intent.ResumeSessionID != "session-1" {
				t.Fatalf("second-attempt intent = %#v, ok=%v", intent, ok)
			}
		})
	}
}

func TestReauthenticationFailurePreservesRecoveryState(t *testing.T) {
	const target = "canonical.example:443"
	var reopened runOptions
	err := restartFromConnectIntentWith([]string{"mecatui"}, ui.ConnectRestartIntent{Target: target, Action: ui.Reauthenticate, ResumeSessionID: "session-1"}, restartTransport{Target: target, TLSCAFile: "/server-ca.pem"}, connectRestartOps{
		run: func(_ []string, options runOptions) error { reopened = options; return nil },
		connection: func(string) (clientauth.Connection, error) {
			return clientauth.Connection{}, errors.New("injected saved connection failure")
		},
		loginContext: func(time.Duration) (context.Context, context.CancelFunc) {
			return context.WithCancel(t.Context())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.connectReason != client.AuthSessionExpired || reopened.connectTarget != target || reopened.connectResumeSessionID != "session-1" || reopened.connectTransport.Target != target || reopened.connectTransport.TLSCAFile != "/server-ca.pem" {
		t.Fatalf("reopened reauthentication options = %#v", reopened)
	}
}

func TestRestartConnectRejectsUnknownAction(t *testing.T) {
	called := false
	err := restartFromConnectIntentWith([]string{"mecatui"}, ui.ConnectRestartIntent{}, restartTransport{}, connectRestartOps{run: func([]string, runOptions) error { called = true; return nil }})
	if err == nil || !strings.Contains(err.Error(), "invalid connect action") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestSavedLoginContextRespondsToTerminationSignals(t *testing.T) {
	for _, delivered := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(delivered.String(), func(t *testing.T) {
			signalCh := make(chan os.Signal, 1)
			var subscribed []os.Signal
			ctx, cancel := newSavedLoginContextWithNotifier(time.Minute, func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
				subscribed = append([]os.Signal(nil), signals...)
				child, stop := context.WithCancel(parent)
				go func() {
					select {
					case sig := <-signalCh:
						if slices.Contains(signals, sig) {
							stop()
						}
					case <-child.Done():
					}
				}()
				return child, stop
			})
			defer cancel()
			if !slices.Contains(subscribed, delivered) {
				t.Fatalf("notifier signals = %v, missing %v", subscribed, delivered)
			}
			signalCh <- delivered
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatalf("context did not respond to %v", delivered)
			}
		})
	}
}

func TestSavedLoginContextIsBoundedAndCancelable(t *testing.T) {
	ctx, cancel := newSavedLoginContext(2 * time.Second)
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
		cancel()
		t.Fatalf("deadline = %v, ok=%v", deadline, ok)
	}
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop login context")
	}
}
