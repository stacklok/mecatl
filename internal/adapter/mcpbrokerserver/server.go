// Package mcpbrokerserver owns the authenticated network boundary around one
// process-local MCP broker. The browser-facing ToolHive routes are deliberately
// separate from workload authentication: callback authority is held by the
// broker's opaque state, never by request fields.
package mcpbrokerserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	oidcadapter "github.com/stacklok/mecatl/authn/oidc"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const maxJWKSStaleness = 24 * time.Hour

// OIDCConfig is the production workload-identity policy. There is intentionally
// no insecure HTTP, private-address, or TLS-verification escape hatch.
type OIDCConfig struct {
	Issuer           string
	JWKSURI          string
	Audience         string
	AllowedSubjects  []string
	TrustedCAPEM     []byte
	MaxJWKSStaleness time.Duration
}

// Factory constructs the ToolHive-owned broker only after workload identity
// configuration and initial JWKS trust have been admitted.
type Factory func(context.Context) (contract.Service, mcpbroker.HandlerBundle, string, func() error, error)

// Config binds one broker service, its complete ToolHive route bundle, and its
// workload identity policy into one lifecycle. Factory is the production path;
// direct Service fields exist for adapter tests and are mutually exclusive with it.
type Config struct {
	Factory          Factory
	Service          contract.Service
	Handlers         mcpbroker.HandlerBundle
	CallbackPath     string
	OIDC             OIDCConfig
	Diagnostics      port.Diagnostics
	Observe          func(operation, outcome string)
	CloseProcess     func() error
	Transport        mcpbrokergrpc.Config
	ReadinessTimeout time.Duration
	ReadinessChecks  []ReadinessCheck
}

type tokenValidator interface {
	Validate(context.Context, string) (*session.Principal, error)
	Close() error
}

type readyTokenValidator interface {
	Ready(context.Context) error
}

type readyBrokerService interface {
	Ready(context.Context) error
}

// Server owns the validator, RPC adapter, mounted browser routes, and optional
// ToolHive process closer supplied by cmd/mecabroker.
type Server struct {
	validator       tokenValidator
	rpc             *mcpbrokergrpc.Server
	mux             *http.ServeMux
	diagnostics     port.Diagnostics
	observe         func(string, string)
	closeProcess    func() error
	coordinator     *Coordinator
	allowedSubjects map[string]struct{}

	mu         sync.Mutex
	grpcServer *grpc.Server
	closeOnce  sync.Once
	closeErr   error
}

// ExecuteDeadline returns the configured broker execution bound for listener validation.
func (s *Server) ExecuteDeadline() time.Duration { return s.rpc.ExecuteDeadline() }

