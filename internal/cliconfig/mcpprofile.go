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

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpcredential"
	"github.com/stacklok/mecatl/internal/adapter/mcpsecretfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

const mcpStaticBearerMode = "static_bearer"

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
// metadata and a stable category; looked-up secret values are never retained.
// Only safe file-read diagnostics are included in Reason.
type MCPProfileError struct {
	Server   string
	Field    string
	Ref      string
	Expected string
	Remedy   string
	Reason   string // Safe diagnostic for a referenced client-secret file, never its contents.
	Kind     error
}

func (e *MCPProfileError) Error() string {
	message := fmt.Sprintf("MCP server %q: %s: %s", e.Server, e.Field, e.Kind)
	if e.Reason != "" {
		message += "; " + e.Reason
	}
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
	Operator    *permconfig.MCPSection
	CLIServers  *MCPServerList
	LookupEnv   func(string) (string, bool)
	Secrets     SecretResolver
	StaticEnv   func(string) (string, bool)
	Diagnostics port.Diagnostics
	Warnings    *MCPWarnings
	// Native custody seams keep offline tests away from desktop keyrings and DBus.
	Keyring mcpcredential.Keyring
	// SelectedName is used by targeted lifecycle commands. An empty value keeps
	// the daemon's all-profile composition unchanged.
	SelectedName string
	// InitializeNativeCustody lets explicit login create missing native custody;
	// ordinary profile loading remains read-only.
	InitializeNativeCustody bool
}

// MCPProfileResolver binds CLI --mcp-server metadata and environment lookup to the
// canonical profile loader. app.Build supplies the operator subtree from its
// already-created settings resolver, avoiding a second YAML parse.
type MCPProfileResolver struct {
	cliServers  *MCPServerList
	lookup      func(string) (string, bool)
	source      *CredentialSource
	diagnostics port.Diagnostics
	warnings    MCPWarnings
}

// NewMCPProfileResolver constructs a side-effect-free resolver. Environment and
// credential stores are touched only when Load is called by composition.
func NewMCPProfileResolver(cliServers *MCPServerList, lookup func(string) (string, bool), source *CredentialSource) *MCPProfileResolver {
	return &MCPProfileResolver{cliServers: cliServers, lookup: lookup, source: source}
}

// SetDiagnostics installs the startup sink before loading, including failure paths.
func (r *MCPProfileResolver) SetDiagnostics(d port.Diagnostics) { r.diagnostics = d }

func (r *MCPProfileResolver) options(operator *permconfig.MCPSection) MCPProfileLoadOptions {
	opts := MCPProfileLoadOptions{Operator: operator, CLIServers: r.cliServers, LookupEnv: r.lookup, Diagnostics: r.diagnostics, Warnings: &r.warnings}
	if r.source != nil {
		opts.Secrets, opts.StaticEnv = r.source, r.source.LookupEnv
	}
	return opts
}

// MCPWarnings deduplicates safe explicit-token-environment selector warnings
// over a loader lifetime.
type MCPWarnings struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (w *MCPWarnings) warn(d port.Diagnostics, name string) {
	if d == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	key := strings.ToLower(name)
	if w.seen[key] {
		return
	}
	if w.seen == nil {
		w.seen = make(map[string]bool)
	}
	w.seen[key] = true
	d.Log(context.Background(), port.LevelWarn, "MCP token_env is deprecated; prefer token_ref with its conventional MECATL_MCP_TOKEN_<REF> environment name", "server", name, "field", "auth.static_bearer.token_env")
}

func mcpWarnings(w *MCPWarnings) *MCPWarnings {
	if w == nil {
		return &MCPWarnings{}
	}
	return w
}

// Load implements app.Config's MCP profile-loader capability without importing
// the composition package.
func (r *MCPProfileResolver) Load(operator *permconfig.MCPSection) ([]mcp.ServerConfig, interface{ Close() error }, error) {
	profiles, err := LoadMCPProfiles(r.options(operator))
	if err != nil {
		return nil, nil, err
	}
	return profiles.Servers, profiles, nil
}

