package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/microvmcmd"
)

type microVMManager interface {
	microvmcmd.Manager
	EnsureReady(context.Context, microvmmanager.ReadyRequest) (string, error)
}

type microVMReadyManager interface {
	EnsureReady(context.Context, microvmmanager.ReadyRequest) (string, error)
}

func defaultMicroVMManager() (microVMManager, error) {
	manager, _, err := microvmmanager.DefaultLocal()
	return manager, err
}

func defaultMicroVMEndpoint() (string, error) {
	_, endpoint, err := microvmmanager.DefaultLocal()
	return endpoint, err
}

func runMicroVMCommand(ctx context.Context, args []string, in io.Reader, out io.Writer, manager microVMManager, interactive bool) error {
	return microvmcmd.Run(ctx, microvmcmd.FrontendMecatui, args, in, out, manager, interactive)
}

func validateMicroVMReleaseStamp() error {
	if microVMReleaseStampRequired == "" {
		return nil
	}
	_, err := microVMReadyRequest()
	return err
}

func microVMReadyRequestWithDevelopment(descriptor string, acknowledge bool, egress ...microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
	request, enabled, err := microVMDevelopmentReadyRequest(descriptor, acknowledge, version, microVMReleaseStampRequired != "" || microVMReleaseDefaultsB64 != "", egress...)
	if enabled || err != nil {
		return request, err
	}
	return microVMReadyRequest(egress...)
}

func microVMReadyRequest(egress ...microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
	return microvmmanager.ReadyRequestFromDefaults(microVMReleaseDefaultsB64, version, egress...)
}

type microVMPrewarmKey struct{}

type readinessHandoff struct {
	mu       sync.Mutex
	ready    func(context.Context) error
	skipNext bool
}

func (h *readinessHandoff) call(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	prewarm, _ := ctx.Value(microVMPrewarmKey{}).(bool)
	if !prewarm && h.skipNext {
		h.skipNext = false
		return nil
	}
	if err := h.ready(ctx); err != nil {
		return err
	}
	if prewarm {
		h.skipNext = true
	}
	return nil
}

func ensureSelectedEnvironmentReady(ctx context.Context, cfg config, out io.Writer) error {
	if cfg.transportMode != modeLocal || cfg.defaultPlacement != microvmmanager.Alias || cfg.microVMReadiness == nil {
		return nil
	}
	ctx = context.WithValue(ctx, microVMPrewarmKey{}, true)
	if cfg.microVMDevRelease != "" {
		_, _ = fmt.Fprintln(out, "mecatui: microvm-local: unsupported development release enabled; trusting acknowledged local artifacts")
	}
	ctx = microvmmanager.WithReadinessObserver(ctx, func(_ microvmmanager.ReadinessStage, message string) {
		_, _ = fmt.Fprintln(out, "mecatui: microvm-local:", message)
	})
	return cfg.microVMReadiness(ctx)
}

func startupResumeAndReadiness(ctx context.Context, source startupResumeSource, cfg config, out io.Writer) (*client.ResumeSelection, string, error) {
	resume, workspace, err := startupResumeConfig(ctx, source, cfg)
	if err != nil {
		return nil, "", err
	}
	if err := ensureSelectedEnvironmentReady(ctx, cfg, out); err != nil {
		return nil, "", err
	}
	return resume, workspace, nil
}

func configureSelectedEnvironmentReadiness(cfg *config, endpoint string, factory func() (microVMReadyManager, error)) error {
	if cfg.transportMode != modeLocal || cfg.defaultPlacement != microvmmanager.Alias {
		return nil
	}
	if endpoint == "" {
		return errors.New("microvm-local endpoint is empty")
	}
	cfg.microVMEndpoint = endpoint
	ready := &readinessHandoff{ready: func(ctx context.Context) error {
		manager, err := factory()
		if err != nil {
			return err
		}
		var egress []microvmmanager.GuestEgressSelection
		if cfg.microVMEgressSet {
			egress = append(egress, cfg.microVMGuestEgress)
		}
		request, err := microVMReadyRequestWithDevelopment(cfg.microVMDevRelease, cfg.microVMDevAcknowledge, egress...)
		if err != nil {
			return err
		}
		readyEndpoint, err := manager.EnsureReady(ctx, request)
		if err != nil {
			return err
		}
		if readyEndpoint != endpoint {
			return fmt.Errorf("microvm-local readiness returned unexpected endpoint %q", readyEndpoint)
		}
		return nil
	}}
	cfg.microVMReadiness = ready.call
	provider, err := microvmadapter.NewPlacementProvider(endpoint, cfg.workspace, microvmmanager.Alias, server.PlacementScope("deployment"), cfg.microVMReadiness)
	if err != nil {
		return err
	}
	cfg.microVMProvider = provider
	return nil
}