// New validates production identity policy before constructing any RPC state.
//
//nolint:gocyclo // one ordered transaction validates identity before construction and rolls resources back in reverse order.
func New(ctx context.Context, cfg Config) (*Server, error) {
	if cfg.Factory != nil && (cfg.Service != nil || !cfg.Handlers.Empty() || cfg.CallbackPath != "" || cfg.CloseProcess != nil) {
		return nil, errors.New("mcpbrokerserver: factory and preconstructed broker resources are mutually exclusive")
	}
	if cfg.Factory == nil && cfg.Service == nil {
		return nil, errors.New("mcpbrokerserver: broker service is required")
	}
	validator, err := newOIDCValidator(ctx, cfg.OIDC)
	if err != nil {
		return nil, err
	}
	if cfg.Factory != nil {
		cfg.Service, cfg.Handlers, cfg.CallbackPath, cfg.CloseProcess, err = cfg.Factory(ctx)
		if err != nil {
			_ = validator.Close()
			return nil, fmt.Errorf("mcpbrokerserver: construct broker process: %w", err)
		}
		if cfg.Service == nil {
			_ = validator.Close()
			if cfg.CloseProcess != nil {
				_ = cfg.CloseProcess()
			}
			return nil, errors.New("mcpbrokerserver: broker factory returned no service")
		}
	}
	transport := cfg.Transport
	if transport == (mcpbrokergrpc.Config{}) {
		transport = mcpbrokergrpc.DefaultConfig()
	}
	rpc, err := mcpbrokergrpc.NewServerWithConfig(cfg.Service, transport)
	if err != nil {
		_ = validator.Close()
		if cfg.CloseProcess != nil {
			_ = cfg.CloseProcess()
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
	checks := append([]ReadinessCheck(nil), cfg.ReadinessChecks...)
	if readyValidator, ok := validator.(readyTokenValidator); ok {
		checks = append(checks, readyValidator.Ready)
	}
	if readyService, ok := cfg.Service.(readyBrokerService); ok {
		checks = append(checks, readyService.Ready)
	}
	coordinator, err := NewCoordinator(readyTimeout, checks...)
	if err != nil {
		_ = rpc.Shutdown(context.Background())
		_ = validator.Close()
		if cfg.CloseProcess != nil {
			_ = cfg.CloseProcess()
		}
		return nil, err
	}
	allowedSubjects := make(map[string]struct{}, len(cfg.OIDC.AllowedSubjects))
	for _, subject := range cfg.OIDC.AllowedSubjects {
		allowedSubjects[subject] = struct{}{}
	}
	s := &Server{validator: validator, rpc: rpc, mux: http.NewServeMux(), diagnostics: diagnostics, observe: cfg.Observe, closeProcess: cfg.CloseProcess, coordinator: coordinator, allowedSubjects: allowedSubjects}
	if !cfg.Handlers.Empty() {
		if err := cfg.Handlers.Mount(s.mux, cfg.CallbackPath); err != nil {
			_ = rpc.Shutdown(context.Background())
			_ = validator.Close()
			if cfg.CloseProcess != nil {
				_ = cfg.CloseProcess()
			}
			return nil, fmt.Errorf("mcpbrokerserver: mount ToolHive routes: %w", err)
		}
	}
	coordinator.Open()
	return s, nil
}

func newOIDCValidator(ctx context.Context, cfg OIDCConfig) (tokenValidator, error) {
	if err := mcpbroker.ValidateProtectedURL(cfg.Issuer, "OIDC issuer"); err != nil {
		return nil, fmt.Errorf("mcpbrokerserver: %w", err)
	}
	if cfg.JWKSURI == "" {
		return nil, errors.New("mcpbrokerserver: an explicit HTTPS JWKS URI is required")
	}
	if err := mcpbroker.ValidateProtectedURL(cfg.JWKSURI, "JWKS URI"); err != nil {
		return nil, fmt.Errorf("mcpbrokerserver: %w", err)
	}
	if cfg.Audience == "" {
		return nil, errors.New("mcpbrokerserver: OIDC audience is required")
	}
	if len(cfg.AllowedSubjects) == 0 {
		return nil, errors.New("mcpbrokerserver: at least one OIDC workload subject is required")
	}
	for _, subject := range cfg.AllowedSubjects {
		if subject == "" || strings.ContainsRune(subject, '\x00') {
			return nil, errors.New("mcpbrokerserver: OIDC workload subjects must be non-empty")
		}
	}
	if len(cfg.TrustedCAPEM) == 0 {
		return nil, errors.New("mcpbrokerserver: an explicit OIDC trust bundle is required")
	}
	if cfg.MaxJWKSStaleness <= 0 || cfg.MaxJWKSStaleness > maxJWKSStaleness {
		return nil, fmt.Errorf("mcpbrokerserver: JWKS staleness must be in (0,%s]", maxJWKSStaleness)
	}
	validator, err := oidcadapter.NewValidator(ctx, oidcadapter.Config{
		Issuer: cfg.Issuer, JWKSURI: cfg.JWKSURI, Audience: cfg.Audience,
		MaxJWKSStaleness:        cfg.MaxJWKSStaleness,
		AllowPrivateHTTPSIssuer: true,
		TrustedCAFile:           "operator-supplied-ca.pem",
		TrustedCAPEM:            append([]byte(nil), cfg.TrustedCAPEM...),
	})
	if err != nil {
		return nil, fmt.Errorf("mcpbrokerserver: initialize workload identity: %w", err)
	}
	return validator, nil
}

// NewGRPCServer creates the sole authenticated RPC server for this lifecycle.
func (s *Server) NewGRPCServer(tlsConfig *tls.Config) (*grpc.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grpcServer != nil {
		return nil, errors.New("mcpbrokerserver: gRPC server already created")
	}
	options := []grpc.ServerOption{grpc.ChainUnaryInterceptor(s.coordinator.UnaryInterceptor, s.authenticate)}
	if tlsConfig != nil {
		if err := ValidateTransport("network", tlsConfig); err != nil {
			return nil, err
		}
		secure := tlsConfig.Clone()
		if secure.MinVersion < tls.VersionTLS12 {
			secure.MinVersion = tls.VersionTLS12
		}
		options = append(options, grpc.Creds(credentials.NewTLS(secure)))
	}
	server := grpc.NewServer(options...)
	mcpbrokergrpc.RegisterServer(server, s.rpc)
	s.grpcServer = server
	return server, nil
}

// HTTPHandler returns the complete fixed ToolHive route bundle behind the same
// admission gate as gRPC. These browser endpoints are not workload-authenticated;
// their authority comes from opaque, single-use state created by the broker process.
func (s *Server) HTTPHandler() http.Handler { return s.coordinator.HTTP(s.mux) }

// Ready reports bounded serving readiness. It is a deployment signal only, not
// an ownership or stale-worker fence.
func (s *Server) Ready(ctx context.Context) bool { return s.coordinator.Ready(ctx) }

// BeginDrain atomically rejects new gRPC and browser callback work.
func (s *Server) BeginDrain() { s.coordinator.BeginDrain() }

// Drain waits for the configured endpoint-propagation interval and admitted
// work, cancelling remaining operations when ctx reaches its finite deadline.
func (s *Server) Drain(ctx context.Context, propagation time.Duration) error {
	return s.coordinator.Drain(ctx, propagation)
}

// ValidateTransport rejects plaintext on any non-loopback TCP bind. A non-nil
// TLS configuration must actually contain a server certificate source.
func ValidateTransport(address string, tlsConfig *tls.Config) error {
	if tlsConfig != nil {
		if len(tlsConfig.Certificates) == 0 && tlsConfig.GetCertificate == nil {
			return errors.New("mcpbrokerserver: TLS server certificate is required")
		}
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("mcpbrokerserver: invalid listen address")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("mcpbrokerserver: plaintext transport is restricted to loopback")
	}
	return nil
}

func (s *Server) authenticate(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	operation := operationName(info.FullMethod)
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 {
		s.record(ctx, operation, "unauthenticated")
		return nil, status.Error(codes.Unauthenticated, "workload authentication required")
	}
	bearer, ok := parseBearer(values[0])
	if !ok {
		s.record(ctx, operation, "unauthenticated")
		return nil, status.Error(codes.Unauthenticated, "invalid workload credential")
	}
	principal, err := s.validator.Validate(ctx, bearer)
	if err != nil {
		if errors.Is(err, oidcadapter.ErrIdentityUnavailable) || errors.Is(err, context.DeadlineExceeded) {
			s.record(ctx, operation, "unavailable")
			return nil, status.Error(codes.Unavailable, "workload identity unavailable")
		}
		s.record(ctx, operation, "unauthenticated")
		return nil, status.Error(codes.Unauthenticated, "invalid workload credential")
	}
	if _, allowed := s.allowedSubjects[principal.Subject]; !allowed {
		s.record(ctx, operation, "unauthorized")
		return nil, status.Error(codes.PermissionDenied, "workload is not authorized")
	}
	s.record(ctx, operation, "allowed")
	return handler(session.WithPrincipal(ctx, principal), req)
}

func parseBearer(value string) (string, bool) {
	scheme, bearer, found := strings.Cut(value, " ")
	return bearer, found && strings.EqualFold(scheme, "Bearer") && bearer != "" && !strings.ContainsAny(bearer, " \t\r\n")
}

func operationName(method string) string {
	const prefix = "/mecatl.broker.v1.BrokerService/"
	name := strings.TrimPrefix(method, prefix)
	switch name {
	case "Attach":
		return "attach"
	case "Commit":
		return "commit"
	case "Abort":
		return "abort"
	case "Close":
		return "close"
	case "Delete":
		return "delete"
	case "Execute":
		return "execute"
	case "RequestAuthorization":
		return "request_authorization"
	case "AbortAuthorization":
		return "abort_authorization"
	case "PresentAuthorization":
		return "present_authorization"
	case "AuthorizationStatus":
		return "authorization_status"
	case "CancelAuthorization":
		return "cancel_authorization"
	case "BeginWorkspaceEnrollment":
		return "begin_workspace_enrollment"
	case "ObserveWorkspaceEnrollment":
		return "observe_workspace_enrollment"
	case "CancelWorkspaceEnrollment":
		return "cancel_workspace_enrollment"
	default:
		return "unknown"
	}
}

func (s *Server) record(ctx context.Context, operation, outcome string) {
	s.diagnostics.Log(ctx, port.LevelInfo, "broker authentication", "operation", operation, "outcome", outcome)
	if s.observe != nil {
		s.observe(operation, outcome)
	}
}

// Close stops admission, joins the RPC adapter, closes identity refresh, and
// finally releases the process-owned ToolHive resources.
func (s *Server) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.coordinator.BeginDrain()
		s.mu.Lock()
		grpcServer := s.grpcServer
		s.mu.Unlock()
		if grpcServer != nil {
			done := make(chan struct{})
			go func() { grpcServer.GracefulStop(); close(done) }()
			select {
			case <-done:
			case <-ctx.Done():
				grpcServer.Stop()
				<-done
				s.closeErr = errors.Join(s.closeErr, ctx.Err())
			}
		}
		s.closeErr = errors.Join(s.closeErr, s.rpc.Shutdown(ctx), s.validator.Close())
		if s.closeProcess != nil {
			s.closeErr = errors.Join(s.closeErr, s.closeProcess())
		}
	})
	return s.closeErr
}
