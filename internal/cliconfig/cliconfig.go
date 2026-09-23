// Package cliconfig holds the small slices of CLI/composition wiring that the four
// command mains (cmd/mecated, cmd/mecatui, cmd/mecatequi, cmd/mecak8s) would otherwise
// copy-paste — extracted here so they cannot drift apart. It is a CMD-SIDE composition
// helper: it may read the process environment (os.Getenv) and register flags
// (flag.FlagSet), then apply the resolved values onto an app.Config.
//
// Why it lives in internal/ and not in internal/app: app.Build deliberately reads the
// environment ONLY through its injected envDetector seam (see internal/app/build.go),
// so the os.Getenv reads for provider credentials belong OUTSIDE app — in the cmd layer
// or a cmd-side helper like this one. The dependency direction stays inward
// (cmd -> cliconfig -> app); cliconfig never imports a cmd main. The credentials-FILE
// parsing itself (auth.yaml) lives one layer further out, in the small leaf adapter
// internal/adapter/authfile, so a future credential-writing subcommand can depend on
// that schema directly without pulling in cliconfig's flag/model-alias machinery too;
// cliconfig only wires it (path resolution + precedence against the environment).
package cliconfig

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
)

// knownAuthProviders is the closed set of provider names an auth.yaml entry
// may use — passed into authfile.Load so that package stays agnostic of which
// providers mecatl specifically knows about.
var knownAuthProviders = []string{"anthropic", "openai", "openrouter", "opencode", "openai-codex"}

// Provider credential / base-URL environment variables. These are the SECRET-shaped
// inputs the cmd layer reads on the operator's behalf (the registry also auto-detects
// them via its envDetector, but reading them here makes credential custody explicit and
// keeps it identical across the three mains). They are NEVER logged or printed.
const (
	envOpenAIKey     = "OPENAI_API_KEY"
	envOpenRouterKey = "OPENROUTER_API_KEY"
	envAnthropicKey  = "ANTHROPIC_API_KEY"
	envOpenCodeKey   = "OPENCODE_API_KEY"
	envTypesafeKey   = "TYPESAFE_API_KEY"
)

// ProviderFlagHelp carries the per-main help text for the three provider base-URL
// flags. The three mains word these slightly differently (mecated is the daemon;
// mecatui prefixes "embedded server only:"), so the help is passed in rather than
// hard-coded — keeping each main's --help BYTE-IDENTICAL across the extraction. A
// zero ProviderFlagHelp falls back to DefaultProviderFlagHelp (the mecated wording),
// which is what a new consumer (mecatequi) uses.
type ProviderFlagHelp struct {
	OpenAIBaseURL     string
	OpenRouterBaseURL string
	AnthropicBaseURL  string
	OpenCodeBaseURL   string
	AuthFile          string
}

// DefaultProviderFlagHelp is the mecated-style wording, used when a field of the passed
// ProviderFlagHelp is empty. It is the right default for a fresh consumer (mecatequi).
var DefaultProviderFlagHelp = ProviderFlagHelp{
	OpenAIBaseURL:     "override the OpenAI API base URL (compatible endpoints)",
	OpenRouterBaseURL: "override the OpenRouter API base URL (default https://openrouter.ai/api/v1; key from OPENROUTER_API_KEY)",
	AnthropicBaseURL:  "override the native Anthropic API base URL (compatible/proxy endpoints; key from ANTHROPIC_API_KEY)",
	OpenCodeBaseURL:   "override the OpenCode Go API base URL (default https://opencode.ai/zen/go/v1; key from OPENCODE_API_KEY)",
	AuthFile:          "provider credentials YAML path (default: $XDG_CONFIG_HOME/mecatl/auth.yaml); environment credentials take precedence",
}

// ProviderFlags holds the values bound by RegisterProviderFlags. The base-URL fields
// are populated by flag parsing; the key fields are populated by Apply (read from the
// environment, then from an auth.yaml credentials file) so a key never has to
// round-trip through the process argv. It is the ONE place the three provider
// credentials + base URLs are wired onto app.Config, so the six fields can never again
// be partially wired (the bug that left mecatequi unable to reach Anthropic /
// OpenRouter).
type ProviderFlags struct {
	openAIBaseURL     *string
	openRouterBaseURL *string
	anthropicBaseURL  *string
	openCodeBaseURL   *string
	authFile          *string
	authSnapshot      *authfile.File
	authSnapshotReady bool
	authSnapshotWarn  string
	apiKeyFile        string
}