// LoadAuthority resolves exactly one authority path without reparsing settings
// or constructing broker runtime resources.
func (r *MCPProfileResolver) LoadAuthority(operator *permconfig.MCPSection, defaultMode mcpauthority.Mode, brokerSupported bool) (*mcpauthority.Result, error) {
	opts := r.options(operator)
	return ResolveMCPAuthority(MCPAuthorityOptions{
		Operator: operator, CLIServers: r.cliServers, LookupEnv: r.lookup, Secrets: opts.Secrets, StaticEnv: opts.StaticEnv, Diagnostics: r.diagnostics, Warnings: &r.warnings,
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

// LoadMCPProfiles resolves the selected operator and CLI --mcp-server entries. Settings
// retain their order; a same-name CLI entry replaces the whole settings
// entry in place, and a distinct CLI entry appends.
func LoadMCPProfiles(opts MCPProfileLoadOptions) (*MCPProfiles, error) {
	if opts.Operator != nil && opts.Operator.Mode == "broker" {
		return nil, fmt.Errorf("%w: broker MCP authority cannot be loaded as direct profiles", ErrMCPProfileInvalid)
	}
	if err := validateCLIHTTPRelaxations(opts.CLIServers); err != nil {
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
	if opts.CLIServers != nil {
		for i := range opts.CLIServers.entries {
			entry := &opts.CLIServers.entries[i]
			replacement := profileInput{cliEntry: entry, relaxed: opts.CLIServers.relaxed(entry.cfg.Name), finalized: opts.CLIServers.finalized}
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
	if opts.SelectedName != "" {
		selected := profiles[:0]
		for _, input := range profiles {
			if strings.EqualFold(input.name(), opts.SelectedName) {
				selected = append(selected, input)
			}
		}
		profiles = selected
	}

	opts.Warnings = mcpWarnings(opts.Warnings)
	if err := prepareMCPStaticProfiles(profiles, opts); err != nil {
		return nil, err
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
	cliEntry  *mcpServerEntry
	relaxed   bool
	finalized bool
}

func (p profileInput) name() string {
	if p.operator != nil {
		return p.operator.Name
	}
	return p.cliEntry.cfg.Name
}

func (l *MCPServerList) relaxed(name string) bool {
	for _, candidate := range l.insecure {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

func validateCLIHTTPRelaxations(list *MCPServerList) error {
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
	if input.cliEntry != nil {
		entry := input.cliEntry
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
				return mcp.ServerConfig{}, &MCPProfileError{Server: cfg.Name, Field: "CLI bearer URL", Ref: entry.envName, Kind: ErrMCPProfileInvalid}
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
	case mcpStaticBearerMode:
		decl := profile.Auth.StaticBearer
		if decl.TokenRef != "" {
			return loadMCPStaticBearer(*profile, loadOpts.Secrets)
		}
		ref := decl.TokenEnv
		staticLookup := loadOpts.StaticEnv
		if staticLookup == nil {
			staticLookup = lookup
		}
		token, ok := lookupMCPEnv(staticLookup, ref)
		if !ok || !validMCPBearer(token) {
			return mcp.ServerConfig{}, &MCPProfileError{Server: profile.Name, Field: "auth.static_bearer.token_env", Ref: ref, Kind: ErrMCPProfileSecret, Expected: "a non-empty secret in the referenced MECATL_* environment variable", Remedy: "set the referenced environment variable before starting mecatl"}
		}
		return mcpBearerConfig(*profile, token)
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

func prepareMCPStaticProfiles(profiles []profileInput, opts MCPProfileLoadOptions) error {
	static := false
	for _, input := range profiles {
		if input.operator == nil {
			continue
		}
		profile := input.operator
		switch profile.Auth.Mode {
		case "none", "oauth":
		case mcpStaticBearerMode:
			if err := validateMCPStaticBearer(*profile); err != nil {
				return err
			}
			static = true
		default:
			return &MCPProfileError{Server: profile.Name, Field: "auth.mode", Kind: ErrMCPProfileInvalid}
		}
	}
	// Warn for every effective valid selector before any credential can fail.
	for _, input := range profiles {
		if p := input.operator; p != nil && p.Auth.Mode == mcpStaticBearerMode && p.Auth.StaticBearer.TokenEnv != "" {
			opts.Warnings.warn(opts.Diagnostics, p.Name)
		}
	}
	if static {
		if source, ok := opts.Secrets.(interface{ prepareStatic() }); ok {
			source.prepareStatic()
		}
	}
	return nil
}

func validateMCPStaticBearer(profile permconfig.MCPServerProfile) error {
	decl := profile.Auth.StaticBearer
	field := "static_bearer"
	switch {
	case decl == nil:
	case (decl.TokenRef == "") == (decl.TokenEnv == ""):
	case decl.TokenRef != "":
		if staticSecretReference.MatchString(decl.TokenRef) {
			return nil
		}
		field = "auth.static_bearer.token_ref"
	default:
		if explicitTokenEnvPattern.MatchString(decl.TokenEnv) {
			return nil
		}
		field = "auth.static_bearer.token_env"
	}
	return &MCPProfileError{Server: profile.Name, Field: field, Kind: ErrMCPProfileInvalid}
}

func loadMCPStaticBearer(profile permconfig.MCPServerProfile, resolver SecretResolver) (mcp.ServerConfig, error) {
	ref := profile.Auth.StaticBearer.TokenRef
	secretError := &MCPProfileError{Server: profile.Name, Field: "auth.static_bearer.token_ref", Kind: ErrMCPProfileSecret}
	if resolver == nil {
		secretError.Kind = ErrMCPProfileStore
		return mcp.ServerConfig{}, secretError
	}
	token, err := resolver.Lookup(MCPBearerSecret, ref)
	if err != nil {
		if errors.Is(err, ErrSecretSource) {
			secretError.Kind = ErrMCPProfileStore
		} else if errors.Is(err, ErrSecretReference) {
			secretError.Kind = ErrMCPProfileInvalid
		}
		return mcp.ServerConfig{}, secretError
	}
	if !validMCPBearer(token) {
		return mcp.ServerConfig{}, secretError
	}
	return mcpBearerConfig(profile, token)
}

func validMCPBearer(token string) bool {
	if strings.TrimSpace(token) == "" || strings.HasSuffix(token, " ") || strings.HasSuffix(token, "\t") {
		return false
	}
	for i := 0; i < len(token); i++ {
		if (token[i] < 0x20 && token[i] != '\t') || token[i] == 0x7f {
			return false
		}
	}
	return true
}

func mcpBearerConfig(profile permconfig.MCPServerProfile, token string) (mcp.ServerConfig, error) {
	if err := mcp.ValidateClientURL(profile.URL); err != nil {
		return mcp.ServerConfig{}, &MCPProfileError{Server: profile.Name, Field: "static bearer URL", Kind: ErrMCPProfileInvalid}
	}
	return mcp.ServerConfig{Name: profile.Name, URL: profile.URL, Headers: map[string]string{"Authorization": "Bearer " + token}}, nil
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

func nativeMCPCustodyRemedy(server string, err error) string {
	command := fmt.Sprintf("`mecated mcp reset-local-credentials %s`", server)
	switch {
	case errors.Is(err, mcpcredential.ErrResetInProgress):
		return "rerun " + command + " to finish custody reset"
	case errors.Is(err, mcpcredential.ErrObsoleteMarker), errors.Is(err, mcpcredential.ErrPendingArtifactMismatch), errors.Is(err, mcpcredential.ErrArtifactMissing), errors.Is(err, mcpcredential.ErrArtifactMismatch):
		return "run " + command + " to reset local custody, then log in again"
	default:
		return ""
	}
}

func loadNativeOAuthProfile(server string, local *permconfig.MCPLocalCredentialProfile, loadOpts MCPProfileLoadOptions, cacheKey string, owner *MCPProfiles, stores map[string]credentialstore.Store, opts *mcp.OAuthOptions) (*mcp.OAuthOptions, error) {
	filePath := ""
	if local.Key.File != nil {
		filePath = local.Key.File.Path
	}
	var selected mcpcredential.Selection
	var err error
	if loadOpts.InitializeNativeCustody {
		selected, err = mcpcredential.Resolve(context.Background(), local.Root, mcpcredential.Options{
			Requested: local.Key.Mode, FilePath: filePath, Keyring: loadOpts.Keyring,
		})
	} else {
		selected, err = mcpcredential.Open(context.Background(), local.Root, local.Key.Mode, filePath, loadOpts.Keyring)
	}
	if err != nil {
		return nil, &MCPProfileError{
			Server: server, Field: "auth.oauth.credentials.local.key", Kind: ErrMCPProfileStore,
			Remedy: nativeMCPCustodyRemedy(server, err),
		}
	}
	store, err := credentialstore.NewEncryptedFile(local.Root, mcpcredential.NativeNamespace, selected.Key)
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
		var secret string
		if client.SecretEnv != "" {
			var ok bool
			secret, ok = lookupMCPEnv(lookup, client.SecretEnv)
			if !ok || secret == "" {
				return &MCPProfileError{Server: profile.Name, Field: "auth.oauth.client.preregistered.secret_env", Ref: client.SecretEnv, Kind: ErrMCPProfileSecret, Expected: "a non-empty secret in the referenced MECATL_* environment variable", Remedy: "set the referenced environment variable before starting mecatl"}
			}
		} else {
			var err error
			secret, err = mcpsecretfile.Read(client.SecretFile)
			if err != nil {
				return &MCPProfileError{Server: profile.Name, Field: "auth.oauth.client.preregistered.secret_file", Ref: client.SecretFile, Kind: ErrMCPProfileSecret, Reason: err.Error(), Remedy: "mount a readable, non-empty regular client secret file before starting mecatl"}
			}
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
