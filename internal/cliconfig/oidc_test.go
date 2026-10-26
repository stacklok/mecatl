package cliconfig

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	oidcauthn "github.com/stacklok/mecatl/authn/oidc"
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

func TestOIDCServerCompositionParity(t *testing.T) {
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

func TestOIDCOwnerIdentityFlags(t *testing.T) {
	fs := flag.NewFlagSet("oidc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var cfg OIDCConfig
	RegisterOIDCFlags(fs, &cfg)
	if err := fs.Parse([]string{"--oidc-owner-identity=sub_id", "--oidc-accepted-client-ids= mecatui, ,studio,"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.OwnerIdentity != oidcauthn.OwnerIdentitySubID || !reflect.DeepEqual(cfg.AcceptedClientIDs, []string{"mecatui", "studio"}) {
		t.Fatalf("owner identity flags = %q %#v", cfg.OwnerIdentity, cfg.AcceptedClientIDs)
	}

	for _, raw := range []string{"upstream_subject", "SUB_ID", " sub_id", "true"} {
		fs = flag.NewFlagSet("unknown", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		var unknown OIDCConfig
		RegisterOIDCFlags(fs, &unknown)
		if err := fs.Parse([]string{"--oidc-owner-identity=" + raw}); err == nil || !strings.Contains(err.Error(), "accepted values: sub_id") {
			t.Fatalf("--oidc-owner-identity=%q: Parse = %v, want an accepted-values error", raw, err)
		}
	}

	fs = flag.NewFlagSet("empty", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var off OIDCConfig
	RegisterOIDCFlags(fs, &off)
	if err := fs.Parse([]string{"--oidc-owner-identity=sub_id", "--oidc-owner-identity="}); err != nil || off.OwnerIdentity != "" {
		t.Fatalf("--oidc-owner-identity= : Parse = %v, OwnerIdentity = %q, want off", err, off.OwnerIdentity)
	}

	for _, raw := range []string{"", ",,", " , "} {
		fs = flag.NewFlagSet("empty", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		var empty OIDCConfig
		RegisterOIDCFlags(fs, &empty)
		if err := fs.Parse([]string{"--oidc-accepted-client-ids=" + raw}); err == nil || !strings.Contains(err.Error(), "at least one client ID") {
			t.Fatalf("--oidc-accepted-client-ids=%q: Parse = %v, want an empty-list error", raw, err)
		}
	}

	var defaults OIDCConfig
	fs = flag.NewFlagSet("defaults", flag.ContinueOnError)
	RegisterOIDCFlags(fs, &defaults)
	if err := fs.Parse(nil); err != nil || defaults.OwnerIdentity != "" || defaults.AcceptedClientIDs != nil {
		t.Fatalf("defaults = %q %#v (%v), want off", defaults.OwnerIdentity, defaults.AcceptedClientIDs, err)
	}
}

// TestOIDCOwnerIdentityMisconfigurationFailsStartup pins that every
// misconfiguration is a fatal ErrOIDCMisconfigured from OIDCValidator: the
// missing-issuer and unaccepted advertised client ID cases are caught here, the
// pairing with client IDs by the real
// authn/oidc constructor before it contacts the issuer.
func TestOIDCOwnerIdentityMisconfigurationFailsStartup(t *testing.T) {
	const issuer = "https://idp.example.invalid"
	subID := oidcauthn.OwnerIdentitySubID
	for _, tc := range []struct {
		name string
		cfg  OIDCConfig
		want string
	}{
		{name: "owner identity without issuer", cfg: OIDCConfig{OwnerIdentity: subID, AcceptedClientIDs: []string{"mecatui"}}, want: "require --oidc-issuer"},
		{name: "client IDs without issuer", cfg: OIDCConfig{AcceptedClientIDs: []string{"mecatui"}}, want: "require --oidc-issuer"},
		{name: "owner identity sub_id without client IDs", cfg: OIDCConfig{Issuer: issuer, Audience: "mecatl", OwnerIdentity: subID}, want: "requires accepted client IDs"},
		{name: "client IDs without owner identity", cfg: OIDCConfig{Issuer: issuer, Audience: "mecatl", AcceptedClientIDs: []string{"mecatui"}}, want: "require owner identity sub_id"},
		{name: "unknown owner identity", cfg: OIDCConfig{Issuer: issuer, Audience: "mecatl", OwnerIdentity: "upstream_subject", AcceptedClientIDs: []string{"mecatui"}}, want: `unknown owner identity "upstream_subject"`},
		{
			name: "advertised client ID not accepted",
			cfg:  OIDCConfig{Issuer: issuer, Audience: "mecatl", Resource: "https://mecatl.example", ClientID: "mecatui", OwnerIdentity: subID, AcceptedClientIDs: []string{"studio"}},
			want: `--oidc-client-id "mecatui" must be listed in --oidc-accepted-client-ids`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := OIDCValidator(context.Background(), tc.cfg)
			if v != nil || !errors.Is(err, ErrOIDCMisconfigured) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("OIDCValidator = (%v, %v), want %q ErrOIDCMisconfigured", v, err, tc.want)
			}
		})
	}
	ok := OIDCConfig{Issuer: issuer, Audience: "mecatl", Resource: "https://mecatl.example", ClientID: "mecatui", OwnerIdentity: subID, AcceptedClientIDs: []string{"studio", "mecatui"}}
	ok.NewValidator = func(_ context.Context, c OIDCConfig) (server.PrincipalValidator, error) {
		if c.OwnerIdentity != subID || len(c.AcceptedClientIDs) != 2 {
			t.Fatalf("constructor saw %#v", c)
		}
		return fakeValidator{}, nil
	}
	if _, err := OIDCValidator(context.Background(), ok); err != nil {
		t.Fatalf("OIDCValidator(valid) = %v", err)
	}
}

func TestOIDCProfileProjection(t *testing.T) {
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

// TestOIDCProfileIdentityBrokerSignInFollowsSubIDOwnerIdentity pins that the
// published metadata asks clients for the RFC 8707 resource parameter only with
// the AS-1 (sub_id) token profile, and omits the field otherwise.
func TestOIDCProfileIdentityBrokerSignInFollowsSubIDOwnerIdentity(t *testing.T) {
	const field = "com.stacklok.mecatl.identity_broker_sign_in"
	for _, tc := range []struct {
		name string
		cfg  OIDCConfig
		want any
	}{
		{"owner identity off", OIDCConfig{Issuer: "https://issuer.example", Audience: "api://mecatl", Resource: "https://api.example.com", ClientID: "mecatui"}, nil},
		{"owner identity sub_id", OIDCConfig{Issuer: "https://issuer.example", Audience: "api://mecatl", Resource: "https://api.example.com", ClientID: "mecatui", OwnerIdentity: oidcauthn.OwnerIdentitySubID, AcceptedClientIDs: []string{"mecatui"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projection, err := tc.cfg.ProfileProjection()
			if err != nil {
				t.Fatalf("ProfileProjection: %v", err)
			}
			h := server.NewProtectedResourceHandler(projection.ProtectedResourceProfile())
			if h == nil {
				t.Fatal("metadata handler disabled")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, server.WellKnownProtectedResourceURL("https://api.example.com"), nil))
			var doc map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatalf("decode metadata (status %d): %v", rec.Code, err)
			}
			got, present := doc[field]
			if tc.want == nil && present || tc.want != nil && got != tc.want {
				t.Fatalf("metadata %s = %#v (present %v), want %#v", field, got, present, tc.want)
			}
		})
	}
}

func TestOIDCProfileConfigurationMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cfg         OIDCConfig
		wantEnabled bool
		wantErr     bool
	}{
		{name: "absent", wantEnabled: false},
		{name: "complete", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://resource", ClientID: "client"}, wantEnabled: true},
		{name: "canonicalizes resource", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://RESOURCE:443/", ClientID: "client"}, wantEnabled: true},
		{name: "force query", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://resource?", ClientID: "client"}, wantErr: true},
		{name: "insecure issuer", cfg: OIDCConfig{Issuer: "http://issuer", Audience: "api", Resource: "https://resource", ClientID: "client", InsecureAllowPrivateIssuer: true}, wantErr: true},
		{name: "resource only", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://resource"}, wantErr: true},
		{name: "client only", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", ClientID: "client"}, wantErr: true},
		{name: "without OIDC", cfg: OIDCConfig{Resource: "https://resource", ClientID: "client"}, wantErr: true},
		{name: "scopes only", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", ScopesCSV: "read"}, wantErr: true},
		{name: "client ID too long", cfg: OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://resource", ClientID: strings.Repeat("a", 1025)}, wantErr: true},
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

func TestOIDCProfileCanonicalizesResourceForEveryProjection(t *testing.T) {
	cfg := OIDCConfig{Issuer: "https://issuer", Audience: "api", Resource: "https://RESOURCE:443/", ClientID: "client"}
	if err := cfg.ValidateOIDCProfile(); err != nil {
		t.Fatal(err)
	}
	projection, err := cfg.ProfileProjection()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Resource != "https://resource" || projection.Resource != cfg.Resource || projection.ProtectedResourceProfile().Resource != cfg.Resource {
		t.Fatalf("resource identities diverged: config=%q projection=%q profile=%q", cfg.Resource, projection.Resource, projection.ProtectedResourceProfile().Resource)
	}
}

func TestOIDCScopeCSV(t *testing.T) {
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
	for _, name := range []string{"--oidc-resource=", "--oidc-client-id="} {
		fs := flag.NewFlagSet("oidc-empty-profile", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		cfg := OIDCConfig{}
		RegisterOIDCFlags(fs, &cfg)
		if err := fs.Parse([]string{name}); err != nil {
			t.Fatalf("Parse %s: %v", name, err)
		}
		if err := cfg.ValidateOIDCProfile(); err == nil {
			t.Fatalf("supplied empty %s unexpectedly accepted", name)
		}
	}
}

func TestOIDCProfileValidation(t *testing.T) {
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