// RegisterProviderFlags registers --openai-base-url / --openrouter-base-url /
// --anthropic-base-url / --api-key-file on fs and returns the binding to pass to Apply
// later. The help text comes from help, falling back per-field to
// DefaultProviderFlagHelp so a caller may pass a zero value (or override only the
// fields it words differently).
func RegisterProviderFlags(fs *flag.FlagSet, help ProviderFlagHelp) *ProviderFlags {
	help = help.withDefaults()
	pf := &ProviderFlags{
		openAIBaseURL:     new(string),
		openRouterBaseURL: new(string),
		anthropicBaseURL:  new(string),
		openCodeBaseURL:   new(string),
		authFile:          new(string),
	}
	fs.StringVar(pf.openAIBaseURL, "openai-base-url", "", help.OpenAIBaseURL)
	fs.StringVar(pf.openRouterBaseURL, "openrouter-base-url", "", help.OpenRouterBaseURL)
	fs.StringVar(pf.anthropicBaseURL, "anthropic-base-url", "", help.AnthropicBaseURL)
	fs.StringVar(pf.openCodeBaseURL, "opencode-base-url", "", help.OpenCodeBaseURL)
	fs.StringVar(pf.authFile, "api-key-file", "", help.AuthFile)
	return pf
}

// Apply is the compatibility wrapper that resolves the four API-key credentials
// plus the file-only Codex credential, then projects the resulting snapshot.
// Production roots instead call Resolve once and ApplyResolved wherever the
// resulting app.Config is assembled. A non-empty AuthFileWarning is safe for a
// root to surface once; credential values must never be logged.
func (pf *ProviderFlags) Apply(cfg *app.Config) ResolvedKeys {
	resolved := pf.Resolve()
	pf.ApplyResolved(cfg, resolved)
	return resolved
}

// Resolve reads every ambient API-key input and auth.yaml exactly once and
// returns the immutable credential snapshot command roots cache for their
// lifetime. The manual Codex token intentionally has no environment seam.
func (pf *ProviderFlags) Resolve() ResolvedCredentials {
	return pf.resolve(xdgconfig.OSEnv, time.Now())
}

func (pf *ProviderFlags) resolve(env xdgconfig.ResolveEnv, now time.Time) ResolvedCredentials {
	keys := readProviderKeys(env.Getenv)

	explicitPath := ""
	if pf != nil {
		explicitPath = value(pf.authFile)
	}
	path := explicitPath
	if path == "" && pf != nil {
		path = pf.apiKeyFile
	}
	if path == "" {
		path = authfile.DefaultPath(env)
	}
	configuredPath := explicitPath != "" || (pf != nil && pf.apiKeyFile != "")
	af, warning := authfile.Load(path, configuredPath, env, nil)
	if pf != nil {
		pf.authSnapshot, pf.authSnapshotReady, pf.authSnapshotWarn = af, true, warning
	}
	keys.AuthFileWarning = warning
	keys.OpenAI = cmp.Or(keys.OpenAI, af.APIKey("openai"))
	keys.OpenRouter = cmp.Or(keys.OpenRouter, af.APIKey("openrouter"))
	keys.Anthropic = cmp.Or(keys.Anthropic, af.APIKey("anthropic"))
	keys.OpenCode = cmp.Or(keys.OpenCode, af.APIKey("opencode"))
	loadCodexCredential(&keys, af, path, now)
	return keys
}

func loadCodexCredential(keys *ResolvedCredentials, file *authfile.File, path string, now time.Time) {
	oauth := file.OAuth("openai-codex")
	if oauth.AccessToken == "" {
		return
	}
	credential, err := openaicodex.NewCredential(oauth.AccessToken, oauth.AccountID, oauth.ExpiresAt, now)
	if err != nil {
		keys.AuthFileWarning = joinCredentialWarnings(keys.AuthFileWarning,
			fmt.Sprintf("auth file %s: openai-codex credential ignored: %v", path, err))
		return
	}
	keys.OpenAICodex = credential
}

// HasOperatorProviderDefinitions reports whether the operator settings declare at
// least one custom provider. It is an embedded-server preflight only; app.Build remains
// the sole owner of the resolved definitions used for construction.
func HasOperatorProviderDefinitions(conventional, importClaude bool, files []string) (bool, error) {
	resolver := permconfig.NewWithEnv(permconfig.Options{
		Conventional: conventional, ImportClaude: importClaude, ExplicitFiles: files,
		Diagnostics: port.NopDiagnostics{},
	}, xdgconfig.OSEnv)
	definitions, _, err := resolver.OperatorProviders()
	return len(definitions) > 0, err
}

