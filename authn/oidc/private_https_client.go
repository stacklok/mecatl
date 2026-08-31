package oidc

import (
	"context"
	"net/http"

	"github.com/stacklok/mecatl/authn/oidc/scopedhttps"
)

func newPrivateHTTPSClient(ctx context.Context, cfg Config) (*http.Client, error) {
	return scopedhttps.NewSingleIssuerClient(ctx, []string{cfg.Issuer, cfg.JWKSURI}, cfg.TrustedCAPEM)
}
