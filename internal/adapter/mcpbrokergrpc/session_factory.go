package mcpbrokergrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// SessionHostClient is an adapter client helper, not a public service contract.
type SessionHostClient interface {
	c.SessionService
	ResumeToolWrapper(c.SessionRef, c.Catalogue, string, session.ToolCallID, c.AuthorizationRef, c.BrokerAttempt) (tool.Tool, error)
}

// NewSessionRemoteFactory shares the TLS/projected-token contract and never
// falls back to the retired attachment protocol.
func NewSessionRemoteFactory(cfg RemoteFactoryConfig) func(context.Context) (SessionHostClient, func() error, error) {
	if cfg.Target == "" {
		return nil
	}
	return func(ctx context.Context) (SessionHostClient, func() error, error) {
		if cfg.CAFile == "" || cfg.ServerName == "" || cfg.TokenFile == "" {
			return nil, nil, errors.New("complete broker TLS/address/workload configuration is required")
		}
		// #nosec G703 -- trusted operator-managed CA path.
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, nil, errors.New("read broker-session CA")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, nil, errors.New("broker-session CA contains no certificates")
		}
		transport := cfg.Transport
		if transport == (Config{}) {
			transport = DefaultConfig()
		}
		if transport.DialTimeout <= 0 {
			return nil, nil, errors.New("broker connection deadline must be positive")
		}
		client, err := NewSessionClient(cfg.Target, transport.RPCDeadline, transport.ExecuteDeadline,
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: cfg.ServerName})),
			grpc.WithPerRPCCredentials(ProjectedTokenCredentials{Path: cfg.TokenFile}),
		)
		if err != nil {
			return nil, nil, err
		}
		// Invalid references prove authenticated canonical RPC admission without
		// opening a session, probing upstreams, or changing broker lifecycle state.
		probeCtx, cancel := context.WithTimeout(ctx, transport.DialTimeout)
		defer cancel()
		_, err = client.rpc.InspectConnectors(probeCtx, &p.InspectConnectorsRequest{}, grpc.MaxRetryRPCBufferSize(0))
		if status.Code(err) != codes.InvalidArgument {
			_ = client.Close()
			return nil, nil, errors.New("broker SessionService readiness verification failed")
		}
		return client, client.Close, nil
	}
}
