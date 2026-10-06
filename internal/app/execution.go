package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	// PlacementHostLocal selects execution in the host-local environment.
	PlacementHostLocal = "host-local"
	// PlacementMicroVMLocal selects execution in a local MicroVM environment.
	PlacementMicroVMLocal = "microvm-local"
)

// MicroVMReadyManager is the narrow manager capability needed by composition.
type MicroVMReadyManager interface {
	EnsureReady(context.Context, microvmmanager.ReadyRequest) (string, error)
}

type resolvedExecution struct {
	placement string
	egress    microvmmanager.GuestEgressSelection
}

// ConfigureExecution applies the resolved execution policy and wires any selected
// backend before Build resolves harness-context registrations.
func ConfigureExecution(cfg Config) (Config, error) {
	if cfg.executionConfigured {
		return cfg, nil
	}
	if cfg.permResolver == nil {
		cfg.permResolver = buildPermResolver(cfg)
	}
	configured, err := foldExecution(cfg)
	if err != nil {
		return cfg, err
	}
	configured.executionConfigured = true
	return configured, nil
}

func foldExecution(cfg Config) (Config, error) {
	resolved := resolvedExecution{
		placement: PlacementHostLocal,
		egress:    microvmmanager.NewGuestEgressSelection(),
	}
	settingsEgress := false
	if resolver, ok := cfg.permResolver.(*permconfig.Resolver); ok {
		section, err := resolver.OperatorExecution()
		if err != nil {
			return cfg, fmt.Errorf("operator execution configuration: %w", err)
		}
		if section != nil {
			resolved.placement = section.DefaultPlacement
			if section.MicroVM != nil && section.MicroVM.GuestEgress != nil {
				settingsEgress = true
				resolved.egress, err = microvmmanager.ParseGuestEgressSelection(
					section.MicroVM.GuestEgress.Mode,
					section.MicroVM.GuestEgress.Allow,
				)
				if err != nil {
					return cfg, fmt.Errorf("execution.microvm.guest_egress.allow: %w", err)
				}
			}
		}
	}
	if cfg.DefaultPlacementSet {
		resolved.placement = cfg.DefaultPlacement
	}
	if cfg.MicroVMGuestEgressSet {
		resolved.egress = cfg.MicroVMGuestEgress
	}
	if resolved.placement != PlacementHostLocal && resolved.placement != PlacementMicroVMLocal {
		return cfg, fmt.Errorf("unsupported execution placement %q", resolved.placement)
	}
	if err := resolved.egress.Validate(); err != nil {
		if settingsEgress && !cfg.MicroVMGuestEgressSet {
			return cfg, fmt.Errorf("execution.microvm.guest_egress: %w", err)
		}
		return cfg, err
	}
	if resolved.placement == PlacementHostLocal {
		if cfg.MicroVMGuestEgressSet {
			return cfg, errors.New("microVM guest egress flags require microvm-local placement")
		}
		return cfg, nil
	}
	return configureMicroVMExecution(cfg, resolved.egress)
}

