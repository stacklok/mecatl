package mcpbrokerserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxCallbackBodyBytes = int64(64 << 10)
	defaultMaxPublicHeaderBytes = 32 << 10
	publicListenerExecuteMargin = 5 * time.Second
)

// PublicListenerConfig contains the finite production bounds for the multiplexed
// TLS HTTP/2 gRPC and browser callback listener.
type PublicListenerConfig struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	CallbackTimeout   time.Duration
	MaxHeaderBytes    int
	MaxCallbackBytes  int64
	ExecuteDeadline   time.Duration
}

// DefaultPublicListenerConfig returns the production public-listener bounds.
func DefaultPublicListenerConfig() PublicListenerConfig {
	return PublicListenerConfig{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       2*time.Minute + publicListenerExecuteMargin,
		WriteTimeout:      2*time.Minute + publicListenerExecuteMargin,
		IdleTimeout:       time.Minute,
		CallbackTimeout:   30 * time.Second,
		MaxHeaderBytes:    defaultMaxPublicHeaderBytes,
		MaxCallbackBytes:  defaultMaxCallbackBodyBytes,
		ExecuteDeadline:   0,
	}
}

func (c PublicListenerConfig) valid() bool {
	return c.ExecuteDeadline > 0 && c.ReadHeaderTimeout > 0 && c.ReadTimeout > 0 && c.WriteTimeout > 0 && c.IdleTimeout > 0 &&
		c.CallbackTimeout > 0 && c.MaxHeaderBytes > 0 && c.MaxCallbackBytes > 0
}

func (c PublicListenerConfig) validForExecute() bool {
	return c.ReadTimeout >= c.ExecuteDeadline+publicListenerExecuteMargin && c.WriteTimeout >= c.ExecuteDeadline+publicListenerExecuteMargin
}

// PublicListener owns the exact production public HTTP/TLS server. Broker.Close
// remains a separate ordered lifecycle step after Shutdown and Drain.
type PublicListener struct {
	server   *http.Server
	listener net.Listener
	serve    sync.Once
	errCh    chan error
}

// NewPublicListener assembles the authenticated broker RPC server and mounted
// callback handler behind one bounded TLS/HTTP2 listener.
func NewPublicListener(listener net.Listener, broker *Server, tlsConfig *tls.Config, cfg PublicListenerConfig) (*PublicListener, error) {
	if listener == nil || broker == nil {
		return nil, errors.New("mcpbrokerserver: public listener and broker are required")
	}
	if cfg.ExecuteDeadline == 0 {
		cfg.ExecuteDeadline = broker.ExecuteDeadline()
	}
	if !cfg.valid() || !cfg.validForExecute() {
		return nil, errors.New("mcpbrokerserver: public listener bounds must be positive and cover ExecuteDeadline plus the required margin")
	}
	if err := ValidateTransport(listener.Addr().String(), tlsConfig); err != nil {
		return nil, err
	}
	grpcServer, err := broker.NewGRPCServer(nil)
	if err != nil {
		return nil, err
	}
	secure := tlsConfig.Clone()
	secure.NextProtos = []string{"h2", "http/1.1"}
	handler := PublicHandler(grpcServer, broker.HTTPHandler(), cfg)
	return &PublicListener{server: &http.Server{
		Addr: listener.Addr().String(), Handler: handler, TLSConfig: secure,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: cfg.MaxHeaderBytes,
	}, listener: listener, errCh: make(chan error, 1)}, nil
}

// Serve starts serving once and returns a channel containing the terminal error.
func (p *PublicListener) Serve() <-chan error {
	p.serve.Do(func() {
		go func() { p.errCh <- p.server.ServeTLS(p.listener, "", "") }()
	})
	return p.errCh
}

// Shutdown stops the HTTP listener after admission/drain handling by its owner.
func (p *PublicListener) Shutdown(ctx context.Context) error { return p.server.Shutdown(ctx) }

// PublicHandler multiplexes authenticated gRPC and bounded browser routes.
func PublicHandler(grpcHandler, callbackHandler http.Handler, cfg PublicListenerConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcHandler.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), cfg.CallbackTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		if status, message := validateBoundedPublicRoute(w, r, cfg.MaxCallbackBytes); status != 0 {
			http.Error(w, message, status)
			return
		}
		callbackHandler.ServeHTTP(w, r)
	})
}

func validateBoundedPublicRoute(w http.ResponseWriter, r *http.Request, maximum int64) (int, string) {
	if status, message := validatePublicRouteMethod(r); status != 0 {
		return status, message
	}
	if r.ContentLength > maximum {
		return http.StatusRequestEntityTooLarge, "public route body is too large"
	}
	if r.Body == nil {
		return 0, ""
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximum)
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			return http.StatusRequestTimeout, "public request deadline exceeded"
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge, "public route body is too large"
		}
		return http.StatusBadRequest, "public route body is unreadable"
	}
	if r.ContentLength >= 0 && int64(len(body)) != r.ContentLength {
		return http.StatusBadRequest, "public route body is incomplete"
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return 0, ""
}

func validatePublicRouteMethod(r *http.Request) (int, string) {
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if status, message, matched := validateFixedToolHiveRoute(r, contentType); matched {
		return status, message
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/mcp/broker/"):
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "ToolHive route method is not supported"
		}
		if r.Method == http.MethodPost && contentType != "application/json" && !strings.HasSuffix(contentType, "+json") {
			return http.StatusUnsupportedMediaType, "ToolHive route content type is not supported"
		}
	case strings.Contains(r.URL.Path, "/authorize"):
		if r.Method != http.MethodGet {
			return http.StatusMethodNotAllowed, "OAuth authorize route requires GET"
		}
	case strings.Contains(r.URL.Path, "/token"):
		if r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "OAuth token route requires POST"
		}
		if contentType != "application/x-www-form-urlencoded" {
			return http.StatusUnsupportedMediaType, "OAuth token route requires form content"
		}
	default:
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "public route method is not supported"
		}
		if r.Method == http.MethodPost && contentType != "application/x-www-form-urlencoded" && contentType != "multipart/form-data" {
			return http.StatusUnsupportedMediaType, "public route content type is not supported"
		}
	}
	return 0, ""
}

func validateFixedToolHiveRoute(r *http.Request, contentType string) (int, string, bool) {
	switch r.URL.Path {
	case "/v1/mcp/broker/oauth/authorize":
		if r.Method != http.MethodGet {
			return http.StatusMethodNotAllowed, "OAuth authorize route requires GET", true
		}
	case "/v1/mcp/broker/oauth/token":
		if r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "OAuth token route requires POST", true
		}
		if contentType != "application/x-www-form-urlencoded" {
			return http.StatusUnsupportedMediaType, "OAuth token route requires form content", true
		}
	case "/v1/mcp/broker/oauth/callback",
		"/v1/mcp/broker/.well-known/openid-configuration",
		"/v1/mcp/broker/.well-known/jwks.json",
		"/v1/mcp/broker/.well-known/oauth-protected-resource":
		if r.Method != http.MethodGet {
			return http.StatusMethodNotAllowed, "OAuth metadata and callback routes require GET", true
		}
	case "/v1/mcp/broker/mcp":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "MCP route method is not supported", true
		}
		if r.Method == http.MethodPost && contentType != "application/json" && !strings.HasSuffix(contentType, "+json") {
			return http.StatusUnsupportedMediaType, "MCP route content type is not supported", true
		}
	default:
		return 0, "", false
	}
	return 0, "", true
}
