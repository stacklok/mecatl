// Package permconfig is the file-based permission-config adapter (issue #13). It
// loads `.mecatl/settings.yaml`-style permission rules from disk and from
// Claude-Code-compatible `settings.json`, and resolves them PER SESSION against
// each session's workspace root via a permpolicy.RuleResolver.
//
// # What it produces
//
// Every loaded entry becomes a governance.Rule tagged with a config Scope
// (project vs user) and an Audience (issue #32): top-level allow/ask bind the
// MAIN engine only, the `subagent:` block binds child engines only, and
// top-level deny binds BOTH (a deny only tightens). The Resolver hands those
// rules to the policy on the SAME lowest-scope `extra` channel the learned-rule
// store rides — so a config rule never out-ranks a static/configured deny or
// ask, and a config Allow can only LOOSEN the built-in Ask floor
// (ScopeBuiltinDefault), never a deny.
//
// # The trust gate
//
// Project-level config is part of the repository the model is editing, so its
// ALLOW rules are gated behind a trust flag: an UNTRUSTED project's allows are
// dropped (the rule reverts to the built-in Ask/whatever a higher scope says),
// while its DENY and ASK rules are ALWAYS honoured (they only tighten). User-
// global config (under the user's XDG dir / home) is the operator's own and is
// always fully trusted. See Resolver.
//
// # Layering
//
// permconfig is an ADAPTER: it reads the filesystem (project files via the
// session tool.Workspace, user-global files via an injectable env), parses YAML/
// JSON, and emits domain governance.Rule values. It implements
// permpolicy.RuleResolver and is wired in internal/app. It imports no other
// adapter and is never imported by the domain/port/agent layers.
package permconfig

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	yaml "go.yaml.in/yaml/v3"

	"github.com/stacklok/mecatl/engine/learning"
)

// MaxContextWindowTokens is the sane upper bound for configured and live model
// context windows. It is deliberately shared with composition's live metadata
// validation so either source cannot disable compaction with an absurd value.
const MaxContextWindowTokens = 2_000_000

// Config is the on-disk `.mecatl/settings.yaml` (or user-global settings.yaml)
// schema for file-based permissions. It is intentionally a small mirror of the
// Claude-Code permissions shape so a user familiar with one can read the other.
//
// Each list entry is a RULE SPEC string of the form "Tool(pattern)" or bare
// "Tool" (tool-wide). For Bash the pattern is a command glob, e.g.
// "Bash(go test*)". See parseSpec / normalizeGlob for the exact grammar.
//
// The TOP level of Config stays LENIENT (other keys — trustedWorkspaces etc. —
// must keep parsing); strictness applies only INSIDE the permissions: subtree,
// where a typo'd key would silently disable a rule list (see the custom
// UnmarshalYAML on Permissions / SubagentPermissions).
type Config struct {
	// Permissions holds the allow/ask/deny rule-spec lists plus the child-scoped
	// `subagent:` block.
	Permissions Permissions `yaml:"permissions"`
	// Guardrails holds the OPERATOR-TIER LLM-content-checker config (issue #27). It
	// is parsed STRICTLY (unknown sub-keys = error, like permissions:) so a typo
	// cannot silently disable a guardrail. It is honoured ONLY from the user-global +
	// CLI tiers; a project-tier file's guardrails: block is IGNORED with a WARN (a
	// project repo weakening/disabling a checker is a security DOWNGRADE — the usual
	// tighten-only gate reverses here). The presence flag (whether the key appeared at
	// all) is tracked via GuardrailsPresent so the resolver can WARN about an ignored
	// project block. A nil Guardrails means the key was absent.
	Guardrails *GuardrailsSection `yaml:"guardrails"`
	// Posture is the OPERATOR-TIER posture-ladder scalar (the graduated trust/
	// automation tier: strict/trusted/auto/yolo). Like Guardrails it is honoured ONLY
	// from the user-global + CLI tiers; a project-tier file's posture: key is IGNORED
	// with a WARN (a project repo RAISING the automation posture — e.g. posture: yolo
	// — is a security DOWNGRADE the tighten-only project gate forbids, the fail-closed
	// core of this feature). Empty = absent (the resolver returns "" and composition
	// keeps the CLI/default). The composition layer parses the string; permconfig only
	// reads the scalar.
	Posture string `yaml:"posture"`
	// Models holds the per-slot model config (ADR 0030): the `models.slots` /
	// `models.aliases` maps, the session `default`, and the operator-tier `allowlist`
	// cap. At the OPERATOR tier (user-global + CLI) all fields are honoured. At the
	// PROJECT tier (Phase 4) a models: block is honoured WITHIN the operator allowlist
	// on a TRUSTED workspace (slots/aliases/default only); with no operator allowlist
	// it stays WARN-ignored (the opt-in — byte-identical to pre-Phase-4), and a
	// project-tier allowlist: key is always ignored with a WARN (non-wideable cap).
	// The TOP `models:` mapping is parsed STRICTLY (an unknown key like `slotz:`
	// errors), the inner slots/aliases maps stay free-form (composition validates the
	// slot keys fail-soft). A nil Models means the key was absent. The composition
	// layer reads the maps; permconfig only carries them.
	Models *ModelsSection `yaml:"models"`
	// ReasoningEffort is the OPERATOR-TIER reasoning-effort scalar (ADR 0055: the
	// neutral vocabulary "" / "auto" / "low" / "medium" / "high" / "xhigh" / "max").
	// Like Posture it is honoured ONLY from the user-global + CLI tiers; a
	// project-tier file's reasoning-effort: key is IGNORED with a WARN
	// (operator-tier only, for consistency — a project cannot raise the model's
	// reasoning spend). Empty = absent (the resolver returns "" and composition uses
	// the provider default). The composition layer interprets + clamps the token;
	// permconfig only reads the scalar.
	ReasoningEffort string `yaml:"reasoning-effort"`
	// PlanModeAutoApprove is the OPERATOR-TIER plan-mode-auto-approve flag (issue
	// #206 Wave 6a). Like Posture/ReasoningEffort it is honoured ONLY
	// from the user-global + CLI tiers; a project-tier file's plan-mode-auto-approve:
	// key is IGNORED with a WARN (operator-tier only — a project repo enabling
	// autonomous plan approval is a security DOWNGRADE). false = absent (the resolver
	// returns false and composition keeps the default OFF). The composition layer
	// interprets the bool; permconfig only reads the scalar.
	PlanModeAutoApprove bool `yaml:"plan-mode-auto-approve"`
	// Learning configures optional completed-trajectory observation. The subtree is
	// strict; composition parses the closed off/review/auto mode vocabulary.
	Learning *LearningSection `yaml:"learning"`
	// OpenRouter holds the OPERATOR-TIER OpenRouter downstream-provider routing
	// config (issue #480): a per-model preferred DOWNSTREAM provider order, sent as
	// OpenRouter's `provider` request-body object. Like Guardrails/Posture it is
	// honoured ONLY from the user-global + CLI tiers; a project-tier file's
	// openrouter: block is IGNORED with a WARN (steering requests to a particular
	// downstream is a spend/compliance/capability decision the operator owns — the
	// same operator-only discipline as default_provider/allowlist/router). It is
	// parsed STRICTLY (unknown sub-keys = error, like guardrails:/router:) so a
	// typo cannot silently drop a routing preference. A nil OpenRouter means the
	// key was absent. The composition layer reads + validates the maps; permconfig
	// only carries them.
	OpenRouter *OpenRouterSection `yaml:"openrouter"`
	// MCP holds named global Streamable HTTP MCP server profiles. It is strict and
	// OPERATOR-TIER ONLY: project files cannot choose endpoints, authentication,
	// credential references, or egress policy. Values are metadata only; parsing
	// never reads an environment variable, opens a credential store, or performs I/O.
	MCP *MCPSection `yaml:"mcp"`
	// Retention is the strict, versioned operator-only automatic session cleanup policy.
	// Project-tier values are ignored; explicit CLI flags remain the highest precedence.
	Retention *RetentionSection `yaml:"retention"`
	// StorageManagement names the verified OIDC identities allowed to operate on
	// process-wide storage. It is strict and operator-tier only.
	StorageManagement *StorageManagementSection `yaml:"storage_management"`
}

