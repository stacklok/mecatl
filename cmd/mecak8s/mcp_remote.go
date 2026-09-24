package main

import (
	"context"

	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	mcpbrokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func mcpBrokerFactory(cfg config) func(context.Context) (mcpbrokercontract.Service, func() error, error) {
	return mcpbrokergrpc.NewRemoteFactory(mcpbrokergrpc.RemoteFactoryConfig{
		Target: cfg.mcpBrokerAddress, CAFile: cfg.mcpBrokerTLSCAFile,
		ServerName: cfg.mcpBrokerServerName, TokenFile: cfg.mcpBrokerTokenFile,
	})
}
