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
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/filewatch"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// serve wires the built Service over gRPC + HTTP/SSE with k8s-native
// operability: a DYNAMIC /readyz (drain-gated + storage-pinged via the SAME
// store the Service serves traffic through), a /drain endpoint (the preStop
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
func serve(ctx context.Context, cfg config, svc *server.Service, obs observability) error {
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

	// --- HTTP: health + /drain mounted OUTSIDE auth/rate-limit; the API mux
	// wrapped in the auth middleware. The readiness probe is DYNAMIC:
	// !draining && storageReady — so SIGTERM/preStop flips /readyz to
	// not-ready (the endpoint controller removes the pod) and a Redis outage
	// does too. StorageReady pings the SAME store the Service serves traffic
	// through (not a second client), bounded by a short timeout so a stalled
	// backend fails the probe quickly rather than wedging readiness. ---
	ready := server.ReadyFunc(func() bool {
		if svc.IsDraining() {
			return false
		}
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return svc.StorageReady(pingCtx)
	})

	httpMux := http.NewServeMux()
	healthH := server.NewHealthHandler(ready)
	healthH.RegisterHealth(httpMux)
	// /drain: the preStop hook target. Arms the drain gate (new runs → 503),
	// blocks ~drainPropagationDelay for endpoint propagation, then returns 200.
	// Mounted OUTSIDE auth (like the health endpoints) so the kubelet can call
	// it without credentials.
	httpMux.HandleFunc("GET /drain", func(w http.ResponseWriter, _ *http.Request) {
		svc.Drain()
		slog.Info("drain armed via /drain; blocking for endpoint propagation", "delay", drainPropagationDelay)
		time.Sleep(drainPropagationDelay)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("draining\n"))
	})
	httpMux.Handle("/", auth.Middleware(server.NewHTTPHandler(svc)))
	httpSrv := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           httpMux,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         tlsCfg,
	}

	// Caller identity counts as authentication: an OIDC deployment may carry no
	// static token at all.
	authed := cfg.authToken != "" || cfg.oidc.Enabled() || tlsCfg != nil
	warnIfNonLoopback("grpc-addr", cfg.grpcAddr, authed)
	warnIfNonLoopback("http-addr", cfg.httpAddr, authed)

	grpcLis, err := net.Listen("tcp", cfg.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen grpc %q: %w", cfg.grpcAddr, err)
	}

	errCh := make(chan error, 3)

	// Metrics loopback listener (issue #343, ADR 0098): when --metrics-addr is
	// set, mount the admin mux (/metrics + the runtime-introspection surface) on a
	// SEPARATE loopback listener. The Registry comes from the observability
	// handles (non-nil when metricsAddr is set — buildObservability runs Setup for
	// the scrape-only path too via Scrape=true). Fail-closed: parse-time loopback
	// refusal already guaranteed by cliconfig.IsLoopbackAddr.
	var metricsSrv *http.Server
	if cfg.metricsAddr != "" {
		reg := obs.Registry
		if reg == nil {
			return fmt.Errorf("metrics-addr %q set but telemetry registry is nil", cfg.metricsAddr)
		}
		metricsMux := telemetry.NewAdminMux(reg, nil)
		metricsLis, lerr := net.Listen("tcp", cfg.metricsAddr)
		if lerr != nil {
			return fmt.Errorf("listen metrics %q: %w", cfg.metricsAddr, lerr)
		}
		metricsSrv = &http.Server{
			Addr:              cfg.metricsAddr,
			Handler:           metricsMux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			slog.Info("metrics admin listener serving (loopback)", "addr", metricsLis.Addr().String())
			if serveErr := metricsSrv.Serve(metricsLis); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				errCh <- fmt.Errorf("metrics serve: %w", serveErr)
			}
		}()
	}

	go func() {
		slog.Info("gRPC server listening", "addr", grpcLis.Addr().String())
		if serveErr := grpcSrv.Serve(grpcLis); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			errCh <- fmt.Errorf("grpc serve: %w", serveErr)
		}
	}()

	go func() {
		slog.Info("HTTP/SSE server listening", "addr", cfg.httpAddr, "tls", tlsCfg != nil)
		var serveErr error
		if tlsCfg != nil {
			serveErr = httpSrv.ListenAndServeTLS("", "")
		} else {
			serveErr = httpSrv.ListenAndServe()
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http serve: %w", serveErr)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received; draining")
	case err := <-errCh:
		slog.Error("server failed; shutting down", "err", err)
		boundedShutdown(grpcSrv, httpSrv, metricsSrv, svc)
		return err
	}

	boundedShutdown(grpcSrv, httpSrv, metricsSrv, svc)
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
//  5. httpSrv.Shutdown(10s ctx).
//
// built.Close() (→ Service.Close()) is the CALLER's deferred responsibility
// (run() defers it); it stops the held-lease renewers and releases every held
// coordination.k8s.io Lease with a cancel-detached short ctx so a survivor
// takes over immediately without the 30s TTL. The ctx that reached serve is
// already cancelled by the signal handler, so boundedShutdown uses a fresh
// background ctx for the HTTP shutdown.
func boundedShutdown(grpcSrv *grpc.Server, httpSrv *http.Server, metricsSrv *http.Server, svc *server.Service) {
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
	// The loopback metrics listener stops alongside the API listener; a metrics
	// scrape failure during shutdown is non-fatal, so its error is logged only.
	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("metrics graceful shutdown", "err", err)
		}
	}
}

