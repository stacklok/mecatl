package app

import (
	"context"
	"os"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/envscrub"
	"github.com/stacklok/mecatl/internal/adapter/gitenv"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func foldOperatorCommandRunnerEnvironment(cfg Config) (Config, error) {
	resolver, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || resolver == nil {
		return cfg, nil
	}
	section, err := resolver.OperatorCommandRunner()
	if err != nil {
		return cfg, err
	}
	if section == nil {
		return cfg, nil
	}
	cfg.commandEnvironmentInherit = append([]string(nil), section.Environment.Inherit...)
	cfg.commandEnvironmentReserved = make(map[string]struct{}, len(envscrub.NonOverridableExact))
	for name := range envscrub.NonOverridableExact {
		cfg.commandEnvironmentReserved[name] = struct{}{}
	}
	for _, name := range resolver.OperatorCredentialEnvironmentNames() {
		cfg.commandEnvironmentReserved[name] = struct{}{}
	}
	for _, server := range cfg.MCPServers {
		cfg.commandEnvironmentReserved["MCP_"+strings.ToUpper(server.Name)+"_TOKEN"] = struct{}{}
	}
	present := make(map[string]struct{})
	for _, entry := range os.Environ() {
		for _, name := range cfg.commandEnvironmentInherit {
			if len(entry) > len(name) && entry[:len(name)+1] == name+"=" {
				present[name] = struct{}{}
			}
		}
	}
	for _, name := range cfg.commandEnvironmentInherit {
		if _, reserved := cfg.commandEnvironmentReserved[name]; reserved {
			continue
		}
		if _, exists := present[name]; !exists {
			cfg.diag().Log(context.Background(), port.LevelWarn, "command runner inherited environment name is absent", "name", name)
		}
	}
	return cfg, nil
}

func mainCommandEnvironment(cfg Config) []string {
	return envscrub.ScrubWithInherited(os.Environ(), cfg.commandEnvironmentInherit, cfg.commandEnvironmentReserved)
}

func gitSafeEnvironment() []string {
	return gitenv.Scrub(envscrub.Scrub(os.Environ()))
}

func directWriteCommandRunner(cfg Config) tool.CommandRunner {
	return buildCommandRunner(cfg)
}
