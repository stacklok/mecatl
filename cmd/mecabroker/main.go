// Command mecabroker runs the process-local ToolHive MCP broker behind an
// authenticated gRPC boundary. Browser OAuth routes share the same lifecycle
// but remain unauthenticated; opaque broker-created state is their authority.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

const (
	shutdownTimeout        = 5 * time.Second
	defaultPropagationWait = 2 * time.Second
	defaultDrainTimeout    = 55 * time.Second
	defaultPublicAddress   = ":8443"
	defaultAdminAddress    = "127.0.0.1:8081"
)

type brokerLifecycle interface {
	Start() <-chan error
	Close(context.Context) error
}

var newProduction = func(ctx context.Context, cfg mcpbrokerserver.ProductionConfig) (brokerLifecycle, error) {
	return mcpbrokerserver.NewProduction(ctx, cfg)
}

type config struct {
	publicAddress    string
	adminAddress     string
	tlsCertFile      string
	tlsKeyFile       string
	oidcIssuer       string
	oidcJWKSURI      string
	oidcAudience     string
	oidcSubject      string
	oidcCAFile       string
	maxJWKSStaleness time.Duration
	brokerConfigFile string
	propagationWait  time.Duration
	drainTimeout     time.Duration
	transport        mcpbrokergrpc.Config
	runtimeLimits    mcpbroker.Limits
}

type fileConfig struct {
	CallbackURL string        `json:"callback_url"`
	Profiles    []fileProfile `json:"profiles"`
}

type fileProfile struct {
	Name   string       `json:"name"`
	URL    string       `json:"url"`
	Auth   string       `json:"auth"`
	OAuth  *fileOAuth   `json:"oauth,omitempty"`
	Static []fileStatic `json:"tools,omitempty"`
}

type fileOAuth struct {
	Issuer                string   `json:"issuer,omitempty"`
	AuthorizationEndpoint string   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string   `json:"token_endpoint,omitempty"`
	ClientID              string   `json:"client_id"`
	ClientSecretEnv       string   `json:"client_secret_env"`
	Scopes                []string `json:"scopes"`
	RequestRefreshToken   bool     `json:"request_refresh_token,omitempty"`
}

type fileStatic struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	ReadOnly    bool            `json:"read_only,omitempty"`
}

