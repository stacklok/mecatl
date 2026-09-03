package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
	"github.com/stacklok/mecatl/internal/adapter/tlsreload"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// serve wires the built Service over gRPC + HTTP/SSE with k8s-native
// operability: a DYNAMIC /readyz (drain-gated + storage-pinged via the SAME
// store the Service serves traffic through), a drain-only listener (the preStop
// hook target), and a BOUNDED GracefulStop on SIGTERM that cancels in-flight
// runs within the termination grace period.
//
// HONEST SHUTDOWN CONTRACT (ADR 0048 §4d): new runs are rejected (503 via the
// drain gate) the moment SIGTERM (or the preStop httpGet /drain) fires.
// In-flight runs are CANCELLED, not drained to completion — a multi-minute LLM
// turn cannot survive a rolling update within terminationGracePeriodSeconds:
// 60. The pod is disposable; the session is not — it is Recover-able on the
// successor (issue #51) from the Redis snapshot + durable event log. This is
// the cloud-native disposability property, stated honestly rather than hidden
// behind an unbounded GracefulStop that would wedge a rolling update.
//
// Readiness closes over svc.StorageReady, which pings the SAME store the
// Service serves traffic through (Service.StorageReady type-asserts the store
// for a Pinger). A non-Redis store (the in-memory fallback) has no ping, so
// readiness is drain-gated only.
func normalHTTPMux(svc *server.Service, auth *server.Authenticator) http.Handler {
	ready := server.ReadyFunc(func() bool {
		if svc.IsDraining() {
			return false
		}
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return svc.StorageReady(pingCtx)
	})

	mux := http.NewServeMux()
	server.NewHealthHandler(ready).RegisterHealth(mux)
	mux.Handle("/", auth.Middleware(server.NewHTTPHandler(svc)))
	return mux
}

func drainHTTPMux(svc *server.Service, wait func(time.Duration)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/drain", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		svc.Drain()
		slog.Info("drain armed via /drain; blocking for endpoint propagation", "delay", drainPropagationDelay)
		wait(drainPropagationDelay)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("draining\n"))
	})
	return mux
}

func listenCoreListeners(cfg config) (net.Listener, net.Listener, net.Listener, error) {
	grpcLis, err := net.Listen("tcp", cfg.grpcAddr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("listen grpc %q: %w", cfg.grpcAddr, err)
	}
	httpLis, err := net.Listen("tcp", cfg.httpAddr)
	if err != nil {
		_ = grpcLis.Close()
		return nil, nil, nil, fmt.Errorf("listen http %q: %w", cfg.httpAddr, err)
	}
	drainLis, err := net.Listen("tcp", cfg.drainAddr)
	if err != nil {
		_ = httpLis.Close()
		_ = grpcLis.Close()
		return nil, nil, nil, fmt.Errorf("listen drain %q: %w", cfg.drainAddr, err)
	}
	return grpcLis, httpLis, drainLis, nil
}

func startMetricsServer(addr string, obs observability, errCh chan<- error) (*http.Server, error) {
	if addr == "" {
		return nil, nil
	}
	if obs.Registry == nil {
		return nil, fmt.Errorf("metrics-addr %q set but telemetry registry is nil", addr)
	}
	metricsLis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen metrics %q: %w", addr, err)
	}
	metricsSrv := &http.Server{
		Addr:              addr,
		Handler:           telemetry.NewAdminMux(obs.Registry, nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("metrics admin listener serving (loopback)", "addr", metricsLis.Addr().String())
		if serveErr := metricsSrv.Serve(metricsLis); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("metrics serve: %w", serveErr)
		}
	}()
	return metricsSrv, nil
}

func serve(ctx context.Context, cfg config, svc *server.Service, obs observability) error {
	return serveWithDrainWait(ctx, cfg, svc, obs, time.Sleep)
}