// ResolveProviderCredentials resolves the one immutable credential snapshot for
// a resolved operator provider definition set. Custom credentials come only from
// auth.yaml; they deliberately have no environment fallback.
func ResolveProviderCredentials(pf *ProviderFlags, definitions permconfig.ProviderDefinitions, env xdgconfig.ResolveEnv) (ResolvedCredentials, error) {
	var keys ResolvedCredentials
	if pf != nil {
		keys = readProviderKeys(env.Getenv)
	}
	path, explicit := authfile.DefaultPath(env), false
	if pf != nil && value(pf.authFile) != "" {
		path, explicit = value(pf.authFile), true
	} else if pf != nil && pf.apiKeyFile != "" {
		path, explicit = pf.apiKeyFile, true
	}
	known := append([]string{}, knownAuthProviders...)
	for id := range definitions {
		known = append(known, id)
	}
	sort.Strings(known)
	var file *authfile.File
	if pf != nil && pf.authSnapshotReady {
		file = pf.authSnapshot
		if file != nil {
			if warning := file.ValidateKnown(known); warning != "" {
				return ResolvedCredentials{}, errors.New(warning)
			}
		}
		if file == nil && pf.authSnapshotWarn != "" {
			return ResolvedCredentials{}, errors.New(pf.authSnapshotWarn)
		}
	} else {
		var err error
		file, err = authfile.LoadStrict(path, explicit, env, known)
		if err != nil {
			return ResolvedCredentials{}, err
		}
	}
	keys.OpenAI = cmp.Or(keys.OpenAI, file.APIKey("openai"))
	keys.OpenRouter = cmp.Or(keys.OpenRouter, file.APIKey("openrouter"))
	keys.Anthropic = cmp.Or(keys.Anthropic, file.APIKey("anthropic"))
	keys.OpenCode = cmp.Or(keys.OpenCode, file.APIKey("opencode"))
	loadCodexCredential(&keys, file, path, time.Now())
	keys.customAPIKeys = make(map[string]string, len(definitions))
	keys.customMethods = make(map[string]string, len(definitions))
	for id, definition := range definitions {
		keys.customMethods[id] = definition.Auth.Method
		if definition.Auth.Method == "api_key" {
			keys.customAPIKeys[id] = file.APIKey(id)
		}
	}
	return keys, nil
}

// SetAPIKeyFile applies the operator-configured API-key file unless the command
// line already selected one. It is called by composition after operator settings
// are resolved and before the immutable credential snapshot is loaded.
func (pf *ProviderFlags) SetAPIKeyFile(path string) {
	if pf == nil || value(pf.authFile) != "" {
		return
	}
	pf.apiKeyFile = path
	pf.authSnapshot = nil
	pf.authSnapshotReady = false
	pf.authSnapshotWarn = ""
}

// AuthFilePath reports the path Resolve would inspect and whether it came from
// --api-key-file. It exposes path provenance without exposing credentials so a
// command-specific startup policy can decide how to present a conventional
// missing-file result.
func (pf *ProviderFlags) AuthFilePath() (path string, explicit bool) {
	if pf != nil && value(pf.authFile) != "" {
		return value(pf.authFile), true
	}
	if pf != nil && pf.apiKeyFile != "" {
		return pf.apiKeyFile, true
	}
	return authfile.DefaultPath(xdgconfig.OSEnv), false
}

// ApplyResolved projects a previously resolved snapshot without touching the
// environment or filesystem. This is the production command-root seam.
func (pf *ProviderFlags) ApplyResolved(cfg *app.Config, keys ResolvedCredentials) {
	pf.applyResolvedAPIKeys(cfg, keys)
	cfg.OpenAICodexCredential = keys.OpenAICodex
}

// ApplyResolvedAPIKeys is the explicit projection for a command root which
// does not support the manual Codex credential (currently mecak8s).
func (pf *ProviderFlags) ApplyResolvedAPIKeys(cfg *app.Config, keys ResolvedCredentials) {
	pf.applyResolvedAPIKeys(cfg, keys)
}

func (*ProviderFlags) applyResolvedAPIKeys(cfg *app.Config, keys ResolvedCredentials) {
	cfg.OpenAIKey = keys.OpenAI
	cfg.OpenRouterKey = keys.OpenRouter
	cfg.AnthropicKey = keys.Anthropic
	cfg.OpenCodeKey = keys.OpenCode
	cfg.TypesafeAPIKey = keys.Typesafe
}

