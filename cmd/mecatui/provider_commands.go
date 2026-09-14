package main

import (
	"context"
	"io"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
)

// providerTerminal is the complete interactive input boundary for provider
// commands. Each operation must stop promptly when ctx is cancelled.
type providerTerminal struct {
	readField   func(context.Context, string) (string, error)
	readAPIKey  func(context.Context, string) (string, error)
	readRemoval func(context.Context, string) (bool, error)
}

// providerBackend groups all non-terminal external effects used by provider
// commands: configuration inspection and mutation, plus credential and lifecycle
// integration. Pure parsing and status presentation stay outside it.
type providerBackend struct {
	settingsPath      func() string
	inspect           func() (providerInspection, error)
	loadCredentials   func() (providerCredentialConfig, error)
	updateProviderMap func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error)
	updateAPIKey      func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error)
	updateDefaults    func(context.Context, string, permconfig.DefaultUpdate) (authfile.CommitState, error)
	openOIDCRuntime   func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error)
	prepareOIDCRoot   func(string) error
	toolHiveAvailable func() bool
	toolHiveLogin     func(context.Context, bool) error
	resolveDefault    func(context.Context, string, string) (string, string, error)
}

// providerCommands owns one invocation's terminal and provider backend dependencies.
// Tests customize a value instead of mutating process-wide package state.
type providerCommands struct {
	terminal                    providerTerminal
	backend                     providerBackend
	deferCredentialCancellation bool // add must establish rollback before claiming no changes
}

var executeToolHiveLogin = func(ctx context.Context, skipBrowser bool) error {
	return toolhivellm.RunInteractiveLogin(ctx, "", skipBrowser, nil)
}

func newProviderCommands() providerCommands {
	return providerCommands{
		terminal: providerTerminal{
			readField:   readProviderFieldFromTerminal,
			readAPIKey:  readHiddenProviderAPIKey,
			readRemoval: readProviderRemovalConfirmationFromTerminal,
		},
		backend: providerBackend{
			settingsPath:      defaultProviderSettingsPath,
			inspect:           inspectLocalProviders,
			loadCredentials:   currentProviderCredentialConfig,
			updateProviderMap: permconfig.UpdateProviderMap,
			updateAPIKey:      authfile.UpdateAPIKey,
			updateDefaults:    permconfig.UpdateDefaults,
			openOIDCRuntime: func(ctx context.Context, definition permconfig.ProviderDefinition, noBrowser bool, urlWriter io.Writer) (nativeEndpointRuntime, error) {
				return openNativeEndpointRuntime(ctx, definition, noBrowser, urlWriter)
			},
			prepareOIDCRoot:   credentialstore.EnsurePrivateRoot,
			toolHiveAvailable: currentToolHiveAvailable,
			toolHiveLogin:     executeToolHiveLogin,
			resolveDefault:    resolveProviderDefaultWithContext,
		},
	}
}
