package mcpbrokergrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const maxProjectedCredentialBytes = 1 << 20

// RemoteFactoryConfig is the production remote-broker connection contract.
// The CA and workload token are reread for every factory generation/RPC so
// rotation and pre-prompt replacement never reuse stale credentials.
type RemoteFactoryConfig struct {
	Target     string
	CAFile     string
	ServerName string
	TokenFile  string
	Transport  Config
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
	// #nosec G703 -- this is a trusted operator-configured projected-token path.
	file, err := os.Open(c.Path)
	if err != nil {
		return nil, errors.New("read projected MCP broker credential")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxProjectedCredentialBytes {
		return nil, errors.New("invalid projected MCP broker credential")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxProjectedCredentialBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxProjectedCredentialBytes {
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
