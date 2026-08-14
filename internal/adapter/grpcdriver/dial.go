package grpcdriver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// This dial path MIRRORS the mecatui client's connection posture
// (cmd/mecatui/client/client.go, Dial): grpc.NewClient (lazy, never the
// deprecated grpc.Dial), loopback plaintext as the single-user default, a
// per-RPC bearer credential whose RequireTransportSecurity() is true for any
// non-loopback target, a hard pre-dial refusal of token-over-cleartext to a
// non-loopback host, and optional TLS with a custom CA. The ~60 lines are
// DUPLICATED rather than shared because an internal adapter cannot import
// cmd/; keep the two in step when touching either. The one addition here is
// mTLS (a client certificate), which a driver deployment may require.

// TLSOptions configures transport TLS for a driver connection: an optional
// custom CA bundle for server verification and an optional client
// certificate/key pair for mutual TLS.
type TLSOptions struct {
	// CAFile is an optional PEM CA bundle used to verify the driver's server
	// certificate (empty uses the system roots).
	CAFile string
	// ClientCertFile/ClientKeyFile are an optional PEM client certificate and
	// key for mutual TLS; set both or neither.
	ClientCertFile string
	ClientKeyFile  string
}

// Option customises Dial.
type Option func(*dialConfig)

type dialConfig struct {
	token  string
	useTLS bool
	tls    TLSOptions
}

// WithBearerToken attaches a per-RPC bearer credential ("authorization:
// Bearer <token>"). For a non-loopback target the credential demands
// transport security, so the token can never ride a cleartext wire to a
// remote host.
func WithBearerToken(token string) Option {
	return func(c *dialConfig) { c.token = token }
}

// WithTLS enables transport TLS per o (a zero o verifies against the system
// roots with no client certificate).
func WithTLS(o TLSOptions) Option {
	return func(c *dialConfig) {
		c.useTLS = true
		c.tls = o
	}
}

// Dial connects to a store driver at target ("host:port") per opts. The
// connection is LAZY (grpc.NewClient): the first RPC surfaces a connect
// error. Plaintext is the LOCAL single-user default (loopback hosts and unix
// sockets); ANY other target requires WithTLS — token or not (see the
// cleartext refusal in dialOptions).
func Dial(target string, opts ...Option) (*grpc.ClientConn, error) {
	var cfg dialConfig
	for _, o := range opts {
		o(&cfg)
	}
	dialOpts, err := dialOptions(target, cfg)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("grpcdriver: dial %q: %w", target, err)
	}
	return conn, nil
}

// dialOptions builds the grpc.DialOptions for target per cfg. Split from Dial
// so tests can append a bufconn dialer without opening a real socket.
func dialOptions(target string, cfg dialConfig) ([]grpc.DialOption, error) {
	// The snapshot-size contract: raise the per-call send AND receive caps to
	// the protocol's required minimum capacity — the gRPC default caps receive
	// at 4 MiB, far below a media-carrying session snapshot. The matching
	// server-side requirement is the DRIVER's (see MaxSnapshotBytes).
	opts := []grpc.DialOption{grpc.WithDefaultCallOptions(
		grpc.MaxCallRecvMsgSize(MaxSnapshotBytes),
		grpc.MaxCallSendMsgSize(MaxSnapshotBytes),
	)}

	local := isLoopbackHost(target) || isUnixTarget(target)

	// Refuse CLEARTEXT to any non-local driver ENTIRELY — token or not. A
	// driver delivers session payloads, memories, model-steering skill bodies,
	// and arbitrary skill payload bytes: an on-path attacker over a cleartext
	// remote link would gain driver-equivalent capability regardless of auth.
	// (This deliberately supersedes the earlier token-only rule for every
	// driver seam.) The bearer credential's RequireTransportSecurity() remains
	// the belt at send time; this hard pre-dial guard gives the operator a
	// clear, actionable error instead of an opaque RPC failure later.
	if !cfg.useTLS && !local {
		return nil, fmt.Errorf(
			"grpcdriver: refusing CLEARTEXT to non-loopback driver %q: drivers carry session payloads, memory, model instructions, and arbitrary skill assets — enable driver TLS (--driver-tls)", target)
	}

	if cfg.useTLS {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if cfg.tls.CAFile != "" {
			pool, err := loadCAPool(cfg.tls.CAFile)
			if err != nil {
				return nil, err
			}
			tlsCfg.RootCAs = pool
		}
		if cfg.tls.ClientCertFile != "" || cfg.tls.ClientKeyFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.tls.ClientCertFile, cfg.tls.ClientKeyFile)
			if err != nil {
				return nil, fmt.Errorf("grpcdriver: load client TLS keypair: %w", err)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	if cfg.token != "" {
		// The credential requires transport security UNLESS the target is
		// local (the documented plaintext single-user default). For any other
		// target it demands TLS even if the guard above were bypassed, so the
		// token can never ride a cleartext wire.
		opts = append(opts, grpc.WithPerRPCCredentials(bearerCreds{token: cfg.token, allowInsecure: local}))
	}
	return opts, nil
}

// loadCAPool reads a PEM CA bundle into a cert pool for server verification.
func loadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path) //nolint:gosec // operator-supplied CA path
	if err != nil {
		return nil, fmt.Errorf("grpcdriver: read TLS CA %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("grpcdriver: no certificates parsed from CA %q", path)
	}
	return pool, nil
}

// bearerCreds implements grpc.PerRPCCredentials, attaching the driver bearer
// token as the lowercase "authorization" metadata.
// RequireTransportSecurity() returns false ONLY for a loopback target (the
// documented plaintext single-user default); for any non-loopback target it
// returns true, so grpc-go refuses to send the token over a cleartext wire.
type bearerCreds struct {
	token         string
	allowInsecure bool // true only for loopback targets
}

// GetRequestMetadata returns the bearer authorization metadata.
func (b bearerCreds) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}

// RequireTransportSecurity reports whether the credential demands TLS (any
// non-loopback target).
func (b bearerCreds) RequireTransportSecurity() bool { return !b.allowInsecure }

// isUnixTarget reports whether target names a unix-domain socket (the grpc
// "unix:"/"unix-abstract:" target schemes) — a same-host transport that, like
// loopback, may legitimately ride without TLS.
func isUnixTarget(target string) bool {
	return strings.HasPrefix(target, "unix:") || strings.HasPrefix(target, "unix-abstract:")
}

// isLoopbackHost reports whether the host part of a "host:port" (or bare
// host) target is loopback: an IP in 127.0.0.0/8, ::1, or the name
// "localhost". A target with no resolvable/parseable host is treated as
// NON-loopback (fail safe — we'd rather demand TLS than dial cleartext).
func isLoopbackHost(server string) bool {
	host := server
	if h, _, err := net.SplitHostPort(server); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
