package mcpbrokergrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/stacklok/mecatl/internal/adapter/tlsreload"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// RemoteFactoryConfig is the production remote-broker connection contract.
// The CA and workload token are reread for every factory generation/RPC so
// rotation and pre-prompt replacement never reuse stale credentials.
type RemoteFactoryConfig struct {
	// Target is the remote broker address. Empty disables construction by returning nil.
	Target string
	// CAFile names the operator-managed PEM bundle used to authenticate the broker server.
	CAFile string
	// ServerName is the TLS name checked against the broker certificate.
	ServerName string
	// TokenFile names the projected workload token reread for each RPC.
	TokenFile string
	// Transport supplies RPC deadlines and retention/capacity bounds; zero selects defaults.
	Transport Config
}

// NewRemoteFactory returns the composition-owned remote broker factory used by
// command roots and by pre-prompt state-loss recovery.
func NewRemoteFactory(cfg RemoteFactoryConfig) func(context.Context) (contract.Service, func() error, error) {
	if cfg.Target == "" {
		return nil
	}
	return func(ctx context.Context) (contract.Service, func() error, error) {
		// #nosec G703 -- these are trusted operator-configured credential paths.
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read MCP broker CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, nil, errors.New("MCP broker CA contains no certificates")
		}
		transport := cfg.Transport
		if transport == (Config{}) {
			transport = DefaultConfig()
		}
		client, conn, err := Dial(ctx, cfg.Target, transport,
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: cfg.ServerName})),
			grpc.WithPerRPCCredentials(ProjectedTokenCredentials{Path: cfg.TokenFile}),
		)
		if err != nil {
			return nil, nil, err
		}
		return client, conn.Close, nil
	}
}

// ProjectedTokenCredentials rereads one bounded projected workload token for
// every RPC and requires transport security.
type ProjectedTokenCredentials struct{ Path string }

// GetRequestMetadata returns a fresh bearer read from the projected token file.
func (c ProjectedTokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	body, err := tlsreload.ReadCredentialFile(c.Path)
	if err != nil {
		return nil, errors.New("read projected MCP broker credential")
	}
	token := strings.TrimSpace(string(body))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("invalid projected MCP broker credential")
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}

// RequireTransportSecurity prevents projected credentials from crossing plaintext transport.
func (ProjectedTokenCredentials) RequireTransportSecurity() bool { return true }