func main() {
	if len(os.Args) == 2 {
		var err error
		switch os.Args[1] {
		case "health":
			err = requestLocalAdmin(http.MethodGet, "/healthz", shutdownTimeout)
		case "ready":
			err = requestLocalAdmin(http.MethodGet, "/readyz", shutdownTimeout)
		case "drain":
			err = requestLocalAdmin(http.MethodGet, "/drain", defaultPropagationWait+shutdownTimeout)
		default:
		}
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "mecabroker: local administration failed")
			os.Exit(1)
		}
		if os.Args[1] == "health" || os.Args[1] == "ready" || os.Args[1] == "drain" {
			return
		}
	}
	cfg := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, slogdiag.NewText(os.Stderr)); err != nil {
		// Startup errors can originate in secret-bearing dependency stacks. Keep the
		// process boundary closed rather than reflecting those values to stderr.
		_, _ = fmt.Fprintln(os.Stderr, "mecabroker: startup or serving failed")
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	cfg.transport = mcpbrokergrpc.DefaultConfig()
	flag.StringVar(&cfg.publicAddress, "listen-addr", defaultPublicAddress, "TLS gRPC and browser callback listen address")
	flag.StringVar(&cfg.adminAddress, "admin-addr", defaultAdminAddress, "loopback-only health, readiness, and drain listen address")
	flag.StringVar(&cfg.tlsCertFile, "tls-cert", "", "PEM public listener server certificate (required)")
	flag.StringVar(&cfg.tlsKeyFile, "tls-key", "", "PEM server private key")
	flag.StringVar(&cfg.oidcIssuer, "oidc-issuer", "", "exact HTTPS workload-token issuer")
	flag.StringVar(&cfg.oidcJWKSURI, "oidc-jwks-uri", "", "explicit HTTPS JWKS endpoint")
	flag.StringVar(&cfg.oidcAudience, "oidc-audience", "", "exact workload-token audience")
	flag.StringVar(&cfg.oidcSubject, "oidc-subject", "", "exact authorized workload-token subject")
	flag.StringVar(&cfg.oidcCAFile, "oidc-ca", "", "PEM trust bundle for issuer and JWKS TLS")
	flag.DurationVar(&cfg.maxJWKSStaleness, "oidc-max-jwks-staleness", 15*time.Minute, "maximum cached-JWKS age during issuer outage")
	flag.StringVar(&cfg.brokerConfigFile, "config", "", "strict JSON ToolHive broker configuration")
	flag.DurationVar(&cfg.propagationWait, "drain-propagation-delay", defaultPropagationWait, "delay after closing admission before waiting for active work")
	flag.DurationVar(&cfg.drainTimeout, "drain-timeout", defaultDrainTimeout, "finite deadline for active broker work during shutdown")
	flag.DurationVar(&cfg.transport.DialTimeout, "broker-dial-timeout", cfg.transport.DialTimeout, "finite broker client connection deadline")
	flag.DurationVar(&cfg.transport.RPCDeadline, "broker-rpc-deadline", cfg.transport.RPCDeadline, "finite non-Execute broker RPC deadline")
	flag.DurationVar(&cfg.transport.ExecuteDeadline, "broker-execute-deadline", cfg.transport.ExecuteDeadline, "finite broker Execute deadline")
	flag.DurationVar(&cfg.transport.HandleIdleTimeout, "broker-handle-idle-timeout", cfg.transport.HandleIdleTimeout, "absolute idle lease for broker attachment handles")
	flag.DurationVar(&cfg.transport.SweepInterval, "broker-sweep-interval", cfg.transport.SweepInterval, "broker retention sweep interval")
	flag.DurationVar(&cfg.transport.CleanupTimeout, "broker-cleanup-timeout", cfg.transport.CleanupTimeout, "bounded broker attachment cleanup deadline")
	flag.IntVar(&cfg.transport.MaxHandles, "broker-max-handles", cfg.transport.MaxHandles, "maximum retained broker attachment handles")
	flag.IntVar(&cfg.transport.MaxOwners, "broker-max-owners", cfg.transport.MaxOwners, "maximum retained authenticated logical-session owners")
	flag.IntVar(&cfg.runtimeLimits.MaxLogicalSessions, "broker-max-logical-sessions", 1024, "maximum broker runtime logical sessions")
	flag.DurationVar(&cfg.runtimeLimits.LogicalRetention, "broker-logical-retention", 24*time.Hour, "idle retention for broker runtime logical sessions")
	flag.IntVar(&cfg.runtimeLimits.MaxPendingStates, "broker-max-pending-auth-states", 1024, "maximum pending broker runtime authorization states")
	flag.IntVar(&cfg.transport.MaxReceipts, "broker-max-receipts", cfg.transport.MaxReceipts, "maximum retained Execute receipts per attachment")
	flag.IntVar(&cfg.transport.MaxReceiptBytes, "broker-max-receipt-bytes", cfg.transport.MaxReceiptBytes, "maximum aggregate retained Execute receipt bytes per attachment")
	flag.IntVar(&cfg.transport.MaxPendingControls, "broker-max-pending-controls", cfg.transport.MaxPendingControls, "maximum concurrent broker lifecycle controls")
	flag.IntVar(&cfg.transport.MaxActiveExecutes, "broker-max-active-executes", cfg.transport.MaxActiveExecutes, "maximum concurrent upstream tool executions")
	flag.Parse()
	return cfg
}