// StorageManagementSection is the explicit operator authority for process-wide
// storage health, migration, and cleanup.
type StorageManagementSection struct {
	// Version is the required schema version; the only supported value is 1.
	Version int `yaml:"version"`
	// Principals lists exact verified OIDC issuer/subject pairs. Empty grants nobody.
	Principals []StorageManagementPrincipal `yaml:"principals"`
}

// StorageManagementPrincipal is one exact verified issuer/subject pair.
type StorageManagementPrincipal struct {
	// Issuer must equal the verified token issuer byte-for-byte.
	Issuer string `yaml:"issuer"`
	// Subject must equal the verified token subject byte-for-byte.
	Subject string `yaml:"subject"`
}

// UnmarshalYAML strictly validates storage-management authority. An empty list
// grants nobody; there is no wildcard or grant-type shortcut.
func (s *StorageManagementSection) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "storage_management", map[string]any{
		"version": &s.Version, "principals": &s.Principals,
	}); err != nil {
		return err
	}
	if s.Version != 1 {
		return fmt.Errorf("storage_management.version: want 1, got %d", s.Version)
	}
	seen := make(map[string]bool, len(s.Principals))
	for i := range s.Principals {
		p := &s.Principals[i]
		p.Issuer, p.Subject = strings.TrimSpace(p.Issuer), strings.TrimSpace(p.Subject)
		if p.Issuer == "" || p.Subject == "" {
			return fmt.Errorf("storage_management.principals[%d]: issuer and subject are required", i)
		}
		key := p.Issuer + "\x00" + p.Subject
		if seen[key] {
			return fmt.Errorf("storage_management.principals[%d]: duplicate issuer/subject", i)
		}
		seen[key] = true
	}
	return nil
}

// UnmarshalYAML keeps each principal mapping closed to prevent a misspelled
// identity field from silently removing the management boundary.
func (p *StorageManagementPrincipal) UnmarshalYAML(node *yaml.Node) error {
	return decodeStrictMapping(node, "storage_management principal", map[string]any{
		"issuer": &p.Issuer, "subject": &p.Subject,
	})
}

// RetentionSection is the versioned operator automatic-cleanup policy.
type RetentionSection struct {
	// Version is the required schema version; the only supported value is 1.
	Version int `yaml:"version"`
	// Main controls top-level operator/service sessions.
	Main RetentionLimitSection `yaml:"main"`
	// Child controls subagent, parallel-branch, and team-member sessions.
	Child RetentionLimitSection `yaml:"child"`
	// Scheduled controls scheduled-fire sessions.
	Scheduled RetentionLimitSection `yaml:"scheduled"`
	// SweepCadence is the repeat interval; 0 disables repeats while retaining the compatibility startup sweep.
	SweepCadence string `yaml:"sweep_cadence"`
	// AcknowledgeMainDeletion explicitly consents to destructive main-session cleanup.
	AcknowledgeMainDeletion bool `yaml:"acknowledge_main_deletion"`
	SweepCadenceSet         bool `yaml:"-"`
}

// RetentionLimitSection controls one durable session-kind partition. Zero disables.
type RetentionLimitSection struct {
	// MaxAge deletes eligible rows older than this Go duration; 0 disables the age limit.
	MaxAge string `yaml:"max_age"`
	// MaxCount keeps the newest eligible rows up to this count; 0 disables the count limit.
	MaxCount               int  `yaml:"max_count"`
	MaxAgeSet, MaxCountSet bool `yaml:"-"`
}

// UnmarshalYAML strictly decodes and validates the versioned retention policy.
func (s *RetentionSection) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "retention", map[string]any{
		"version": &s.Version, "main": &s.Main, "child": &s.Child,
		"scheduled": &s.Scheduled, "sweep_cadence": &s.SweepCadence,
		"acknowledge_main_deletion": &s.AcknowledgeMainDeletion,
	}); err != nil {
		return err
	}
	if s.Version != 1 {
		return fmt.Errorf("retention.version: want 1, got %d", s.Version)
	}
	s.SweepCadenceSet = mappingHasKey(node, "sweep_cadence")
	for name, value := range map[string]RetentionLimitSection{"main": s.Main, "child": s.Child, "scheduled": s.Scheduled} {
		if value.MaxCount < 0 {
			return fmt.Errorf("retention.%s.max_count: must be non-negative", name)
		}
		if err := validateRetentionDuration(value.MaxAge); err != nil {
			return fmt.Errorf("retention.%s.max_age: %w", name, err)
		}
	}
	if err := validateRetentionDuration(s.SweepCadence); err != nil {
		return fmt.Errorf("retention.sweep_cadence: %w", err)
	}
	return nil
}

// UnmarshalYAML strictly decodes one retention partition.
func (s *RetentionLimitSection) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "retention limit", map[string]any{"max_age": &s.MaxAge, "max_count": &s.MaxCount}); err != nil {
		return err
	}
	s.MaxAgeSet, s.MaxCountSet = mappingHasKey(node, "max_age"), mappingHasKey(node, "max_count")
	return nil
}

func validateRetentionDuration(raw string) error {
	_, err := ParseRetentionDuration(raw)
	return err
}

// ParseRetentionDuration parses a retention/sweep-cadence duration string.
// An empty or "0" value means disabled (0, nil); anything else must be a
// valid, non-negative time.ParseDuration value.
func ParseRetentionDuration(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("must be non-negative")
	}
	return d, nil
}

// MCPSection is the strict operator-only mcp: subtree.
type MCPSection struct {
	// Servers is the ordered list of named global Streamable HTTP servers.
	Servers []MCPServerProfile `yaml:"servers"`
}

// MCPServerProfile is one named Streamable HTTP endpoint and its explicit auth mode.
type MCPServerProfile struct {
	// Name is an ASCII [A-Za-z0-9_]+ identifier, unique case-insensitively.
	Name string `yaml:"name"`
	// URL is an absolute HTTP(S) endpoint without userinfo or a fragment.
	URL string `yaml:"url"`
	// Auth selects exactly one of none, static_bearer, or oauth.
	Auth MCPAuthProfile `yaml:"auth"`
}

// MCPAuthProfile is a closed tagged union. none has no payload; the other modes
// require exactly their matching payload and reject cross-variant fields.
type MCPAuthProfile struct {
	// Mode is exactly none, static_bearer, or oauth.
	Mode string `yaml:"mode"`
	// StaticBearer names the bearer-token environment reference.
	StaticBearer *MCPStaticBearerProfile `yaml:"static_bearer"`
	// OAuth declares the OAuth identity, client, credentials, scopes, and network policy.
	OAuth *MCPOAuthProfile `yaml:"oauth"`
}

// MCPStaticBearerProfile contains a reference only, never a bearer-token value.
type MCPStaticBearerProfile struct {
	// TokenEnv is a MECATL_* environment variable name containing the opaque token.
	TokenEnv string `yaml:"token_env"`
}

