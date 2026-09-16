package cliconfig

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpcredential"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

const mcpOAuthCredentialNamespace = "mecatl-mcp-oauth" // #nosec G101 -- public namespace discriminator, not a credential.

var (
	// ErrMCPProfileInvalid identifies invalid resolved profile metadata.
	ErrMCPProfileInvalid = errors.New("invalid MCP profile")
	// ErrMCPProfileSecret identifies a missing or malformed referenced secret.
	ErrMCPProfileSecret = errors.New("MCP profile secret unavailable")
	// ErrMCPProfileStore identifies a credential source that could not be opened.
	ErrMCPProfileStore = errors.New("MCP credential source unavailable")
)

// MCPProfileError is a redacted profile-loading error. It retains only safe
// metadata and a stable category; looked-up values and adapter causes are never
// retained.
type MCPProfileError struct {
	Server   string
	Field    string
	Ref      string
	Expected string
	Remedy   string
	Kind     error
}

func (e *MCPProfileError) Error() string {
	message := fmt.Sprintf("MCP server %q: %s: %s", e.Server, e.Field, e.Kind)
	if e.Expected != "" {
		message += "; expected " + e.Expected
	}
	if e.Remedy != "" {
		message += "; " + e.Remedy
	}
	return message
}

// Is reports whether target is this error's stable safe category.
func (e *MCPProfileError) Is(target error) bool { return target == e.Kind }

// MCPProfileLoadOptions are explicit metadata and environment inputs for the
// canonical runtime profile loader.
type MCPProfileLoadOptions struct {
	Operator  *permconfig.MCPSection
	Legacy    *MCPServerList
	LookupEnv func(string) (string, bool)
	// Native custody seams keep offline tests away from desktop keyrings and DBus.
	Keyring     mcpcredential.Keyring
	DetectLinux mcpcredential.Detector
}

// MCPProfileResolver binds the legacy CLI metadata and environment lookup to the
// canonical profile loader. app.Build supplies the operator subtree from its
// already-created settings resolver, avoiding a second YAML parse.
type MCPProfileResolver struct {
	legacy *MCPServerList
	lookup func(string) (string, bool)
}

// NewMCPProfileResolver constructs a side-effect-free resolver. Environment and
// credential stores are touched only when Load is called by composition.
func NewMCPProfileResolver(legacy *MCPServerList, lookup func(string) (string, bool)) *MCPProfileResolver {
	return &MCPProfileResolver{legacy: legacy, lookup: lookup}
}

// Load implements app.Config's MCP profile-loader capability without importing
// the composition package.
func (r *MCPProfileResolver) Load(operator *permconfig.MCPSection) ([]mcp.ServerConfig, interface{ Close() error }, error) {
	profiles, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: operator, Legacy: r.legacy, LookupEnv: r.lookup})
	if err != nil {
		return nil, nil, err
	}
	return profiles.Servers, profiles, nil
}

// LoadAuthority resolves exactly one authority path without reparsing settings
// or constructing broker runtime resources.
func (r *MCPProfileResolver) LoadAuthority(operator *permconfig.MCPSection, defaultMode mcpauthority.Mode, brokerSupported bool) (*mcpauthority.Result, error) {
	return ResolveMCPAuthority(MCPAuthorityOptions{
		Operator: operator, Legacy: r.legacy, LookupEnv: r.lookup,
		DefaultMode: defaultMode, BrokerSupported: brokerSupported,
	})
}

// MCPProfiles owns the credential stores/readers backing Servers. Close it only
// after every manager/controller using those servers has closed.
type MCPProfiles struct {
	Servers []mcp.ServerConfig
	owned   []credentialstore.Reader
	once    sync.Once
	err     error
}

// Close closes each loader-created source exactly once.
func (p *MCPProfiles) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		for i := len(p.owned) - 1; i >= 0; i-- {
			p.err = errors.Join(p.err, p.owned[i].Close())
		}
	})
	return p.err
}

