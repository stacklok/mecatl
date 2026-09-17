package cliconfig

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestLoadMCPProfilesSelectedNameSkipsUnrelatedSecret(t *testing.T) {
	operator := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{
		{Name: "native", URL: "https://native.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}},
		{Name: "legacy", URL: "https://legacy.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: environmentOAuth()}},
	}}
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{
		Operator: operator, SelectedName: "native",
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	if len(profiles.Servers) != 1 || profiles.Servers[0].Name != "native" {
		t.Fatalf("selected profiles = %#v", profiles.Servers)
	}
}

func TestLoadMCPProfilesModesAndWholeEntryPrecedence(t *testing.T) {
	operator := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{
		{Name: "public", URL: "https://settings.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: environmentOAuth()}},
		{Name: "static", URL: "https://static.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: &permconfig.MCPStaticBearerProfile{TokenEnv: "MECATL_STATIC"}}},
		{Name: "none", URL: "http://public.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}},
	}}
	legacy := new(MCPServerList)
	if err := legacy.Set("PUBLIC=https://cli.example/mcp"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Set("tail=https://tail.example/mcp"); err != nil {
		t.Fatal(err)
	}
	lookups := make([]string, 0)
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: operator, Legacy: legacy, LookupEnv: func(name string) (string, bool) {
		lookups = append(lookups, name)
		values := map[string]string{"MCP_PUBLIC_TOKEN": "cli-token", "MECATL_STATIC": "static-token"}
		value, ok := values[name]
		return value, ok
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	if got := namesOf(profiles.Servers); !slices.Equal(got, []string{"PUBLIC", "static", "none", "tail"}) {
		t.Fatalf("order = %v", got)
	}
	if profiles.Servers[0].URL != "https://cli.example/mcp" || profiles.Servers[0].OAuth != nil || profiles.Servers[0].Headers["Authorization"] != "Bearer cli-token" {
		t.Fatalf("CLI replacement field-merged: %#v", profiles.Servers[0])
	}
	if slices.Contains(lookups, "MECATL_ENV_CREDENTIAL") {
		t.Fatalf("overridden OAuth secret was looked up: %v", lookups)
	}
	if profiles.Servers[1].Headers["Authorization"] != "Bearer static-token" || profiles.Servers[2].Headers != nil {
		t.Fatalf("static/none configs = %#v / %#v", profiles.Servers[1], profiles.Servers[2])
	}
}

func TestMCPServerParsingIsLookupFree(t *testing.T) {
	list := new(MCPServerList)
	t.Setenv("MCP_SVC_TOKEN", "before")
	if err := list.Set("svc=https://svc.example/mcp"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_SVC_TOKEN", "runtime")
	if err := list.Finalize(); err != nil {
		t.Fatal(err)
	}
	if got := list.Servers()[0].Headers["Authorization"]; got != "Bearer runtime" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestLoadMCPProfilesNoConfigurationHasNoLookup(t *testing.T) {
	calls := 0
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{LookupEnv: func(string) (string, bool) { calls++; return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(profiles.Servers) != 0 {
		t.Fatalf("calls=%d servers=%v", calls, profiles.Servers)
	}
	none := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{{Name: "none", URL: "http://public.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}
	profiles, err = LoadMCPProfiles(MCPProfileLoadOptions{Operator: none})
	if err != nil || len(profiles.Servers) != 1 {
		t.Fatalf("lookup-free none profile = %v, %v", profiles.Servers, err)
	}
}

func TestLoadMCPProfilesEnvironmentReaderUsesCanonicalRecordKey(t *testing.T) {
	profile := permconfig.MCPServerProfile{Name: "env", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: environmentOAuth()}}
	encoded := base64.StdEncoding.EncodeToString([]byte("opaque-record"))
	lookups := 0
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{profile}}, LookupEnv: func(name string) (string, bool) {
		lookups++
		if name == "MECATL_ENV_CREDENTIAL" {
			return encoded, true
		}
		return "", false
	}})
	if err != nil {
		t.Fatal(err)
	}
	if lookups != 1 {
		t.Fatalf("environment credential validation lookups = %d, want 1", lookups)
	}
	cfg, ok := profiles.OAuthServer("ENV")
	if !ok || cfg.OAuth.CredentialReader == nil || cfg.OAuth.CredentialStore != nil {
		t.Fatalf("OAuthServer = %#v, %v", cfg, ok)
	}
	key, err := mcp.OAuthCredentialRecordKey(cfg.URL, *cfg.OAuth)
	if err != nil {
		t.Fatal(err)
	}
	record, err := cfg.OAuth.CredentialReader.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if string(record.Value) != "opaque-record" {
		t.Fatalf("record = %q", record.Value)
	}
	if err := profiles.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.OAuth.CredentialReader.Get(context.Background(), key); !errors.Is(err, credentialstore.ErrClosed) {
		t.Fatalf("Get after Close = %v", err)
	}
	if err := profiles.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

func TestLocalMCPProfileLifecycleIsIdempotentAndTerminal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	oauth := environmentOAuth()
	oauth.Credentials = permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: root, KeyEnv: "MECATL_KEY"}}
	profile := permconfig.MCPServerProfile{Name: "local", URL: "https://local.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: oauth}}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{profile}}, LookupEnv: func(string) (string, bool) { return key, true }})
	if err != nil {
		t.Fatal(err)
	}
	store := profiles.Servers[0].OAuth.CredentialStore
	if store == nil {
		t.Fatal("local OAuth profile has no mutable store")
	}
	if err := profiles.Close(); err != nil {
		t.Fatal(err)
	}
	if err := profiles.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, err := store.Get(context.Background(), []byte("record")); !errors.Is(err, credentialstore.ErrClosed) {
		t.Fatalf("Get after Close = %v, want ErrClosed", err)
	}
}

func TestLoadMCPProfilesSharesLocalStoreAndRejectsBadKeysSafely(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	local := func(name string) permconfig.MCPServerProfile {
		oauth := environmentOAuth()
		oauth.Client = permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "client-id", SecretEnv: "MECATL_CLIENT_SECRET"}}
		oauth.Credentials = permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: root, KeyEnv: "MECATL_KEY"}}
		return permconfig.MCPServerProfile{Name: name, URL: "https://" + name + ".example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: oauth}}
	}
	section := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{local("one"), local("two")}}
	secret := "canary-key-value-never-in-error"
	if _, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: section, LookupEnv: func(name string) (string, bool) {
		if name == "MECATL_CLIENT_SECRET" {
			return "client-secret", true
		}
		return secret, true
	}}); !errors.Is(err, ErrMCPProfileSecret) {
		t.Fatalf("bad key error = %v", err)
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked key: %v", err)
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: section, LookupEnv: func(name string) (string, bool) {
		if name == "MECATL_CLIENT_SECRET" {
			return "client-secret", true
		}
		return key, true
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	if profiles.Servers[0].OAuth.CredentialStore != profiles.Servers[1].OAuth.CredentialStore {
		t.Fatal("same root/key reference did not share one store")
	}
	client := profiles.Servers[0].OAuth.Client.Preregistered
	if client == nil || client.ClientSecretAuth == nil || client.ClientID != "client-id" || client.ClientSecretAuth.ClientSecret != "client-secret" || client.Issuer != "https://issuer.example" {
		t.Fatalf("preregistered client = %#v", client)
	}
}

func TestLoadMCPProfilesSelectedSecretErrorsAreTypedAndRedacted(t *testing.T) {
	section := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{{Name: "static", URL: "https://example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: &permconfig.MCPStaticBearerProfile{TokenEnv: "MECATL_MISSING"}}}}}
	_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: section, LookupEnv: func(string) (string, bool) { return "", false }})
	if !errors.Is(err, ErrMCPProfileSecret) {
		t.Fatalf("error = %v", err)
	}
	var profileErr *MCPProfileError
	if !errors.As(err, &profileErr) || profileErr.Server != "static" || profileErr.Ref != "MECATL_MISSING" {
		t.Fatalf("typed error = %#v", err)
	}
}

func TestLoadMCPProfilesRejectsUnlistedCIMDOrigin(t *testing.T) {
	oauth := environmentOAuth()
	oauth.Network.AdditionalOrigins = nil
	profile := permconfig.MCPServerProfile{Name: "cimd", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: oauth}}
	_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{profile}}, LookupEnv: func(string) (string, bool) {
		return base64.StdEncoding.EncodeToString([]byte("credential")), true
	}})
	if !errors.Is(err, ErrMCPProfileInvalid) || !strings.Contains(err.Error(), "auth.oauth.client.cimd.document_url") || !strings.Contains(err.Error(), "additional_origins") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadMCPProfileSecretErrorsAreActionableAndRedacted(t *testing.T) {
	const canary = "secret-value-canary"
	root := filepath.Join(t.TempDir(), "credentials")
	localOAuth := environmentOAuth()
	localOAuth.Client = permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "client", SecretEnv: "MECATL_CLIENT"}}
	localOAuth.Credentials = permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: root, KeyEnv: "MECATL_KEY"}}
	environment := environmentOAuth()
	cases := []struct {
		name, field, expected string
		profile               *permconfig.MCPOAuthProfile
		lookup                func(string) (string, bool)
	}{
		{name: "unset local key", field: "auth.oauth.credentials.local.key_env", expected: "exactly 32 bytes", profile: localOAuth, lookup: func(name string) (string, bool) {
			if name == "MECATL_CLIENT" {
				return "client-secret", true
			}
			return "", false
		}},
		{name: "malformed local key", field: "auth.oauth.credentials.local.key_env", expected: "canonical padded base64", profile: localOAuth, lookup: func(name string) (string, bool) {
			if name == "MECATL_CLIENT" {
				return "client-secret", true
			}
			return canary, true
		}},
		{name: "unset environment credential", field: "auth.oauth.credentials.environment.credential_env", expected: "provision", profile: environment, lookup: func(string) (string, bool) { return "", false }},
		{name: "malformed environment credential", field: "auth.oauth.credentials.environment.credential_env", expected: "canonical padded base64", profile: environment, lookup: func(string) (string, bool) { return canary, true }},
		{name: "oversized environment credential", field: "auth.oauth.credentials.environment.credential_env", expected: fmt.Sprintf("1..%d bytes", credentialstore.MaxValueBytes), profile: environment, lookup: func(string) (string, bool) {
			return strings.Repeat("A", base64.StdEncoding.EncodedLen(credentialstore.MaxValueBytes)+4), true
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			profile := permconfig.MCPServerProfile{Name: "safe_server", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: test.profile}}
			_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{profile}}, LookupEnv: test.lookup})
			if !errors.Is(err, ErrMCPProfileSecret) || !strings.Contains(err.Error(), `MCP server "safe_server"`) || !strings.Contains(err.Error(), test.field) || !strings.Contains(err.Error(), test.expected) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "MECATL_KEY") || strings.Contains(err.Error(), "MECATL_ENV_CREDENTIAL") {
				t.Fatalf("error leaked secret or reference: %v", err)
			}
		})
	}
}