// MCPOAuthProfile is the metadata-only OAuth configuration for one server.
type MCPOAuthProfile struct {
	// Profile is the required operator-defined credential identity profile.
	Profile string `yaml:"profile"`
	// Principal is the required operator-defined credential identity principal.
	Principal string `yaml:"principal"`
	// Issuer is the required canonical exact HTTP(S) origin of the authorization server.
	Issuer string `yaml:"issuer"`
	// Client selects exactly one preregistered or CIMD client declaration.
	Client MCPOAuthClientProfile `yaml:"client"`
	// Scopes is the non-empty allowlist of OAuth scopes the client may request.
	Scopes []string `yaml:"scopes"`
	// RequestRefreshToken asks the authorization server for refresh capability.
	RequestRefreshToken bool `yaml:"request_refresh_token"`
	// Credentials selects exactly one local or environment credential source.
	Credentials MCPOAuthCredentialProfile `yaml:"credentials"`
	// Network is required and declares immutable exact-origin egress policy.
	Network *MCPOAuthNetworkProfile `yaml:"network"`
}

// MCPOAuthClientProfile is a closed preregistered/CIMD tagged union. DCR is unsupported.
type MCPOAuthClientProfile struct {
	// Mode is exactly preregistered or cimd.
	Mode string `yaml:"mode"`
	// Preregistered declares a confidential client registered with the issuer.
	Preregistered *MCPPreregisteredClientProfile `yaml:"preregistered"`
	// CIMD declares an HTTPS client-id metadata document URL.
	CIMD *MCPCIMDClientProfile `yaml:"cimd"`
}

// MCPPreregisteredClientProfile contains client identity metadata and a secret reference.
type MCPPreregisteredClientProfile struct {
	// ID is the required preregistered OAuth client identifier.
	ID string `yaml:"id"`
	// SecretEnv is a MECATL_* environment variable name containing the client secret.
	SecretEnv string `yaml:"secret_env"`
}

// MCPCIMDClientProfile contains the HTTPS client-id metadata document URL.
type MCPCIMDClientProfile struct {
	// DocumentURL is the required HTTPS metadata-document URL.
	DocumentURL string `yaml:"document_url"`
}

// MCPOAuthCredentialProfile is a closed local/environment tagged union.
type MCPOAuthCredentialProfile struct {
	// Mode is exactly local or environment.
	Mode string `yaml:"mode"`
	// Local declares encrypted mutable credentials rooted at an absolute path.
	Local *MCPLocalCredentialProfile `yaml:"local"`
	// Environment declares one externally provisioned read-only credential record.
	Environment *MCPEnvironmentCredentialProfile `yaml:"environment"`
}

// MCPLocalCredentialProfile declares local encrypted credential persistence metadata.
type MCPLocalCredentialProfile struct {
	// Root is the required absolute credential-store root.
	Root string `yaml:"root"`
	// KeyEnv is a MECATL_* environment variable name containing the encryption key.
	KeyEnv string `yaml:"key_env"`
}

// MCPEnvironmentCredentialProfile declares a read-only environment credential source.
type MCPEnvironmentCredentialProfile struct {
	// CredentialEnv is a MECATL_* environment variable containing the opaque credential record.
	CredentialEnv string `yaml:"credential_env"`
	// AllowProcessLocalRefresh permits refreshed credentials to live only in this process.
	AllowProcessLocalRefresh bool `yaml:"allow_process_local_refresh"`
}

// MCPOAuthNetworkProfile is the required immutable OAuth egress policy.
type MCPOAuthNetworkProfile struct {
	// AdditionalOrigins lists canonical exact origins additionally allowed for OAuth traffic.
	AdditionalOrigins []string `yaml:"additional_origins"`
	// PrivateOrigins lists allowed origins that may resolve only to RFC1918 IPv4 or ULA IPv6 addresses. Loopback, link-local, metadata, unspecified, multicast, mapped, public, and other special addresses remain denied.
	PrivateOrigins []string `yaml:"private_origins"`
	// MaxRedirects is the redirect bound, from zero through five.
	MaxRedirects int `yaml:"max_redirects"`
}

var (
	mcpProfileName        = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	mecatlSecretReference = regexp.MustCompile(`^MECATL_[A-Z0-9_]+$`)
)

const modeKey = "mode"

func (s *MCPSection) strictFields() map[string]any { return map[string]any{"servers": &s.Servers} }

// UnmarshalYAML strictly decodes and validates an MCP operator section.
func (s *MCPSection) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp", s.strictFields()); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(s.Servers))
	for i := range s.Servers {
		name := strings.ToLower(s.Servers[i].Name)
		if _, ok := seen[name]; ok {
			return fmt.Errorf("mcp.servers[%d].name: duplicate case-insensitive server name", i)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func (s *MCPServerProfile) strictFields() map[string]any {
	return map[string]any{"name": &s.Name, "url": &s.URL, "auth": &s.Auth}
}

// UnmarshalYAML strictly decodes and validates one MCP server profile.
func (s *MCPServerProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[]", s.strictFields()); err != nil {
		return err
	}
	if !mappingHasKey(node, "auth") {
		return errors.New("mcp.servers[].auth is required")
	}
	if !mcpProfileName.MatchString(s.Name) || strings.Contains(s.Name, "__") {
		return errors.New("mcp.servers[].name must match [A-Za-z0-9_]+ and must not contain __")
	}
	u, err := validateMCPHTTPURL("mcp.servers[].url", s.URL, false)
	if err != nil {
		return err
	}
	if s.Auth.Mode != "none" && u.Scheme != "https" && !mcpLoopback(u.Hostname()) {
		return errors.New("mcp.servers[].url must use https for authenticated profiles except loopback http")
	}
	if s.Auth.OAuth != nil {
		allowed := map[string]struct{}{mcpURLOrigin(u): {}, s.Auth.OAuth.Issuer: {}}
		for _, origin := range s.Auth.OAuth.Network.AdditionalOrigins {
			allowed[origin] = struct{}{}
		}
		if client := s.Auth.OAuth.Client.CIMD; client != nil {
			document, _ := url.Parse(client.DocumentURL)
			if _, ok := allowed[mcpURLOrigin(document)]; !ok {
				return errors.New("mcp.servers[].auth.oauth.client.cimd.document_url origin must match the issuer or resource origin, or appear in mcp.servers[].auth.oauth.network.additional_origins")
			}
		}
		for _, origin := range s.Auth.OAuth.Network.PrivateOrigins {
			if _, ok := allowed[origin]; !ok {
				return errors.New("mcp.servers[].auth.oauth.network.private_origins[] must also be an issuer, resource, or additional origin")
			}
		}
	}
	return nil
}

func (a *MCPAuthProfile) strictFields() map[string]any {
	return map[string]any{modeKey: &a.Mode, "static_bearer": &a.StaticBearer, "oauth": &a.OAuth}
}

// UnmarshalYAML strictly decodes the closed MCP authentication union.
func (a *MCPAuthProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth", a.strictFields()); err != nil {
		return err
	}
	switch a.Mode {
	case "none":
		if mappingHasKey(node, "static_bearer") || mappingHasKey(node, "oauth") {
			return errors.New("mcp.servers[].auth: none must not contain a variant payload")
		}
	case "static_bearer":
		if a.StaticBearer == nil || mappingHasKey(node, "oauth") {
			return errors.New("mcp.servers[].auth: static_bearer requires only static_bearer payload")
		}
	case "oauth":
		if a.OAuth == nil || mappingHasKey(node, "static_bearer") {
			return errors.New("mcp.servers[].auth: oauth requires only oauth payload")
		}
	default:
		return errors.New("mcp.servers[].auth.mode must be exactly none, static_bearer, or oauth")
	}
	return nil
}

