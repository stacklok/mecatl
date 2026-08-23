package cliconfig

import (
	"context"
	"errors"
	"flag"
	"io"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type fakeValidator struct{}

func (fakeValidator) Validate(context.Context, string) (*session.Principal, error) { return nil, nil }

func TestOIDCMaxJWKSStalenessFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want time.Duration
	}{
		{name: "default", want: time.Hour},
		{name: "override", args: []string{"--oidc-max-jwks-staleness=15m"}, want: 15 * time.Minute},
		{name: "disabled", args: []string{"--oidc-max-jwks-staleness=0"}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet(tc.name, flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			var cfg OIDCConfig
			RegisterOIDCFlags(fs, &cfg)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cfg.MaxJWKSStaleness != tc.want {
				t.Fatalf("MaxJWKSStaleness = %v, want %v", cfg.MaxJWKSStaleness, tc.want)
			}
		})
	}
}

func TestOIDCValidatorRejectsNegativeMaxJWKSStaleness(t *testing.T) {
	_, err := OIDCValidator(context.Background(), OIDCConfig{MaxJWKSStaleness: -time.Second})
	if !errors.Is(err, ErrOIDCMisconfigured) {
		t.Fatalf("OIDCValidator error = %v, want ErrOIDCMisconfigured", err)
	}
}

func TestOIDCPrivateHTTPSIssuerFlagsAndValidation(t *testing.T) {
	fs := flag.NewFlagSet("oidc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var cfg OIDCConfig
	RegisterOIDCFlags(fs, &cfg)
	if err := fs.Parse([]string{"--oidc-allow-private-https-issuer", "--oidc-ca-cert-file=/run/oidc/ca.pem"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.AllowPrivateHTTPSIssuer || cfg.TrustedCAFile != "/run/oidc/ca.pem" {
		t.Fatalf("private HTTPS flags did not parse: %#v", cfg)
	}

	for _, tc := range []struct {
		name string
		cfg  OIDCConfig
		want bool
	}{
		{name: "private mode requires CA", cfg: OIDCConfig{Issuer: "https://idp.example", Audience: "mecatl", AllowPrivateHTTPSIssuer: true}, want: true},
		{name: "CA alone remains secure", cfg: OIDCConfig{Issuer: "https://idp.example", Audience: "mecatl", TrustedCAFile: "/run/oidc/ca.pem"}},
		{name: "private mode with CA", cfg: OIDCConfig{Issuer: "https://idp.example", Audience: "mecatl", AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/oidc/ca.pem"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.NewValidator = func(_ context.Context, _ OIDCConfig) (server.PrincipalValidator, error) { return fakeValidator{}, nil }
			_, err := OIDCValidator(context.Background(), tc.cfg)
			if tc.want != (err != nil) {
				t.Fatalf("OIDCValidator(%#v) error = %v, want error=%t", tc.cfg, err, tc.want)
			}
		})
	}
}

func TestValidateOIDCAuthToken(t *testing.T) {
	for _, tc := range []struct {
		name      string
		oidc      OIDCConfig
		authToken string
		wantErr   bool
	}{
		{name: "neither"},
		{name: "static token only", authToken: "secret"},
		{name: "OIDC only", oidc: OIDCConfig{Issuer: "https://idp.example"}},
		{name: "both", oidc: OIDCConfig{Issuer: "https://idp.example"}, authToken: "secret", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOIDCAuthToken(tc.oidc, tc.authToken)
			if tc.wantErr && !errors.Is(err, ErrOIDCMisconfigured) {
				t.Fatalf("ValidateOIDCAuthToken() error = %v, want ErrOIDCMisconfigured", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateOIDCAuthToken() error = %v, want nil", err)
			}
		})
	}
}