func TestADR_0325_DirectDCRProfileScopeAndStorePolicy(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	newProfile := func(root string) permconfig.MCPServerProfile {
		return permconfig.MCPServerProfile{
			Name: "connector", URL: "https://connector.example/gw/mcp",
			Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
				Profile: "connector", Principal: "local-user", Issuer: "http://issuer.example",
				Client:      permconfig.MCPOAuthClientProfile{Mode: "dcr", DCR: &permconfig.MCPDCRClientProfile{}},
				Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: root, KeyEnv: "MECATL_KEY"}},
				Network:     &permconfig.MCPOAuthNetworkProfile{},
			}},
		}
	}

	root := filepath.Join(t.TempDir(), "must-not-exist")
	lookups := 0
	profile := newProfile(root)
	_, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{profile}}, LookupEnv: func(string) (string, bool) {
		lookups++
		return key, true
	}})
	if !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("non-HTTPS direct DCR issuer error = %v, want invalid profile", err)
	}
	if lookups != 0 {
		t.Fatalf("credential lookup occurred before issuer rejection: %d", lookups)
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential store path was touched before issuer rejection: %v", statErr)
	}

	preregistered := newProfile(filepath.Join(t.TempDir(), "preregistered"))
	preregistered.Auth.OAuth.Client = permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "client", SecretEnv: "MECATL_CLIENT"}}
	preregistered.Auth.OAuth.Scopes = []string{"read"}
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{preregistered}}, LookupEnv: func(name string) (string, bool) {
		if name == "MECATL_CLIENT" {
			return "secret", true
		}
		return key, name == "MECATL_KEY"
	}})
	if err != nil {
		t.Fatalf("existing preregistered HTTP issuer policy changed: %v", err)
	}
	profiles.Close()
}

