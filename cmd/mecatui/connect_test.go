package main

import (
	"context"
	"errors"
	"net"
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
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

type anonymousConnectServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
}

func (anonymousConnectServer) GetCompatibilityInfo(context.Context, *mecatlv1.GetCompatibilityInfoRequest) (*mecatlv1.GetCompatibilityInfoResponse, error) {
	return &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: 1, Capabilities: &mecatlv1.ServerCapabilities{}}, nil
}

func (anonymousConnectServer) CreateSession(context.Context, *mecatlv1.CreateSessionRequest) (*mecatlv1.CreateSessionResponse, error) {
	return &mecatlv1.CreateSessionResponse{SessionId: "anonymous-local"}, nil
}

func TestUnenrolledLocalTargetConnectsAnonymouslyWithoutCreatingAuthState(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, anonymousConnectServer{})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	target, dial, cleanup, err := resolveTransport(t.Context(), config{
		transportMode:  modeConnect,
		connectAddress: listener.Addr().String(),
	})
	defer cleanup()
	if err != nil || target != listener.Addr().String() || dial.AuthToken != "" || dial.TokenSource != nil {
		t.Fatalf("resolve = target %q dial %#v err %v, want credential-free local dial", target, dial, err)
	}
	if _, statErr := os.Stat(filepath.Join(xdg.ConfigHome, "mecatl")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("auth registry root was touched or created: %v", statErr)
	}

	cl, err := client.Dial(dial)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	id, _, _, err := cl.CreateSession(t.Context(), mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, client.ModelSelection{})
	if err != nil || id != "anonymous-local" {
		t.Fatalf("anonymous CreateSession = %q, %v", id, err)
	}
}

func TestUnenrolledRemoteTargetReachesCredentialFreeServer(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })

	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, anonymousConnectServer{})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	if client.IsLocalTarget(listener.Addr().String()) {
		t.Fatalf("test target %q unexpectedly classified local", listener.Addr())
	}
	invocation := resolveInvocation([]string{"mecatui", "connect", listener.Addr().String(), "--tls=false"})
	cfg, err := parseRunConfig(invocation)
	if err != nil {
		t.Fatal(err)
	}
	target, dial, cleanup, err := resolveTransport(t.Context(), cfg)
	defer cleanup()
	if err != nil || target != listener.Addr().String() || !dial.RemotePlaintextAllowed || dial.AuthToken != "" || dial.TokenSource != nil {
		t.Fatalf("resolve = target %q dial %#v err %v, want credential-free remote dial", target, dial, err)
	}
	cl, err := client.Dial(dial)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	id, _, _, err := cl.CreateSession(t.Context(), mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, client.ModelSelection{})
	if err != nil || id != "anonymous-local" {
		t.Fatalf("credential-free CreateSession = %q, %v", id, err)
	}
}

func TestUnenrolledRemoteUnauthenticatedOffersServerAuthoritativeRecovery(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })

	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
		return nil, status.Error(codes.Unauthenticated, "missing bearer")
	}))
	mecatlv1.RegisterHarnessServiceServer(grpcServer, anonymousConnectServer{})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	_, dial, cleanup, err := resolveTransport(t.Context(), config{
		transportMode:  modeConnect,
		connectAddress: listener.Addr().String(),
		tlsExplicit:    true,
	})
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	cl, err := client.Dial(dial)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	_, _, _, err = cl.CreateSession(t.Context(), mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, client.ModelSelection{})
	reason, ok := client.AuthFailure(err, false)
	for _, want := range []string{"server requires caller authentication", "--auth-token", "if this server supports OIDC enrollment", "mecatui login " + listener.Addr().String()} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("CreateSession error missing %q: %v", want, err)
		}
	}
	if !ok || reason != client.AuthNotEnrolled || strings.Contains(err.Error(), "no saved credential") {
		t.Fatalf("CreateSession error=%v reason=%q ok=%v, want server-authoritative auth recovery", err, reason, ok)
	}
}

func TestUnenrolledLocalTargetMatrixResolvesAnonymous(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })

	for _, target := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080", "unix:///run/user/1000/mecated.sock"} {
		t.Run(target, func(t *testing.T) {
			got, dial, cleanup, err := resolveTransport(t.Context(), config{transportMode: modeConnect, connectAddress: target})
			defer cleanup()
			if err != nil || got != target || dial.Server != target || dial.AuthToken != "" || dial.TokenSource != nil {
				t.Fatalf("resolve = target %q dial %#v err %v, want anonymous local", got, dial, err)
			}
		})
	}
}

