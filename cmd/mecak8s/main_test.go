package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/testutil/codextest"
)

func TestADR_0290_TerminationBudgetFitsPodGracePeriod(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	const preStop = drainPropagationDelay
	used := preStop + cfg.drainTimeout + cfg.grpcStopTimeout + cfg.httpShutdownTimeout + cfg.closeTimeout + cfg.otlpShutdownTimeout
	if used >= 60*time.Second {
		t.Fatalf("default termination budget = %s, want < 60s (preStop=%s drain=%s grpc=%s http=%s close=%s telemetry=%s)", used, preStop, cfg.drainTimeout, cfg.grpcStopTimeout, cfg.httpShutdownTimeout, cfg.closeTimeout, cfg.otlpShutdownTimeout)
	}

	custom, err := parseFlags([]string{"--drain-timeout=1s", "--grpc-stop-timeout=2s", "--http-shutdown-timeout=3s", "--close-timeout=4s"})
	if err != nil {
		t.Fatal(err)
	}
	if custom.drainTimeout != time.Second || custom.grpcStopTimeout != 2*time.Second || custom.httpShutdownTimeout != 3*time.Second || custom.closeTimeout != 4*time.Second {
		t.Fatalf("custom shutdown bounds = (%s, %s, %s, %s), want (1s, 2s, 3s, 4s)", custom.drainTimeout, custom.grpcStopTimeout, custom.httpShutdownTimeout, custom.closeTimeout)
	}
	for _, flag := range []string{"drain-timeout", "grpc-stop-timeout", "http-shutdown-timeout", "close-timeout"} {
		if _, err := parseFlags([]string{"--" + flag + "=0s"}); err == nil {
			t.Errorf("--%s accepted a non-positive duration", flag)
		}
	}
}

func TestVersionInvocationIsExact(t *testing.T) {
	if !buildinfo.IsVersion([]string{"mecak8s", "--version"}) {
		t.Fatal("exact --version was not recognized")
	}
	for _, args := range [][]string{{"--version", "--mock"}, {"-version"}} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%v) accepted a non-exact version invocation", args)
		}
	}
}

