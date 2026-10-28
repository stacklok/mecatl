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

// MCPBearerSecret identifies static MCP bearer credentials.
const MCPBearerSecret SecretKind = "mcp_bearer"

var (
	// ErrSecretReference reports an invalid static secret reference.
	ErrSecretReference = errors.New("invalid static secret reference")
	// ErrSecretUnavailable reports a missing or invalid static secret.
	ErrSecretUnavailable = errors.New("static secret unavailable")
	// ErrSecretSource reports an unavailable static credential source.
	ErrSecretSource         = errors.New("static credential source unavailable")
	explicitTokenEnvPattern = regexp.MustCompile(`^MECATL_[A-Z0-9_]+$`)
	mcpSecretReference      = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// SecretResolver exposes logical static credentials, never storage paths.
type SecretResolver interface {
	Lookup(SecretKind, string) (string, error)
}

// CredentialSource owns path provenance, the shared file and the captured static
// environment. Preliminary provider discovery may acquire the file; settings can
// reselect it only before the authoritative provider/MCP view is finalized.
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
// Environment values and absence are captured now, not at each credential lookup.
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
	if kind != MCPBearerSecret || !mcpSecretReference.MatchString(ref) {
		return "", ErrSecretReference
	}
	s.prepareStatic()
	s.mu.Lock()
	defer s.mu.Unlock()
	if token, ok := s.LookupEnv("MECATL_MCP_TOKEN_" + strings.ToUpper(ref)); ok {
		if !validMCPBearer(token) {
			return "", ErrSecretUnavailable
		}
		return token, nil
	}
	if s.strictWarning != "" || s.file == nil {
		return "", ErrSecretSource
	}
	token := s.file.MCPToken(ref)
	if !validMCPBearer(token) {
		return "", ErrSecretUnavailable
	}
	return token, nil
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
