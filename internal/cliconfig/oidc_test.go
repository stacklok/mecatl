package cliconfig

import (
	"context"
	"errors"
	"flag"
	"io"
	"reflect"
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

func TestADR_0290_ServerCompositionParity(t *testing.T) {
	for _, name := range []string{"oidc-issuer", "oidc-audience", "oidc-resource", "oidc-client-id", "oidc-scopes"} {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		var cfg OIDCConfig
		RegisterOIDCFlags(fs, &cfg)
		if fs.Lookup(name) == nil {
			t.Fatalf("shared OIDC flag %q is not registered", name)
		}
	}
	var cfg OIDCConfig
	fs := flag.NewFlagSet("parity", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	RegisterOIDCFlags(fs, &cfg)
	if err := fs.Parse([]string{"--oidc-issuer=https://issuer.example", "--oidc-audience=api", "--oidc-resource=https://resource.example", "--oidc-client-id=mecatui", "--oidc-scopes=profile,openid,profile"}); err != nil {
		t.Fatal(err)
	}
	projection, err := cfg.ProfileProjection()
	if err != nil {
		t.Fatal(err)
	}
	if projection.Resource != "https://resource.example" || projection.ClientID != "mecatui" || len(projection.Scopes) != 2 {
		t.Fatalf("shared composition contract = %#v", projection)
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

func TestADR_0290_ProfileProjection(t *testing.T) {
	var got OIDCConfig
	cfg := OIDCConfig{Issuer: "https://issuer.example", Audience: "api://mecatl", Resource: "https://api.example.com", ClientID: "mecatui"}
	cfg.NewValidator = func(_ context.Context, c OIDCConfig) (server.PrincipalValidator, error) {
		got = c
		return fakeValidator{}, nil
	}
	if _, err := OIDCValidator(context.Background(), cfg); err != nil {
		t.Fatalf("OIDCValidator: %v", err)
	}
	if got.Issuer != cfg.Issuer || got.Audience != cfg.Audience {
		t.Fatalf("validator projection = issuer %q audience %q, want %q %q", got.Issuer, got.Audience, cfg.Issuer, cfg.Audience)
	}
	projection, err := cfg.ProfileProjection()
	if err != nil {
		t.Fatalf("ProfileProjection: %v", err)
	}
	if projection.Issuer != cfg.Issuer || projection.Audience != cfg.Audience {
		t.Fatalf("metadata projection = issuer %q audience %q, want %q %q", projection.Issuer, projection.Audience, cfg.Issuer, cfg.Audience)
	}
}

func TestADR_0290_ProfileConfigurationMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cfg         OIDCConfig
		wantEnabled bool
		wantErr     bool
	}{
		{name: "absent", wantEnabled: false},
		{name: "complete", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://resource", ClientID: "client"}, wantEnabled: true},
		{name: "force query", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://resource?", ClientID: "client"}, wantErr: true},
		{name: "insecure issuer", cfg: OIDCConfig{Issuer: "http://issuer", Audience: "api", Resource: "https://resource", ClientID: "client", InsecureAllowPrivateIssuer: true}, wantErr: true},
		{name: "resource only", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://resource"}, wantErr: true},
		{name: "client only", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", ClientID: "client"}, wantErr: true},
		{name: "without OIDC", cfg: OIDCConfig{Resource: "https://resource", ClientID: "client"}, wantErr: true},
		{name: "scopes only", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", ScopesCSV: "read"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.ValidateOIDCProfile()
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateOIDCProfile: %v, want error %t", err, tc.wantErr)
			}
			if !tc.wantErr && tc.cfg.ProtectedResourceEnabled() != tc.wantEnabled {
				t.Fatalf("ProtectedResourceEnabled = %t, want %t", tc.cfg.ProtectedResourceEnabled(), tc.wantEnabled)
			}
		})
	}
}

func TestADR_0290_ScopeCSV(t *testing.T) {
	for _, tc := range []struct {
		csv     string
		want    []string
		wantErr bool
	}{
		{csv: " z, a, z,b ", want: []string{"a", "b", "z"}},
		{csv: "", want: nil},
		{csv: "read,,write", wantErr: true},
		{csv: "read,not safe", wantErr: true},
		{csv: "read,\"write\"", wantErr: true},
	} {
		got, err := ParseOIDCScopes(tc.csv)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ParseOIDCScopes(%q): %v, want error %t", tc.csv, err, tc.wantErr)
		}
		if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("ParseOIDCScopes(%q) = %#v, want %#v", tc.csv, got, tc.want)
		}
	}
	cfg := OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://resource", ClientID: "client"}
	if err := cfg.ValidateOIDCProfile(); err != nil {
		t.Fatal(err)
	}
	if cfg.Scopes != nil {
		t.Fatalf("absent scopes = %#v, want nil", cfg.Scopes)
	}
	fs := flag.NewFlagSet("oidc-profile", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flagCfg := OIDCConfig{}
	RegisterOIDCFlags(fs, &flagCfg)
	if err := fs.Parse([]string{"--oidc-scopes", " z, a, z,b "}); err != nil {
		t.Fatalf("Parse flags: %v", err)
	}
	flagCfg.Issuer, flagCfg.Audience = "https://issuer", "api"
	flagCfg.Resource, flagCfg.ClientID = "https://resource", "client"
	if err := flagCfg.ValidateOIDCProfile(); err != nil {
		t.Fatalf("ValidateOIDCProfile after flag parse: %v", err)
	}
	if !reflect.DeepEqual(flagCfg.Scopes, []string{"a", "b", "z"}) {
		t.Fatalf("flag scopes = %#v, want [a b z]", flagCfg.Scopes)
	}
	emptyFlags := flag.NewFlagSet("oidc-empty-scope", flag.ContinueOnError)
	emptyFlags.SetOutput(io.Discard)
	emptyCfg := OIDCConfig{}
	RegisterOIDCFlags(emptyFlags, &emptyCfg)
	if err := emptyFlags.Parse([]string{"--oidc-scopes="}); err != nil {
		t.Fatalf("Parse empty scopes: %v", err)
	}
	if err := emptyCfg.ValidateOIDCProfile(); err == nil {
		t.Fatal("supplied empty --oidc-scopes unexpectedly accepted")
	}
}

func TestADR_0290_ProfileValidation(t *testing.T) {
	cases := []OIDCConfig{
		{Issuer: "https://issuer", Audience: "api", Resource: "http://resource", ClientID: "client"},
		{Issuer: "https://issuer", Audience: "api", Resource: "https://resource", ClientID: "client", ScopesCSV: "read,,write"},
		{Issuer: "https://issuer", Audience: "api", Resource: "https://resource"},
	}
	for _, cfg := range cases {
		if err := cfg.ValidateOIDCProfile(); err == nil {
			t.Fatalf("ValidateOIDCProfile(%#v) = nil, want startup error", cfg)
		}
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