func TestMecak8sRejectsOpenAICodexCredential(t *testing.T) {
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	path := filepath.Join(t.TempDir(), "auth.yaml")
	body := fmt.Sprintf("providers:\n  openai-codex:\n    oauth:\n      access_token: %s\n      account_id: acct-k8s\n      expires_at: %s\n", codextest.Token(expires, "acct-k8s"), expires.Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := parseFlags([]string{"--auth-file", path})
	if err == nil {
		t.Fatal("mecak8s accepted an openai-codex credential")
	}
	for _, want := range []string{"mecak8s", "openai-codex", "unsupported", "mecated"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// TestParseFlagsK8sDefaults asserts the k8s-native defaults parse: --headless
// defaults true, --posture defaults "auto", --session-lease-k8s-namespace
// defaults "mecatl", and --grpc-addr/--http-addr/--drain-addr bind 0.0.0.0,
// and --redis-url is empty by default (storage-free is opt-in via the flag, not forced).
func TestParseFlagsK8sDefaults(t *testing.T) {
	def, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if !def.headless {
		t.Errorf("headless default = false, want true (mecak8s is a headless daemon)")
	}
	if def.posture != "auto" {
		t.Errorf("posture default = %q, want auto (recommended UNATTENDED tier)", def.posture)
	}
	if def.sessionLeaseK8sNamespace != defaultK8sLeaseNamespace {
		t.Errorf("sessionLeaseK8sNamespace default = %q, want %q", def.sessionLeaseK8sNamespace, defaultK8sLeaseNamespace)
	}
	if def.grpcAddr != defaultGRPCAddr {
		t.Errorf("grpcAddr default = %q, want %q (a pod binds 0.0.0.0)", def.grpcAddr, defaultGRPCAddr)
	}
	if def.workspace != "" {
		t.Errorf("workspace default = %q, want empty (file-less by default; a container cwd must never become the agent workspace)", def.workspace)
	}
	if def.httpAddr != defaultHTTPAddr {
		t.Errorf("httpAddr default = %q, want %q", def.httpAddr, defaultHTTPAddr)
	}
	if def.drainAddr != defaultDrainAddr {
		t.Errorf("drainAddr default = %q, want %q", def.drainAddr, defaultDrainAddr)
	}
	if def.redisURL != "" {
		t.Errorf("redisURL default = %q, want empty (storage-free is opt-in)", def.redisURL)
	}
	if def.postureFlagSet {
		t.Error("postureFlagSet default = true, want false (flag not given)")
	}
	if def.reasoningEffort != "" {
		t.Errorf("reasoningEffort default = %q, want empty (unset = provider default)", def.reasoningEffort)
	}
	if def.reasoningEffortFlagSet {
		t.Error("reasoningEffortFlagSet default = true, want false (flag not given)")
	}
	// The cadence-floor security default (ADR 0073, the panel-review repair):
	// --scheduler-min-interval defaults to 1m (NOT 0/off), so the on-by-default
	// scheduler + the floor-Allow Schedule tool cannot mint an unbounded
	// tight-cadence recurring fire out of the box.
	if def.schedulerMinInterval != time.Minute {
		t.Errorf("--scheduler-min-interval default = %v, want 1m (the bounded-by-default cadence floor)", def.schedulerMinInterval)
	}
}

// TestAppConfigMapsK8sFields asserts appConfig threads the k8s-native fields
// onto the shared app.Config: RedisURL, SessionLeaseK8sNamespace, the headless
// inversion (Interactive=!headless), and the posture.
func TestAppConfigMapsK8sFields(t *testing.T) {
	cfg, err := parseFlags([]string{
		"--redis-url", "redis:6379",
		"--redis-allow-plaintext",
		"--redis-username-file", "/var/run/redis/username",
		"--redis-password-file", "/var/run/redis/password",
		"--redis-tls-ca", "/var/run/redis/ca.pem",
		"--redis-tls",
		"--session-lease-k8s-namespace", "myns",
		"--headless=false",
		"--posture", "trusted",
		"--reasoning-effort", "high",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
	if ac.ServerImplementation != mecak8sServerImplementation {
		t.Errorf("app.Config ServerImplementation = %q, want %q", ac.ServerImplementation, mecak8sServerImplementation)
	}
	if ac.RedisURL != "redis:6379" {
		t.Errorf("app.Config RedisURL = %q, want redis:6379", ac.RedisURL)
	}
	if !ac.RedisAllowPlaintext {
		t.Error("app.Config RedisAllowPlaintext = false after --redis-allow-plaintext")
	}
	if ac.RedisUsernameFile != "/var/run/redis/username" || ac.RedisPasswordFile != "/var/run/redis/password" || ac.RedisTLSCAFile != "/var/run/redis/ca.pem" {
		t.Error("app.Config Redis Secret file paths did not match parsed paths")
	}
	if !ac.RedisTLS {
		t.Error("app.Config RedisTLS = false after --redis-tls")
	}
	if ac.SessionLeaseK8sNamespace != "myns" {
		t.Errorf("app.Config SessionLeaseK8sNamespace = %q, want myns", ac.SessionLeaseK8sNamespace)
	}
	if !ac.Interactive {
		t.Error("app.Config Interactive = false with --headless=false, want true (Interactive=!headless)")
	}
	if ac.Posture != app.PostureTrusted {
		t.Errorf("app.Config Posture = %v, want PostureTrusted", ac.Posture)
	}
	if !cfg.postureFlagSet {
		t.Error("postureFlagSet = false after --posture, want true")
	}
	if ac.ReasoningEffort != "high" {
		t.Errorf("app.Config ReasoningEffort = %q, want high", ac.ReasoningEffort)
	}
	if !ac.ReasoningEffortFlagSet {
		t.Error("app.Config ReasoningEffortFlagSet = false after --reasoning-effort, want true")
	}
	if !cfg.reasoningEffortFlagSet {
		t.Error("reasoningEffortFlagSet = false after --reasoning-effort, want true")
	}
}

// TestAppConfigHeadlessDefault asserts the headless DEFAULT (true) maps to
// Interactive=false so a child's unresolved ask engages the auto-deny path.
func TestAppConfigHeadlessDefault(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
	if ac.Interactive {
		t.Error("default mecak8s must be Interactive=false (headless=true default) so the auto-deny/reviewer path engages")
	}
}

func TestMecak8sDefaultsToNoFS(t *testing.T) {
	cfg, err := parseFlags([]string{"--mock", "--posture", "strict", "--no-soul", "--no-user-model", "--permissions-conventional=false", "--agents-conventional=false"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	// No --workspace: the default is a file-less deployment. The flag no longer
	// defaults to the process cwd, so a container root can never become the agent
	// workspace by omission (the reason this used to force cfg.workspace = "/").
	if cfg.workspace != "" {
		t.Fatalf("default workspace = %q, want empty (file-less by default)", cfg.workspace)
	}
	// Disable the k8s session lease: this offline test has no kubeconfig/in-cluster
	// config, and app.Build builds a lease client when the namespace is set.
	cfg.sessionLeaseK8sNamespace = ""
	built, err := app.Build(context.Background(), appConfig(cfg, port.NopDiagnostics{}, observability{}))
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	harness := server.NewHarnessServer(built.Service)
	resp, err := harness.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession(empty wire profile and workspace): %v", err)
	}
	sess, err := built.Service.GetSession(context.Background(), session.SessionID(resp.GetSessionId()))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Profile != string(server.ProfileNoFS) {
		t.Errorf("session profile = %q, want %q", sess.Profile, server.ProfileNoFS)
	}
	if sess.EnvironmentRef.Kind != session.EnvKindNoFS {
		t.Errorf("session workspace = %q, want empty for no-FS", sess.EnvironmentRef)
	}
}

func TestMecak8sRejectsUnsupportedFilesystemProfile(t *testing.T) {
	cfg, err := parseFlags([]string{"--mock", "--posture", "strict", "--no-soul", "--no-user-model", "--permissions-conventional=false", "--agents-conventional=false"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	// Disable the k8s session lease (no kubeconfig in this offline test).
	cfg.sessionLeaseK8sNamespace = ""
	built, err := app.Build(context.Background(), appConfig(cfg, port.NopDiagnostics{}, observability{}))
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	harness := server.NewHarnessServer(built.Service)
	if _, err := harness.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Profile: string(server.ProfileNoFS)}); err != nil {
		t.Fatalf("CreateSession(no-fs, empty workspace): %v", err)
	}
	for _, tc := range []struct {
		name string
		req  *mecatlv1.CreateSessionRequest
	}{
		{name: "unsupported filesystem profile", req: &mecatlv1.CreateSessionRequest{Profile: "filesystem"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := harness.CreateSession(context.Background(), tc.req)
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Fatalf("CreateSession(%+v) status = %s, want %s (error: %v)", tc.req, got, codes.InvalidArgument, err)
			}
		})
	}
}

func TestMecak8sMountedWorkspaceIsServerAssigned(t *testing.T) {
	// A configured --workspace (a mounted PVC path) is an operator-enabled
	// filesystem deployment: server-assigned authority rooted at the mount, so a
	// default-profile session mints on that root and a client cannot select
	// another. A real temp dir stands in for the mount.
	mount := t.TempDir()
	cfg, err := parseFlags([]string{"--workspace", mount, "--mock", "--posture", "strict", "--no-soul", "--no-user-model", "--permissions-conventional=false", "--agents-conventional=false"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	// Disable the k8s session lease (no kubeconfig in this offline test).
	cfg.sessionLeaseK8sNamespace = ""
	ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
	if ac.Workspace != mount {
		t.Fatalf("workspace = %q, want the mount %q", ac.Workspace, mount)
	}

	built, err := app.Build(context.Background(), ac)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()
	harness := server.NewHarnessServer(built.Service)

	// An omitted (empty) workspace requests the operator root; the session mints
	// on the mount as a default-profile (filesystem) session, not no-FS.
	resp, err := harness.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession(empty): %v", err)
	}
	sess, err := built.Service.GetSession(context.Background(), session.SessionID(resp.GetSessionId()))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Profile != "" {
		t.Errorf("session profile = %q, want default (filesystem) on a mounted deployment", sess.Profile)
	}
	if sess.EnvironmentRef.ID != mount {
		t.Errorf("private session placement ID = %q, want exact configured mount %q", sess.EnvironmentRef.ID, mount)
	}

	// A client cannot send placement authority; the generated request has no such field.
}

func TestParseFlagsMecak8sRejectsRelativeWorkspace(t *testing.T) {
	if _, err := parseFlags([]string{"--workspace", "relative/mount"}); err == nil {
		t.Fatal("parseFlags(--workspace relative/mount) = nil, want an absolute-path error")
	}
}

func TestMecak8sFixtureRunsNoFS(t *testing.T) {
	cfg, err := parseFlags([]string{"--mock", "--posture", "strict", "--no-soul", "--no-user-model", "--permissions-conventional=false", "--agents-conventional=false"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	// No --workspace: file-less by default (the flag no longer defaults to cwd, so
	// the container root cannot become the agent workspace by omission).
	cfg.sessionLeaseK8sNamespace = ""
	appCfg := appConfig(cfg, port.NopDiagnostics{}, observability{})
	if appCfg.Workspace != "" {
		t.Fatalf("mecak8s app workspace = %q, want empty: the container root must not be an agent workspace", appCfg.Workspace)
	}
	built, err := app.Build(context.Background(), appCfg)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "hello from mecak8s")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if stop != session.StopEndTurn {
		t.Errorf("run stop = %q, want %q", stop, session.StopEndTurn)
	}
}

// TestBuildOverRedisDrivesRunToCompletion is the composition e2e: a full
// app.Build with --mock + a real (miniredis) Redis store, then a run driven to
// completion through the Service. It proves the k8s-native composition wiring
// (Redis session store + durable event log, headless, auto posture) assembles
// and serves a run end-to-end. Fully offline: mockllm + miniredis, no API key,
// no network.
func TestBuildOverRedisDrivesRunToCompletion(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	// A k8s lease namespace would require a real apiserver; leave it empty so
	// no lease is wired (the drain gate + run path are exercised regardless).
	built, err := app.Build(context.Background(), app.Config{
		Workspace:               t.TempDir(),
		UseMock:                 true,
		NoSoul:                  true,
		NoUserModel:             true,
		RedisURL:                mr.Addr(),
		RedisAllowPlaintext:     true,
		Interactive:             false, // mecak8s headless default (Interactive=!headless)
		Posture:                 app.PostureAuto,
		PermissionsConventional: false,
		AgentsConventional:      false,
		Diagnostics:             port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("app.Build over Redis: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "hello from mecak8s")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if stop != session.StopEndTurn {
		t.Fatalf("run stop = %q, want end_turn (the mock produces a clean text turn)", stop)
	}

	// The session must be persisted to Redis (storage-free): a fresh Load
	// recovers it, proving the store is wired through composition.
	loaded, lerr := built.Service.GetSession(context.Background(), sess.ID)
	if lerr != nil {
		t.Fatalf("GetSession after run (Redis persistence): %v", lerr)
	}
	if loaded.ID != sess.ID {
		t.Errorf("loaded session ID = %q, want %q", loaded.ID, sess.ID)
	}
}

// TestDrainRejectsNewRunsViaComposition asserts the drain gate (ADR 0048)
// works through the composition-built Service: after Drain, StartRun returns
// ErrUnavailable and IsDraining reports true. It exercises the IsDraining
// method the /readyz ReadyFunc closes over.
func TestDrainRejectsNewRunsViaComposition(t *testing.T) {
	built, err := app.Build(context.Background(), app.Config{
		Workspace:               t.TempDir(),
		UseMock:                 true,
		NoSoul:                  true,
		NoUserModel:             true,
		PermissionsConventional: false,
		AgentsConventional:      false,
		Diagnostics:             port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	if built.Service.IsDraining() {
		t.Fatal("IsDraining = true on a fresh service, want false")
	}
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	built.Service.Drain()
	if !built.Service.IsDraining() {
		t.Fatal("IsDraining = false after Drain, want true")
	}
	_, err = built.Service.StartRun(context.Background(), sess.ID, "after drain")
	if !errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("StartRun after Drain = %v, want ErrUnavailable (503 via the drain gate)", err)
	}
}

// TestDrainHTTPRouting keeps lifecycle traffic off the normal API listener. The
// drain handler is plaintext and isolated so a TLS/authenticated API endpoint
// cannot expose a credential-free drain operation.
func TestDrainHTTPRouting(t *testing.T) {
	built, err := app.Build(context.Background(), app.Config{
		Workspace:               t.TempDir(),
		UseMock:                 true,
		NoSoul:                  true,
		NoUserModel:             true,
		PermissionsConventional: false,
		AgentsConventional:      false,
		Diagnostics:             port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "test-token"})
	defer auth.Close()
	normal := httptest.NewServer(normalHTTPMux(built.Service, auth))
	defer normal.Close()
	drain := httptest.NewServer(drainHTTPMux(built.Service, func(time.Duration) {}))
	defer drain.Close()

	if code := probe(t, normal.URL+"/readyz"); code != http.StatusOK {
		t.Fatalf("normal /readyz = %d, want 200", code)
	}
	if code := probe(t, normal.URL+"/v1/info"); code != http.StatusUnauthorized {
		t.Fatalf("normal unauthenticated API = %d, want 401", code)
	}
	req, err := http.NewRequest(http.MethodGet, normal.URL+"/v1/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("normal authenticated API = %d, want 200", resp.StatusCode)
	}
	req, err = http.NewRequest(http.MethodGet, normal.URL+"/drain", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("normal authenticated /drain = %d, want 404", resp.StatusCode)
	}

	if code := probeMethod(t, http.MethodHead, drain.URL+"/drain"); code != http.StatusMethodNotAllowed {
		t.Fatalf("drain HEAD /drain = %d, want 405", code)
	}
	if built.Service.IsDraining() {
		t.Fatal("IsDraining = true after HEAD /drain, want false")
	}

	resp, err = http.Get(drain.URL + "/drain")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "draining\n" {
		t.Fatalf("drain GET = %d %q, want 200 %q", resp.StatusCode, body, "draining\n")
	}
	if !built.Service.IsDraining() {
		t.Fatal("IsDraining = false after drain endpoint, want true")
	}
	if code := probe(t, normal.URL+"/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("normal /readyz after drain = %d, want 503", code)
	}
	if code := probe(t, drain.URL+"/other"); code != http.StatusNotFound {
		t.Fatalf("drain /other = %d, want 404", code)
	}
	if code := probeMethod(t, http.MethodPost, drain.URL+"/drain"); code != http.StatusMethodNotAllowed {
		t.Fatalf("drain POST /drain = %d, want 405", code)
	}
}

func TestServeWiresSeparateDrainListener(t *testing.T) {
	cfg, err := parseFlags([]string{
		"--mock",
		"--grpc-addr", freeLoopbackPort(t),
		"--http-addr", freeLoopbackPort(t),
		"--drain-addr", freeLoopbackPort(t),
		"--session-lease-k8s-namespace", "",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	built, err := app.Build(context.Background(), appConfig(cfg, port.NopDiagnostics{}, observability{}))
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serveWithDrainWait(ctx, cfg, built.Service, observability{}, func(time.Duration) {})
	}()

	waitForHTTPStatus(t, "http://"+cfg.httpAddr+"/drain", http.StatusNotFound)
	if built.Service.IsDraining() {
		t.Fatal("normal listener drained the service")
	}
	waitForHTTPStatus(t, "http://"+cfg.drainAddr+"/drain", http.StatusOK)
	if !built.Service.IsDraining() {
		t.Fatal("drain listener did not drain the service")
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop after cancellation")
	}
}

func waitForHTTPStatus(t *testing.T, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s did not return %d", url, want)
}

func TestListenCoreListenersClosesEarlierListenersOnDrainFailure(t *testing.T) {
	drain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = drain.Close() }()
	cfg := config{grpcAddr: freeLoopbackPort(t), httpAddr: freeLoopbackPort(t), drainAddr: drain.Addr().String()}
	_, _, _, err = listenCoreListeners(cfg)
	if err == nil {
		t.Fatal("listenCoreListeners succeeded with occupied drain address")
	}
	for _, addr := range []string{cfg.grpcAddr, cfg.httpAddr} {
		lis, listenErr := net.Listen("tcp", addr)
		if listenErr != nil {
			t.Fatalf("listener %s remained open after drain failure: %v", addr, listenErr)
		}
		_ = lis.Close()
	}
}

// TestStorageReadyViaComposition asserts Service.StorageReady (the readiness
// probe the /readyz ReadyFunc closes over) pings the SAME store the Service
// serves traffic through — not a second client. Over a live miniredis it
// reports true; after the broker closes it reports false (so /readyz flips
// not-ready on a Redis outage); a memstore-backed service (no Ping method) is
// always ready.
func TestStorageReadyViaComposition(t *testing.T) {
	// Redis-backed service: pings the store the Service serves traffic through.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	built, err := app.Build(context.Background(), app.Config{
		Workspace:               t.TempDir(),
		UseMock:                 true,
		NoSoul:                  true,
		NoUserModel:             true,
		RedisURL:                mr.Addr(),
		RedisAllowPlaintext:     true,
		PermissionsConventional: false,
		AgentsConventional:      false,
		Diagnostics:             port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("app.Build over Redis: %v", err)
	}
	defer built.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !built.Service.StorageReady(ctx) {
		t.Error("StorageReady on a live miniredis = false, want true")
	}
	mr.Close()
	// After the broker closes, the ping must fail (a short-timeout ctx keeps it
	// from wedging). Allow a brief window for the client to notice.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if !built.Service.StorageReady(pingCtx) {
			pingCancel()
			return
		}
		pingCancel()
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("StorageReady on a closed miniredis stayed true, want false (Redis outage → /readyz not-ready)")
}

// TestParseFlagsHasNoStoreDir asserts mecak8s wires NO --store-dir flag at all
// (storage-free): the flag is simply absent from the FlagSet.
func TestParseFlagsHasNoStoreDir(t *testing.T) {
	if _, err := parseFlags([]string{"--store-dir", "/tmp/x"}); err == nil {
		t.Fatal("parseFlags(--store-dir) = nil, want an error (mecak8s is storage-free; --store-dir is not a flag)")
	}
}

func probeMethod(t *testing.T, method, url string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new %s request: %v", method, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// probe issues a GET and returns the status code.
func probe(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}