// OAuthServer selects a configured OAuth server case-insensitively. The
// returned value borrows its credential source from p and is suitable for the
// explicit login path while p remains open.
func (p *MCPProfiles) OAuthServer(name string) (mcp.ServerConfig, bool) {
	if p == nil {
		return mcp.ServerConfig{}, false
	}
	for _, server := range p.Servers {
		if strings.EqualFold(server.Name, name) && server.OAuth != nil {
			return server, true
		}
	}
	return mcp.ServerConfig{}, false
}

// LoadMCPProfiles resolves the selected operator and legacy entries. Settings
// retain their order; a same-name legacy CLI entry replaces the whole settings
// entry in place, and a distinct legacy entry appends.
func LoadMCPProfiles(opts MCPProfileLoadOptions) (*MCPProfiles, error) {
	if opts.Operator != nil && opts.Operator.Mode == "broker" {
		return nil, fmt.Errorf("%w: broker MCP authority cannot be loaded as direct profiles", ErrMCPProfileInvalid)
	}
	if err := validateLegacyRelaxations(opts.Legacy); err != nil {
		return nil, err
	}
	profiles := make([]profileInput, 0)
	if opts.Operator != nil {
		seen := make(map[string]struct{}, len(opts.Operator.Servers))
		for i := range opts.Operator.Servers {
			profile := &opts.Operator.Servers[i]
			folded := strings.ToLower(profile.Name)
			if !mcpServerName.MatchString(profile.Name) || strings.Contains(profile.Name, "__") {
				return nil, &MCPProfileError{Server: profile.Name, Field: "name", Kind: ErrMCPProfileInvalid}
			}
			if _, exists := seen[folded]; exists {
				return nil, &MCPProfileError{Server: profile.Name, Field: "duplicate name", Kind: ErrMCPProfileInvalid}
			}
			seen[folded] = struct{}{}
			profiles = append(profiles, profileInput{operator: profile})
		}
	}
	if opts.Legacy != nil {
		for i := range opts.Legacy.entries {
			entry := &opts.Legacy.entries[i]
			replacement := profileInput{legacy: entry, relaxed: opts.Legacy.relaxed(entry.cfg.Name), finalized: opts.Legacy.finalized}
			found := false
			for j := range profiles {
				if strings.EqualFold(profiles[j].name(), entry.cfg.Name) {
					profiles[j] = replacement
					found = true
					break
				}
			}
			if !found {
				profiles = append(profiles, replacement)
			}
		}
	}
	if len(profiles) == 0 {
		return &MCPProfiles{}, nil
	}

	result := &MCPProfiles{Servers: make([]mcp.ServerConfig, 0, len(profiles))}
	stores := make(map[string]credentialstore.Store)
	fail := func(err error) (*MCPProfiles, error) {
		_ = result.Close()
		return nil, err
	}
	for _, input := range profiles {
		cfg, err := loadMCPProfile(input, opts, result, stores)
		if err != nil {
			return fail(err)
		}
		result.Servers = append(result.Servers, cfg)
	}
	return result, nil
}

type profileInput struct {
	operator  *permconfig.MCPServerProfile
	legacy    *mcpServerEntry
	relaxed   bool
	finalized bool
}

func (p profileInput) name() string {
	if p.operator != nil {
		return p.operator.Name
	}
	return p.legacy.cfg.Name
}

