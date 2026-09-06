package main

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

	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	mcpbrokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const maxBrokerCredentialBytes = 1 << 20

type projectedBrokerCredentials struct{ path string }

func (c projectedBrokerCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	file, err := os.Open(c.path)
	if err != nil {
		return nil, errors.New("read projected MCP broker credential")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBrokerCredentialBytes {
		return nil, errors.New("invalid projected MCP broker credential")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxBrokerCredentialBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxBrokerCredentialBytes {
		return nil, errors.New("read projected MCP broker credential")
	}
	token := strings.TrimSpace(string(body))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("invalid projected MCP broker credential")
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}

func (projectedBrokerCredentials) RequireTransportSecurity() bool { return true }

func mcpBrokerFactory(cfg config) func(context.Context) (mcpbrokercontract.Service, func() error, error) {
	if cfg.mcpBrokerAddress == "" {
		return nil
	}
	return func(ctx context.Context) (mcpbrokercontract.Service, func() error, error) {
		// #nosec G703 -- this is an operator-configured CA path, not client input.
		pem, err := os.ReadFile(cfg.mcpBrokerTLSCAFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read MCP broker CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, nil, errors.New("MCP broker CA contains no certificates")
		}
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: cfg.mcpBrokerServerName,
		}
		client, conn, err := mcpbrokergrpc.Dial(ctx, cfg.mcpBrokerAddress, mcpbrokergrpc.DefaultConfig(),
			grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
			grpc.WithPerRPCCredentials(projectedBrokerCredentials{path: cfg.mcpBrokerTokenFile}),
		)
		if err != nil {
			return nil, nil, err
		}
		return client, conn.Close, nil
	}
}
