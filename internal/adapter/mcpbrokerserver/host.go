package mcpbrokerserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type brokerRuntime struct {
	Service      contract.Service
	Handlers     mcpbroker.HandlerBundle
	CallbackPath string
	Close        func() error
	SessionAPI   contract.SessionService
}

type buildBrokerRuntime func(context.Context) (brokerRuntime, error)

type hostConfig struct {
	Runtime          buildBrokerRuntime
	Service          contract.Service
	Handlers         mcpbroker.HandlerBundle
	CallbackPath     string
	WorkloadJWT      WorkloadJWTConfig
	Verifier         workloadVerifier // package-local focused-test seam
	Diagnostics      port.Diagnostics
	Observe          func(operation, outcome string)
	CloseProcess     func() error
	SessionAPI       contract.SessionService
	ReadinessTimeout time.Duration
	ReadinessChecks  []readinessCheck
}

type readyBrokerService interface{ Ready(context.Context) error }

// brokerHost owns the verifier, RPC adapter, mounted callback routes, and the
// process-owned runtime closer.
type brokerHost struct {
	service         contract.Service
	verifier        workloadVerifier
	sessionRPC      *mcpbrokergrpc.SessionRPC
	mux             *http.ServeMux
	diagnostics     port.Diagnostics
	observe         func(string, string)
	closeProcess    func() error
	admission       *admissionGate
	allowedSubjects map[string]struct{}

	mu         sync.Mutex
	grpcServer *grpc.Server
	closeOnce  sync.Once
	closeErr   error
}

//nolint:gocyclo // Construction is one ordered transaction with reverse-order rollback.
func newBrokerHost(ctx context.Context, cfg hostConfig) (*brokerHost, error) {
	if cfg.Runtime != nil && (cfg.Service != nil || !cfg.Handlers.Empty() || cfg.CallbackPath != "" || cfg.CloseProcess != nil || cfg.SessionAPI != nil) {
		return nil, errors.New("mcpbrokerserver: factory and preconstructed broker resources are mutually exclusive")
	}
	if cfg.Runtime == nil && cfg.SessionAPI == nil {
		return nil, errors.New("mcpbrokerserver: session service is required")
	}
	var verifier workloadVerifier
	var err error
	if cfg.Verifier != nil {
		verifier = cfg.Verifier
	} else {
		verifier, err = newWorkloadJWTVerifier(ctx, cfg.WorkloadJWT)
		if err != nil {
			return nil, err
		}
	}
	runtime := brokerRuntime{Service: cfg.Service, SessionAPI: cfg.SessionAPI, Handlers: cfg.Handlers, CallbackPath: cfg.CallbackPath, Close: cfg.CloseProcess}
	if cfg.Runtime != nil {
		runtime, err = cfg.Runtime(ctx)
		if err != nil {
			_ = verifier.Close()
			return nil, fmt.Errorf("mcpbrokerserver: construct broker process: %w", err)
		}
	}
	rpc, err := mcpbrokergrpc.NewSessionRPC(runtime.SessionAPI)
	if err != nil {
		_ = verifier.Close()
		if runtime.Close != nil {
			_ = runtime.Close()
		}
		return nil, fmt.Errorf("mcpbrokerserver: construct RPC service: %w", err)
	}
	diagnostics := cfg.Diagnostics
	if diagnostics == nil {
		diagnostics = port.NopDiagnostics{}
	}
	readyTimeout := cfg.ReadinessTimeout
	if readyTimeout == 0 {
		readyTimeout = 2 * time.Second
	}
	checks := append([]readinessCheck(nil), cfg.ReadinessChecks...)
	checks = append(checks, verifier.Ready)
	if readyService, ok := runtime.Service.(readyBrokerService); ok {
		checks = append(checks, readyService.Ready)
	}
	if readyService, ok := runtime.SessionAPI.(readyBrokerService); ok {
		checks = append(checks, readyService.Ready)
	}
	admission, err := newAdmissionGate(readyTimeout, checks...)
	if err != nil {
		_ = verifier.Close()
		if runtime.Close != nil {
			_ = runtime.Close()
		}
		return nil, err
	}
	allowedSubjects := make(map[string]struct{}, len(cfg.WorkloadJWT.AllowedSubjects))
	for _, subject := range cfg.WorkloadJWT.AllowedSubjects {
		allowedSubjects[subject] = struct{}{}
	}
	h := &brokerHost{service: runtime.Service, verifier: verifier, sessionRPC: rpc, mux: http.NewServeMux(), diagnostics: diagnostics, observe: cfg.Observe, closeProcess: runtime.Close, admission: admission, allowedSubjects: allowedSubjects}
	if !runtime.Handlers.Empty() {
		if err := runtime.Handlers.Mount(h.mux, runtime.CallbackPath); err != nil {
			_ = verifier.Close()
			if runtime.Close != nil {
				_ = runtime.Close()
			}
			return nil, fmt.Errorf("mcpbrokerserver: mount ToolHive routes: %w", err)
		}
	}
	admission.Open()
	return h, nil
}