func (l *MCPServerList) relaxed(name string) bool {
	for _, candidate := range l.insecure {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

func validateLegacyRelaxations(list *MCPServerList) error {
	if list == nil {
		return nil
	}
	for _, name := range list.insecure {
		found := false
		for _, entry := range list.entries {
			if !strings.EqualFold(entry.cfg.Name, name) {
				continue
			}
			found = true
			if !plainHTTPOffHost(entry.cfg.URL) {
				return &MCPProfileError{Server: entry.cfg.Name, Field: "insecure HTTP acknowledgement", Kind: ErrMCPProfileInvalid}
			}
			break
		}
		if !found {
			return &MCPProfileError{Server: name, Field: "insecure HTTP acknowledgement", Kind: ErrMCPProfileInvalid}
		}
	}
	return nil
}

func loadMCPProfile(input profileInput, loadOpts MCPProfileLoadOptions, owner *MCPProfiles, stores map[string]credentialstore.Store) (mcp.ServerConfig, error) {
	lookup := loadOpts.LookupEnv
	if input.legacy != nil {
		entry := input.legacy
		if input.finalized {
			return entry.cfg, nil
		}
		token, ok := lookupMCPEnv(lookup, entry.envName)
		cfg := mcp.ServerConfig{Name: entry.cfg.Name, URL: entry.cfg.URL}
		if !ok || token == "" {
			return cfg, nil
		}
		if !input.relaxed {
			if err := mcp.ValidateClientURL(cfg.URL); err != nil {
				return mcp.ServerConfig{}, &MCPProfileError{Server: cfg.Name, Field: "legacy bearer URL", Ref: entry.envName, Kind: ErrMCPProfileInvalid}
			}
		} else if !plainHTTPOffHost(cfg.URL) {
			return mcp.ServerConfig{}, &MCPProfileError{Server: cfg.Name, Field: "insecure HTTP acknowledgement", Kind: ErrMCPProfileInvalid}
		}
		cfg.Headers = map[string]string{"Authorization": "Bearer " + token}
		return cfg, nil
	}

	profile := input.operator
	cfg := mcp.ServerConfig{Name: profile.Name, URL: profile.URL}
	switch profile.Auth.Mode {
	case "none":
		return cfg, nil
	case "static_bearer":
		if profile.Auth.StaticBearer == nil {
			return mcp.ServerConfig{}, &MCPProfileError{Server: profile.Name, Field: "static_bearer", Kind: ErrMCPProfileInvalid}
		}
		ref := profile.Auth.StaticBearer.TokenEnv
		token, ok := lookupMCPEnv(lookup, ref)
		if !ok || token == "" {
			return mcp.ServerConfig{}, &MCPProfileError{Server: profile.Name, Field: "auth.static_bearer.token_env", Ref: ref, Kind: ErrMCPProfileSecret, Expected: "a non-empty secret in the referenced MECATL_* environment variable", Remedy: "set the referenced environment variable before starting mecatl"}
		}
		cfg.Headers = map[string]string{"Authorization": "Bearer " + token}
		return cfg, nil
	case "oauth":
		if profile.Auth.OAuth == nil {
			return mcp.ServerConfig{}, &MCPProfileError{Server: profile.Name, Field: "oauth", Kind: ErrMCPProfileInvalid}
		}
		oauth, err := loadOAuthProfile(*profile, lookup, loadOpts, owner, stores)
		if err != nil {
			return mcp.ServerConfig{}, err
		}
		cfg.OAuth = oauth
		return cfg, nil
	default:
		return mcp.ServerConfig{}, &MCPProfileError{Server: profile.Name, Field: "auth.mode", Kind: ErrMCPProfileInvalid}
	}
}

func resolvedOAuthScopePolicy(decl *permconfig.MCPOAuthProfile) (bool, []string) {
	scopes := append([]string(nil), decl.Scopes...)
	if decl.Client.Mode == "dcr" && len(scopes) == 0 {
		scopes = []string{"openid"}
	}
	return decl.RequestRefreshToken, scopes
}

func loadOAuthProfile(profile permconfig.MCPServerProfile, lookup func(string) (string, bool), loadOpts MCPProfileLoadOptions, owner *MCPProfiles, stores map[string]credentialstore.Store) (*mcp.OAuthOptions, error) {
	decl := profile.Auth.OAuth
	if decl == nil || decl.Network == nil {
		return nil, &MCPProfileError{Server: profile.Name, Field: "oauth.network", Kind: ErrMCPProfileInvalid}
	}
	if err := validateGlobalOAuth(profile); err != nil {
		return nil, err
	}
	requestRefresh, allowedScopes := resolvedOAuthScopePolicy(decl)
	opts := &mcp.OAuthOptions{
		Subject:       mcp.OAuthSubject{Profile: decl.Profile, Principal: decl.Principal},
		Issuer:        decl.Issuer,
		AllowedScopes: allowedScopes, RequestRefreshToken: requestRefresh,
		Network: mcp.OAuthNetworkPolicy{AdditionalOrigins: append([]string(nil), decl.Network.AdditionalOrigins...), PrivateOrigins: append([]string(nil), decl.Network.PrivateOrigins...), MaxRedirects: decl.Network.MaxRedirects},
	}
	if err := loadOAuthClient(profile, decl, lookup, opts); err != nil {
		return nil, err
	}

	credentials := decl.Credentials
	switch credentials.Mode {
	case "local":
		local := credentials.Local
		if local == nil {
			return nil, &MCPProfileError{Server: profile.Name, Field: "oauth.credentials.local", Kind: ErrMCPProfileInvalid}
		}
		cacheKey := local.Root + "\x00" + local.KeyEnv
		store := stores[cacheKey]
		if local.Key != nil {
			return loadNativeOAuthProfile(profile.Name, local, loadOpts, cacheKey, owner, stores, opts)
		} else if store == nil {
			encoded, ok := lookupMCPEnv(lookup, local.KeyEnv)
			if !ok || encoded == "" {
				return nil, &MCPProfileError{Server: profile.Name, Field: "auth.oauth.credentials.local.key_env", Ref: local.KeyEnv, Kind: ErrMCPProfileSecret, Expected: "a canonical padded base64 value decoding to exactly 32 bytes", Remedy: "set the referenced environment variable to a generated 32-byte encryption key"}
			}
			key, err := decodeMCPKey(encoded)
			if err != nil {
				return nil, &MCPProfileError{Server: profile.Name, Field: "auth.oauth.credentials.local.key_env", Ref: local.KeyEnv, Kind: ErrMCPProfileSecret, Expected: "canonical padded base64 decoding to exactly 32 bytes", Remedy: "replace the referenced environment value with a valid 32-byte encryption key"}
			}
			store, err = credentialstore.NewEncryptedFile(local.Root, mcpOAuthCredentialNamespace, key)
			clear(key)
			runtime.KeepAlive(key)
			if err != nil {
				return nil, &MCPProfileError{Server: profile.Name, Field: "oauth.credentials.local", Kind: ErrMCPProfileStore}
			}
			stores[cacheKey] = store
			owner.owned = append(owner.owned, store)
		}
		opts.CredentialStore = store
	case "environment":
		env := credentials.Environment
		if env == nil {
			return nil, &MCPProfileError{Server: profile.Name, Field: "oauth.credentials.environment", Kind: ErrMCPProfileInvalid}
		}
		encoded, ok := lookupMCPEnv(lookup, env.CredentialEnv)
		if !ok || encoded == "" {
			return nil, &MCPProfileError{Server: profile.Name, Field: "auth.oauth.credentials.environment.credential_env", Ref: env.CredentialEnv, Kind: ErrMCPProfileSecret, Expected: "a canonical padded base64 OAuth credential record", Remedy: "provision the referenced environment variable before starting mecatl"}
		}
		if !validMCPEnvironmentCredential(encoded) {
			return nil, &MCPProfileError{Server: profile.Name, Field: "auth.oauth.credentials.environment.credential_env", Ref: env.CredentialEnv, Kind: ErrMCPProfileSecret, Expected: fmt.Sprintf("canonical padded base64 decoding to 1..%d bytes", credentialstore.MaxValueBytes), Remedy: "replace the referenced environment value with a valid exported OAuth credential record"}
		}
		recordKey, err := mcp.OAuthCredentialRecordKey(profile.URL, *opts)
		if err != nil {
			return nil, &MCPProfileError{Server: profile.Name, Field: "oauth identity", Kind: ErrMCPProfileInvalid}
		}
		reader, err := credentialstore.NewEnvironment(mcpOAuthCredentialNamespace, recordKey, env.CredentialEnv, lookup)
		clear(recordKey)
		runtime.KeepAlive(recordKey)
		if err != nil {
			return nil, &MCPProfileError{Server: profile.Name, Field: "oauth.credentials.environment", Ref: env.CredentialEnv, Kind: ErrMCPProfileStore}
		}
		owner.owned = append(owner.owned, reader)
		opts.CredentialReader = reader
		opts.AllowInMemoryRefresh = env.AllowProcessLocalRefresh
	default:
		return nil, &MCPProfileError{Server: profile.Name, Field: "oauth.credentials.mode", Kind: ErrMCPProfileInvalid}
	}
	return opts, nil
}

func loadNativeOAuthProfile(server string, local *permconfig.MCPLocalCredentialProfile, loadOpts MCPProfileLoadOptions, cacheKey string, owner *MCPProfiles, stores map[string]credentialstore.Store, opts *mcp.OAuthOptions) (*mcp.OAuthOptions, error) {
	filePath := ""
	if local.Key.File != nil {
		filePath = local.Key.File.Path
	}
	selected, err := mcpcredential.Open(context.Background(), local.Root, local.Key.Mode, filePath, loadOpts.Keyring)
	if err != nil {
		return nil, &MCPProfileError{Server: server, Field: "auth.oauth.credentials.local.key", Kind: ErrMCPProfileStore}
	}
	store, err := credentialstore.NewEncryptedFile(local.Root, mcpOAuthCredentialNamespace, selected.Key)
	clear(selected.Key)
	runtime.KeepAlive(selected.Key)
	if err != nil {
		return nil, &MCPProfileError{Server: server, Field: "auth.oauth.credentials.local", Kind: ErrMCPProfileStore}
	}
	stores[cacheKey] = store
	owner.owned = append(owner.owned, store)
	opts.CredentialStore = store
	return opts, nil
}

func loadOAuthClient(profile permconfig.MCPServerProfile, decl *permconfig.MCPOAuthProfile, lookup func(string) (string, bool), opts *mcp.OAuthOptions) error {
	if client := decl.Client.Preregistered; client != nil {
		secret, ok := lookupMCPEnv(lookup, client.SecretEnv)
		if !ok || secret == "" {
			return &MCPProfileError{Server: profile.Name, Field: "auth.oauth.client.preregistered.secret_env", Ref: client.SecretEnv, Kind: ErrMCPProfileSecret, Expected: "a non-empty client secret in the referenced MECATL_* environment variable", Remedy: "set the referenced environment variable before starting mecatl"}
		}
		opts.Client.Preregistered = &oauthex.ClientCredentials{ClientID: client.ID, ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: secret}, Issuer: decl.Issuer}
		return nil
	}
	if decl.Client.DCR != nil {
		opts.Client.DCR = &mcp.OAuthDCRConfig{ServerName: profile.Name}
		return nil
	}
	if decl.Client.CIMD == nil {
		return &MCPProfileError{Server: profile.Name, Field: "oauth.client", Kind: ErrMCPProfileInvalid}
	}
	if !cimdOriginAllowed(profile.URL, decl.Issuer, decl.Client.CIMD.DocumentURL, decl.Network.AdditionalOrigins) {
		return &MCPProfileError{Server: profile.Name, Field: "auth.oauth.client.cimd.document_url", Kind: ErrMCPProfileInvalid, Expected: "a URL on the exact issuer or MCP resource origin, or an origin listed in auth.oauth.network.additional_origins", Remedy: "add the CIMD origin to additional_origins if it is intentionally separate"}
	}
	opts.Client.ClientIDMetadataDocumentURL = decl.Client.CIMD.DocumentURL
	return nil
}

func lookupMCPEnv(lookup func(string) (string, bool), name string) (string, bool) {
	if lookup == nil {
		return "", false
	}
	return lookup(name)
}

func validMCPEnvironmentCredential(encoded string) bool {
	if len(encoded) > base64.StdEncoding.EncodedLen(credentialstore.MaxValueBytes) {
		return false
	}
	value, err := base64.StdEncoding.Strict().DecodeString(encoded)
	valid := err == nil && len(value) > 0 && len(value) <= credentialstore.MaxValueBytes && base64.StdEncoding.EncodeToString(value) == encoded
	clear(value)
	return valid
}

func cimdOriginAllowed(resource, issuer, document string, additional []string) bool {
	documentURL, err := url.Parse(document)
	if err != nil {
		return false
	}
	documentOrigin := mcpProfileOrigin(documentURL)
	for _, raw := range append([]string{resource, issuer}, additional...) {
		u, err := url.Parse(raw)
		if err == nil && mcpProfileOrigin(u) == documentOrigin {
			return true
		}
	}
	return false
}

func mcpProfileOrigin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func decodeMCPKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != encoded {
		clear(key)
		return nil, ErrMCPProfileSecret
	}
	return key, nil
}