func environmentOAuth() *permconfig.MCPOAuthProfile {
	return &permconfig.MCPOAuthProfile{
		Profile: "work", Principal: "principal", Issuer: "https://issuer.example",
		Client:      permconfig.MCPOAuthClientProfile{Mode: "cimd", CIMD: &permconfig.MCPCIMDClientProfile{DocumentURL: "https://client.example/metadata.json"}},
		Scopes:      []string{"read"},
		Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "environment", Environment: &permconfig.MCPEnvironmentCredentialProfile{CredentialEnv: "MECATL_ENV_CREDENTIAL"}},
		Network:     &permconfig.MCPOAuthNetworkProfile{AdditionalOrigins: []string{"https://client.example"}},
	}
}

func namesOf(configs []mcp.ServerConfig) []string {
	out := make([]string, len(configs))
	for i := range configs {
		out[i] = configs[i].Name
	}
	return out
}

func TestLoadMCPProfilesInjectsDCRLifecycleServerName(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	profile := permconfig.MCPServerProfile{
		Name: "connector", URL: "https://connector.example/gw/mcp",
		Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
			Profile: "connector", Principal: "local-user", Issuer: "https://issuer.example",
			Client:      permconfig.MCPOAuthClientProfile{Mode: "dcr", DCR: &permconfig.MCPDCRClientProfile{}},
			Credentials: permconfig.MCPOAuthCredentialProfile{Mode: "local", Local: &permconfig.MCPLocalCredentialProfile{Root: filepath.Join(t.TempDir(), "credentials"), KeyEnv: "MECATL_KEY"}},
			Network:     &permconfig.MCPOAuthNetworkProfile{},
		}},
	}
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{profile}}, LookupEnv: func(string) (string, bool) { return key, true }})
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	if got := profiles.Servers[0].OAuth.Client.DCR.ServerName; got != "connector" {
		t.Fatalf("DCR lifecycle server name = %q, want connector", got)
	}
}
