package mcpbrokerserver

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	defaultMaxCallbackBodyBytes = int64(64 << 10)
	defaultMaxPublicHeaderBytes = 32 << 10
	publicListenerExecuteMargin = 5 * time.Second
)

// PublicListenerConfig contains the finite production bounds for the multiplexed TLS HTTP/2 gRPC and browser callback listener.
type PublicListenerConfig struct {
	// ReadHeaderTimeout bounds reading an HTTP request header before the connection is rejected.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds reading a complete HTTP request. It must include ExecuteDeadline plus
	// the required five-second margin.
	ReadTimeout time.Duration
	// WriteTimeout bounds writing an HTTP response. It must include ExecuteDeadline plus the
	// required five-second margin.
	WriteTimeout time.Duration
	// IdleTimeout bounds how long an inactive keep-alive connection remains open.
	IdleTimeout time.Duration
	// CallbackTimeout bounds each non-gRPC browser callback request, including body reading.
	CallbackTimeout time.Duration
	// MaxHeaderBytes limits request headers on both the public and local administration listeners.
	MaxHeaderBytes int
	// MaxCallbackBytes limits a non-gRPC callback request body in bytes, including streamed bodies.
	MaxCallbackBytes int64
	// ExecuteDeadline is the RPC execution limit used to verify ReadTimeout and WriteTimeout
	// have enough headroom. Zero derives it from the broker transport; it is not an unbounded limit.
	ExecuteDeadline time.Duration
}

// DefaultPublicListenerConfig returns the production public-listener bounds.
func DefaultPublicListenerConfig() PublicListenerConfig {
	return PublicListenerConfig{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 2*time.Minute + publicListenerExecuteMargin, WriteTimeout: 2*time.Minute + publicListenerExecuteMargin, IdleTimeout: time.Minute, CallbackTimeout: 30 * time.Second, MaxHeaderBytes: defaultMaxPublicHeaderBytes, MaxCallbackBytes: defaultMaxCallbackBodyBytes}
}
func (c PublicListenerConfig) valid() bool {
	return c.ExecuteDeadline > 0 && c.ReadHeaderTimeout > 0 && c.ReadTimeout > 0 && c.WriteTimeout > 0 && c.IdleTimeout > 0 && c.CallbackTimeout > 0 && c.MaxHeaderBytes > 0 && c.MaxCallbackBytes > 0
}
func (c PublicListenerConfig) validForExecute() bool {
	return c.ReadTimeout >= c.ExecuteDeadline+publicListenerExecuteMargin && c.WriteTimeout >= c.ExecuteDeadline+publicListenerExecuteMargin
}

type publicListener struct {
	server   *http.Server
	listener net.Listener
	serve    sync.Once
	errCh    chan error
}

func newPublicListener(listener net.Listener, host *brokerHost, tlsConfig *tls.Config, cfg PublicListenerConfig) (*publicListener, error) {
	if listener == nil || host == nil {
		return nil, errors.New("mcpbrokerserver: public listener and broker are required")
	}
	if cfg.ExecuteDeadline == 0 {
		cfg.ExecuteDeadline = host.executeDeadline()
	}
	if !cfg.valid() || !cfg.validForExecute() {
		return nil, errors.New("mcpbrokerserver: public listener bounds must be positive and cover ExecuteDeadline plus the required margin")
	}
	if err := validateTransport(listener.Addr().String(), tlsConfig); err != nil {
		return nil, err
	}
	grpcServer, err := host.newGRPCServer(nil)
	if err != nil {
		return nil, err
	}
	secure := tlsConfig.Clone()
	secure.NextProtos = []string{"h2", "http/1.1"}
	return &publicListener{server: &http.Server{Addr: listener.Addr().String(), Handler: publicHandler(grpcServer, host.httpHandler(), cfg), TLSConfig: secure, ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: cfg.MaxHeaderBytes}, listener: listener, errCh: make(chan error, 1)}, nil
}
func (p *publicListener) Serve() <-chan error {
	p.serve.Do(func() { go func() { p.errCh <- p.server.ServeTLS(p.listener, "", "") }() })
	return p.errCh
}
func (p *publicListener) Shutdown(ctx context.Context) error { return p.server.Shutdown(ctx) }
func validateTransport(address string, tlsConfig *tls.Config) error {
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
	if host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return errors.New("mcpbrokerserver: plaintext transport is restricted to loopback")
}
