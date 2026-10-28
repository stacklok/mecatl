package cliconfig

import (
	"errors"
	"regexp"
	"strings"
	"sync"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// SecretKind is a closed static-credential namespace. OAuth has separate custody.
type SecretKind string

const (
	// MCPBearerSecret identifies static MCP bearer credentials.
	MCPBearerSecret SecretKind = "mcp_bearer"
	// SearchAPIKeySecret identifies optional search API keys.
	SearchAPIKeySecret SecretKind = "search_api_key"
)

var (
	// ErrSecretReference reports an invalid static secret reference.
	ErrSecretReference = errors.New("invalid static secret reference")
	// ErrSecretSource reports an unavailable static credential source.
	ErrSecretSource         = errors.New("static credential source unavailable")
	explicitTokenEnvPattern = regexp.MustCompile(`^MECATL_[A-Z0-9_]+$`)
	staticSecretReference   = regexp.MustCompile(`^[a-z0-9_]+$`)
)

var searchAPIKeyAliases = map[string]string{
	"exa":       envExaKey,
	"brave":     envBraveKey,
	"websearch": envWebSearchKey,
}

// SecretResolver exposes logical static credentials, never storage paths.
// It is a startup-time lookup boundary for search API keys and static MCP bearer
// credentials, not a Mecatl-wide secret store: it provides no writes, OAuth
// lifecycle, or key custody.
// Shared explicit and system-managed secret storage is tracked in
// https://github.com/stacklok/mecatl/issues/2585.
type SecretResolver interface {
	// Lookup returns an opaque value, or "" when no credential is present.
	// Callers enforce requiredness and format; errors identify invalid references
	// or rejected file fallback, not missing credentials.
	Lookup(SecretKind, string) (string, error)
}

// CredentialSource owns path provenance, the shared auth.yaml snapshot and the
// captured static environment; it does not use keyring or external-store backends.
// Preliminary provider discovery may acquire the file; settings can reselect it
// only before the authoritative provider/MCP view is finalized.
type CredentialSource struct {
	mu            sync.Mutex
	env           xdgconfig.ResolveEnv
	staticEnv     map[string]string
	cliPath       string
	path          string
	explicit      bool
	finalized     bool
	reselected    bool
	file          *authfile.File
	ready         bool
	warning       string
	strictWarning string
}

// NewCredentialSource reuses a preliminary provider acquisition when available.
// Canonical token environment values and absence are captured now; provider keys
// and search aliases retain their environment lookup at resolution time.
func NewCredentialSource(flags *ProviderFlags, preliminary ResolvedCredentials) *CredentialSource {
	if preliminary.source != nil {
		return preliminary.source
	}
	return newCredentialSource(flags, xdgconfig.OSEnv)
}

func newCredentialSource(flags *ProviderFlags, env xdgconfig.ResolveEnv) *CredentialSource {
	values := make(map[string]string)
	if env.Environ != nil {
		for _, entry := range env.Environ() {
			if key, value, ok := strings.Cut(entry, "="); ok && explicitTokenEnvPattern.MatchString(key) {
				values[key] = value
			}
		}
	}
	cli := ""
	if flags != nil {
		cli = value(flags.authFile)
	}
	path := cli
	if path == "" {
		path = authfile.DefaultPath(env)
	}
	return &CredentialSource{env: env, staticEnv: values, cliPath: cli, path: path, explicit: cli != ""}
}

// AuthFilePath reports the selected path and explicit-source policy, never values.
func (s *CredentialSource) AuthFilePath() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, s.explicit
}

// SetAPIKeyFile selects the settings path before authoritative use. CLI wins.
func (s *CredentialSource) SetAPIKeyFile(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalized || s.cliPath != "" {
		return
	}
	explicit := path != ""
	if path == "" {
		path = authfile.DefaultPath(s.env)
	}
	if s.path != path {
		s.reselected = true
		s.path, s.explicit = path, explicit
		s.file, s.ready, s.warning, s.strictWarning = nil, false, "", ""
	} else if s.explicit != explicit {
		s.explicit, s.reselected = explicit, true
		// Explicit selection makes a cached conventional absence fatal without rereading.
		if explicit && s.ready && s.file == nil && s.warning == "" {
			s.warning = "auth file " + path + ": unavailable"
			s.strictWarning = s.warning
		}
	}
}

