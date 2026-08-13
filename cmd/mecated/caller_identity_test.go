package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// TestCallerIdentity_Scenario1_MisconfiguredOIDCFailsToStart pins AC1.6: with
// the OIDC flags set but validator construction failing (an unreachable JWKS and
// no --oidc-jwks-uri static override), the server REFUSES TO START. It must
// never degrade silently to the unauthenticated path — that is the classic
// misconfigured-verifier fail-open (ADR 0100 decision 3).
func TestCallerIdentity_Scenario1_MisconfiguredOIDCFailsToStart(t *testing.T) {
	t.Parallel()

	jwksDown := func(context.Context, cliconfig.OIDCConfig) (server.PrincipalValidator, error) {
		return nil, errors.New("discovery: Get \"https://idp.example.com/.well-known/openid-configuration\": dial tcp: connection refused")
	}

	cases := []struct {
		name    string
		oidc    cliconfig.OIDCConfig
		wantErr bool
	}{
		{
			name:    "unreachable JWKS, no static override",
			oidc:    cliconfig.OIDCConfig{Issuer: "https://idp.example.com", Audience: "mecatl", NewValidator: jwksDown},
			wantErr: true,
		},
		{
			name:    "issuer set, audience missing",
			oidc:    cliconfig.OIDCConfig{Issuer: "https://idp.example.com", NewValidator: jwksDown},
			wantErr: true,
		},
		{
			name:    "issuer set, no validator available",
			oidc:    cliconfig.OIDCConfig{Issuer: "https://idp.example.com", Audience: "mecatl"},
			wantErr: true,
		},
		{
			name:    "negative maximum JWKS staleness",
			oidc:    cliconfig.OIDCConfig{MaxJWKSStaleness: -1},
			wantErr: true,
		},
		{
			// OIDC off: unchanged, the authenticator builds.
			name:    "oidc not configured",
			oidc:    cliconfig.OIDCConfig{},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := config{oidc: tc.oidc}
			_, auth, err := buildEdge(context.Background(), cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatal("buildEdge err = nil, want a startup failure (silent fail-open)")
				}
				if auth != nil {
					t.Fatal("buildEdge returned an Authenticator despite a fatal OIDC misconfiguration")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildEdge err = %v, want nil", err)
			}
			if auth == nil {
				t.Fatal("buildEdge returned no Authenticator")
			}
		})
	}
}