//nolint:gocyclo // composition root keeps file/flag validation separate from the shared lifecycle.
func run(ctx context.Context, cfg config, diagnostics port.Diagnostics) error {
	if cfg.brokerConfigFile == "" || cfg.oidcCAFile == "" {
		return errors.New("required broker configuration is absent")
	}
	if cfg.propagationWait < 0 || cfg.drainTimeout <= 0 {
		return errors.New("broker drain bounds are invalid")
	}
	if cfg.runtimeLimits.MaxLogicalSessions <= 0 || cfg.runtimeLimits.LogicalRetention <= 0 || cfg.runtimeLimits.MaxPendingStates <= 0 {
		return errors.New("broker runtime limits are invalid")
	}
	if (cfg.tlsCertFile == "") != (cfg.tlsKeyFile == "") {
		return errors.New("TLS certificate and key must be configured together")
	}
	if cfg.tlsCertFile == "" {
		return errors.New("broker public TLS certificate and key are required")
	}
	certificate, err := tls.LoadX509KeyPair(cfg.tlsCertFile, cfg.tlsKeyFile)
	if err != nil {
		return errors.New("load server identity")
	}
	caPEM, err := os.ReadFile(cfg.oidcCAFile)
	if err != nil {
		return errors.New("read OIDC trust bundle")
	}
	declaration, err := readConfig(cfg.brokerConfigFile)
	if err != nil {
		return err
	}
	bounds := mcpbrokerserver.DefaultPublicListenerConfig()
	lifecycle, err := newProduction(ctx, mcpbrokerserver.ProductionConfig{
		PublicAddress: cfg.publicAddress, AdminAddress: cfg.adminAddress,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
		OIDC:      mcpbrokerserver.OIDCConfig{Issuer: cfg.oidcIssuer, JWKSURI: cfg.oidcJWKSURI, Audience: cfg.oidcAudience, AllowedSubjects: []string{cfg.oidcSubject}, TrustedCAPEM: caPEM, MaxJWKSStaleness: cfg.maxJWKSStaleness},
		ToolHive:  declaration.toolHive(), Diagnostics: diagnostics, PropagationWait: cfg.propagationWait,
		DrainTimeout: cfg.drainTimeout, ShutdownTimeout: shutdownTimeout, PublicBounds: bounds, Transport: cfg.transport,
		RuntimeLimits: cfg.runtimeLimits,
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = lifecycle.Close(closeCtx)
	}()
	errs := lifecycle.Start()
	select {
	case <-ctx.Done():
		return lifecycle.Close(context.Background())
	case <-errs:
		return errors.New("broker listener stopped")
	}
}

func requestLocalAdmin(method, path string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(method, "http://"+defaultAdminAddress+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("local drain rejected")
	}
	return nil
}

func readConfig(path string) (fileConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return fileConfig{}, errors.New("open broker configuration")
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var cfg fileConfig
	if err := decoder.Decode(&cfg); err != nil {
		return fileConfig{}, errors.New("decode broker configuration")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fileConfig{}, errors.New("broker configuration has trailing data")
	}
	if cfg.CallbackURL == "" || len(cfg.Profiles) == 0 {
		return fileConfig{}, errors.New("broker callback and profiles are required")
	}
	if err := mcpbroker.ValidateProtectedURL(cfg.CallbackURL, "broker callback"); err != nil {
		return fileConfig{}, err
	}
	for _, profile := range cfg.Profiles {
		if strings.EqualFold(profile.Auth, "oauth") {
			if err := mcpbroker.ValidateProtectedURL(profile.URL, "protected upstream URL"); err != nil {
				return fileConfig{}, err
			}
			if profile.OAuth == nil {
				return fileConfig{}, errors.New("protected profile OAuth configuration is required")
			}
			for label, endpoint := range map[string]string{"issuer": profile.OAuth.Issuer, "authorization endpoint": profile.OAuth.AuthorizationEndpoint, "token endpoint": profile.OAuth.TokenEndpoint} {
				if endpoint != "" {
					if err := mcpbroker.ValidateProtectedURL(endpoint, label); err != nil {
						return fileConfig{}, err
					}
				}
			}
		}
	}
	return cfg, nil
}

func (cfg fileConfig) toolHive() mcpbroker.ToolHiveConfig {
	profiles := make([]mcpbroker.ToolHiveProfile, len(cfg.Profiles))
	for i, profile := range cfg.Profiles {
		converted := mcpbroker.ToolHiveProfile{Name: profile.Name, URL: profile.URL, Auth: profile.Auth}
		if profile.OAuth != nil {
			converted.OAuth = &mcpbroker.ToolHiveOAuth{Issuer: profile.OAuth.Issuer, AuthorizationEndpoint: profile.OAuth.AuthorizationEndpoint, TokenEndpoint: profile.OAuth.TokenEndpoint, ClientID: profile.OAuth.ClientID, ClientSecretEnv: profile.OAuth.ClientSecretEnv, Scopes: append([]string(nil), profile.OAuth.Scopes...), RequestRefreshToken: profile.OAuth.RequestRefreshToken}
		}
		converted.Static = make([]mcpbroker.StaticTool, len(profile.Static))
		for j, spec := range profile.Static {
			converted.Static[j] = mcpbroker.StaticTool{Name: spec.Name, Description: spec.Description, Schema: append(json.RawMessage(nil), spec.Schema...), ReadOnly: spec.ReadOnly}
		}
		profiles[i] = converted
	}
	return mcpbroker.ToolHiveConfig{CallbackURL: cfg.CallbackURL, Profiles: profiles}
}
