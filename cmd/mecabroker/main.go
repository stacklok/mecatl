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
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const shutdownTimeout = 10 * time.Second

type config struct {
	grpcAddress      string
	httpAddress      string
	tlsCertFile      string
	tlsKeyFile       string
	oidcIssuer       string
	oidcJWKSURI      string
	oidcAudience     string
	oidcCAFile       string
	maxJWKSStaleness time.Duration
	brokerConfigFile string
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
	flag.StringVar(&cfg.grpcAddress, "grpc-addr", "127.0.0.1:9080", "authenticated broker gRPC listen address")
	flag.StringVar(&cfg.httpAddress, "http-addr", "127.0.0.1:9081", "browser callback and ToolHive route listen address")
	flag.StringVar(&cfg.tlsCertFile, "tls-cert", "", "PEM server certificate (required with --tls-key outside loopback)")
	flag.StringVar(&cfg.tlsKeyFile, "tls-key", "", "PEM server private key")
	flag.StringVar(&cfg.oidcIssuer, "oidc-issuer", "", "exact HTTPS workload-token issuer")
	flag.StringVar(&cfg.oidcJWKSURI, "oidc-jwks-uri", "", "explicit HTTPS JWKS endpoint")
	flag.StringVar(&cfg.oidcAudience, "oidc-audience", "", "exact workload-token audience")
	flag.StringVar(&cfg.oidcCAFile, "oidc-ca", "", "PEM trust bundle for issuer and JWKS TLS")
	flag.DurationVar(&cfg.maxJWKSStaleness, "oidc-max-jwks-staleness", 15*time.Minute, "maximum cached-JWKS age during issuer outage")
	flag.StringVar(&cfg.brokerConfigFile, "config", "", "strict JSON ToolHive broker configuration")
	flag.Parse()
	return cfg
}

func run(ctx context.Context, cfg config, diagnostics port.Diagnostics) error {
	if cfg.brokerConfigFile == "" || cfg.oidcCAFile == "" {
		return errors.New("required broker configuration is absent")
	}
	if (cfg.tlsCertFile == "") != (cfg.tlsKeyFile == "") {
		return errors.New("TLS certificate and key must be configured together")
	}
	var tlsConfig *tls.Config
	if cfg.tlsCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(cfg.tlsCertFile, cfg.tlsKeyFile)
		if err != nil {
			return errors.New("load server identity")
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	}
	if err := mcpbrokerserver.ValidateTransport(cfg.grpcAddress, tlsConfig); err != nil {
		return err
	}
	if err := mcpbrokerserver.ValidateTransport(cfg.httpAddress, tlsConfig); err != nil {
		return err
	}
	caPEM, err := os.ReadFile(cfg.oidcCAFile)
	if err != nil {
		return errors.New("read OIDC trust bundle")
	}
	declaration, err := readConfig(cfg.brokerConfigFile)
	if err != nil {
		return err
	}
	callbackPath, err := exactCallbackPath(declaration.CallbackURL)
	if err != nil {
		return err
	}

	server, err := mcpbrokerserver.New(ctx, mcpbrokerserver.Config{
		OIDC:        mcpbrokerserver.OIDCConfig{Issuer: cfg.oidcIssuer, JWKSURI: cfg.oidcJWKSURI, Audience: cfg.oidcAudience, TrustedCAPEM: caPEM, MaxJWKSStaleness: cfg.maxJWKSStaleness},
		Diagnostics: diagnostics,
		Factory: func(factoryCtx context.Context) (contract.Service, mcpbroker.HandlerBundle, string, func() error, error) {
			process, processErr := mcpbroker.NewToolHiveProcess(factoryCtx, declaration.toolHive())
			if processErr != nil {
				return nil, mcpbroker.HandlerBundle{}, "", nil, processErr
			}
			return process.Runtime, process.Handlers, callbackPath, process.Close, nil
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = server.Close(closeCtx)
	}()

	grpcListener, err := net.Listen("tcp", cfg.grpcAddress)
	if err != nil {
		return errors.New("listen for broker RPC")
	}
	defer func() { _ = grpcListener.Close() }()
	grpcServer, err := server.NewGRPCServer(tlsConfig)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: cfg.httpAddress, Handler: server.HTTPHandler(), ReadHeaderTimeout: 5 * time.Second}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = httpServer.Shutdown(closeCtx)
	}()
	errCh := make(chan error, 2)
	go func() { errCh <- grpcServer.Serve(grpcListener) }()
	go func() {
		if tlsConfig == nil {
			errCh <- httpServer.ListenAndServe()
			return
		}
		httpServer.TLSConfig = tlsConfig.Clone()
		errCh <- httpServer.ListenAndServeTLS("", "")
	}()

	select {
	case <-ctx.Done():
		closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = httpServer.Shutdown(closeCtx)
		return server.Close(closeCtx)
	case serveErr := <-errCh:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return errors.New("broker listener stopped")
	}
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
	return cfg, nil
}

func exactCallbackPath(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.EscapedPath() != parsed.Path {
		return "", errors.New("broker callback must be an exact HTTPS URL")
	}
	if parsed.Path == "" {
		return "/", nil
	}
	return parsed.Path, nil
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
