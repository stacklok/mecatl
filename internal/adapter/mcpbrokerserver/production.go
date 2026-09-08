package mcpbrokerserver

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const defaultShutdownTimeout = 5 * time.Second

// ProductionConfig is the complete process-local broker assembly. It owns the
// ToolHive factory, authenticated RPC boundary, public listener, readiness
// coordinator, and ordered shutdown lifecycle.
type ProductionConfig struct {
	PublicAddress   string
	AdminAddress    string
	TLSConfig       *tls.Config
	OIDC            OIDCConfig
	ToolHive        mcpbroker.ToolHiveConfig
	ToolHiveOptions []mcpbroker.Option
	Diagnostics     port.Diagnostics

	PropagationWait time.Duration
	DrainTimeout    time.Duration
	ShutdownTimeout time.Duration
	PublicBounds    PublicListenerConfig
	Transport       mcpbrokergrpc.Config
	RuntimeLimits   mcpbroker.Limits
}

// Lifecycle is the production broker process lifecycle. Start serves the public
// TLS/HTTP2 and loopback administration listeners; Close performs the ordered
// admission, propagation, drain, listener, validator, and ToolHive shutdown.
type Lifecycle struct {
	broker          *Server
	public          *PublicListener
	admin           *http.Server
	adminListener   net.Listener
	propagation     time.Duration
	drainTimeout    time.Duration
	shutdownTimeout time.Duration

	admissionOnce sync.Once
	propagated    chan struct{}
	closeOnce     sync.Once
	closeErr      error
}

