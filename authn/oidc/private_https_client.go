package oidc

import (
	"context"
	"net/http"

	"github.com/stacklok/mecatl/authn/oidc/scopedhttps"
)

func newPrivateHTTPSClient(ctx context.Context, cfg Config) (*http.Client, error) {
	endpoints := []string{cfg.JWKSURI}
	if cfg.JWKSURI == "" {
		endpoints = []string{cfg.Issuer}
	}
	return scopedhttps.NewSingleIssuerClient(ctx, endpoints, cfg.TrustedCAPEM)
}