func serveWithDrainWait(ctx context.Context, cfg config, svc *server.Service, obs observability, drainWait func(time.Duration)) error {
	tlsCfg, tlsLifecycle, err := buildTLSConfig(cfg)
	if err != nil {
		return err
	}
	defer closeTLSLifecycle(tlsLifecycle)

	auth, err := newAuthenticator(ctx, cfg)
	if err != nil {
		return err
	}
	defer auth.Close()

	// --- gRPC: auth interceptors, standard health service ---
	grpcOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(auth.UnaryInterceptor()),
		grpc.StreamInterceptor(auth.StreamInterceptor()),
	}
	if tlsCfg != nil {
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	mecatlv1.RegisterHarnessServiceServer(grpcSrv, server.NewHarnessServer(svc))
	mecatlv1.RegisterScheduleServiceServer(grpcSrv, server.NewScheduleServer(svc))
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("mecatl.v1.HarnessService", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("mecatl.v1.ScheduleService", healthpb.HealthCheckResponse_SERVING)

	// HTTP/SSE carries health endpoints outside auth and the API inside auth.
	httpSrv := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           normalHTTPMux(svc, auth),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         tlsCfg,
	}
	drainSrv := &http.Server{
		Addr:              cfg.drainAddr,
		Handler:           drainHTTPMux(svc, drainWait),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// A bearer token, OIDC, or verified mTLS authenticates callers. Ordinary
	// server TLS only authenticates the server.
	authed := callerAuthenticationConfigured(cfg, tlsCfg)
	warnIfNonLoopback("grpc-addr", cfg.grpcAddr, authed)
	warnIfNonLoopback("http-addr", cfg.httpAddr, authed)
	warnDrainExposure(cfg.drainAddr)

	grpcLis, httpLis, drainLis, err := listenCoreListeners(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = grpcLis.Close() }()
	defer func() { _ = httpLis.Close() }()
	defer func() { _ = drainLis.Close() }()

	errCh := make(chan error, 4)

	metricsSrv, err := startMetricsServer(cfg.metricsAddr, obs, errCh)
	if err != nil {
		return err
	}

	go func() {
		slog.Info("gRPC server listening", "addr", grpcLis.Addr().String())
		if serveErr := grpcSrv.Serve(grpcLis); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			errCh <- fmt.Errorf("grpc serve: %w", serveErr)
		}
	}()

	go func() {
		slog.Info("HTTP/SSE server listening", "addr", httpLis.Addr().String(), "tls", tlsCfg != nil)
		var serveErr error
		if tlsCfg != nil {
			serveErr = httpSrv.ServeTLS(httpLis, "", "")
		} else {
			serveErr = httpSrv.Serve(httpLis)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http serve: %w", serveErr)
		}
	}()

	go func() {
		slog.Info("drain server listening", "addr", drainLis.Addr().String())
		if serveErr := drainSrv.Serve(drainLis); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("drain serve: %w", serveErr)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received; draining")
	case err := <-errCh:
		slog.Error("server failed; shutting down", "err", err)
		boundedShutdown(grpcSrv, httpSrv, drainSrv, metricsSrv, svc)
		return err
	}

	boundedShutdown(grpcSrv, httpSrv, drainSrv, metricsSrv, svc)
	return nil
}

// newAuthenticator constructs the caller-identity boundary with the server-root
// context so its JWKS refresh survives individual requests. A broken OIDC setup
// is fatal: the pod must not silently serve unauthenticated traffic.
func newAuthenticator(ctx context.Context, cfg config) (*server.Authenticator, error) {
	// Log before construction so an insecure test-only relaxation is visible even
	// when validator construction then fails.
	warnInsecureIssuer(cfg.oidc)
	if err := cliconfig.ValidateOIDCAuthToken(cfg.oidc, cfg.authToken); err != nil {
		return nil, err
	}
	validator, err := cliconfig.OIDCValidator(ctx, cfg.oidc)
	if err != nil {
		return nil, err
	}
	return server.NewAuthenticator(server.SecurityConfig{
		AuthToken:   cfg.authToken,
		Validator:   validator,
		Diagnostics: cfg.diagnostics,
		// No rate limiting on a pod: it is fronted by the Service/mesh, not a
		// raw public port. RateBurst 0 leaves the authenticator's rate limiter
		// disabled.
	}), nil
}

// boundedShutdown is the ADR-0048-§4d shutdown sequence:
//  1. arm the drain gate (new runs → 503); /readyz flips not-ready.
//  2. log "draining: N active runs" (Service.ActiveRuns()).
//  3. grpcSrv.GracefulStop() in a goroutine + select on gracefulStopTimeout.
//  4. on timeout: grpcSrv.Stop() (hard) — in-flight runs cancelled,
//     Recover-able on the successor (issue #51).
//  5. httpSrv + drainSrv Shutdown(10s ctx).
//
// built.Close() (→ Service.Close()) is the CALLER's deferred responsibility
// (run() defers it); it stops the held-lease renewers and releases every held
// coordination.k8s.io Lease with a cancel-detached short ctx so a survivor
// takes over immediately without the 30s TTL. The ctx that reached serve is
// already cancelled by the signal handler, so boundedShutdown uses a fresh
// background ctx for the HTTP shutdown.
func boundedShutdown(grpcSrv *grpc.Server, httpSrv *http.Server, drainSrv *http.Server, metricsSrv *http.Server, svc *server.Service) {
	svc.Drain()
	slog.Info("draining: active runs", "count", svc.ActiveRuns())

	stopped := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
		slog.Info("gRPC server stopped gracefully")
	case <-time.After(gracefulStopTimeout):
		slog.Warn("graceful stop timed out; hard-stopping gRPC (in-flight runs cancelled, Recover-able on successor)", "timeout", gracefulStopTimeout)
		grpcSrv.Stop()
		<-stopped
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http graceful shutdown", "err", err)
	}
	if err := drainSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("drain graceful shutdown", "err", err)
	}
	// The loopback metrics listener stops alongside the API listener; a metrics
	// scrape failure during shutdown is non-fatal, so its error is logged only.
	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("metrics graceful shutdown", "err", err)
		}
	}
}

