package main

import (
	"context"
	"io"

	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

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
