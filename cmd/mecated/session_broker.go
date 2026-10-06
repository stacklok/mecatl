package main

import (
	"context"
	"errors"
	"flag"

	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
)

type sessionBrokerFlags struct {
	enabled bool
	remote  mcpbrokergrpc.RemoteFactoryConfig
}

func registerSessionBrokerFlags(fs *flag.FlagSet, cfg *sessionBrokerFlags) {
	fs.StringVar(&cfg.remote.Target, "mcp-broker-address", "", "Remote broker host:port; requires verified TLS and projected workload JWT")
	fs.StringVar(&cfg.remote.CAFile, "mcp-broker-tls-ca", "", "Remote broker PEM CA bundle")
	fs.StringVar(&cfg.remote.ServerName, "mcp-broker-server-name", "", "Expected remote broker TLS DNS name")
	fs.StringVar(&cfg.remote.TokenFile, "mcp-broker-token-file", "", "Projected workload JWT file reread for every RPC")
}
func (cfg *sessionBrokerFlags) validate() error {
	count := 0
	for _, v := range []string{cfg.remote.Target, cfg.remote.CAFile, cfg.remote.ServerName, cfg.remote.TokenFile} {
		if v != "" {
			count++
		}
	}
	if count != 0 && count != 4 {
		return errors.New("--mcp-broker-address, --mcp-broker-tls-ca, --mcp-broker-server-name, and --mcp-broker-token-file must be configured together")
	}
	cfg.enabled = count == 4
	return nil
}
func (cfg sessionBrokerFlags) factory() func(context.Context) (mcpbrokergrpc.SessionHostClient, func() error, error) {
	if !cfg.enabled {
		return nil
	}
	return mcpbrokergrpc.NewSessionRemoteFactory(cfg.remote)
}
func (cfg sessionBrokerFlags) authorityDefault() mcpauthority.Mode {
	if cfg.enabled {
		return mcpauthority.Broker
	}
	return mcpauthority.Global
}