// EndpointOverrides returns the non-secret CLI endpoint overrides. Command roots
// map this directly onto app.Config; Build merges it over settings-derived overrides.
func (pf *ProviderFlags) EndpointOverrides() permconfig.ProviderOverrides {
	if pf == nil {
		return nil
	}
	overrides := permconfig.ProviderOverrides{}
	for id, baseURL := range map[string]string{
		"openai": value(pf.openAIBaseURL), "openrouter": value(pf.openRouterBaseURL),
		"anthropic": value(pf.anthropicBaseURL), "opencode": value(pf.openCodeBaseURL),
	} {
		if baseURL != "" {
			overrides[id] = permconfig.ProviderOverride{BaseURL: baseURL}
		}
	}
	return overrides
}

// ReadProviderKeys reads provider credentials from the environment alone
// (no auth.yaml). It is the SINGLE definition of which env vars hold which credential.
// A caller that must account for auth.yaml should call ProviderFlags.Resolve instead.
// The values are SECRET-shaped; callers must not log or print them.
func ReadProviderKeys() ResolvedKeys {
	return readProviderKeys(os.Getenv)
}

func readProviderKeys(getenv func(string) string) ResolvedCredentials {
	return ResolvedCredentials{
		OpenAI:     getenv(envOpenAIKey),
		OpenRouter: getenv(envOpenRouterKey),
		Anthropic:  getenv(envAnthropicKey),
		OpenCode:   getenv(envOpenCodeKey),
		Typesafe:   getenv(envTypesafeKey),
	}
}

// ResolvedCredentials is the immutable-by-value snapshot resolved from the
// environment and auth.yaml. The four API-key fields are SECRET-shaped: callers
// must not log or print them.
type ResolvedCredentials struct {
	OpenAI     string
	OpenRouter string
	Anthropic  string
	OpenCode   string
	Typesafe   string
	// OpenAICodex is a distinct billing identity from OpenAIKey. Its fields are
	// immutable outside the provider adjunct and it is populated only after
	// startup validation of a file-backed manual token.
	OpenAICodex openaicodex.Credential
	// customAPIKeys and customMethods are private so generic config formatting
	// cannot accidentally project custom credentials.
	customAPIKeys map[string]string
	customMethods map[string]string
	// AuthFileWarning is non-empty when the auth.yaml credentials file (the explicit
	// --api-key-file path, or the conventional default) could not be read or parsed
	// cleanly. It is set by Resolve/Apply (ReadProviderKeys alone never touches the file).
	// Never fatal — Apply always falls back to whatever was resolved from the
	// environment — but a caller should log it (cmd/ mains: slog.Warn) so a typo in
	// auth.yaml doesn't fail silently. Not secret-shaped: it names the file path and the
	// problem, never a key value.
	AuthFileWarning string
}

// ResolvedKeys remains as the source-compatible name for callers that only
// used the original API-key snapshot.
type ResolvedKeys = ResolvedCredentials

// HasOpenAICodex reports whether validation produced a usable manual token.
func (k ResolvedCredentials) HasOpenAICodex() bool { return k.OpenAICodex.Configured() }

// CustomAPIKey returns the file-only key for one custom provider.
func (k ResolvedCredentials) CustomAPIKey(id string) string { return k.customAPIKeys[id] }

// CustomAvailable reports whether the custom provider has the authentication its
// validated definition requires.
func (k ResolvedCredentials) CustomAvailable(id string) bool {
	return k.customMethods[id] == "none" || (k.customMethods[id] == "api_key" && k.customAPIKeys[id] != "")
}