func TestRemoteAnonymousProductionPathResolvesTLSAndPlaintext(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	t.Setenv("MECATL_AUTH_TOKEN", "")
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clientauth-connections.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name                    string
		argv                    []string
		wantTLS                 bool
		wantPlaintextAuthorized bool
	}{
		{
			name:    "remote defaults to verified TLS",
			argv:    []string{"mecatui", "connect", "remote.example:443", "--anonymous"},
			wantTLS: true,
		},
		{
			name:                    "explicit TLS false authorizes credential-free plaintext",
			argv:                    []string{"mecatui", "connect", "remote.example:9080", "--anonymous", "--tls=false"},
			wantPlaintextAuthorized: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invocation := resolveInvocation(tc.argv)
			if invocation.err != nil {
				t.Fatalf("resolveInvocation: %v", invocation.err)
			}
			cfg, err := parseRunConfig(invocation)
			if err != nil {
				t.Fatalf("parseRunConfig: %v", err)
			}
			if !cfg.anonymous {
				t.Fatal("production parse path did not retain --anonymous")
			}
			target, dial, cleanup, err := resolveTransport(t.Context(), cfg)
			defer cleanup()
			if err != nil || target != cfg.connectAddress || dial.UseTLS != tc.wantTLS || dial.RemotePlaintextAllowed != tc.wantPlaintextAuthorized || dial.AuthToken != "" || dial.TokenSource != nil || !dial.ExplicitAnonymous {
				t.Fatalf("target=%q dial=%#v err=%v", target, dial, err)
			}
		})
	}
}

func TestExplicitStaticTokenBypassesSavedEnrollmentLookup(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clientauth-connections.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}

	target, dial, cleanup, err := resolveTransport(t.Context(), config{
		transportMode:  modeConnect,
		connectAddress: "remote.example:443",
		authToken:      "explicit-token",
		useTLS:         true,
	})
	defer cleanup()
	if err != nil || target != "remote.example:443" || dial.AuthToken != "explicit-token" || dial.TokenSource != nil || dial.ExplicitAnonymous {
		t.Fatalf("target=%q dial=%#v err=%v, want explicit token without registry access", target, dial, err)
	}
}

func TestMalformedOrUnreadableRegistryFailsClosedForAllTargets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "malformed", setup: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unreadable shape", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldConfigHome := xdg.ConfigHome
			xdg.ConfigHome = t.TempDir()
			t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
			root := filepath.Join(xdg.ConfigHome, "mecatl")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, filepath.Join(root, "clientauth-connections.json"))

			for _, target := range []string{"127.0.0.1:8080", "remote.example:443"} {
				_, dial, cleanup, err := resolveTransport(t.Context(), config{transportMode: modeConnect, connectAddress: target, useTLS: true})
				cleanup()
				reason, ok := client.AuthFailure(err, false)
				if dial.Server != "" || !ok || reason != client.AuthStorageUnavailable {
					t.Fatalf("target=%q dial=%#v err=%v reason=%q ok=%v, want fail-closed storage error", target, dial, err, reason, ok)
				}
			}
		})
	}
}

func TestSavedLocalEnrollmentIsNotIgnored(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	registry, err := clientauth.OpenRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Upsert(clientauth.Connection{Identity: clientauth.Identity{
		Target: "127.0.0.1:8080", Issuer: "https://issuer.example", ClientID: "client",
		Audience: "audience", RedirectURI: "http://127.0.0.1:18473/oauth/callback", Scopes: []string{"openid"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	_, dial, cleanup, resolveErr := resolveTransport(t.Context(), config{transportMode: modeConnect, connectAddress: "127.0.0.1:8080"})
	defer cleanup()
	reason, ok := client.AuthFailure(resolveErr, false)
	if dial.Server != "" || !ok || reason != client.AuthStorageUnavailable {
		t.Fatalf("dial=%#v err=%v reason=%q ok=%v, want saved-enrollment credential path", dial, resolveErr, reason, ok)
	}
}

func TestUnenrolledRemoteTargetResolvesCredentialFreeWithVerifiedTLS(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })

	invocation := resolveInvocation([]string{"mecatui", "connect", "new.example:443"})
	if invocation.err != nil {
		t.Fatal(invocation.err)
	}
	cfg, err := parseRunConfig(invocation)
	if err != nil {
		t.Fatal(err)
	}
	target, dial, cleanup, err := resolveTransport(t.Context(), cfg)
	defer cleanup()
	if err != nil || target != "new.example:443" || dial.Server != target || !dial.UseTLS || dial.Insecure || dial.RemotePlaintextAllowed || dial.AuthToken != "" || dial.TokenSource != nil {
		t.Fatalf("target=%q dial=%#v err=%v, want credential-free verified-TLS dial", target, dial, err)
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
