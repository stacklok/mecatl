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
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

const (
	shutdownTimeout        = 5 * time.Second
	defaultPropagationWait = 2 * time.Second
	defaultDrainTimeout    = 55 * time.Second
	maxCallbackBodyBytes   = 64 << 10
	maxPublicHeaderBytes   = 32 << 10
	publicReadTimeout      = 2 * time.Minute
	publicWriteTimeout     = 2 * time.Minute
	publicIdleTimeout      = 60 * time.Second
)

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
	flag.StringVar(&cfg.publicAddress, "listen-addr", "127.0.0.1:9080", "TLS gRPC and browser callback listen address")
	flag.StringVar(&cfg.adminAddress, "admin-addr", "127.0.0.1:9082", "loopback-only health, readiness, and drain listen address")
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
	bounds.MaxCallbackBytes = maxCallbackBodyBytes
	bounds.MaxHeaderBytes = maxPublicHeaderBytes
	bounds.ReadTimeout = publicReadTimeout
	bounds.WriteTimeout = publicWriteTimeout
	bounds.IdleTimeout = publicIdleTimeout
	lifecycle, err := mcpbrokerserver.NewProduction(ctx, mcpbrokerserver.ProductionConfig{
		PublicAddress: cfg.publicAddress, AdminAddress: cfg.adminAddress,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
		OIDC:      mcpbrokerserver.OIDCConfig{Issuer: cfg.oidcIssuer, JWKSURI: cfg.oidcJWKSURI, Audience: cfg.oidcAudience, AllowedSubjects: []string{cfg.oidcSubject}, TrustedCAPEM: caPEM, MaxJWKSStaleness: cfg.maxJWKSStaleness},
		ToolHive:  declaration.toolHive(), Diagnostics: diagnostics, PropagationWait: cfg.propagationWait,
		DrainTimeout: cfg.drainTimeout, ShutdownTimeout: shutdownTimeout, PublicBounds: bounds,
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
	case serveErr := <-errs:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		_ = lifecycle.Close(context.Background())
		return errors.New("broker listener stopped")
	}
}

func validateAdminAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("broker admin listen address is invalid")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("broker admin listener must bind to loopback")
	}
	return nil
}

func publicHandler(grpcHandler, callbackHandler http.Handler) http.Handler {
	cfg := mcpbrokerserver.DefaultPublicListenerConfig()
	cfg.MaxCallbackBytes = maxCallbackBodyBytes
	return mcpbrokerserver.PublicHandler(grpcHandler, callbackHandler, cfg)
}

func adminHandler(ready func(context.Context) bool, beginDrain func(), propagated <-chan struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready(r.Context()) {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /drain", func(w http.ResponseWriter, r *http.Request) {
		beginDrain()
		select {
		case <-propagated:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			http.Error(w, "drain propagation incomplete", http.StatusServiceUnavailable)
		}
	})
	return mux
}

func requestLocalAdmin(method, path string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(method, "http://127.0.0.1:9082"+path, nil)
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
