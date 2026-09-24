package mcpbrokerserver

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
)

const defaultShutdownTimeout = 5 * time.Second

// ProductionConfig is the complete process-local broker assembly.
type ProductionConfig struct {
	// PublicAddress is the TCP endpoint for authenticated broker RPC and browser callbacks.
	// A non-loopback address requires TLS.
	PublicAddress string
	// AdminAddress is the required loopback-only TCP endpoint for health, readiness, and drain control.
	AdminAddress string
	// TLSConfig supplies the public listener's server identity. It is cloned before use; nil
	// permits plaintext only on a loopback PublicAddress.
	TLSConfig *tls.Config
	// WorkloadJWT configures authentication for every broker gRPC request.
	WorkloadJWT WorkloadJWTConfig
	// ToolHive configures the process-owned upstream runtime, which Lifecycle closes during shutdown.
	ToolHive mcpbroker.ToolHiveConfig
	// ToolHiveOptions are copied and passed to the process-owned upstream runtime at construction.
	ToolHiveOptions []mcpbroker.Option
	// Diagnostics receives broker operational events; nil selects no-op diagnostics.
	Diagnostics port.Diagnostics
	// PropagationWait delays draining admitted work after admission closes, allowing endpoint
	// removal to propagate. Zero drains immediately; negative values are rejected.
	PropagationWait time.Duration
	// DrainTimeout bounds waiting for already admitted work after propagation; expiry cancels
	// that work. It must be positive.
	DrainTimeout time.Duration
	// ShutdownTimeout bounds listener, RPC, verifier, and runtime shutdown after draining.
	// Zero or a negative value selects the five-second default.
	ShutdownTimeout time.Duration
	// PublicBounds sets public-listener HTTP limits. An entirely zero value selects
	// DefaultPublicListenerConfig; a partially specified value must be valid.
	PublicBounds PublicListenerConfig
	// Transport configures the broker RPC adapter. An entirely zero value selects its defaults.
	// A positive RuntimeLimits.LogicalRetention overrides its OwnerRetention so ownership state
	// outlives each retained logical session.
	Transport mcpbrokergrpc.Config
	// RuntimeLimits bound process-owned logical sessions and pending authorization state. Their
	// zero values select mcpbroker defaults; positive LogicalRetention also sets Transport owner retention.
	RuntimeLimits mcpbroker.Limits
}

// NewProduction constructs the only production broker assembly.
func NewProduction(ctx context.Context, cfg ProductionConfig) (*Lifecycle, error) {
	if cfg.PropagationWait < 0 || cfg.DrainTimeout <= 0 {
		return nil, errors.New("mcpbrokerserver: broker drain bounds are invalid")
	}
	if err := validateAdminAddress(cfg.AdminAddress); err != nil {
		return nil, err
	}
	if err := validateTransport(cfg.PublicAddress, cfg.TLSConfig); err != nil {
		return nil, err
	}
	transport := cfg.Transport
	if transport == (mcpbrokergrpc.Config{}) {
		transport = mcpbrokergrpc.DefaultConfig()
	}
	if cfg.RuntimeLimits.LogicalRetention > 0 {
		transport.OwnerRetention = cfg.RuntimeLimits.LogicalRetention
	}
	broker, err := newBrokerHost(ctx, hostConfig{WorkloadJWT: cfg.WorkloadJWT, Diagnostics: cfg.Diagnostics, Transport: transport, Runtime: func(factoryCtx context.Context) (brokerRuntime, error) {
		options := append([]mcpbroker.Option(nil), cfg.ToolHiveOptions...)
		options = append(options, mcpbroker.WithLimits(cfg.RuntimeLimits))
		toolHiveConfig := cfg.ToolHive
		toolHiveConfig.Diagnostics = cfg.Diagnostics
		process, processErr := mcpbroker.NewToolHiveProcess(factoryCtx, toolHiveConfig, options...)
		if processErr != nil {
			return brokerRuntime{}, processErr
		}
		return brokerRuntime{Service: process, Handlers: process.Handlers, CallbackPath: process.CallbackPath, Close: process.Close}, nil
	}})
	if err != nil {
		return nil, err
	}
	cleanupBroker := true
	defer func() {
		if cleanupBroker {
			_ = broker.close(context.Background())
		}
	}()
	listener, err := net.Listen("tcp", cfg.PublicAddress)
	if err != nil {
		return nil, errors.New("mcpbrokerserver: listen for broker public traffic")
	}
	cleanupPublic := true
	defer func() {
		if cleanupPublic {
			_ = listener.Close()
		}
	}()
	bounds := cfg.PublicBounds
	if bounds == (PublicListenerConfig{}) {
		bounds = DefaultPublicListenerConfig()
	}
	public, err := newPublicListener(listener, broker, cfg.TLSConfig, bounds)
	if err != nil {
		return nil, err
	}
	adminListener, err := net.Listen("tcp", cfg.AdminAddress)
	if err != nil {
		return nil, errors.New("mcpbrokerserver: listen for broker administration")
	}
	admin := newAdminServer(adminListener, bounds.MaxHeaderBytes)
	lifecycle := &Lifecycle{broker: broker, public: public, admin: admin, adminListener: adminListener, propagation: cfg.PropagationWait, drainTimeout: cfg.DrainTimeout, shutdownTimeout: cfg.ShutdownTimeout, propagated: make(chan struct{})}
	if lifecycle.shutdownTimeout <= 0 {
		lifecycle.shutdownTimeout = defaultShutdownTimeout
	}
	admin.Handler = lifecycle.adminHandler()
	cleanupBroker, cleanupPublic = false, false
	return lifecycle, nil
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