const (
	tlsReloadDebounce    = 100 * time.Millisecond
	tlsReloadMaxDebounce = time.Second
)

// certificateReloader publishes complete keypairs atomically. Invalid rotations
// never displace the last certificate that passed key matching and DER parsing.
type certificateReloader struct {
	cert    atomic.Pointer[tls.Certificate]
	watcher *filewatch.Watcher
}

func newCertificateReloader(certFile, keyFile string) (*certificateReloader, error) {
	cert, err := loadServerCertificate(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	r := &certificateReloader{}
	r.cert.Store(cert)
	watcher, err := filewatch.New(
		[]string{certFile, keyFile},
		tlsReloadDebounce,
		tlsReloadMaxDebounce,
		r.reload(certFile, keyFile),
		func(err error) {
			slog.Warn("TLS certificate watch error", "component", "tls_certificate", "operation", "watch", "outcome", "failure", "err", err)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("watch TLS keypair: %w", err)
	}
	r.watcher = watcher
	return r, nil
}

func (r *certificateReloader) reload(certFile, keyFile string) func() {
	return func() {
		cert, err := loadServerCertificate(certFile, keyFile)
		if err != nil {
			slog.Warn("TLS certificate reload rejected; retaining last valid certificate", "component", "tls_certificate", "operation", "reload", "outcome", "failure", "err", err)
			return
		}
		r.cert.Store(cert)
		slog.Info("TLS certificate reloaded", "component", "tls_certificate", "operation", "reload", "outcome", "success")
	}
}

func (r *certificateReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.cert.Load(), nil
}

func (r *certificateReloader) Close() error {
	return r.watcher.Close()
}

func closeTLSLifecycle(reloader *certificateReloader) {
	if reloader == nil {
		return
	}
	if err := reloader.Close(); err != nil {
		slog.Warn("TLS certificate watcher shutdown failed", "component", "tls_certificate", "operation", "shutdown", "err", err)
	}
}

func loadServerCertificate(certFile, keyFile string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS keypair: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("load TLS keypair: certificate chain is empty")
	}
	cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse TLS leaf certificate: %w", err)
	}
	return &cert, nil
}

// buildTLSConfig assembles the *tls.Config for the gRPC + HTTP servers from the
// TLS flags. It returns nil (plaintext) when neither --tls-cert nor --tls-key
// is set. The returned lifecycle owns the projected-volume watcher and must be
// closed after both servers stop. Client CA material deliberately remains static.
func buildTLSConfig(cfg config) (*tls.Config, *certificateReloader, error) {
	if cfg.tlsCert == "" && cfg.tlsKey == "" {
		if cfg.clientCA != "" {
			return nil, nil, errors.New("--client-ca requires --tls-cert/--tls-key (mTLS needs server TLS)")
		}
		return nil, nil, nil
	}
	if cfg.tlsCert == "" || cfg.tlsKey == "" {
		return nil, nil, errors.New("--tls-cert and --tls-key must be supplied together")
	}
	reloader, err := newCertificateReloader(cfg.tlsCert, cfg.tlsKey)
	if err != nil {
		return nil, nil, err
	}
	tlsCfg := &tls.Config{GetCertificate: reloader.getCertificate, MinVersion: tls.VersionTLS12}
	if cfg.clientCA != "" {
		caPEM, err := os.ReadFile(cfg.clientCA)
		if err != nil {
			_ = reloader.Close()
			return nil, nil, fmt.Errorf("read --client-ca: %w", err)
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

// warnIfNonLoopback logs the API trust assumption for the given bind address.
// A k8s pod intentionally binds 0.0.0.0 (the endpoint controller probes it); a
// non-loopback bind WITH authentication (bearer token and/or TLS) is logged at
// info, and a non-loopback bind with NO authentication is logged as a prominent
// WARNING (it exposes UNAUTHENTICATED command/file execution to the network —
// rely on the NetworkPolicy/mesh, not a bare public port). It never hard-fails.
// Mirrors cmd/mecated's warnIfNonLoopback.
func warnIfNonLoopback(flagName, addr string, authed bool) {
	if cliconfig.IsLoopbackAddr(addr) {
		slog.Info("API bound to loopback", "flag", flagName, "addr", addr, "authenticated", authed)
		return
	}
	if authed {
		slog.Info("API bound to a non-loopback address WITH authentication (bearer token and/or TLS)", "flag", flagName, "addr", addr)
		return
	}
	slog.Warn("API bound to a NON-loopback address with NO authentication: it exposes UNAUTHENTICATED command/file execution to the network — set --auth-token / --tls-cert (or front it with a trusted mesh/NetworkPolicy) before doing this", "flag", flagName, "addr", addr)
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