// NewProduction constructs the only production broker assembly. Tests may use
// local TLS endpoints, but use this constructor rather than assembling a gRPC
// server, interceptor, or ToolHive process themselves.
func NewProduction(ctx context.Context, cfg ProductionConfig) (*Lifecycle, error) {
	if cfg.PropagationWait < 0 || cfg.DrainTimeout <= 0 {
		return nil, errors.New("mcpbrokerserver: broker drain bounds are invalid")
	}
	if err := validateAdminAddress(cfg.AdminAddress); err != nil {
		return nil, err
	}
	if err := ValidateTransport(cfg.PublicAddress, cfg.TLSConfig); err != nil {
		return nil, err
	}
	transport := cfg.Transport
	if transport == (mcpbrokergrpc.Config{}) {
		transport = mcpbrokergrpc.DefaultConfig()
	}
	if cfg.RuntimeLimits.LogicalRetention > 0 {
		transport.OwnerRetention = cfg.RuntimeLimits.LogicalRetention
	}
	cfg.Transport = transport
	callbackPath, err := callbackPath(cfg.ToolHive.CallbackURL)
	if err != nil {
		return nil, err
	}
	broker, err := New(ctx, Config{
		OIDC: cfg.OIDC, Diagnostics: cfg.Diagnostics, Transport: cfg.Transport,
		Factory: func(factoryCtx context.Context) (contract.Service, mcpbroker.HandlerBundle, string, func() error, error) {
			options := append([]mcpbroker.Option(nil), cfg.ToolHiveOptions...)
			options = append(options, mcpbroker.WithLimits(cfg.RuntimeLimits))
			process, processErr := mcpbroker.NewToolHiveProcess(factoryCtx, cfg.ToolHive, options...)
			if processErr != nil {
				return nil, mcpbroker.HandlerBundle{}, "", nil, processErr
			}
			return process.Runtime, process.Handlers, callbackPath, process.Close, nil
		},
	})
	if err != nil {
		return nil, err
	}
	cleanupBroker := true
	defer func() {
		if cleanupBroker {
			_ = broker.Close(context.Background())
		}
	}()
	publicListener, err := net.Listen("tcp", cfg.PublicAddress)
	if err != nil {
		return nil, errors.New("mcpbrokerserver: listen for broker public traffic")
	}
	cleanupPublic := true
	defer func() {
		if cleanupPublic {
			_ = publicListener.Close()
		}
	}()
	bounds := cfg.PublicBounds
	if bounds == (PublicListenerConfig{}) {
		bounds = DefaultPublicListenerConfig()
	}
	public, err := NewPublicListener(publicListener, broker, cfg.TLSConfig, bounds)
	if err != nil {
		return nil, err
	}
	adminListener, err := net.Listen("tcp", cfg.AdminAddress)
	if err != nil {
		return nil, errors.New("mcpbrokerserver: listen for broker administration")
	}
	admin := &http.Server{Addr: adminListener.Addr().String(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: bounds.MaxHeaderBytes}
	lifecycle := &Lifecycle{broker: broker, public: public, admin: admin, adminListener: adminListener, propagation: cfg.PropagationWait, drainTimeout: cfg.DrainTimeout, shutdownTimeout: cfg.ShutdownTimeout, propagated: make(chan struct{})}
	if lifecycle.shutdownTimeout <= 0 {
		lifecycle.shutdownTimeout = defaultShutdownTimeout
	}
	admin.Handler = lifecycle.adminHandler()
	cleanupBroker, cleanupPublic = false, false
	return lifecycle, nil
}

// PublicAddress returns the actual public listener address, including a port
// selected by the kernel when ProductionConfig.PublicAddress ends in :0.
func (l *Lifecycle) PublicAddress() string { return l.public.listener.Addr().String() }

// AdminAddress returns the actual loopback administration listener address.
func (l *Lifecycle) AdminAddress() string { return l.adminListener.Addr().String() }

// Ready reports production readiness through the shared coordinator.
func (l *Lifecycle) Ready(ctx context.Context) bool { return l.broker.Ready(ctx) }

// Start begins both owned listeners and returns their terminal errors.
func (l *Lifecycle) Start() <-chan error {
	errs := make(chan error, 2)
	go func() { errs <- <-l.public.Serve() }()
	go func() { errs <- l.admin.Serve(l.adminListener) }()
	return errs
}

// BeginDrain closes admission and starts the bounded endpoint-propagation wait.
func (l *Lifecycle) BeginDrain() {
	l.admissionOnce.Do(func() {
		l.broker.BeginDrain()
		go func() {
			timer := time.NewTimer(l.propagation)
			defer timer.Stop()
			<-timer.C
			close(l.propagated)
		}()
	})
}

// Close follows the production shutdown ordering: admission closure,
// propagation, active-work drain, public/admin listener shutdown, then broker
// validator and ToolHive process closure.
func (l *Lifecycle) Close(ctx context.Context) error {
	l.closeOnce.Do(func() {
		l.BeginDrain()
		select {
		case <-l.propagated:
		case <-ctx.Done():
			l.closeErr = errors.Join(l.closeErr, ctx.Err())
		}
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), l.drainTimeout)
		l.closeErr = errors.Join(l.closeErr, l.broker.Drain(drainCtx, 0))
		cancelDrain()
		stopCtx, cancelStop := context.WithTimeout(context.Background(), l.shutdownTimeout)
		l.closeErr = errors.Join(l.closeErr, l.public.Shutdown(stopCtx), l.admin.Shutdown(stopCtx), l.broker.Close(stopCtx))
		cancelStop()
	})
	return l.closeErr
}

func (l *Lifecycle) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !l.Ready(r.Context()) {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /drain", func(w http.ResponseWriter, r *http.Request) {
		l.BeginDrain()
		select {
		case <-l.propagated:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			http.Error(w, "drain propagation incomplete", http.StatusServiceUnavailable)
		}
	})
	return mux
}

func validateAdminAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("mcpbrokerserver: broker admin listen address is invalid")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("mcpbrokerserver: broker admin listener must bind to loopback")
	}
	return nil
}

func callbackPath(raw string) (string, error) {
	if err := mcpbroker.ValidateProtectedURL(raw, "broker callback"); err != nil {
		return "", err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("mcpbrokerserver: broker callback must be an exact HTTPS URL")
	}
	if parsed.Path == "" {
		return "/", nil
	}
	return parsed.Path, nil
}