func closeTLSLifecycle(reloader *tlsreload.Reloader) {
	if reloader != nil {
		_ = reloader.Close()
	}
}

// buildTLSConfig assembles the *tls.Config for the gRPC + HTTP servers from the
// TLS flags. It returns nil (plaintext) when neither --tls-cert nor --tls-key
// is set. The returned lifecycle owns the projected-volume watcher and must be
// closed after both servers stop. Client CA material deliberately remains static.
func buildTLSConfig(cfg config) (*tls.Config, *tlsreload.Reloader, error) {
	if cfg.tlsCert == "" && cfg.tlsKey == "" {
		if cfg.clientCA != "" {
			return nil, nil, errors.New("--client-ca requires --tls-cert/--tls-key (mTLS needs server TLS)")
		}
		return nil, nil, nil
	}
	if cfg.tlsCert == "" || cfg.tlsKey == "" {
		return nil, nil, errors.New("--tls-cert and --tls-key must be supplied together")
	}
	reloader, err := tlsreload.New(cfg.tlsCert, cfg.tlsKey, cfg.diagnostics)
	if err != nil {
		return nil, nil, err
	}
	tlsCfg := &tls.Config{GetCertificate: reloader.GetCertificate, MinVersion: tls.VersionTLS12}
	if cfg.clientCA != "" {
		caPEM, err := tlsreload.ReadCredentialFile(cfg.clientCA)
		if err != nil {
			_ = reloader.Close()
			return nil, nil, errors.New("--client-ca load failed")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			_ = reloader.Close()
			return nil, nil, errors.New("--client-ca: no valid certificates in CA bundle")
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return tlsCfg, reloader, nil
}

func callerAuthenticationConfigured(cfg config, tlsCfg *tls.Config) bool {
	return cfg.authToken != "" || cfg.oidc.Enabled() || (tlsCfg != nil && tlsCfg.ClientAuth == tls.RequireAndVerifyClientCert)
}

// warnIfNonLoopback logs the API trust assumption for the given bind address.
// A k8s pod intentionally binds 0.0.0.0 (the endpoint controller probes it); a
// non-loopback bind WITH caller authentication (bearer, OIDC, or mTLS) is logged
// at info, and a non-loopback bind without it is logged as a prominent WARNING:
// ordinary TLS authenticates the server, not the caller. It never hard-fails:
// a trusted NetworkPolicy or mesh may deliberately be the shared authority
// boundary. Mirrors cmd/mecated's warnIfNonLoopback.
func warnIfNonLoopback(flagName, addr string, callerAuthenticated bool) {
	if cliconfig.IsLoopbackAddr(addr) {
		slog.Info("API bound to loopback", "flag", flagName, "addr", addr, "caller_authenticated", callerAuthenticated)
		return
	}
	if callerAuthenticated {
		slog.Info("API bound to a non-loopback address WITH caller authentication (bearer, OIDC, or mTLS)", "flag", flagName, "addr", addr)
		return
	}
	slog.Warn("API bound to a NON-loopback address with NO caller authentication: it exposes UNAUTHENTICATED command/file execution to every network caller — configure --auth-token, OIDC, or --client-ca, or deliberately enforce shared authority at a trusted private-network/mesh boundary; TLS alone is not caller authentication", "flag", flagName, "addr", addr)
}

// warnDrainExposure logs the drain-listener trust assumption. Unlike
// warnIfNonLoopback's other two callers, --drain-addr has NO authentication
// option at all (ADR 0290: kubelet's preStop httpGet calls it directly with
// no credentials) — so the warning wording never suggests --auth-token/
// --tls-cert, and a non-loopback bind is ALWAYS worth a WARNING, not merely
// an info line, regardless of the deployment's auth posture elsewhere.
func warnDrainExposure(addr string) {
	if cliconfig.IsLoopbackAddr(addr) {
		slog.Info("drain listener bound to loopback", "flag", "drain-addr", "addr", addr)
		return
	}
	slog.Warn("drain listener bound to a NON-loopback address: it is ALWAYS unauthenticated (kubelet preStop carries no credentials) — do not expose it beyond the Pod without a NetworkPolicy/mesh restricting access to it", "flag", "drain-addr", "addr", addr)
}

// signalCtx returns a context cancelled on SIGINT/SIGTERM. It is the serve-time
// signal boundary (mecak8s reacts to SIGTERM by draining + bounded-stopping).
func signalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// warnInsecureIssuer logs the SSRF-relaxation warning when the operator enabled
// it, and is silent otherwise. It is a function rather than an inline branch so
// the caller does not grow another decision point (gocyclo), and so both server
// mains surface the warning identically.
func warnInsecureIssuer(c cliconfig.OIDCConfig) {
	if w := c.InsecureIssuerWarning(); w != "" {
		slog.Warn(w)
	}
}