func (h *brokerHost) newGRPCServer(tlsConfig *tls.Config) (*grpc.Server, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.grpcServer != nil {
		return nil, errors.New("mcpbrokerserver: gRPC server already created")
	}
	options := []grpc.ServerOption{grpc.ChainUnaryInterceptor(h.authenticate, h.admission.UnaryInterceptor, h.traceRPC)}
	if tlsConfig != nil {
		if err := validateTransport("network", tlsConfig); err != nil {
			return nil, err
		}
		secure := tlsConfig.Clone()
		if secure.MinVersion < tls.VersionTLS12 {
			secure.MinVersion = tls.VersionTLS12
		}
		options = append(options, grpc.Creds(credentials.NewTLS(secure)))
	}
	server := grpc.NewServer(options...)
	brokerv1.RegisterSessionServiceServer(server, h.sessionRPC)
	h.grpcServer = server
	return server, nil
}
func (h *brokerHost) httpHandler() http.Handler { return h.admission.HTTP(h.mux) }

// traceRPC logs only verified workload identities and protocol identifiers; it
// deliberately never projects request bodies, authorization state, or errors.
func (h *brokerHost) traceRPC(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	response, err := handler(ctx, req)
	outcome := "success"
	if err != nil {
		outcome = status.Code(err).String()
	}
	fields := []any{"operation", operationName(info.FullMethod), "outcome", outcome}
	if principal := verifiedSessionWorkload(ctx); principal != nil {
		fields = append(fields, "principal", boundedDiagnosticPrincipal(principal.Subject))
	}
	h.diagnostics.Log(ctx, port.LevelDebug, "broker RPC", fields...)
	return response, err
}

func boundedDiagnosticPrincipal(subject string) string {
	const limit = 256
	if len(subject) > limit || !utf8.ValidString(subject) {
		return "[redacted]"
	}
	for _, r := range subject {
		if r < 0x20 || r == 0x7f {
			return "[redacted]"
		}
	}
	return subject
}

func (h *brokerHost) ready(ctx context.Context) bool { return h.admission.Ready(ctx) }
func (h *brokerHost) beginDrain()                    { h.admission.BeginDrain() }
func (h *brokerHost) drain(ctx context.Context, propagation time.Duration) error {
	return h.admission.Drain(ctx, propagation)
}
func (h *brokerHost) close(ctx context.Context) error {
	h.closeOnce.Do(func() {
		h.admission.BeginDrain()
		h.mu.Lock()
		grpcServer := h.grpcServer
		h.mu.Unlock()
		if grpcServer != nil {
			done := make(chan struct{})
			go func() { grpcServer.GracefulStop(); close(done) }()
			select {
			case <-done:
			case <-ctx.Done():
				grpcServer.Stop()
				<-done
				h.closeErr = errors.Join(h.closeErr, ctx.Err())
			}
		}
		h.closeErr = errors.Join(h.closeErr, h.verifier.Close())
		if h.closeProcess != nil {
			h.closeErr = errors.Join(h.closeErr, h.closeProcess())
		}
	})
	return h.closeErr
}
