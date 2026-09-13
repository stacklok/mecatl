package main

import (
	"context"
	"errors"
	"io"
	"slices"

	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

type configuredNativeLLMHost struct {
	definitions permconfig.ProviderDefinitions
	runtimes    map[string]nativeEndpointRuntime
	noBrowser   bool
	urlWriter   io.Writer
}

type nativeEndpointRuntime interface {
	Login(context.Context) error
	Status(context.Context) llmendpoint.Status
	Logout(context.Context) error
	Close() error
}

var openNativeEndpointRuntime = func(_ context.Context, definition permconfig.ProviderDefinition, noBrowser bool, urlWriter io.Writer) (nativeEndpointRuntime, error) {
	present := func(ctx context.Context, authorizationURL string) (oauthlogin.Result, error) {
		runtime, err := oauthlogin.New(nativeLLMOAuthOptions(noBrowser, urlWriter))
		if err != nil {
			return oauthlogin.Result{}, err
		}
		var result oauthlogin.Result
		err = runtime.Authorize(ctx, definition.Auth.OIDC.Issuer, func(ctx context.Context, _ string, present func(context.Context, string) (oauthlogin.Result, error)) error {
			var presentErr error
			result, presentErr = present(ctx, authorizationURL)
			return presentErr
		})
		return result, err
	}
	return cliconfig.OpenNativeEndpointRuntime(definition, present)
}

func nativeLLMOAuthOptions(noBrowser bool, urlWriter io.Writer) oauthlogin.Options {
	opts := oauthlogin.Options{RedirectURL: oauthlogin.ToolHiveCompatibleRedirectURL}
	if noBrowser {
		opts.NoBrowser = true
		opts.URLWriter = urlWriter
	}
	return opts
}

func newNativeLLMHost(_ context.Context, noBrowser bool, urlWriter io.Writer) (nativeLLMHost, error) {
	resolver := permconfig.NewWithEnv(permconfig.Options{Conventional: true}, xdgconfig.OSEnv)
	definitions, _, err := resolver.OperatorProviders()
	if err != nil {
		return nil, err
	}
	native := make(permconfig.ProviderDefinitions)
	for id, definition := range definitions {
		if definition.Auth.Method == "oidc" {
			native[id] = definition
		}
	}
	return &configuredNativeLLMHost{
		definitions: native,
		runtimes:    make(map[string]nativeEndpointRuntime),
		noBrowser:   noBrowser,
		urlWriter:   urlWriter,
	}, nil
}

func (h *configuredNativeLLMHost) EndpointIDs() []string {
	ids := make([]string, 0, len(h.definitions))
	for id := range h.definitions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (h *configuredNativeLLMHost) runtime(ctx context.Context, id string) (nativeEndpointRuntime, error) {
	if runtime := h.runtimes[id]; runtime != nil {
		return runtime, nil
	}
	definition, ok := h.definitions[id]
	if !ok {
		return nil, errors.New("unknown native LLM endpoint")
	}
	runtime, err := openNativeEndpointRuntime(ctx, definition, h.noBrowser, h.urlWriter)
	if err != nil {
		return nil, err
	}
	h.runtimes[id] = runtime
	return runtime, nil
}

func (h *configuredNativeLLMHost) Login(ctx context.Context, id string) error {
	runtime, err := h.runtime(ctx, id)
	if err != nil {
		return err
	}
	return runtime.Login(ctx)
}

func (h *configuredNativeLLMHost) Status(ctx context.Context, id string) llmendpoint.Status {
	runtime, err := h.runtime(ctx, id)
	if err != nil {
		return llmendpoint.StatusStorageUnavailable
	}
	return runtime.Status(ctx)
}

func (h *configuredNativeLLMHost) Logout(ctx context.Context, id string) error {
	runtime, err := h.runtime(ctx, id)
	if err != nil {
		return err
	}
	return runtime.Logout(ctx)
}

func (h *configuredNativeLLMHost) Close() error {
	var errs []error
	for _, runtime := range h.runtimes {
		errs = append(errs, runtime.Close())
	}
	return errors.Join(errs...)
}