func (s *MCPStaticBearerProfile) strictFields() map[string]any {
	return map[string]any{"token_env": &s.TokenEnv}
}

// UnmarshalYAML strictly decodes a static bearer secret reference.
func (s *MCPStaticBearerProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth.static_bearer", s.strictFields()); err != nil {
		return err
	}
	return validateMCPSecretRef("mcp.servers[].auth.static_bearer.token_env", s.TokenEnv)
}

func (o *MCPOAuthProfile) strictFields() map[string]any {
	return map[string]any{"profile": &o.Profile, "principal": &o.Principal, "issuer": &o.Issuer, "client": &o.Client, "scopes": &o.Scopes, "request_refresh_token": &o.RequestRefreshToken, "credentials": &o.Credentials, "network": &o.Network}
}

// UnmarshalYAML strictly decodes and validates OAuth profile metadata.
func (o *MCPOAuthProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth.oauth", o.strictFields()); err != nil {
		return err
	}
	if !mappingHasKey(node, "client") {
		return errors.New("mcp.servers[].auth.oauth.client is required")
	}
	if !mappingHasKey(node, "credentials") {
		return errors.New("mcp.servers[].auth.oauth.credentials is required")
	}
	if err := validateMCPSafeValue("mcp.servers[].auth.oauth.profile", o.Profile); err != nil {
		return err
	}
	if err := validateMCPSafeValue("mcp.servers[].auth.oauth.principal", o.Principal); err != nil {
		return err
	}
	if err := validateMCPOrigin("mcp.servers[].auth.oauth.issuer", o.Issuer); err != nil {
		return err
	}
	if len(o.Scopes) == 0 {
		return errors.New("mcp.servers[].auth.oauth.scopes is required")
	}
	for _, scope := range o.Scopes {
		if err := validateMCPSafeValue("mcp.servers[].auth.oauth.scopes[]", scope); err != nil {
			return err
		}
	}
	if o.Network == nil {
		return errors.New("mcp.servers[].auth.oauth.network is required")
	}
	return nil
}

func (c *MCPOAuthClientProfile) strictFields() map[string]any {
	return map[string]any{modeKey: &c.Mode, "preregistered": &c.Preregistered, "cimd": &c.CIMD}
}

// UnmarshalYAML strictly decodes the closed preregistered/CIMD client union.
func (c *MCPOAuthClientProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth.oauth.client", c.strictFields()); err != nil {
		return err
	}
	switch c.Mode {
	case "preregistered":
		if c.Preregistered == nil || mappingHasKey(node, "cimd") {
			return errors.New("mcp.servers[].auth.oauth.client: preregistered requires only preregistered payload")
		}
	case "cimd":
		if c.CIMD == nil || mappingHasKey(node, "preregistered") {
			return errors.New("mcp.servers[].auth.oauth.client: cimd requires only cimd payload")
		}
	default:
		return errors.New("mcp.servers[].auth.oauth.client.mode must be exactly preregistered or cimd")
	}
	return nil
}

func (c *MCPPreregisteredClientProfile) strictFields() map[string]any {
	return map[string]any{"id": &c.ID, "secret_env": &c.SecretEnv}
}

// UnmarshalYAML strictly decodes preregistered client metadata.
func (c *MCPPreregisteredClientProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth.oauth.client.preregistered", c.strictFields()); err != nil {
		return err
	}
	if err := validateMCPSafeValue("mcp.servers[].auth.oauth.client.preregistered.id", c.ID); err != nil {
		return err
	}
	return validateMCPSecretRef("mcp.servers[].auth.oauth.client.preregistered.secret_env", c.SecretEnv)
}

func (c *MCPCIMDClientProfile) strictFields() map[string]any {
	return map[string]any{"document_url": &c.DocumentURL}
}

// UnmarshalYAML strictly decodes CIMD client metadata.
func (c *MCPCIMDClientProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth.oauth.client.cimd", c.strictFields()); err != nil {
		return err
	}
	u, err := validateMCPHTTPURL("mcp.servers[].auth.oauth.client.cimd.document_url", c.DocumentURL, true)
	if err != nil {
		return err
	}
	if u.Path == "" || u.Path == "/" {
		return errors.New("mcp.servers[].auth.oauth.client.cimd.document_url must include a document path")
	}
	return nil
}

func (c *MCPOAuthCredentialProfile) strictFields() map[string]any {
	return map[string]any{modeKey: &c.Mode, "local": &c.Local, "environment": &c.Environment}
}

// UnmarshalYAML strictly decodes the closed local/environment credential union.
func (c *MCPOAuthCredentialProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth.oauth.credentials", c.strictFields()); err != nil {
		return err
	}
	switch c.Mode {
	case "local":
		if c.Local == nil || mappingHasKey(node, "environment") {
			return errors.New("mcp.servers[].auth.oauth.credentials: local requires only local payload")
		}
	case "environment":
		if c.Environment == nil || mappingHasKey(node, "local") {
			return errors.New("mcp.servers[].auth.oauth.credentials: environment requires only environment payload")
		}
	default:
		return errors.New("mcp.servers[].auth.oauth.credentials.mode must be exactly local or environment")
	}
	return nil
}

func (c *MCPLocalCredentialProfile) strictFields() map[string]any {
	return map[string]any{"root": &c.Root, "key_env": &c.KeyEnv}
}

// UnmarshalYAML strictly decodes local credential-store metadata.
func (c *MCPLocalCredentialProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth.oauth.credentials.local", c.strictFields()); err != nil {
		return err
	}
	if c.Root == "" || !filepath.IsAbs(c.Root) {
		return errors.New("mcp.servers[].auth.oauth.credentials.local.root must be absolute")
	}
	return validateMCPSecretRef("mcp.servers[].auth.oauth.credentials.local.key_env", c.KeyEnv)
}

func (c *MCPEnvironmentCredentialProfile) strictFields() map[string]any {
	return map[string]any{"credential_env": &c.CredentialEnv, "allow_process_local_refresh": &c.AllowProcessLocalRefresh}
}

// UnmarshalYAML strictly decodes environment credential metadata.
func (c *MCPEnvironmentCredentialProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth.oauth.credentials.environment", c.strictFields()); err != nil {
		return err
	}
	return validateMCPSecretRef("mcp.servers[].auth.oauth.credentials.environment.credential_env", c.CredentialEnv)
}

func (n *MCPOAuthNetworkProfile) strictFields() map[string]any {
	return map[string]any{"additional_origins": &n.AdditionalOrigins, "private_origins": &n.PrivateOrigins, "max_redirects": &n.MaxRedirects}
}

// UnmarshalYAML strictly decodes immutable OAuth network policy metadata.
func (n *MCPOAuthNetworkProfile) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "mcp.servers[].auth.oauth.network", n.strictFields()); err != nil {
		return err
	}
	if n.MaxRedirects < 0 || n.MaxRedirects > 5 {
		return errors.New("mcp.servers[].auth.oauth.network.max_redirects must be between 0 and 5")
	}
	for _, raw := range n.AdditionalOrigins {
		if err := validateMCPOrigin("mcp.servers[].auth.oauth.network.additional_origins[]", raw); err != nil {
			return err
		}
	}
	for _, raw := range n.PrivateOrigins {
		if err := validateMCPOrigin("mcp.servers[].auth.oauth.network.private_origins[]", raw); err != nil {
			return err
		}
	}
	return nil
}