func configureMicroVMExecution(cfg Config, egress microvmmanager.GuestEgressSelection) (Config, error) {
	if cfg.MicroVMReadyRequest == nil {
		return cfg, errors.New("microvm-local placement requires release readiness configuration")
	}
	factory := cfg.MicroVMManagerFactory
	if factory == nil {
		factory = func() (MicroVMReadyManager, string, error) {
			manager, endpoint, err := microvmmanager.DefaultLocal()
			return manager, endpoint, err
		}
	}
	manager, endpoint, err := factory()
	if err != nil {
		return cfg, err
	}
	if manager == nil || endpoint == "" {
		return cfg, errors.New("microvm-local readiness manager and endpoint are required")
	}
	if cfg.MicroVMReadinessObserver != nil && egress.Mode == microvmmanager.GuestEgressPermissive {
		cfg.MicroVMReadinessObserver(microvmmanager.StagePrepare, "Guest IPv4 egress is permissive by default; set execution.microvm.guest_egress.mode to deny-all or allowlist before first use")
	}
	readiness := func(ctx context.Context) error {
		request, requestErr := cfg.MicroVMReadyRequest(egress)
		if requestErr != nil {
			if cfg.MicroVMReadinessFailed != nil {
				cfg.MicroVMReadinessFailed(microvmmanager.StagePrepare)
			}
			cfg.diag().Log(ctx, port.LevelWarn, "microvm-local readiness failed", "stage", string(microvmmanager.StagePrepare), "error", requestErr)
			return publicMicroVMReadinessError(requestErr, microvmmanager.StagePrepare)
		}
		lastStage := microvmmanager.StagePrepare
		ctx = microvmmanager.WithReadinessObserver(ctx, func(stage microvmmanager.ReadinessStage, message string) {
			lastStage = stage
			cfg.diag().Log(ctx, port.LevelInfo, "microvm-local readiness", "stage", string(stage), "status", message)
			if cfg.MicroVMReadinessObserver != nil {
				cfg.MicroVMReadinessObserver(stage, message)
			}
		})
		readyEndpoint, readyErr := manager.EnsureReady(ctx, request)
		if readyErr != nil {
			if cfg.MicroVMReadinessFailed != nil {
				cfg.MicroVMReadinessFailed(lastStage)
			}
			cfg.diag().Log(ctx, port.LevelWarn, "microvm-local readiness failed", "stage", string(lastStage), "error", readyErr)
			return publicMicroVMReadinessError(readyErr, lastStage)
		}
		if readyEndpoint != endpoint {
			endpointErr := fmt.Errorf("microvm-local readiness returned unexpected endpoint %q", readyEndpoint)
			if cfg.MicroVMReadinessFailed != nil {
				cfg.MicroVMReadinessFailed(lastStage)
			}
			cfg.diag().Log(ctx, port.LevelWarn, "microvm-local readiness failed", "stage", string(lastStage), "error", endpointErr)
			return publicMicroVMReadinessError(endpointErr, lastStage)
		}
		return nil
	}
	const scope server.PlacementScope = "deployment"
	provider, err := microvmadapter.NewPlacementProvider(endpoint, cfg.Workspace, microvmmanager.Alias, scope, readiness)
	if err != nil {
		return cfg, err
	}
	cfg.PlacementProvider = provider
	cfg.PlacementScope = scope
	cfg.HarnessInstructionSources = slices.Clone(cfg.HarnessInstructionSources)
	cfg.HarnessCommandSources = slices.Clone(cfg.HarnessCommandSources)
	cfg.EnvironmentForkers = maps.Clone(cfg.EnvironmentForkers)
	cfg.EnvironmentMergers = maps.Clone(cfg.EnvironmentMergers)
	registerRepositorySources(&cfg)
	if cfg.EnvironmentForkers == nil {
		cfg.EnvironmentForkers = make(map[session.EnvironmentKind]tool.EnvironmentForker)
	}
	if cfg.EnvironmentMergers == nil {
		cfg.EnvironmentMergers = make(map[session.EnvironmentKind]tool.EnvironmentMerger)
	}
	kind := session.EnvironmentKind("microvm")
	cfg.EnvironmentForkers[kind] = provider
	cfg.EnvironmentMergers[kind] = provider
	return cfg, nil
}

func registerRepositorySources(cfg *Config) {
	cfg.HarnessInstructionSources = append(cfg.HarnessInstructionSources, HarnessSourceRegistration[prompt.InstructionAssembler]{
		ID: "repository", Scope: HarnessSourceScopePrincipal,
		Provenance: HarnessProvenancePolicy{Fixed: harnessProjectTier}, UsesExecutionWorkspace: true,
		Bind: func(ctx context.Context, sourceScope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			if sourceScope.AcquireExecutionWorkspace == nil {
				return nil, nil, errors.New("repository instruction source requires execution workspace acquisition")
			}
			workspace, release, acquireErr := sourceScope.AcquireExecutionWorkspace(ctx)
			if acquireErr != nil {
				return nil, nil, acquireErr
			}
			return prompt.RootAssembler{Source: workspace}, release, nil
		},
	})
	cfg.HarnessCommandSources = append(cfg.HarnessCommandSources, HarnessSourceRegistration[server.CommandSourceBinding]{
		ID: "repository", Scope: HarnessSourceScopePrincipal,
		Provenance: HarnessProvenancePolicy{Fixed: harnessProjectTier}, UsesExecutionWorkspace: true,
		Bind: func(ctx context.Context, sourceScope HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			if sourceScope.AcquireExecutionWorkspace == nil {
				return nil, nil, errors.New("repository command source requires execution workspace acquisition")
			}
			workspace, release, acquireErr := sourceScope.AcquireExecutionWorkspace(ctx)
			if acquireErr != nil {
				return nil, nil, acquireErr
			}
			return prompt.NewDirCommandExpander(workspace), release, nil
		},
	})
}

func publicMicroVMReadinessError(err error, fallback microvmmanager.ReadinessStage) error {
	failure := microvmmanager.ClassifyReadinessFailure(err, fallback)
	return server.NewPlacementReadinessError(failure.Stage, failure.Category, failure.Cause)
}