// Any reports whether at least one provider credential is present. It is the shared
// "is any real provider configured?" predicate (mecatui uses it for its startup guard).
func (k ResolvedCredentials) Any() bool {
	return k.OpenAI != "" || k.OpenRouter != "" || k.Anthropic != "" || k.OpenCode != "" || k.HasOpenAICodex()
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

func joinCredentialWarnings(first, second string) string {
	if first == "" {
		return second
	}
	if second == "" {
		return first
	}
	return first + "; " + second
}

func (h ProviderFlagHelp) withDefaults() ProviderFlagHelp {
	if h.OpenAIBaseURL == "" {
		h.OpenAIBaseURL = DefaultProviderFlagHelp.OpenAIBaseURL
	}
	if h.OpenRouterBaseURL == "" {
		h.OpenRouterBaseURL = DefaultProviderFlagHelp.OpenRouterBaseURL
	}
	if h.AnthropicBaseURL == "" {
		h.AnthropicBaseURL = DefaultProviderFlagHelp.AnthropicBaseURL
	}
	if h.OpenCodeBaseURL == "" {
		h.OpenCodeBaseURL = DefaultProviderFlagHelp.OpenCodeBaseURL
	}
	if h.AuthFile == "" {
		h.AuthFile = DefaultProviderFlagHelp.AuthFile
	}
	return h
}

// ToolhiveLLMFlagHelp carries the per-main help text for the two ToolHive LLM
// gateway flags (issue #262). mecatui prefixes "embedded server only:" (it
// only matters when mecatui hosts its OWN in-process server); the other three
// mains use DefaultToolhiveLLMFlagHelp verbatim.
type ToolhiveLLMFlagHelp struct {
	Enable  string
	BaseURL string
	// Mode is the per-main help for --toolhive-llm-mode (issue #265).
	Mode string
}

// DefaultToolhiveLLMFlagHelp is the shared wording every consumer starts from.
// It explicitly disambiguates from the UNRELATED --toolhive flag (ToolHive MCP
// workload discovery) — the two features share a vendor name and nothing
// else, and issue #262's own review flagged the naming collision as the #1
// confusion risk.
var DefaultToolhiveLLMFlagHelp = ToolhiveLLMFlagHelp{
	Enable: "auto-detect a locally-running ToolHive LLM proxy by reading ToolHive's config and probing " +
		"127.0.0.1, and register it as a model provider (id \"toolhive\", no API key needed); unrelated to " +
		"--toolhive (MCP workload discovery). Set =false on shared hosts",
	BaseURL: "explicit ToolHive LLM proxy base URL (must resolve to loopback); skips the config-file " +
		"auto-detect but keeps the startup probe",
	Mode: "ToolHive LLM routing mode: \"auto\" (default; direct when the OIDC trio is configured, else the " +
		"loopback proxy), \"proxy\" (force the loopback reverse proxy), or \"direct\" (talk to the real " +
		"gateway_url with an in-process OIDC token; fails when OIDC is not configured). The explicit " +
		"--toolhive-llm-base-url override is always proxy mode",
}

// ToolhiveLLMFlags holds the values bound by RegisterToolhiveLLMFlags.
type ToolhiveLLMFlags struct {
	enable  *bool
	baseURL *string
	mode    *string
}

// RegisterToolhiveLLMFlags registers --toolhive-llm (default true),
// --toolhive-llm-base-url (default ""), and --toolhive-llm-mode (default
// "auto") on fs. A zero ToolhiveLLMFlagHelp field falls back to
// DefaultToolhiveLLMFlagHelp, mirroring RegisterProviderFlags.
func RegisterToolhiveLLMFlags(fs *flag.FlagSet, help ToolhiveLLMFlagHelp) *ToolhiveLLMFlags {
	if help.Enable == "" {
		help.Enable = DefaultToolhiveLLMFlagHelp.Enable
	}
	if help.BaseURL == "" {
		help.BaseURL = DefaultToolhiveLLMFlagHelp.BaseURL
	}
	if help.Mode == "" {
		help.Mode = DefaultToolhiveLLMFlagHelp.Mode
	}
	tf := &ToolhiveLLMFlags{enable: new(bool), baseURL: new(string), mode: new(string)}
	fs.BoolVar(tf.enable, "toolhive-llm", true, help.Enable)
	fs.StringVar(tf.baseURL, "toolhive-llm-base-url", "", help.BaseURL)
	fs.StringVar(tf.mode, "toolhive-llm-mode", "auto", help.Mode)
	return tf
}

// Apply writes the three resolved values onto cfg. A nil receiver (a config
// built WITHOUT RegisterToolhiveLLMFlags — e.g. a test that constructs the
// cmd config struct directly) leaves the app.Config fields at their zero
// value (ToolhiveLLM=false, ToolhiveLLMBaseURL="", ToolhiveLLMMode=""), so
// app.Config's byte-identical-when-unset invariant holds for a caller that
// never wires this flag set (ToolhiveLLMMode="" resolves to "auto" in
// resolveToolhiveIntent, the pre-#265 default).
func (tf *ToolhiveLLMFlags) Apply(cfg *app.Config) {
	if tf == nil {
		return
	}
	cfg.ToolhiveLLM = *tf.enable
	cfg.ToolhiveLLMBaseURL = *tf.baseURL
	cfg.ToolhiveLLMMode = *tf.mode
}

// KeyValueList is a repeatable "key=value" flag.Value collecting into a
// last-write-wins map. It backs --model-alias (e.g.
// --model-alias fast=gpt-4o-mini --model-alias smart=gpt-5) and --model-slot
// (e.g. --model-slot compaction=cheap) in both cmd/mecated and cmd/mecatui.
// Extracted here (issue #93) so the two mains cannot drift apart — the twin
// copies already had divergent Set error messages in #87.
//
// A nil *KeyValueList is usable: Set lazily allocates the backing map, exactly
// as the pre-extraction inline types did, so a config struct field's zero value
// (a nil map) is a valid flag binding.
type KeyValueList map[string]string

// String implements flag.Value. It renders the map as a sorted, comma-separated
// list of key=value pairs (empty for a nil/empty map), the stable form flag's
// default-value display expects.
func (m KeyValueList) String() string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Set implements flag.Value. A later occurrence of the same key overrides an
// earlier one. A value without '=' (or with an empty key) is a parse error.
func (m *KeyValueList) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	k = strings.TrimSpace(k)
	if !ok || k == "" {
		return fmt.Errorf("must be key=value, got %q", v)
	}
	if *m == nil {
		*m = KeyValueList{}
	}
	(*m)[k] = strings.TrimSpace(val)
	return nil
}