func validateMCPSecretRef(field, value string) error {
	if !mecatlSecretReference.MatchString(value) {
		return fmt.Errorf("%s must name a MECATL_* environment variable", field)
	}
	return nil
}
func validateMCPSafeValue(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", field)
		}
	}
	return nil
}
func validateMCPHTTPURL(field, raw string, httpsOnly bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" || !validMCPHostname(u.Hostname()) {
		return nil, fmt.Errorf("%s must be an absolute HTTP(S) URL without userinfo or fragment", field)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "https" && (httpsOnly || u.Scheme != "http") {
		return nil, fmt.Errorf("%s has an unsupported scheme", field)
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%s has an invalid port", field)
		}
	}
	return u, nil
}
func validateMCPOrigin(field, raw string) error {
	u, err := validateMCPHTTPURL(field, raw, false)
	if err != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return fmt.Errorf("%s must be a canonical exact origin", field)
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	canonical := u.Scheme + "://" + host
	if raw != canonical {
		return fmt.Errorf("%s must use canonical exact-origin form", field)
	}
	return nil
}
func validMCPHostname(host string) bool {
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}
func mcpURLOrigin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	return u.Scheme + "://" + host
}
func mcpLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// LearningSection is the strict learning: settings subtree.
type LearningSection struct {
	// Mode controls automatic completed-trajectory observation: off (default; no
	// automatic reflection), review (signal-gated reflection stages durable proposals
	// without memory writes), or auto (stage first, then conservatively promote only
	// eligible non-conflicting facts). Operator settings establish the ceiling;
	// project settings may only tighten it under off < review < auto and never raise
	// autonomy. It does not override separately configured maintenance schedules such
	// as --user-model-consolidate-interval.
	Mode string `yaml:"mode"`
	// Sensitivity controls weighted automatic admission. Empty means balanced.
	Sensitivity string `yaml:"sensitivity"`
	// Skills controls learned-skill lifecycle policy.
	Skills *LearningSkillsSection `yaml:"skills"`
	// Automatic is operator-only process-local rate policy.
	Automatic *LearningAutomaticSection `yaml:"automatic"`
}

// LearningSkillsSection is the strict learned-skill policy subtree.
type LearningSkillsSection struct {
	// Activation is validated (default for Auto) or evaluated. Project settings
	// may only tighten validated to evaluated.
	Activation string `yaml:"activation"`
}

// UnmarshalYAML strictly decodes learning.skills.activation.
func (s *LearningSkillsSection) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "learning.skills", map[string]any{"activation": &s.Activation}); err != nil {
		return err
	}
	if s.Activation != "" {
		_, err := learning.ParseSkillActivationPolicy(s.Activation)
		return err
	}
	return nil
}

// LearningAutomaticSection is the strict process-local automatic-admission budget.
type LearningAutomaticSection struct {
	// Cooldown is the per-principal weighted-admission cooldown; zero disables it.
	Cooldown time.Duration `yaml:"cooldown"`
	// Window is the sliding count/token window, strictly 1m..24h.
	Window time.Duration `yaml:"window"`
	// MaxReflections is the process-wide count cap; zero disables automatic reflection.
	MaxReflections int `yaml:"max_reflections"`
	// MaxTokens is the process-wide reserved-token cap; zero disables automatic reflection.
	MaxTokens int `yaml:"max_tokens"`
	// MaxReflectionsPerPrincipal is the per-principal count cap; zero disables automatic reflection.
	MaxReflectionsPerPrincipal int `yaml:"max_reflections_per_principal"`
	// MaxTokensPerPrincipal is the per-principal reserved-token cap; zero disables automatic reflection.
	MaxTokensPerPrincipal int `yaml:"max_tokens_per_principal"`
}

func durationScalar(node *yaml.Node, name string) (time.Duration, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return 0, fmt.Errorf("%s: must be a duration string", name)
	}
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
}

// UnmarshalYAML strictly decodes and bounds the automatic-admission policy.
func (s *LearningAutomaticSection) UnmarshalYAML(node *yaml.Node) error {
	s.Cooldown, s.Window = 10*time.Minute, time.Hour
	s.MaxReflections, s.MaxTokens = 8, 100000
	s.MaxReflectionsPerPrincipal, s.MaxTokensPerPrincipal = 4, 50000
	var cooldown, window yaml.Node
	if err := decodeStrictMapping(node, "learning.automatic", map[string]any{
		"cooldown": &cooldown, "window": &window,
		"max_reflections": &s.MaxReflections, "max_tokens": &s.MaxTokens,
		"max_reflections_per_principal": &s.MaxReflectionsPerPrincipal,
		"max_tokens_per_principal":      &s.MaxTokensPerPrincipal,
	}); err != nil {
		return err
	}
	var err error
	if cooldown.Kind != 0 {
		if s.Cooldown, err = durationScalar(&cooldown, "learning.automatic.cooldown"); err != nil {
			return err
		}
		if s.Cooldown < 0 {
			return fmt.Errorf("learning.automatic.cooldown: must be nonnegative")
		}
	}
	if window.Kind != 0 {
		if s.Window, err = durationScalar(&window, "learning.automatic.window"); err != nil {
			return err
		}
		if s.Window < time.Minute || s.Window > 24*time.Hour {
			return fmt.Errorf("learning.automatic.window: must be between 1m and 24h")
		}
	}
	for name, value := range map[string]int{"max_reflections": s.MaxReflections, "max_tokens": s.MaxTokens, "max_reflections_per_principal": s.MaxReflectionsPerPrincipal, "max_tokens_per_principal": s.MaxTokensPerPrincipal} {
		if value < 0 || value > 1_000_000_000 {
			return fmt.Errorf("learning.automatic.%s: must be between 0 and 1000000000", name)
		}
	}
	return nil
}

// UnmarshalYAML strictly decodes learning.mode and validates its closed vocabulary.
func (s *LearningSection) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "learning", map[string]any{modeKey: &s.Mode, "sensitivity": &s.Sensitivity, "skills": &s.Skills, "automatic": &s.Automatic}); err != nil {
		return err
	}
	if s.Mode != "" {
		if _, err := learning.ParseMode(s.Mode); err != nil {
			return err
		}
	}
	if s.Sensitivity != "" {
		if _, err := learning.ParseSensitivity(s.Sensitivity); err != nil {
			return err
		}
	}
	return nil
}

// OpenRouterSection is the `openrouter:` operator-tier YAML subtree (issue #480):
// per-model downstream-provider routing preferences. mecatl's "provider" stays
// the wire adapter — these are the DOWNSTREAM inference providers OpenRouter
// routes a model to (Anthropic, Amazon Bedrock, Google Vertex, …). The TOP
// mapping is parsed STRICTLY (unknown keys error); the per-model entries are
// also strict. Validated fail-soft in composition (invalid slugs WARN-dropped).
type OpenRouterSection struct {
	// Models maps a model id (or alias, resolved in composition) to its
	// downstream-provider routing preference.
	Models map[string]OpenRouterModelRoute `yaml:"models"`
}

// OpenRouterModelRoute is one model's downstream-provider routing preference
// (issue #480), the on-disk mirror of OpenRouter's `provider` request object
// (v1 surface: order + allow_fallbacks). Composition maps it to
// OpenRouterProviderPreferences.
type OpenRouterModelRoute struct {
	// Order lists downstream provider slugs (lowercase-kebab, e.g. "anthropic",
	// "google-vertex", "deepinfra/turbo") tried in order. Setting it disables
	// OpenRouter's default price load-balancing. Base-slug matching applies:
	// "google-vertex" matches all its regions/variants (service tiers excepted).
	Order []string `yaml:"order"`
	// AllowFallbacks, when explicitly false, pins the request to Order with no
	// fallback to other downstreams. Omit the key to keep OpenRouter's default
	// (true); set it to false to disable fallback.
	AllowFallbacks *bool `yaml:"allow_fallbacks"`
}

