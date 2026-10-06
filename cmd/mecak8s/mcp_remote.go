package main

import (
	"context"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
)

func sessionBrokerFactory(cfg config) func(context.Context) (mcpbrokergrpc.SessionHostClient, func() error, error) {
	if cfg.mcpBrokerAddress == "" {
		return nil
	}
	return mcpbrokergrpc.NewSessionRemoteFactory(mcpbrokergrpc.RemoteFactoryConfig{
		Target: cfg.mcpBrokerAddress, CAFile: cfg.mcpBrokerTLSCAFile,
		ServerName: cfg.mcpBrokerServerName, TokenFile: cfg.mcpBrokerTokenFile,
	})
}