func (s *CredentialSource) acquire() {
	if !s.ready {
		s.file, s.warning = authfile.Load(s.path, s.explicit, s.env, nil)
		s.strictWarning = s.warning
		s.ready = true
	}
}

// LookupEnv returns the startup static environment, including stable absence.
// Raw OAuth environment lookup intentionally remains outside this capability.
func (s *CredentialSource) LookupEnv(name string) (string, bool) {
	value, ok := s.staticEnv[name]
	return value, ok
}

// prepareStatic freezes the fallback, including absence and load failures, even
// when the first selected static credential comes from the environment.
func (s *CredentialSource) prepareStatic() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalized = true
	s.acquire()
}

// Lookup resolves a static secret by kind and logical reference.
func (s *CredentialSource) Lookup(kind SecretKind, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lookup := credentialLookup{source: s, env: s.lookupCredentialEnv}
	secret, err := lookup.lookupLocked(kind, ref)
	if err != ErrSecretReference {
		s.finalized = true
	}
	return secret, err
}

// credentialLookup is a lock-held view of a source and its resolution-time environment.
type credentialLookup struct {
	source *CredentialSource
	env    func(string) (string, bool)
}

// providerLookupLocked captures all search aliases before any file acquisition.
func (s *CredentialSource) providerLookupLocked() credentialLookup {
	aliases := make(map[string]string, len(searchAPIKeyAliases))
	for _, name := range searchAPIKeyAliases {
		aliases[name] = s.env.Getenv(name)
	}
	return credentialLookup{source: s, env: func(name string) (string, bool) {
		if value, ok := aliases[name]; ok {
			return value, value != ""
		}
		return s.lookupCredentialEnv(name)
	}}
}

func (s *CredentialSource) lookupCredentialEnv(name string) (string, bool) {
	if strings.HasPrefix(name, "MECATL_") {
		return s.LookupEnv(name)
	}
	value := s.env.Getenv(name)
	return value, value != ""
}

func lookupSecretEnv(kind SecretKind, ref string, lookupEnv func(string) (string, bool)) (string, bool) {
	name := secretTokenEnv(kind, ref)
	if name == "" {
		return "", false
	}
	if token, ok := lookupEnv(name); ok {
		return token, true
	}
	if kind == SearchAPIKeySecret {
		if alias := searchAPIKeyEnv(ref); alias != "" {
			token, _ := lookupEnv(alias)
			return token, token != ""
		}
	}
	return "", false
}

// lookupLocked shares source policy without finalizing preliminary discovery
// or recursively locking final resolution.
func (l credentialLookup) lookupLocked(kind SecretKind, ref string) (string, error) {
	if secretTokenEnv(kind, ref) == "" {
		return "", ErrSecretReference
	}
	token, present := lookupSecretEnv(kind, ref, l.env)
	s := l.source
	s.acquire()
	if present {
		return token, nil
	}
	if s.strictWarning != "" {
		return "", ErrSecretSource
	}
	if kind == SearchAPIKeySecret {
		return s.file.SearchAPIKey(ref), nil
	}
	return s.file.MCPAPIKey(ref), nil
}

func secretTokenEnv(kind SecretKind, ref string) string {
	if !staticSecretReference.MatchString(ref) {
		return ""
	}
	var namespace string
	switch kind {
	case MCPBearerSecret:
		namespace = "MCP"
	case SearchAPIKeySecret:
		namespace = "SEARCH"
	default:
		return ""
	}
	return "MECATL_" + namespace + "_TOKEN_" + strings.ToUpper(ref)
}

func searchAPIKeyEnv(ref string) string {
	return searchAPIKeyAliases[ref]
}