func (s *OpenRouterSection) strictFields() map[string]any {
	return map[string]any{
		"models": &s.Models,
	}
}

// UnmarshalYAML decodes the openrouter: mapping STRICTLY (issue #480): an
// unknown key inside the subtree is a parse error — a typo like `moddels:` must
// not silently drop the routing preferences. Same rationale as GuardrailsSection.
func (s *OpenRouterSection) UnmarshalYAML(node *yaml.Node) error {
	return decodeStrictMapping(node, "openrouter", s.strictFields())
}

func (m *OpenRouterModelRoute) strictFields() map[string]any {
	return map[string]any{
		"order":           &m.Order,
		"allow_fallbacks": &m.AllowFallbacks,
	}
}

// UnmarshalYAML decodes an openrouter.models.<id> entry STRICTLY.
func (m *OpenRouterModelRoute) UnmarshalYAML(node *yaml.Node) error {
	return decodeStrictMapping(node, "openrouter.models[]", m.strictFields())
}

// ModelsSection is the `models:` YAML subtree (ADR 0030): a per-slot model-binding
// map, an alias map, a session-default binding, and the operator-tier allowlist cap.
// The TOP mapping is parsed STRICTLY (unknown keys error); the inner Slots/Aliases
// maps are free-form name→selector (composition validates the slot names fail-soft
// via knownSlotNames).
//
// The block appears at BOTH tiers but the tiers differ in what they may carry
// (Phase 4):
//   - OPERATOR tier (user-global + CLI): all four fields. The Allowlist is the
//     non-wideable cap on what a PROJECT may bind; Slots/Aliases/Default are the
//     operator's own bindings (never capped — the operator is authoritative).
//   - PROJECT tier (.mecatl/settings.yaml): Slots/Aliases/Default ONLY, honoured
//     only within the operator Allowlist and only on a TRUSTED workspace. A project
//     Allowlist: key is IGNORED with a WARN (a project cannot widen its own cap).
type ModelsSection struct {
	// Slots binds a slot name (a call-slot "compaction"/"ask-reviewer"/"guardrail" or
	// a tier "cheap"/"fast"/"reasoning") to a model selector (alias or concrete id).
	Slots map[string]string `yaml:"slots"`
	// Aliases binds a short alias to a concrete model id (merged onto the CLI
	// --model-alias map, CLI winning per key).
	Aliases map[string]string `yaml:"aliases"`
	// Default is the session-default model selector (alias or concrete id). It is the
	// project-overridable session default (ADR 0030 Phase 4) — within the operator
	// allowlist; the operator's own Default is uncapped. Empty = absent.
	Default string `yaml:"default"`
	// Subagent is the OPERATOR-TIER def-less child-default model selector (alias or
	// concrete id): the settings.yaml twin of the --subagent-model flag (issue #288).
	// It sets the global default model for every Subagent / Parallel-branch / team-member
	// child that does not pin its own model (via an agent definition or a per-call
	// override). Operator-tier ONLY: a project-tier subagent: is IGNORED with a WARN (the
	// child-default model is an operator decision — the same operator-only captureModels
	// discipline as default_provider/allowlist/router). The CLI --subagent-model WINS when
	// both are set. Validated FAIL-FAST at Build (normalizeSubagentModel): a value that
	// does not resolve to a usable model id is a startup error (unlike fail-soft
	// models.default). Empty = absent (the flag/inherit-parent behaviour is unchanged).
	Subagent string `yaml:"subagent"`
	// DefaultProvider is the OPERATOR-TIER deployment-wide default provider id (e.g.
	// openai, openrouter, anthropic, toolhive). It mirrors the --default-provider flag
	// (app.Config.DefaultProvider) so an operator can declare "toolhive is my default
	// despite my API key" persistently in settings.yaml without unsetting the key. It
	// feeds the UNCHANGED preferredDefaultProvider ladder as an explicit override — it
	// does NOT lower the precedence of key-driven providers. Operator-tier only: a
	// project-tier default_provider: is IGNORED with a WARN (the same operator-only
	// captureModels discipline as posture/guardrails/allowlist). Validated FAIL-FAST at
	// Build (validateDefaultModel): an unknown/unavailable provider is a startup error.
	// Empty = absent (the ladder's preferred default wins). The name pair
	// (default = model, default_provider = provider) mirrors the wire grammar exactly.
	DefaultProvider string `yaml:"default_provider"`
	// Allowlist is the OPERATOR-TIER, non-wideable cap (ADR 0030 Phase 4): the set of
	// model selectors (alias names and/or concrete ids) a PROJECT-tier models: block
	// may bind to. An empty/absent allowlist means project models stay WARN-ignored
	// (the opt-in: no cap ⇒ no project override, byte-identical to pre-Phase-4). It is
	// honoured ONLY from the operator tiers; a project-tier allowlist: key is ignored
	// with a WARN (a project cannot widen its own cap).
	Allowlist []string `yaml:"allowlist"`
	// Router is the OPERATOR-TIER semantic Subagent model-router taxonomy (ADR 0031,
	// Phase 5; enable model superseded by ADR 0042): a classifier slot, the routing
	// categories, the default category, and the YAML kill-switch. It is operator-tier
	// ONLY — a project-tier router: sub-block is STRIPPED with a WARN (the taxonomy is
	// an autonomous-spend/capability decision the operator owns, like the allowlist).
	// nil/absent = no taxonomy ⇒ the router is OFF (byte-identical, silent). Per ADR
	// 0042 the TAXONOMY is the enable: a non-empty router: with categories turns the
	// router ON unless `disabled: true` (or the CLI kill-switch) forces it off — the
	// guardrails-parity enable model, replacing 0031's flag-to-enable.
	Router *RouterSection `yaml:"router"`
	// ContextWindows is the OPERATOR-TIER exact provider ID → exact final model ID
	// → total context token override map. It is intentionally not a selector map:
	// aliases and slots are resolved before this lookup, and project values are ignored.
	ContextWindows ContextWindows `yaml:"context_windows"`
}

// ContextWindows is the operator-owned exact provider → final model → token map.
// Its custom decoder keeps type/range failures attributable to the precise entry;
// yaml's generic nested-map error otherwise reports only a line number.
type ContextWindows map[string]map[string]int