// AsMap returns the backing map as a plain map[string]string (the type
// app.Config.ModelAliases / ModelSlots expects), or nil for a nil receiver so
// an unset flag yields the byte-identical default (a nil map, not an empty
// one). It is the ONE conversion a cmd main does to thread a parsed KeyValueList
// onto app.Config.
func (m *KeyValueList) AsMap() map[string]string {
	if m == nil {
		return nil
	}
	return map[string]string(*m)
}

// ModelFlagHelp carries the per-main help text for the two repeatable model
// flags --model-alias and --model-slot. The two mains word these slightly
// differently (mecatui prefixes "embedded server only:"), so the help is passed
// in rather than hard-coded — keeping each main's --help BYTE-IDENTICAL across
// the extraction. A zero ModelFlagHelp falls back to DefaultModelFlagHelp (the
// mecated wording), which is what a new consumer (mecatequi) would use.
type ModelFlagHelp struct {
	ModelAlias string
	ModelSlot  string
}

// DefaultModelFlagHelp is the mecated-style wording, used when a field of the
// passed ModelFlagHelp is empty. It mirrors the help text the mecated twin
// carried before the extraction.
var DefaultModelFlagHelp = ModelFlagHelp{
	ModelAlias: "model alias mapping as name=model-id. Repeatable; for example, --model-alias fast=gpt-4o-mini. Agent definitions can use these aliases in their `model` field.",
	ModelSlot:  "model binding as slot=selector. Repeatable; for example, --model-slot compaction=cheap. Slots `compaction`, `ask-reviewer`, and `guardrail` select models for those operations. The selector is a --model-alias or model identifier. An invalid selector uses the session model. `ask-reviewer` and `guardrail` slots do not enable those features.",
}

// RegisterModelFlags registers --model-alias / --model-slot on fs, each bound
// to its own *KeyValueList, and returns the pair so the caller can thread them
// onto app.Config (ModelAliases / ModelSlots). The help text comes from help,
// falling back per-field to DefaultModelFlagHelp so a caller may pass a zero
// value (or override only the fields it words differently). It is the ONE place
// the two repeatable model flags are wired, so the two mains (and a future
// mecatequi consumer) cannot drift apart.
func RegisterModelFlags(fs *flag.FlagSet, help ModelFlagHelp) (aliases, slots *KeyValueList) {
	help = help.withModelDefaults()
	aliases = new(KeyValueList)
	slots = new(KeyValueList)
	fs.Var(aliases, "model-alias", help.ModelAlias)
	fs.Var(slots, "model-slot", help.ModelSlot)
	return aliases, slots
}

func (h ModelFlagHelp) withModelDefaults() ModelFlagHelp {
	if h.ModelAlias == "" {
		h.ModelAlias = DefaultModelFlagHelp.ModelAlias
	}
	if h.ModelSlot == "" {
		h.ModelSlot = DefaultModelFlagHelp.ModelSlot
	}
	return h
}