// UnmarshalYAML decodes and validates each configured context window with its full path.
func (c *ContextWindows) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("models.context_windows: must be a mapping")
	}
	out := make(ContextWindows, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		providerNode := node.Content[i]
		if providerNode.Kind != yaml.ScalarNode || providerNode.Tag != "!!str" {
			return fmt.Errorf("models.context_windows: provider key must be a string")
		}
		provider := providerNode.Value
		if strings.TrimSpace(provider) == "" {
			return fmt.Errorf("models.context_windows: provider key must not be empty")
		}
		if _, duplicate := out[provider]; duplicate {
			return fmt.Errorf("models.context_windows.%s: duplicate provider key", provider)
		}
		modelsNode := node.Content[i+1]
		if modelsNode.Kind != yaml.MappingNode {
			return fmt.Errorf("models.context_windows.%s: must be a model-to-token mapping", provider)
		}
		models := make(map[string]int, len(modelsNode.Content)/2)
		for j := 0; j < len(modelsNode.Content); j += 2 {
			modelNode := modelsNode.Content[j]
			if modelNode.Kind != yaml.ScalarNode || modelNode.Tag != "!!str" {
				return fmt.Errorf("models.context_windows.%s: model key must be a string", provider)
			}
			model := modelNode.Value
			path := fmt.Sprintf("models.context_windows.%s.%s", provider, model)
			if strings.TrimSpace(model) == "" {
				return fmt.Errorf("%s: model key must not be empty", path)
			}
			if _, duplicate := models[model]; duplicate {
				return fmt.Errorf("%s: duplicate model key", path)
			}
			valueNode := modelsNode.Content[j+1]
			if valueNode.Kind != yaml.ScalarNode || valueNode.Tag != "!!int" {
				return fmt.Errorf("%s: context window must be an integer", path)
			}
			var tokens int
			if err := valueNode.Decode(&tokens); err != nil {
				return fmt.Errorf("%s: context window must be a runtime-representable integer: %w", path, err)
			}
			if tokens <= 0 || tokens > MaxContextWindowTokens {
				return fmt.Errorf("%s: context window must be between 1 and %d tokens", path, MaxContextWindowTokens)
			}
			models[model] = tokens
		}
		out[provider] = models
	}
	*c = out
	return nil
}

// RouterSection is the `models.router:` operator-tier subtree (ADR 0031): the semantic
// Subagent model-router taxonomy. The classifier reads the category descriptions to
// choose which category a delegated task belongs to; composition maps the chosen
// category's Model selector through the alias/slot machinery to a concrete model id.
type RouterSection struct {
	// ClassifierSlot names the model slot the CLASSIFIER itself runs on (the tiny,
	// cheap one-turn classification call). Empty falls through to the `router` slot's
	// default tier (cheap) — the classifier is housekeeping, not the routed work.
	ClassifierSlot string `yaml:"classifier-slot"`
	// Categories are the routing choices. Each carries a Name (the classifier's verdict
	// key), a Description (the classifier's only signal — make them distinct), and a
	// Model selector (an alias / slot / concrete id, resolved through the operator-
	// merged alias map; operator taxonomy targets are UNCAPPED).
	Categories []RouterCategory `yaml:"categories"`
	// DefaultCategory is the category the classifier is told to choose when none clearly
	// fits (advisory to the classifier; the real safety net is the fail-soft inherit).
	DefaultCategory string `yaml:"default-category"`
	// Disabled is the YAML-level kill switch (ADR 0042, mirroring
	// GuardrailsSection.Disabled): per ADR 0042 a non-empty taxonomy ENABLES the router,
	// so `disabled: true` is the "taxonomy defined but temporarily off" override. The
	// CLI kill-switch --subagent-model-router=false also sets it (the two OR together).
	// Default false ⇒ the router is enabled whenever categories are present.
	Disabled bool `yaml:"disabled"`
}

// RouterCategory is one routing category in the operator taxonomy (ADR 0031): a name,
// a one-line description the classifier reads, and the model selector the category maps
// to. A category with an empty Name or Description is WARN-dropped fail-soft in
// composition (foldOperatorModelRouter) — a category the classifier cannot describe or
// name is useless.
type RouterCategory struct {
	// Name is the routing key the classifier echoes back as its verdict and the key
	// composition maps to Model.
	Name string `yaml:"name"`
	// Description is the one-line summary the classifier reads to choose this category.
	Description string `yaml:"description"`
	// Model is the model selector (alias / slot / concrete id) a task classified into
	// this category is minted on, resolved through the operator-merged alias map.
	Model string `yaml:"model"`
}

// strictFields is the single binding map for the models.router: subtree — the ONE
// authoritative key set both UnmarshalYAML (the parser) and the configgen drift guard
// (via the test-only KnownKeys accessor) read, so they cannot diverge.
func (r *RouterSection) strictFields() map[string]any {
	return map[string]any{
		"classifier-slot":  &r.ClassifierSlot,
		"categories":       &r.Categories,
		"default-category": &r.DefaultCategory,
		"disabled":         &r.Disabled,
	}
}

// UnmarshalYAML decodes the models.router: mapping STRICTLY (ADR 0031): an unknown key
// inside the router subtree is a parse error (same rationale as ModelsSection).
func (r *RouterSection) UnmarshalYAML(node *yaml.Node) error {
	return decodeStrictMapping(node, "models.router", r.strictFields())
}

func (c *RouterCategory) strictFields() map[string]any {
	return map[string]any{
		"name":        &c.Name,
		"description": &c.Description,
		"model":       &c.Model,
	}
}

// UnmarshalYAML decodes a router category mapping STRICTLY.
func (c *RouterCategory) UnmarshalYAML(node *yaml.Node) error {
	return decodeStrictMapping(node, "models.router.categories[]", c.strictFields())
}

func (m *ModelsSection) strictFields() map[string]any {
	return map[string]any{
		"slots":            &m.Slots,
		"aliases":          &m.Aliases,
		"default":          &m.Default,
		"subagent":         &m.Subagent,
		"default_provider": &m.DefaultProvider,
		"allowlist":        &m.Allowlist,
		"router":           &m.Router,
		"context_windows":  &m.ContextWindows,
	}
}

// UnmarshalYAML decodes the models: mapping STRICTLY (ADR 0030): an unknown key
// inside the models subtree is a parse error — a typo like `slotz:` or `aliasez:`
// must not silently drop a whole binding map. Same rationale as GuardrailsSection.
func (m *ModelsSection) UnmarshalYAML(node *yaml.Node) error {
	if node != nil && node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "context_windows" && node.Content[i+1].Tag == "!!null" {
				return fmt.Errorf("models.context_windows: must be a mapping")
			}
		}
	}
	return decodeStrictMapping(node, "models", m.strictFields())
}

// GuardrailsSection is the operator-tier `guardrails:` YAML subtree (issue #27): a
// checker model, a master-disable, and the rule list. It is parsed STRICTLY
// (unknown keys error).
type GuardrailsSection struct {
	// Model is the checker model id / alias. Empty leaves the CLI --guardrails-model
	// to supply it; a value here is overridden by the CLI flag when both are set.
	Model string `yaml:"model"`
	// MinContentBytes skips the checker for content shorter than this. 0 = check all.
	MinContentBytes int `yaml:"minContentBytes"`
	// Disabled is the YAML-level kill switch (the CLI --guardrails=off also sets it).
	Disabled bool `yaml:"disabled"`
	// OnCheckerDown sets the global posture when the checker model is unavailable
	// (error/timeout): "warn" (default, fail-open) or "fail" (fail-closed for all
	// rules). Per-rule failClosed overrides: failClosed:true tightens even under
	// warn; failClosed:false (explicit) loosens even under fail. Empty = warn.
	OnCheckerDown string `yaml:"onCheckerDown"`
	// DefaultMode sets the enforcement mode for the built-in default rules when no
	// explicit rules are configured: "block" (default), "advisory", or "sanitize".
	// An explicit rules list replaces the defaults entirely (this key is ignored).
	DefaultMode string `yaml:"defaultMode"`
	// Escape is the ADR-0080 escape knob: when true AND a checker model is
	// configured, an out-of-root FS escape at posture auto routes through the
	// guardrail checker (an unsafe verdict denies; a checker error fails closed
	// to the write-escape Ask). Default false = the un-routed posture table.
	Escape bool `yaml:"escape"`
	// Rules is the guardrail rule list.
	Rules []GuardrailRuleSpec `yaml:"rules"`
}

// GuardrailRuleSpec is one operator-tier guardrail rule as parsed from YAML. It is
// the on-disk mirror of app.GuardrailRule; composition maps the two. Parsed strictly.
type GuardrailRuleSpec struct {
	// Match is the tool-name matcher (exact / "prefix*" / "*").
	Match string `yaml:"match"`
	// Phases lists "pre"/"post"; empty = both.
	Phases []string `yaml:"phases"`
	// Mode is "block"/"sanitize"/"advisory"; empty defaults to block.
	Mode string `yaml:"mode"`
	// Prompt overrides the built-in inspection rubric.
	Prompt string `yaml:"prompt"`
	// FailClosed flips the fail-open default for enforcing modes.
	FailClosed bool `yaml:"failClosed"`
	// FailClosedPresent reports whether the failClosed key was explicitly set in
	// the YAML — a bool can't distinguish "false" from "not set", so this lets the
	// global onCheckerDown toggle distinguish a per-rule explicit opt-out from an
	// unset rule that should inherit the global.
	FailClosedPresent bool `yaml:"-"`
}

// UnmarshalYAML decodes the guardrails: mapping STRICTLY (issue #27): an unknown key
// inside the guardrails subtree is a parse error — a typo like `moddel:` or `rulez:`
// must not silently disable a guardrail. Same rationale as Permissions.UnmarshalYAML.
func (g *GuardrailsSection) UnmarshalYAML(node *yaml.Node) error {
	return decodeStrictMapping(node, "guardrails", g.strictFields())
}

func (g *GuardrailsSection) strictFields() map[string]any {
	return map[string]any{
		"model":           &g.Model,
		"minContentBytes": &g.MinContentBytes,
		"disabled":        &g.Disabled,
		"onCheckerDown":   &g.OnCheckerDown,
		"defaultMode":     &g.DefaultMode,
		"escape":          &g.Escape,
		"rules":           &g.Rules,
	}
}

func (r *GuardrailRuleSpec) strictFields() map[string]any {
	return map[string]any{
		"match":      &r.Match,
		"phases":     &r.Phases,
		modeKey:      &r.Mode,
		"prompt":     &r.Prompt,
		"failClosed": &r.FailClosed,
	}
}

// UnmarshalYAML decodes a guardrails rule mapping STRICTLY.
func (r *GuardrailRuleSpec) UnmarshalYAML(node *yaml.Node) error {
	if err := decodeStrictMapping(node, "guardrails.rules[]", r.strictFields()); err != nil {
		return err
	}
	// Track whether failClosed was explicitly present so the global onCheckerDown
	// toggle can distinguish a per-rule opt-out from an unset rule.
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "failClosed" {
			r.FailClosedPresent = true
			break
		}
	}
	return nil
}

// Permissions is the three-bucket rule-spec set plus the child-scoped
// `subagent:` block (issue #32). Resolution is deny-dominant, so a spec
// appearing in Deny always wins over the same spec in Allow regardless of
// bucket order here. Audience semantics (applied by rulesFromConfig):
// Allow/Ask bind the MAIN engine only; Deny binds BOTH main and subagents
// (tighten-only); the Subagent block binds child engines only.
type Permissions struct {
	// Allow lists rule specs that GRANT a tool call (effect Allow) on the MAIN
	// engine. Under an untrusted project these are DROPPED by the trust gate
	// (see Resolver).
	Allow []string `yaml:"allow"`
	// Ask lists rule specs that REQUIRE approval (effect Ask) on the MAIN
	// engine. Always honoured.
	Ask []string `yaml:"ask"`
	// Deny lists rule specs that BLOCK a tool call (effect Deny) EVERYWHERE —
	// main engine and subagents (a deny only tightens). Always honoured.
	Deny []string `yaml:"deny"`
	// Subagent holds the child-scoped rule-spec lists (issue #32): rules that
	// bind ONLY subagent/member/branch engines, resolved through the child-ask
	// model (a subagent allow can clear a substitution-floored ask; a subagent
	// ask surfaces to the human or auto-denies; a subagent deny blocks).
	Subagent SubagentPermissions `yaml:"subagent"`
}

// SubagentPermissions is the child-scoped allow/ask/deny rule-spec set
// (issue #32). Same spec grammar as the top-level buckets; every parsed rule is
// tagged AudienceSubagent so it binds only child engines. Project-tier subagent
// ALLOWS are trust-gated exactly like top-level allows; ask/deny always hold.
type SubagentPermissions struct {
	// Allow lists child-scoped rule specs with effect Allow (trust-gated for
	// project tiers).
	Allow []string `yaml:"allow"`
	// Ask lists child-scoped rule specs with effect Ask (always honoured). A
	// configured subagent Ask is NEVER auto-approved by the isolation carve-out —
	// it surfaces to a human or auto-denies.
	Ask []string `yaml:"ask"`
	// Deny lists child-scoped rule specs with effect Deny (always honoured).
	Deny []string `yaml:"deny"`
}

// UnmarshalYAML decodes the permissions: mapping STRICTLY (issue #32): an
// unknown key inside the permissions subtree is a parse error — surfaced
// through the existing per-file fail-soft log-and-skip — rather than silently
// ignored config (a typo like `alow:` or `subagnet:` would otherwise disable a
// whole rule list without a trace). The top level of Config stays lenient.
func (p *Permissions) UnmarshalYAML(node *yaml.Node) error {
	return decodeStrictMapping(node, "permissions", p.strictFields())
}

func (p *Permissions) strictFields() map[string]any {
	return map[string]any{
		"allow":    &p.Allow,
		"ask":      &p.Ask,
		"deny":     &p.Deny,
		"subagent": &p.Subagent,
	}
}

func (s *SubagentPermissions) strictFields() map[string]any {
	return map[string]any{
		"allow": &s.Allow,
		"ask":   &s.Ask,
		"deny":  &s.Deny,
	}
}

// UnmarshalYAML decodes the permissions.subagent: mapping STRICTLY — same
// rationale as Permissions.UnmarshalYAML.
func (s *SubagentPermissions) UnmarshalYAML(node *yaml.Node) error {
	return decodeStrictMapping(node, "permissions.subagent", s.strictFields())
}

func mappingHasKey(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return true
		}
	}
	return false
}

// decodeStrictMapping walks a YAML mapping node and decodes each known key's
// value into its target, erroring on any unknown key (with the key's line for
// the operator). A null/absent node (e.g. a bare `permissions:` line) decodes
// to the zero value. A non-mapping node is an error — the subtree's shape is
// part of the strict contract.
func decodeStrictMapping(node *yaml.Node, where string, known map[string]any) error {
	if node == nil || node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a mapping (line %d)", where, node.Line)
	}
	seen := make(map[string]struct{}, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode, valNode := node.Content[i], node.Content[i+1]
		if _, duplicate := seen[keyNode.Value]; duplicate {
			return fmt.Errorf("%s: duplicate key %q (line %d)", where, keyNode.Value, keyNode.Line)
		}
		seen[keyNode.Value] = struct{}{}
		target, ok := known[keyNode.Value]
		if !ok {
			return fmt.Errorf("%s: unknown key %q (line %d); known keys: %s",
				where, keyNode.Value, keyNode.Line, knownKeyList(known))
		}
		if err := valNode.Decode(target); err != nil {
			return fmt.Errorf("%s.%s: %w", where, keyNode.Value, err)
		}
	}
	return nil
}

// knownKeyList renders the known-key set for the unknown-key error message in a
// stable (sorted) order, so a typo'd key names exactly the keys this mapping accepts.
func knownKeyList(known map[string]any) string {
	keys := make([]string, 0, len(known))
	for k := range known {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
